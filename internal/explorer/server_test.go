package explorer

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func getJSON(t *testing.T, srv *httptest.Server, path string, want int, v any) {
	t.Helper()
	resp, err := http.Get(srv.URL + path)
	require.NoError(t, err)
	defer resp.Body.Close() //nolint:errcheck // test
	require.Equal(t, want, resp.StatusCode, path)
	assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))
	if v != nil {
		require.NoError(t, json.NewDecoder(resp.Body).Decode(v))
	}
}

func testServer(t *testing.T, ix *Index, opts ServerOptions) *httptest.Server {
	srv := httptest.NewServer(NewServer(ix, func() Status { return Status{Source: "test", Live: true, Connected: true} }, opts))
	t.Cleanup(srv.Close)
	return srv
}

func TestServerJSON(t *testing.T) {
	srv := testServer(t, testIndex(t), ServerOptions{})

	var status StatusResponse
	getJSON(t, srv, "/v1/status", http.StatusOK, &status)
	assert.Equal(t, StatusResponse{Channel: "luther", Height: 8, Source: Status{Source: "test", Live: true, Connected: true}}, status)

	var blocks BlocksResponse
	getJSON(t, srv, "/v1/blocks?limit=3", http.StatusOK, &blocks)
	require.Len(t, blocks.Blocks, 3)
	assert.Equal(t, uint64(7), blocks.Blocks[0].Number)
	assert.Equal(t, uint64(5), blocks.NextBefore)
	var older BlocksResponse
	getJSON(t, srv, "/v1/blocks?before=2&limit=5", http.StatusOK, &older)
	require.Len(t, older.Blocks, 2)
	assert.Zero(t, older.NextBefore)
	getJSON(t, srv, "/v1/blocks?limit=0", http.StatusBadRequest, nil)
	getJSON(t, srv, "/v1/blocks?before=x", http.StatusBadRequest, nil)

	var blk Block
	getJSON(t, srv, "/v1/blocks/2", http.StatusOK, &blk)
	assert.Equal(t, 2, blk.TxCount)
	assert.Equal(t, "tx-conflict", blk.Transactions[1].ID)
	getJSON(t, srv, "/v1/blocks/99", http.StatusNotFound, nil)
	getJSON(t, srv, "/v1/blocks/-1", http.StatusBadRequest, nil)

	var txs TransactionsResponse
	getJSON(t, srv, "/v1/transactions?valid=false", http.StatusOK, &txs)
	require.Len(t, txs.Transactions, 1)
	assert.Equal(t, "MVCC_READ_CONFLICT", txs.Transactions[0].ValidationCode)
	getJSON(t, srv, "/v1/transactions?method=update", http.StatusOK, &txs)
	require.Len(t, txs.Transactions, 1)
	getJSON(t, srv, "/v1/transactions?valid=maybe", http.StatusBadRequest, nil)

	var tx Transaction
	getJSON(t, srv, "/v1/transactions/tx-b", http.StatusOK, &tx)
	assert.Equal(t, "transfer", tx.Method)
	assert.Len(t, tx.Endorsers, 2)
	getJSON(t, srv, "/v1/transactions/nope", http.StatusNotFound, nil)

	var keys KeysResponse
	getJSON(t, srv, "/v1/keys?kind=framework&limit=2", http.StatusOK, &keys)
	require.Len(t, keys.Keys, 2)
	require.NotNil(t, keys.Next)
	var rest KeysResponse
	getJSON(t, srv, "/v1/keys?kind=framework&after_namespace="+keys.Next.Namespace+"&after_key="+url.QueryEscape(keys.Next.Key), http.StatusOK, &rest)
	assert.Len(t, rest.Keys, 2)
	assert.Nil(t, rest.Next)
	getJSON(t, srv, "/v1/keys?kind=other", http.StatusBadRequest, nil)

	var hist KeyHistoryResponse
	getJSON(t, srv, "/v1/keys/history?namespace="+testNamespace+"&key=account:1", http.StatusOK, &hist)
	assert.Len(t, hist.Writes, 3)
	assert.Empty(t, hist.Space)
	getJSON(t, srv, "/v1/keys/history?namespace="+testNamespace+"&key="+url.QueryEscape("luther:batch:queue:q"), http.StatusOK, &hist)
	assert.Equal(t, "luther:batch:", hist.Space)
	getJSON(t, srv, "/v1/keys/history?key=account:1", http.StatusBadRequest, nil)
	getJSON(t, srv, "/v1/keys/history?namespace="+testNamespace+"&key=nope", http.StatusNotFound, nil)

	var fw FrameworkKeysResponse
	getJSON(t, srv, "/v1/framework-keys", http.StatusOK, &fw)
	assert.Len(t, fw.Spaces, len(frameworkSpaces))
	assert.Equal(t, 1, fw.Phylum.Keys)

	var pv PhylumVersionsResponse
	getJSON(t, srv, "/v1/phylum-versions", http.StatusOK, &pv)
	require.Len(t, pv.Versions, 1)
	assert.Len(t, pv.Versions[0].Events, 3)
}

func TestServerAllowOrigin(t *testing.T) {
	srv := testServer(t, testIndex(t), ServerOptions{AllowOrigin: "http://localhost:3000"})
	resp, err := http.Get(srv.URL + "/v1/status")
	require.NoError(t, err)
	defer resp.Body.Close() //nolint:errcheck // test
	assert.Equal(t, "http://localhost:3000", resp.Header.Get("Access-Control-Allow-Origin"))
}

// sseEvent is one server-sent event.
type sseEvent struct {
	id, event, data string
}

// readEvents reads server-sent events, skipping comments and the retry
// field, and sends them on a channel.
func readEvents(t *testing.T, srv *httptest.Server, path string, header http.Header) (<-chan sseEvent, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+path, nil)
	require.NoError(t, err)
	for k, v := range header {
		req.Header[k] = v
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))
	out := make(chan sseEvent, 100)
	go func() {
		defer resp.Body.Close() //nolint:errcheck // test
		defer close(out)
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(nil, 1<<20)
		var ev sseEvent
		for sc.Scan() {
			line := sc.Text()
			switch {
			case line == "":
				if ev.event != "" {
					out <- ev
				}
				ev = sseEvent{}
			case strings.HasPrefix(line, "id: "):
				ev.id = strings.TrimPrefix(line, "id: ")
			case strings.HasPrefix(line, "event: "):
				ev.event = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				ev.data = strings.TrimPrefix(line, "data: ")
			}
		}
	}()
	return out, cancel
}

func nextEvent(t *testing.T, events <-chan sseEvent) sseEvent {
	t.Helper()
	select {
	case ev, ok := <-events:
		require.True(t, ok, "stream ended")
		return ev
	case <-time.After(5 * time.Second):
		t.Fatal("no event")
		return sseEvent{}
	}
}

func TestStream(t *testing.T) {
	chain := testChain(t)
	ix := NewIndex("luther")
	for _, b := range chain[:6] {
		require.NoError(t, ix.Add(b))
	}
	srv := testServer(t, ix, ServerOptions{Heartbeat: 10 * time.Millisecond})

	// From the tip: only new blocks.
	live, cancelLive := readEvents(t, srv, "/v1/stream/blocks", nil)
	defer cancelLive()
	// From a block: the blocks the index holds, then new blocks.
	replay, cancelReplay := readEvents(t, srv, "/v1/stream/blocks?from=4", nil)
	defer cancelReplay()
	// A reconnecting EventSource resumes after its last event.
	resumed, cancelResumed := readEvents(t, srv, "/v1/stream/blocks", http.Header{"Last-Event-Id": {"4"}})
	defer cancelResumed()

	for _, want := range []string{"4", "5"} {
		ev := nextEvent(t, replay)
		assert.Equal(t, want, ev.id)
	}
	assert.Equal(t, "5", nextEvent(t, resumed).id)

	// Let the heartbeats run, then add a block.
	time.Sleep(50 * time.Millisecond)
	require.NoError(t, ix.Add(chain[6]))
	for _, events := range []<-chan sseEvent{live, replay, resumed} {
		ev := nextEvent(t, events)
		assert.Equal(t, "6", ev.id)
		assert.Equal(t, "block", ev.event)
		var blk Block
		require.NoError(t, json.Unmarshal([]byte(ev.data), &blk))
		assert.Equal(t, uint64(6), blk.Number)
		require.Len(t, blk.Transactions, 1)
		assert.Equal(t, "tx-batch", blk.Transactions[0].ID)
	}

	getJSON(t, srv, "/v1/stream/blocks?from=x", http.StatusBadRequest, nil)
	resp, err := http.DefaultClient.Do(func() *http.Request {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/stream/blocks", nil)
		req.Header.Set("Last-Event-ID", "x")
		return req
	}())
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

// TestStreamCatchUp streams more blocks than one read of the index.
func TestStreamCatchUp(t *testing.T) {
	ix := NewIndex("luther")
	require.NoError(t, ix.Add(testBlock(t, 0)))
	n := streamBatch + 5
	for i := 1; i <= n; i++ {
		require.NoError(t, ix.Add(testBlock(t, uint64(i))))
	}
	srv := testServer(t, ix, ServerOptions{})
	events, cancel := readEvents(t, srv, "/v1/stream/blocks?from=0", nil)
	defer cancel()
	for i := 0; i <= n; i++ {
		ev := nextEvent(t, events)
		var blk Block
		require.NoError(t, json.Unmarshal([]byte(ev.data), &blk))
		require.Equal(t, uint64(i), blk.Number)
	}
}

// TestTransactionsPaging pages through every transaction with the next
// cursor, including pages that end inside a block.
func TestTransactionsPaging(t *testing.T) {
	ix := testIndex(t)
	srv := testServer(t, ix, ServerOptions{})
	want := ix.Transactions(TxFilter{Limit: 100})
	var got []string
	path := "/v1/transactions?limit=2"
	for range 20 {
		var page TransactionsResponse
		getJSON(t, srv, path, http.StatusOK, &page)
		for _, tx := range page.Transactions {
			got = append(got, tx.ID)
		}
		if page.Next == nil {
			break
		}
		path = fmt.Sprintf("/v1/transactions?limit=2&before=%d&before_index=%d", page.Next.Before, page.Next.BeforeIndex)
	}
	var ids []string
	for _, tx := range want {
		ids = append(ids, tx.ID)
	}
	assert.Equal(t, ids, got)
	getJSON(t, srv, "/v1/transactions?before_index=1", http.StatusBadRequest, nil)
	getJSON(t, srv, "/v1/transactions?before=2&before_index=-1", http.StatusBadRequest, nil)
}

func TestServerPreflight(t *testing.T) {
	srv := testServer(t, testIndex(t), ServerOptions{AllowOrigin: "http://localhost:3000"})
	req, err := http.NewRequest(http.MethodOptions, srv.URL+"/v1/stream/blocks", nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close() //nolint:errcheck // test
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)
	assert.Equal(t, "Last-Event-ID", resp.Header.Get("Access-Control-Allow-Headers"))
}
