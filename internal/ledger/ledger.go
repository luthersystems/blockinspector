package ledger

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/hyperledger/fabric-protos-go/common"
	cledger "github.com/hyperledger/fabric/common/ledger"
	"github.com/hyperledger/fabric/common/ledger/blkstorage"
	"github.com/hyperledger/fabric/common/ledger/blkstorage/fsblkstorage"
	"github.com/hyperledger/fabric/common/metrics/disabled"
)

const (
	ledgersDataDirName = "ledgersData"
	ordererDataDirName = "orderer"
)

type blockStore struct {
	blockStoreProvider blkstorage.BlockStoreProvider
	path               string
	orderer            bool
}

func NewBlockStore(path string, orderer bool) (*blockStore, error) {
	if len(path) == 0 {
		return nil, fmt.Errorf("empty path")
	}
	return &blockStore{path: path, orderer: orderer}, nil
}

func (s *blockStore) Open() error {
	if s.blockStoreProvider != nil {
		return fmt.Errorf("blockStore open")
	}
	if blockStoreProvider, err := getBlockStoreProvider(s.path, s.orderer); err != nil {
		return err
	} else {
		s.blockStoreProvider = blockStoreProvider
	}

	return nil
}

func (s *blockStore) Close() error {
	if s.blockStoreProvider == nil {
		return fmt.Errorf("blockStore closed")
	}
	s.blockStoreProvider.Close()
	s.blockStoreProvider = nil
	return nil
}

func (s *blockStore) ListChannels() ([]string, error) {
	if s.blockStoreProvider == nil {
		return nil, fmt.Errorf("blockStore closed")
	}

	return s.blockStoreProvider.List()
}

type ledger struct {
	parentProvider *blockStore
	blockStore     blkstorage.BlockStore
	channelID      string
}

func (s *blockStore) NewLedger(channelID string) (*ledger, error) {
	hasLedger := false
	channelIDs, err := s.ListChannels()
	if err != nil {
		return nil, err
	}
	for _, id := range channelIDs {
		if id == channelID {
			hasLedger = true
			break
		}
	}
	if !hasLedger {
		return nil, fmt.Errorf("missing channel: [%s] from [%s]", channelID, strings.Join(channelIDs, ","))
	}

	return &ledger{
		parentProvider: s,
		channelID:      channelID,
	}, nil
}

func (s *ledger) Open() error {
	if s.blockStore != nil {
		return fmt.Errorf("ledger open")
	}
	if blockStore, err := s.parentProvider.blockStoreProvider.OpenBlockStore(s.channelID); err != nil {
		return err
	} else {
		s.blockStore = blockStore
	}

	return nil
}

func (s *ledger) Close() error {
	if s.blockStore == nil {
		return fmt.Errorf("ledger closed")
	}
	s.blockStore.Shutdown()
	return nil
}

func (s *ledger) BlockHeight() (uint64, error) {
	if s.blockStore == nil {
		return 0, fmt.Errorf("ledger closed")
	}
	info, err := s.blockStore.GetBlockchainInfo()
	if err != nil {
		return 0, err
	}
	return info.GetHeight(), nil
}

type BlockIterator interface {
	Next() (CommonBlock, error)
	Close()
}

type ledgerBlockIterator struct {
	start uint64
	end   uint64
	iter  cledger.ResultsIterator
}

// Next returns the next block, or nil if there are no more blocks.
func (s *ledgerBlockIterator) Next() (*CommonBlock, error) {
	if s.iter == nil {
		return nil, fmt.Errorf("closed")
	}
	i := s.start
	if i >= s.end {
		return nil, nil
	}

	result, err := s.iter.Next()
	if err != nil {
		return nil, err
	}

	s.start++

	if result == nil {
		return nil, nil
	}

	block, ok := result.(*common.Block)
	if !ok {
		return nil, fmt.Errorf("cannot decode the block %d", i)
	}

	if block.Header.Number != i {
		return nil, fmt.Errorf("the next block is expected to be %d but got %d", i, block.Header.Number)
	}

	return &CommonBlock{*block}, nil
}

// Close frees the underlying resources.
func (s *ledgerBlockIterator) Close() {
	if s.iter == nil {
		return
	}
	s.iter.Close()
	s.iter = nil
}

// RangeBlocks returns an iterator for accessing blocks in the ledger for
// [start, end) range.
// If end == 0  or end > block height then it will iterate across all blocks.
// If the ledger is a snapshot, and start < the snapshot height, then
// it starts from the snapshot height.
func (s *ledger) RangeBlocks(start uint64, end uint64) (*ledgerBlockIterator, error) {
	if s.blockStore == nil {
		return nil, fmt.Errorf("ledger closed")
	}
	info, err := s.blockStore.GetBlockchainInfo()
	if err != nil {
		return nil, err
	}
	maxHeight := info.GetHeight()
	if end == 0 {
		end = maxHeight
	} else if end > maxHeight {
		end = maxHeight
	}
	firstBlockNumber := getFirstBlockNumberInLedger(info)
	if start < firstBlockNumber {
		start = firstBlockNumber
	}
	if start >= end {
		start = end
	}
	iterator, err := s.blockStore.RetrieveBlocks(start)
	if err != nil {
		return nil, err
	}
	return &ledgerBlockIterator{iter: iterator, start: start, end: end}, nil
}

// getFirstBlockNumberInLedger - Gets the number of the first block in the ledger.
// Usually it should be zero, but when the ledger is started from a snapshot,
// it should be the last block number in the snapshot.
func getFirstBlockNumberInLedger(info *common.BlockchainInfo) uint64 {
	snapshotInfo := info.GetBootstrappingSnapshotInfo()
	if snapshotInfo == nil {
		return uint64(0)
	}

	return snapshotInfo.GetLastBlockInSnapshot() + uint64(1)
}

func getBlockStorePath(fsPath string, orderer bool) (string, error) {
	var blockStorePath string
	if orderer {
		blockStorePath = filepath.Join(fsPath, ordererDataDirName)
	} else {
		// BlockStorePath(rootFSPath) returns filepath.Join(rootFSPath, "chains")
		blockStorePath = filepath.Join(fsPath, ledgersDataDirName, "chains")
	}
	isEmpty, err := dirEmpty(blockStorePath)
	if err != nil {
		return "", fmt.Errorf("dirEmpty: %w", err)
	}
	if isEmpty {
		return "", fmt.Errorf("path [%s] is empty", fsPath)
	}
	return blockStorePath, nil
}

// getBlockStoreProvider - Gets a default block store provider to access the peer's block store
func getBlockStoreProvider(fsPath string, orderer bool) (blkstorage.BlockStoreProvider, error) {
	blockStorePath, err := getBlockStorePath(fsPath, orderer)
	if err != nil {
		return nil, err
	}

	attrs := []blkstorage.IndexableAttr{
		blkstorage.IndexableAttrBlockNum,
	}
	if !orderer {
		attrs = append(attrs, []blkstorage.IndexableAttr{
			blkstorage.IndexableAttrBlockHash,
			blkstorage.IndexableAttrTxID,
			blkstorage.IndexableAttrBlockNumTranNum,
		}...)
	}

	conf := fsblkstorage.NewConf(blockStorePath, -1)
	indexConfig := &blkstorage.IndexConfig{
		AttrsToIndex: attrs,
	}
	metricsProvider := &disabled.Provider{}

	// Create new block store provider
	blockStoreProvider, err := fsblkstorage.NewProvider(conf, indexConfig, metricsProvider)
	if err != nil {
		return nil, err
	}

	return blockStoreProvider, nil
}

type simpleBlockIterator struct {
	iter       *ledgerBlockIterator
	ledger     *ledger
	blockStore *blockStore
}

func (s *simpleBlockIterator) Close() {
	if s.iter == nil {
		return
	}
	s.iter = nil
	s.iter.Close()
	s.ledger.Close()
	s.blockStore.Close()
}

func (s *simpleBlockIterator) Next() (*CommonBlock, error) {
	if s.iter == nil {
		return nil, fmt.Errorf("closed")
	}
	if blk, err := s.iter.Next(); err != nil {
		return nil, err
	} else {
		return blk, nil
	}
}

func RangeBlocks(ledgerDir string, orderer bool, channel string, start uint64, end uint64) (*simpleBlockIterator, error) {
	blockStore, err := NewBlockStore(ledgerDir, orderer)
	if err != nil {
		return nil, err
	}

	err = blockStore.Open()
	if err != nil {
		return nil, err
	}

	ledger, err := blockStore.NewLedger(channel)
	if err != nil {
		blockStore.Close()
		return nil, err
	}

	err = ledger.Open()
	if err != nil {
		blockStore.Close()
		return nil, err
	}

	iter, err := ledger.RangeBlocks(0, 0)
	if err != nil {
		ledger.Close()
		blockStore.Close()
		return nil, err
	}

	return &simpleBlockIterator{
		iter:       iter,
		ledger:     ledger,
		blockStore: blockStore,
	}, nil
}
