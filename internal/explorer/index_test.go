package explorer

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDecodeTransaction(t *testing.T) {
	ix := testIndex(t)

	tx, ok := ix.Transaction("tx-a")
	require.True(t, ok)
	assert.Equal(t, "create_account", tx.Method)
	assert.Equal(t, "ENDORSER_TRANSACTION", tx.Type)
	assert.Equal(t, "VALID", tx.ValidationCode)
	assert.True(t, tx.Valid)
	assert.Equal(t, "Org1MSP", tx.CreatorMSP)
	assert.Equal(t, []Identity{{MSPID: "Org1MSP"}, {MSPID: "Org2MSP"}}, tx.Endorsers)
	assert.JSONEq(t, call("create_account"), string(tx.Request))
	require.NotNil(t, tx.Response)
	assert.Equal(t, int32(200), tx.Response.Status)
	assert.Equal(t, `{"ok":true}`, tx.Response.Payload.Data)
	require.NotNil(t, tx.Event)
	assert.Equal(t, "luther", tx.Event.Name)
	require.Len(t, tx.RWSets, 1)
	assert.Equal(t, testNamespace, tx.RWSets[0].Namespace)
	assert.Equal(t, []Write{{Key: "account:1", Value: &Value{Encoding: "utf8", Data: "100", Size: 3}}}, tx.RWSets[0].Writes)
	assert.Equal(t, []PrivateWrite{{Collection: "private", KeyHash: "0102", ValueHash: "03"}}, tx.RWSets[0].PrivateWrites)

	conflict, ok := ix.Transaction("tx-conflict")
	require.True(t, ok)
	assert.False(t, conflict.Valid)
	assert.Equal(t, "MVCC_READ_CONFLICT", conflict.ValidationCode)
	assert.Equal(t, []Read{{Key: "account:1", Version: &Version{BlockNum: 1}}}, conflict.RWSets[0].Reads)

	batch, ok := ix.Transaction("tx-batch")
	require.True(t, ok)
	assert.Equal(t, "batch", batch.Method)
	assert.Equal(t, []string{"a", "b"}, batch.BatchMethods)

	raw, ok := ix.Transaction("tx-raw")
	require.True(t, ok)
	assert.Empty(t, raw.Method)
	assert.Nil(t, raw.Request)
	assert.Equal(t, []Value{{Encoding: "utf8", Data: "other", Size: 5}, {Encoding: "base64", Data: "/wA=", Size: 2}}, raw.Args)
	assert.Equal(t, []byte{0xff, 0x00}, raw.Args[1].Bytes())
}

func TestBlocks(t *testing.T) {
	ix := testIndex(t)
	height, ok := ix.Height()
	require.True(t, ok)
	assert.Equal(t, uint64(8), height)

	genesis, ok := ix.Block(0)
	require.True(t, ok)
	assert.True(t, genesis.Config)
	assert.Equal(t, "CONFIG", genesis.Transactions[0].Type)

	b2, ok := ix.Block(2)
	require.True(t, ok)
	assert.Equal(t, 2, b2.TxCount)
	assert.Equal(t, 1, b2.ValidTxCount)
	require.NotNil(t, b2.Time)
	assert.Equal(t, testTime.Add(2e9), *b2.Time)

	_, ok = ix.Block(8)
	assert.False(t, ok)

	page := ix.Blocks(0, 3)
	require.Len(t, page, 3)
	assert.Equal(t, []uint64{7, 6, 5}, []uint64{page[0].Number, page[1].Number, page[2].Number})
	page = ix.Blocks(2, 10)
	require.Len(t, page, 2)
	assert.Equal(t, uint64(1), page[0].Number)

	from := ix.BlocksFrom(6, 10)
	require.Len(t, from, 2)
	assert.Equal(t, uint64(6), from[0].Number)
}

func TestAddOrder(t *testing.T) {
	chain := testChain(t)
	ix := NewIndex("luther")
	// A peer that joined from a snapshot starts after block 0.
	require.NoError(t, ix.Add(chain[3]))
	assert.Equal(t, uint64(3), ix.First())
	// A resent block is ignored; a gap is refused.
	require.NoError(t, ix.Add(chain[3]))
	require.Error(t, ix.Add(chain[5]))
	changed := ix.Changed()
	require.NoError(t, ix.Add(chain[4]))
	select {
	case <-changed:
	default:
		t.Fatal("Add did not signal a change")
	}
	height, _ := ix.Height()
	assert.Equal(t, uint64(5), height)
	assert.Equal(t, []BlockSummary{}, NewIndex("x").Blocks(0, 5))
}

func TestTransactionsFilter(t *testing.T) {
	ix := testIndex(t)
	all := ix.Transactions(TxFilter{Limit: 100})
	assert.Len(t, all, 9)
	assert.Equal(t, "tx-raw", all[0].ID)

	transfers := ix.Transactions(TxFilter{Method: "transfer", Limit: 100})
	require.Len(t, transfers, 2)
	assert.Equal(t, "tx-b", transfers[0].ID)

	invalid := false
	bad := ix.Transactions(TxFilter{Valid: &invalid, Limit: 100})
	require.Len(t, bad, 1)
	assert.Equal(t, "tx-conflict", bad[0].ID)

	inBatch := ix.Transactions(TxFilter{Method: "b", Limit: 100})
	require.Len(t, inBatch, 1)
	assert.Equal(t, "tx-batch", inBatch[0].ID)

	older := ix.Transactions(TxFilter{Before: 3, Limit: 2})
	require.Len(t, older, 2)
	assert.Equal(t, "tx-conflict", older[0].ID)
}

func TestKeyHistory(t *testing.T) {
	ix := testIndex(t)
	hist := ix.KeyHistory(testNamespace, "account:1")
	// The invalid transaction's write is not in the history.
	require.Len(t, hist, 3)
	assert.Equal(t, "tx-a", hist[0].TxID)
	assert.Nil(t, hist[0].Before)
	assert.Equal(t, "100", hist[0].After.Data)
	assert.Equal(t, "tx-b", hist[1].TxID)
	assert.Equal(t, "100", hist[1].Before.Data)
	assert.Equal(t, "80", hist[1].After.Data)
	assert.Equal(t, "tx-batch", hist[2].TxID)
	assert.True(t, hist[2].IsDelete)
	assert.Equal(t, "80", hist[2].Before.Data)
	assert.Nil(t, hist[2].After)

	assert.Empty(t, ix.KeyHistory(testNamespace, "nope"))
}

func TestFrameworkKeys(t *testing.T) {
	assert.Equal(t, "", frameworkSpaceOf("account:1"))
	assert.Equal(t, "", frameworkSpaceOf("lutherx:a"))
	assert.Equal(t, "", frameworkSpaceOf("Luther:a"))
	assert.Equal(t, "luther:", frameworkSpaceOf("luther:vbc:tx:1"))
	assert.Equal(t, "luther:flow:", frameworkSpaceOf("luther:flow:run:F:h:R"))
	assert.Equal(t, "luther:app_control", frameworkSpaceOf("luther:app_control"))
	assert.Equal(t, "luther:app_control:", frameworkSpaceOf("luther:app_control:distinct:substrate_x"))
	assert.Equal(t, "luther:", frameworkSpaceOf("luther:app_controlx"))
	assert.Equal(t, "luther:batchreq:", frameworkSpaceOf("luther:batchreq:1"))
	assert.Equal(t, "utl:migration-ctx:", frameworkSpaceOf("utl:migration-ctx:a"))

	ix := testIndex(t)
	spaces, phylum := ix.FrameworkSummary()
	require.Len(t, spaces, len(frameworkSpaces))
	counts := map[string][3]int{}
	for _, s := range spaces {
		counts[s.Prefix] = [3]int{s.Keys, s.LiveKeys, s.Writes}
	}
	assert.Equal(t, [3]int{2, 2, 4}, counts["luther:phylum:"])
	assert.Equal(t, [3]int{1, 1, 1}, counts["luther:app_control"])
	assert.Equal(t, [3]int{1, 1, 2}, counts["luther:batch:"])
	assert.Equal(t, [3]int{0, 0, 0}, counts["luther:flow:"])
	assert.Equal(t, 1, phylum.Keys)
	assert.Equal(t, 0, phylum.LiveKeys)
	assert.Equal(t, 3, phylum.Writes)

	fw := ix.Keys(KeyFilter{Kind: "framework", Limit: 100})
	var names []string
	for _, k := range fw {
		names = append(names, k.Key)
	}
	assert.Equal(t, []string{"luther:app_control", "luther:batch:queue:q", "luther:phylum:desc:v1", "luther:phylum:src:v1"}, names)

	ph := ix.Keys(KeyFilter{Kind: "phylum", Limit: 100})
	require.Len(t, ph, 1)
	assert.Equal(t, "account:1", ph[0].Key)
	assert.True(t, ph[0].IsDelete)
	assert.Equal(t, 3, ph[0].Writes)

	batch := ix.Keys(KeyFilter{Space: "luther:batch:", Limit: 100})
	require.Len(t, batch, 1)
	assert.Equal(t, "tx-batch", batch[0].LastWrite.TxID)

	page := ix.Keys(KeyFilter{Limit: 2})
	require.Len(t, page, 2)
	rest := ix.Keys(KeyFilter{AfterNamespace: page[1].Namespace, AfterKey: page[1].Key, Limit: 100})
	assert.Len(t, rest, 3)
	assert.Equal(t, "luther:batch:queue:q", rest[0].Key)

	assert.Len(t, ix.Keys(KeyFilter{Prefix: "luther:phylum:", Limit: 100}), 2)
}

func TestPhylumVersions(t *testing.T) {
	ix := testIndex(t)
	versions := ix.PhylumVersions()
	require.Len(t, versions, 1)
	v := versions[0]
	assert.Equal(t, testNamespace, v.Namespace)
	assert.Equal(t, "v1", v.Version)
	assert.Equal(t, "IN_SERVICE", v.Status)
	assert.Equal(t, "sha512-abc", v.Fingerprint)
	assert.Equal(t, "2026-11-14T05:00:01Z", v.InitTimestamp)
	assert.Equal(t, len("source bytes"), v.SourceSize)
	var actions, methods []string
	for _, e := range v.Events {
		actions = append(actions, e.Action)
		methods = append(methods, e.Method)
	}
	assert.Equal(t, []string{"install", "disable", "enable"}, actions)
	assert.Equal(t, []string{"update", "disable", "enable"}, methods)
}

// TestLedgerSource reads a substrate network's block files (testdata) and
// checks the views decode its phylum install and its calls.
func TestLedgerSource(t *testing.T) {
	ix := NewIndex("luther")
	f := NewFollower(&LedgerSource{Dir: "../../testdata/sandbox_peer0", Channel: "luther"}, ix)
	require.NoError(t, f.Run(context.Background()))
	assert.True(t, f.Status().Done)
	height, _ := ix.Height()
	assert.Equal(t, uint64(8), height)

	versions := ix.PhylumVersions()
	require.Len(t, versions, 1)
	assert.Equal(t, "local-1709855909", versions[0].Version)
	assert.Equal(t, "IN_SERVICE", versions[0].Status)
	require.Len(t, versions[0].Events, 1)
	assert.Equal(t, "install", versions[0].Events[0].Action)
	assert.Equal(t, "update", versions[0].Events[0].Method)

	txs := ix.Transactions(TxFilter{Method: "transfer", Limit: 10})
	require.Len(t, txs, 1)
	tx, ok := ix.Transaction(txs[0].ID)
	require.True(t, ok)
	require.NotNil(t, tx.Creator)
	assert.Equal(t, "Admin@org1.luther.systems", tx.Creator.Subject)
	require.Len(t, tx.Endorsers, 1)
	assert.Equal(t, "peer0.org1.luther.systems", tx.Endorsers[0].Subject)
}
