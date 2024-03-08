package ledger

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/golang/protobuf/proto" //nolint SA1019 hlf does not support latest protobuf
	"github.com/hyperledger/fabric-protos-go/common"
	cb "github.com/hyperledger/fabric-protos-go/common"
	fabricpeer "github.com/hyperledger/fabric-protos-go/peer"
	"github.com/olekukonko/tablewriter"
	"github.com/sirupsen/logrus"
)

const (
	LutherEventKey         = "luther"
	LutherEventName        = "txEventName"
	LutherEventRequestID   = "txRequestID"
	LutherEventTxTimestamp = "txTimestamp"
)

type TransactionID string

type CommonBlock struct {
	common.Block
}

func (s *CommonBlock) Write(w io.Writer) {
	if s == nil {
		return
	}
	if blk, err := processBlock(&s.Block); err != nil {
		logrus.WithError(err).Error("process block")
		return
	} else {
		blk.Write(w)
	}
}

// Block is a container for the transaction information.
type Block struct {
	hash         string
	blockNum     uint64
	transactions []*Transaction
	size         int
}

func (b *Block) Write(w io.Writer) {
	if w == nil {
		return
	}
	if b == nil {
		fmt.Fprint(w, "empty block")
		return
	}
	table := tablewriter.NewWriter(w)
	table.SetHeader([]string{"Block Num", "Hash", "Num Txs", "Size (bytes)"})
	table.Append([]string{
		fmt.Sprintf("%d", b.blockNum),
		b.hash,
		fmt.Sprintf("%d", len(b.transactions)),
		fmt.Sprintf("%d", b.size),
	})
	table.Render() // Send output
	for _, tx := range b.transactions {
		tx.Write(w)
	}
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
func NewBlock(hash string, blockNum uint64, size int, transactions []*Transaction) *Block {
	txsCopy := make([]*Transaction, len(transactions))
	copy(txsCopy, transactions)
	return &Block{
		hash:         hash,
		blockNum:     blockNum,
		size:         size,
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
	event          *Event
	validationCode int32
	size           int
}

func (t *Transaction) Write(w io.Writer) {
	if t == nil {
		fmt.Fprint(w, "empty block")
		return
	}
	table := tablewriter.NewWriter(w)
	table.SetHeader([]string{"ID", "Validation", "Chaincode ID", "Luther Event", "Event Size (bytes)", "Size (bytes)"})
	table.Append([]string{
		string(t.ID()),
		t.Reason(),
		t.GetEvent().GetChaincodeId(),
		t.GetEvent().LutherEndpoint(),
		fmt.Sprintf("%d", len(t.GetEvent().GetPayload())),
		fmt.Sprintf("%d", t.size),
	})
	table.Render() // Send output
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
func NewTransaction(id TransactionID, reason string, event *Event, validationCode int32, size int) *Transaction {
	return &Transaction{
		id:             id,
		reason:         reason,
		event:          event,
		validationCode: validationCode,
		size:           size,
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

// ValidationCode is the status code for the transaction.
func (t *Transaction) ValidationCode() int32 {
	return t.validationCode
}

// GetEvent is the transaction event data.
func (t *Transaction) GetEvent() *Event {
	if t == nil || t.event == nil {
		return &Event{}
	}
	return t.event
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

	out := make([]*Transaction, 0, len(blk.GetData().GetData()))

	for i, buf := range blk.GetData().GetData() {
		validationCode := int32(-1)
		reason := ""
		// NOTE: orderer chains do not have filters, so return -1
		if len(transactionsIn) == len(filterIn) {
			validationCode = int32(filterIn[i])
			var ok bool
			reason, ok = fabricpeer.TxValidationCode_name[validationCode]
			if !ok {
				return nil, fmt.Errorf("process block: invalid validation code [%d]", validationCode)
			}
		}

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

		event, err := getEvent(buf)
		if err != nil {
			return nil, fmt.Errorf("process block: %w", err)
		}

		size := len(buf)

		out = append(out, NewTransaction(txID, reason, event, validationCode, size))
	}

	size := proto.Size(blk)
	processedBlock := NewBlock(blockHashHex, blockNum, size, out)

	return processedBlock, nil
}

type lutherEvent map[string]string

func (s *lutherEvent) Name() string {
	if s == nil {
		return ""
	}
	value, exists := (*s)[LutherEventName]
	if !exists {
		return ""
	}
	return value
}

func (s *lutherEvent) RequestID() string {
	if s == nil {
		return ""
	}
	value, exists := (*s)[LutherEventRequestID]
	if !exists {
		return ""
	}
	return value
}

func (s *lutherEvent) Timestamp() string {
	if s == nil {
		return ""
	}
	value, exists := (*s)[LutherEventTxTimestamp]
	if !exists {
		return ""
	}
	return value
}

type Event struct {
	fabricpeer.ChaincodeEvent
}

func (s *Event) IsLutherEvent() bool {
	if s == nil {
		return false
	}
	return s.GetEventName() == LutherEventKey
}

func (s *Event) LutherEndpoint() string {
	if !s.IsLutherEvent() {
		return "<none>"
	}
	if lutherEvent, err := newLutherEvent(s); err != nil {
		return "<corrupt>"
	} else {
		return lutherEvent.Name()
	}
}

func newLutherEvent(event *Event) (*lutherEvent, error) {
	if event.GetEventName() != LutherEventKey {
		return nil, fmt.Errorf("non-luther event")
	}
	lEvent := &lutherEvent{}
	if err := json.Unmarshal(event.GetPayload(), lEvent); err != nil {
		return nil, err
	}
	return lEvent, nil
}

func getEvent(data []byte) (*Event, error) {
	envelope := &cb.Envelope{}
	err := proto.Unmarshal(data, envelope)
	if err != nil {
		return nil, err
	}

	payload := &cb.Payload{}
	err = proto.Unmarshal(envelope.GetPayload(), payload)
	if err != nil {
		return nil, err
	}

	// Thanks to: https://stackoverflow.com/questions/57571732/how-to-parse-protobuf-data-of-envelope-payload-data

	tx := &fabricpeer.Transaction{}
	err = proto.Unmarshal(payload.GetData(), tx)
	if err != nil {
		return nil, err
	}

	actions := tx.GetActions()
	if len(actions) == 0 {
		return nil, nil
	}

	action := actions[0]

	chaincodeActionPayload := &fabricpeer.ChaincodeActionPayload{}
	err = proto.Unmarshal(action.GetPayload(), chaincodeActionPayload)
	if err != nil {
		return nil, err
	}

	proposalResponsePayload := &fabricpeer.ProposalResponsePayload{}
	err = proto.Unmarshal(chaincodeActionPayload.GetAction().GetProposalResponsePayload(), proposalResponsePayload)
	if err != nil {
		return nil, err
	}

	chaincodeAction := &fabricpeer.ChaincodeAction{}
	err = proto.Unmarshal(proposalResponsePayload.GetExtension(), chaincodeAction)
	if err != nil {
		return nil, err
	}

	if len(chaincodeAction.GetEvents()) == 0 {
		return nil, nil
	}

	event := &fabricpeer.ChaincodeEvent{}
	err = proto.Unmarshal(chaincodeAction.GetEvents(), event)
	if err != nil {
		return nil, err
	}

	return &Event{*event}, nil
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
