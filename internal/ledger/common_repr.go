package ledger

import (
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"sort"

	"github.com/olekukonko/tablewriter"
	"github.com/olekukonko/tablewriter/tw"
)

// KeyRecord represents a single key-value record in the ledger
type KeyRecord struct {
	Type        string // "state" or "private"
	Namespace   string // namespace for state, collection for private
	Key         string
	Value       []byte
	ValueSize   int
	Version     *Version // optional, for read sets
	IsRead      bool     // true if this is from a read set
	IsWrite     bool     // true if this is from a write set or current state
	Transaction string   // optional transaction ID
	BlockNum    uint64   // optional block number
}

// LedgerView represents a unified view of ledger data
// that can be populated from either Fabric blocks or Badger snapshots
type LedgerView struct {
	Keys       []*KeyRecord
	Statistics *LedgerStatistics
}

// LedgerStatistics contains aggregated statistics about the ledger
type LedgerStatistics struct {
	TotalKeys        int
	TotalStateKeys   int
	TotalPrivateKeys int
	TotalSize        int64
	TotalStateSize   int64
	TotalPrivateSize int64
	Namespaces       map[string]int
	Collections      map[string]int
	BlockCount       uint64
	TransactionCount int
}

// NewLedgerView creates a new empty ledger view
func NewLedgerView() *LedgerView {
	return &LedgerView{
		Keys: []*KeyRecord{},
		Statistics: &LedgerStatistics{
			Namespaces:  make(map[string]int),
			Collections: make(map[string]int),
		},
	}
}

// AddKey adds a key record to the view
func (lv *LedgerView) AddKey(key *KeyRecord) {
	if key == nil {
		return
	}
	lv.Keys = append(lv.Keys, key)
	lv.updateStatistics(key)
}

// updateStatistics updates statistics based on a new key
func (lv *LedgerView) updateStatistics(key *KeyRecord) {
	lv.Statistics.TotalKeys++
	lv.Statistics.TotalSize += int64(key.ValueSize)

	if key.Type == "state" {
		lv.Statistics.TotalStateKeys++
		lv.Statistics.TotalStateSize += int64(key.ValueSize)
		if key.Namespace != "" {
			lv.Statistics.Namespaces[key.Namespace]++
		}
	} else if key.Type == "private" {
		lv.Statistics.TotalPrivateKeys++
		lv.Statistics.TotalPrivateSize += int64(key.ValueSize)
		if key.Namespace != "" { // namespace is collection for private keys
			lv.Statistics.Collections[key.Namespace]++
		}
	}
}

// Write writes a formatted summary of the ledger view
func (lv *LedgerView) Write(w io.Writer) error {
	if lv == nil || w == nil {
		return nil
	}

	stats := lv.Statistics

	// Write summary statistics
	summary := [][]string{
		{"Total Keys", fmt.Sprintf("%d", stats.TotalKeys)},
		{"State Keys", fmt.Sprintf("%d", stats.TotalStateKeys)},
		{"Private Keys", fmt.Sprintf("%d", stats.TotalPrivateKeys)},
		{"Total Size (bytes)", fmt.Sprintf("%d", stats.TotalSize)},
		{"State Size (bytes)", fmt.Sprintf("%d", stats.TotalStateSize)},
		{"Private Size (bytes)", fmt.Sprintf("%d", stats.TotalPrivateSize)},
	}
	if stats.BlockCount > 0 {
		summary = append(summary, []string{"Blocks", fmt.Sprintf("%d", stats.BlockCount)})
	}
	if stats.TransactionCount > 0 {
		summary = append(summary, []string{"Transactions", fmt.Sprintf("%d", stats.TransactionCount)})
	}
	if err := writeTable(w, "Ledger Summary", []any{"Metric", "Value"}, summary); err != nil {
		return err
	}

	// Write namespace statistics
	if len(stats.Namespaces) > 0 {
		var rows [][]string
		for _, ns := range sortedKeys(stats.Namespaces) {
			rows = append(rows, []string{ns, fmt.Sprintf("%d", stats.Namespaces[ns])})
		}
		if err := writeTable(w, "State Namespaces", []any{"Namespace", "Key Count"}, rows); err != nil {
			return err
		}
	}

	// Write collection statistics
	if len(stats.Collections) > 0 {
		var rows [][]string
		for _, coll := range sortedKeys(stats.Collections) {
			rows = append(rows, []string{coll, fmt.Sprintf("%d", stats.Collections[coll])})
		}
		if err := writeTable(w, "Private Data Collections", []any{"Collection", "Key Count"}, rows); err != nil {
			return err
		}
	}

	// Group and write keys by namespace/collection
	keysByNamespace := make(map[string]map[string][]*KeyRecord) // type -> namespace -> keys
	for _, key := range lv.Keys {
		if keysByNamespace[key.Type] == nil {
			keysByNamespace[key.Type] = make(map[string][]*KeyRecord)
		}
		ns := key.Namespace
		if ns == "" {
			ns = "<default>"
		}
		keysByNamespace[key.Type][ns] = append(keysByNamespace[key.Type][ns], key)
	}

	// Write state keys
	if stateKeys, ok := keysByNamespace["state"]; ok {
		for _, namespace := range sortedMapKeys(stateKeys) {
			var rows [][]string
			for _, key := range stateKeys[namespace] {
				read := ""
				write := ""
				if key.IsRead {
					read = "✓"
				}
				if key.IsWrite {
					write = "✓"
				}
				rows = append(rows, []string{key.Key, fmt.Sprintf("%d", key.ValueSize), read, write})
			}
			caption := fmt.Sprintf("State Keys (namespace=[%s])", namespace)
			if err := writeTable(w, caption, []any{"Key", "Value Size (bytes)", "Read", "Write"}, rows); err != nil {
				return err
			}
		}
	}

	// Write private keys
	if privateKeys, ok := keysByNamespace["private"]; ok {
		for _, collection := range sortedMapKeys(privateKeys) {
			var rows [][]string
			for _, key := range privateKeys[collection] {
				rows = append(rows, []string{key.Key, fmt.Sprintf("%d", key.ValueSize)})
			}
			caption := fmt.Sprintf("Private Data Keys (collection=[%s])", collection)
			if err := writeTable(w, caption, []any{"Key", "Value Size (bytes)"}, rows); err != nil {
				return err
			}
		}
	}
	return nil
}

// writeTable renders one captioned table and the blank line after it. It
// returns the first error from tablewriter or the writer, so a failed write
// to stdout (a closed pipe, a full disk) is reported instead of dropped.
func writeTable(w io.Writer, caption string, header []any, rows [][]string) error {
	table := tablewriter.NewWriter(w)
	table.Header(header...)
	if caption != "" {
		table.Caption(tw.Caption{Text: caption})
	}
	for _, row := range rows {
		if err := table.Append(row); err != nil {
			return fmt.Errorf("table %q: %w", caption, err)
		}
	}
	if err := table.Render(); err != nil {
		return fmt.Errorf("table %q: %w", caption, err)
	}
	_, err := fmt.Fprintln(w)
	return err
}

func (lv *LedgerView) WriteCSV(w *csv.Writer) {
	if lv == nil || w == nil {
		return
	}

	for _, key := range lv.Keys {
		row := []string{
			key.Type,
			key.Namespace,
			key.Key,
			fmt.Sprintf("%d", key.ValueSize),
			fmt.Sprintf("%v", key.IsRead),
			fmt.Sprintf("%v", key.IsWrite),
			key.Transaction,
			fmt.Sprintf("%d", key.BlockNum),
		}
		if err := w.Write(row); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to write CSV row: %v\n", err)
		}
	}
}

func sortedKeys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedMapKeys(m map[string][]*KeyRecord) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// GetCSVLedgerViewHeader returns the CSV header for ledger view data
func GetCSVLedgerViewHeader() []string {
	return []string{"type", "namespace_or_collection", "key", "value_size_bytes", "is_read", "is_write", "tx_id", "block_num"}
}
