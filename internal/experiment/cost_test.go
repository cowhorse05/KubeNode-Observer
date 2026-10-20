package experiment

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/cowhorse05/KubeNode-Observer/internal/telemetry"
)

func TestCostManifestPreservesOriginal(t *testing.T) {
	original := []byte(`{"spec":{"template":{"spec":{"hostPID":true,"containers":[{"name":"agent","image":"old:dev","args":["--cpu-interval=1s","--debug-listen","127.0.0.1:9999","--debug-listen=127.0.0.1:8888"],"resources":{"requests":{"cpu":"2","memory":"64Mi"},"limits":{"cpu":"2","memory":"128Mi"}}}]}}}}`)
	saved := string(original)
	manifest, err := costManifest(original, "local/agent:cost", "200m")
	if err != nil {
		t.Fatal(err)
	}
	if string(original) != saved {
		t.Fatal("original modified")
	}
	var ds map[string]any
	if err := json.Unmarshal(manifest, &ds); err != nil {
		t.Fatal(err)
	}
	container := ds["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)
	if container["image"] != "local/agent:cost" || container["imagePullPolicy"] != "Never" {
		t.Fatal("incorrect image")
	}
	if args := container["args"].([]any); len(args) != 2 || args[1] != "--debug-listen=127.0.0.1:18090" {
		t.Fatalf("args: %v", args)
	}
	resources := container["resources"].(map[string]any)
	if limits := resources["limits"].(map[string]any); limits["cpu"] != "200m" || limits["memory"] != "128Mi" {
		t.Fatalf("limits: %v", limits)
	}
	if requests := resources["requests"].(map[string]any); requests["cpu"] != "100m" || requests["memory"] != "64Mi" {
		t.Fatalf("requests: %v", requests)
	}
	if _, err := costManifest([]byte(`{}`), "x", "1"); err == nil {
		t.Fatal("missing container accepted")
	}
}

func TestCostCounterDeltas(t *testing.T) {
	before := telemetry.Snapshot{At: time.Unix(100, 0), CgroupPath: "/agent", CPUMax: "20000 100000", TotalAlloc: 100, CPUStat: map[string]uint64{"usage_usec": 100, "throttled_usec": 100, "nr_throttled": 2}, Stages: map[string]telemetry.Stage{"cpu.scan": {Count: 2, TotalNS: 100, MaxNS: 80, Over250MS: 1}}}
	after := telemetry.Snapshot{At: time.Unix(110, 0), CgroupPath: "/agent", CPUMax: before.CPUMax, TotalAlloc: 500, CPUStat: map[string]uint64{"usage_usec": 2000100, "throttled_usec": 1000100, "nr_throttled": 12}, Stages: map[string]telemetry.Stage{"cpu.scan": {Count: 12, TotalNS: 1100, MaxNS: 800, Over250MS: 3}}}
	got, err := summarizeCost(before, after)
	if err != nil {
		t.Fatal(err)
	}
	if got.AgentCPUSeconds != 2 || got.ThrottledSeconds != 1 || got.ThrottledPeriods != 10 || got.AllocatedBytes != 400 || got.Stages["cpu.scan"].Count != 10 || got.Stages["cpu.scan"].MaxNS != 0 || got.Stages["cpu.scan"].Over250MS != 2 {
		t.Fatalf("delta: %+v", got)
	}
	after.CPUStat["usage_usec"] = 0
	if _, err := summarizeCost(before, after); err == nil || !strings.Contains(err.Error(), "counter") {
		t.Fatal("counter regression not rejected")
	}
}
