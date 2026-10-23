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
	t.Parallel()
	entries := map[string]string{
		"state\x00mycc\x00key1":              "value1",
		"state\x00mycc\x00key2":              "value2",
		"state\x00othercc\x00key3":           "value3",
		"private\x00mycollection\x00secret1": "secretval",
	}

	buf := createTestSnapshot(t, entries)

	state, err := LoadSnapshot(buf)
	require.NoError(t, err)

	// Verify statistics
	stats := state.GetStats()
	assert.Equal(t, 3, stats.TotalStateKeys)
	assert.Equal(t, 1, stats.TotalPrivateKeys)
	assert.Equal(t, int64(len("value1")+len("value2")+len("value3")), stats.TotalStateSize)
	assert.Equal(t, int64(len("secretval")), stats.TotalPrivateSize)
	assert.Equal(t, 2, stats.Namespaces["mycc"])
	assert.Equal(t, 1, stats.Namespaces["othercc"])
	assert.Equal(t, 1, stats.Collections["mycollection"])

	// Verify actual key content
	require.Len(t, state.StateKeys, 3)
	stateByKey := make(map[string]*StateKey)
	for _, sk := range state.StateKeys {
		stateByKey[sk.Namespace+"/"+sk.Key] = sk
	}
	assert.Equal(t, []byte("value1"), stateByKey["mycc/key1"].Value)
	assert.Equal(t, []byte("value2"), stateByKey["mycc/key2"].Value)
	assert.Equal(t, "othercc", stateByKey["othercc/key3"].Namespace)

	require.Len(t, state.PrivateKeys, 1)
	assert.Equal(t, "mycollection", state.PrivateKeys[0].Collection)
	assert.Equal(t, "secret1", state.PrivateKeys[0].Key)
	assert.Equal(t, []byte("secretval"), state.PrivateKeys[0].Value)
}

func TestLoadSnapshotSkipsMalformedKeys(t *testing.T) {
	t.Parallel()
	entries := map[string]string{
		"state\x00mycc\x00key1":  "value1",
		"badkey":                 "ignored",        // no null separators
		"also\x00bad":            "also_ignored",   // only one separator
		"metadata\x00foo\x00bar": "unknown_prefix", // correct format but unknown prefix
	}

	buf := createTestSnapshot(t, entries)

	state, err := LoadSnapshot(buf)
	require.NoError(t, err)

	assert.Equal(t, 1, state.GetStats().TotalStateKeys)
	assert.Equal(t, 0, state.GetStats().TotalPrivateKeys)
	assert.Len(t, state.StateKeys, 1)
	assert.Equal(t, "key1", state.StateKeys[0].Key)
}

func TestLoadSnapshotEmpty(t *testing.T) {
	t.Parallel()
	buf := createTestSnapshot(t, map[string]string{})

	state, err := LoadSnapshot(buf)
	require.NoError(t, err)

	assert.Equal(t, 0, state.GetStats().TotalStateKeys)
	assert.Equal(t, 0, state.GetStats().TotalPrivateKeys)
	assert.Empty(t, state.StateKeys)
	assert.Empty(t, state.PrivateKeys)
}

func TestLoadSnapshotFromFileError(t *testing.T) {
	t.Parallel()
	_, err := LoadSnapshotFromFile("/nonexistent/path/snapshot.bak")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to open snapshot file")
}

func TestSnapshotToLedgerView(t *testing.T) {
	t.Parallel()
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
		assert.False(t, key.IsRead, "snapshot keys should not be reads")
		assert.True(t, key.IsWrite, "snapshot keys should be writes")
		assert.Empty(t, key.Transaction, "snapshot keys should have no transaction info")
		assert.Equal(t, uint64(0), key.BlockNum, "snapshot keys should have no block number")
	}

	// Verify key content is preserved through conversion
	keysByName := make(map[string]*KeyRecord)
	for _, k := range view.Keys {
		keysByName[k.Key] = k
	}
	require.Contains(t, keysByName, "key1")
	assert.Equal(t, "state", keysByName["key1"].Type)
	assert.Equal(t, "mycc", keysByName["key1"].Namespace)
	assert.Equal(t, len("value1"), keysByName["key1"].ValueSize)

	require.Contains(t, keysByName, "secret1")
	assert.Equal(t, "private", keysByName["secret1"].Type)
	assert.Equal(t, "mycollection", keysByName["secret1"].Namespace)
}

func TestSnapshotToLedgerViewNil(t *testing.T) {
	t.Parallel()
	var s *SnapshotState
	view := s.ToLedgerView()
	require.NotNil(t, view)
	assert.Equal(t, 0, view.Statistics.TotalKeys)
	assert.Empty(t, view.Keys)
	assert.NotNil(t, view.Statistics.Namespaces)
	assert.NotNil(t, view.Statistics.Collections)
}

func TestSnapshotWrite(t *testing.T) {
	t.Parallel()
	entries := map[string]string{
		"state\x00mycc\x00key1": "value1",
	}

	buf := createTestSnapshot(t, entries)
	state, err := LoadSnapshot(buf)
	require.NoError(t, err)

	var out bytes.Buffer
	require.NoError(t, state.Write(&out))

	output := out.String()
	assert.Contains(t, output, "Ledger Summary")
	assert.Contains(t, output, "Total Keys")
	assert.Contains(t, output, "State Namespaces")
	assert.Contains(t, output, "mycc")
	assert.Contains(t, output, "key1")
	// Verify value size appears in the output
	assert.Contains(t, output, "6", "value1 is 6 bytes")
}

func TestSnapshotWriteNil(t *testing.T) {
	t.Parallel()
	var s *SnapshotState
	var buf bytes.Buffer
	require.NoError(t, s.Write(&buf)) // should not panic
	assert.Empty(t, buf.String())
}

func TestSnapshotWriteCSV(t *testing.T) {
	t.Parallel()
	entries := map[string]string{
		"state\x00mycc\x00key1":              "value1",
		"private\x00mycollection\x00secret1": "secretval",
	}

	buf := createTestSnapshot(t, entries)
	state, err := LoadSnapshot(buf)
	require.NoError(t, err)

	var out bytes.Buffer
	w := csv.NewWriter(&out)
	require.NoError(t, w.Write(GetCSVLedgerViewHeader()))
	state.WriteCSV(w)
	w.Flush()

	// Parse CSV properly
	reader := csv.NewReader(strings.NewReader(out.String()))
	records, err := reader.ReadAll()
	require.NoError(t, err)
	require.Len(t, records, 3, "expected 1 header + 2 data rows")

	// Verify header
	assert.Equal(t, GetCSVLedgerViewHeader(), records[0])

	// Verify all rows have the same number of columns as the header
	for i, row := range records {
		assert.Len(t, row, len(records[0]), "row %d should have same column count as header", i)
	}

	// Verify no duplicate header
	headerCount := 0
	for _, row := range records {
		if row[0] == "type" {
			headerCount++
		}
	}
	assert.Equal(t, 1, headerCount, "should have exactly one header row")

	// Verify data content by building a map of key -> record
	dataByKey := make(map[string][]string)
	for _, row := range records[1:] {
		dataByKey[row[2]] = row // index by key column
	}

	// Check state key
	require.Contains(t, dataByKey, "key1")
	stateRow := dataByKey["key1"]
	assert.Equal(t, "state", stateRow[0])
	assert.Equal(t, "mycc", stateRow[1])
	assert.Equal(t, "6", stateRow[3])     // value_size_bytes for "value1"
	assert.Equal(t, "false", stateRow[4]) // is_read
	assert.Equal(t, "true", stateRow[5])  // is_write

	// Check private key
	require.Contains(t, dataByKey, "secret1")
	privateRow := dataByKey["secret1"]
	assert.Equal(t, "private", privateRow[0])
	assert.Equal(t, "mycollection", privateRow[1])
	assert.Equal(t, "9", privateRow[3])     // value_size_bytes for "secretval"
	assert.Equal(t, "false", privateRow[4]) // is_read
	assert.Equal(t, "true", privateRow[5])  // is_write
}
