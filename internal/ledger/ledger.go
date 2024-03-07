package ledger

import (
	"fmt"
	"path/filepath"

	"github.com/luthersystems/blockinspector/internal/fileutil"

	"github.com/hyperledger/fabric-protos-go/common"
	cledger "github.com/hyperledger/fabric/common/ledger"
	"github.com/hyperledger/fabric/common/ledger/blkstorage"
	"github.com/hyperledger/fabric/common/metrics/disabled"
	"github.com/hyperledger/fabric/core/ledger/kvledger"
)

const (
	ledgersDataDirName = "ledgersData"
)

type blockStore struct {
	blockStoreProvider *blkstorage.BlockStoreProvider
	path               string
}

func NewBlockStore(path string) (*blockStore, error) {
	if len(path) == 0 {
		return nil, fmt.Errorf("empty path")
	}
	return &blockStore{path: path}, nil
}

func (s *blockStore) Open() error {
	if s.blockStoreProvider != nil {
		return fmt.Errorf("blockStore open")
	}
	if blockStoreProvider, err := getBlockStoreProvider(s.path); err != nil {
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
	blockStore     *blkstorage.BlockStore
	channelID      string
}

func (s *blockStore) NewLedger(channelID string) (*ledger, error) {
	hasLedger := false
	ledgerIDs, err := s.ListChannels()
	if err != nil {
		return nil, err
	}
	for _, id := range ledgerIDs {
		if id == channelID {
			hasLedger = true
			break
		}
	}
	if !hasLedger {
		return nil, fmt.Errorf("missing channel: %s", channelID)
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
	if blockStore, err := s.parentProvider.blockStoreProvider.Open(s.channelID); err != nil {
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

type BlockIterator struct {
	start uint64
	end   uint64
	iter  cledger.ResultsIterator
}

// Next returns the next block, or nil if there are no more blocks.
func (s *BlockIterator) Next() (*common.Block, error) {
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

	return block, nil
}

// Close frees the underlying resources.
func (s *BlockIterator) Close() {
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
func (s *ledger) RangeBlocks(start uint64, end uint64) (*BlockIterator, error) {
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
	return &BlockIterator{iter: iterator, start: start, end: end}, nil
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

// getBlockStoreProvider - Gets a default block store provider to access the peer's block store
func getBlockStoreProvider(fsPath string) (*blkstorage.BlockStoreProvider, error) {
	// Format path to block store
	blockStorePath := kvledger.BlockStorePath(filepath.Join(fsPath, ledgersDataDirName))
	isEmpty, err := fileutil.DirEmpty(blockStorePath)
	if err != nil {
		return nil, err
	}
	if isEmpty {
		return nil, fmt.Errorf("provided path %s is empty. Aborting verify", fsPath)
	}
	// Default fields for block store provider
	conf := blkstorage.NewConf(blockStorePath, 0)
	indexConfig := &blkstorage.IndexConfig{
		AttrsToIndex: []blkstorage.IndexableAttr{
			blkstorage.IndexableAttrBlockNum,
			blkstorage.IndexableAttrBlockHash,
			blkstorage.IndexableAttrTxID,
			blkstorage.IndexableAttrBlockNumTranNum,
		},
	}
	metricsProvider := &disabled.Provider{}
	// Create new block store provider
	blockStoreProvider, err := blkstorage.NewProvider(conf, indexConfig, metricsProvider)
	if err != nil {
		return nil, err
	}

	return blockStoreProvider, nil
}
