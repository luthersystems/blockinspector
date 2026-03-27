package ledger

import (
	"bytes"
	"container/heap"
	"encoding/binary"
	"encoding/csv"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"

	"github.com/golang/protobuf/proto" //nolint SA1019 hlf does not support latest protobuf
	"github.com/olekukonko/tablewriter"
	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/iterator"
	"github.com/syndtr/goleveldb/leveldb/opt"
	"github.com/syndtr/goleveldb/leveldb/util"
)

const (
	// privateDataNsSep is the separator between chaincode name and collection
	// in Fabric's state database namespace for private data.
	// Format: <chaincode>$$p<collection> for actual data
	// Format: <chaincode>$$h<collection> for hashed data
	privateDataNsSep = "$$p"
)

var (
	dataKeyPrefix = []byte{'d'}
	nsKeySep      = []byte{0x00}
)

// PrivateDataKeyEntry represents a private data key with its version info.
type PrivateDataKeyEntry struct {
	Chaincode  string
	Collection string
	Key        string
	BlockNum   uint64
	TxNum      uint64
	ValueSize  int
}

// PvtDataKeysOptions controls filtering and output for the pvtdata-keys command.
type PvtDataKeysOptions struct {
	Collection    string // filter to a specific collection
	SortByVersion bool   // sort by (blockNum, txNum) ascending
	Limit         int    // max results (0 = all)
}

// versionedValueProto mirrors Fabric's VersionedValueProto protobuf message.
// We define our own to avoid importing Fabric's statedb packages.
type versionedValueProto struct {
	VersionBytes []byte `protobuf:"bytes,1,opt,name=version_bytes,proto3"`
	Value        []byte `protobuf:"bytes,2,opt,name=value,proto3"`
	Metadata     []byte `protobuf:"bytes,3,opt,name=metadata,proto3"`
}

func (m *versionedValueProto) Reset()         { *m = versionedValueProto{} }
func (m *versionedValueProto) String() string { return proto.CompactTextString(m) }
func (m *versionedValueProto) ProtoMessage()  {}

// StateLevelDBPath returns the path to the state LevelDB for a given peer ledger path.
func StateLevelDBPath(ledgerPath string) string {
	return filepath.Join(ledgerPath, "ledgersData", "stateLeveldb")
}

// OpenStateLevelDB opens the peer's state LevelDB in read-only mode.
func OpenStateLevelDB(dbPath string) (*leveldb.DB, error) {
	db, err := leveldb.OpenFile(dbPath, &opt.Options{
		ReadOnly: true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to open state LevelDB at %s: %w", dbPath, err)
	}
	return db, nil
}

// ReadPrivateDataKeys reads private data keys from the state LevelDB.
// It streams through the database without loading all entries into memory,
// which is critical for production ledgers that can be hundreds of GB.
//
// When opts.SortByVersion is true and opts.Limit > 0, it uses a bounded
// max-heap to efficiently find the N entries with the smallest versions
// without sorting the entire dataset.
func ReadPrivateDataKeys(db *leveldb.DB, channel string, opts PvtDataKeysOptions) ([]*PrivateDataKeyEntry, error) {
	// Build the prefix to scan: <channel>\x00d
	prefix := append([]byte(channel), 0x00)
	prefix = append(prefix, dataKeyPrefix...)

	iter := db.NewIterator(util.BytesPrefix(prefix), nil)
	defer iter.Release()

	// If sorting by version with a limit, use a bounded max-heap
	// to find the N smallest entries efficiently.
	if opts.SortByVersion && opts.Limit > 0 {
		return readWithMinN(iter, channel, opts)
	}

	var results []*PrivateDataKeyEntry
	count := 0

	for iter.Next() {
		entry, err := parseStateDBEntry(iter.Key(), iter.Value(), channel)
		if err != nil {
			continue // skip unparseable entries
		}
		if entry == nil {
			continue // not a private data key
		}

		if opts.Collection != "" && entry.Collection != opts.Collection {
			continue
		}

		results = append(results, entry)
		count++

		// For unsorted queries with a limit, we can stop early
		if !opts.SortByVersion && opts.Limit > 0 && count >= opts.Limit {
			break
		}
	}

	if err := iter.Error(); err != nil {
		return nil, fmt.Errorf("iterator error: %w", err)
	}

	if opts.SortByVersion {
		sort.Slice(results, func(i, j int) bool {
			if results[i].BlockNum != results[j].BlockNum {
				return results[i].BlockNum < results[j].BlockNum
			}
			return results[i].TxNum < results[j].TxNum
		})
		if opts.Limit > 0 && len(results) > opts.Limit {
			results = results[:opts.Limit]
		}
	}

	return results, nil
}

// readWithMinN uses a max-heap of size N to find the N entries with
// the smallest (blockNum, txNum) without loading everything into memory.
func readWithMinN(iter iterator.Iterator, channel string, opts PvtDataKeysOptions) ([]*PrivateDataKeyEntry, error) {
	h := &entryMaxHeap{}
	heap.Init(h)

	for iter.Next() {
		entry, err := parseStateDBEntry(iter.Key(), iter.Value(), channel)
		if err != nil || entry == nil {
			continue
		}

		if opts.Collection != "" && entry.Collection != opts.Collection {
			continue
		}

		heap.Push(h, entry)
		if h.Len() > opts.Limit {
			heap.Pop(h) // remove the largest
		}
	}

	if err := iter.Error(); err != nil {
		return nil, fmt.Errorf("iterator error: %w", err)
	}

	// Extract results in sorted order (smallest first)
	results := make([]*PrivateDataKeyEntry, h.Len())
	for i := len(results) - 1; i >= 0; i-- {
		results[i] = heap.Pop(h).(*PrivateDataKeyEntry)
	}

	return results, nil
}

// parseStateDBEntry parses a raw LevelDB key/value into a PrivateDataKeyEntry.
// Returns nil if the entry is not a private data key.
func parseStateDBEntry(rawKey, rawValue []byte, channel string) (*PrivateDataKeyEntry, error) {
	// Raw key format: <channel>\x00d<namespace>\x00<key>
	// Strip channel prefix + separator
	channelPrefix := append([]byte(channel), 0x00)
	if !bytes.HasPrefix(rawKey, channelPrefix) {
		return nil, nil
	}
	dataKey := rawKey[len(channelPrefix):]

	// Must start with 'd' prefix
	if len(dataKey) == 0 || dataKey[0] != 'd' {
		return nil, nil
	}
	dataKey = dataKey[1:] // strip 'd'

	// Split namespace from key on \x00
	parts := bytes.SplitN(dataKey, nsKeySep, 2)
	if len(parts) != 2 {
		return nil, nil
	}

	namespace := string(parts[0])
	key := string(parts[1])

	// Check if this is a private data namespace (contains $$p)
	chaincode, collection, found := strings.Cut(namespace, privateDataNsSep)
	if !found {
		return nil, nil // not private data
	}

	// Decode the value to get version
	blockNum, txNum, valueSize, err := decodeVersionedValue(rawValue)
	if err != nil {
		return nil, fmt.Errorf("failed to decode value for key %q: %w", key, err)
	}

	return &PrivateDataKeyEntry{
		Chaincode:  chaincode,
		Collection: collection,
		Key:        key,
		BlockNum:   blockNum,
		TxNum:      txNum,
		ValueSize:  valueSize,
	}, nil
}

// decodeVersionedValue decodes a Fabric VersionedValueProto and extracts version info.
func decodeVersionedValue(data []byte) (blockNum, txNum uint64, valueSize int, err error) {
	msg := &versionedValueProto{}
	if err := proto.Unmarshal(data, msg); err != nil {
		return 0, 0, 0, fmt.Errorf("failed to unmarshal value: %w", err)
	}

	if len(msg.VersionBytes) == 0 {
		return 0, 0, len(msg.Value), nil
	}

	blockNum, n1, err := DecodeOrderPreservingVarUint64(msg.VersionBytes)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("failed to decode block number: %w", err)
	}

	txNum, _, err = DecodeOrderPreservingVarUint64(msg.VersionBytes[n1:])
	if err != nil {
		return 0, 0, 0, fmt.Errorf("failed to decode tx number: %w", err)
	}

	return blockNum, txNum, len(msg.Value), nil
}

// DecodeOrderPreservingVarUint64 decodes a uint64 encoded with Fabric's
// order-preserving variable-length encoding.
// Returns the decoded number and the number of bytes consumed.
func DecodeOrderPreservingVarUint64(data []byte) (uint64, int, error) {
	if len(data) == 0 {
		return 0, 0, fmt.Errorf("empty input")
	}

	// First byte is the size (number of remaining bytes, 0-8)
	size := int(data[0])
	if size > 8 {
		return 0, 0, fmt.Errorf("decoded size %d exceeds maximum of 8", size)
	}
	if size == 0 {
		return 0, 1, nil
	}
	if len(data) < size+1 {
		return 0, 0, fmt.Errorf("need %d bytes but only %d available", size+1, len(data))
	}

	// Decode big-endian uint64 from the remaining bytes
	buf := make([]byte, 8)
	copy(buf[8-size:], data[1:size+1])
	return binary.BigEndian.Uint64(buf), size + 1, nil
}

// EncodeOrderPreservingVarUint64 encodes a uint64 using Fabric's
// order-preserving variable-length encoding.
func EncodeOrderPreservingVarUint64(n uint64) []byte {
	buf := make([]byte, 8)
	binary.BigEndian.PutUint64(buf, n)
	// Find first non-zero byte
	startIdx := 0
	size := 0
	for i, b := range buf {
		if b != 0x00 {
			startIdx = i
			size = 8 - i
			break
		}
	}
	result := make([]byte, size+1)
	result[0] = byte(size)
	copy(result[1:], buf[startIdx:])
	return result
}

// WritePvtDataKeysTable writes private data key entries in table format.
func WritePvtDataKeysTable(w io.Writer, entries []*PrivateDataKeyEntry) {
	if len(entries) == 0 {
		fmt.Fprintln(w, "No private data keys found.")
		return
	}

	table := tablewriter.NewWriter(w)
	table.Header("Collection", "Key", "Block Num", "Tx Num", "Value Size")

	for _, e := range entries {
		table.Append([]string{
			e.Collection,
			e.Key,
			fmt.Sprintf("%d", e.BlockNum),
			fmt.Sprintf("%d", e.TxNum),
			fmt.Sprintf("%d", e.ValueSize),
		})
	}
	table.Render()
}

// WritePvtDataKeysCSV writes private data key entries in CSV format.
// The caller is responsible for writing the header (see GetCSVPvtDataKeysHeader).
func WritePvtDataKeysCSV(w *csv.Writer, entries []*PrivateDataKeyEntry) {
	for _, e := range entries {
		row := []string{
			e.Collection,
			e.Key,
			fmt.Sprintf("%d", e.BlockNum),
			fmt.Sprintf("%d", e.TxNum),
			fmt.Sprintf("%d", e.ValueSize),
		}
		_ = w.Write(row)
	}
}

// GetCSVPvtDataKeysHeader returns the CSV header for private data key output.
func GetCSVPvtDataKeysHeader() []string {
	return []string{"collection", "key", "block_num", "tx_num", "value_size"}
}

// entryMaxHeap is a max-heap of PrivateDataKeyEntry by (BlockNum, TxNum).
// Used to efficiently find the N entries with the smallest versions.
type entryMaxHeap []*PrivateDataKeyEntry

func (h entryMaxHeap) Len() int { return len(h) }
func (h entryMaxHeap) Less(i, j int) bool {
	// Max-heap: larger values have higher priority
	if h[i].BlockNum != h[j].BlockNum {
		return h[i].BlockNum > h[j].BlockNum
	}
	return h[i].TxNum > h[j].TxNum
}
func (h entryMaxHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *entryMaxHeap) Push(x any) {
	*h = append(*h, x.(*PrivateDataKeyEntry))
}
func (h *entryMaxHeap) Pop() any {
	old := *h
	n := len(old)
	item := old[n-1]
	old[n-1] = nil
	*h = old[:n-1]
	return item
}
