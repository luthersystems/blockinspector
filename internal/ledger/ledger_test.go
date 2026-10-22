package ledger

import (
	"fmt"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	TestDataDir         = "../../testdata/"
	SampleGoodLedgerDir = TestDataDir + "sample_prod/"
)

func TestLedger(t *testing.T) {
	expectChannel := "mychannel"
	expectHeight := uint64(8)
	ledgerDir := SampleGoodLedgerDir

	bs, err := NewBlockStore(ledgerDir, false)
	require.NoError(t, err)

	err = bs.Open()
	require.NoError(t, err)

	defer bs.Close()

	channels, err := bs.ListChannels()
	require.NoError(t, err)

	t.Logf("Found channels: %v", channels)
	require.Contains(t, channels, expectChannel, fmt.Sprintf("expected channel %s to be in list: %v", expectChannel, channels))
	channel := expectChannel

	ledger, err := bs.NewLedger(channel)
	require.NoError(t, err)

	err = ledger.Open()
	require.NoError(t, err)

	height, err := ledger.BlockHeight()
	require.NoError(t, err)

	require.Equal(t, height, expectHeight)

	iter, err := ledger.RangeBlocks(0, 0)
	require.NoError(t, err)

	defer iter.Close()

	numBlocks := uint64(0)
	for {
		block, err := iter.Next()
		require.NoError(t, err)
		if block == nil {
			break
		}
		numBlocks++
	}

	require.Equal(t, numBlocks, expectHeight)
}

func TestRangeBlocks(t *testing.T) {
	channel := "mychannel"
	expectHeight := uint64(8)
	ledgerDir := SampleGoodLedgerDir

	iter, err := RangeBlocks(ledgerDir, false, channel, 0, 0)
	require.NoError(t, err)

	numBlocks := uint64(0)
	for {
		block, err := iter.Next()
		require.NoError(t, err)
		if block == nil {
			break
		}
		require.NoError(t, block.Write(os.Stderr))
		fmt.Fprintln(os.Stderr)

		numBlocks++
	}

	require.Equal(t, numBlocks, expectHeight)
}
