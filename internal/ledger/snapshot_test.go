package ledger

import (
	"bytes"
	"encoding/csv"
	"strings"
	"testing"

	badger "github.com/dgraph-io/badger/v4"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// createTestSnapshot creates a Badger backup in memory with the given key-value pairs.
// Keys should be in the format "prefix\x00bucket\x00key".
func createTestSnapshot(t *testing.T, entries map[string]string) *bytes.Buffer {
	t.Helper()

	log := logrus.New()
	log.SetLevel(logrus.WarnLevel)
	opts := badger.DefaultOptions("").
		WithInMemory(true).
		WithLogger(log)

	db, err := badger.Open(opts)
	require.NoError(t, err)
	defer db.Close()

	err = db.Update(func(txn *badger.Txn) error {
		for k, v := range entries {
			if err := txn.Set([]byte(k), []byte(v)); err != nil {
				return err
			}
		}
		return nil
	})
	require.NoError(t, err)

	var buf bytes.Buffer
	_, err = db.Backup(&buf, 0)
	require.NoError(t, err)

	return &buf
}

func TestLoadSnapshot(t *testing.T) {
	entries := map[string]string{
		"state\x00mycc\x00key1":              "value1",
		"state\x00mycc\x00key2":              "value2",
		"state\x00othercc\x00key3":           "value3",
		"private\x00mycollection\x00secret1": "secretval",
	}

	buf := createTestSnapshot(t, entries)

	state, err := LoadSnapshot(buf)
	require.NoError(t, err)

	stats := state.GetStats()
	assert.Equal(t, 3, stats.TotalStateKeys)
	assert.Equal(t, 1, stats.TotalPrivateKeys)
	assert.Equal(t, int64(len("value1")+len("value2")+len("value3")), stats.TotalStateSize)
	assert.Equal(t, int64(len("secretval")), stats.TotalPrivateSize)

	assert.Equal(t, 2, stats.Namespaces["mycc"])
	assert.Equal(t, 1, stats.Namespaces["othercc"])
	assert.Equal(t, 1, stats.Collections["mycollection"])
}

func TestLoadSnapshotSkipsMalformedKeys(t *testing.T) {
	entries := map[string]string{
		"state\x00mycc\x00key1": "value1",
		"badkey":                "ignored",     // no null separators
		"also\x00bad":          "also_ignored", // only one separator
	}

	buf := createTestSnapshot(t, entries)

	state, err := LoadSnapshot(buf)
	require.NoError(t, err)

	assert.Equal(t, 1, state.GetStats().TotalStateKeys)
	assert.Equal(t, 0, state.GetStats().TotalPrivateKeys)
}

func TestLoadSnapshotEmpty(t *testing.T) {
	buf := createTestSnapshot(t, map[string]string{})

	state, err := LoadSnapshot(buf)
	require.NoError(t, err)

	assert.Equal(t, 0, state.GetStats().TotalStateKeys)
	assert.Equal(t, 0, state.GetStats().TotalPrivateKeys)
	assert.Empty(t, state.StateKeys)
	assert.Empty(t, state.PrivateKeys)
}

func TestSnapshotToLedgerView(t *testing.T) {
	entries := map[string]string{
		"state\x00mycc\x00key1":              "value1",
		"private\x00mycollection\x00secret1": "secretval",
	}

	buf := createTestSnapshot(t, entries)
	state, err := LoadSnapshot(buf)
	require.NoError(t, err)

	view := state.ToLedgerView()

	assert.Equal(t, 2, view.Statistics.TotalKeys)
	assert.Equal(t, 1, view.Statistics.TotalStateKeys)
	assert.Equal(t, 1, view.Statistics.TotalPrivateKeys)
	assert.Equal(t, int64(len("value1")), view.Statistics.TotalStateSize)
	assert.Equal(t, int64(len("secretval")), view.Statistics.TotalPrivateSize)

	// Snapshot keys should be marked as writes, not reads
	for _, key := range view.Keys {
		assert.False(t, key.IsRead)
		assert.True(t, key.IsWrite)
		assert.Empty(t, key.Transaction, "snapshot keys should have no transaction info")
	}
}

func TestSnapshotToLedgerViewNil(t *testing.T) {
	var s *SnapshotState
	view := s.ToLedgerView()
	assert.NotNil(t, view)
	assert.Equal(t, 0, view.Statistics.TotalKeys)
}

func TestSnapshotWrite(t *testing.T) {
	entries := map[string]string{
		"state\x00mycc\x00key1": "value1",
	}

	buf := createTestSnapshot(t, entries)
	state, err := LoadSnapshot(buf)
	require.NoError(t, err)

	var out bytes.Buffer
	state.Write(&out)

	output := out.String()
	assert.Contains(t, output, "Ledger Summary")
	assert.Contains(t, output, "Total Keys")
	assert.Contains(t, output, "mycc")
	assert.Contains(t, output, "key1")
}

func TestSnapshotWriteNil(t *testing.T) {
	var s *SnapshotState
	var buf bytes.Buffer
	s.Write(&buf) // should not panic
	assert.Empty(t, buf.String())
}

func TestSnapshotWriteCSV(t *testing.T) {
	entries := map[string]string{
		"state\x00mycc\x00key1":              "value1",
		"private\x00mycollection\x00secret1": "secretval",
	}

	buf := createTestSnapshot(t, entries)
	state, err := LoadSnapshot(buf)
	require.NoError(t, err)

	var out bytes.Buffer
	w := csv.NewWriter(&out)
	err = w.Write(GetCSVLedgerViewHeader())
	require.NoError(t, err)
	state.WriteCSV(w)
	w.Flush()

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	assert.Equal(t, 3, len(lines), "expected 1 header + 2 data rows")

	// Verify header
	assert.Equal(t, "type,namespace_or_collection,key,value_size_bytes,is_read,is_write,tx_id,block_num", lines[0])

	// Verify no duplicate header
	headerCount := 0
	for _, line := range lines {
		if strings.HasPrefix(line, "type,") {
			headerCount++
		}
	}
	assert.Equal(t, 1, headerCount, "should have exactly one header row")
}
