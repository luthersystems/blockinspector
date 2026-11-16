package explorer

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hyperledger/fabric-protos-go/ledger/rwset/kvrwset"
	fabricpeer "github.com/hyperledger/fabric-protos-go/peer"
	"github.com/stretchr/testify/require"
)

// batchItem builds an item key as substrate's batchItemKey does (escaped
// name and id, with upper-case hex; the due time in Unix seconds plus 2^63
// as 16 hex digits).
func batchItem(escName string, due time.Time, escID string) string {
	return fmt.Sprintf("%s%s~%016x~%s", batchItemRoot, escName, uint64(due.Unix())+1<<63, escID) //nolint:gosec // G115: test times are after 1970
}

func batchIndex(t *testing.T) *Index {
	t.Helper()
	ix := NewIndex("luther")
	early, late := testTime.Add(-time.Hour), testTime.Add(time.Hour)
	blocks := [][]testTx{
		nil,
		{{id: "tx-schedule", request: call("schedule"), code: fabricpeer.TxValidationCode_VALID, writes: []*kvrwset.KVWrite{
			write(batchQueueRoot+"orders", "1"),
			write(batchItem("orders", early, "A"), "1"),
			write(batchItem("orders", late, "B"), "1"),
			write(batchItem("orders", early, "D"), "1"),
			write(batchItem("orders", early, "E"), "1"),
			write(batchRequestRoot+"A", `{"message":{"card":"4111"}}`),
			write(batchRequestRoot+"B", `{"message":{}}`),
			// A queue name with a byte outside the plain set is escaped.
			write(batchItem("a%7Eb", early.Add(-time.Minute), "C"), "1"),
			write(appControlKey, `{"BATCHING_PAUSED":true}`),
			// A key of no known form under luther:batch: is counted.
			write("luther:batch:other", "1"),
			write(batchItem("orders", early, "G"), "1"),
		}}},
		{
			{id: "tx-collect", request: call(batchCollectMethod), code: fabricpeer.TxValidationCode_VALID, writes: []*kvrwset.KVWrite{
				del(batchItem("orders", early, "A")),
				write(batchPendingRoot+"orders~b1", `["A"]`),
				// D has no request record, so the collection skips it.
				del(batchItem("orders", early, "D")),
			}},
			// A refused transaction's writes are not state.
			{id: "tx-bad", request: call("schedule"), code: fabricpeer.TxValidationCode_MVCC_READ_CONFLICT, writes: []*kvrwset.KVWrite{
				write(batchItem("orders", early, "F"), "1"),
			}},
		},
		{
			{id: "tx-reinit", request: call("reinit"), code: fabricpeer.TxValidationCode_VALID, writes: []*kvrwset.KVWrite{
				del(batchItem("orders", early, "E")),
			}},
			// In a JSON-RPC batch with a reinit, a delete is not a skip.
			{id: "tx-mixed", request: `[{"jsonrpc":"2.0","method":"reinit","id":1},{"jsonrpc":"2.0","method":"` + batchCollectMethod + `","id":2}]`,
				code: fabricpeer.TxValidationCode_VALID, writes: []*kvrwset.KVWrite{
					del(batchItem("orders", early, "G")),
				}},
		},
	}
	for i, txs := range blocks {
		require.NoError(t, ix.Add(testBlock(t, uint64(i), txs...))) //nolint:gosec // G115: small test index
	}
	return ix
}

func TestBatchKeys(t *testing.T) {
	due := time.Date(1969, 12, 31, 23, 0, 0, 0, time.UTC)
	q, d, id, ok := batchItemKey(batchItem("a%7Eb", due, "x%25y"))
	require.True(t, ok)
	require.Equal(t, "a~b", q)
	require.True(t, due.Equal(d), "a time before 1970 decodes")
	require.Equal(t, "x%y", id)
	_, _, _, ok = batchItemKey(batchItemRoot + "q~zz~x")
	require.False(t, ok)
	_, _, _, ok = batchItemKey(batchItem("bad%7", due, "x"))
	require.False(t, ok)
	_, _, _, ok = batchItemKey(batchItem("a%7eb", due, "x"))
	require.False(t, ok, "substrate writes upper-case hex only")

	q, b, ok := batchPendingKey(batchPendingRoot + "orders~b%2F1")
	require.True(t, ok)
	require.Equal(t, "orders", q)
	require.Equal(t, "b/1", b)
	_, _, ok = batchPendingKey(batchPendingRoot + "orders~b~1")
	require.False(t, ok)
}

func TestBatchQueues(t *testing.T) {
	ix := batchIndex(t)
	qs, undecodable := ix.BatchQueues()
	require.Equal(t, 1, undecodable)
	require.Len(t, qs, 2)
	require.Equal(t, "a~b", qs[0].Name)
	require.False(t, qs[0].Created)
	require.Equal(t, 1, qs[0].Due)

	o := qs[1]
	require.Equal(t, "orders", o.Name)
	require.True(t, o.Created)
	require.Equal(t, 1, o.Prepared, "a refused transaction's item is not prepared")
	require.Equal(t, 0, o.Due)
	require.Equal(t, 1, o.PendingBatches)
	require.Equal(t, 1, o.PendingItems)
	require.NotNil(t, o.Paused)
	require.True(t, *o.Paused)
}

func TestBatchQueuePage(t *testing.T) {
	ix := batchIndex(t)
	p, ok := ix.BatchQueue("", "orders")
	require.True(t, ok)
	require.Len(t, p.Items, 1)
	require.Equal(t, "B", p.Items[0].ID)
	require.True(t, p.Items[0].HasRequest)
	require.False(t, p.Items[0].IsDue)
	require.Equal(t, "tx-schedule", p.Items[0].Scheduled.TxID)

	require.Len(t, p.Pending, 1)
	require.Equal(t, "b1", p.Pending[0].ID)
	require.Equal(t, []string{"A"}, p.Pending[0].Items)
	require.Equal(t, "tx-collect", p.Pending[0].Taken.TxID)

	require.Len(t, p.Finished, 4)
	require.Equal(t, "G", p.Finished[0].ID, "latest first")
	require.Equal(t, "removed", p.Finished[0].Outcome)
	require.Equal(t, "E", p.Finished[1].ID)
	require.Equal(t, "removed", p.Finished[1].Outcome)
	byID := map[string]BatchFinished{}
	for _, f := range p.Finished {
		byID[f.ID] = f
	}
	require.Equal(t, "collected", byID["A"].Outcome)
	require.Equal(t, "b1", byID["A"].Batch)
	require.Equal(t, "skipped", byID["D"].Outcome)

	_, ok = ix.BatchQueue("", "missing")
	require.False(t, ok)
}

func TestBatchPausedProperty(t *testing.T) {
	ix := NewIndex("luther")
	require.NoError(t, ix.Add(testBlock(t, 0)))
	require.NoError(t, ix.Add(testBlock(t, 1, testTx{id: "tx", request: call("x"), code: fabricpeer.TxValidationCode_VALID,
		writes: []*kvrwset.KVWrite{write(batchItem("q", testTime, "a"), "1"), write(appControlKey, `{"BATCHING_PAUSED":false}`)}})))
	qs, _ := ix.BatchQueues()
	require.Len(t, qs, 1)
	require.False(t, *qs[0].Paused)
	require.Equal(t, 1, qs[0].Due, "an item due at the latest block's time is due")
}

func TestServerBatches(t *testing.T) {
	s := NewServer(batchIndex(t), func() Status { return Status{} }, ServerOptions{})
	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec
	}
	require.Equal(t, http.StatusOK, get("/v1/batches").Code)
	rec := get("/v1/batches/queue?name=orders")
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotContains(t, rec.Body.String(), "4111", "a request record is not shown")
	require.Equal(t, http.StatusOK, get("/v1/batches/queue?name=a~b").Code)
	require.Equal(t, http.StatusBadRequest, get("/v1/batches/queue").Code)
	require.Equal(t, http.StatusNotFound, get("/v1/batches/queue?name=nope").Code)
}
