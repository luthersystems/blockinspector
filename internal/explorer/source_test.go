package explorer

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	cb "github.com/hyperledger/fabric-protos-go/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// flakySource fails after its first blocks, then resumes.
type flakySource struct {
	blocks []*cb.Block
	calls  []struct {
		next      uint64
		fromStart bool
	}
	mu sync.Mutex
}

func (s *flakySource) Name() string { return "flaky" }
func (s *flakySource) Live() bool   { return true }

func (s *flakySource) Stream(ctx context.Context, req StreamRequest, fn func(*cb.Block) error) error {
	next := req.Next
	s.mu.Lock()
	s.calls = append(s.calls, struct {
		next      uint64
		fromStart bool
	}{next, req.FromStart})
	first := len(s.calls) == 1
	s.mu.Unlock()
	req.OnOpen()
	if first {
		for _, b := range s.blocks[:3] {
			if err := fn(b); err != nil {
				return err
			}
		}
		return errors.New("connection reset")
	}
	for _, b := range s.blocks[next:] {
		if err := fn(b); err != nil {
			return err
		}
	}
	<-ctx.Done()
	return ctx.Err()
}

func TestFollowerReconnects(t *testing.T) {
	src := &flakySource{blocks: testChain(t)}
	ix := NewIndex("luther")
	f := NewFollower(src, ix)
	f.MinBackoff = time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- f.Run(ctx) }()

	require.Eventually(t, func() bool {
		h, _ := ix.Height()
		return h == uint64(len(src.blocks))
	}, 5*time.Second, time.Millisecond)
	st := f.Status()
	assert.True(t, st.Connected)
	assert.True(t, st.Live)
	assert.Equal(t, "connection reset", st.LastError)
	assert.NotNil(t, st.LastErrorAt)

	cancel()
	assert.ErrorIs(t, <-done, context.Canceled)
	assert.False(t, f.Status().Connected)

	src.mu.Lock()
	defer src.mu.Unlock()
	require.Len(t, src.calls, 2)
	assert.True(t, src.calls[0].fromStart)
	assert.False(t, src.calls[1].fromStart)
	assert.Equal(t, uint64(3), src.calls[1].next)
}

func TestLedgerSourceMissing(t *testing.T) {
	f := NewFollower(&LedgerSource{Dir: t.TempDir(), Channel: "luther"}, NewIndex("luther"))
	require.Error(t, f.Run(context.Background()))
	assert.NotEmpty(t, f.Status().LastError)
}
