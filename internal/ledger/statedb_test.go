package ledger

import (
	"bytes"
	"encoding/binary"
	"encoding/csv"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/golang/protobuf/proto" //nolint SA1019
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/syndtr/goleveldb/leveldb"
)

// --- Version encoding tests ---

func TestEncodeDecodeOrderPreservingVarUint64(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		val  uint64
	}{
		{"zero", 0},
		{"one", 1},
		{"small", 42},
		{"byte_boundary", 255},
		{"two_bytes", 256},
		{"medium", 65535},
		{"large", 1_000_000},
		{"block_num_realistic", 13},
		{"very_large", 1 << 40},
		{"max", ^uint64(0)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			encoded := EncodeOrderPreservingVarUint64(tt.val)
			decoded, n, err := DecodeOrderPreservingVarUint64(encoded)
			require.NoError(t, err)
			assert.Equal(t, tt.val, decoded)
			assert.Equal(t, len(encoded), n)
		})
	}
}

func TestDecodeOrderPreservingVarUint64Errors(t *testing.T) {
	t.Parallel()

	_, _, err := DecodeOrderPreservingVarUint64(nil)
	assert.Error(t, err, "empty input")

	_, _, err = DecodeOrderPreservingVarUint64([]byte{})
	assert.Error(t, err, "empty input")

	_, _, err = DecodeOrderPreservingVarUint64([]byte{9}) // size > 8
	assert.Error(t, err, "size exceeds max")

	_, _, err = DecodeOrderPreservingVarUint64([]byte{3, 0x01}) // says 3 bytes but only 1 available
	assert.Error(t, err, "not enough bytes")
}

func TestDecodeOrderPreservingVarUint64CompatibleWithFabric(t *testing.T) {
	t.Parallel()
	// Verify our encoding matches Fabric's by encoding known values
	// and checking the byte format.

	// 0 should encode as [0] (size=0)
	enc := EncodeOrderPreservingVarUint64(0)
	assert.Equal(t, []byte{0}, enc)

	// 1 should encode as [1, 1] (size=1, value=0x01)
	enc = EncodeOrderPreservingVarUint64(1)
	assert.Equal(t, []byte{1, 1}, enc)

	// 256 should encode as [2, 1, 0] (size=2, value=0x0100)
	enc = EncodeOrderPreservingVarUint64(256)
	assert.Equal(t, []byte{2, 1, 0}, enc)

	// Order preservation: encoded bytes should sort in the same order as the numbers
	enc13 := EncodeOrderPreservingVarUint64(13)
	enc100 := EncodeOrderPreservingVarUint64(100)
	enc1000 := EncodeOrderPreservingVarUint64(1000)
	assert.True(t, bytes.Compare(enc13, enc100) < 0, "13 < 100 in encoded form")
	assert.True(t, bytes.Compare(enc100, enc1000) < 0, "100 < 1000 in encoded form")
}

// --- Key parsing tests ---

func TestParseStateDBEntry(t *testing.T) {
	t.Parallel()

	channel := "mychannel"
	chaincode := "mycc"
	collection := "private"
	key := "luther:private:dsr:key:abc123"
	blockNum := uint64(13)
	txNum := uint64(0)

	rawKey := buildTestLevelDBKey(channel, chaincode+"$$p"+collection, key)
	rawValue := buildTestVersionedValue(t, blockNum, txNum, []byte("somevalue"))

	entry, err := parseStateDBEntry(rawKey, rawValue, channel)
	require.NoError(t, err)
	require.NotNil(t, entry)

	assert.Equal(t, chaincode, entry.Chaincode)
	assert.Equal(t, collection, entry.Collection)
	assert.Equal(t, key, entry.Key)
	assert.Equal(t, blockNum, entry.BlockNum)
	assert.Equal(t, txNum, entry.TxNum)
	assert.Equal(t, len("somevalue"), entry.ValueSize)
}

func TestParseStateDBEntryIgnoresPublicData(t *testing.T) {
	t.Parallel()
	rawKey := buildTestLevelDBKey("mychannel", "mycc", "publickey")
	rawValue := buildTestVersionedValue(t, 10, 0, []byte("val"))

	entry, err := parseStateDBEntry(rawKey, rawValue, "mychannel")
	require.NoError(t, err)
	assert.Nil(t, entry, "public data keys should be skipped")
}

func TestParseStateDBEntryIgnoresHashedData(t *testing.T) {
	t.Parallel()
	// $$h is hashed data, not actual private data
	rawKey := buildTestLevelDBKey("mychannel", "mycc$$hprivate", "hashedkey")
	rawValue := buildTestVersionedValue(t, 10, 0, []byte("val"))

	entry, err := parseStateDBEntry(rawKey, rawValue, "mychannel")
	require.NoError(t, err)
	assert.Nil(t, entry, "hashed private data ($$h) should be skipped")
}

func TestParseStateDBEntryIgnoresWrongChannel(t *testing.T) {
	t.Parallel()
	rawKey := buildTestLevelDBKey("otherchannel", "mycc$$pprivate", "key1")
	rawValue := buildTestVersionedValue(t, 10, 0, []byte("val"))

	entry, err := parseStateDBEntry(rawKey, rawValue, "mychannel")
	require.NoError(t, err)
	assert.Nil(t, entry, "wrong channel should be skipped")
}

// --- LevelDB integration tests ---

func TestReadPrivateDataKeys(t *testing.T) {
	t.Parallel()
	db := createTestLevelDB(t, map[string]testEntry{
		"mychannel/mycc$$pprivate/key1":        {blockNum: 13, txNum: 0, value: []byte("val1")},
		"mychannel/mycc$$pprivate/key2":        {blockNum: 100, txNum: 1, value: []byte("val2")},
		"mychannel/mycc$$pcollection2/key3":    {blockNum: 50, txNum: 0, value: []byte("val3")},
		"mychannel/mycc/publickey":             {blockNum: 5, txNum: 0, value: []byte("pub")},
		"mychannel/mycc$$hprivate/hashedkey":   {blockNum: 13, txNum: 0, value: []byte("hash")},
		"otherchannel/mycc$$pprivate/otherkey": {blockNum: 1, txNum: 0, value: []byte("other")},
	})
	defer db.Close()

	// Read all private data keys for mychannel
	entries, err := ReadPrivateDataKeys(db, "mychannel", PvtDataKeysOptions{})
	require.NoError(t, err)
	assert.Len(t, entries, 3, "should find 3 private data keys (key1, key2, key3)")

	// Verify no public, hashed, or wrong-channel entries
	for _, e := range entries {
		assert.NotEqual(t, "publickey", e.Key)
		assert.NotEqual(t, "hashedkey", e.Key)
		assert.NotEqual(t, "otherkey", e.Key)
	}
}

func TestReadPrivateDataKeysFilterByCollection(t *testing.T) {
	t.Parallel()
	db := createTestLevelDB(t, map[string]testEntry{
		"ch/mycc$$pprivate/key1":     {blockNum: 13, txNum: 0, value: []byte("v")},
		"ch/mycc$$pcollection2/key2": {blockNum: 50, txNum: 0, value: []byte("v")},
		"ch/mycc$$pprivate/key3":     {blockNum: 100, txNum: 0, value: []byte("v")},
	})
	defer db.Close()

	entries, err := ReadPrivateDataKeys(db, "ch", PvtDataKeysOptions{
		Collection: "private",
	})
	require.NoError(t, err)
	assert.Len(t, entries, 2)
	for _, e := range entries {
		assert.Equal(t, "private", e.Collection)
	}
}

func TestReadPrivateDataKeysSortByVersion(t *testing.T) {
	t.Parallel()
	db := createTestLevelDB(t, map[string]testEntry{
		"ch/cc$$pcoll/key_block100": {blockNum: 100, txNum: 0, value: []byte("v")},
		"ch/cc$$pcoll/key_block13":  {blockNum: 13, txNum: 0, value: []byte("v")},
		"ch/cc$$pcoll/key_block50":  {blockNum: 50, txNum: 2, value: []byte("v")},
		"ch/cc$$pcoll/key_block50b": {blockNum: 50, txNum: 0, value: []byte("v")},
	})
	defer db.Close()

	entries, err := ReadPrivateDataKeys(db, "ch", PvtDataKeysOptions{
		SortByVersion: true,
	})
	require.NoError(t, err)
	require.Len(t, entries, 4)

	// Should be sorted: block 13, block 50 tx0, block 50 tx2, block 100
	assert.Equal(t, uint64(13), entries[0].BlockNum)
	assert.Equal(t, uint64(50), entries[1].BlockNum)
	assert.Equal(t, uint64(0), entries[1].TxNum)
	assert.Equal(t, uint64(50), entries[2].BlockNum)
	assert.Equal(t, uint64(2), entries[2].TxNum)
	assert.Equal(t, uint64(100), entries[3].BlockNum)
}

func TestReadPrivateDataKeysSortByVersionWithLimit(t *testing.T) {
	t.Parallel()
	db := createTestLevelDB(t, map[string]testEntry{
		"ch/cc$$pcoll/key_a": {blockNum: 100, txNum: 0, value: []byte("v")},
		"ch/cc$$pcoll/key_b": {blockNum: 13, txNum: 0, value: []byte("v")},
		"ch/cc$$pcoll/key_c": {blockNum: 50, txNum: 0, value: []byte("v")},
		"ch/cc$$pcoll/key_d": {blockNum: 1, txNum: 0, value: []byte("v")},
	})
	defer db.Close()

	entries, err := ReadPrivateDataKeys(db, "ch", PvtDataKeysOptions{
		SortByVersion: true,
		Limit:         2,
	})
	require.NoError(t, err)
	require.Len(t, entries, 2)

	// Should be the 2 earliest: block 1 and block 13
	assert.Equal(t, uint64(1), entries[0].BlockNum)
	assert.Equal(t, uint64(13), entries[1].BlockNum)
}

func TestReadPrivateDataKeysEmpty(t *testing.T) {
	t.Parallel()
	db := createTestLevelDB(t, map[string]testEntry{
		"ch/cc/publickey": {blockNum: 5, txNum: 0, value: []byte("v")},
	})
	defer db.Close()

	entries, err := ReadPrivateDataKeys(db, "ch", PvtDataKeysOptions{})
	require.NoError(t, err)
	assert.Empty(t, entries)
}

// --- Output tests ---

func TestWritePvtDataKeysTable(t *testing.T) {
	t.Parallel()
	entries := []*PrivateDataKeyEntry{
		{Collection: "private", Key: "key1", BlockNum: 13, TxNum: 0, ValueSize: 100},
	}

	var buf bytes.Buffer
	WritePvtDataKeysTable(&buf, entries)

	output := buf.String()
	assert.Contains(t, output, "private")
	assert.Contains(t, output, "key1")
	assert.Contains(t, output, "13")
}

func TestWritePvtDataKeysTableEmpty(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	WritePvtDataKeysTable(&buf, nil)
	assert.Contains(t, buf.String(), "No private data keys found")
}

func TestWritePvtDataKeysCSV(t *testing.T) {
	t.Parallel()
	entries := []*PrivateDataKeyEntry{
		{Collection: "private", Key: "key1", BlockNum: 13, TxNum: 0, ValueSize: 100},
		{Collection: "coll2", Key: "key2", BlockNum: 50, TxNum: 1, ValueSize: 200},
	}

	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	require.NoError(t, w.Write(GetCSVPvtDataKeysHeader()))
	WritePvtDataKeysCSV(w, entries)
	w.Flush()

	reader := csv.NewReader(strings.NewReader(buf.String()))
	records, err := reader.ReadAll()
	require.NoError(t, err)
	require.Len(t, records, 3) // header + 2 rows

	// Check header
	assert.Equal(t, GetCSVPvtDataKeysHeader(), records[0])

	// Check all rows have same column count
	for i, row := range records {
		assert.Len(t, row, 5, "row %d should have 5 columns", i)
	}

	// Check data
	assert.Equal(t, "private", records[1][0])
	assert.Equal(t, "key1", records[1][1])
	assert.Equal(t, "13", records[1][2])
	assert.Equal(t, "0", records[1][3])
	assert.Equal(t, "100", records[1][4])
}

// --- Test helpers ---

type testEntry struct {
	blockNum uint64
	txNum    uint64
	value    []byte
}

// buildTestLevelDBKey builds a raw LevelDB key matching Fabric's format:
// <channel>\x00d<namespace>\x00<key>
// The input format is "channel/namespace/key".
func buildTestLevelDBKey(channel, namespace, key string) []byte {
	var buf []byte
	buf = append(buf, []byte(channel)...)
	buf = append(buf, 0x00) // channel separator
	buf = append(buf, 'd')  // data key prefix
	buf = append(buf, []byte(namespace)...)
	buf = append(buf, 0x00) // namespace/key separator
	buf = append(buf, []byte(key)...)
	return buf
}

// buildTestVersionedValue creates a protobuf-encoded VersionedValueProto.
func buildTestVersionedValue(t *testing.T, blockNum, txNum uint64, value []byte) []byte {
	t.Helper()
	versionBytes := append(
		EncodeOrderPreservingVarUint64(blockNum),
		EncodeOrderPreservingVarUint64(txNum)...,
	)
	msg := &versionedValueProto{
		VersionBytes: versionBytes,
		Value:        value,
	}
	data, err := proto.Marshal(msg)
	require.NoError(t, err)
	return data
}

// createTestLevelDB creates a temporary LevelDB with test data.
// Keys in the map use format "channel/namespace/key".
func createTestLevelDB(t *testing.T, entries map[string]testEntry) *leveldb.DB {
	t.Helper()

	dir := filepath.Join(t.TempDir(), "testdb")
	db, err := leveldb.OpenFile(dir, nil)
	require.NoError(t, err)

	for keySpec, entry := range entries {
		parts := strings.SplitN(keySpec, "/", 3)
		require.Len(t, parts, 3, "key spec must be channel/namespace/key: %s", keySpec)

		channel := parts[0]
		namespace := parts[1]
		key := parts[2]

		rawKey := buildTestLevelDBKey(channel, namespace, key)
		rawValue := buildTestVersionedValue(t, entry.blockNum, entry.txNum, entry.value)

		require.NoError(t, db.Put(rawKey, rawValue, nil))
	}

	// Close and reopen read-only to match production usage
	require.NoError(t, db.Close())
	db, err = leveldb.OpenFile(dir, nil)
	require.NoError(t, err)

	// Also write the format version key that Fabric expects
	// (not strictly needed for our reader, but good practice)
	formatKey := append([]byte("_"), 0x00)
	formatKey = append(formatKey, 'f')
	_ = db.Put(formatKey, []byte("2.0"), nil)

	return db
}

func TestStateLevelDBPath(t *testing.T) {
	t.Parallel()
	path := StateLevelDBPath("/var/hyperledger/production")
	assert.Equal(t, "/var/hyperledger/production/ledgersData/stateLeveldb", path)
}

func TestOpenStateLevelDBError(t *testing.T) {
	t.Parallel()
	_, err := OpenStateLevelDB("/nonexistent/path/db")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to open state LevelDB")
}

func TestOpenStateLevelDBReadOnly(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "testdb")

	// Create a DB first
	db, err := leveldb.OpenFile(dir, nil)
	require.NoError(t, err)
	require.NoError(t, db.Put([]byte("test"), []byte("value"), nil))
	require.NoError(t, db.Close())

	// Open read-only
	db, err = OpenStateLevelDB(dir)
	require.NoError(t, err)
	defer db.Close()

	// Should be able to read
	val, err := db.Get([]byte("test"), nil)
	require.NoError(t, err)
	assert.Equal(t, []byte("value"), val)

	// Should not be able to write
	err = db.Put([]byte("test2"), []byte("value2"), nil)
	assert.Error(t, err)
}

// TestEncodeDecodeRoundtripWithFabricUtil verifies our encoding matches
// what Fabric's util.go would produce by checking specific byte patterns.
func TestEncodeDecodeRoundtripWithFabricUtil(t *testing.T) {
	t.Parallel()

	// Build a VersionedValueProto the same way Fabric would
	blockNum := uint64(13)
	txNum := uint64(0)

	versionBytes := append(
		EncodeOrderPreservingVarUint64(blockNum),
		EncodeOrderPreservingVarUint64(txNum)...,
	)

	// Verify the version bytes
	// 13 should encode as [1, 13] (1 byte needed, value 0x0D)
	// 0 should encode as [0] (0 bytes needed)
	assert.Equal(t, []byte{1, 13, 0}, versionBytes)

	// Decode and verify
	b, n1, err := DecodeOrderPreservingVarUint64(versionBytes)
	require.NoError(t, err)
	assert.Equal(t, uint64(13), b)
	assert.Equal(t, 2, n1)

	tx, n2, err := DecodeOrderPreservingVarUint64(versionBytes[n1:])
	require.NoError(t, err)
	assert.Equal(t, uint64(0), tx)
	assert.Equal(t, 1, n2)
}

// TestDecodeVersionedValue tests decoding a complete protobuf value
func TestDecodeVersionedValue(t *testing.T) {
	t.Parallel()

	value := buildTestVersionedValue(t, 42, 3, []byte("hello world"))
	blockNum, txNum, valueSize, err := decodeVersionedValue(value)
	require.NoError(t, err)
	assert.Equal(t, uint64(42), blockNum)
	assert.Equal(t, uint64(3), txNum)
	assert.Equal(t, len("hello world"), valueSize)
}

func TestDecodeVersionedValueEmptyVersion(t *testing.T) {
	t.Parallel()

	msg := &versionedValueProto{
		VersionBytes: nil,
		Value:        []byte("data"),
	}
	data, err := proto.Marshal(msg)
	require.NoError(t, err)

	blockNum, txNum, valueSize, err := decodeVersionedValue(data)
	require.NoError(t, err)
	assert.Equal(t, uint64(0), blockNum)
	assert.Equal(t, uint64(0), txNum)
	assert.Equal(t, 4, valueSize)
}

func TestDecodeVersionedValueInvalid(t *testing.T) {
	t.Parallel()
	_, _, _, err := decodeVersionedValue([]byte{0xff, 0xff, 0xff})
	assert.Error(t, err)
}

// Verify the encoding produces correct big-endian byte patterns
func TestEncodeKnownValues(t *testing.T) {
	t.Parallel()

	// 65536 = 0x10000 = 3 bytes: [3, 0x01, 0x00, 0x00]
	enc := EncodeOrderPreservingVarUint64(65536)
	assert.Equal(t, byte(3), enc[0])
	assert.Equal(t, 4, len(enc))

	decoded, _, err := DecodeOrderPreservingVarUint64(enc)
	require.NoError(t, err)
	assert.Equal(t, uint64(65536), decoded)

	// Max uint64 = 8 bytes: [8, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF]
	maxEnc := EncodeOrderPreservingVarUint64(^uint64(0))
	assert.Equal(t, byte(8), maxEnc[0])
	assert.Equal(t, 9, len(maxEnc))
	maxBuf := make([]byte, 8)
	binary.BigEndian.PutUint64(maxBuf, ^uint64(0))
	assert.Equal(t, maxBuf, maxEnc[1:])
}

// Verify we handle the OpenStateLevelDB + LOCK file scenario
func TestOpenStateLevelDBWithExistingLock(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "testdb")

	// Create a DB
	db, err := leveldb.OpenFile(dir, nil)
	require.NoError(t, err)

	// Try to open again while first is still open — should fail
	_, err = OpenStateLevelDB(dir)
	assert.Error(t, err, "should fail when DB is locked by another process")

	db.Close()

	// Now should work
	db2, err := OpenStateLevelDB(dir)
	require.NoError(t, err)
	db2.Close()
}

// Smoke test: verify we handle the "no entries for this channel" case
func TestReadPrivateDataKeysWrongChannel(t *testing.T) {
	t.Parallel()
	db := createTestLevelDB(t, map[string]testEntry{
		"ch1/cc$$pcoll/key1": {blockNum: 1, txNum: 0, value: []byte("v")},
	})
	defer db.Close()

	entries, err := ReadPrivateDataKeys(db, "ch2", PvtDataKeysOptions{})
	require.NoError(t, err)
	assert.Empty(t, entries)
}

// Test that we handle the format version entry gracefully
func TestReadPrivateDataKeysSkipsFormatEntry(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "testdb")
	db, err := leveldb.OpenFile(dir, nil)
	require.NoError(t, err)

	// Write a format version entry like Fabric does
	formatKey := append([]byte("_"), 0x00)
	formatKey = append(formatKey, 'f')
	require.NoError(t, db.Put(formatKey, []byte("2.0"), nil))

	// Write a private data entry
	rawKey := buildTestLevelDBKey("ch", "cc$$pcoll", "key1")
	rawValue := buildTestVersionedValue(t, 10, 0, []byte("val"))
	require.NoError(t, db.Put(rawKey, rawValue, nil))

	db.Close()
	db, err = leveldb.OpenFile(dir, nil)
	require.NoError(t, err)
	defer db.Close()

	entries, err := ReadPrivateDataKeys(db, "ch", PvtDataKeysOptions{})
	require.NoError(t, err)
	assert.Len(t, entries, 1)
	assert.Equal(t, "key1", entries[0].Key)
}

// Test to verify format of entries
func TestPrivateDataKeyEntryDiscovery(t *testing.T) {
	t.Parallel()
	db := createTestLevelDB(t, map[string]testEntry{
		"ch/cc1$$pprivate/key1": {blockNum: 10, txNum: 0, value: []byte("v")},
		"ch/cc1$$pcollA/key2":   {blockNum: 20, txNum: 0, value: []byte("v")},
		"ch/cc2$$pcollB/key3":   {blockNum: 30, txNum: 0, value: []byte("v")},
	})
	defer db.Close()

	entries, err := ReadPrivateDataKeys(db, "ch", PvtDataKeysOptions{})
	require.NoError(t, err)
	require.Len(t, entries, 3)

	// Collect unique collections discovered
	collections := make(map[string]bool)
	for _, e := range entries {
		collections[e.Collection] = true
	}
	assert.True(t, collections["private"])
	assert.True(t, collections["collA"])
	assert.True(t, collections["collB"])
	assert.Len(t, collections, 3)
}

func init() {
	// Suppress log output during tests
	_ = os.Setenv("FABRIC_LOGGING_SPEC", "error")
}
