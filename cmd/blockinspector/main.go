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

var CLI struct {
	RangePeer    Range `cmd:"" help:"Range blocks on peer ledger."`
	RangeOrderer Range `cmd:"" help:"Range blocks on orderer ledger."`
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

func main() {
	ctx := kong.Parse(&CLI)
	switch ctx.Command() {
	case "range-peer <ledger-dir> <channel> <start> <end>":
		rangeBlock(false, CLI.RangePeer.LedgerDir, CLI.RangePeer.Channel, CLI.RangePeer.Start, CLI.RangePeer.End, CLI.RangePeer.OutputCSV)
	case "range-orderer <ledger-dir> <channel> <start> <end>":
		rangeBlock(true, CLI.RangeOrderer.LedgerDir, CLI.RangeOrderer.Channel, CLI.RangeOrderer.Start, CLI.RangeOrderer.End, CLI.RangePeer.OutputCSV)
	default:
		fmt.Println(ctx.Command())
	}
}
