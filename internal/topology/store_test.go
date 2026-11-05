package topology

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCurrentSnapshotRequiresFreshSuccessfulTopology(t *testing.T) {
	s := NewStore(fakeCollector{})
	if _, err := s.CurrentSnapshot(); err == nil {
		t.Fatal("missing topology accepted")
	}
	s.snapshot.Store(&Snapshot{GeneratedAt: time.Now().Add(-time.Minute)})
	if _, err := s.CurrentSnapshot(); err == nil {
		t.Fatal("stale topology accepted")
	}
	s.snapshot.Store(&Snapshot{GeneratedAt: time.Now()})
	if _, err := s.CurrentSnapshot(); err != nil {
		t.Fatal(err)
	}
	s.collector = fakeCollector{err: fmt.Errorf("CRI unavailable")}
	_ = s.Refresh(context.Background())
	if _, err := s.CurrentSnapshot(); err == nil {
		t.Fatal("failed refresh hidden behind previous snapshot")
	}
}

type fakeCollector struct {
	snapshot Snapshot
	err      error
}

func (f fakeCollector) Snapshot(context.Context) (Snapshot, error) {
	return f.snapshot, f.err
}

type sequenceCollector struct {
	snapshots []Snapshot
	index     int
}

func (s *sequenceCollector) Snapshot(context.Context) (Snapshot, error) {
	result := s.snapshots[s.index]
	if s.index < len(s.snapshots)-1 {
		s.index++
	}
	return result, nil
}

func TestStoreFiltersTopology(t *testing.T) {
	store := NewStore(fakeCollector{snapshot: Snapshot{
		GeneratedAt: time.Unix(10, 0),
		NodeName:    "node-a",
		NodeUID:     "node-uid",
		Containers: []Container{
			{Namespace: "default", PodName: "demo"},
			{Namespace: "kube-system", PodName: "dns"},
		},
	}})
	store.refresh(context.Background())

	request := httptest.NewRequest(http.MethodGet, "/api/v1/topology?namespace=default&pod=demo", nil)
	response := httptest.NewRecorder()
	store.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	var payload Response
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Snapshot == nil || len(payload.Snapshot.Containers) != 1 || payload.Snapshot.Containers[0].PodName != "demo" {
		t.Fatalf("unexpected response: %#v", payload)
	}
	if payload.Snapshot.NodeUID != "node-uid" {
		t.Fatalf("node uid = %q", payload.Snapshot.NodeUID)
	}
}

func TestStoreResolvesDeletedContainerFromTombstone(t *testing.T) {
	collector := &sequenceCollector{snapshots: []Snapshot{
		{Containers: []Container{{CgroupID: 42, Namespace: "default", PodName: "demo"}}},
		{Containers: []Container{}},
	}}
	store := NewStore(collector)
	if err := store.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, source, ok := store.Resolve(42); !ok || source != "current" {
		t.Fatalf("initial resolution = %q, %v", source, ok)
	}
	if err := store.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	container, source, ok := store.Resolve(42)
	if !ok || source != "tombstone" || container.PodName != "demo" {
		t.Fatalf("tombstone resolution = %#v, %q, %v", container, source, ok)
	}
}
