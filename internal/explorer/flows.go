package explorer

import (
	"encoding/json"
	"sort"
	"strings"
)

// DefFlow keys (luthersystems/substrate
// internal/substrate/shiro/libflow/flowstore: record.go, parked.go). A run's
// records are in the flow's :store. For a flow with a public store the blocks
// hold them; for a private store (the default) a block holds only hashes, so
// the explorer shows that flow's public keys (manifests and parked rows) and
// counts its private writes.
const (
	flowRunRoot      = "luther:flow:run:"
	flowManifestRoot = "luther:flow:man:"
	flowParkedRoot   = "luther:flow:park:"
	// flowHeaderVersion is the run header format the explorer decodes
	// (flowstore.HeaderVersion). A header of another version is shown raw.
	flowHeaderVersion = 2
)

// flowHeader is the part of a run header (flowstore.Header) that the views
// show. The saved locals, the request envelope and the result are other keys
// and can hold personal data, so the explorer does not read them.
type flowHeader struct {
	Parent    *flowParentRef `json:"parent,omitempty"`
	Error     *flowRunError  `json:"error,omitempty"`
	RetryCap  *int           `json:"retry_cap,omitempty"`
	Flow      string         `json:"flow"`
	ID        string         `json:"id"`
	State     string         `json:"state"`
	Label     string         `json:"label,omitempty"`
	Kind      string         `json:"kind,omitempty"`
	Version   string         `json:"version"`
	Manifest  string         `json:"manifest,omitempty"`
	Phylum    string         `json:"phylum,omitempty"`
	Caller    string         `json:"caller"`
	Child     string         `json:"child,omitempty"`
	ChildFlow string         `json:"child_flow,omitempty"`
	RIDs      []string       `json:"rids,omitempty"`
	From      []string       `json:"from,omitempty"`
	Key       string         `json:"key,omitempty"`
	V         int            `json:"v"`
	Gen       int64          `json:"gen"`
	DueAt     int64          `json:"due_at,omitempty"`
	StartedAt int64          `json:"started_at"`
	UpdatedAt int64          `json:"updated_at"`
	Depth     int            `json:"depth,omitempty"`
	Attempt   int            `json:"attempt,omitempty"`
	Hist      int64          `json:"hist"`
	Inputs    int64          `json:"inputs,omitempty"`
	Pinned    bool           `json:"pinned,omitempty"`
}

type flowParentRef struct {
	Flow   string `json:"flow"`
	Run    string `json:"run"`
	Label  string `json:"label,omitempty"`
	Gen    int64  `json:"gen"`
	Linked bool   `json:"linked,omitempty"`
}

// flowRunError is flowstore.RunError without its data, which can hold
// personal data.
type flowRunError struct {
	Reason   string `json:"reason"`
	Kind     string `json:"kind,omitempty"`
	Label    string `json:"label,omitempty"`
	Message  string `json:"message,omitempty"`
	Attempts int    `json:"attempts,omitempty"`
}

// FlowHistoryEntry is one entry of a run's history (libflow historyRow),
// with the transaction that wrote it.
type FlowHistoryEntry struct {
	At     string `json:"at"`
	Event  string `json:"event"`
	From   string `json:"from"`
	To     string `json:"to"`
	Sender string `json:"sender,omitempty"`
	TxRef
	N   int64 `json:"n"`
	Gen int64 `json:"gen"`
}

// flowParkedRow is flowstore.ParkedRow.
type flowParkedRow struct {
	Outcome *struct {
		Status string `json:"status"`
		Target string `json:"target"`
		Error  string `json:"error,omitempty"`
	} `json:"outcome,omitempty"`
	Label  string `json:"label"`
	Phylum string `json:"phylum,omitempty"`
	Gen    int64  `json:"gen"`
}

// FlowRun is a run as the run list and the run page show it. Header is set
// when the header has the known version; Raw holds a header of another
// version, or one that does not decode.
type FlowRun struct {
	Header    *flowHeader     `json:"header,omitempty"`
	Raw       json.RawMessage `json:"raw,omitempty"`
	Namespace string          `json:"namespace"`
	Flow      string          `json:"flow"`
	ID        string          `json:"id"`
	LastWrite TxRef           `json:"last_write"`
}

// FlowRunPage is a run with its history, children and parked rows.
type FlowRunPage struct {
	FlowRun
	History  []FlowHistoryEntry `json:"history"`
	Children []string           `json:"children"`
	Parked   []FlowParked       `json:"parked"`
	// HasResult and HasVars say whether the run has a result or saved
	// locals. Their values are not shown: they can hold personal data.
	HasResult bool `json:"has_result,omitempty"`
	HasVars   bool `json:"has_vars,omitempty"`
}

// FlowParked is a parked-index row of a run at one flow version.
type FlowParked struct {
	Row     *flowParkedRow  `json:"row,omitempty"`
	Raw     json.RawMessage `json:"raw,omitempty"`
	Version string          `json:"version"`
	Run     string          `json:"run"`
	TxRef
}

// FlowSummary is one flow on one chaincode namespace.
type FlowSummary struct {
	ByState     map[string]int `json:"by_state"`
	ByKind      map[string]int `json:"by_kind"`
	Namespace   string         `json:"namespace"`
	Flow        string         `json:"flow"`
	Manifests   []string       `json:"manifests"`
	Runs        int            `json:"runs"`
	Parked      int            `json:"parked"`
	Undecodable int            `json:"undecodable,omitempty"`
}

// flowRunKey splits a run record key into its flow, kind and the rest
// (luther:flow:run:FLOW:KIND:REST). Flow names and kinds hold no colon.
func flowRunKey(key string) (flow, kind, rest string, ok bool) {
	s, ok := strings.CutPrefix(key, flowRunRoot)
	if !ok {
		return "", "", "", false
	}
	flow, s, ok = strings.Cut(s, ":")
	if !ok || flow == "" {
		return "", "", "", false
	}
	kind, rest, ok = strings.Cut(s, ":")
	if !ok || rest == "" {
		return "", "", "", false
	}
	return flow, kind, rest, true
}

// flowParkedKey splits a parked-row key (luther:flow:park:FLOW:VERSION:RUN).
// The version may hold colons; the run id holds none.
func flowParkedKey(key string) (flow, version, run string, ok bool) {
	s, ok := strings.CutPrefix(key, flowParkedRoot)
	if !ok {
		return "", "", "", false
	}
	flow, s, ok = strings.Cut(s, ":")
	if !ok || flow == "" {
		return "", "", "", false
	}
	i := strings.LastIndexByte(s, ':')
	if i <= 0 || i == len(s)-1 {
		return "", "", "", false
	}
	return flow, s[:i], s[i+1:], true
}

// flowManifestKey splits a manifest key (luther:flow:man:FLOW:HASH).
func flowManifestKey(key string) (flow, hash string, ok bool) {
	s, ok := strings.CutPrefix(key, flowManifestRoot)
	if !ok {
		return "", "", false
	}
	flow, hash, ok = strings.Cut(s, ":")
	return flow, hash, ok && flow != "" && hash != ""
}

// decodeFlowRun decodes a header write into a run.
func decodeFlowRun(namespace, flow, id string, w *KeyWrite) FlowRun {
	run := FlowRun{Namespace: namespace, Flow: flow, ID: id, LastWrite: w.TxRef}
	b := w.After.Bytes()
	var probe struct {
		V int `json:"v"`
	}
	if json.Unmarshal(b, &probe) == nil && probe.V == flowHeaderVersion {
		var h flowHeader
		if json.Unmarshal(b, &h) == nil {
			run.Header = &h
			return run
		}
	}
	if json.Valid(b) {
		run.Raw = json.RawMessage(b)
	} else {
		run.Raw, _ = json.Marshal(string(b))
	}
	return run
}

// liveWrite returns the last write of k when it holds a value.
func (ix *Index) liveWrite(k stateKey) *KeyWrite {
	hist := ix.history[k]
	if len(hist) == 0 {
		return nil
	}
	w := hist[len(hist)-1]
	if w.IsDelete || w.After == nil {
		return nil
	}
	return w
}

// flowRuns returns the live runs of every flow, ix.mu held for reading.
func (ix *Index) flowRuns() []FlowRun {
	var runs []FlowRun
	for k := range ix.history {
		flow, kind, id, ok := flowRunKey(k.key)
		if !ok || kind != "h" {
			continue
		}
		if w := ix.liveWrite(k); w != nil {
			runs = append(runs, decodeFlowRun(k.namespace, flow, id, w))
		}
	}
	sort.Slice(runs, func(i, j int) bool {
		a, b := runs[i].LastWrite, runs[j].LastWrite
		if a.BlockNumber != b.BlockNumber {
			return a.BlockNumber > b.BlockNumber
		}
		if a.TxIndex != b.TxIndex {
			return a.TxIndex > b.TxIndex
		}
		return runs[i].ID < runs[j].ID
	})
	return runs
}

// Flows returns each flow seen in public state: from its manifests, its run
// headers and its parked rows.
func (ix *Index) Flows() []FlowSummary {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	type fk struct{ namespace, flow string }
	flows := map[fk]*FlowSummary{}
	get := func(namespace, flow string) *FlowSummary {
		k := fk{namespace, flow}
		f := flows[k]
		if f == nil {
			f = &FlowSummary{Namespace: namespace, Flow: flow, Manifests: []string{}, ByState: map[string]int{}, ByKind: map[string]int{}}
			flows[k] = f
		}
		return f
	}
	for k := range ix.history {
		if ix.liveWrite(k) == nil {
			continue
		}
		if flow, hash, ok := flowManifestKey(k.key); ok {
			f := get(k.namespace, flow)
			f.Manifests = append(f.Manifests, hash)
		} else if flow, _, _, ok := flowParkedKey(k.key); ok {
			get(k.namespace, flow).Parked++
		}
	}
	for _, r := range ix.flowRuns() {
		f := get(r.Namespace, r.Flow)
		f.Runs++
		if r.Header == nil {
			f.Undecodable++
			continue
		}
		f.ByState[r.Header.State]++
		if r.Header.State == "waiting" && r.Header.Kind != "" {
			f.ByKind[r.Header.Kind]++
		}
	}
	out := make([]FlowSummary, 0, len(flows))
	for _, f := range flows {
		sort.Strings(f.Manifests)
		out = append(out, *f)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Flow != out[j].Flow {
			return out[i].Flow < out[j].Flow
		}
		return out[i].Namespace < out[j].Namespace
	})
	return out
}

// FlowRunFilter selects runs. Empty fields match every run.
type FlowRunFilter struct {
	Namespace string
	Flow      string
	State     string
	Kind      string
	Label     string
	Phylum    string
	Caller    string
	// Query matches a run ID prefix.
	Query string
	Limit int
}

// FlowRuns returns runs that match f, most recently written first.
func (ix *Index) FlowRuns(f FlowRunFilter) []FlowRun {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	out := []FlowRun{}
	for _, r := range ix.flowRuns() {
		if (f.Namespace != "" && r.Namespace != f.Namespace) || (f.Flow != "" && r.Flow != f.Flow) ||
			(f.Query != "" && !strings.HasPrefix(r.ID, f.Query)) {
			continue
		}
		h := r.Header
		if f.State != "" || f.Kind != "" || f.Label != "" || f.Phylum != "" || f.Caller != "" {
			if h == nil || (f.State != "" && h.State != f.State) || (f.Kind != "" && h.Kind != f.Kind) ||
				(f.Label != "" && h.Label != f.Label) || (f.Phylum != "" && h.Phylum != f.Phylum) ||
				(f.Caller != "" && h.Caller != f.Caller) {
				continue
			}
		}
		out = append(out, r)
		if f.Limit > 0 && len(out) >= f.Limit {
			break
		}
	}
	return out
}

// FlowRun returns a run with its history, child links and parked rows. With
// namespace empty, the first namespace that has the run is used.
func (ix *Index) FlowRun(namespace, flow, id string) (FlowRunPage, bool) {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	var page FlowRunPage
	found := false
	for k := range ix.history {
		if namespace != "" && k.namespace != namespace {
			continue
		}
		if f, kind, rest, ok := flowRunKey(k.key); ok && f == flow && kind == "h" && rest == id {
			if w := ix.liveWrite(k); w != nil && (!found || k.namespace < page.Namespace) {
				page.FlowRun = decodeFlowRun(k.namespace, flow, id, w)
				found = true
			}
		}
	}
	if !found {
		return page, false
	}
	ns := page.Namespace
	page.History = []FlowHistoryEntry{}
	page.Children = []string{}
	page.Parked = []FlowParked{}
	for k, hist := range ix.history {
		if k.namespace != ns {
			continue
		}
		if pf, version, run, ok := flowParkedKey(k.key); ok && pf == flow && run == id {
			if w := ix.liveWrite(k); w != nil {
				p := FlowParked{Version: version, Run: run, TxRef: w.TxRef}
				var row flowParkedRow
				if err := json.Unmarshal(w.After.Bytes(), &row); err == nil {
					p.Row = &row
				} else {
					p.Raw, _ = json.Marshal(string(w.After.Bytes()))
				}
				page.Parked = append(page.Parked, p)
			}
			continue
		}
		f, kind, rest, ok := flowRunKey(k.key)
		if !ok || f != flow {
			continue
		}
		switch kind {
		case "hist":
			run, _, ok := strings.Cut(rest, ":")
			if !ok || run != id {
				continue
			}
			// A history entry is written once; take its first write.
			for _, w := range hist {
				if w.IsDelete || w.After == nil {
					continue
				}
				var e FlowHistoryEntry
				if json.Unmarshal(w.After.Bytes(), &e) == nil {
					e.TxRef = w.TxRef
					page.History = append(page.History, e)
				}
				break
			}
		case "c":
			run, child, ok := strings.Cut(rest, ":")
			if ok && run == id && ix.liveWrite(k) != nil {
				page.Children = append(page.Children, child)
			}
		case "r":
			if rest == id && ix.liveWrite(k) != nil {
				page.HasResult = true
			}
		case "v":
			if rest == id && ix.liveWrite(k) != nil {
				page.HasVars = true
			}
		}
	}
	sort.Slice(page.History, func(i, j int) bool { return page.History[i].N < page.History[j].N })
	sort.Strings(page.Children)
	sort.Slice(page.Parked, func(i, j int) bool { return page.Parked[i].Version < page.Parked[j].Version })
	return page, true
}
