package event

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cowhorse05/KubeNode-Observer/internal/probe"
	"github.com/cowhorse05/KubeNode-Observer/internal/topology"
)

type fakeResolver struct {
	resolved bool
}

func (f *fakeResolver) Resolve(cgroupID uint64) (topology.Container, string, bool) {
	if !f.resolved || cgroupID != 42 {
		return topology.Container{}, "unresolved", false
	}
	return topology.Container{Namespace: "default", PodName: "demo", Name: "app", ID: "container-id"}, "tombstone", true
}

func TestStoreReenrichesUnresolvedEvent(t *testing.T) {
	resolver := &fakeResolver{}
	store := NewStore(10, resolver)
	record := store.Add(probe.ExecEvent{Type: probe.EventTypeExit, CgroupID: 42, PID: 10, ExitCode: 512})
	if record.Workload != nil || record.MetadataSource != "unresolved" {
		t.Fatalf("unexpected initial event: %#v", record)
	}
	if record.ExitCode != 2 || record.RawExitCode != 512 {
		t.Fatalf("exit code was not decoded: %#v", record)
	}
	resolver.resolved = true

	request := httptest.NewRequest(http.MethodGet, "/api/v1/events?namespace=default&pod=demo&type=exit&limit=5", nil)
	response := httptest.NewRecorder()
	store.ServeHTTP(response, request)
	var payload Response
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Events) != 1 || payload.Events[0].Workload.ContainerName != "app" || payload.Events[0].MetadataSource != "tombstone" {
		t.Fatalf("unexpected response: %#v", payload)
	}
}
