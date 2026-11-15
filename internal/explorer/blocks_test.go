package explorer

import (
	"crypto/sha256"
	"fmt"
	"testing"
	"time"

	"github.com/golang/protobuf/proto" //nolint:staticcheck // fabric-protos-go v0.3 is generated for the v1 API
	cb "github.com/hyperledger/fabric-protos-go/common"
	"github.com/hyperledger/fabric-protos-go/ledger/rwset"
	"github.com/hyperledger/fabric-protos-go/ledger/rwset/kvrwset"
	"github.com/hyperledger/fabric-protos-go/msp"
	fabricpeer "github.com/hyperledger/fabric-protos-go/peer"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// testTx describes an endorser transaction for testBlock.
type testTx struct {
	id      string
	request string // the JSON-RPC request; empty for none
	writes  []*kvrwset.KVWrite
	reads   []*kvrwset.KVRead
	private []*rwset.CollectionHashedReadWriteSet
	code    fabricpeer.TxValidationCode
}

const testNamespace = "substrate"

var testTime = time.Date(2026, 11, 14, 5, 0, 0, 0, time.UTC)

func mustMarshal(t *testing.T, m proto.Message) []byte {
	t.Helper()
	b, err := proto.Marshal(m)
	require.NoError(t, err)
	return b
}

func identity(t *testing.T, mspID string) []byte {
	return mustMarshal(t, &msp.SerializedIdentity{Mspid: mspID, IdBytes: []byte("not a certificate")})
}

// testEnvelope builds an endorser transaction envelope.
func testEnvelope(t *testing.T, num uint64, tx testTx) []byte {
	t.Helper()
	kv := mustMarshal(t, &kvrwset.KVRWSet{Reads: tx.reads, Writes: tx.writes})
	results := mustMarshal(t, &rwset.TxReadWriteSet{
		DataModel: rwset.TxReadWriteSet_KV,
		NsRwset: []*rwset.NsReadWriteSet{{
			Namespace:             testNamespace,
			Rwset:                 kv,
			CollectionHashedRwset: tx.private,
		}},
	})
	action := mustMarshal(t, &fabricpeer.ChaincodeAction{
		Results:     results,
		Response:    &fabricpeer.Response{Status: 200, Payload: []byte(`{"ok":true}`)},
		ChaincodeId: &fabricpeer.ChaincodeID{Name: testNamespace},
		Events:      mustMarshal(t, &fabricpeer.ChaincodeEvent{ChaincodeId: testNamespace, TxId: tx.id, EventName: "luther", Payload: []byte(`{}`)}),
	})
	prp := mustMarshal(t, &fabricpeer.ProposalResponsePayload{ProposalHash: []byte("h"), Extension: action})
	var args [][]byte
	if tx.request != "" {
		args = [][]byte{[]byte("Invoke"), []byte(tx.request)}
	} else {
		args = [][]byte{[]byte("other"), {0xff, 0x00}}
	}
	cis := mustMarshal(t, &fabricpeer.ChaincodeInvocationSpec{ChaincodeSpec: &fabricpeer.ChaincodeSpec{
		ChaincodeId: &fabricpeer.ChaincodeID{Name: testNamespace},
		Input:       &fabricpeer.ChaincodeInput{Args: args},
	}})
	cap := mustMarshal(t, &fabricpeer.ChaincodeActionPayload{
		ChaincodeProposalPayload: mustMarshal(t, &fabricpeer.ChaincodeProposalPayload{Input: cis}),
		Action: &fabricpeer.ChaincodeEndorsedAction{
			ProposalResponsePayload: prp,
			Endorsements: []*fabricpeer.Endorsement{
				{Endorser: identity(t, "Org1MSP")},
				{Endorser: identity(t, "Org2MSP")},
			},
		},
	})
	ftx := mustMarshal(t, &fabricpeer.Transaction{Actions: []*fabricpeer.TransactionAction{{Payload: cap}}})
	chdr := mustMarshal(t, &cb.ChannelHeader{
		Type:      int32(cb.HeaderType_ENDORSER_TRANSACTION),
		ChannelId: "luther",
		TxId:      tx.id,
		Timestamp: timestamppb.New(testTime.Add(time.Duration(num) * time.Second)), //nolint:gosec // G115: test block numbers are small
	})
	shdr := mustMarshal(t, &cb.SignatureHeader{Creator: identity(t, "Org1MSP"), Nonce: []byte("n")})
	payload := mustMarshal(t, &cb.Payload{Header: &cb.Header{ChannelHeader: chdr, SignatureHeader: shdr}, Data: ftx})
	return mustMarshal(t, &cb.Envelope{Payload: payload, Signature: []byte("sig")})
}

// testConfigEnvelope builds a config transaction envelope.
func testConfigEnvelope(t *testing.T) []byte {
	chdr := mustMarshal(t, &cb.ChannelHeader{Type: int32(cb.HeaderType_CONFIG), ChannelId: "luther", Timestamp: timestamppb.New(testTime)})
	payload := mustMarshal(t, &cb.Payload{Header: &cb.Header{ChannelHeader: chdr}, Data: []byte{}})
	return mustMarshal(t, &cb.Envelope{Payload: payload})
}

// testBlock builds block num with the given transactions.
func testBlock(t *testing.T, num uint64, txs ...testTx) *cb.Block {
	t.Helper()
	var data [][]byte
	filter := make([]byte, 0, len(txs))
	if num == 0 {
		data = append(data, testConfigEnvelope(t))
		filter = append(filter, byte(fabricpeer.TxValidationCode_VALID))
	}
	for _, tx := range txs {
		data = append(data, testEnvelope(t, num, tx))
		filter = append(filter, byte(tx.code)) //nolint:gosec // G115: validation codes fit a byte
	}
	hash := sha256.Sum256([]byte(fmt.Sprint(num)))
	md := make([][]byte, len(cb.BlockMetadataIndex_name))
	md[cb.BlockMetadataIndex_TRANSACTIONS_FILTER] = filter
	return &cb.Block{
		Header:   &cb.BlockHeader{Number: num, DataHash: hash[:]},
		Data:     &cb.BlockData{Data: data},
		Metadata: &cb.BlockMetadata{Metadata: md},
	}
}

func write(key, value string) *kvrwset.KVWrite {
	return &kvrwset.KVWrite{Key: key, Value: []byte(value)}
}

func del(key string) *kvrwset.KVWrite {
	return &kvrwset.KVWrite{Key: key, IsDelete: true}
}

func call(method string) string {
	return `{"jsonrpc":"2.0","id":"1","method":"` + method + `","params":[]}`
}

// testChain is a short substrate history: an install of phylum v1, two
// writes of a key, an invalid write, a disable and an enable of v1, and a
// batch call.
func testChain(t *testing.T) []*cb.Block {
	desc := func(status string) string {
		return `{"phylum_id":"v1","fingerprint":"sha512-abc","status":"` + status + `","init_timestamp":"2026-11-14T05:00:01Z"}`
	}
	return []*cb.Block{
		testBlock(t, 0),
		testBlock(t, 1, testTx{id: "tx-install", request: call("update"), code: fabricpeer.TxValidationCode_VALID,
			writes: []*kvrwset.KVWrite{
				write("luther:phylum:desc:v1", desc("IN_SERVICE")),
				write("luther:phylum:src:v1", "source bytes"),
				write("luther:app_control", `{"BATCHING_PAUSED":false}`),
			}}),
		testBlock(t, 2,
			testTx{id: "tx-a", request: call("create_account"), code: fabricpeer.TxValidationCode_VALID,
				writes: []*kvrwset.KVWrite{write("account:1", "100")},
				private: []*rwset.CollectionHashedReadWriteSet{{
					CollectionName: "private",
					HashedRwset: func() []byte {
						return mustMarshal(t, &kvrwset.HashedRWSet{HashedWrites: []*kvrwset.KVWriteHash{{KeyHash: []byte{1, 2}, ValueHash: []byte{3}}}})
					}(),
				}}},
			testTx{id: "tx-conflict", request: call("transfer"), code: fabricpeer.TxValidationCode_MVCC_READ_CONFLICT,
				reads:  []*kvrwset.KVRead{{Key: "account:1", Version: &kvrwset.Version{BlockNum: 1, TxNum: 0}}},
				writes: []*kvrwset.KVWrite{write("account:1", "999")}}),
		testBlock(t, 3, testTx{id: "tx-b", request: call("transfer"), code: fabricpeer.TxValidationCode_VALID,
			reads:  []*kvrwset.KVRead{{Key: "account:1", Version: &kvrwset.Version{BlockNum: 2, TxNum: 0}}},
			writes: []*kvrwset.KVWrite{write("account:1", "80"), write("luther:batch:queue:q", "{}")}}),
		testBlock(t, 4, testTx{id: "tx-disable", request: call("disable"), code: fabricpeer.TxValidationCode_VALID,
			writes: []*kvrwset.KVWrite{write("luther:phylum:desc:v1", desc("DISABLED"))}}),
		testBlock(t, 5, testTx{id: "tx-enable", request: call("enable"), code: fabricpeer.TxValidationCode_VALID,
			writes: []*kvrwset.KVWrite{write("luther:phylum:desc:v1", desc("IN_SERVICE"))}}),
		testBlock(t, 6, testTx{id: "tx-batch", request: `[` + call("a") + `,` + call("b") + `]`, code: fabricpeer.TxValidationCode_VALID,
			writes: []*kvrwset.KVWrite{del("account:1"), write("luther:batch:queue:q", "{}")}}),
		testBlock(t, 7, testTx{id: "tx-raw", code: fabricpeer.TxValidationCode_VALID}),
	}
}

func testIndex(t *testing.T) *Index {
	t.Helper()
	ix := NewIndex("luther")
	for _, b := range testChain(t) {
		require.NoError(t, ix.Add(b))
	}
	return ix
}
