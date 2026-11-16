package explorer

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Connector Hub requests (luthersystems/substrate
// internal/substrate/shiro/libframework/connectorevents.go). A transaction
// that raises a connector event sets the chaincode event "luther", whose
// payload is the transaction's metadata: a JSON object of strings. Each
// event is the entry "luther:ev:N" (N counts from 0 in the transaction),
// whose value is the JSON {"rid": REQUEST ID}. The raise also writes:
//
//   - the request body at the event's key (the request id unless the event
//     names another key), in collection pdc ("private" by default), or in
//     public state when the event has no pdc;
//   - with a handler, the callback state at luther:cr:RID in collection
//     "private".
//
// The $ch_callback transaction that hands the reply to the handler purges
// luther:cr:RID. So does DefFlow when a run leaves its await-connector
// without a reply (a timeout, a cancel, a retry): that request is dropped,
// not replied. A handler that fails commits nothing, so its request stays
// open. A block holds only the SHA-256 of a private key, so the explorer
// finds those writes by the hash of the key it expects.
//
// Fabric keeps one chaincode event per transaction, the last one set. A
// phylum that sets its own event after it raises a connector event hides
// the raise, and the explorer does not see that request.
const (
	connectorEventName   = "luther"
	connectorEventPrefix = "luther:ev:"
	connectorCallbackKey = "luther:cr:"
	connectorCollection  = "private"
	// connectorReplyMethod is the endpoint that hands replies to handlers.
	connectorReplyMethod = "$ch_callback"
)

// ConnectorRequest is one connector event and what the ledger shows of it.
type ConnectorRequest struct {
	// Reply is the $ch_callback transaction that purged the callback
	// state: it handed the reply to the handler.
	Reply *TxRef `json:"reply,omitempty"`
	// Dropped is a transaction of another method that purged the callback
	// state, such as a DefFlow timeout or cancel: no reply was handled.
	Dropped *TxRef `json:"dropped,omitempty"`
	// LatencyMS is the time from the raise to the reply.
	LatencyMS *int64 `json:"latency_ms,omitempty"`
	// Flow names the DefFlow run whose header lists the request id (a flow
	// with a public store).
	Flow      *FlowRunRef `json:"flow,omitempty"`
	RID       string      `json:"rid"`
	Namespace string      `json:"namespace"`
	// Body is "public" or "private" when the raise wrote the body at the
	// request id; Collection is its collection. A body at another key is
	// not found, and Body is empty. The body is not shown: it can hold
	// personal data.
	Body       string `json:"body,omitempty"`
	Collection string `json:"collection,omitempty"`
	Raised     TxRef  `json:"raised"`
	// Event is N of the event's luther:ev:N entry.
	Event int `json:"event"`
	// HasCallback is true when the raise registered a handler.
	HasCallback bool `json:"has_callback"`
}

// FlowRunRef names a DefFlow run.
type FlowRunRef struct {
	Flow string `json:"flow"`
	Run  string `json:"run"`
}

// connectorIndex holds the connector requests of an Index, built as blocks
// are added.
type connectorIndex struct {
	// byRID maps a namespace and request id to its latest request: a
	// reused seed can raise a request id again.
	byRID map[stateKey]*ConnectorRequest
	// open maps a namespace and the hex SHA-256 of luther:cr:RID to the
	// request whose callback state is not purged yet.
	open map[stateKey]*ConnectorRequest
	// flowByRID maps a namespace and request id to the last DefFlow run
	// whose public header listed it. A header drops its rids when the run
	// leaves the wait, so this is kept, not read from the live header.
	flowByRID map[stateKey]FlowRunRef
	order     []*ConnectorRequest
}

func newConnectorIndex() connectorIndex {
	return connectorIndex{
		byRID:     map[stateKey]*ConnectorRequest{},
		open:      map[stateKey]*ConnectorRequest{},
		flowByRID: map[stateKey]FlowRunRef{},
	}
}

func keyHash(key string) string {
	h := sha256.Sum256([]byte(key))
	return hex.EncodeToString(h[:])
}

// connectorEvents returns the request ids of a transaction's connector
// events, by event number.
func connectorEvents(tx *Transaction) map[int]string {
	if tx.Event == nil || tx.Event.Name != connectorEventName || tx.Event.Payload == nil {
		return nil
	}
	var meta map[string]string
	if json.Unmarshal(tx.Event.Payload.Bytes(), &meta) != nil {
		return nil
	}
	out := map[int]string{}
	for k, v := range meta {
		s, ok := strings.CutPrefix(k, connectorEventPrefix)
		if !ok {
			continue
		}
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 || strconv.Itoa(n) != s {
			continue
		}
		var ref struct {
			RID string `json:"rid"`
		}
		if json.Unmarshal([]byte(v), &ref) == nil && ref.RID != "" {
			out[n] = ref.RID
		}
	}
	return out
}

// applyConnector records the connector events, the closes of callback
// states and the DefFlow request ids of a valid transaction. ix.mu is held.
func (ix *Index) applyConnector(tx *Transaction) {
	ref := txRefOf(tx)
	ci := &ix.connector
	reply := tx.Method == connectorReplyMethod || contains(tx.BatchMethods, connectorReplyMethod)
	// Closes first: a transaction's own callback writes are its raise.
	for _, ns := range tx.RWSets {
		for _, w := range ns.PrivateWrites {
			if (!w.IsDelete && !w.IsPurge) || w.Collection != connectorCollection {
				continue
			}
			k := stateKey{namespace: ns.Namespace, key: w.KeyHash}
			r := ci.open[k]
			if r == nil {
				continue
			}
			delete(ci.open, k)
			closed := ref
			if !reply {
				r.Dropped = &closed
				continue
			}
			r.Reply = &closed
			if r.Raised.Time != nil && ref.Time != nil {
				ms := ref.Time.Sub(*r.Raised.Time).Milliseconds()
				r.LatencyMS = &ms
			}
		}
		for _, w := range ns.Writes {
			ix.recordFlowRIDs(ns.Namespace, w)
		}
	}
	events := connectorEvents(tx)
	if len(events) == 0 {
		return
	}
	nums := make([]int, 0, len(events))
	for n := range events {
		nums = append(nums, n)
	}
	sort.Ints(nums)
	namespace := tx.Event.ChaincodeID
	for _, n := range nums {
		rid := events[n]
		r := &ConnectorRequest{RID: rid, Namespace: namespace, Raised: ref, Event: n}
		cbHash, bodyHash := keyHash(connectorCallbackKey+rid), keyHash(rid)
		for _, ns := range tx.RWSets {
			if ns.Namespace != namespace {
				continue
			}
			for _, w := range ns.Writes {
				if w.Key == rid && !w.IsDelete {
					r.Body = "public"
				}
			}
			for _, w := range ns.PrivateWrites {
				if w.IsDelete || w.IsPurge {
					continue
				}
				switch {
				case w.KeyHash == bodyHash && r.Body == "":
					r.Body, r.Collection = "private", w.Collection
				case w.KeyHash == cbHash && w.Collection == connectorCollection:
					r.HasCallback = true
				}
			}
		}
		ci.byRID[stateKey{namespace: namespace, key: rid}] = r
		if r.HasCallback {
			ci.open[stateKey{namespace: namespace, key: cbHash}] = r
		}
		ci.order = append(ci.order, r)
	}
}

// recordFlowRIDs records the request ids a DefFlow run header lists.
// ix.mu is held.
func (ix *Index) recordFlowRIDs(namespace string, w Write) {
	if w.IsDelete {
		return
	}
	flow, kind, id, ok := flowRunKey(w.Key)
	if !ok || kind != "h" {
		return
	}
	b := w.Value.Bytes()
	if flowHeaderVersionOf(b) != flowHeaderVersion {
		return
	}
	var h struct {
		RIDs []string `json:"rids"`
	}
	if json.Unmarshal(b, &h) != nil {
		return
	}
	for _, rid := range h.RIDs {
		ix.connector.flowByRID[stateKey{namespace: namespace, key: rid}] = FlowRunRef{Flow: flow, Run: id}
	}
}

// ConnectorFilter selects connector requests.
type ConnectorFilter struct {
	// State is "open" (a handler and no close yet), "replied", "dropped"
	// (the callback state was purged without a reply) or "no-callback";
	// empty is all.
	State string
	// Query matches part of a request id.
	Query string
	Limit int
}

// ConnectorSummary counts connector requests.
type ConnectorSummary struct {
	// OldestOpen is the raise time of the oldest open request.
	OldestOpen *time.Time `json:"oldest_open,omitempty"`
	Requests   int        `json:"requests"`
	Open       int        `json:"open"`
	Replied    int        `json:"replied"`
	Dropped    int        `json:"dropped"`
	NoCallback int        `json:"no_callback"`
}

func (r *ConnectorRequest) state() string {
	switch {
	case !r.HasCallback:
		return "no-callback"
	case r.Reply != nil:
		return "replied"
	case r.Dropped != nil:
		return "dropped"
	}
	return "open"
}

// withFlow returns a copy of r with its DefFlow run. ix.mu is held.
func (ix *Index) withFlow(r *ConnectorRequest) ConnectorRequest {
	c := *r
	if fr, ok := ix.connector.flowByRID[stateKey{namespace: r.Namespace, key: r.RID}]; ok {
		c.Flow = &fr
	}
	return c
}

// ConnectorRequests returns the requests that match f, most recent first,
// and the counts of all requests.
func (ix *Index) ConnectorRequests(f ConnectorFilter) ([]ConnectorRequest, ConnectorSummary) {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	var sum ConnectorSummary
	for _, r := range ix.connector.order {
		sum.Requests++
		switch r.state() {
		case "open":
			sum.Open++
			if t := r.Raised.Time; t != nil && (sum.OldestOpen == nil || t.Before(*sum.OldestOpen)) {
				sum.OldestOpen = t
			}
		case "replied":
			sum.Replied++
		case "dropped":
			sum.Dropped++
		case "no-callback":
			sum.NoCallback++
		}
	}
	out := []ConnectorRequest{}
	for i := len(ix.connector.order) - 1; i >= 0; i-- {
		r := ix.connector.order[i]
		if f.State != "" && r.state() != f.State {
			continue
		}
		if f.Query != "" && !strings.Contains(r.RID, f.Query) {
			continue
		}
		out = append(out, ix.withFlow(r))
		if f.Limit > 0 && len(out) >= f.Limit {
			break
		}
	}
	return out, sum
}

// ConnectorRequest returns the latest request with an id. With namespace
// empty, the first namespace that has it is used.
func (ix *Index) ConnectorRequest(namespace, rid string) (ConnectorRequest, bool) {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	var r *ConnectorRequest
	for k, c := range ix.connector.byRID {
		if k.key == rid && (namespace == "" || k.namespace == namespace) && (r == nil || k.namespace < r.Namespace) {
			r = c
		}
	}
	if r == nil {
		return ConnectorRequest{}, false
	}
	return ix.withFlow(r), true
}
