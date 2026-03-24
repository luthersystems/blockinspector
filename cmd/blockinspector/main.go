package main

import (
	"encoding/csv"
	"fmt"
	"os"

	"github.com/luthersystems/blockinspector/internal/ledger"

	"github.com/alecthomas/kong"
)

type Range struct {
	LedgerDir string `arg:"" required:"" placeholder:"LEDGERDIR" help:"Directory containing ledger data."`
	Channel   string `arg:"" required:"" placeholder:"CHANNEL" help:"Channel name."`
	Start     uint64 `arg:"" required:"" placeholder:"START" help:"Start block number (inclusive)."`
	End       uint64 `arg:"" required:"" placeholder:"END" help:"End block number (exclusive), or 0 to iterate all blocks."`
	OutputCSV bool   `optional:"" name:"output-csv" help:"Flag to output in CSV format to stdout."`
}

type Snapshot struct {
	SnapshotFile string `arg:"" required:"" placeholder:"SNAPSHOTFILE" help:"Path to Luther snapshot file (Badger DB backup)."`
	OutputCSV    bool   `optional:"" name:"output-csv" help:"Flag to output in CSV format to stdout."`
}

type PvtdataKeys struct {
	LedgerPath    string `arg:"" required:"" placeholder:"LEDGERPATH" help:"Path to peer ledger data (e.g., /var/hyperledger/production)."`
	Channel       string `arg:"" required:"" placeholder:"CHANNEL" help:"Channel name."`
	Collection    string `optional:"" name:"collection" help:"Filter by collection name."`
	SortByVersion bool   `optional:"" name:"sort-by-version" help:"Sort by version (block number) ascending."`
	Limit         int    `optional:"" name:"limit" default:"0" help:"Limit output to N results (0 = all)."`
	OutputCSV     bool   `optional:"" name:"output-csv" help:"Flag to output in CSV format to stdout."`
}

var CLI struct {
	RangePeer    Range       `cmd:"" help:"Range blocks on peer ledger."`
	RangeOrderer Range       `cmd:"" help:"Range blocks on orderer ledger."`
	Snapshot     Snapshot    `cmd:"" help:"Inspect Luther snapshot ledger (Badger DB backup)."`
	PvtdataKeys  PvtdataKeys `cmd:"" name:"pvtdata-keys" help:"List private data collection keys with version info from peer state DB."`
}

func rangeBlock(orderer bool, dir string, channel string, start uint64, end uint64, outputCSV bool) {
	iter, err := ledger.RangeBlocks(dir, orderer, channel, start, end)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to range: %v\n", err)
		os.Exit(1)
	}

	var csvWriter *csv.Writer
	if outputCSV {
		csvWriter = csv.NewWriter(os.Stdout)
		defer csvWriter.Flush()

		if err := csvWriter.Write(ledger.GetCSVBlockHeader()); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to write header: %v\n", err)
		}
	}

	for {
		block, err := iter.Next()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to get block iterator: %v\n", err)
			os.Exit(1)
		}
		if block == nil {
			break
		}

		if csvWriter != nil {
			block.WriteCSV(csvWriter)
		} else {
			block.Write(os.Stdout)
		}
	}
}

func inspectSnapshot(snapshotFile string, outputCSV bool) {
	snapshot, err := ledger.LoadSnapshotFromFile(snapshotFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load snapshot: %v\n", err)
		os.Exit(1)
	}

	var csvWriter *csv.Writer
	if outputCSV {
		csvWriter = csv.NewWriter(os.Stdout)
		defer csvWriter.Flush()

		if err := csvWriter.Write(ledger.GetCSVLedgerViewHeader()); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to write header: %v\n", err)
		}
	}

	if csvWriter != nil {
		snapshot.WriteCSV(csvWriter)
	} else {
		snapshot.Write(os.Stdout)
	}
}

func inspectPvtdataKeys(cmd PvtdataKeys) {
	dbPath := ledger.StateLevelDBPath(cmd.LedgerPath)
	db, err := ledger.OpenStateLevelDB(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to open state DB: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	entries, err := ledger.ReadPrivateDataKeys(db, cmd.Channel, ledger.PvtDataKeysOptions{
		Collection:    cmd.Collection,
		SortByVersion: cmd.SortByVersion,
		Limit:         cmd.Limit,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to read private data keys: %v\n", err)
		os.Exit(1)
	}

	if cmd.OutputCSV {
		w := csv.NewWriter(os.Stdout)
		defer w.Flush()
		if err := w.Write(ledger.GetCSVPvtDataKeysHeader()); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to write header: %v\n", err)
		}
		ledger.WritePvtDataKeysCSV(w, entries)
	} else {
		ledger.WritePvtDataKeysTable(os.Stdout, entries)
	}
}

func main() {
	ctx := kong.Parse(&CLI)
	switch ctx.Command() {
	case "range-peer <ledger-dir> <channel> <start> <end>":
		rangeBlock(false, CLI.RangePeer.LedgerDir, CLI.RangePeer.Channel, CLI.RangePeer.Start, CLI.RangePeer.End, CLI.RangePeer.OutputCSV)
	case "range-orderer <ledger-dir> <channel> <start> <end>":
		rangeBlock(true, CLI.RangeOrderer.LedgerDir, CLI.RangeOrderer.Channel, CLI.RangeOrderer.Start, CLI.RangeOrderer.End, CLI.RangeOrderer.OutputCSV)
	case "snapshot <snapshot-file>":
		inspectSnapshot(CLI.Snapshot.SnapshotFile, CLI.Snapshot.OutputCSV)
	case "pvtdata-keys <ledger-path> <channel>":
		inspectPvtdataKeys(CLI.PvtdataKeys)
	default:
		fmt.Println(ctx.Command())
	}
}
