// Package explorer decodes Fabric blocks into JSON views, keeps them in an
// in-memory index, and serves the index over HTTP with a server-sent events
// stream of new blocks. It reads blocks only: it sends no transaction.
package explorer

import (
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/golang/protobuf/proto" //nolint:staticcheck // fabric-protos-go v0.3 is generated for the v1 API
	cb "github.com/hyperledger/fabric-protos-go/common"
	"github.com/hyperledger/fabric-protos-go/ledger/rwset"
	"github.com/hyperledger/fabric-protos-go/ledger/rwset/kvrwset"
	fabricpeer "github.com/hyperledger/fabric-protos-go/peer"

	"github.com/luthersystems/blockinspector/internal/protoutil"
)

// substrate's gateway invokes the chaincode with two arguments: the function
// name "Invoke" and the JSON-RPC request (luthersystems/substrate
// internal/shiroclient/fabricclient, encodedRPCArg).
const substrateInvokeFcn = "Invoke"

// BlockSummary is the block list's row.
type BlockSummary struct {
	// Time is the channel header timestamp of the block's first transaction.
	// A Fabric block has no time of its own.
	Time         *time.Time `json:"time,omitempty"`
	DataHash     string     `json:"data_hash"`
	PreviousHash string     `json:"previous_hash"`
	Number       uint64     `json:"number"`
	TxCount      int        `json:"tx_count"`
	ValidTxCount int        `json:"valid_tx_count"`
	Size         int        `json:"size"`
	Config       bool       `json:"config"`
}

// Block is a block with the summaries of its transactions.
type Block struct {
	Transactions []TxSummary `json:"transactions"`
	BlockSummary
}

// TxSummary is a transaction's row in a block or a transaction list.
type TxSummary struct {
	Time           *time.Time `json:"time,omitempty"`
	ID             string     `json:"id"`
	Type           string     `json:"type"`
	ValidationCode string     `json:"validation_code"`
	CreatorMSP     string     `json:"creator_msp,omitempty"`
	ChaincodeID    string     `json:"chaincode_id,omitempty"`
	// Method is the JSON-RPC method of a substrate request, or "batch" for a
	// JSON-RPC batch (BatchMethods lists its methods).
	Method       string   `json:"method,omitempty"`
	BatchMethods []string `json:"batch_methods,omitempty"`
	BlockNumber  uint64   `json:"block_number"`
	Index        int      `json:"index"`
	Valid        bool     `json:"valid"`
}

// Transaction is the transaction page.
type Transaction struct {
	Creator   *Identity  `json:"creator,omitempty"`
	Response  *Response  `json:"response,omitempty"`
	Event     *Event     `json:"event,omitempty"`
	Endorsers []Identity `json:"endorsers"`
	// Request is the JSON-RPC request when the chaincode input is a substrate
	// call. Args holds the input arguments otherwise.
	Request json.RawMessage  `json:"request,omitempty"`
	Args    []Value          `json:"args,omitempty"`
	RWSets  []NamespaceRWSet `json:"rwsets"`
	TxSummary
}

// Identity is a creator or an endorser.
type Identity struct {
	MSPID string `json:"msp_id"`
	// Subject is the certificate's subject common name, when the identity is
	// an X.509 certificate.
	Subject string `json:"subject,omitempty"`
}

// Response is the chaincode response the endorsers signed.
type Response struct {
	Message string `json:"message,omitempty"`
	Payload *Value `json:"payload,omitempty"`
	Status  int32  `json:"status"`
}

// Event is the chaincode event a transaction set.
type Event struct {
	ChaincodeID string `json:"chaincode_id"`
	Name        string `json:"name"`
	Payload     *Value `json:"payload,omitempty"`
}

// NamespaceRWSet is the read and write set of one namespace.
type NamespaceRWSet struct {
	Namespace     string         `json:"namespace"`
	Reads         []Read         `json:"reads"`
	Writes        []Write        `json:"writes"`
	PrivateWrites []PrivateWrite `json:"private_writes,omitempty"`
	RangeQueries  int            `json:"range_queries,omitempty"`
}

// Read is a key in a read set, with the version the transaction read.
type Read struct {
	Version *Version `json:"version,omitempty"`
	Key     string   `json:"key"`
}

// Version is a key's version: the block and transaction that last wrote it.
type Version struct {
	BlockNum uint64 `json:"block"`
	TxNum    uint64 `json:"tx"`
}

// Write is a key in a public write set.
type Write struct {
	Value    *Value `json:"value,omitempty"`
	Key      string `json:"key"`
	IsDelete bool   `json:"is_delete,omitempty"`
}

// PrivateWrite is a private data write. A block holds only its hashes.
type PrivateWrite struct {
	Collection string `json:"collection"`
	KeyHash    string `json:"key_hash"`
	ValueHash  string `json:"value_hash,omitempty"`
	IsDelete   bool   `json:"is_delete,omitempty"`
	IsPurge    bool   `json:"is_purge,omitempty"`
}

// Value is a byte string: as text when it is valid UTF-8, else base64.
type Value struct {
	Encoding string `json:"encoding"`
	Data     string `json:"data"`
	Size     int    `json:"size"`
}

// NewValue returns b as a Value, or nil for nil.
func NewValue(b []byte) *Value {
	if b == nil {
		return nil
	}
	if utf8.Valid(b) {
		return &Value{Encoding: "utf8", Data: string(b), Size: len(b)}
	}
	return &Value{Encoding: "base64", Data: base64.StdEncoding.EncodeToString(b), Size: len(b)}
}

// Bytes returns the value's bytes.
func (v *Value) Bytes() []byte {
	if v == nil {
		return nil
	}
	if v.Encoding == "base64" {
		b, err := base64.StdEncoding.DecodeString(v.Data)
		if err != nil {
			return nil
		}
		return b
	}
	return []byte(v.Data)
}

// decodedBlock is a decoded block with its full transactions.
type decodedBlock struct {
	txs []*Transaction
	Block
}

// DecodeBlock decodes a block. A transaction that does not decode is kept,
// with its type and ID when those decode, so a block never loses a row.
func DecodeBlock(blk *cb.Block) (*decodedBlock, error) {
	if blk == nil || blk.GetHeader() == nil {
		return nil, fmt.Errorf("block has no header")
	}
	num := blk.GetHeader().GetNumber()
	envs := blk.GetData().GetData()
	var filter []byte
	if md := blk.GetMetadata().GetMetadata(); len(md) > int(cb.BlockMetadataIndex_TRANSACTIONS_FILTER) {
		filter = md[cb.BlockMetadataIndex_TRANSACTIONS_FILTER]
	}
	out := &decodedBlock{
		Block: Block{
			BlockSummary: BlockSummary{
				Number:       num,
				DataHash:     hex.EncodeToString(blk.GetHeader().GetDataHash()),
				PreviousHash: hex.EncodeToString(blk.GetHeader().GetPreviousHash()),
				TxCount:      len(envs),
				Size:         proto.Size(blk),
			},
			Transactions: make([]TxSummary, 0, len(envs)),
		},
		txs: make([]*Transaction, 0, len(envs)),
	}
	for i, env := range envs {
		code := fabricpeer.TxValidationCode_NOT_VALIDATED
		if i < len(filter) {
			code = fabricpeer.TxValidationCode(filter[i])
		}
		tx := decodeTransaction(env)
		tx.BlockNumber = num
		tx.Index = i
		tx.ValidationCode = validationCodeName(code)
		tx.Valid = code == fabricpeer.TxValidationCode_VALID
		if tx.Valid {
			out.ValidTxCount++
		}
		if tx.Type == cb.HeaderType_CONFIG.String() {
			out.Config = true
		}
		if out.Time == nil && tx.Time != nil {
			out.Time = tx.Time
		}
		out.txs = append(out.txs, tx)
		out.Transactions = append(out.Transactions, tx.TxSummary)
	}
	return out, nil
}

func validationCodeName(code fabricpeer.TxValidationCode) string {
	if name, ok := fabricpeer.TxValidationCode_name[int32(code)]; ok {
		return name
	}
	return fmt.Sprintf("UNKNOWN_%d", code)
}

// decodeTransaction decodes as much of an envelope as it can. A part that
// does not decode is left empty; the transaction is still listed.
func decodeTransaction(envBytes []byte) *Transaction {
	tx := &Transaction{Endorsers: []Identity{}, RWSets: []NamespaceRWSet{}}
	env, err := protoutil.UnmarshalEnvelope(envBytes)
	if err != nil {
		tx.Type = "UNDECODABLE"
		return tx
	}
	payload, err := protoutil.UnmarshalPayload(env.GetPayload())
	if err != nil {
		tx.Type = "UNDECODABLE"
		return tx
	}
	chdr, err := protoutil.UnmarshalChannelHeader(payload.GetHeader().GetChannelHeader())
	if err != nil {
		tx.Type = "UNDECODABLE"
		return tx
	}
	tx.ID = chdr.GetTxId()
	tx.Type = cb.HeaderType(chdr.GetType()).String()
	if ts := chdr.GetTimestamp(); ts != nil {
		t := time.Unix(ts.GetSeconds(), int64(ts.GetNanos())).UTC()
		tx.Time = &t
	}
	if shdr, err := protoutil.UnmarshalSignatureHeader(payload.GetHeader().GetSignatureHeader()); err == nil {
		if id := decodeIdentity(shdr.GetCreator()); id != nil {
			tx.Creator = id
			tx.CreatorMSP = id.MSPID
		}
	}
	if chdr.GetType() != int32(cb.HeaderType_ENDORSER_TRANSACTION) {
		return tx
	}
	decodeEndorserTransaction(tx, payload.GetData())
	return tx
}

func decodeIdentity(b []byte) *Identity {
	if len(b) == 0 {
		return nil
	}
	sid, err := protoutil.UnmarshalSerializedIdentity(b)
	if err != nil {
		return nil
	}
	id := &Identity{MSPID: sid.GetMspid()}
	if block, _ := pem.Decode(sid.GetIdBytes()); block != nil {
		if cert, err := x509.ParseCertificate(block.Bytes); err == nil {
			id.Subject = cert.Subject.CommonName
		}
	}
	return id
}

func decodeEndorserTransaction(tx *Transaction, data []byte) {
	ftx, err := protoutil.UnmarshalTransaction(data)
	if err != nil || len(ftx.GetActions()) == 0 {
		return
	}
	ccap, err := protoutil.UnmarshalChaincodeActionPayload(ftx.GetActions()[0].GetPayload())
	if err != nil {
		return
	}
	if ccpp, err := protoutil.UnmarshalChaincodeProposalPayload(ccap.GetChaincodeProposalPayload()); err == nil {
		if cis, err := protoutil.UnmarshalChaincodeInvocationSpec(ccpp.GetInput()); err == nil {
			decodeInput(tx, cis.GetChaincodeSpec())
		}
	}
	for _, e := range ccap.GetAction().GetEndorsements() {
		if id := decodeIdentity(e.GetEndorser()); id != nil {
			tx.Endorsers = append(tx.Endorsers, *id)
		}
	}
	prp, err := protoutil.UnmarshalProposalResponsePayload(ccap.GetAction().GetProposalResponsePayload())
	if err != nil {
		return
	}
	action, err := protoutil.UnmarshalChaincodeAction(prp.GetExtension())
	if err != nil {
		return
	}
	if tx.ChaincodeID == "" {
		tx.ChaincodeID = action.GetChaincodeId().GetName()
	}
	if r := action.GetResponse(); r != nil {
		tx.Response = &Response{Status: r.GetStatus(), Message: r.GetMessage(), Payload: NewValue(r.GetPayload())}
	}
	if len(action.GetEvents()) > 0 {
		if ev, err := protoutil.UnmarshalChaincodeEvents(action.GetEvents()); err == nil {
			tx.Event = &Event{ChaincodeID: ev.GetChaincodeId(), Name: ev.GetEventName(), Payload: NewValue(ev.GetPayload())}
		}
	}
	tx.RWSets = decodeRWSets(action.GetResults())
}

func decodeInput(tx *Transaction, spec *fabricpeer.ChaincodeSpec) {
	tx.ChaincodeID = spec.GetChaincodeId().GetName()
	args := spec.GetInput().GetArgs()
	if len(args) == 2 && string(args[0]) == substrateInvokeFcn && json.Valid(args[1]) {
		tx.Request = json.RawMessage(args[1])
		tx.Method, tx.BatchMethods = jsonRPCMethods(args[1])
		return
	}
	for _, a := range args {
		tx.Args = append(tx.Args, *NewValue(a))
	}
}

// jsonRPCMethods returns the method of a JSON-RPC request, or "batch" and
// the methods of a JSON-RPC batch.
func jsonRPCMethods(req []byte) (string, []string) {
	type call struct {
		Method string `json:"method"`
	}
	var one call
	if err := json.Unmarshal(req, &one); err == nil {
		return one.Method, nil
	}
	var many []call
	if err := json.Unmarshal(req, &many); err != nil {
		return "", nil
	}
	methods := make([]string, 0, len(many))
	for _, c := range many {
		methods = append(methods, c.Method)
	}
	return "batch", methods
}

func decodeRWSets(results []byte) []NamespaceRWSet {
	out := []NamespaceRWSet{}
	if len(results) == 0 {
		return out
	}
	txrw := &rwset.TxReadWriteSet{}
	if err := proto.Unmarshal(results, txrw); err != nil {
		return out
	}
	for _, ns := range txrw.GetNsRwset() {
		nsOut := NamespaceRWSet{Namespace: ns.GetNamespace(), Reads: []Read{}, Writes: []Write{}}
		kv := &kvrwset.KVRWSet{}
		if err := proto.Unmarshal(ns.GetRwset(), kv); err == nil {
			for _, r := range kv.GetReads() {
				read := Read{Key: r.GetKey()}
				if v := r.GetVersion(); v != nil {
					read.Version = &Version{BlockNum: v.GetBlockNum(), TxNum: v.GetTxNum()}
				}
				nsOut.Reads = append(nsOut.Reads, read)
			}
			for _, w := range kv.GetWrites() {
				write := Write{Key: w.GetKey(), IsDelete: w.GetIsDelete()}
				if !w.GetIsDelete() {
					write.Value = NewValue(nonNil(w.GetValue()))
				}
				nsOut.Writes = append(nsOut.Writes, write)
			}
			nsOut.RangeQueries = len(kv.GetRangeQueriesInfo())
		}
		for _, coll := range ns.GetCollectionHashedRwset() {
			h := &kvrwset.HashedRWSet{}
			if err := proto.Unmarshal(coll.GetHashedRwset(), h); err != nil {
				continue
			}
			for _, w := range h.GetHashedWrites() {
				nsOut.PrivateWrites = append(nsOut.PrivateWrites, PrivateWrite{
					Collection: coll.GetCollectionName(),
					KeyHash:    hex.EncodeToString(w.GetKeyHash()),
					ValueHash:  hex.EncodeToString(w.GetValueHash()),
					IsDelete:   w.GetIsDelete(),
					IsPurge:    w.GetIsPurge(),
				})
			}
		}
		out = append(out, nsOut)
	}
	return out
}

// nonNil keeps an empty write value distinct from no value.
func nonNil(b []byte) []byte {
	if b == nil {
		return []byte{}
	}
	return b
}
