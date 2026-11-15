package explorer

import (
	"context"
	"errors"
	"sync"
	"time"

	cb "github.com/hyperledger/fabric-protos-go/common"
	"github.com/sirupsen/logrus"

	internalledger "github.com/luthersystems/blockinspector/internal/ledger"
)

// Source streams a channel's blocks.
type Source interface {
	// Name describes the source, for the status view.
	Name() string
	// Live reports whether the source follows new blocks. A source that is
	// not live ends after its last block.
	Live() bool
	// Stream calls fn with each block from req.Next (or from the first
	// block the source has, when req.FromStart is true), in order, until ctx
	// ends, fn fails, or the source ends or fails.
	Stream(ctx context.Context, req StreamRequest, fn func(*cb.Block) error) error
}

// StreamRequest says where a stream starts.
type StreamRequest struct {
	// OnOpen, when set, is called once the source has accepted the request,
	// before the first block. A live source at the tip sends no block for a
	// while, so this is how the follower knows it is connected.
	OnOpen    func()
	Next      uint64
	FromStart bool
}

// Status is the state of the source that fills the index.
type Status struct {
	LastErrorAt *time.Time `json:"last_error_at,omitempty"`
	ConnectedAt *time.Time `json:"connected_at,omitempty"`
	Source      string     `json:"source"`
	LastError   string     `json:"last_error,omitempty"`
	Live        bool       `json:"live"`
	Connected   bool       `json:"connected"`
	Done        bool       `json:"done,omitempty"`
}

// Follower fills an index from a source, and reconnects a live source that
// fails, with backoff.
type Follower struct {
	src    Source
	ix     *Index
	status Status
	mu     sync.Mutex
	// MinBackoff and MaxBackoff bound the wait between reconnects.
	MinBackoff time.Duration
	MaxBackoff time.Duration
}

// NewFollower returns a follower of src into ix.
func NewFollower(src Source, ix *Index) *Follower {
	return &Follower{
		src:        src,
		ix:         ix,
		status:     Status{Source: src.Name(), Live: src.Live()},
		MinBackoff: time.Second,
		MaxBackoff: 30 * time.Second,
	}
}

// Status returns the source's state.
func (f *Follower) Status() Status {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.status
}

func (f *Follower) setConnected(connected bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status.Connected = connected
	if connected {
		now := time.Now().UTC()
		f.status.ConnectedAt = &now
	}
}

func (f *Follower) setError(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := time.Now().UTC()
	f.status.Connected = false
	f.status.LastError = err.Error()
	f.status.LastErrorAt = &now
}

// Run fills the index until ctx ends. For a source that is not live, it
// returns after the source's last block.
func (f *Follower) Run(ctx context.Context) error {
	backoff := f.MinBackoff
	for {
		next, have := f.ix.Height()
		req := StreamRequest{Next: next, FromStart: !have, OnOpen: func() {
			backoff = f.MinBackoff
			f.setConnected(true)
		}}
		err := f.src.Stream(ctx, req, f.ix.Add)
		if ctx.Err() != nil {
			f.setConnected(false)
			return ctx.Err()
		}
		if !f.src.Live() {
			f.setConnected(false)
			if err != nil {
				f.setError(err)
				return err
			}
			f.mu.Lock()
			f.status.Done = true
			f.mu.Unlock()
			return nil
		}
		if err == nil {
			err = errors.New("source ended")
		}
		f.setError(err)
		logrus.WithError(err).WithField("retry_in", backoff).Warn("block source failed")
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > f.MaxBackoff {
			backoff = f.MaxBackoff
		}
	}
}

// LedgerSource reads a peer's or an orderer's block files, offline. It is
// not live: it ends after the last block in the files.
type LedgerSource struct {
	Dir     string
	Channel string
	Orderer bool
}

// Name describes the source.
func (s *LedgerSource) Name() string { return "ledger:" + s.Dir }

// Live reports false.
func (s *LedgerSource) Live() bool { return false }

// Stream reads the blocks from req.Next to the end of the files.
func (s *LedgerSource) Stream(ctx context.Context, req StreamRequest, fn func(*cb.Block) error) error {
	iter, err := internalledger.RangeBlocks(s.Dir, s.Orderer, s.Channel, 0, 0)
	if err != nil {
		return err
	}
	defer iter.Close()
	if req.OnOpen != nil {
		req.OnOpen()
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		blk, err := iter.Next()
		if err != nil {
			return err
		}
		if blk == nil {
			return nil
		}
		if !req.FromStart && blk.GetHeader().GetNumber() < req.Next {
			continue
		}
		if err := fn(&blk.Block); err != nil {
			return err
		}
	}
}
