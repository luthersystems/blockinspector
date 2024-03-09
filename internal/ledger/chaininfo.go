package ledger

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	cb "github.com/hyperledger/fabric-protos-go/common"
	fabricpeer "github.com/hyperledger/fabric-protos-go/peer"
	"github.com/hyperledger/fabric/protoutil"
	"github.com/olekukonko/tablewriter"
	"github.com/sirupsen/logrus"
)

const (
	LutherEventKey         = "luther"
	LutherEventName        = "txEventName"
	LutherEventRequestID   = "txRequestID"
	LutherEventTxTimestamp = "txTimestamp"
)

var skipLifecycleRWSet = true

type TransactionID string

type CommonBlock struct {
	cb.Block
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
	txValidations []TxValidation
}

func (s *Block) GetValidation(txNum int) *TxValidation {
	if s == nil || txNum < 0 || txNum >= len(s.txValidations) {
		missing := TxValidation(-1)
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
	details := t.GetDetails()
	event := details.GetEvent()
	table.Append([]string{
		formatWithTrailingDots(string(t.GetID()), 6),
		b.GetValidation(i).GetReason(),
		event.GetChaincodeId(),
		event.LutherDescription(),
		fmt.Sprintf("%d", len(event.GetPayload())),
		fmt.Sprintf("%d", t.GetSize()),
	})
	table.Render()

	for _, nsRWSet := range details.GetNamespaceReadWriteSet() {
		if nsRWSet.GetNamespace() == "_lifecycle" && skipLifecycleRWSet {
			continue
		}
		rwSet := nsRWSet.GetReadWriteSet()

		table = tablewriter.NewWriter(w)
		table.SetCaption(true, fmt.Sprintf("Read Set [%s]", nsRWSet.GetNamespace()))
		table.SetHeader([]string{"Key", "Version"})
		for _, record := range rwSet.GetReadSet() {
			table.Append([]string{
				record.GetKey(),
				record.GetVersion().String(),
			})
		}
		table.Render()

		table = tablewriter.NewWriter(w)
		table.SetCaption(true, fmt.Sprintf("Write Set [%s]", nsRWSet.GetNamespace()))
		table.SetHeader([]string{"Key", "Val (size)"})
		for _, record := range rwSet.GetWriteSet() {
			table.Append([]string{
				record.GetKey(),
				fmt.Sprintf("%d", len(record.GetVal())),
			})
		}
		table.Render()

	}
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
	num     int
	id      TransactionID
	size    int
	details *TransactionDetails
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

func (t *Transaction) GetDetails() *TransactionDetails {
	if t == nil {
		return nil
	}
	return t.details
}

type TxValidation int32

func (s *TxValidation) GetReason() string {
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

	size := blk.XXX_Size() // NOTE: this isn't great, but it's better than importing the deprecated proto pkg

	validations := make([]TxValidation, 0, len(filterIn))

	retBlock := &Block{
		hash:     blockHashHex,
		blockNum: blockNum,
		size:     size,
	}

	for i, buf := range blk.GetData().GetData() {
		if len(transactionsIn) == len(filterIn) {
			validations = append(validations, TxValidation(int32(filterIn[i])))
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
	*fabricpeer.ChaincodeEvent
}

func (s *Event) GetPayload() []byte {
	if s == nil {
		return nil
	}
	return s.Payload
}

func (s *Event) GetChaincodeId() string {
	if s == nil {
		return ""
	}
	return s.ChaincodeId
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

type Version struct {
	BlockNum uint64
	TxNum    uint64
}

func (s *Version) String() string {
	return fmt.Sprintf("(%d:%d)", s.BlockNum, s.TxNum)
}

type RSetRecord struct {
	Key     string
	Version *Version
}

func (s *RSetRecord) GetKey() string {
	if s == nil {
		return ""
	}
	return s.Key
}

func (s *RSetRecord) GetVersion() *Version {
	if s == nil {
		return nil
	}
	return s.Version
}

type WSetRecord struct {
	Key string
	Val []byte
}

func (s *WSetRecord) GetKey() string {
	if s == nil {
		return ""
	}
	return s.Key
}

func (s *WSetRecord) GetVal() []byte {
	if s == nil {
		return nil
	}
	return s.Val
}

type RWSet struct {
	ReadSet  []*RSetRecord
	WriteSet []*WSetRecord
}

func (s *RWSet) GetWriteSet() []*WSetRecord {
	if s == nil {
		return nil
	}
	return s.WriteSet
}

func (s *RWSet) GetReadSet() []*RSetRecord {
	if s == nil {
		return nil
	}
	return s.ReadSet
}

type NSRWSet struct {
	Namespace string
	RWSet     *RWSet
}

func (s *NSRWSet) GetNamespace() string {
	if s == nil {
		return ""
	}
	return s.Namespace
}

func (s *NSRWSet) GetReadWriteSet() *RWSet {
	if s == nil {
		return nil
	}
	return s.RWSet
}

func getNSRWSets(results []byte) ([]*NSRWSet, error) {
	if len(results) == 0 {
		return nil, nil
	}
	txRwSet, err := protoutil.UnmarshalTxReadWriteSet(results)
	if err != nil {
		return nil, err
	}

	nsRWSets := make([]*NSRWSet, 0, len(txRwSet.GetNsRwset()))

	for _, txrw := range txRwSet.GetNsRwset() {
		rwSet, err := protoutil.UnmarshalKVRWSet(txrw.GetRwset())
		if err != nil {
			return nil, err
		}
		rwSetExtracted := &RWSet{
			ReadSet:  []*RSetRecord{},
			WriteSet: []*WSetRecord{},
		}
		for _, read := range rwSet.Reads {
			rwSetExtracted.ReadSet = append(rwSetExtracted.ReadSet, &RSetRecord{
				Key: read.GetKey(),
				Version: &Version{
					BlockNum: read.GetVersion().GetBlockNum(),
					TxNum:    read.GetVersion().GetTxNum(),
				},
			})
		}
		for _, write := range rwSet.Writes {
			rwSetExtracted.WriteSet = append(rwSetExtracted.WriteSet, &WSetRecord{
				Key: write.GetKey(),
				Val: write.GetValue(),
			})
		}
		nsRWSets = append(nsRWSets, &NSRWSet{
			Namespace: txrw.GetNamespace(),
			RWSet:     rwSetExtracted,
		})
	}

	return nsRWSets, nil
}

func getAction(txBytes []byte) (*fabricpeer.ChaincodeAction, error) {
	if len(txBytes) == 0 {
		return nil, nil
	}
	tx, err := protoutil.UnmarshalTransaction(txBytes)
	if err != nil {
		return nil, err
	}

	actions := tx.GetActions()
	if len(actions) == 0 {
		return nil, nil
	}

	action := actions[0]

	chaincodeActionPayload, err := protoutil.UnmarshalChaincodeActionPayload(action.GetPayload())
	if err != nil {
		return nil, err
	}

	proposalResponse, err := protoutil.UnmarshalProposalResponsePayload(chaincodeActionPayload.GetAction().GetProposalResponsePayload())
	if err != nil {
		logrus.WithError(err).Info("invalid proposal response")
		return nil, nil
	}

	chaincodeAction, err := protoutil.UnmarshalChaincodeAction(proposalResponse.GetExtension())
	if err != nil {
		return nil, err
	}

	return chaincodeAction, nil
}

func getEvent(eventBytes []byte) (*Event, error) {
	if len(eventBytes) == 0 {
		return nil, nil
	}
	event, err := protoutil.UnmarshalChaincodeEvents(eventBytes)
	if err != nil {
		return nil, err
	}

	return &Event{event}, nil
}

type TransactionDetails struct {
	event    *Event
	nsRWSets []*NSRWSet
}

func (s *TransactionDetails) GetEvent() *Event {
	if s == nil {
		return nil
	}
	return s.event
}

func (s *TransactionDetails) GetNamespaceReadWriteSet() []*NSRWSet {
	if s == nil {
		return nil
	}
	return s.nsRWSets
}

func NewTransactionDetails(txBytes []byte) (*TransactionDetails, error) {
	chaincodeAction, err := getAction(txBytes)
	if err != nil {
		return nil, err
	}

	details := &TransactionDetails{}

	if event, err := getEvent(chaincodeAction.GetEvents()); err != nil {
		return nil, err
	} else {
		details.event = event
	}

	if nsRWSets, err := getNSRWSets(chaincodeAction.GetResults()); err != nil {
		return nil, err
	} else {
		details.nsRWSets = nsRWSets
	}

	return details, nil
}

func getPayload(envelopeBytes []byte) (*cb.Payload, error) {
	if len(envelopeBytes) == 0 {
		return nil, fmt.Errorf("empty envelope")
	}

	envelope, err := protoutil.UnmarshalEnvelope(envelopeBytes)
	if err != nil {
		return nil, err
	}

	payload, err := protoutil.UnmarshalPayload(envelope.GetPayload())
	if err != nil {
		return nil, err
	}

	return payload, nil
}

func getTransactionID(chanHeaderBytes []byte) (TransactionID, error) {
	channelheader, err := protoutil.UnmarshalChannelHeader(chanHeaderBytes)
	if err != nil {
		return "", err
	} else {
		return TransactionID(channelheader.GetTxId()), nil
	}
}

// NewTransaction creates an immutable transaction object.
func NewTransaction(envelopeBytes []byte) (*Transaction, error) {
	retTx := &Transaction{
		size: len(envelopeBytes),
	}

	payload, err := getPayload(envelopeBytes)
	if err != nil {
		return nil, err
	}

	txID, err := getTransactionID(payload.GetHeader().GetChannelHeader())
	if err != nil {
		return nil, err
	}

	retTx.id = txID

	if details, err := NewTransactionDetails(payload.GetData()); err != nil {
		return nil, err
	} else {
		retTx.details = details
	}

	return retTx, nil
}
