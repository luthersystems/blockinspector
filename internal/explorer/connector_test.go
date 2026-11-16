package explorer

import (
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hyperledger/fabric-protos-go/ledger/rwset"
	"github.com/hyperledger/fabric-protos-go/ledger/rwset/kvrwset"
	fabricpeer "github.com/hyperledger/fabric-protos-go/peer"
	"github.com/stretchr/testify/require"
)

// The values below copy what substrate writes when it raises a connector
// event (libframework/connectorevents.go): the event metadata entry
// luther:ev:N with {"rid": ...}, the body at the request id in a
// collection, and the callback state at luther:cr:RID in "private", which
// $ch_callback purges on a reply and DefFlow purges when a run stops
// waiting (libflow/runtime.go dropRequest).
const (
	ridReplied = "11111111-1111-4111-8111-111111111111"
	ridOpen    = "22222222-2222-4222-8222-222222222222"
	ridDropped = "33333333-3333-4333-8333-333333333333"
	ridNoCB    = "44444444-4444-4444-8444-444444444444"
)

func hashedWrite(key string, del, purge bool) *kvrwset.KVWriteHash {
	h := sha256.Sum256([]byte(key))
	w := &kvrwset.KVWriteHash{KeyHash: h[:], IsDelete: del, IsPurge: purge}
	if !del {
		w.ValueHash = []byte{1}
	}
	return w
}

func collection(t *testing.T, name string, writes ...*kvrwset.KVWriteHash) *rwset.CollectionHashedReadWriteSet {
	return &rwset.CollectionHashedReadWriteSet{CollectionName: name,
		HashedRwset: mustMarshal(t, &kvrwset.HashedRWSet{HashedWrites: writes})}
}

func eventMeta(t *testing.T, rids ...string) string {
	m := map[string]string{"txTrace": "true"}
	for i, rid := range rids {
		b, err := json.Marshal(map[string]string{"rid": rid})
		require.NoError(t, err)
		m[connectorEventPrefix+itoa(int64(i))] = string(b)
	}
	b, err := json.Marshal(m)
	require.NoError(t, err)
	return string(b)
}

func connectorTestIndex(t *testing.T) *Index {
	t.Helper()
	ix := NewIndex("luther")
	blocks := [][]testTx{
		nil,
		{
			{id: "tx-raise", request: call("order_start"), code: fabricpeer.TxValidationCode_VALID,
				event: eventMeta(t, ridReplied, ridOpen),
				private: []*rwset.CollectionHashedReadWriteSet{
					collection(t, "private",
						hashedWrite(ridReplied, false, false),
						hashedWrite(connectorCallbackKey+ridReplied, false, false),
						hashedWrite(connectorCallbackKey+ridOpen, false, false)),
					collection(t, "orgpdc", hashedWrite(ridOpen, false, false)),
				}},
			{id: "tx-raise2", request: call("notify"), code: fabricpeer.TxValidationCode_VALID,
				event:  eventMeta(t, ridNoCB),
				writes: []*kvrwset.KVWrite{write(ridNoCB, `{"to":"someone@example.com"}`)}},
			{id: "tx-raise3", request: call("charge"), code: fabricpeer.TxValidationCode_VALID,
				event:   eventMeta(t, ridDropped),
				private: []*rwset.CollectionHashedReadWriteSet{collection(t, "private", hashedWrite(connectorCallbackKey+ridDropped, false, false))}},
			// A refused transaction raises nothing.
			{id: "tx-bad", request: call("order_start"), code: fabricpeer.TxValidationCode_MVCC_READ_CONFLICT,
				event: eventMeta(t, "55555555-5555-4555-8555-555555555555")},
		},
		{
			{id: "tx-reply", request: call("$ch_callback"), code: fabricpeer.TxValidationCode_VALID,
				private: []*rwset.CollectionHashedReadWriteSet{collection(t, "private", hashedWrite(connectorCallbackKey+ridReplied, true, true))}},
			// A DefFlow timeout purges the callback state without a reply.
			{id: "tx-timeout", request: call("order_tick"), code: fabricpeer.TxValidationCode_VALID,
				private: []*rwset.CollectionHashedReadWriteSet{collection(t, "private", hashedWrite(connectorCallbackKey+ridDropped, true, false))}},
			// A refused reply does not answer the request.
			{id: "tx-reply-bad", request: call("$ch_callback"), code: fabricpeer.TxValidationCode_MVCC_READ_CONFLICT,
				private: []*rwset.CollectionHashedReadWriteSet{collection(t, "private", hashedWrite(connectorCallbackKey+ridOpen, true, true))}},
		},
	}
	for i, txs := range blocks {
		require.NoError(t, ix.Add(testBlock(t, uint64(i), txs...))) //nolint:gosec // G115: small test index
	}
	return ix
}

func TestConnectorRequests(t *testing.T) {
	ix := connectorTestIndex(t)
	all, sum := ix.ConnectorRequests(ConnectorFilter{})
	require.Len(t, all, 4)
	require.Equal(t, ConnectorSummary{OldestOpen: sum.OldestOpen, Requests: 4, Open: 1, Replied: 1, Dropped: 1, NoCallback: 1}, sum)
	require.NotNil(t, sum.OldestOpen)
	require.Equal(t, ridDropped, all[0].RID, "most recent first")

	r, ok := ix.ConnectorRequest("", ridReplied)
	require.True(t, ok)
	require.Equal(t, "tx-raise", r.Raised.TxID)
	require.Equal(t, 0, r.Event)
	require.Equal(t, "private", r.Body)
	require.Equal(t, "private", r.Collection)
	require.True(t, r.HasCallback)
	require.NotNil(t, r.Reply)
	require.Equal(t, "tx-reply", r.Reply.TxID)
	require.Equal(t, int64(1000), *r.LatencyMS)

	r, _ = ix.ConnectorRequest("", ridOpen)
	require.Equal(t, 1, r.Event)
	require.Equal(t, "orgpdc", r.Collection)
	require.Nil(t, r.Reply, "a refused reply does not answer")

	r, _ = ix.ConnectorRequest("", ridDropped)
	require.Nil(t, r.Reply)
	require.Equal(t, "tx-timeout", r.Dropped.TxID)
	require.Nil(t, r.LatencyMS)
	require.Equal(t, "", r.Body, "a body at another key is not found")

	r, _ = ix.ConnectorRequest("", ridNoCB)
	require.Equal(t, "public", r.Body)
	require.False(t, r.HasCallback)

	_, ok = ix.ConnectorRequest("", "55555555-5555-4555-8555-555555555555")
	require.False(t, ok, "a refused transaction raises nothing")

	open, _ := ix.ConnectorRequests(ConnectorFilter{State: "open"})
	require.Len(t, open, 1)
	replied, _ := ix.ConnectorRequests(ConnectorFilter{State: "replied"})
	require.Len(t, replied, 1)
	dropped, _ := ix.ConnectorRequests(ConnectorFilter{State: "dropped"})
	require.Len(t, dropped, 1)
	q, _ := ix.ConnectorRequests(ConnectorFilter{Query: "4444"})
	require.Len(t, q, 1)
	lim, _ := ix.ConnectorRequests(ConnectorFilter{Limit: 1})
	require.Len(t, lim, 1)
}

func TestConnectorFlowLink(t *testing.T) {
	ix := connectorTestIndex(t)
	require.NoError(t, ix.Add(testBlock(t, 3, testTx{id: "tx-flow", request: call("order_start"), code: fabricpeer.TxValidationCode_VALID,
		writes: []*kvrwset.KVWrite{write("luther:flow:run:order:h:"+flowRun1,
			flowHeaderJSON(flowRun1, "waiting", "charge", "connector", 1, 1, `,"rids":["`+ridOpen+`"]`))}})))
	// The run leaves the wait: its header drops the rids, the link stays.
	require.NoError(t, ix.Add(testBlock(t, 4, testTx{id: "tx-flow2", request: call("order_input"), code: fabricpeer.TxValidationCode_VALID,
		writes: []*kvrwset.KVWrite{write("luther:flow:run:order:h:"+flowRun1, flowHeaderJSON(flowRun1, "done", "", "", 2, 2, ""))}})))
	r, ok := ix.ConnectorRequest("", ridOpen)
	require.True(t, ok)
	require.Equal(t, &FlowRunRef{Flow: "order", Run: flowRun1}, r.Flow)
}

func TestConnectorReusedRID(t *testing.T) {
	ix := connectorTestIndex(t)
	// A reused seed raises ridReplied again, with a handler.
	require.NoError(t, ix.Add(testBlock(t, 3, testTx{id: "tx-again", request: call("order_start"), code: fabricpeer.TxValidationCode_VALID,
		event:   eventMeta(t, ridReplied),
		private: []*rwset.CollectionHashedReadWriteSet{collection(t, "private", hashedWrite(connectorCallbackKey+ridReplied, false, false))}})))
	all, sum := ix.ConnectorRequests(ConnectorFilter{})
	require.Len(t, all, 5)
	require.Equal(t, 2, sum.Open)
	r, _ := ix.ConnectorRequest("", ridReplied)
	require.Equal(t, "tx-again", r.Raised.TxID, "the latest request with the id")
	require.Nil(t, r.Reply)
}

func TestServerConnector(t *testing.T) {
	s := NewServer(connectorTestIndex(t), func() Status { return Status{} }, ServerOptions{})
	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec
	}
	rec := get("/v1/connector/requests?state=open")
	require.Equal(t, http.StatusOK, rec.Code)
	var resp ConnectorRequestsResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Len(t, resp.Requests, 1)
	require.Equal(t, 4, resp.Summary.Requests)
	require.NotContains(t, get("/v1/connector/requests").Body.String(), "someone@example.com", "a request body is not shown")
	require.Equal(t, http.StatusBadRequest, get("/v1/connector/requests?state=nope").Code)
	require.Equal(t, http.StatusOK, get("/v1/connector/requests/"+ridReplied).Code)
	require.Equal(t, http.StatusNotFound, get("/v1/connector/requests/nope").Code)
}
