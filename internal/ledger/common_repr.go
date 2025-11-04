package ledger

import (
	"encoding/csv"
	"fmt"
	"io"
	"os"

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
func (lv *LedgerView) Write(w io.Writer) {
	if lv == nil || w == nil {
		return
	}

	stats := lv.Statistics

	// Write summary statistics
	table := tablewriter.NewWriter(w)
	table.Header("Metric", "Value")
	table.Caption(tw.Caption{Text: "Ledger Summary"})
	table.Append([]string{"Total Keys", fmt.Sprintf("%d", stats.TotalKeys)})
	table.Append([]string{"State Keys", fmt.Sprintf("%d", stats.TotalStateKeys)})
	table.Append([]string{"Private Keys", fmt.Sprintf("%d", stats.TotalPrivateKeys)})
	table.Append([]string{"Total Size (bytes)", fmt.Sprintf("%d", stats.TotalSize)})
	table.Append([]string{"State Size (bytes)", fmt.Sprintf("%d", stats.TotalStateSize)})
	table.Append([]string{"Private Size (bytes)", fmt.Sprintf("%d", stats.TotalPrivateSize)})
	if stats.BlockCount > 0 {
		table.Append([]string{"Blocks", fmt.Sprintf("%d", stats.BlockCount)})
	}
	if stats.TransactionCount > 0 {
		table.Append([]string{"Transactions", fmt.Sprintf("%d", stats.TransactionCount)})
	}
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
		for namespace, keys := range stateKeys {
			table = tablewriter.NewWriter(w)
			table.Header("Key", "Value Size (bytes)", "Read", "Write")
			table.Caption(tw.Caption{Text: fmt.Sprintf("State Keys (namespace=[%s])", namespace)})
			for _, key := range keys {
				read := ""
				write := ""
				if key.IsRead {
					read = "✓"
				}
				if key.IsWrite {
					write = "✓"
				}
				table.Append([]string{key.Key, fmt.Sprintf("%d", key.ValueSize), read, write})
			}
			table.Render()
			fmt.Fprintln(w)
		}
	}

	// Write private keys
	if privateKeys, ok := keysByNamespace["private"]; ok {
		for collection, keys := range privateKeys {
			table = tablewriter.NewWriter(w)
			table.Header("Key", "Value Size (bytes)")
			table.Caption(tw.Caption{Text: fmt.Sprintf("Private Data Keys (collection=[%s])", collection)})
			for _, key := range keys {
				table.Append([]string{key.Key, fmt.Sprintf("%d", key.ValueSize)})
			}
			table.Render()
			fmt.Fprintln(w)
		}
	}
}

// WriteCSV writes ledger view data in CSV format
func (lv *LedgerView) WriteCSV(w *csv.Writer) {
	if lv == nil || w == nil {
		return
	}

	// Write header
	header := []string{"type", "namespace_or_collection", "key", "value_size_bytes", "is_read", "is_write"}
	if lv.hasTransactionInfo() {
		header = append(header, "tx_id", "block_num")
	}
	if err := w.Write(header); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to write CSV header: %v\n", err)
		return
	}

	// Write keys
	for _, key := range lv.Keys {
		row := []string{
			key.Type,
			key.Namespace,
			key.Key,
			fmt.Sprintf("%d", key.ValueSize),
			fmt.Sprintf("%v", key.IsRead),
			fmt.Sprintf("%v", key.IsWrite),
		}
		if lv.hasTransactionInfo() {
			row = append(row, key.Transaction, fmt.Sprintf("%d", key.BlockNum))
		}
		if err := w.Write(row); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to write CSV row: %v\n", err)
		}
	}
}

// hasTransactionInfo checks if any keys have transaction information
func (lv *LedgerView) hasTransactionInfo() bool {
	for _, key := range lv.Keys {
		if key.Transaction != "" || key.BlockNum > 0 {
			return true
		}
	}
	return false
}

// GetCSVHeader returns the CSV header for ledger view data
func GetCSVLedgerViewHeader() []string {
	return []string{"type", "namespace_or_collection", "key", "value_size_bytes", "is_read", "is_write", "tx_id", "block_num"}
}
