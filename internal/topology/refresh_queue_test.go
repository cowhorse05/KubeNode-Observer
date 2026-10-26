package topology

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type retryResolver struct {
	mu      sync.Mutex
	calls   int
	foundAt int
	called  chan int
}

func (s *retryResolver) Refresh(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.called != nil {
		s.called <- s.calls
	}
	if s.calls == 1 {
		return errors.New("container not ready")
	}
	return nil
}
func (s *retryResolver) Resolve(uint64) (Container, string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Container{}, "current", s.foundAt > 0 && s.calls >= s.foundAt
}

func TestRefreshQueueRetriesWithoutAnotherExec(t *testing.T) {
	s := &retryResolver{foundAt: 2, called: make(chan int, 10)}
	q := NewRefreshQueue(s)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); q.Run(ctx) }()
	defer func() { cancel(); <-done }()
	q.Request(42)
	for want := 1; want <= 2; want++ {
		select {
		case got := <-s.called:
			if got != want {
				t.Fatal(got)
			}
		case <-time.After(4 * time.Second):
			t.Fatal("retry requires another event")
		}
	}
}

func TestRefreshQueueBoundsRetriesAndExpiresUnknownCgroups(t *testing.T) {
	s := &retryResolver{}
	q := NewRefreshQueue(s)
	for i := 0; i < 100; i++ {
		q.Request(42)
	}
	if len(q.pending) != 1 {
		t.Fatal(len(q.pending))
	}
	now := time.Now()
	for i := 0; i < 10; i++ {
		q.refresh(context.Background(), now)
	}
	if s.calls != 5 {
		t.Fatal(s.calls)
	}
	q.refresh(context.Background(), now.Add(31*time.Second))
	if len(q.pending) != 0 {
		t.Fatal("unknown cgroups retained forever")
	}
	for i := uint64(0); i < 128; i++ {
		if !q.Request(i) {
			t.Fatal(i)
		}
	}
	if q.Request(999) || len(q.pending) != 128 {
		t.Fatal("queue not bounded")
	}
}

func TestRefreshQueueRemovesResolvedAndHandlesConcurrentRequests(t *testing.T) {
	s := &retryResolver{foundAt: 1}
	q := NewRefreshQueue(s)
	var wg sync.WaitGroup
	for i := uint64(0); i < 100; i++ {
		wg.Add(1)
		go func(id uint64) { defer wg.Done(); q.Request(id) }(i)
	}
	wg.Wait()
	q.refresh(context.Background(), time.Now())
	if len(q.pending) != 0 || s.calls != 1 {
		t.Fatal("resolved requests not coalesced/removed")
	}
}
