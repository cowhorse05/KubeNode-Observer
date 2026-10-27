package topology

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cowhorse05/KubeNode-Observer/internal/telemetry"
)

type SnapshotCollector interface {
	Snapshot(context.Context) (Snapshot, error)
}

type Store struct {
	Timing    *telemetry.Recorder // Set before Run; includes refresh lock wait.
	collector SnapshotCollector
	snapshot  atomic.Pointer[Snapshot]
	refreshMu sync.Mutex
	errorMu   sync.RWMutex
	lastError string
	tombMu    sync.RWMutex
	tombstone map[uint64]expiredContainer
	tombTTL   time.Duration
}

type expiredContainer struct {
	container Container
	expiresAt time.Time
}

func NewStore(collector SnapshotCollector) *Store {
	return &Store{collector: collector, tombstone: make(map[uint64]expiredContainer), tombTTL: 5 * time.Minute}
}

func (s *Store) Run(ctx context.Context, interval time.Duration) {
	s.refresh(ctx)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.refresh(ctx)
		}
	}
}

func (s *Store) refresh(ctx context.Context) {
	_ = s.Refresh(ctx)
}

func (s *Store) Refresh(ctx context.Context) error {
	defer s.Timing.Observe(telemetry.TopologyRefresh, time.Now())
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()

	snapshot, err := s.collector.Snapshot(ctx)
	s.errorMu.Lock()
	if err != nil {
		s.lastError = err.Error()
		s.errorMu.Unlock()
		return err
	}
	s.lastError = ""
	s.errorMu.Unlock()

	s.updateTombstones(&snapshot)
	s.snapshot.Store(&snapshot)
	return nil
}

func (s *Store) updateTombstones(next *Snapshot) {
	now := time.Now()
	currentIDs := make(map[uint64]struct{}, len(next.Containers))
	for _, container := range next.Containers {
		if container.CgroupID != 0 {
			currentIDs[container.CgroupID] = struct{}{}
		}
	}

	s.tombMu.Lock()
	defer s.tombMu.Unlock()
	for id, expired := range s.tombstone {
		if now.After(expired.expiresAt) {
			delete(s.tombstone, id)
		}
	}
	for id := range currentIDs {
		delete(s.tombstone, id)
	}
	if previous := s.snapshot.Load(); previous != nil {
		for _, container := range previous.Containers {
			if container.CgroupID == 0 {
				continue
			}
			if _, stillRunning := currentIDs[container.CgroupID]; !stillRunning {
				s.tombstone[container.CgroupID] = expiredContainer{container: container, expiresAt: now.Add(s.tombTTL)}
			}
		}
	}
}

func (s *Store) Resolve(cgroupID uint64) (Container, string, bool) {
	if cgroupID == 0 {
		return Container{}, "unresolved", false
	}
	if current := s.snapshot.Load(); current != nil {
		for _, container := range current.Containers {
			if container.CgroupID == cgroupID {
				return container, "current", true
			}
		}
	}

	s.tombMu.RLock()
	expired, ok := s.tombstone[cgroupID]
	s.tombMu.RUnlock()
	if ok && time.Now().Before(expired.expiresAt) {
		return expired.container, "tombstone", true
	}
	return Container{}, "unresolved", false
}

func (s *Store) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	response := Response{Snapshot: s.filteredSnapshot(request)}
	s.errorMu.RLock()
	response.Error = s.lastError
	s.errorMu.RUnlock()

	status := http.StatusOK
	if response.Snapshot == nil {
		status = http.StatusServiceUnavailable
	}
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(response)
}

func (s *Store) filteredSnapshot(request *http.Request) *Snapshot {
	current := s.snapshot.Load()
	if current == nil {
		return nil
	}

	namespace := request.URL.Query().Get("namespace")
	pod := request.URL.Query().Get("pod")
	if namespace == "" && pod == "" {
		copy := *current
		return &copy
	}

	filtered := &Snapshot{GeneratedAt: current.GeneratedAt, NodeName: current.NodeName, NodeUID: current.NodeUID, Containers: []Container{}, Sandboxes: []Sandbox{}}
	for _, container := range current.Containers {
		if namespace != "" && container.Namespace != namespace {
			continue
		}
		if pod != "" && container.PodName != pod {
			continue
		}
		filtered.Containers = append(filtered.Containers, container)
	}
	for _, sandbox := range current.Sandboxes {
		if namespace != "" && sandbox.Namespace != namespace {
			continue
		}
		if pod != "" && sandbox.PodName != pod {
			continue
		}
		filtered.Sandboxes = append(filtered.Sandboxes, sandbox)
	}
	return filtered
}

func (s *Store) HasSandbox(id string) bool {
	if snapshot := s.snapshot.Load(); snapshot != nil {
		for _, sandbox := range snapshot.Sandboxes {
			if sandbox.ID == id {
				return true
			}
		}
	}
	return false
}
