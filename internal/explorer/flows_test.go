package explorer

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hyperledger/fabric-protos-go/ledger/rwset/kvrwset"
	fabricpeer "github.com/hyperledger/fabric-protos-go/peer"
	"github.com/stretchr/testify/require"
)

// The values below are copies of what substrate writes for a flow with a
// public store (internal/substrate/shiro/libflow: flowstore.Header with
// HeaderVersion 2, events.go historyRow, flowstore.ParkedRow), so a change of
// those formats that this decoder misses fails here once the copies are
// updated.
const (
	flowRun1 = "0123456789abcdef0123456789abcdef"
	flowRun2 = "fedcba9876543210fedcba9876543210"
	flowRun3 = "00000000000000000000000000000003"
)

func flowHeaderJSON(id, state, label, kind string, gen, hist int64, extra string) string {
	return `{"v":2,"flow":"order","id":"` + id + `","state":"` + state + `","label":"` + label + `","kind":"` + kind +
		`","gen":` + itoa(gen) + `,"version":"m1","phylum":"v1","caller":"Org1MSP","started_at":1763096400000000000,"updated_at":1763096460123456789` +
		extra + `,"hist":` + itoa(hist) + `}`
}

func itoa(n int64) string { b, _ := json.Marshal(n); return string(b) }

func flowHist(n, gen int64, event, from, to string) string {
	return `{"n":` + itoa(n) + `,"gen":` + itoa(gen) + `,"at":"2026-11-14T05:00:0` + itoa(n) + `Z","event":"` + event + `","from":"` + from + `","to":"` + to + `"}`
}

func flowIndex(t *testing.T) *Index {
	t.Helper()
	ix := NewIndex("luther")
	h := "luther:flow:run:order:h:"
	hist := "luther:flow:run:order:hist:"
	blocks := [][]testTx{
		nil,
		{{id: "tx-start", request: call("order_start"), code: fabricpeer.TxValidationCode_VALID, writes: []*kvrwset.KVWrite{
			write("luther:flow:man:order:m1", `{"flow":"order"}`),
			write(h+flowRun1, flowHeaderJSON(flowRun1, "waiting", "approve", "input", 1, 1, `,"from":["Org1MSP"],"key":"k1","due_at":1763100000000000000`)),
			write(hist+flowRun1+":000000000001", flowHist(1, 1, "start", "", "approve")),
			write("luther:flow:run:order:v:"+flowRun1, `{"secret":"personal"}`),
		}}},
		{
			{id: "tx-input", request: call("order_input"), code: fabricpeer.TxValidationCode_VALID, writes: []*kvrwset.KVWrite{
				write(h+flowRun1, flowHeaderJSON(flowRun1, "done", "", "", 2, 2, "")),
				write(hist+flowRun1+":000000000002", flowHist(2, 2, "input", "approve", "done")),
				write("luther:flow:run:order:r:"+flowRun1, `{"total":5}`),
				write("luther:flow:run:order:c:"+flowRun1+":"+flowRun3, `{"flow":"payment","run":"`+flowRun3+`","label":"approve"}`),
			}},
			{id: "tx-fail", request: call("order_start"), code: fabricpeer.TxValidationCode_VALID, writes: []*kvrwset.KVWrite{
				write(h+flowRun2, flowHeaderJSON(flowRun2, "failed", "charge", "", 1, 1,
					`,"error":{"reason":"connector-error","kind":"connector-error","label":"charge","attempts":3,"message":"declined","data":{"card":"4111"}}`)),
				write("luther:flow:park:order:1:v1:"+flowRun2, `{"label":"charge","phylum":"v1","gen":1,"outcome":{"status":"failed","target":"v2","error":"no route"}}`),
			}},
			// A header of a newer format is shown raw, not guessed at.
			{id: "tx-v3", request: call("order_start"), code: fabricpeer.TxValidationCode_VALID, writes: []*kvrwset.KVWrite{
				write(h+flowRun3, `{"v":3,"flow":"order","id":"`+flowRun3+`","state":"new-state","key":"secret-key","error":{"data":{"card":"4111"}}}`),
				// A history row deleted later is not shown.
				write(hist+flowRun1+":000000000003", flowHist(3, 3, "input", "done", "done")),
			}},
			// A refused transaction's writes are not state.
			{id: "tx-bad", request: call("order_start"), code: fabricpeer.TxValidationCode_MVCC_READ_CONFLICT, writes: []*kvrwset.KVWrite{
				write(h+"ffffffffffffffffffffffffffffffff", flowHeaderJSON("ffffffffffffffffffffffffffffffff", "waiting", "x", "input", 1, 0, "")),
			}},
		},
		{{id: "tx-purge", request: call("order_purge"), code: fabricpeer.TxValidationCode_VALID, writes: []*kvrwset.KVWrite{
			del(hist + flowRun1 + ":000000000003"),
		}}},
	}
	for i, txs := range blocks {
		require.NoError(t, ix.Add(testBlock(t, uint64(i), txs...))) //nolint:gosec // G115: small test index
	}
	return ix
}

func TestFlowKeys(t *testing.T) {
	flow, kind, rest, ok := flowRunKey("luther:flow:run:order:hist:" + flowRun1 + ":000000000002")
	require.True(t, ok)
	require.Equal(t, "order", flow)
	require.Equal(t, "hist", kind)
	require.Equal(t, flowRun1+":000000000002", rest)
	_, _, _, ok = flowRunKey("luther:flow:run:order")
	require.False(t, ok)

	flow, version, run, ok := flowParkedKey("luther:flow:park:order:1:v1:" + flowRun2)
	require.True(t, ok)
	require.Equal(t, "order", flow)
	require.Equal(t, "1:v1", version, "a version may hold colons")
	require.Equal(t, flowRun2, run)
	_, _, _, ok = flowParkedKey("luther:flow:park:order:")
	require.False(t, ok)

	flow, hash, ok := flowManifestKey("luther:flow:man:order:m1")
	require.True(t, ok)
	require.Equal(t, "order", flow)
	require.Equal(t, "m1", hash)
}

func TestFlowRaw(t *testing.T) {
	require.JSONEq(t, `{"omitted":1}`, string(flowRaw([]byte(`not json`))))
	require.JSONEq(t, `{"omitted":1}`, string(flowRaw([]byte(`["x"]`))))
	require.JSONEq(t, `{"omitted":1}`, string(flowRaw([]byte(`null`))))
	require.JSONEq(t, `{"state":"done","omitted":2}`,
		string(flowRaw([]byte(`{"state":"done","label":{"x":1},"message":"personal"}`))),
		"only named scalar fields are kept")
}

func TestFlows(t *testing.T) {
	ix := flowIndex(t)
	flows := ix.Flows()
	require.Len(t, flows, 1)
	f := flows[0]
	require.Equal(t, "order", f.Flow)
	require.Equal(t, testNamespace, f.Namespace)
	require.Equal(t, []string{"m1"}, f.Manifests)
	require.Equal(t, 3, f.Runs)
	require.Equal(t, 1, f.Undecodable)
	require.Equal(t, map[string]int{"done": 1, "failed": 1}, f.ByState)
	require.Equal(t, 1, f.Parked)
}

func TestFlowRuns(t *testing.T) {
	ix := flowIndex(t)
	all := ix.FlowRuns(FlowRunFilter{Flow: "order"})
	require.Len(t, all, 3)
	// Most recently written first.
	require.Equal(t, "tx-v3", all[0].LastWrite.TxID)

	failed := ix.FlowRuns(FlowRunFilter{Flow: "order", State: "failed"})
	require.Len(t, failed, 1)
	require.Equal(t, flowRun2, failed[0].ID)
	require.Equal(t, "connector-error", failed[0].Header.Error.Reason)
	require.Equal(t, 3, failed[0].Header.Error.Attempts)

	b, err := json.Marshal(failed[0])
	require.NoError(t, err)
	require.NotContains(t, string(b), "4111", "error data can hold personal data and is not shown")

	require.Len(t, ix.FlowRuns(FlowRunFilter{Flow: "order", Query: "fedc"}), 1)
	require.Empty(t, ix.FlowRuns(FlowRunFilter{Flow: "other"}))
	require.Len(t, ix.FlowRuns(FlowRunFilter{Flow: "order", Limit: 2}), 2)

	raw := ix.FlowRuns(FlowRunFilter{Flow: "order", Query: flowRun3})
	require.Len(t, raw, 1)
	require.Nil(t, raw[0].Header)
	require.JSONEq(t, `{"v":3,"flow":"order","id":"`+flowRun3+`","state":"new-state","omitted":2}`, string(raw[0].Raw),
		"a header of an unknown format keeps only fields that hold no personal data")
	require.NotContains(t, string(raw[0].Raw), "4111")
	require.Len(t, ix.FlowRuns(FlowRunFilter{Flow: "order", Query: "89abcdef0123"}), 1, "q matches part of a run ID")
}

func TestFlowRunPage(t *testing.T) {
	ix := flowIndex(t)
	page, ok := ix.FlowRun("", "order", flowRun1)
	require.True(t, ok)
	require.Equal(t, "done", page.Header.State)
	require.Len(t, page.History, 2)
	require.Equal(t, "start", page.History[0].Event)
	require.Equal(t, "tx-start", page.History[0].TxID)
	require.Equal(t, "input", page.History[1].Event)
	require.Equal(t, "approve", page.History[1].From)
	require.Equal(t, "tx-input", page.History[1].TxID)
	require.Equal(t, []FlowChild{{Flow: "payment", Run: flowRun3, Label: "approve"}}, page.Children)
	require.True(t, page.HasResult)
	require.True(t, page.HasVars)

	b, err := json.Marshal(page)
	require.NoError(t, err)
	require.NotContains(t, string(b), "personal", "saved locals are not shown")
	require.NotContains(t, string(b), `"total"`, "the result is not shown")

	page, ok = ix.FlowRun(testNamespace, "order", flowRun2)
	require.True(t, ok)
	require.Len(t, page.Parked, 1)
	require.Equal(t, "1:v1", page.Parked[0].Version)
	require.Equal(t, "failed", page.Parked[0].Row.Outcome.Status)

	_, ok = ix.FlowRun("", "order", "ffffffffffffffffffffffffffffffff")
	require.False(t, ok, "a refused transaction's run is not in state")
}

func TestServerFlows(t *testing.T) {
	s := NewServer(flowIndex(t), func() Status { return Status{} }, ServerOptions{})
	get := func(path string, v any) int {
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if v != nil && rec.Code == http.StatusOK {
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), v))
		}
		return rec.Code
	}
	var flows FlowsResponse
	require.Equal(t, http.StatusOK, get("/v1/flows", &flows))
	require.Len(t, flows.Flows, 1)

	var runs FlowRunsResponse
	require.Equal(t, http.StatusOK, get("/v1/flows/order/runs?state=done", &runs))
	require.Len(t, runs.Runs, 1)
	require.Equal(t, http.StatusBadRequest, get("/v1/flows/order/runs?limit=0", nil))

	var page FlowRunPage
	require.Equal(t, http.StatusOK, get("/v1/flows/order/runs/"+flowRun1, &page))
	require.Len(t, page.History, 2)
	require.Equal(t, http.StatusNotFound, get("/v1/flows/order/runs/nope", nil))
}
