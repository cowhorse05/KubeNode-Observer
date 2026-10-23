package experiment

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cowhorse05/KubeNode-Observer/internal/telemetry"
)

type MemoryPoint struct {
	At        time.Time         `json:"at"`
	Phase     string            `json:"phase"`
	RawFile   string            `json:"rawFile"`
	RSS       uint64            `json:"rssBytes"`
	Heap      uint64            `json:"heapAllocBytes"`
	GoManaged uint64            `json:"goManagedBytes"`
	GoLimit   int64             `json:"goMemoryLimitBytes"`
	Current   uint64            `json:"cgroupCurrentBytes"`
	Peak      *uint64           `json:"cgroupPeakBytes,omitempty"`
	Anon      uint64            `json:"anonBytes"`
	File      uint64            `json:"fileBytes"`
	Kernel    uint64            `json:"kernelBytes"`
	NumGC     uint32            `json:"numGc"`
	Events    map[string]uint64 `json:"memoryEvents"`
	Metrics   MetricsRead       `json:"metrics"`
}

type MemoryCase struct {
	Setting     string        `json:"setting"`
	Prefix      string        `json:"prefix"`
	Samples     []MemoryPoint `json:"samples"`
	Pulses      []Workload    `json:"pulses"`
	CPUSeconds  float64       `json:"cpuSeconds"`
	WallSeconds float64       `json:"wallSeconds"`
}

func memoryManifest(original []byte, image, setting string) ([]byte, error) {
	if setting != "original" && setting != "80MiB" {
		return nil, fmt.Errorf("invalid memory variant")
	}
	manifest, err := costManifest(original, image, "200m")
	if err != nil {
		return nil, err
	}
	var ds map[string]any
	if err := json.Unmarshal(manifest, &ds); err != nil {
		return nil, err
	}
	container := ds["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)
	container["resources"].(map[string]any)["limits"].(map[string]any)["memory"] = "128Mi"
	container["resources"].(map[string]any)["requests"].(map[string]any)["memory"] = "32Mi"
	if setting != "original" {
		env, _ := container["env"].([]any)
		next := make([]any, 0, len(env)+1)
		for _, entry := range env {
			item, ok := entry.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("invalid environment entry")
			}
			if item["name"] != "GOMEMLIMIT" {
				next = append(next, item)
			}
		}
		container["env"] = append(next, map[string]any{"name": "GOMEMLIMIT", "value": setting})
	}
	return json.Marshal(ds)
}

func (r *runner) memory() error {
	for index, setting := range strings.Split(r.config.MemoryGoLimits, ",") {
		prefix := fmt.Sprintf("memory-%02d-%s", index+1, setting)
		manifest, err := memoryManifest(r.original, r.config.MemoryImage, setting)
		if err != nil {
			return err
		}
		r.active = manifest
		if err := r.write(prefix+"-manifest.json", manifest); err != nil {
			return err
		}
		if err := r.agent("on", prefix); err != nil {
			return err
		}
		var endpoint string
		r.debugForward, endpoint, err = r.forward(prefix+"-debug-forward.txt", "kube-system", "daemonset/kno-agent", 18090)
		if err != nil {
			return err
		}
		r.summary.Memory = append(r.summary.Memory, MemoryCase{Setting: setting, Prefix: prefix})
		item := &r.summary.Memory[len(r.summary.Memory)-1]
		if err := r.memoryCase(item, endpoint); err != nil {
			captureCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			body, _ := r.kube(captureCtx, nil, "-n", "kube-system", "get", "pods", "-l", "app.kubernetes.io/name=kno-agent", "-o", "json")
			_ = r.write(prefix+"-failure-state.json", body)
			logs, _ := r.kube(captureCtx, nil, "-n", "kube-system", "logs", "daemonset/kno-agent", "--previous", "--tail=80")
			_ = r.write(prefix+"-previous-log.txt", logs)
			cancel()
			return err
		}
	}
	// Parse heap profiles only after all workloads/observations are complete.
	for _, item := range r.summary.Memory {
		for _, phase := range []string{"before", "after-churn", "after-idle"} {
			name := item.Prefix + "-" + phase
			top, err := r.command(r.ctx, nil, "go", "tool", "pprof", "-top", "-inuse_space", "-nodecount=25", filepath.Join(r.dir, name+"-heap.pb.gz"))
			if err != nil {
				return err
			}
			if err := r.write(name+"-heap-top.txt", top); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *runner) memoryCase(item *MemoryCase, endpoint string) error {
	uid, err := r.podState(item.Prefix + "-before")
	if err != nil {
		return err
	}
	var anchor string
	var first, last telemetry.Snapshot
	sample := func(phase string) error {
		name := fmt.Sprintf("%s-%03d-%s", item.Prefix, len(item.Samples)+1, phase)
		var current telemetry.Snapshot
		if err := r.get(r.ctx, endpoint+"/diagnostics", name+".json", &current); err != nil {
			return err
		}
		if current.Error != "" || current.MemoryError != "" || current.RSSError != "" || current.Memory == nil {
			return fmt.Errorf("incomplete memory diagnostics: CPU=%s memory=%s RSS=%s", current.Error, current.MemoryError, current.RSSError)
		}
		if current.Memory.Max != "134217728" {
			return fmt.Errorf("memory experiment expected 128Mi, got %s", current.Memory.Max)
		}
		if item.Setting == "80MiB" && current.GoMemoryLimit != 80<<20 {
			return fmt.Errorf("GOMEMLIMIT not applied: %d", current.GoMemoryLimit)
		}
		if anchor == "" {
			anchor = current.CgroupPath
			first = current
		} else if anchor != current.CgroupPath {
			return fmt.Errorf("Agent cgroup changed during memory case")
		}
		last = current
		if err := r.get(r.ctx, r.agentEndpoint+"/metrics", name+"-metrics.txt", nil); err != nil {
			return err
		}
		body, err := os.ReadFile(filepath.Join(r.dir, name+"-metrics.txt"))
		if err != nil {
			return err
		}
		metrics, err := parseMetrics(body)
		if err != nil {
			return err
		}
		m := current.Memory
		item.Samples = append(item.Samples, MemoryPoint{At: current.At, Phase: phase, RawFile: name + ".json", RSS: current.RSS, Heap: current.HeapAlloc, GoManaged: current.GoManaged, GoLimit: current.GoMemoryLimit, Current: m.Current, Peak: m.Peak, Anon: m.Stat["anon"], File: m.Stat["file"], Kernel: m.Stat["kernel"], NumGC: current.NumGC, Events: m.Events, Metrics: metrics})
		if err := r.writeJSON("summary.json", r.summary); err != nil {
			return err
		}
		if m.Events["oom_kill"] > first.Memory.Events["oom_kill"] || m.Events["oom"] > first.Memory.Events["oom"] {
			return fmt.Errorf("Agent cgroup reported OOM")
		}
		return nil
	}
	if err := sample("before"); err != nil {
		return err
	}
	if err := r.get(r.ctx, endpoint+"/debug/pprof/heap?gc=0", item.Prefix+"-before-heap.pb.gz", nil); err != nil {
		return err
	}
	for pulse := 1; pulse <= r.config.MemoryPulses; pulse++ {
		ctx, cancel := context.WithCancel(r.ctx)
		type completion struct {
			work Workload
			err  error
		}
		finished := make(chan completion, 1)
		go func() {
			w, e := r.workload(ctx, "churn", r.config.Duration, 0, fmt.Sprintf("%s-pulse-%02d.json", item.Prefix, pulse))
			finished <- completion{w, e}
		}()
		joined := false
		observeErr := func() error {
			defer func() {
				cancel()
				if !joined {
					<-finished
				}
			}()
			ticker := time.NewTicker(r.config.MemoryInterval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case result := <-finished:
					joined = true
					if result.err != nil {
						return result.err
					}
					item.Pulses = append(item.Pulses, result.work)
					return sample("churn")
				case <-ticker.C:
					if err := sample("churn"); err != nil {
						return err
					}
				}
			}
		}()
		if observeErr != nil {
			return observeErr
		}
		fmt.Printf("memory %s pulse %d/%d: cgroup %.1fMiB, RSS %.1fMiB, heap %.1fMiB\n", item.Setting, pulse, r.config.MemoryPulses, float64(last.Memory.Current)/(1<<20), float64(last.RSS)/(1<<20), float64(last.HeapAlloc)/(1<<20))
	}
	if err := r.get(r.ctx, endpoint+"/debug/pprof/heap?gc=0", item.Prefix+"-after-churn-heap.pb.gz", nil); err != nil {
		return err
	}
	deadline := time.Now().Add(r.config.MemoryIdle)
	for time.Now().Before(deadline) {
		wait := r.config.MemoryInterval
		if remaining := time.Until(deadline); remaining < wait {
			wait = remaining
		}
		if err := pause(r.ctx, wait); err != nil {
			return err
		}
		if err := sample("idle"); err != nil {
			return err
		}
	}
	if err := r.get(r.ctx, endpoint+"/debug/pprof/heap?gc=0", item.Prefix+"-after-idle-heap.pb.gz", nil); err != nil {
		return err
	}
	after, err := r.podState(item.Prefix + "-after")
	if err != nil {
		return err
	}
	if !bytes.Equal(uid, after) {
		return fmt.Errorf("Agent replaced during memory case")
	}
	if last.CPUStat["usage_usec"] < first.CPUStat["usage_usec"] {
		return fmt.Errorf("CPU counter regressed")
	}
	item.CPUSeconds = float64(last.CPUStat["usage_usec"]-first.CPUStat["usage_usec"]) / 1e6
	item.WallSeconds = last.At.Sub(first.At).Seconds()
	return r.writeJSON("summary.json", r.summary)
}
