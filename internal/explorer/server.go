package explorer

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/sirupsen/logrus"
)

// API limits.
const (
	defaultBlockLimit = 20
	maxBlockLimit     = 500
	defaultTxLimit    = 50
	maxTxLimit        = 1000
	defaultKeyLimit   = 100
	maxKeyLimit       = 1000
	// streamBatch is how many blocks the stream reads from the index at a
	// time while it catches up.
	streamBatch = 100
)

// ServerOptions configures the HTTP API.
type ServerOptions struct {
	// AllowOrigin, when set, is sent as Access-Control-Allow-Origin, so a UI
	// served from another origin can call the API.
	AllowOrigin string
	// Heartbeat is the interval of the stream's keepalive comments.
	Heartbeat time.Duration
}

// Server serves the index over HTTP: JSON views under /v1/, and a
// server-sent events stream of new blocks.
type Server struct {
	ix     *Index
	status func() Status
	mux    *http.ServeMux
	opts   ServerOptions
}

// NewServer returns the API for an index. status reports the source's
// state; it may be nil.
func NewServer(ix *Index, status func() Status, opts ServerOptions) *Server {
	if opts.Heartbeat <= 0 {
		opts.Heartbeat = 15 * time.Second
	}
	if status == nil {
		status = func() Status { return Status{} }
	}
	s := &Server{ix: ix, status: status, mux: http.NewServeMux(), opts: opts}
	s.mux.HandleFunc("GET /v1/status", s.handleStatus)
	s.mux.HandleFunc("GET /v1/blocks", s.handleBlocks)
	s.mux.HandleFunc("GET /v1/blocks/{number}", s.handleBlock)
	s.mux.HandleFunc("GET /v1/transactions", s.handleTransactions)
	s.mux.HandleFunc("GET /v1/transactions/{id}", s.handleTransaction)
	s.mux.HandleFunc("GET /v1/keys", s.handleKeys)
	s.mux.HandleFunc("GET /v1/keys/history", s.handleKeyHistory)
	s.mux.HandleFunc("GET /v1/framework-keys", s.handleFrameworkKeys)
	s.mux.HandleFunc("GET /v1/phylum-versions", s.handlePhylumVersions)
	s.mux.HandleFunc("GET /v1/stream/blocks", s.handleStream)
	return s
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s.opts.AllowOrigin != "" {
		w.Header().Set("Access-Control-Allow-Origin", s.opts.AllowOrigin)
		w.Header().Set("Access-Control-Allow-Headers", "Last-Event-ID")
	}
	s.mux.ServeHTTP(w, r)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	if err := enc.Encode(v); err != nil {
		logrus.WithError(err).Debug("write response")
	}
}

func writeError(w http.ResponseWriter, code int, format string, args ...any) {
	writeJSON(w, code, map[string]string{"error": fmt.Sprintf(format, args...)})
}

// queryUint reads an unsigned query parameter, or def when it is absent.
func queryUint(r *http.Request, name string, def uint64) (uint64, error) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return def, nil
	}
	n, err := strconv.ParseUint(v, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s: not an unsigned integer", name)
	}
	return n, nil
}

// queryLimit reads the limit parameter, from 1 to max, or def when it is
// absent.
func queryLimit(r *http.Request, def, max int) (int, error) {
	v := r.URL.Query().Get("limit")
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 || n > max {
		return 0, fmt.Errorf("limit: must be from 1 to %d", max)
	}
	return n, nil
}

// StatusResponse is the body of GET /v1/status.
type StatusResponse struct {
	Source  Status `json:"source"`
	Channel string `json:"channel"`
	// Height is the number of the next block; FirstBlock the first block
	// the explorer holds. Both are 0 before the first block.
	Height     uint64 `json:"height"`
	FirstBlock uint64 `json:"first_block"`
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	height, have := s.ix.Height()
	resp := StatusResponse{Channel: s.ix.Channel(), Source: s.status()}
	if have {
		resp.Height = height
		resp.FirstBlock = s.ix.First()
	}
	writeJSON(w, http.StatusOK, resp)
}

// BlocksResponse is the body of GET /v1/blocks.
type BlocksResponse struct {
	Blocks []BlockSummary `json:"blocks"`
	// NextBefore is the before parameter of the next page, or 0 at the end.
	NextBefore uint64 `json:"next_before,omitempty"`
}

func (s *Server) handleBlocks(w http.ResponseWriter, r *http.Request) {
	before, err := queryUint(r, "before", 0)
	if err != nil {
		writeError(w, http.StatusBadRequest, "%v", err)
		return
	}
	limit, err := queryLimit(r, defaultBlockLimit, maxBlockLimit)
	if err != nil {
		writeError(w, http.StatusBadRequest, "%v", err)
		return
	}
	blocks := s.ix.Blocks(before, limit)
	resp := BlocksResponse{Blocks: blocks}
	if n := len(blocks); n == limit && blocks[n-1].Number > s.ix.First() {
		resp.NextBefore = blocks[n-1].Number
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleBlock(w http.ResponseWriter, r *http.Request) {
	num, err := strconv.ParseUint(r.PathValue("number"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "block number: not an unsigned integer")
		return
	}
	blk, ok := s.ix.Block(num)
	if !ok {
		writeError(w, http.StatusNotFound, "block %d not found", num)
		return
	}
	writeJSON(w, http.StatusOK, blk)
}

// TransactionsResponse is the body of GET /v1/transactions.
type TransactionsResponse struct {
	Transactions []TxSummary `json:"transactions"`
}

func (s *Server) handleTransactions(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := TxFilter{Method: q.Get("method")}
	var err error
	if f.Before, err = queryUint(r, "before", 0); err != nil {
		writeError(w, http.StatusBadRequest, "%v", err)
		return
	}
	if f.Limit, err = queryLimit(r, defaultTxLimit, maxTxLimit); err != nil {
		writeError(w, http.StatusBadRequest, "%v", err)
		return
	}
	if v := q.Get("valid"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "valid: not true or false")
			return
		}
		f.Valid = &b
	}
	writeJSON(w, http.StatusOK, TransactionsResponse{Transactions: s.ix.Transactions(f)})
}

func (s *Server) handleTransaction(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	tx, ok := s.ix.Transaction(id)
	if !ok {
		writeError(w, http.StatusNotFound, "transaction %q not found", id)
		return
	}
	writeJSON(w, http.StatusOK, tx)
}

// KeysResponse is the body of GET /v1/keys.
type KeysResponse struct {
	Next *KeyCursor `json:"next,omitempty"`
	Keys []KeyState `json:"keys"`
}

// KeyCursor is the after_namespace and after_key parameters of the next
// page.
type KeyCursor struct {
	Namespace string `json:"after_namespace"`
	Key       string `json:"after_key"`
}

func (s *Server) handleKeys(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := KeyFilter{
		Namespace:      q.Get("namespace"),
		Prefix:         q.Get("prefix"),
		Kind:           q.Get("kind"),
		Space:          q.Get("space"),
		AfterNamespace: q.Get("after_namespace"),
		AfterKey:       q.Get("after_key"),
	}
	switch f.Kind {
	case "", "phylum", "framework":
	default:
		writeError(w, http.StatusBadRequest, "kind: must be phylum or framework")
		return
	}
	var err error
	if f.Limit, err = queryLimit(r, defaultKeyLimit, maxKeyLimit); err != nil {
		writeError(w, http.StatusBadRequest, "%v", err)
		return
	}
	keys := s.ix.Keys(f)
	resp := KeysResponse{Keys: keys}
	if n := len(keys); n == f.Limit {
		resp.Next = &KeyCursor{Namespace: keys[n-1].Namespace, Key: keys[n-1].Key}
	}
	writeJSON(w, http.StatusOK, resp)
}

// KeyHistoryResponse is the body of GET /v1/keys/history.
type KeyHistoryResponse struct {
	Namespace string      `json:"namespace"`
	Key       string      `json:"key"`
	Space     string      `json:"space,omitempty"`
	Writes    []*KeyWrite `json:"writes"`
}

func (s *Server) handleKeyHistory(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	ns, key := q.Get("namespace"), q.Get("key")
	if ns == "" || !q.Has("key") {
		writeError(w, http.StatusBadRequest, "namespace and key are required")
		return
	}
	writes := s.ix.KeyHistory(ns, key)
	if len(writes) == 0 {
		writeError(w, http.StatusNotFound, "no valid transaction wrote key %q in namespace %q", key, ns)
		return
	}
	writeJSON(w, http.StatusOK, KeyHistoryResponse{Namespace: ns, Key: key, Space: frameworkSpaceOf(key), Writes: writes})
}

// FrameworkKeysResponse is the body of GET /v1/framework-keys.
type FrameworkKeysResponse struct {
	Spaces []SpaceSummary `json:"spaces"`
	Phylum SpaceSummary   `json:"phylum"`
}

func (s *Server) handleFrameworkKeys(w http.ResponseWriter, r *http.Request) {
	spaces, phylum := s.ix.FrameworkSummary()
	writeJSON(w, http.StatusOK, FrameworkKeysResponse{Spaces: spaces, Phylum: phylum})
}

// PhylumVersionsResponse is the body of GET /v1/phylum-versions.
type PhylumVersionsResponse struct {
	Versions []PhylumVersion `json:"versions"`
}

func (s *Server) handlePhylumVersions(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, PhylumVersionsResponse{Versions: s.ix.PhylumVersions()})
}

// handleStream sends each block as a server-sent event, id the block
// number and type "block", data the Block JSON. It starts after the block in
// Last-Event-ID (a reconnecting EventSource sends it), or at the from
// parameter, or at the tip (only new blocks).
func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming is not supported")
		return
	}
	next, _ := s.ix.Height()
	if last := r.Header.Get("Last-Event-ID"); last != "" {
		n, err := strconv.ParseUint(last, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "Last-Event-ID: not a block number")
			return
		}
		next = n + 1
	} else if r.URL.Query().Has("from") {
		n, err := queryUint(r, "from", 0)
		if err != nil {
			writeError(w, http.StatusBadRequest, "%v", err)
			return
		}
		next = n
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if _, err := fmt.Fprint(w, "retry: 3000\n\n"); err != nil {
		return
	}
	flusher.Flush()

	ticker := time.NewTicker(s.opts.Heartbeat)
	defer ticker.Stop()
	ctx := r.Context()
	for {
		// Take the change channel before reading, so a block added between
		// the read and the wait still wakes the stream.
		changed := s.ix.Changed()
		blocks := s.ix.BlocksFrom(next, streamBatch)
		for _, b := range blocks {
			data, err := json.Marshal(b)
			if err != nil {
				logrus.WithError(err).Error("encode block event")
				return
			}
			if _, err := fmt.Fprintf(w, "id: %d\nevent: block\ndata: %s\n\n", b.Number, data); err != nil {
				return
			}
			next = b.Number + 1
		}
		if len(blocks) > 0 {
			flusher.Flush()
			if len(blocks) == streamBatch {
				continue
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-changed:
		case <-ticker.C:
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}
