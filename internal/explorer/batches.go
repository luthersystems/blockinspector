package explorer

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Substrate batch keys (luthersystems/substrate
// internal/substrate/shiro/libframework/batch.go, "Storage"):
//
//	luther:batch:queue:<name>                   "1": init created the queue
//	luther:batch:item:<name>~<time>~<item id>   "1": a prepared item
//	luther:batch:pending:<name>~<batch id>      a JSON list of item ids
//	luther:batchreq:<request id>                {"message": <request>}
//
// <time> is 16 lower-case hex digits of the due time in Unix seconds plus
// 2^63. <name>, <item id> and <batch id> are escaped: any byte outside
// A-Z, a-z, 0-9, ".", "_" and "-" is "%" and two upper-case hex digits.
const (
	batchQueueRoot   = "luther:batch:queue:"
	batchItemRoot    = "luther:batch:item:"
	batchPendingRoot = "luther:batch:pending:"
	batchRequestRoot = "luther:batchreq:"
	batchSep         = "~"
	// batchPausedProperty is the app control property that pauses batch
	// processing, in the map at appControlKey.
	batchPausedProperty = "BATCHING_PAUSED"
	appControlKey       = "luther:app_control"
	// batchCollectMethod is the method that collects due items.
	batchCollectMethod = "batch_get_requests"
	// batchFinishedLimit caps the finished items a queue page lists.
	batchFinishedLimit = 200
)

// batchHexDigits are the digits substrate's batchEscape writes.
const batchHexDigits = "0123456789ABCDEF"

// batchUnescape reverses substrate's batchEscape (and refuses what
// substrate's batchUnescape refuses).
func batchUnescape(s string) (string, bool) {
	if !strings.Contains(s, "%") {
		return s, true
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '%' {
			b.WriteByte(s[i])
			continue
		}
		if i+2 >= len(s) {
			return "", false
		}
		hi := strings.IndexByte(batchHexDigits, s[i+1])
		lo := strings.IndexByte(batchHexDigits, s[i+2])
		if hi < 0 || lo < 0 {
			return "", false
		}
		b.WriteByte(byte(hi<<4 | lo)) //nolint:gosec // G115: two hex digits, 0 to 255
		i += 2
	}
	return b.String(), true
}

// batchItemKey splits an item key into its queue, due time and item id.
func batchItemKey(key string) (queue string, due time.Time, id string, ok bool) {
	s, ok := strings.CutPrefix(key, batchItemRoot)
	if !ok {
		return "", time.Time{}, "", false
	}
	parts := strings.Split(s, batchSep)
	if len(parts) != 3 || len(parts[1]) != 16 {
		return "", time.Time{}, "", false
	}
	t, err := strconv.ParseUint(parts[1], 16, 64)
	if err != nil {
		return "", time.Time{}, "", false
	}
	q, ok1 := batchUnescape(parts[0])
	i, ok2 := batchUnescape(parts[2])
	if !ok1 || !ok2 {
		return "", time.Time{}, "", false
	}
	return q, time.Unix(int64(t^(1<<63)), 0).UTC(), i, true //nolint:gosec // G115: the key stores the time plus 2^63
}

// batchPendingKey splits a pending batch key into its queue and batch id.
func batchPendingKey(key string) (queue, batch string, ok bool) {
	s, ok := strings.CutPrefix(key, batchPendingRoot)
	if !ok {
		return "", "", false
	}
	q, b, ok := strings.Cut(s, batchSep)
	if !ok || strings.Contains(b, batchSep) {
		return "", "", false
	}
	q, ok1 := batchUnescape(q)
	b, ok2 := batchUnescape(b)
	return q, b, ok1 && ok2
}

// batchQueueKey returns the queue of a queue record key.
func batchQueueKey(key string) (string, bool) {
	s, ok := strings.CutPrefix(key, batchQueueRoot)
	if !ok {
		return "", false
	}
	q, ok := batchUnescape(s)
	return q, ok && q != ""
}

// BatchQueue is a summary of one batch queue.
type BatchQueue struct {
	// OldestDue is the due time of the earliest prepared item.
	OldestDue *time.Time `json:"oldest_due,omitempty"`
	// Paused is true when app control pauses batching on the namespace,
	// nil when its app control does not decode.
	Paused    *bool  `json:"paused,omitempty"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	// Prepared counts prepared items; Due those due at the time of the
	// latest block.
	Prepared       int `json:"prepared"`
	Due            int `json:"due"`
	PendingBatches int `json:"pending_batches"`
	PendingItems   int `json:"pending_items"`
	// Created is true when the queue record exists.
	Created bool `json:"created"`
}

// BatchItem is a prepared item.
type BatchItem struct {
	Due       time.Time `json:"due"`
	ID        string    `json:"id"`
	Key       string    `json:"key"`
	Scheduled TxRef     `json:"scheduled"`
	// HasRequest says whether the item has its request record. The record
	// is not shown: a request can hold personal data.
	HasRequest bool `json:"has_request"`
	IsDue      bool `json:"is_due"`
}

// BatchPending is a batch a processor took and has not answered.
type BatchPending struct {
	ID    string   `json:"id"`
	Key   string   `json:"key"`
	Items []string `json:"items"`
	Taken TxRef    `json:"taken"`
	// Undecodable is true when the item list is not a JSON list of strings.
	Undecodable bool `json:"undecodable,omitempty"`
}

// BatchFinished is an item that left the queue.
type BatchFinished struct {
	Due     time.Time `json:"due"`
	ID      string    `json:"id"`
	Key     string    `json:"key"`
	Removed TxRef     `json:"removed"`
	// Outcome is "collected" (Batch names the batch), "skipped" (a
	// collection removed it because its request record was missing or not
	// a JSON object) or "removed" (any other removal, such as reinit).
	Outcome string `json:"outcome"`
	Batch   string `json:"batch,omitempty"`
}

// BatchQueuePage is a queue with its items and pending batches.
type BatchQueuePage struct {
	// AsOf is the time of the latest block, which decides what is due.
	AsOf *time.Time `json:"as_of,omitempty"`
	BatchQueue
	Items    []BatchItem     `json:"items"`
	Pending  []BatchPending  `json:"pending"`
	Finished []BatchFinished `json:"finished"`
}

// latestTime returns the time of the latest block that has one. ix.mu is
// held.
func (ix *Index) latestTime() *time.Time {
	for i := len(ix.blocks) - 1; i >= 0; i-- {
		if t := ix.blocks[i].Time; t != nil {
			return t
		}
	}
	return nil
}

// batchPaused reads the paused property of a namespace's app control.
// ix.mu is held.
func (ix *Index) batchPaused(namespace string) *bool {
	w := ix.liveWrite(stateKey{namespace: namespace, key: appControlKey})
	f := false
	if w == nil {
		return &f
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(w.After.Bytes(), &m) != nil {
		return nil
	}
	v := strings.TrimSpace(string(m[batchPausedProperty]))
	p := v != "" && v != "null" && v != "false"
	return &p
}

type batchQueueID struct{ namespace, name string }

// batchScan collects the live batch state of the queues that match:
// namespace and name empty match every queue. With finished set it also
// collects each queue's finished items. It returns the number of live keys
// under luther:batch: that do not decode. ix.mu is held.
func (ix *Index) batchScan(namespace, name string, finished bool) (map[batchQueueID]*BatchQueuePage, int) {
	asOf := ix.latestTime()
	queues := map[batchQueueID]*BatchQueuePage{}
	match := func(ns, q string) bool {
		return (namespace == "" || ns == namespace) && (name == "" || q == name)
	}
	get := func(ns, q string) *BatchQueuePage {
		id := batchQueueID{ns, q}
		p := queues[id]
		if p == nil {
			p = &BatchQueuePage{
				AsOf:       asOf,
				BatchQueue: BatchQueue{Namespace: ns, Name: q},
				Items:      []BatchItem{}, Pending: []BatchPending{}, Finished: []BatchFinished{},
			}
			queues[id] = p
		}
		return p
	}
	undecodable := 0
	for k, hist := range ix.history {
		if !strings.HasPrefix(k.key, "luther:batch:") {
			continue
		}
		w := ix.liveWrite(k)
		if q, ok := batchQueueKey(k.key); ok {
			if w != nil && match(k.namespace, q) {
				get(k.namespace, q).Created = true
			}
		} else if q, due, id, ok := batchItemKey(k.key); ok {
			if !match(k.namespace, q) {
				continue
			}
			if w != nil {
				it := BatchItem{Due: due, ID: id, Key: k.key, Scheduled: w.TxRef}
				it.IsDue = asOf != nil && !due.After(*asOf)
				it.HasRequest = ix.liveWrite(stateKey{namespace: k.namespace, key: batchRequestRoot + id}) != nil
				p := get(k.namespace, q)
				p.Items = append(p.Items, it)
			}
			if finished {
				for _, d := range hist {
					if d.IsDelete {
						p := get(k.namespace, q)
						p.Finished = append(p.Finished, ix.batchFinished(k.namespace, q, id, due, k.key, d))
					}
				}
			}
		} else if q, batch, ok := batchPendingKey(k.key); ok {
			if w != nil && match(k.namespace, q) {
				b := BatchPending{ID: batch, Key: k.key, Taken: w.TxRef, Items: []string{}}
				if json.Unmarshal(w.After.Bytes(), &b.Items) != nil || b.Items == nil {
					b.Items, b.Undecodable = []string{}, true
				}
				p := get(k.namespace, q)
				p.Pending = append(p.Pending, b)
			}
		} else if w != nil {
			undecodable++
		}
	}
	paused := map[string]*bool{}
	for _, p := range queues {
		p.Prepared = len(p.Items)
		for _, it := range p.Items {
			if it.IsDue {
				p.Due++
			}
			if p.OldestDue == nil || it.Due.Before(*p.OldestDue) {
				d := it.Due
				p.OldestDue = &d
			}
		}
		p.PendingBatches = len(p.Pending)
		for _, b := range p.Pending {
			p.PendingItems += len(b.Items)
		}
		v, ok := paused[p.Namespace]
		if !ok {
			v = ix.batchPaused(p.Namespace)
			paused[p.Namespace] = v
		}
		p.Paused = v
	}
	return queues, undecodable
}

// BatchQueues returns each batch queue, by name, and the number of live
// keys under luther:batch: that do not decode.
func (ix *Index) BatchQueues() ([]BatchQueue, int) {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	queues, undecodable := ix.batchScan("", "", false)
	out := make([]BatchQueue, 0, len(queues))
	for _, q := range queues {
		out = append(out, q.BatchQueue)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Namespace < out[j].Namespace
	})
	return out, undecodable
}

// BatchQueue returns one queue with its prepared items (earliest due
// first), its pending batches and the items that left it (latest first, at
// most batchFinishedLimit). With namespace empty, the first namespace that
// has the queue is used.
func (ix *Index) BatchQueue(namespace, name string) (BatchQueuePage, bool) {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	queues, _ := ix.batchScan(namespace, name, true)
	var page *BatchQueuePage
	for _, q := range queues {
		if page == nil || q.Namespace < page.Namespace {
			page = q
		}
	}
	if page == nil {
		return BatchQueuePage{}, false
	}
	sort.Slice(page.Items, func(i, j int) bool {
		a, b := page.Items[i], page.Items[j]
		if !a.Due.Equal(b.Due) {
			return a.Due.Before(b.Due)
		}
		return a.Key < b.Key
	})
	sort.Slice(page.Pending, func(i, j int) bool { return txBefore(page.Pending[i].Taken, page.Pending[j].Taken) })
	f := page.Finished
	sort.Slice(f, func(i, j int) bool {
		if txBefore(f[j].Removed, f[i].Removed) {
			return true
		}
		if txBefore(f[i].Removed, f[j].Removed) {
			return false
		}
		return f[i].Key < f[j].Key
	})
	if len(f) > batchFinishedLimit {
		page.Finished = f[:batchFinishedLimit]
	}
	return *page, true
}

// txBefore orders transaction references by block and index.
func txBefore(a, b TxRef) bool {
	if a.BlockNumber != b.BlockNumber {
		return a.BlockNumber < b.BlockNumber
	}
	return a.TxIndex < b.TxIndex
}

// batchFinished describes the delete d of an item key. ix.mu is held.
func (ix *Index) batchFinished(namespace, queue, id string, due time.Time, key string, d *KeyWrite) BatchFinished {
	f := BatchFinished{Due: due, ID: id, Key: key, Removed: d.TxRef, Outcome: "removed"}
	tx := ix.txLocked(d.TxID)
	if tx == nil {
		return f
	}
	if batch, ok := collectedInto(tx, namespace, queue, id); ok {
		f.Outcome, f.Batch = "collected", batch
		return f
	}
	// A collection deletes an item it does not deliver only when its
	// request record is missing or not a JSON object. Only a transaction
	// that calls nothing but batch_get_requests is read that way: in a
	// JSON-RPC batch with another call (such as a reinit), the delete is
	// "removed".
	methods := tx.BatchMethods
	if len(methods) == 0 {
		methods = []string{tx.Method}
	}
	only := true
	for _, m := range methods {
		only = only && m == batchCollectMethod
	}
	if only {
		f.Outcome = "skipped"
	}
	return f
}

// collectedInto returns the pending batch of the queue that tx wrote with
// item id in it.
func collectedInto(tx *Transaction, namespace, queue, id string) (string, bool) {
	for _, ns := range tx.RWSets {
		if ns.Namespace != namespace {
			continue
		}
		for _, w := range ns.Writes {
			q, batch, ok := batchPendingKey(w.Key)
			if !ok || q != queue || w.IsDelete {
				continue
			}
			var items []string
			if json.Unmarshal(w.Value.Bytes(), &items) == nil && contains(items, id) {
				return batch, true
			}
		}
	}
	return "", false
}

// txLocked returns a transaction by ID. ix.mu is held.
func (ix *Index) txLocked(id string) *Transaction {
	loc, ok := ix.txByID[id]
	if !ok {
		return nil
	}
	return ix.blocks[loc.block].txs[loc.tx]
}
