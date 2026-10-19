package runqlat

import (
	"testing"
	"time"

	"github.com/cowhorse05/KubeNode-Observer/internal/probe"
	"github.com/cowhorse05/KubeNode-Observer/internal/topology"
)

type fakeResolver struct{}

func (fakeResolver) Resolve(cgroupID uint64) (topology.Container, string, bool) {
	if cgroupID != 42 {
		return topology.Container{}, "unresolved", false
	}
	return topology.Container{
		ID: "container-a", Name: "app", Namespace: "default", PodName: "demo", PodUID: "pod-a",
	}, "current", true
}

func TestBuildResponseCalculatesDeltaAndPercentiles(t *testing.T) {
	before := probe.RunQLatHistogram{CgroupID: 42, TGID: 10, PID: 10, TotalNS: 1000, Count: 1}
	before.Buckets[0] = 1
	after := before
	after.TotalNS = 13000
	after.Count = 4
	after.Buckets[3] = 3
	copy(after.Comm[:], "worker")

	response := buildResponse(time.Unix(100, 0), time.Second, "default", "demo", []probe.RunQLatHistogram{before}, []probe.RunQLatHistogram{after}, fakeResolver{})
	if response.Count != 3 || response.AverageUS != 4 || response.P50US != 15 || response.P99US != 15 {
		t.Fatalf("latency = %#v", response.Latency)
	}
	if len(response.Processes) != 1 || response.Processes[0].Command != "worker" {
		t.Fatalf("processes = %#v", response.Processes)
	}
	if len(response.Buckets) != 1 || response.Buckets[0].LowerUS != 8 || response.Buckets[0].UpperUS != 15 || response.Buckets[0].Count != 3 {
		t.Fatalf("buckets = %#v", response.Buckets)
	}
}

func TestBucketBounds(t *testing.T) {
	tests := []struct {
		index        int
		lower, upper uint64
	}{{0, 1, 1}, {1, 2, 3}, {10, 1024, 2047}}
	for _, test := range tests {
		lower, upper := bucketBounds(test.index)
		if lower != test.lower || upper != test.upper {
			t.Fatalf("bucket %d = %d-%d", test.index, lower, upper)
		}
	}
}
