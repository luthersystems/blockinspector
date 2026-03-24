/*
Copyright IBM Corp. 2017 All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

// Copied from github.com/hyperledger/fabric@v2.1.1
// common/ledger/blkstorage/fsblkstorage/fs_blockstore_provider.go

package fsblkstorage

import (
	"os"

	"github.com/luthersystems/blockinspector/internal/blkstorage"
	"github.com/luthersystems/blockinspector/internal/fabricmetrics"
	"github.com/luthersystems/blockinspector/internal/fabricutil"
	"github.com/luthersystems/blockinspector/internal/fabricutil/leveldbhelper"
	"github.com/pkg/errors"
)

const (
	dataformatVersion1x = ""
	dataformatVersion20 = "2.0"
)

func dataFormatVersion(indexConfig *blkstorage.IndexConfig) string {
	if indexConfig.Contains(blkstorage.IndexableAttrTxID) {
		return dataformatVersion20
	}
	return dataformatVersion1x
}

// FsBlockstoreProvider provides handle to block storage - this is not thread-safe
type FsBlockstoreProvider struct {
	conf            *Conf
	indexConfig     *blkstorage.IndexConfig
	leveldbProvider *leveldbhelper.Provider
	stats           *stats
}

// NewProvider constructs a filesystem based block store provider
func NewProvider(conf *Conf, indexConfig *blkstorage.IndexConfig, metricsProvider fabricmetrics.Provider) (blkstorage.BlockStoreProvider, error) {
	dbConf := &leveldbhelper.Conf{
		DBPath:                conf.getIndexDir(),
		ExpectedFormatVersion: dataFormatVersion(indexConfig),
	}

	p, err := leveldbhelper.NewProvider(dbConf)
	if err != nil {
		return nil, err
	}

	dirPath := conf.getChainsDir()
	if _, err := os.Stat(dirPath); err != nil {
		if !os.IsNotExist(err) {
			return nil, errors.Wrapf(err, "failed to read ledger directory %s", dirPath)
		}

		logger.Info("Creating new file ledger directory at", dirPath)
		if err = os.MkdirAll(dirPath, 0755); err != nil {
			return nil, errors.Wrapf(err, "failed to create ledger directory: %s", dirPath)
		}
	}

	stats := newStats(metricsProvider)
	return &FsBlockstoreProvider{conf, indexConfig, p, stats}, nil
}

// CreateBlockStore simply calls OpenBlockStore
func (p *FsBlockstoreProvider) CreateBlockStore(ledgerid string) (blkstorage.BlockStore, error) {
	return p.OpenBlockStore(ledgerid)
}

// OpenBlockStore opens a block store for given ledgerid.
func (p *FsBlockstoreProvider) OpenBlockStore(ledgerid string) (blkstorage.BlockStore, error) {
	indexStoreHandle := p.leveldbProvider.GetDBHandle(ledgerid)
	return newFsBlockStore(ledgerid, p.conf, p.indexConfig, indexStoreHandle, p.stats), nil
}

// Exists tells whether the BlockStore with given id exists
func (p *FsBlockstoreProvider) Exists(ledgerid string) (bool, error) {
	exists, _, err := fabricutil.FileExists(p.conf.getLedgerBlockDir(ledgerid))
	return exists, err
}

// List lists the ids of the existing ledgers
func (p *FsBlockstoreProvider) List() ([]string, error) {
	return fabricutil.ListSubdirs(p.conf.getChainsDir())
}

// Close closes the FsBlockstoreProvider
func (p *FsBlockstoreProvider) Close() {
	p.leveldbProvider.Close()
}
