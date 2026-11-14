// Package peerclient pulls blocks from a Fabric peer's deliver service over
// mutual TLS. It signs its seek requests with a client identity, so it works
// against peers that set CORE_PEER_TLS_CLIENTAUTHREQUIRED=true. It sends no
// transaction.
package peerclient

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/asn1"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/golang/protobuf/proto" //nolint:staticcheck // fabric-protos-go v0.3 is generated for the v1 API
	cb "github.com/hyperledger/fabric-protos-go/common"
	"github.com/hyperledger/fabric-protos-go/msp"
	ab "github.com/hyperledger/fabric-protos-go/orderer"
	fabricpeer "github.com/hyperledger/fabric-protos-go/peer"

	"github.com/luthersystems/blockinspector/internal/explorer"
	"github.com/luthersystems/blockinspector/internal/protoutil"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Config names the peer and the client identity.
type Config struct {
	// Address is the peer's host:port.
	Address string
	// ServerName is the name in the peer's TLS certificate, when it differs
	// from Address's host (a local network's port published on 127.0.0.1).
	ServerName string
	// TLSRootCAFile verifies the peer's TLS certificate.
	TLSRootCAFile string
	// TLSCertFile and TLSKeyFile are the client's TLS certificate and key.
	TLSCertFile string
	TLSKeyFile  string
	// MSPID, SignCertFile and SignKeyFile are the identity that signs the
	// seek requests. It must satisfy the channel's Readers policy.
	MSPID        string
	SignCertFile string
	SignKeyFile  string
	// Channel is the channel to read.
	Channel string
}

// Client pulls blocks from one peer.
type Client struct {
	signer      *signer
	tlsConfig   *tls.Config
	tlsCertHash []byte
	cfg         Config
}

// New loads the TLS material and the signing identity.
func New(cfg Config) (*Client, error) {
	if cfg.Address == "" || cfg.Channel == "" || cfg.MSPID == "" {
		return nil, errors.New("peer address, channel and MSP ID are required")
	}
	caPEM, err := os.ReadFile(cfg.TLSRootCAFile)
	if err != nil {
		return nil, fmt.Errorf("read TLS root CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("no certificate in TLS root CA file %s", cfg.TLSRootCAFile)
	}
	pair, err := tls.LoadX509KeyPair(cfg.TLSCertFile, cfg.TLSKeyFile)
	if err != nil {
		return nil, fmt.Errorf("load client TLS key pair: %w", err)
	}
	// The peer's binding inspector requires the hash of the client's TLS
	// certificate in the channel header when mutual TLS is on.
	sum := sha256.Sum256(pair.Certificate[0])
	s, err := loadSigner(cfg.MSPID, cfg.SignCertFile, cfg.SignKeyFile)
	if err != nil {
		return nil, err
	}
	return &Client{
		cfg:    cfg,
		signer: s,
		tlsConfig: &tls.Config{
			RootCAs:      pool,
			Certificates: []tls.Certificate{pair},
			ServerName:   cfg.ServerName,
			MinVersion:   tls.VersionTLS12,
		},
		tlsCertHash: sum[:],
	}, nil
}

// Address returns the peer's address.
func (c *Client) Address() string { return c.cfg.Address }

// Dial opens a connection to the peer.
func (c *Client) Dial() (*grpc.ClientConn, error) {
	return grpc.NewClient(c.cfg.Address,
		grpc.WithTransportCredentials(credentials.NewTLS(c.tlsConfig)),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{Time: time.Minute, Timeout: 20 * time.Second}),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(256<<20)),
	)
}

// Deliver streams blocks from number start (or the oldest block the peer
// has, when oldest is true) and calls fn with each, until ctx ends, fn
// returns an error, or the stream fails. It waits for new blocks at the tip.
// It calls onOpen, when set, once the peer has accepted the request: at the
// first block, or when the stream is still open OpenGrace after the
// request (a peer at the tip sends nothing). A refused request (a wrong
// channel, an identity that is not a reader) ends at once with a status, so
// it never counts as open.
func (c *Client) Deliver(ctx context.Context, conn *grpc.ClientConn, start uint64, oldest bool, onOpen func(), fn func(*cb.Block) error) error {
	env, err := c.seekEnvelope(start, oldest)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream, err := fabricpeer.NewDeliverClient(conn).Deliver(ctx)
	if err != nil {
		return fmt.Errorf("open deliver stream: %w", err)
	}
	if err := stream.Send(env); err != nil {
		return fmt.Errorf("send seek request: %w", err)
	}
	if err := stream.CloseSend(); err != nil {
		return fmt.Errorf("close send: %w", err)
	}
	var (
		mu     sync.Mutex
		ended  bool
		opened bool
	)
	open := func() {
		mu.Lock()
		defer mu.Unlock()
		if ended || opened || onOpen == nil {
			return
		}
		opened = true
		onOpen()
	}
	timer := time.AfterFunc(OpenGrace, open)
	defer func() {
		timer.Stop()
		mu.Lock()
		ended = true
		mu.Unlock()
	}()
	for {
		resp, err := stream.Recv()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err == io.EOF {
			return errors.New("peer closed the deliver stream")
		}
		if err != nil {
			return fmt.Errorf("receive: %w", err)
		}
		switch t := resp.GetType().(type) {
		case *fabricpeer.DeliverResponse_Block:
			open()
			if err := fn(t.Block); err != nil {
				return err
			}
		case *fabricpeer.DeliverResponse_Status:
			return fmt.Errorf("peer ended the deliver stream: %s", t.Status)
		}
	}
}

// OpenGrace is how long a deliver stream must stay open with no block
// before it counts as accepted.
var OpenGrace = 2 * time.Second

// seekEnvelope builds the signed SeekInfo request.
func (c *Client) seekEnvelope(start uint64, oldest bool) (*cb.Envelope, error) {
	startPos := &ab.SeekPosition{Type: &ab.SeekPosition_Specified{Specified: &ab.SeekSpecified{Number: start}}}
	if oldest {
		startPos = &ab.SeekPosition{Type: &ab.SeekPosition_Oldest{Oldest: &ab.SeekOldest{}}}
	}
	seek := &ab.SeekInfo{
		Start:    startPos,
		Stop:     &ab.SeekPosition{Type: &ab.SeekPosition_Specified{Specified: &ab.SeekSpecified{Number: math.MaxUint64}}},
		Behavior: ab.SeekInfo_BLOCK_UNTIL_READY,
	}
	seekBytes, err := proto.Marshal(seek)
	if err != nil {
		return nil, err
	}
	nonce, err := protoutil.CreateNonce()
	if err != nil {
		return nil, err
	}
	creator := c.signer.serialized
	chdr, err := proto.Marshal(&cb.ChannelHeader{
		Type:        int32(cb.HeaderType_DELIVER_SEEK_INFO),
		ChannelId:   c.cfg.Channel,
		TxId:        protoutil.ComputeTxID(nonce, creator),
		Timestamp:   timestamppb.New(time.Now()),
		TlsCertHash: c.tlsCertHash,
	})
	if err != nil {
		return nil, err
	}
	shdr, err := proto.Marshal(protoutil.MakeSignatureHeader(creator, nonce))
	if err != nil {
		return nil, err
	}
	payload, err := proto.Marshal(&cb.Payload{
		Header: &cb.Header{ChannelHeader: chdr, SignatureHeader: shdr},
		Data:   seekBytes,
	})
	if err != nil {
		return nil, err
	}
	sig, err := c.signer.sign(payload)
	if err != nil {
		return nil, err
	}
	return &cb.Envelope{Payload: payload, Signature: sig}, nil
}

// signer signs with an ECDSA key, as Fabric's MSP does.
type signer struct {
	key        *ecdsa.PrivateKey
	serialized []byte
}

func loadSigner(mspID, certFile, keyFile string) (*signer, error) {
	certPEM, err := os.ReadFile(certFile) //nolint:gosec // G304: the operator names the identity files
	if err != nil {
		return nil, fmt.Errorf("read signing certificate: %w", err)
	}
	if b, _ := pem.Decode(certPEM); b == nil {
		return nil, fmt.Errorf("no PEM certificate in %s", certFile)
	}
	keyPEM, err := os.ReadFile(keyFile) //nolint:gosec // G304: the operator names the identity files
	if err != nil {
		return nil, fmt.Errorf("read signing key: %w", err)
	}
	key, err := parseECKey(keyPEM)
	if err != nil {
		return nil, fmt.Errorf("signing key %s: %w", keyFile, err)
	}
	serialized, err := proto.Marshal(&msp.SerializedIdentity{Mspid: mspID, IdBytes: certPEM})
	if err != nil {
		return nil, err
	}
	return &signer{key: key, serialized: serialized}, nil
}

func parseECKey(keyPEM []byte) (*ecdsa.PrivateKey, error) {
	b, _ := pem.Decode(keyPEM)
	if b == nil {
		return nil, errors.New("no PEM key")
	}
	if k, err := x509.ParsePKCS8PrivateKey(b.Bytes); err == nil {
		ec, ok := k.(*ecdsa.PrivateKey)
		if !ok {
			return nil, errors.New("key is not ECDSA")
		}
		return ec, nil
	}
	return x509.ParseECPrivateKey(b.Bytes)
}

// sign returns the DER signature of SHA-256(msg), with a low S, which Fabric
// requires.
func (s *signer) sign(msg []byte) ([]byte, error) {
	digest := sha256.Sum256(msg)
	r, sv, err := ecdsa.Sign(rand.Reader, s.key, digest[:])
	if err != nil {
		return nil, err
	}
	halfOrder := new(big.Int).Rsh(s.key.Params().N, 1)
	if sv.Cmp(halfOrder) > 0 {
		sv = new(big.Int).Sub(s.key.Params().N, sv)
	}
	return asn1.Marshal(struct{ R, S *big.Int }{r, sv})
}

// FirstFile returns the one file in dir, for an MSP's signcerts and keystore
// directories.
func FirstFile(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		if !e.IsDir() {
			return filepath.Join(dir, e.Name()), nil
		}
	}
	return "", fmt.Errorf("no file in %s", dir)
}

// Source adapts a Client to a stream that dials the peer for each call.
type Source struct {
	*Client
}

// Name describes the source.
func (s Source) Name() string { return "peer:" + s.cfg.Address }

// Live reports true: the peer's deliver service waits for new blocks.
func (s Source) Live() bool { return true }

// Stream dials the peer and streams blocks until ctx ends or the stream
// fails.
func (s Source) Stream(ctx context.Context, req explorer.StreamRequest, fn func(*cb.Block) error) error {
	conn, err := s.Dial()
	if err != nil {
		return fmt.Errorf("dial %s: %w", s.cfg.Address, err)
	}
	defer conn.Close() //nolint:errcheck // nothing to do on a close error
	return s.Deliver(ctx, conn, req.Next, req.FromStart, req.OnOpen, fn)
}
