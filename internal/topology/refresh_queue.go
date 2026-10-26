package topology

import (
	"context"
	"sync"
	"time"
)

type refreshResolver interface {
	Refresh(context.Context) error
	Resolve(uint64) (Container, string, bool)
}

type refreshAttempt struct {
	attempts int
	expires  time.Time
}

// RefreshQueue coalesces unresolved execs into one bounded global refresher.
// The first CRI read may race container startup; retries must not require a
// second exec event. Unknown host cgroups expire rather than growing forever.
type RefreshQueue struct {
	store   refreshResolver
	mu      sync.Mutex
	pending map[uint64]refreshAttempt
	wake    chan struct{}
}

func NewRefreshQueue(store refreshResolver) *RefreshQueue {
	return &RefreshQueue{store: store, pending: make(map[uint64]refreshAttempt), wake: make(chan struct{}, 1)}
}

func (q *RefreshQueue) Request(id uint64) bool {
	q.mu.Lock()
	now := time.Now()
	q.expire(now)
	if _, exists := q.pending[id]; !exists {
		if len(q.pending) >= 128 {
			q.mu.Unlock()
			return false
		}
		q.pending[id] = refreshAttempt{expires: now.Add(30 * time.Second)}
	}
	q.mu.Unlock()
	select {
	case q.wake <- struct{}{}:
	default:
	}
	return true
}

func (q *RefreshQueue) expire(now time.Time) {
	for id, entry := range q.pending {
		if !now.Before(entry.expires) {
			delete(q.pending, id)
		}
	}
}

func (q *RefreshQueue) refresh(ctx context.Context, now time.Time) bool {
	q.mu.Lock()
	q.expire(now)
	ids := make([]uint64, 0, len(q.pending))
	for id, entry := range q.pending {
		if entry.attempts < 5 {
			ids = append(ids, id)
		}
	}
	q.mu.Unlock()
	if len(ids) == 0 {
		return false
	}
	refreshCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	// Store.Refresh retains its error for the topology API; do not invent a
	// successful mapping on failure. A later attempt can still recover.
	_ = q.store.Refresh(refreshCtx)
	cancel()
	for _, id := range ids {
		_, _, resolved := q.store.Resolve(id)
		q.mu.Lock()
		if entry, exists := q.pending[id]; exists {
			if resolved {
				delete(q.pending, id)
			} else {
				entry.attempts++
				q.pending[id] = entry
			}
		}
		q.mu.Unlock()
	}
	return true
}

func (q *RefreshQueue) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var next time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-q.wake:
		case <-ticker.C:
		}
		if ctx.Err() != nil {
			return
		}
		now := time.Now()
		if now.Before(next) {
			continue
		}
		if q.refresh(ctx, now) {
			next = time.Now().Add(time.Second)
		}
	}
}
