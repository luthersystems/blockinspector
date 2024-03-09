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
	if blk, err := NewBlock(&s.Block); err != nil {
		logrus.WithError(err).Error("new block")
		return
	} else {
		blk.Write(w)
	}
}

// Block is a container for the transaction information.
type Block struct {
	hash          string
	blockNum      uint64
	transactions  []*Transaction
	size          int
	txValidations []txValidation
}

func (s *Block) GetValidation(txNum int) *txValidation {
	if s == nil || txNum < 0 || txNum >= len(s.txValidations) {
		missing := txValidation(-1)
		return &missing
	}
	return &s.txValidations[txNum]
}

// formatWithTrailingDots truncates the input string to the specified length
// and adds trailing dots if the original string was longer.
func formatWithTrailingDots(input string, length int) string {
	if len(input) > length {
		return fmt.Sprintf("%.*s...", length, input)
	}
	return input
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
		formatWithTrailingDots(b.hash, 6),
		fmt.Sprintf("%d", len(b.transactions)),
		fmt.Sprintf("%d", b.size),
	})
	table.Render() // Send output
	for i := range b.transactions {
		b.WriteTx(i, w)
	}
}

func (b *Block) WriteTx(i int, w io.Writer) {
	if w == nil {
		return
	}
	if i < 0 || i >= len(b.transactions) {
		return
	}
	t := b.transactions[i]
	table := tablewriter.NewWriter(w)
	table.SetHeader([]string{"ID", "Validation", "Chaincode ID", "Luther Event", "Event Size (bytes)", "Size (bytes)"})
	table.Append([]string{
		formatWithTrailingDots(string(t.GetID()), 6),
		b.GetValidation(i).GetReason(),
		t.GetEvent().GetChaincodeId(),
		t.GetEvent().LutherDescription(),
		fmt.Sprintf("%d", len(t.GetEvent().GetPayload())),
		fmt.Sprintf("%d", t.GetSize()),
	})
	table.Render() // Send output
}

// GetHash returns block hash.
func (b *Block) GetHash() string {
	if b == nil {
		return ""
	}
	return b.hash
}

// GetBlockNum returns the block number.
func (b *Block) GetBlockNum() uint64 {
	if b == nil {
		return 0
	}
	return b.blockNum
}

// GetTransactions returns the transactions in the block.
func (b *Block) GetTransactions() []*Transaction {
	if b == nil {
		return nil
	}
	txsCopy := make([]*Transaction, len(b.transactions))
	copy(txsCopy, b.transactions)
	return txsCopy
}

// Transaction is a container for the transaction information exported
// by QueryBlock.
type Transaction struct {
	num   int
	block *Block
	id    TransactionID
	event *Event
	code  *txValidation
	size  int
}

func (t *Transaction) GetNumber() int {
	if t == nil {
		return -1
	}
	return t.num
}

// ID is the transaction ID.
func (t *Transaction) GetID() TransactionID {
	if t == nil || t.id == "" {
		return TransactionID("n/a")
	}
	return t.id
}

func (t *Transaction) GetSize() int {
	if t == nil {
		return 0
	}
	return t.size
}

func (t *Transaction) GetCode() *txValidation {
	if t == nil {
		return nil
	}
	return t.code
}

// GetEvent is the transaction event data.
func (t *Transaction) GetEvent() *Event {
	if t == nil || t.event == nil {
		return &Event{}
	}
	return t.event
}

type txValidation int32

func (s *txValidation) GetReason() string {
	if s == nil || int32(*s) < 0 {
		return "n/a"
	}
	if reason, ok := fabricpeer.TxValidationCode_name[int32(*s)]; !ok {
		return fmt.Sprintf("invalid code [%d]", s)
	} else {
		return reason
	}
}

func NewBlock(blk *cb.Block) (*Block, error) {
	if blk == nil {
		return nil, fmt.Errorf("nil")
	}
	blockNum := blk.GetHeader().GetNumber()
	transactionsIn := blk.GetData().GetData()
	filterIn := blk.GetMetadata().Metadata[cb.BlockMetadataIndex_TRANSACTIONS_FILTER]
	blockHash := blk.GetHeader().GetDataHash()
	if blockHash == nil {
		return nil, errors.New("no data hash")
	}
	blockHashHex := hex.EncodeToString(blockHash)

	txs := make([]*Transaction, 0, len(blk.GetData().GetData()))

	size := proto.Size(blk)

	validations := make([]txValidation, 0, len(filterIn))

	retBlock := &Block{
		hash:     blockHashHex,
		blockNum: blockNum,
		size:     size,
	}

	for i, buf := range blk.GetData().GetData() {
		if len(transactionsIn) == len(filterIn) {
			validations = append(validations, txValidation(int32(filterIn[i])))
		}

		if tx, err := NewTransaction(buf); err != nil {
			return nil, err
		} else {
			txs = append(txs, tx)
		}
	}

	retBlock.txValidations = validations
	retBlock.transactions = txs

	return retBlock, nil
}

type lutherEvent map[string]string

func (s *lutherEvent) GetName() string {
	if s == nil {
		return ""
	}
	value, exists := (*s)[LutherEventName]
	if !exists {
		return ""
	}
	return value
}

func (s *lutherEvent) GetRequestID() string {
	if s == nil {
		return ""
	}
	value, exists := (*s)[LutherEventRequestID]
	if !exists {
		return ""
	}
	return value
}

func (s *lutherEvent) GetTimestamp() string {
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

func (s *Event) LutherDescription() string {
	if !s.IsLutherEvent() {
		return "<none>"
	}
	if lutherEvent, err := newLutherEvent(s); err != nil {
		return "<corrupt>"
	} else {
		return fmt.Sprintf("%s@%s [%s]", lutherEvent.GetName(), lutherEvent.GetTimestamp(), lutherEvent.GetRequestID())
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

func getEvent(tx *fabricpeer.Transaction) (*Event, error) {
	if tx == nil {
		return nil, nil
	}
	actions := tx.GetActions()
	if len(actions) == 0 {
		return nil, nil
	}

	action := actions[0]

	chaincodeActionPayload := &fabricpeer.ChaincodeActionPayload{}
	if err := proto.Unmarshal(action.GetPayload(), chaincodeActionPayload); err != nil {
		return nil, err
	}

	proposalResponsePayload := &fabricpeer.ProposalResponsePayload{}
	if err := proto.Unmarshal(chaincodeActionPayload.GetAction().GetProposalResponsePayload(), proposalResponsePayload); err != nil {
		logrus.WithError(err).Info("invalid proposal response")
		return nil, nil
	}

	chaincodeAction := &fabricpeer.ChaincodeAction{}
	if err := proto.Unmarshal(proposalResponsePayload.GetExtension(), chaincodeAction); err != nil {
		return nil, err
	}

	if len(chaincodeAction.GetEvents()) == 0 {
		return nil, nil
	}

	event := &fabricpeer.ChaincodeEvent{}
	if err := proto.Unmarshal(chaincodeAction.GetEvents(), event); err != nil {
		return nil, err
	}

	return &Event{*event}, nil
}

// NewTransaction creates an immutable transaction object.
func NewTransaction(envelopeBytes []byte) (*Transaction, error) {
	size := len(envelopeBytes)
	if size == 0 {
		return nil, fmt.Errorf("empty envelope")
	}
	retTx := &Transaction{
		size: size,
	}
	envelope := &cb.Envelope{}
	if err := proto.Unmarshal(envelopeBytes, envelope); err != nil {
		return nil, err
	}

	payload := &cb.Payload{}
	if err := proto.Unmarshal(envelope.GetPayload(), payload); err != nil {
		return nil, err
	}

	channelheader := &cb.ChannelHeader{}
	if err := proto.Unmarshal(payload.GetHeader().GetChannelHeader(), channelheader); err != nil {
		return nil, err
	}

	retTx.id = TransactionID(channelheader.GetTxId())

	tx := &fabricpeer.Transaction{}
	if err := proto.Unmarshal(payload.GetData(), tx); err != nil {
		return nil, err
	}

	if event, err := getEvent(tx); err != nil {
		return nil, err
	} else {
		retTx.event = event
	}

	return retTx, nil
}
