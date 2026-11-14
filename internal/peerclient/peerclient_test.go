package peerclient

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/golang/protobuf/proto" //nolint:staticcheck // fabric-protos-go v0.3 is generated for the v1 API
	cb "github.com/hyperledger/fabric-protos-go/common"
	"github.com/hyperledger/fabric-protos-go/msp"
	ab "github.com/hyperledger/fabric-protos-go/orderer"
	fabricpeer "github.com/hyperledger/fabric-protos-go/peer"
	"github.com/luthersystems/blockinspector/internal/explorer"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	grpcpeer "google.golang.org/grpc/peer"
)

type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pem  []byte
}

func newCA(t *testing.T, name string) *testCA {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: name},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	return &testCA{cert: cert, key: key, pem: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
}

// issue returns a certificate and a PKCS#8 key, both PEM.
func (ca *testCA) issue(t *testing.T, cn string, usage []x509.ExtKeyUsage, dns ...string) (certPEM, keyPEM []byte) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  usage,
		DNSNames:     dns,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	require.NoError(t, err)
	k, err := x509.MarshalPKCS8PrivateKey(key)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: k})
}

func writeFile(t *testing.T, path string, data []byte) {
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o750))
	require.NoError(t, os.WriteFile(path, data, 0o600))
}

// network writes the files fabric-network-builder generates for org1's
// User1, and returns the network directory.
func network(t *testing.T, tlsCA, signCA *testCA) string {
	dir := t.TempDir()
	// A second org, so the test shows the first one (sorted) is picked.
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "crypto-config/peerOrganizations/org2.example.com"), 0o750))
	user := filepath.Join(dir, "crypto-config/peerOrganizations/org1.example.com/users/User1@org1.example.com")
	cert, key := tlsCA.issue(t, "User1@org1.example.com", []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth})
	writeFile(t, filepath.Join(user, "tls/ca.crt"), tlsCA.pem)
	writeFile(t, filepath.Join(user, "tls/client.crt"), cert)
	writeFile(t, filepath.Join(user, "tls/client.key"), key)
	cert, key = signCA.issue(t, "User1@org1.example.com", nil)
	writeFile(t, filepath.Join(user, "msp/signcerts/User1@org1.example.com-cert.pem"), cert)
	writeFile(t, filepath.Join(user, "msp/keystore/priv_sk"), key)
	return dir
}

// fakePeer is a deliver service that checks each seek request as a Fabric
// peer with mutual TLS does, then sends blocks.
type fakePeer struct {
	fabricpeer.UnimplementedDeliverServer
	t       *testing.T
	signCA  *testCA
	blocks  []*cb.Block
	seeks   chan *ab.SeekInfo
	channel string
}

func (p *fakePeer) Deliver(stream fabricpeer.Deliver_DeliverServer) error {
	env, err := stream.Recv()
	if err != nil {
		return err
	}
	if err := p.check(stream.Context(), env); err != nil {
		return stream.Send(&fabricpeer.DeliverResponse{Type: &fabricpeer.DeliverResponse_Status{Status: cb.Status_FORBIDDEN}})
	}
	payload := &cb.Payload{}
	require.NoError(p.t, proto.Unmarshal(env.Payload, payload))
	seek := &ab.SeekInfo{}
	require.NoError(p.t, proto.Unmarshal(payload.Data, seek))
	p.seeks <- seek
	start := uint64(0)
	if s := seek.GetStart().GetSpecified(); s != nil {
		start = s.Number
	}
	for _, b := range p.blocks {
		if b.Header.Number < start {
			continue
		}
		if err := stream.Send(&fabricpeer.DeliverResponse{Type: &fabricpeer.DeliverResponse_Block{Block: b}}); err != nil {
			return err
		}
	}
	<-stream.Context().Done()
	return nil
}

// check verifies the TLS binding, the creator and the signature.
func (p *fakePeer) check(ctx context.Context, env *cb.Envelope) error {
	t := p.t
	pr, ok := grpcpeer.FromContext(ctx)
	require.True(t, ok)
	tlsInfo := pr.AuthInfo.(credentials.TLSInfo)
	require.NotEmpty(t, tlsInfo.State.PeerCertificates, "no client certificate")
	clientHash := sha256.Sum256(tlsInfo.State.PeerCertificates[0].Raw)

	payload := &cb.Payload{}
	require.NoError(t, proto.Unmarshal(env.Payload, payload))
	chdr := &cb.ChannelHeader{}
	require.NoError(t, proto.Unmarshal(payload.Header.ChannelHeader, chdr))
	assert.Equal(t, int32(cb.HeaderType_DELIVER_SEEK_INFO), chdr.Type)
	assert.Equal(t, p.channel, chdr.ChannelId)
	assert.Equal(t, clientHash[:], chdr.TlsCertHash, "TLS binding")
	assert.WithinDuration(t, time.Now(), chdr.Timestamp.AsTime(), time.Minute)

	shdr := &cb.SignatureHeader{}
	require.NoError(t, proto.Unmarshal(payload.Header.SignatureHeader, shdr))
	assert.Len(t, shdr.Nonce, 24)
	creator := &msp.SerializedIdentity{}
	require.NoError(t, proto.Unmarshal(shdr.Creator, creator))
	assert.Equal(t, "Org1MSP", creator.Mspid)
	txID := sha256.Sum256(append(append([]byte{}, shdr.Nonce...), shdr.Creator...))
	assert.Equal(t, txID[:], mustHex(t, chdr.TxId))

	block, _ := pem.Decode(creator.IdBytes)
	require.NotNil(t, block)
	cert, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)
	require.NoError(t, cert.CheckSignatureFrom(p.signCA.cert))
	var sig struct{ R, S *big.Int }
	_, err = asn1.Unmarshal(env.Signature, &sig)
	require.NoError(t, err)
	pub := cert.PublicKey.(*ecdsa.PublicKey)
	half := new(big.Int).Rsh(pub.Params().N, 1)
	assert.True(t, sig.S.Cmp(half) <= 0, "high S")
	digest := sha256.Sum256(env.Payload)
	assert.True(t, ecdsa.Verify(pub, digest[:], sig.R, sig.S), "signature")
	return nil
}

func mustHex(t *testing.T, s string) []byte {
	b := make([]byte, len(s)/2)
	for i := range b {
		var v byte
		for _, c := range s[2*i : 2*i+2] {
			v <<= 4
			switch {
			case c >= '0' && c <= '9':
				v |= byte(c - '0')
			case c >= 'a' && c <= 'f':
				v |= byte(c-'a') + 10
			default:
				t.Fatalf("not hex: %q", s)
			}
		}
		b[i] = v
	}
	return b
}

// startPeer serves fakePeer with mutual TLS: it requires a client
// certificate from tlsCA.
func startPeer(t *testing.T, tlsCA *testCA, p *fakePeer) string {
	certPEM, keyPEM := tlsCA.issue(t, "peer0.org1.example.com", []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, "peer0.org1.example.com")
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	require.NoError(t, err)
	pool := x509.NewCertPool()
	pool.AddCert(tlsCA.cert)
	srv := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{pair},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    pool,
		MinVersion:   tls.VersionTLS12,
	})))
	fabricpeer.RegisterDeliverServer(srv, p)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(srv.Stop)
	return ln.Addr().String()
}

func testBlocks(n int) []*cb.Block {
	var out []*cb.Block
	for i := 0; i < n; i++ {
		out = append(out, &cb.Block{Header: &cb.BlockHeader{Number: uint64(i)}, Data: &cb.BlockData{}})
	}
	return out
}

func TestDeliverMutualTLS(t *testing.T) {
	tlsCA, signCA := newCA(t, "tlsca.org1.example.com"), newCA(t, "ca.org1.example.com")
	p := &fakePeer{t: t, signCA: signCA, blocks: testBlocks(5), seeks: make(chan *ab.SeekInfo, 2), channel: "luther"}
	addr := startPeer(t, tlsCA, p)

	cfg, err := NetworkConfig(NetworkOptions{Dir: network(t, tlsCA, signCA), Peer: "peer0", User: "User1", Address: addr, Channel: "luther"})
	require.NoError(t, err)
	assert.Equal(t, "Org1MSP", cfg.MSPID)
	assert.Equal(t, "peer0.org1.example.com", cfg.ServerName)
	client, err := New(cfg)
	require.NoError(t, err)
	src := Source{Client: client}
	assert.True(t, src.Live())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var got []uint64
	opened := false
	err = src.Stream(ctx, explorer.StreamRequest{FromStart: true, OnOpen: func() { opened = true }}, func(b *cb.Block) error {
		got = append(got, b.Header.Number)
		if len(got) == 3 {
			cancel()
		}
		return nil
	})
	require.Error(t, err)
	assert.True(t, opened)
	assert.Equal(t, []uint64{0, 1, 2}, got)
	seek := <-p.seeks
	assert.NotNil(t, seek.GetStart().GetOldest())
	assert.Equal(t, ab.SeekInfo_BLOCK_UNTIL_READY, seek.Behavior)

	// Resume from block 3.
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	got = nil
	_ = src.Stream(ctx, explorer.StreamRequest{Next: 3}, func(b *cb.Block) error {
		got = append(got, b.Header.Number)
		if len(got) == 2 {
			cancel()
		}
		return nil
	})
	assert.Equal(t, []uint64{3, 4}, got)
	assert.Equal(t, uint64(3), (<-p.seeks).GetStart().GetSpecified().GetNumber())
}

// TestDeliverUntrustedClient: a peer that requires client TLS refuses a
// client certificate from another CA, as the old explorer was refused.
func TestDeliverUntrustedClient(t *testing.T) {
	tlsCA, signCA := newCA(t, "tlsca.org1.example.com"), newCA(t, "ca.org1.example.com")
	p := &fakePeer{t: t, signCA: signCA, seeks: make(chan *ab.SeekInfo, 1), channel: "luther"}
	addr := startPeer(t, tlsCA, p)

	other := newCA(t, "tlsca.other")
	dir := network(t, other, signCA)
	cfg, err := NetworkConfig(NetworkOptions{Dir: dir, Peer: "peer0", User: "User1", Address: addr, Channel: "luther"})
	require.NoError(t, err)
	// Trust the real peer, but present the other CA's client certificate.
	caFile := filepath.Join(t.TempDir(), "ca.crt")
	writeFile(t, caFile, tlsCA.pem)
	cfg.TLSRootCAFile = caFile
	client, err := New(cfg)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err = Source{Client: client}.Stream(ctx, explorer.StreamRequest{FromStart: true}, func(*cb.Block) error { return nil })
	require.Error(t, err)
	assert.NoError(t, ctx.Err(), "the stream should fail, not time out")
}

func TestNetworkConfigErrors(t *testing.T) {
	_, err := NetworkConfig(NetworkOptions{Dir: t.TempDir(), User: "User1"})
	assert.ErrorContains(t, err, "fabric-network-builder generate")

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "crypto-config/peerOrganizations/org1.example.com"), 0o750))
	_, err = NetworkConfig(NetworkOptions{Dir: dir, User: "User1"})
	assert.ErrorContains(t, err, "signing certificate")
	_, err = NetworkConfig(NetworkOptions{Dir: dir, Org: "org3", User: "User1"})
	assert.ErrorContains(t, err, "no org org3")

	tlsCA, signCA := newCA(t, "tlsca"), newCA(t, "ca")
	cfg, err := NetworkConfig(NetworkOptions{Dir: network(t, tlsCA, signCA), Org: "org2", Peer: "peer1", User: "User1"})
	assert.ErrorContains(t, err, "signing certificate", "org2 has no users in the test network")
	assert.Empty(t, cfg.MSPID)

	_, err = New(Config{})
	assert.Error(t, err)
	assert.Equal(t, "Org2MSP", mspID("org2"))
}

// TestDeliverOpenAtTip: a peer at the tip sends no block, so the stream
// counts as open once it has stayed open for OpenGrace.
func TestDeliverOpenAtTip(t *testing.T) {
	old := OpenGrace
	OpenGrace = 50 * time.Millisecond
	t.Cleanup(func() { OpenGrace = old })
	tlsCA, signCA := newCA(t, "tlsca.org1.example.com"), newCA(t, "ca.org1.example.com")
	p := &fakePeer{t: t, signCA: signCA, seeks: make(chan *ab.SeekInfo, 1), channel: "luther"}
	addr := startPeer(t, tlsCA, p)
	cfg, err := NetworkConfig(NetworkOptions{Dir: network(t, tlsCA, signCA), Peer: "peer0", User: "User1", Address: addr, Channel: "luther"})
	require.NoError(t, err)
	client, err := New(cfg)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	opened := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- Source{Client: client}.Stream(ctx, explorer.StreamRequest{Next: 7, OnOpen: func() { close(opened) }}, func(*cb.Block) error { return nil })
	}()
	select {
	case <-opened:
	case <-time.After(5 * time.Second):
		t.Fatal("stream at the tip never counted as open")
	}
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
}
