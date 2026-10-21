package experiment

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/cowhorse05/KubeNode-Observer/internal/cpu"
	"github.com/cowhorse05/KubeNode-Observer/internal/telemetry"
	"github.com/cowhorse05/KubeNode-Observer/internal/topology"
)

type CostCase struct {
	Mode               string                     `json:"mode"`
	CPULimit           string                     `json:"cpuLimit"`
	Repeat             int                        `json:"repeat"`
	Prefix             string                     `json:"prefix"`
	WindowSeconds      float64                    `json:"windowSeconds"`
	AgentCPUSeconds    float64                    `json:"agentCpuSeconds"`
	ThrottledSeconds   float64                    `json:"throttledSeconds"`
	ThrottledPeriods   uint64                     `json:"throttledPeriods"`
	AllocatedBytes     uint64                     `json:"allocatedBytes"`
	ScanMaxSeconds     float64                    `json:"accuracyReadMaxSeconds"`
	ScanCount          int                        `json:"accuracyReadCount"`
	SignedErrorPercent float64                    `json:"signedErrorPercent"`
	Stages             map[string]telemetry.Stage `json:"stageDeltas"`
}

// Never modify r.original: restoration must use the exact pre-experiment spec.
func costManifest(original []byte, image, limit string) ([]byte, error) {
	var ds struct {
		Spec struct {
			Template struct {
				Spec struct {
					Containers []json.RawMessage `json:"containers"`
				} `json:"spec"`
			} `json:"template"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(original, &ds); err != nil {
		return nil, err
	}
	if len(ds.Spec.Template.Spec.Containers) != 1 {
		return nil, fmt.Errorf("cost experiment requires a single Agent container")
	}
	var full map[string]any
	if err := json.Unmarshal(original, &full); err != nil {
		return nil, err
	}
	container := full["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)
	container["image"], container["imagePullPolicy"] = image, "Never"
	args, _ := container["args"].([]any)
	filtered := make([]any, 0, len(args)+1)
	for i := 0; i < len(args); i++ {
		arg, ok := args[i].(string)
		if !ok {
			return nil, fmt.Errorf("non-string Agent argument")
		}
		if arg == "--debug-listen" {
			i++
			continue
		}
		if !strings.HasPrefix(arg, "--debug-listen=") {
			filtered = append(filtered, arg)
		}
	}
	container["args"] = append(filtered, "--debug-listen=127.0.0.1:18090")
	resources, ok := container["resources"].(map[string]any)
	if !ok {
		resources = map[string]any{}
		container["resources"] = resources
	}
	limits, ok := resources["limits"].(map[string]any)
	if !ok {
		limits = map[string]any{}
		resources["limits"] = limits
	}
	limits["cpu"] = limit
	requests, ok := resources["requests"].(map[string]any)
	if !ok {
		requests = map[string]any{}
		resources["requests"] = requests
	}
	// A copied request must never exceed the experimental limit.
	requests["cpu"] = "100m"
	return json.Marshal(full)
}

func (r *runner) cost() error {
	// Each case starts a fresh Agent. Reverse both factors on alternate repeats.
	for repeat := 1; repeat <= r.config.Repeats; repeat++ {
		limits, modes := []string{"200m", "1"}, []string{"steady", "churn"}
		if repeat%2 == 0 {
			limits[0], limits[1] = limits[1], limits[0]
			modes[0], modes[1] = modes[1], modes[0]
		}
		for _, limit := range limits {
			for _, mode := range modes {
				prefix := fmt.Sprintf("cost-%s-%s-%02d", limit, mode, repeat)
				manifest, err := costManifest(r.original, r.config.AgentImage, limit)
				if err != nil {
					return err
				}
				r.active = manifest
				if err := r.write(prefix+"-agent-manifest.json", manifest); err != nil {
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
				if err := r.costCase(mode, limit, repeat, prefix, endpoint); err != nil {
					return err
				}
			}
		}
	}
	// Symbolization happens only after every load measurement has finished.
	for _, item := range r.summary.Cost {
		top, err := r.command(r.ctx, nil, "go", "tool", "pprof", "-top", "-nodecount=25", r.dir+"/"+item.Prefix+"-cpu.pb.gz")
		if err != nil {
			return err
		}
		if err := r.write(item.Prefix+"-cpu-top.txt", top); err != nil {
			return err
		}
	}
	return nil
}

func (r *runner) costCase(mode, limit string, repeat int, prefix, debugEndpoint string) error {
	uid, err := r.podState(prefix + "-before")
	if err != nil {
		return err
	}
	var response topology.Response
	for i := 0; i < 30; i++ {
		if err := r.get(r.ctx, r.agentEndpoint+"/api/v1/topology?namespace=default&pod=kno-observer-experiment", prefix+"-topology.json", &response); err != nil {
			return err
		}
		if response.Snapshot != nil && len(response.Snapshot.Containers) == 1 {
			break
		}
		if err := pause(r.ctx, time.Second); err != nil {
			return err
		}
	}
	if response.Snapshot == nil || len(response.Snapshot.Containers) != 1 {
		return fmt.Errorf("workload topology not ready")
	}
	id := response.Snapshot.Containers[0].CgroupID
	ctx, cancel := context.WithCancel(r.ctx)
	defer cancel()
	workFinished := make(chan error, 1)
	workJoined := false
	go func() {
		_, err := r.workload(ctx, mode, r.config.Duration+r.config.Warmup+10*time.Second, 0, prefix+"-workload.json")
		workFinished <- err
	}()
	// Join the workload on every return, including errors/cancellation.
	defer func() {
		cancel()
		if !workJoined {
			<-workFinished
		}
	}()
	started := false
	for i := 0; i < 50; i++ {
		var status struct {
			Running bool `json:"running"`
		}
		if err := r.get(ctx, r.workEndpoint+"/status", prefix+"-start.json", &status); err != nil {
			return err
		}
		if status.Running {
			started = true
			break
		}
		if err := pause(ctx, 100*time.Millisecond); err != nil {
			return err
		}
	}
	if !started {
		return fmt.Errorf("cost workload did not start")
	}
	if err := pause(ctx, r.config.Warmup); err != nil {
		return err
	}
	var before, after telemetry.Snapshot
	if err := r.get(ctx, debugEndpoint+"/diagnostics", prefix+"-diagnostics-before.json", &before); err != nil {
		return err
	}
	if before.Error != "" {
		return fmt.Errorf("self cgroup diagnostics: %s", before.Error)
	}
	// Accuracy scans and one metrics scrape intentionally run inside the profile.
	// This is a diagnostic workload, not an uninstrumented overhead benchmark.
	profileFinished, accuracyFinished := make(chan error, 1), make(chan error, 1)
	go func() {
		profileFinished <- r.get(ctx, debugEndpoint+"/debug/pprof/profile?seconds="+strconv.Itoa(int(r.config.Duration/time.Second)), prefix+"-cpu.pb.gz", nil)
	}()
	var accuracy cpu.AccuracyResponse
	go func() {
		accuracyFinished <- r.get(ctx, r.agentEndpoint+"/api/v1/cpu/accuracy?cgroupId="+strconv.FormatUint(id, 10)+"&duration="+r.config.Duration.String()+"&interval=250ms", prefix+"-accuracy.json", &accuracy)
	}()
	// Always join both HTTP requests before returning or reading accuracy.
	scrapeErr := pause(ctx, r.config.Duration/2)
	if scrapeErr == nil {
		scrapeErr = r.get(ctx, r.agentEndpoint+"/metrics", prefix+"-metrics.txt", nil)
	}
	profileErr, accuracyErr := <-profileFinished, <-accuracyFinished
	if scrapeErr != nil {
		return scrapeErr
	}
	if profileErr != nil {
		return profileErr
	}
	if accuracyErr != nil {
		return accuracyErr
	}
	if err := r.get(ctx, debugEndpoint+"/diagnostics", prefix+"-diagnostics-after.json", &after); err != nil {
		return err
	}
	var status struct {
		Running bool `json:"running"`
	}
	if err := r.get(ctx, r.workEndpoint+"/status", prefix+"-end.json", &status); err != nil {
		return err
	}
	if !status.Running {
		return fmt.Errorf("workload ended before profile window completed")
	}
	// Allow the finite workload to finish before validation or starting another case.
	workErr := <-workFinished
	workJoined = true
	if workErr != nil {
		return workErr
	}
	afterUID, err := r.podState(prefix + "-after")
	if err != nil {
		return err
	}
	if !bytes.Equal(uid, afterUID) {
		return fmt.Errorf("Agent UID changed during cost case")
	}
	item, err := summarizeCost(before, after)
	if err != nil {
		return err
	}
	item.Mode, item.CPULimit, item.Repeat, item.Prefix = mode, limit, repeat, prefix
	item.ScanCount, item.SignedErrorPercent = len(accuracy.Samples), accuracy.SignedErrorPercent
	for _, sample := range accuracy.Samples {
		if sample.ReadSeconds > item.ScanMaxSeconds {
			item.ScanMaxSeconds = sample.ReadSeconds
		}
	}
	r.summary.Cost = append(r.summary.Cost, item)
	fmt.Printf("cost %s %s repeat %d: CPU %.3fs, throttle %.3fs, scan max %.1fms\n", mode, limit, repeat, item.AgentCPUSeconds, item.ThrottledSeconds, item.ScanMaxSeconds*1000)
	return r.writeJSON("summary.json", r.summary)
}

func summarizeCost(before, after telemetry.Snapshot) (CostCase, error) {
	result := CostCase{WindowSeconds: after.At.Sub(before.At).Seconds(), Stages: map[string]telemetry.Stage{}}
	if after.Error != "" || before.Error != "" || before.CgroupPath == "" || before.CgroupPath != after.CgroupPath || before.CPUMax != after.CPUMax || result.WindowSeconds <= 0 || after.TotalAlloc < before.TotalAlloc {
		return result, fmt.Errorf("invalid diagnostic window/cgroup: %s", after.Error)
	}
	for _, key := range []string{"usage_usec", "throttled_usec", "nr_throttled"} {
		a, okA := after.CPUStat[key]
		b, okB := before.CPUStat[key]
		if !okA || !okB || a < b {
			return result, fmt.Errorf("invalid cgroup counter %s", key)
		}
	}
	result.AgentCPUSeconds = float64(after.CPUStat["usage_usec"]-before.CPUStat["usage_usec"]) / 1e6
	result.ThrottledSeconds = float64(after.CPUStat["throttled_usec"]-before.CPUStat["throttled_usec"]) / 1e6
	result.ThrottledPeriods = after.CPUStat["nr_throttled"] - before.CPUStat["nr_throttled"]
	result.AllocatedBytes = after.TotalAlloc - before.TotalAlloc
	for name, a := range after.Stages {
		b := before.Stages[name]
		if a.Count < b.Count || a.TotalNS < b.TotalNS || a.Over100MS < b.Over100MS || a.Over250MS < b.Over250MS || a.Over1S < b.Over1S {
			return result, fmt.Errorf("stage counter regressed: %s", name)
		}
		// Max cannot be differenced. Keep it only in raw cumulative snapshots.
		result.Stages[name] = telemetry.Stage{Count: a.Count - b.Count, TotalNS: a.TotalNS - b.TotalNS, Over100MS: a.Over100MS - b.Over100MS, Over250MS: a.Over250MS - b.Over250MS, Over1S: a.Over1S - b.Over1S}
	}
	return result, nil
}
