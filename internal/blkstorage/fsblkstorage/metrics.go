/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

// Copied from github.com/hyperledger/fabric@v2.1.1
// common/ledger/blkstorage/fsblkstorage/metrics.go

package fsblkstorage

import (
	"time"

	"github.com/luthersystems/blockinspector/internal/fabricmetrics"
)

type stats struct {
	blockchainHeight       fabricmetrics.Gauge
	blockstorageCommitTime fabricmetrics.Histogram
}

func newStats(metricsProvider fabricmetrics.Provider) *stats {
	stats := &stats{}
	stats.blockchainHeight = metricsProvider.NewGauge(blockchainHeightOpts)
	stats.blockstorageCommitTime = metricsProvider.NewHistogram(blockstorageCommitTimeOpts)
	return stats
}

type ledgerStats struct {
	stats    *stats
	ledgerid string
}

func (s *stats) ledgerStats(ledgerid string) *ledgerStats {
	return &ledgerStats{s, ledgerid}
}

func (s *ledgerStats) updateBlockchainHeight(height uint64) {
	s.stats.blockchainHeight.With("channel", s.ledgerid).Set(float64(height))
}

func (s *ledgerStats) updateBlockstorageCommitTime(timeTaken time.Duration) {
	s.stats.blockstorageCommitTime.With("channel", s.ledgerid).Observe(timeTaken.Seconds())
}

var (
	blockchainHeightOpts = fabricmetrics.GaugeOpts{
		Namespace:    "ledger",
		Name:         "blockchain_height",
		Help:         "Height of the chain in blocks.",
		LabelNames:   []string{"channel"},
		StatsdFormat: "%{#fqname}.%{channel}",
	}

	blockstorageCommitTimeOpts = fabricmetrics.HistogramOpts{
		Namespace:    "ledger",
		Name:         "blockstorage_commit_time",
		Help:         "Time taken in seconds for committing the block to storage.",
		LabelNames:   []string{"channel"},
		StatsdFormat: "%{#fqname}.%{channel}",
		Buckets:      []float64{0.005, 0.01, 0.015, 0.05, 0.1, 1, 10},
	}
)
