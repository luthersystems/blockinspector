/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

// Package blkstorage is a copy of github.com/hyperledger/fabric@v2.1.1
// common/ledger/blkstorage. This avoids depending on the full Fabric module
// which has unresolvable CVEs and broken Go module paths.
package blkstorage

import (
	"github.com/hyperledger/fabric-protos-go/common"
	"github.com/hyperledger/fabric-protos-go/peer"
	"github.com/pkg/errors"
)

// ResultsIterator - an iterator for query result set.
// Copied from github.com/hyperledger/fabric@v2.1.1 common/ledger.
type ResultsIterator interface {
	Next() (QueryResult, error)
	Close()
}

// QueryResult - a general interface for supporting different types of query results.
type QueryResult interface{}

// NotFoundInIndexErr is used to indicate missing entry in the index.
// Copied from github.com/hyperledger/fabric@v2.1.1 core/ledger.
type NotFoundInIndexErr string

func (NotFoundInIndexErr) Error() string {
	return "Entry not found in index"
}

// IndexableAttr represents an indexable attribute
type IndexableAttr string

// constants for indexable attributes
const (
	IndexableAttrBlockNum        = IndexableAttr("BlockNum")
	IndexableAttrBlockHash       = IndexableAttr("BlockHash")
	IndexableAttrTxID            = IndexableAttr("TxID")
	IndexableAttrBlockNumTranNum = IndexableAttr("BlockNumTranNum")
)

// IndexConfig - a configuration that includes a list of attributes that should be indexed
type IndexConfig struct {
	AttrsToIndex []IndexableAttr
}

// Contains returns true iff the supplied parameter is present in the IndexConfig.AttrsToIndex
func (c *IndexConfig) Contains(indexableAttr IndexableAttr) bool {
	for _, a := range c.AttrsToIndex {
		if a == indexableAttr {
			return true
		}
	}
	return false
}

var (
	// ErrNotFoundInIndex is used to indicate missing entry in the index
	ErrNotFoundInIndex = NotFoundInIndexErr("")

	// ErrAttrNotIndexed is used to indicate that an attribute is not indexed
	ErrAttrNotIndexed = errors.New("attribute not indexed")
)

// BlockStoreProvider provides an handle to a BlockStore
type BlockStoreProvider interface {
	CreateBlockStore(ledgerid string) (BlockStore, error)
	OpenBlockStore(ledgerid string) (BlockStore, error)
	Exists(ledgerid string) (bool, error)
	List() ([]string, error)
	Close()
}

// BlockStore - an interface for persisting and retrieving blocks
type BlockStore interface {
	AddBlock(block *common.Block) error
	GetBlockchainInfo() (*common.BlockchainInfo, error)
	RetrieveBlocks(startNum uint64) (ResultsIterator, error)
	RetrieveBlockByHash(blockHash []byte) (*common.Block, error)
	RetrieveBlockByNumber(blockNum uint64) (*common.Block, error)
	RetrieveTxByID(txID string) (*common.Envelope, error)
	RetrieveTxByBlockNumTranNum(blockNum uint64, tranNum uint64) (*common.Envelope, error)
	RetrieveBlockByTxID(txID string) (*common.Block, error)
	RetrieveTxValidationCodeByTxID(txID string) (peer.TxValidationCode, error)
	Shutdown()
}
