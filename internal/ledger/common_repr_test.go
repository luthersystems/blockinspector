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
	view := NewLedgerView()
	assert.NotNil(t, view)
	assert.Empty(t, view.Keys)
	assert.NotNil(t, view.Statistics)
	assert.Equal(t, 0, view.Statistics.TotalKeys)
}

func TestLedgerViewAddKey(t *testing.T) {
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
	view := NewLedgerView()
	view.AddKey(nil) // should not panic
	assert.Equal(t, 0, view.Statistics.TotalKeys)
}

func TestLedgerViewWrite(t *testing.T) {
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
	view.Write(&buf)

	output := buf.String()
	assert.Contains(t, output, "Ledger Summary")
	assert.Contains(t, output, "Total Keys")
	assert.Contains(t, output, "2") // total keys
	assert.Contains(t, output, "State Namespaces")
	assert.Contains(t, output, "mycc")
	assert.Contains(t, output, "Private Data Collections")
	assert.Contains(t, output, "mycoll")
	assert.Contains(t, output, "key1")
	assert.Contains(t, output, "secret1")
}

func TestLedgerViewWriteNil(t *testing.T) {
	var lv *LedgerView
	var buf bytes.Buffer
	lv.Write(&buf) // should not panic
	assert.Empty(t, buf.String())
}

func TestLedgerViewWriteNilWriter(t *testing.T) {
	view := NewLedgerView()
	view.Write(nil) // should not panic
}

func TestLedgerViewWriteDeterministicOrder(t *testing.T) {
	// Run multiple times to catch non-determinism
	for i := 0; i < 10; i++ {
		view := NewLedgerView()
		view.AddKey(&KeyRecord{Type: "state", Namespace: "zz_last", Key: "k1", ValueSize: 1, IsWrite: true})
		view.AddKey(&KeyRecord{Type: "state", Namespace: "aa_first", Key: "k2", ValueSize: 1, IsWrite: true})
		view.AddKey(&KeyRecord{Type: "state", Namespace: "mm_middle", Key: "k3", ValueSize: 1, IsWrite: true})

		var buf bytes.Buffer
		view.Write(&buf)
		output := buf.String()

		// aa_first should appear before mm_middle which should appear before zz_last
		aaIdx := strings.Index(output, "aa_first")
		mmIdx := strings.Index(output, "mm_middle")
		zzIdx := strings.Index(output, "zz_last")

		assert.Greater(t, mmIdx, aaIdx, "mm_middle should appear after aa_first")
		assert.Greater(t, zzIdx, mmIdx, "zz_last should appear after mm_middle")
	}
}

func TestLedgerViewWriteCSV(t *testing.T) {
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

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	assert.Equal(t, 2, len(lines), "expected 1 header + 1 data row")
	assert.Equal(t, "type,namespace_or_collection,key,value_size_bytes,is_read,is_write,tx_id,block_num", lines[0])
	assert.Contains(t, lines[1], "state,mycc,key1,100,false,true")
}

func TestLedgerViewWriteCSVWithTransactionInfo(t *testing.T) {
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

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	assert.Equal(t, 2, len(lines))
	assert.Contains(t, lines[1], "tx123")
	assert.Contains(t, lines[1], "5")
}

func TestLedgerViewWriteCSVNil(t *testing.T) {
	var lv *LedgerView
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	lv.WriteCSV(w) // should not panic
	w.Flush()
	assert.Empty(t, buf.String())
}

func TestGetCSVLedgerViewHeader(t *testing.T) {
	header := GetCSVLedgerViewHeader()
	assert.Equal(t, []string{"type", "namespace_or_collection", "key", "value_size_bytes", "is_read", "is_write", "tx_id", "block_num"}, header)
}

func TestLedgerViewBlockAndTransactionStats(t *testing.T) {
	view := NewLedgerView()
	view.Statistics.BlockCount = 3
	view.Statistics.TransactionCount = 10

	var buf bytes.Buffer
	view.Write(&buf)

	output := buf.String()
	assert.Contains(t, output, "Blocks")
	assert.Contains(t, output, "3")
	assert.Contains(t, output, "Transactions")
	assert.Contains(t, output, "10")
}
