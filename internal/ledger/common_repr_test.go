package ledger

import (
	"bytes"
	"encoding/csv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewLedgerView(t *testing.T) {
	t.Parallel()
	view := NewLedgerView()
	require.NotNil(t, view)
	assert.Empty(t, view.Keys)
	assert.NotNil(t, view.Statistics)
	assert.NotNil(t, view.Statistics.Namespaces)
	assert.NotNil(t, view.Statistics.Collections)
	assert.Equal(t, 0, view.Statistics.TotalKeys)
}

func TestLedgerViewAddKey(t *testing.T) {
	t.Parallel()
	view := NewLedgerView()

	view.AddKey(&KeyRecord{
		Type:      "state",
		Namespace: "mycc",
		Key:       "key1",
		ValueSize: 100,
		IsWrite:   true,
	})
	view.AddKey(&KeyRecord{
		Type:      "private",
		Namespace: "mycoll",
		Key:       "secret1",
		ValueSize: 50,
		IsWrite:   true,
	})

	assert.Equal(t, 2, view.Statistics.TotalKeys)
	assert.Equal(t, 1, view.Statistics.TotalStateKeys)
	assert.Equal(t, 1, view.Statistics.TotalPrivateKeys)
	assert.Equal(t, int64(150), view.Statistics.TotalSize)
	assert.Equal(t, int64(100), view.Statistics.TotalStateSize)
	assert.Equal(t, int64(50), view.Statistics.TotalPrivateSize)
	assert.Equal(t, 1, view.Statistics.Namespaces["mycc"])
	assert.Equal(t, 1, view.Statistics.Collections["mycoll"])
}

func TestLedgerViewAddKeyNil(t *testing.T) {
	t.Parallel()
	view := NewLedgerView()
	view.AddKey(nil) // should not panic
	assert.Equal(t, 0, view.Statistics.TotalKeys)
}

func TestLedgerViewWrite(t *testing.T) {
	t.Parallel()
	view := NewLedgerView()
	view.AddKey(&KeyRecord{
		Type:      "state",
		Namespace: "mycc",
		Key:       "key1",
		ValueSize: 100,
		IsRead:    true,
		IsWrite:   true,
	})
	view.AddKey(&KeyRecord{
		Type:      "private",
		Namespace: "mycoll",
		Key:       "secret1",
		ValueSize: 50,
		IsWrite:   true,
	})

	var buf bytes.Buffer
	require.NoError(t, view.Write(&buf))

	output := buf.String()
	assert.Contains(t, output, "Ledger Summary")
	assert.Contains(t, output, "Total Keys")
	assert.Contains(t, output, "State Namespaces")
	assert.Contains(t, output, "mycc")
	assert.Contains(t, output, "Private Data Collections")
	assert.Contains(t, output, "mycoll")
	assert.Contains(t, output, "key1")
	assert.Contains(t, output, "100") // value size for key1
	assert.Contains(t, output, "secret1")
	assert.Contains(t, output, "50") // value size for secret1
}

func TestLedgerViewWriteNil(t *testing.T) {
	t.Parallel()
	var lv *LedgerView
	var buf bytes.Buffer
	require.NoError(t, lv.Write(&buf)) // should not panic
	assert.Empty(t, buf.String())
}

func TestLedgerViewWriteNilWriter(t *testing.T) {
	t.Parallel()
	view := NewLedgerView()
	require.NoError(t, view.Write(nil)) // should not panic
}

func TestLedgerViewWriteDeterministicOrder(t *testing.T) {
	t.Parallel()
	// Run multiple times to catch non-determinism
	for range 10 {
		view := NewLedgerView()
		view.AddKey(&KeyRecord{Type: "state", Namespace: "zz_last", Key: "k1", ValueSize: 1, IsWrite: true})
		view.AddKey(&KeyRecord{Type: "state", Namespace: "aa_first", Key: "k2", ValueSize: 1, IsWrite: true})
		view.AddKey(&KeyRecord{Type: "state", Namespace: "mm_middle", Key: "k3", ValueSize: 1, IsWrite: true})

		var buf bytes.Buffer
		require.NoError(t, view.Write(&buf))
		output := buf.String()

		// In the namespace summary table, aa_first should appear before mm_middle before zz_last
		aaIdx := strings.Index(output, "aa_first")
		mmIdx := strings.Index(output, "mm_middle")
		zzIdx := strings.Index(output, "zz_last")

		assert.Greater(t, mmIdx, aaIdx, "mm_middle should appear after aa_first")
		assert.Greater(t, zzIdx, mmIdx, "zz_last should appear after mm_middle")
	}
}

func TestLedgerViewWriteCSV(t *testing.T) {
	t.Parallel()
	view := NewLedgerView()
	view.AddKey(&KeyRecord{
		Type:      "state",
		Namespace: "mycc",
		Key:       "key1",
		ValueSize: 100,
		IsRead:    false,
		IsWrite:   true,
	})

	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	require.NoError(t, w.Write(GetCSVLedgerViewHeader()))
	view.WriteCSV(w)
	w.Flush()

	// Parse CSV properly
	reader := csv.NewReader(strings.NewReader(buf.String()))
	records, err := reader.ReadAll()
	require.NoError(t, err)
	require.Len(t, records, 2, "expected 1 header + 1 data row")

	// Verify header
	assert.Equal(t, GetCSVLedgerViewHeader(), records[0])

	// Verify all rows have same column count
	for i, row := range records {
		assert.Len(t, row, len(records[0]), "row %d should have same column count as header", i)
	}

	// Verify data row field by field
	row := records[1]
	assert.Equal(t, "state", row[0]) // type
	assert.Equal(t, "mycc", row[1])  // namespace_or_collection
	assert.Equal(t, "key1", row[2])  // key
	assert.Equal(t, "100", row[3])   // value_size_bytes
	assert.Equal(t, "false", row[4]) // is_read
	assert.Equal(t, "true", row[5])  // is_write
	assert.Equal(t, "", row[6])      // tx_id (empty for non-block data)
	assert.Equal(t, "0", row[7])     // block_num (zero for non-block data)
}

func TestLedgerViewWriteCSVWithTransactionInfo(t *testing.T) {
	t.Parallel()
	view := NewLedgerView()
	view.AddKey(&KeyRecord{
		Type:        "state",
		Namespace:   "mycc",
		Key:         "key1",
		ValueSize:   100,
		IsWrite:     true,
		Transaction: "tx123",
		BlockNum:    5,
	})

	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	require.NoError(t, w.Write(GetCSVLedgerViewHeader()))
	view.WriteCSV(w)
	w.Flush()

	reader := csv.NewReader(strings.NewReader(buf.String()))
	records, err := reader.ReadAll()
	require.NoError(t, err)
	require.Len(t, records, 2)

	// Verify transaction info is in the correct columns
	row := records[1]
	assert.Equal(t, "tx123", row[6]) // tx_id column specifically
	assert.Equal(t, "5", row[7])     // block_num column specifically
}

func TestLedgerViewWriteCSVNil(t *testing.T) {
	t.Parallel()
	var lv *LedgerView
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	lv.WriteCSV(w) // should not panic
	w.Flush()
	assert.Empty(t, buf.String())
}

func TestGetCSVLedgerViewHeader(t *testing.T) {
	t.Parallel()
	header := GetCSVLedgerViewHeader()
	assert.Len(t, header, 8)
	assert.Equal(t, "type", header[0])
	assert.Equal(t, "tx_id", header[6])
	assert.Equal(t, "block_num", header[7])
}

func TestLedgerViewBlockAndTransactionStats(t *testing.T) {
	t.Parallel()
	view := NewLedgerView()
	view.Statistics.BlockCount = 3
	view.Statistics.TransactionCount = 10

	var buf bytes.Buffer
	require.NoError(t, view.Write(&buf))

	output := buf.String()
	assert.Contains(t, output, "Blocks")
	assert.Contains(t, output, "Transactions")
}
