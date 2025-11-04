package ledger

import (
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"strings"

	badger "github.com/dgraph-io/badger/v4"
	"github.com/olekukonko/tablewriter"
	"github.com/olekukonko/tablewriter/tw"
	"github.com/sirupsen/logrus"
)

const (
	bucketPrefixState   = "state"
	bucketPrefixPrivate = "private"
)

// SnapshotState represents the state of a Luther snapshot ledger
type SnapshotState struct {
	StateKeys   []*StateKey
	PrivateKeys []*PrivateKey
	stats       *SnapshotStats
}

// StateKey represents a state key in the snapshot
type StateKey struct {
	Namespace string
	Key       string
	Value     []byte
}

// PrivateKey represents a private data key in the snapshot
type PrivateKey struct {
	Collection string
	Key        string
	Value      []byte
}

// SnapshotStats contains statistics about the snapshot
type SnapshotStats struct {
	TotalStateKeys   int
	TotalPrivateKeys int
	TotalStateSize   int64
	TotalPrivateSize int64
	Namespaces       map[string]int
	Collections      map[string]int
}

// LoadSnapshot loads a Luther snapshot from an io.Reader
func LoadSnapshot(r io.Reader) (*SnapshotState, error) {
	log := logrus.New()
	log.SetLevel(logrus.WarnLevel)
	opts := badger.DefaultOptions("").
		WithInMemory(true).
		WithLogger(log).
		WithBaseTableSize(8 << 20)

	db, err := badger.Open(opts)
	if err != nil {
		return nil, fmt.Errorf("failed to open badger db: %w", err)
	}
	defer db.Close()

	// Load snapshot data
	err = db.Load(r, 1)
	if err != nil {
		return nil, fmt.Errorf("failed to load snapshot: %w", err)
	}

	state := &SnapshotState{
		StateKeys:   []*StateKey{},
		PrivateKeys: []*PrivateKey{},
		stats: &SnapshotStats{
			Namespaces:  make(map[string]int),
			Collections: make(map[string]int),
		},
	}

	// Iterate through all keys
	err = db.View(func(txn *badger.Txn) error {
		it := txn.NewIterator(badger.DefaultIteratorOptions)
		defer it.Close()

		for it.Rewind(); it.Valid(); it.Next() {
			item := it.Item()
			k := item.KeyCopy(nil)
			val, err := item.ValueCopy(nil)
			if err != nil {
				return fmt.Errorf("failed to copy value: %w", err)
			}

			// Parse key based on prefix
			parts := strings.SplitN(string(k), "\x00", 3)
			if len(parts) != 3 {
				// Skip keys that don't match expected format
				continue
			}

			prefix := parts[0]
			bucket := parts[1]
			key := parts[2]

			switch prefix {
			case bucketPrefixState:
				state.StateKeys = append(state.StateKeys, &StateKey{
					Namespace: bucket,
					Key:       key,
					Value:     val,
				})
				state.stats.TotalStateKeys++
				state.stats.TotalStateSize += int64(len(val))
				state.stats.Namespaces[bucket]++

			case bucketPrefixPrivate:
				state.PrivateKeys = append(state.PrivateKeys, &PrivateKey{
					Collection: bucket,
					Key:        key,
					Value:      val,
				})
				state.stats.TotalPrivateKeys++
				state.stats.TotalPrivateSize += int64(len(val))
				state.stats.Collections[bucket]++
			}
		}

		return nil
	})

	if err != nil {
		return nil, err
	}

	state.stats = state.stats
	return state, nil
}

// LoadSnapshotFromFile loads a snapshot from a file path
func LoadSnapshotFromFile(filePath string) (*SnapshotState, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open snapshot file: %w", err)
	}
	defer file.Close()

	return LoadSnapshot(file)
}

// GetStats returns statistics about the snapshot
func (s *SnapshotState) GetStats() *SnapshotStats {
	return s.stats
}

// ToLedgerView converts a SnapshotState to a LedgerView
func (s *SnapshotState) ToLedgerView() *LedgerView {
	if s == nil {
		return NewLedgerView()
	}

	view := NewLedgerView()

	// Convert state keys
	for _, stateKey := range s.StateKeys {
		view.AddKey(&KeyRecord{
			Type:      "state",
			Namespace: stateKey.Namespace,
			Key:       stateKey.Key,
			Value:     stateKey.Value,
			ValueSize: len(stateKey.Value),
			IsRead:    false, // snapshots show current state, not read/write operations
			IsWrite:   true,  // current state implies it was written at some point
		})
	}

	// Convert private keys
	for _, privateKey := range s.PrivateKeys {
		view.AddKey(&KeyRecord{
			Type:      "private",
			Namespace: privateKey.Collection,
			Key:       privateKey.Key,
			Value:     privateKey.Value,
			ValueSize: len(privateKey.Value),
			IsRead:    false,
			IsWrite:   true,
		})
	}

	return view
}

// Write writes a formatted summary of the snapshot to the writer
func (s *SnapshotState) Write(w io.Writer) {
	if s == nil || w == nil {
		return
	}

	// Use common representation for consistency
	view := s.ToLedgerView()
	view.Write(w)
}

// WriteLegacy writes the original snapshot format (kept for compatibility)
func (s *SnapshotState) WriteLegacy(w io.Writer) {
	if s == nil || w == nil {
		return
	}

	stats := s.GetStats()

	// Write summary statistics
	table := tablewriter.NewWriter(w)
	table.Header("Metric", "Value")
	table.Caption(tw.Caption{Text: "Snapshot Summary"})
	table.Append([]string{"Total State Keys", fmt.Sprintf("%d", stats.TotalStateKeys)})
	table.Append([]string{"Total Private Keys", fmt.Sprintf("%d", stats.TotalPrivateKeys)})
	table.Append([]string{"Total State Size (bytes)", fmt.Sprintf("%d", stats.TotalStateSize)})
	table.Append([]string{"Total Private Size (bytes)", fmt.Sprintf("%d", stats.TotalPrivateSize)})
	table.Render()
	fmt.Fprintln(w)

	// Write namespace statistics
	if len(stats.Namespaces) > 0 {
		table = tablewriter.NewWriter(w)
		table.Header("Namespace", "Key Count")
		table.Caption(tw.Caption{Text: "State Namespaces"})
		for ns, count := range stats.Namespaces {
			table.Append([]string{ns, fmt.Sprintf("%d", count)})
		}
		table.Render()
		fmt.Fprintln(w)
	}

	// Write collection statistics
	if len(stats.Collections) > 0 {
		table = tablewriter.NewWriter(w)
		table.Header("Collection", "Key Count")
		table.Caption(tw.Caption{Text: "Private Data Collections"})
		for coll, count := range stats.Collections {
			table.Append([]string{coll, fmt.Sprintf("%d", count)})
		}
		table.Render()
		fmt.Fprintln(w)
	}

	// Write state keys grouped by namespace
	if len(s.StateKeys) > 0 {
		// Group keys by namespace
		keysByNamespace := make(map[string][]*StateKey)
		for _, stateKey := range s.StateKeys {
			keysByNamespace[stateKey.Namespace] = append(keysByNamespace[stateKey.Namespace], stateKey)
		}

		// Write each namespace group
		for namespace, keys := range keysByNamespace {
			table = tablewriter.NewWriter(w)
			table.Header("Key", "Value Size (bytes)")
			table.Caption(tw.Caption{Text: fmt.Sprintf("State Keys (namespace=[%s])", namespace)})
			for _, stateKey := range keys {
				table.Append([]string{stateKey.Key, fmt.Sprintf("%d", len(stateKey.Value))})
			}
			table.Render()
			fmt.Fprintln(w)
		}
	}

	// Write private keys grouped by collection
	if len(s.PrivateKeys) > 0 {
		// Group keys by collection
		keysByCollection := make(map[string][]*PrivateKey)
		for _, privateKey := range s.PrivateKeys {
			keysByCollection[privateKey.Collection] = append(keysByCollection[privateKey.Collection], privateKey)
		}

		// Write each collection group
		for collection, keys := range keysByCollection {
			table = tablewriter.NewWriter(w)
			table.Header("Key", "Value Size (bytes)")
			table.Caption(tw.Caption{Text: fmt.Sprintf("Private Data Keys (collection=[%s])", collection)})
			for _, privateKey := range keys {
				table.Append([]string{privateKey.Key, fmt.Sprintf("%d", len(privateKey.Value))})
			}
			table.Render()
			fmt.Fprintln(w)
		}
	}
}

// WriteCSV writes snapshot data in CSV format
func (s *SnapshotState) WriteCSV(w *csv.Writer) {
	if s == nil || w == nil {
		return
	}

	// Use common representation for consistency
	view := s.ToLedgerView()
	view.WriteCSV(w)
}

// GetCSVHeader returns the CSV header for snapshot data
func GetCSVSnapshotHeader() []string {
	return []string{"type", "namespace_or_collection", "key", "value_size_bytes"}
}

// SnapshotIterator provides an iterator interface for snapshot keys
type SnapshotIterator struct {
	stateKeys   []*StateKey
	privateKeys []*PrivateKey
	stateIdx    int
	privateIdx  int
}

// Next returns the next key-value pair, alternating between state and private keys
func (it *SnapshotIterator) Next() (*StateKey, *PrivateKey) {
	if it.stateIdx < len(it.stateKeys) {
		key := it.stateKeys[it.stateIdx]
		it.stateIdx++
		return key, nil
	}
	if it.privateIdx < len(it.privateKeys) {
		key := it.privateKeys[it.privateIdx]
		it.privateIdx++
		return nil, key
	}
	return nil, nil
}

// HasNext returns true if there are more keys
func (it *SnapshotIterator) HasNext() bool {
	return it.stateIdx < len(it.stateKeys) || it.privateIdx < len(it.privateKeys)
}

// NewSnapshotIterator creates a new iterator for the snapshot
func (s *SnapshotState) NewSnapshotIterator() *SnapshotIterator {
	return &SnapshotIterator{
		stateKeys:   s.StateKeys,
		privateKeys: s.PrivateKeys,
		stateIdx:    0,
		privateIdx:  0,
	}
}
