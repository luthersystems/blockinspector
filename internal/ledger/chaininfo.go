package ledger

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"io"

	"github.com/golang/protobuf/proto" //nolint SA1019 hlf does not support latest protobuf
	cb "github.com/hyperledger/fabric-protos-go/common"
	fabricpeer "github.com/hyperledger/fabric-protos-go/peer"
	"github.com/olekukonko/tablewriter"
)

const (
	LutherEventKey = "luther"
)

type TransactionID string

// Block is a container for the transaction information exported
// by QueryBlock.
type Block struct {
	hash         string
	blockNum     uint64
	transactions []*Transaction
}

func (b *Block) Render() io.Reader {
	if b == nil {
		return bytes.NewBufferString("Empty Block")
	}
	buf := new(bytes.Buffer)
	table := tablewriter.NewWriter(buf)
	table.SetHeader([]string{"Block Num", "Hash", "Num Txs"})
	table.Append([]string{
		fmt.Sprintf("%d", b.blockNum),
		b.hash,
		fmt.Sprintf("%d", len(b.transactions)),
	})
	table.Render() // Send output
	for _, tx := range b.transactions {
		_, _ = io.Copy(buf, tx.Render())
	}
	return buf // Return an empty string for the same reason as above
}

// Valid validates a block.
func (b *Block) Valid() error {
	if b == nil {
		return fmt.Errorf("missing block")
	}
	if b.BlockNum() <= 0 {
		return fmt.Errorf("invalid block num [%d]", b.BlockNum())
	}
	if len(b.Transactions()) == 0 {
		return fmt.Errorf("empty block [%d]", b.BlockNum())
	}
	for idx, tx := range b.Transactions() {
		if err := tx.Valid(); err != nil {
			return fmt.Errorf("block has invalid tx [%d] at [%d]: %w", b.BlockNum(), idx, err)
		}
	}
	return nil
}

// NewBlock creates an immutable block object.
func NewBlock(hash string, blockNum uint64, transactions []*Transaction) *Block {
	txsCopy := make([]*Transaction, len(transactions))
	copy(txsCopy, transactions)
	return &Block{
		hash:         hash,
		blockNum:     blockNum,
		transactions: txsCopy,
	}
}

// Hash returns block hash.
func (b *Block) Hash() string {
	return b.hash
}

// BlockNum returns the block number.
func (b *Block) BlockNum() uint64 {
	return b.blockNum
}

// Transactions returns the transactions in the block.
func (b *Block) Transactions() []*Transaction {
	txsCopy := make([]*Transaction, len(b.transactions))
	copy(txsCopy, b.transactions)
	return txsCopy
}

// Transaction is a container for the transaction information exported
// by QueryBlock.
type Transaction struct {
	id             TransactionID
	reason         string
	event          []byte
	chaincodeID    string
	validationCode int32
}

func (t *Transaction) Render() io.Reader {
	if t == nil {
		return bytes.NewBufferString("Empty Transaction")
	}
	buf := new(bytes.Buffer)
	table := tablewriter.NewWriter(buf)
	table.SetHeader([]string{"ID", "Reason", "Chaincode ID", "Validation Code", "Event"})
	eventStr := "No Event"
	if len(t.Event()) > 0 {
		eventStr = hex.EncodeToString(t.Event())
	}
	table.Append([]string{
		string(t.ID()),
		t.Reason(),
		t.ChaincodeID(),
		fmt.Sprintf("%d", t.ValidationCode()),
		eventStr,
	})
	table.Render() // Send output
	return buf     // Return an empty string for the same reason as above
}

// Valid validates a transaction.
func (t *Transaction) Valid() error {
	if t == nil {
		return fmt.Errorf("missing tx")
	}
	if t.ID() == "" {
		return fmt.Errorf("missing tx ID")
	}
	return nil
}

// NewTransaction creates an immutable transaction object.
func NewTransaction(id TransactionID, reason string, event []byte, chaincodeID string, validationCode int32) *Transaction {
	eventCopy := make([]byte, len(event))
	copy(eventCopy, event)
	return &Transaction{
		id:             id,
		reason:         reason,
		event:          eventCopy,
		validationCode: validationCode,
	}
}

// ID is the transaction ID.
func (t *Transaction) ID() TransactionID {
	return t.id
}

// Reason is an english description of the transaction event.
func (t *Transaction) Reason() string {
	return t.reason
}

// ChaincodeID is the chaincode ID for the transaction.
func (t *Transaction) ChaincodeID() string {
	return t.chaincodeID
}

// ValidationCode is the status code for the transaction.
func (t *Transaction) ValidationCode() int32 {
	return t.validationCode
}

// Event is the transaction event data.
func (t *Transaction) Event() []byte {
	if len(t.event) == 0 {
		return nil
	}
	eventCopy := make([]byte, len(t.event))
	copy(eventCopy, t.event)
	return eventCopy
}

func processBlock(blk *cb.Block) (*Block, error) {
	if blk == nil {
		return nil, fmt.Errorf("process block: nil")
	}
	blockNum := blk.GetHeader().GetNumber()
	transactionsIn := blk.GetData().GetData()
	filterIn := blk.GetMetadata().Metadata[cb.BlockMetadataIndex_TRANSACTIONS_FILTER]
	blockHash := blk.GetHeader().GetDataHash()
	if blockHash == nil {
		return nil, errors.New("process block: could not get data hash")
	}
	blockHashHex := hex.EncodeToString(blockHash)

	if len(transactionsIn) != len(filterIn) {
		return nil, fmt.Errorf("process block: mismatched parallel arrays")
	}

	out := make([]*Transaction, 0, len(blk.GetData().GetData()))

	for i, buf := range blk.GetData().GetData() {
		txID, err := getTransactionID(buf)
		if err != nil {
			return nil, fmt.Errorf("process block: %w", err)
		}

		// an empty transaction ID is used by fabric to signify special
		// data such as channel creation artifacts. here we ignore
		// anything that isn't a regular transaction.
		if len(txID) == 0 {
			continue
		}

		validationCode := int32(filterIn[i])

		reason, ok := fabricpeer.TxValidationCode_name[validationCode]
		if !ok {
			return nil, fmt.Errorf("process block: invalid validation code [%d]", validationCode)
		}

		event, ccID, err := getEvent(buf)
		if err != nil {
			return nil, fmt.Errorf("process block: %w", err)
		}

		out = append(out, NewTransaction(txID, reason, event, ccID, validationCode))
	}

	processedBlock := NewBlock(blockHashHex, blockNum, out)

	return processedBlock, nil
}

func getEvent(data []byte) ([]byte, string, error) {
	envelope := &cb.Envelope{}
	err := proto.Unmarshal(data, envelope)
	if err != nil {
		return nil, "", err
	}

	payload := &cb.Payload{}
	err = proto.Unmarshal(envelope.GetPayload(), payload)
	if err != nil {
		return nil, "", err
	}

	// Thanks to: https://stackoverflow.com/questions/57571732/how-to-parse-protobuf-data-of-envelope-payload-data

	tx := &fabricpeer.Transaction{}
	err = proto.Unmarshal(payload.GetData(), tx)
	if err != nil {
		return nil, "", err
	}

	actions := tx.GetActions()
	if len(actions) == 0 {
		return nil, "", nil
	}

	action := actions[0]

	chaincodeActionPayload := &fabricpeer.ChaincodeActionPayload{}
	err = proto.Unmarshal(action.GetPayload(), chaincodeActionPayload)
	if err != nil {
		return nil, "", err
	}

	proposalResponsePayload := &fabricpeer.ProposalResponsePayload{}
	err = proto.Unmarshal(chaincodeActionPayload.GetAction().GetProposalResponsePayload(), proposalResponsePayload)
	if err != nil {
		return nil, "", err
	}

	chaincodeAction := &fabricpeer.ChaincodeAction{}
	err = proto.Unmarshal(proposalResponsePayload.GetExtension(), chaincodeAction)
	if err != nil {
		return nil, "", err
	}

	if len(chaincodeAction.GetEvents()) == 0 {
		return nil, "", nil
	}

	event := &fabricpeer.ChaincodeEvent{}
	err = proto.Unmarshal(chaincodeAction.GetEvents(), event)
	if err != nil {
		return nil, "", err
	}

	if event.GetEventName() != LutherEventKey {
		// unrecognized event
		return nil, event.GetChaincodeId(), nil
	}

	return event.GetPayload(), event.GetChaincodeId(), nil
}

func getTransactionID(data []byte) (TransactionID, error) {
	envelope := &cb.Envelope{}
	err := proto.Unmarshal(data, envelope)
	if err != nil {
		return "", err
	}

	payload := &cb.Payload{}
	err = proto.Unmarshal(envelope.GetPayload(), payload)
	if err != nil {
		return "", err
	}

	channelheader := &cb.ChannelHeader{}
	err = proto.Unmarshal(payload.GetHeader().GetChannelHeader(), channelheader)
	if err != nil {
		return "", err
	}

	return TransactionID(channelheader.GetTxId()), nil
}
