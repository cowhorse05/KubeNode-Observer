package experiment

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestMemoryManifest(t *testing.T) {
	original := []byte(`{"spec":{"template":{"spec":{"containers":[{"name":"agent","image":"original","env":[{"name":"NODE_NAME","value":"node"},{"name":"GOMEMLIMIT","value":"90MiB"}],"resources":{"limits":{"memory":"256Mi"},"requests":{"memory":"256Mi"}}}]}}}}`)
	for _, setting := range []string{"original", "80MiB"} {
		manifest, err := memoryManifest(original, "new:memory", setting)
		if err != nil {
			t.Fatal(err)
		}
		var ds map[string]any
		if err := json.Unmarshal(manifest, &ds); err != nil {
			t.Fatal(err)
		}
		container := ds["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)
		resources := container["resources"].(map[string]any)
		if resources["limits"].(map[string]any)["memory"] != "128Mi" || resources["requests"].(map[string]any)["memory"] != "32Mi" {
			t.Fatal("incorrect memory budget")
		}
		count := 0
		for _, env := range container["env"].([]any) {
			e := env.(map[string]any)
			if e["name"] == "GOMEMLIMIT" {
				count++
				want := "90MiB"
				if setting == "80MiB" {
					want = setting
				}
				if e["value"] != want {
					t.Fatalf("limit: %v", e)
				}
			}
		}
		if count != 1 {
			t.Fatal("duplicate/missing environment setting")
		}
	}
	if !strings.Contains(string(original), "256Mi") {
		t.Fatal("original changed")
	}
}

func TestMemoryConfig(t *testing.T) {
	c := Config{Mode: "memory", Context: "test", Image: "work", MemoryImage: "agent", MemoryGoLimits: "original,80MiB", MemoryPulses: 5, MemoryIdle: time.Minute, MemoryInterval: 5 * time.Second, Duration: time.Minute, Pairs: 4, Repeats: 1, Warmup: time.Second, Settle: time.Second, Intervals: "1s"}
	if _, err := validate(c); err != nil {
		t.Fatal(err)
	}
	c.MemoryGoLimits = "original,original"
	if _, err := validate(c); err == nil {
		t.Fatal("duplicate setting accepted")
	}
	c.MemoryGoLimits = "arbitrary"
	if _, err := validate(c); err == nil {
		t.Fatal("invalid runtime setting accepted")
	}
}
