package main

import (
	"encoding/csv"
	"fmt"
	"os"

	internalledger "github.com/luthersystems/blockinspector/internal/ledger"
	"github.com/luthersystems/blockinspector/ledger"

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

type Serve struct {
	Channel     string `optional:"" name:"channel" default:"luther" help:"Channel to read."`
	Listen      string `optional:"" name:"listen" default:"127.0.0.1:8090" help:"Address the HTTP API listens on."`
	AllowOrigin string `optional:"" name:"allow-origin" help:"Send this Access-Control-Allow-Origin, for a UI on another origin."`

	NetworkDir  string `optional:"" name:"network-dir" placeholder:"DIR" help:"fabric-network-builder directory (the one with crypto-config/). Reads the peer live over mutual TLS."`
	Org         string `optional:"" name:"org" help:"Org of the client identity and the peer (default: the network's first org)."`
	Peer        string `optional:"" name:"peer" default:"peer0" help:"Peer host name in the org."`
	User        string `optional:"" name:"user" default:"User1" help:"User whose TLS and signing identity the client uses."`
	PeerAddress string `optional:"" name:"peer-address" default:"127.0.0.1:7051" help:"Peer host:port."`

	ServerName string `optional:"" name:"server-name" help:"Name in the peer's TLS certificate (overrides --network-dir)."`
	TLSRootCA  string `optional:"" name:"tls-root-ca" type:"path" help:"PEM CA that signed the peer's TLS certificate (overrides --network-dir)."`
	TLSCert    string `optional:"" name:"tls-cert" type:"path" help:"Client TLS certificate (overrides --network-dir)."`
	TLSKey     string `optional:"" name:"tls-key" type:"path" help:"Client TLS key (overrides --network-dir)."`
	MSPID      string `optional:"" name:"msp-id" help:"MSP ID of the signing identity (overrides --network-dir)."`
	SignCert   string `optional:"" name:"sign-cert" type:"path" help:"Signing certificate (overrides --network-dir)."`
	SignKey    string `optional:"" name:"sign-key" type:"path" help:"Signing key (overrides --network-dir)."`

	LedgerDir string `optional:"" name:"ledger-dir" placeholder:"DIR" help:"Read block files offline from this peer or orderer data directory, in place of a peer."`
	Orderer   bool   `optional:"" name:"orderer" help:"--ledger-dir is an orderer's."`
}

var CLI struct {
	RangePeer    Range       `cmd:"" help:"Range blocks on peer ledger."`
	RangeOrderer Range       `cmd:"" help:"Range blocks on orderer ledger."`
	Snapshot     Snapshot    `cmd:"" help:"Inspect Luther snapshot ledger (Badger DB backup)."`
	PvtdataKeys  PvtdataKeys `cmd:"" name:"pvtdata-keys" help:"List private data collection keys with version info from peer state DB."`
	Serve        Serve       `cmd:"" help:"Serve the block explorer API (JSON and a server-sent events stream of new blocks)."`
}

func rangeBlock(orderer bool, dir string, channel string, start uint64, end uint64, outputCSV bool) {
	iter, err := internalledger.RangeBlocks(dir, orderer, channel, start, end)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to range: %v\n", err)
		os.Exit(1)
	}

	var csvWriter *csv.Writer
	if outputCSV {
		csvWriter = csv.NewWriter(os.Stdout)
		defer csvWriter.Flush()

		if err := csvWriter.Write(internalledger.GetCSVBlockHeader()); err != nil {
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
			if err := block.Write(os.Stdout); err != nil {
				fmt.Fprintf(os.Stderr, "Failed to write block: %v\n", err)
				os.Exit(1)
			}
		}
	}
}

func inspectSnapshot(snapshotFile string, outputCSV bool) {
	snapshot, err := internalledger.LoadSnapshotFromFile(snapshotFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load snapshot: %v\n", err)
		os.Exit(1)
	}

	var csvWriter *csv.Writer
	if outputCSV {
		csvWriter = csv.NewWriter(os.Stdout)
		defer csvWriter.Flush()

		if err := csvWriter.Write(internalledger.GetCSVLedgerViewHeader()); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to write header: %v\n", err)
		}
	}

	if csvWriter != nil {
		snapshot.WriteCSV(csvWriter)
	} else {
		if err := snapshot.Write(os.Stdout); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to write snapshot: %v\n", err)
			os.Exit(1)
		}
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
		if err := ledger.WritePvtDataKeysTable(os.Stdout, entries); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to write private data keys: %v\n", err)
			os.Exit(1)
		}
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
	case "serve":
		if err := serve(CLI.Serve); err != nil {
			fmt.Fprintf(os.Stderr, "serve: %v\n", err)
			os.Exit(1)
		}
	default:
		fmt.Println(ctx.Command())
	}
}
