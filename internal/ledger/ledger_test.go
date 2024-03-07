package ledger

import (
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	TestDataDir         = "../testdata/"
	SampleGoodLedgerDir = TestDataDir + "sample_prod/"
)

func TestLedger(t *testing.T) {
	bs, err := NewBlockStore(SampleGoodLedgerDir)
	require.NoError(t, err)

	ledgerIDs, err := bs.List()
	require.NoError(t, err)

	require.Len(t, ledgerIDs, 1, "expected single ledger")
}
