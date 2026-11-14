package explorer

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	cb "github.com/hyperledger/fabric-protos-go/common"
)

// Index holds every decoded block of one channel in memory, with the views
// built from them. It is safe for concurrent use. Blocks must be added in
// order; the first block may be any number (a peer that joined from a
// snapshot starts after block 0).
type Index struct {
	txByID  map[string]txLoc
	history map[stateKey][]*KeyWrite
	phylum  map[stateKey]*PhylumVersion
	changed chan struct{}
	channel string
	blocks  []*decodedBlock
	// phylumOrder lists phylum versions in the order they were installed.
	phylumOrder []stateKey
	first       uint64
	mu          sync.RWMutex
}

type txLoc struct {
	block int
	tx    int
}

type stateKey struct {
	namespace string
	key       string
}

// KeyWrite is one valid transaction's write of a key, with the value it
// replaced. Before is nil when the key had no value before the write.
type KeyWrite struct {
	Before *Value `json:"before,omitempty"`
	After  *Value `json:"after,omitempty"`
	TxRef
	IsDelete bool `json:"is_delete,omitempty"`
}

// NewIndex returns an empty index for a channel.
func NewIndex(channel string) *Index {
	return &Index{
		channel: channel,
		txByID:  map[string]txLoc{},
		history: map[stateKey][]*KeyWrite{},
		phylum:  map[stateKey]*PhylumVersion{},
		changed: make(chan struct{}),
	}
}

// Channel returns the channel name.
func (ix *Index) Channel() string { return ix.channel }

// Height returns the number of the next block the index expects, and
// whether it holds any block.
func (ix *Index) Height() (uint64, bool) {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return ix.first + uint64(len(ix.blocks)), len(ix.blocks) > 0
}

// First returns the number of the first block in the index.
func (ix *Index) First() uint64 {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return ix.first
}

// Changed returns a channel that is closed when the next block is added.
func (ix *Index) Changed() <-chan struct{} {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return ix.changed
}

// Add decodes a block and adds it. A block the index already holds is
// ignored, so a source can resend blocks after a reconnect. A gap is an
// error.
func (ix *Index) Add(blk *cb.Block) error {
	d, err := DecodeBlock(blk)
	if err != nil {
		return err
	}
	ix.mu.Lock()
	defer ix.mu.Unlock()
	next := ix.first + uint64(len(ix.blocks))
	switch {
	case len(ix.blocks) == 0:
		ix.first = d.Number
	case d.Number < next:
		return nil
	case d.Number > next:
		return fmt.Errorf("block %d is not the next block %d", d.Number, next)
	}
	bi := len(ix.blocks)
	ix.blocks = append(ix.blocks, d)
	for ti, tx := range d.txs {
		if _, ok := ix.txByID[tx.ID]; !ok && tx.ID != "" {
			ix.txByID[tx.ID] = txLoc{block: bi, tx: ti}
		}
		if tx.Valid {
			ix.applyWrites(tx)
		}
	}
	close(ix.changed)
	ix.changed = make(chan struct{})
	return nil
}

func txRefOf(tx *Transaction) TxRef {
	return TxRef{Time: tx.Time, TxID: tx.ID, Method: tx.Method, BlockNumber: tx.BlockNumber, TxIndex: tx.Index}
}

// applyWrites records a valid transaction's public writes. ix.mu is held.
func (ix *Index) applyWrites(tx *Transaction) {
	ref := txRefOf(tx)
	for _, ns := range tx.RWSets {
		for _, w := range ns.Writes {
			k := stateKey{namespace: ns.Namespace, key: w.Key}
			hist := ix.history[k]
			kw := &KeyWrite{TxRef: ref, IsDelete: w.IsDelete, After: w.Value}
			if n := len(hist); n > 0 {
				kw.Before = hist[n-1].After
			}
			ix.history[k] = append(hist, kw)
			ix.applyPhylumWrite(ns.Namespace, w, ref)
		}
	}
}

// applyPhylumWrite records a write of a phylum description or source.
func (ix *Index) applyPhylumWrite(namespace string, w Write, ref TxRef) {
	switch {
	case strings.HasPrefix(w.Key, phylumDescPrefix):
		version := strings.TrimPrefix(w.Key, phylumDescPrefix)
		k := stateKey{namespace: namespace, key: version}
		pv := ix.phylum[k]
		if w.IsDelete {
			if pv != nil {
				pv.Deleted = true
				pv.Events = append(pv.Events, PhylumEvent{TxRef: ref, Action: "delete"})
			}
			return
		}
		var desc phylumDescription
		_ = json.Unmarshal(w.Value.Bytes(), &desc) // an undecodable description keeps its event, with no status
		action := phylumAction(pv, desc.Status)
		if pv == nil {
			pv = &PhylumVersion{Namespace: namespace, Version: version, Events: []PhylumEvent{}}
			ix.phylum[k] = pv
			ix.phylumOrder = append(ix.phylumOrder, k)
		}
		pv.Deleted = false
		pv.Status = desc.Status
		if f := rawString(desc.Fingerprint); f != "" {
			pv.Fingerprint = f
		}
		if desc.InitTimestamp != "" {
			pv.InitTimestamp = desc.InitTimestamp
		}
		pv.Events = append(pv.Events, PhylumEvent{TxRef: ref, Action: action, Status: desc.Status})
	case strings.HasPrefix(w.Key, phylumSrcPrefix) && !w.IsDelete && w.Value != nil:
		k := stateKey{namespace: namespace, key: strings.TrimPrefix(w.Key, phylumSrcPrefix)}
		if pv := ix.phylum[k]; pv != nil {
			pv.SourceSize = w.Value.Size
		}
	}
}

// Blocks returns up to limit block summaries, newest first, with numbers
// below before (or from the tip when before is 0).
func (ix *Index) Blocks(before uint64, limit int) []BlockSummary {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	end := ix.first + uint64(len(ix.blocks))
	if before != 0 && before < end {
		end = before
	}
	out := []BlockSummary{}
	for n := end; n > ix.first && len(out) < limit; n-- {
		out = append(out, ix.blocks[n-1-ix.first].BlockSummary)
	}
	return out
}

// BlocksFrom returns up to limit blocks from number from, oldest first.
func (ix *Index) BlocksFrom(from uint64, limit int) []Block {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	if from < ix.first {
		from = ix.first
	}
	out := []Block{}
	for n := from; n < ix.first+uint64(len(ix.blocks)) && len(out) < limit; n++ {
		out = append(out, ix.blocks[n-ix.first].Block)
	}
	return out
}

// Block returns a block, or false when the index does not hold it.
func (ix *Index) Block(num uint64) (Block, bool) {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	if num < ix.first || num >= ix.first+uint64(len(ix.blocks)) {
		return Block{}, false
	}
	return ix.blocks[num-ix.first].Block, true
}

// Transaction returns a transaction by ID. For a transaction ID that is in
// more than one block (a duplicate is invalid), it is the first.
func (ix *Index) Transaction(id string) (*Transaction, bool) {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	loc, ok := ix.txByID[id]
	if !ok {
		return nil, false
	}
	return ix.blocks[loc.block].txs[loc.tx], true
}

// TxFilter selects transactions.
type TxFilter struct {
	Method string
	// Valid selects valid (true) or invalid (false) transactions; nil is all.
	Valid *bool
	// Before selects transactions in blocks below this number; 0 is all.
	Before uint64
	Limit  int
}

// Transactions returns transaction summaries, newest first.
func (ix *Index) Transactions(f TxFilter) []TxSummary {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	out := []TxSummary{}
	end := ix.first + uint64(len(ix.blocks))
	if f.Before != 0 && f.Before < end {
		end = f.Before
	}
	for n := end; n > ix.first && len(out) < f.Limit; n-- {
		txs := ix.blocks[n-1-ix.first].Transactions
		for i := len(txs) - 1; i >= 0 && len(out) < f.Limit; i-- {
			tx := txs[i]
			if f.Method != "" && tx.Method != f.Method && !contains(tx.BatchMethods, f.Method) {
				continue
			}
			if f.Valid != nil && tx.Valid != *f.Valid {
				continue
			}
			out = append(out, tx)
		}
	}
	return out
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// KeyHistory returns each valid write of a key, oldest first.
func (ix *Index) KeyHistory(namespace, key string) []*KeyWrite {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return append([]*KeyWrite{}, ix.history[stateKey{namespace: namespace, key: key}]...)
}

// KeyState is a key's row in a key list.
type KeyState struct {
	LastWrite TxRef  `json:"last_write"`
	Namespace string `json:"namespace"`
	Key       string `json:"key"`
	// Space is the framework key space, or "" for a phylum key.
	Space    string `json:"space,omitempty"`
	Writes   int    `json:"writes"`
	Size     int    `json:"size"`
	IsDelete bool   `json:"is_delete,omitempty"`
}

// KeyFilter selects keys.
type KeyFilter struct {
	Namespace string
	Prefix    string
	// Kind is "phylum", "framework" or "" for both.
	Kind string
	// Space selects one framework key space.
	Space string
	// After returns keys that sort after this namespace and key, for paging.
	AfterNamespace string
	AfterKey       string
	Limit          int
}

// Keys returns the keys that valid transactions wrote, sorted by namespace
// and key, with their last write.
func (ix *Index) Keys(f KeyFilter) []KeyState {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	var keys []stateKey
	for k := range ix.history {
		if f.Namespace != "" && k.namespace != f.Namespace {
			continue
		}
		if !strings.HasPrefix(k.key, f.Prefix) {
			continue
		}
		if f.AfterNamespace != "" || f.AfterKey != "" {
			if k.namespace < f.AfterNamespace || (k.namespace == f.AfterNamespace && k.key <= f.AfterKey) {
				continue
			}
		}
		space := frameworkSpaceOf(k.key)
		switch {
		case f.Kind == "phylum" && space != "":
			continue
		case f.Kind == "framework" && space == "":
			continue
		case f.Space != "" && space != f.Space:
			continue
		}
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].namespace != keys[j].namespace {
			return keys[i].namespace < keys[j].namespace
		}
		return keys[i].key < keys[j].key
	})
	if f.Limit > 0 && len(keys) > f.Limit {
		keys = keys[:f.Limit]
	}
	out := make([]KeyState, 0, len(keys))
	for _, k := range keys {
		out = append(out, ix.keyState(k))
	}
	return out
}

func (ix *Index) keyState(k stateKey) KeyState {
	hist := ix.history[k]
	last := hist[len(hist)-1]
	ks := KeyState{
		Namespace: k.namespace,
		Key:       k.key,
		Space:     frameworkSpaceOf(k.key),
		Writes:    len(hist),
		LastWrite: last.TxRef,
		IsDelete:  last.IsDelete,
	}
	if last.After != nil {
		ks.Size = last.After.Size
	}
	return ks
}

// SpaceSummary counts the keys and writes in a framework key space.
type SpaceSummary struct {
	FrameworkSpace
	Keys     int `json:"keys"`
	LiveKeys int `json:"live_keys"`
	Writes   int `json:"writes"`
}

// FrameworkSummary counts the keys and writes in each framework key space,
// and in phylum keys (space "").
func (ix *Index) FrameworkSummary() (spaces []SpaceSummary, phylum SpaceSummary) {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	bySpace := map[string]*SpaceSummary{}
	for _, s := range frameworkSpaces {
		bySpace[s.Prefix] = &SpaceSummary{FrameworkSpace: s}
	}
	phylum = SpaceSummary{FrameworkSpace: FrameworkSpace{Owner: "phylum"}}
	for k, hist := range ix.history {
		var sum *SpaceSummary
		if space := frameworkSpaceOf(k.key); space == "" {
			sum = &phylum
		} else {
			sum = bySpace[space]
		}
		sum.Keys++
		sum.Writes += len(hist)
		if !hist[len(hist)-1].IsDelete {
			sum.LiveKeys++
		}
	}
	for _, s := range frameworkSpaces {
		spaces = append(spaces, *bySpace[s.Prefix])
	}
	return spaces, phylum
}

// PhylumVersions returns each phylum version in the order it was first
// installed.
func (ix *Index) PhylumVersions() []PhylumVersion {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	out := make([]PhylumVersion, 0, len(ix.phylumOrder))
	for _, k := range ix.phylumOrder {
		pv := *ix.phylum[k]
		pv.Events = append([]PhylumEvent{}, pv.Events...)
		out = append(out, pv)
	}
	return out
}
