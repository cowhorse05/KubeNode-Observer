package experiment

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cowhorse05/KubeNode-Observer/internal/telemetry"
)

type MetricsRead struct {
	At            time.Time `json:"at"`
	ClientSeconds float64   `json:"clientSeconds"`
	Errors        float64   `json:"collectionErrors"`
	CacheSources  int       `json:"cacheSources"`
	ReadySources  int       `json:"readySources"`
	MaxAgeSeconds float64   `json:"maxAgeSeconds"`
	RefreshErrors float64   `json:"refreshErrors"`
	EventsLost    float64   `json:"eventsLost"`
}

type MetricsCase struct {
	Image      string        `json:"image"`
	Prefix     string        `json:"prefix"`
	P50Seconds float64       `json:"p50Seconds"`
	P95Seconds float64       `json:"p95Seconds"`
	MaxSeconds float64       `json:"maxSeconds"`
	Reads      []MetricsRead `json:"reads"`
	Cost       CostCase      `json:"cost"`
	Workload   Workload      `json:"workload"`
}

type MetricsPair struct {
	Number          int         `json:"number"`
	Order           string      `json:"order"`
	Baseline        MetricsCase `json:"baseline"`
	Candidate       MetricsCase `json:"candidate"`
	CPUDeltaPercent float64     `json:"cpuDeltaPercent"`
	P95DeltaPercent float64     `json:"p95DeltaPercent"`
}

func imageManifest(original []byte, image string) ([]byte, error) {
	var ds map[string]any
	if err := json.Unmarshal(original, &ds); err != nil {
		return nil, err
	}
	spec, ok := ds["spec"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("missing DaemonSet spec")
	}
	template, ok := spec["template"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("missing Pod template")
	}
	pod, ok := template["spec"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("missing Pod spec")
	}
	containers, ok := pod["containers"].([]any)
	if !ok || len(containers) != 1 {
		return nil, fmt.Errorf("expected one Agent container")
	}
	container, ok := containers[0].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("invalid Agent container")
	}
	container["image"], container["imagePullPolicy"] = image, "Never"
	return json.Marshal(ds)
}

func (r *runner) metricsComparison() error {
	var cpuDeltas, p95Deltas []float64
	for pair := 1; pair <= r.config.Pairs; pair++ {
		order := []string{"baseline", "candidate"}
		if pair%2 == 0 {
			order[0], order[1] = order[1], order[0]
		}
		result := MetricsPair{Number: pair, Order: strings.Join(order, "/")}
		for _, variant := range order {
			image := r.config.BaselineImage
			if variant == "candidate" {
				image = r.config.CandidateImage
			}
			prefix := fmt.Sprintf("metrics-%02d-%s", pair, variant)
			var err error
			r.active, err = costManifest(r.original, image, "200m")
			if err != nil {
				return err
			}
			if err := r.write(prefix+"-manifest.json", r.active); err != nil {
				return err
			}
			if err := r.agent("on", prefix); err != nil {
				return err
			}
			var debugEndpoint string
			r.debugForward, debugEndpoint, err = r.forward(prefix+"-debug-forward.txt", "kube-system", "daemonset/kno-agent", 18090)
			if err != nil {
				return err
			}
			measurement, err := r.metricsCase(prefix, image, debugEndpoint)
			if err != nil {
				return err
			}
			if variant == "candidate" {
				result.Candidate = measurement
			} else {
				result.Baseline = measurement
			}
			fmt.Printf("metrics pair %d %s: %d scrapes, p95 %.2fms, CPU %.3fs\n", pair, variant, len(measurement.Reads), measurement.P95Seconds*1000, measurement.Cost.AgentCPUSeconds)
		}
		if result.Baseline.Cost.AgentCPUSeconds <= 0 || result.Baseline.P95Seconds <= 0 {
			return fmt.Errorf("empty baseline")
		}
		result.CPUDeltaPercent = ((result.Candidate.Cost.AgentCPUSeconds/result.Candidate.Cost.WindowSeconds)/(result.Baseline.Cost.AgentCPUSeconds/result.Baseline.Cost.WindowSeconds) - 1) * 100
		result.P95DeltaPercent = (result.Candidate.P95Seconds/result.Baseline.P95Seconds - 1) * 100
		cpuDeltas = append(cpuDeltas, result.CPUDeltaPercent)
		p95Deltas = append(p95Deltas, result.P95DeltaPercent)
		r.summary.Metrics = append(r.summary.Metrics, result)
		if err := r.writeJSON("summary.json", r.summary); err != nil {
			return err
		}
	}
	cpu, p95 := pairedEffect(cpuDeltas), pairedEffect(p95Deltas)
	r.summary.MetricsCPUEffect, r.summary.MetricsP95Effect = &cpu, &p95
	return nil
}

func (r *runner) metricsCase(prefix, image, debugEndpoint string) (MetricsCase, error) {
	result := MetricsCase{Image: image, Prefix: prefix}
	uid, err := r.podState(prefix + "-before")
	if err != nil {
		return result, err
	}
	ctx, cancel := context.WithCancel(r.ctx)
	defer cancel()
	type completion struct {
		work Workload
		err  error
	}
	finished := make(chan completion, 1)
	joined := false
	go func() {
		w, e := r.workload(ctx, "churn", r.config.Duration+r.config.Warmup+10*time.Second, 0, prefix+"-workload.json")
		finished <- completion{w, e}
	}()
	defer func() {
		cancel()
		if !joined {
			<-finished
		}
	}()
	started := false
	for i := 0; i < 50; i++ {
		var status struct {
			Running bool `json:"running"`
		}
		if err := r.get(ctx, r.workEndpoint+"/status", prefix+"-start.json", &status); err != nil {
			return result, err
		}
		if status.Running {
			started = true
			break
		}
		if err := pause(ctx, 100*time.Millisecond); err != nil {
			return result, err
		}
	}
	if !started {
		return result, fmt.Errorf("metrics workload did not start")
	}
	if err := pause(ctx, r.config.Warmup); err != nil {
		return result, err
	}
	var before, after telemetry.Snapshot
	if err := r.get(ctx, debugEndpoint+"/diagnostics", prefix+"-diagnostics-before.json", &before); err != nil {
		return result, err
	}
	if before.Error != "" {
		return result, fmt.Errorf("self cgroup: %s", before.Error)
	}
	deadline := time.Now().Add(r.config.Duration)
	var latencies []float64
	for time.Now().Before(deadline) {
		at := time.Now()
		name := fmt.Sprintf("%s-scrape-%03d.txt", prefix, len(result.Reads)+1)
		if err := r.get(ctx, r.agentEndpoint+"/metrics", name, nil); err != nil {
			return result, err
		}
		elapsed := time.Since(at).Seconds()
		body, err := os.ReadFile(filepath.Join(r.dir, name))
		if err != nil {
			return result, err
		}
		sample, err := parseMetrics(body)
		if err != nil {
			return result, err
		}
		if image == r.config.CandidateImage && sample.CacheSources != 3 {
			return result, fmt.Errorf("candidate must expose 3 map cache sources")
		}
		sample.At, sample.ClientSeconds = at.UTC(), elapsed
		result.Reads = append(result.Reads, sample)
		latencies = append(latencies, elapsed)
		if err := pause(ctx, time.Until(at.Add(time.Second))); err != nil {
			return result, err
		}
	}
	if err := r.get(ctx, debugEndpoint+"/diagnostics", prefix+"-diagnostics-after.json", &after); err != nil {
		return result, err
	}
	var status struct {
		Running bool `json:"running"`
	}
	if err := r.get(ctx, r.workEndpoint+"/status", prefix+"-end.json", &status); err != nil {
		return result, err
	}
	if !status.Running {
		return result, fmt.Errorf("load ended before metrics window")
	}
	completed := <-finished
	joined = true
	if completed.err != nil {
		return result, completed.err
	}
	result.Workload = completed.work
	afterUID, err := r.podState(prefix + "-after")
	if err != nil {
		return result, err
	}
	if !bytes.Equal(uid, afterUID) {
		return result, fmt.Errorf("Agent replaced during metrics window")
	}
	result.Cost, err = summarizeCost(before, after)
	if err != nil {
		return result, err
	}
	if len(latencies) < 2 {
		return result, fmt.Errorf("insufficient scrape samples")
	}
	sort.Float64s(latencies)
	result.P50Seconds = quantile(latencies, 0.5)
	result.P95Seconds = quantile(latencies, 0.95)
	result.MaxSeconds = latencies[len(latencies)-1]
	return result, r.writeJSON(prefix+".json", result)
}

// Nearest-rank quantile; input is sorted and non-empty.
func quantile(values []float64, q float64) float64 {
	return values[int(math.Ceil(float64(len(values))*q))-1]
}

func parseMetrics(body []byte) (MetricsRead, error) {
	var result MetricsRead
	found := false
	for _, line := range strings.Split(string(body), "\n") {
		if strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		name := fields[0]
		if name != "kno_agent_metrics_collection_errors" && name != "kno_agent_events_lost_total" && !strings.HasPrefix(name, "kno_agent_bpf_map_stats_") {
			continue
		}
		value, err := strconv.ParseFloat(fields[1], 64)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
			return result, fmt.Errorf("invalid metric %s", line)
		}
		switch {
		case name == "kno_agent_metrics_collection_errors":
			result.Errors = value
			found = true
		case name == "kno_agent_events_lost_total":
			result.EventsLost = value
		case strings.HasPrefix(name, "kno_agent_bpf_map_stats_ready{"):
			result.CacheSources++
			if value == 1 {
				result.ReadySources++
			}
		case strings.HasPrefix(name, "kno_agent_bpf_map_stats_age_seconds{"):
			if value > result.MaxAgeSeconds {
				result.MaxAgeSeconds = value
			}
		case strings.HasPrefix(name, "kno_agent_bpf_map_stats_refresh_errors_total{"):
			result.RefreshErrors += value
		}
	}
	if !found {
		return result, fmt.Errorf("metrics collection error gauge missing")
	}
	return result, nil
}
