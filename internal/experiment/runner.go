package experiment

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cowhorse05/KubeNode-Observer/internal/cpu"
	"github.com/cowhorse05/KubeNode-Observer/internal/topology"
)

type Config struct {
	Mode       string        `json:"mode"`
	Context    string        `json:"context"`
	Output     string        `json:"output"`
	Image      string        `json:"image"`
	AgentImage string        `json:"costAgentImage,omitempty"`
	Pairs      int           `json:"pairs"`
	Repeats    int           `json:"accuracyRepeats"`
	Intervals  string        `json:"intervals"`
	Duration   time.Duration `json:"durationNs"`
	Warmup     time.Duration `json:"warmupNs"`
	Settle     time.Duration `json:"settleNs"`
}

type Workload struct {
	Mode            string  `json:"mode"`
	DurationSeconds float64 `json:"durationSeconds"`
	Operations      uint64  `json:"operations"`
	Throughput      float64 `json:"throughputOpsPerSecond"`
	P99US           uint64  `json:"p99Us"`
	LatencyOverflow uint64  `json:"latencyOverflow"`
}

type Pair struct {
	Number          int      `json:"number"`
	Order           string   `json:"order"`
	Off             Workload `json:"off"`
	On              Workload `json:"on"`
	ThroughputDelta float64  `json:"throughputDeltaPercent"`
	P99Delta        float64  `json:"p99DeltaPercent"`
}

type AccuracyCase struct {
	Mode                 string  `json:"mode"`
	Interval             string  `json:"interval"`
	Repeat               int     `json:"repeat"`
	File                 string  `json:"rawFile"`
	AbsoluteErrorPercent float64 `json:"absoluteErrorPercent"`
	SignedErrorPercent   float64 `json:"signedErrorPercent"`
	ReferenceSkewPercent float64 `json:"referenceSkewPercent"`
	MissingKeys          int     `json:"missingKeys"`
	ResetKeys            int     `json:"resetKeys"`
}

type Summary struct {
	Status           string         `json:"status"`
	Config           Config         `json:"config"`
	Pairs            []Pair         `json:"pairedRounds,omitempty"`
	ThroughputEffect *Effect        `json:"throughputEffect,omitempty"`
	P99Effect        *Effect        `json:"p99Effect,omitempty"`
	Accuracy         []AccuracyCase `json:"accuracyCases,omitempty"`
	Cost             []CostCase     `json:"costCases,omitempty"`
	Error            string         `json:"error,omitempty"`
	RestoreError     string         `json:"restoreError,omitempty"`
}

type runner struct {
	config        Config
	ctx           context.Context
	dir           string
	original      []byte
	active        []byte
	node          string
	commands      *os.File
	commandMu     sync.Mutex
	agentForward  *exec.Cmd
	debugForward  *exec.Cmd
	workForward   *exec.Cmd
	workEndpoint  string
	agentEndpoint string
	intervals     []time.Duration
	summary       Summary
	podOwned      bool
	locked        bool
}

func Run(ctx context.Context, config Config) (runErr error) {
	intervals, err := validate(config)
	if err != nil {
		return err
	}
	if config.Output == "" {
		config.Output = filepath.Join("log", "experiments", time.Now().UTC().Format("20060102T150405.000000000Z"))
	}
	// Refuse overwrite: interrupted and failed experiments are evidence too.
	if err := os.Mkdir(config.Output, 0o755); err != nil {
		if os.IsNotExist(err) {
			if err = os.MkdirAll(filepath.Dir(config.Output), 0o755); err == nil {
				err = os.Mkdir(config.Output, 0o755)
			}
		}
		if err != nil {
			return err
		}
	}
	commands, err := os.Create(filepath.Join(config.Output, "commands.jsonl"))
	if err != nil {
		return err
	}
	defer commands.Close()
	r := &runner{ctx: ctx, config: config, dir: config.Output, commands: commands, intervals: intervals, summary: Summary{Status: "running", Config: config}}
	fmt.Println("Experiment artifacts:", config.Output)
	defer func() {
		r.stopForward(&r.debugForward)
		r.stopForward(&r.workForward)
		r.stopForward(&r.agentForward)
		if r.original != nil {
			restoreCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			// Preserve the original DaemonSet configuration, not the checked-in YAML.
			_, restoreErr := r.kube(restoreCtx, r.original, "apply", "-f", "-")
			if restoreErr == nil {
				_, restoreErr = r.kube(restoreCtx, nil, "-n", "kube-system", "rollout", "status", "daemonset/kno-agent", "--timeout=90s")
			}
			if restoreErr != nil {
				r.summary.RestoreError = restoreErr.Error()
				runErr = errors.Join(runErr, fmt.Errorf("restore original Agent: %w", restoreErr))
			}
			if r.podOwned {
				_, cleanupErr := r.kube(restoreCtx, nil, "-n", "default", "delete", "pod", "kno-observer-experiment", "--ignore-not-found", "--wait=true", "--timeout=30s")
				if cleanupErr != nil {
					runErr = errors.Join(runErr, cleanupErr)
				}
			}
			state, stateErr := r.kube(restoreCtx, nil, "-n", "kube-system", "get", "ds,pods", "-l", "app.kubernetes.io/name=kno-agent", "-o", "json")
			if stateErr == nil {
				runErr = errors.Join(runErr, r.write("restored-agent.json", state))
			} else {
				runErr = errors.Join(runErr, stateErr)
			}
		}
		if r.locked {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			_, err := r.kube(cleanupCtx, nil, "-n", "kube-system", "delete", "configmap", "kno-experiment-lock")
			cancel()
			runErr = errors.Join(runErr, err)
		}
		if runErr != nil {
			r.summary.Status = "failed"
			r.summary.Error = runErr.Error()
		} else {
			r.summary.Status = "complete"
		}
		if err := r.writeJSON("summary.json", r.summary); err != nil {
			runErr = errors.Join(runErr, err)
		}
	}()
	if err := r.prepare(); err != nil {
		return err
	}
	if config.Mode == "paired" || config.Mode == "all" {
		if err := r.paired(); err != nil {
			return err
		}
	}
	if config.Mode == "accuracy" || config.Mode == "all" {
		if err := r.accuracy(); err != nil {
			return err
		}
	}
	if config.Mode == "cost" {
		return r.cost()
	}
	return nil
}

func validate(c Config) ([]time.Duration, error) {
	if c.Mode != "all" && c.Mode != "paired" && c.Mode != "accuracy" && c.Mode != "cost" {
		return nil, fmt.Errorf("mode must be all, paired, accuracy or cost")
	}
	if c.Mode == "cost" && (c.AgentImage == "" || c.Duration%time.Second != 0) {
		return nil, fmt.Errorf("cost needs an agent image and whole-second duration")
	}
	if c.Context == "" || c.Pairs < 2 || c.Pairs%2 != 0 || c.Repeats < 1 || c.Duration < time.Second || c.Duration > time.Minute || c.Warmup < time.Second || c.Warmup > 20*time.Second || c.Settle < time.Second {
		return nil, fmt.Errorf("need an explicit context, even pairs >=2, repeats >=1, duration 1s..1m, warmup 1s..20s, settle >=1s")
	}
	var intervals []time.Duration
	seen := make(map[time.Duration]bool)
	for _, value := range strings.Split(c.Intervals, ",") {
		interval, err := time.ParseDuration(strings.TrimSpace(value))
		if err != nil || interval < 100*time.Millisecond || interval > c.Duration || seen[interval] {
			return nil, fmt.Errorf("invalid accuracy interval %q", value)
		}
		seen[interval] = true
		intervals = append(intervals, interval)
	}
	return intervals, nil
}

func (r *runner) command(ctx context.Context, input []byte, binary string, args ...string) ([]byte, error) {
	commandCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	command := exec.CommandContext(commandCtx, binary, args...)
	command.Stdin = bytes.NewReader(input)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	started := time.Now().UTC()
	output, err := command.Output()
	r.commandMu.Lock()
	_ = json.NewEncoder(r.commands).Encode(map[string]any{"at": started, "binary": binary, "args": args, "stderr": stderr.String(), "error": fmt.Sprint(err)})
	r.commandMu.Unlock()
	if err != nil {
		return output, fmt.Errorf("%s %v: %w: %s", binary, args, err, stderr.String())
	}
	return output, nil
}

func (r *runner) kube(ctx context.Context, input []byte, args ...string) ([]byte, error) {
	return r.command(ctx, input, "kubectl", append([]string{"--context", r.config.Context}, args...)...)
}

func (r *runner) write(name string, body []byte) error {
	return os.WriteFile(filepath.Join(r.dir, name), body, 0o644)
}
func (r *runner) writeJSON(name string, value any) error {
	body, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return r.write(name, append(body, '\n'))
}

func (r *runner) prepare() error {
	if _, err := r.kube(r.ctx, nil, "-n", "kube-system", "create", "configmap", "kno-experiment-lock", "--from-literal=run="+filepath.Base(r.dir)); err != nil {
		return fmt.Errorf("acquire exclusive experiment lock: %w", err)
	}
	r.locked = true
	if err := r.archiveSource(); err != nil {
		return err
	}
	checks := []struct {
		file, binary string
		args         []string
	}{
		{"git-head.txt", "git", []string{"rev-parse", "HEAD"}},
		{"git-status.txt", "git", []string{"status", "--short", "--untracked-files=no"}},
		{"source.patch", "git", []string{"diff", "HEAD", "--", "bpf", "cmd", "internal", "examples", "hack", "Makefile"}},
		{"go-version.txt", "go", []string{"version"}},
	}
	for _, check := range checks {
		output, err := r.command(r.ctx, nil, check.binary, check.args...)
		if err != nil {
			return err
		}
		if err := r.write(check.file, output); err != nil {
			return err
		}
	}
	nodes, err := r.kube(r.ctx, nil, "get", "nodes", "-o", "json")
	if err != nil {
		return err
	}
	if err := r.write("nodes.json", nodes); err != nil {
		return err
	}
	var list struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
		} `json:"items"`
	}
	if err := json.Unmarshal(nodes, &list); err != nil {
		return err
	}
	if len(list.Items) != 1 {
		return fmt.Errorf("experiment requires a single-node test cluster")
	}
	r.node = list.Items[0].Metadata.Name
	version, err := r.kube(r.ctx, nil, "version", "-o", "json")
	if err != nil {
		return err
	}
	if err := r.write("kubernetes-version.json", version); err != nil {
		return err
	}
	original, err := r.kube(r.ctx, nil, "-n", "kube-system", "get", "ds", "kno-agent", "-o", "json")
	if err != nil {
		return fmt.Errorf("install Agent first: %w", err)
	}
	if err := r.write("original-agent.json", original); err != nil {
		return err
	}
	var ds map[string]any
	if err := json.Unmarshal(original, &ds); err != nil {
		return err
	}
	delete(ds, "status")
	metadata := ds["metadata"].(map[string]any)
	for _, key := range []string{"uid", "resourceVersion", "generation", "creationTimestamp", "managedFields"} {
		delete(metadata, key)
	}
	r.original, err = json.Marshal(ds)
	if err != nil {
		return err
	}
	// Dedicated test pod name must be free; never replace a pre-existing user pod.
	existing, err := r.kube(r.ctx, nil, "-n", "default", "get", "pod", "kno-observer-experiment", "--ignore-not-found", "-o", "name")
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(existing)) != 0 {
		r.original = nil
		return fmt.Errorf("test pod already exists; refusing to replace it")
	}
	pod := map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"name": "kno-observer-experiment", "namespace": "default"}, "spec": map[string]any{"nodeName": r.node, "restartPolicy": "Never", "containers": []any{map[string]any{"name": "benchmark", "image": r.config.Image, "imagePullPolicy": "Never", "args": []string{"--listen=:8080", "--work-iterations=64"}, "readinessProbe": map[string]any{"httpGet": map[string]any{"path": "/healthz", "port": 8080}, "periodSeconds": 1}, "resources": map[string]any{"requests": map[string]string{"cpu": "1", "memory": "32Mi"}, "limits": map[string]string{"cpu": "1", "memory": "128Mi"}}}}}}
	manifest, _ := json.Marshal(pod)
	if err := r.write("workload-pod.json", manifest); err != nil {
		return err
	}
	if _, err := r.kube(r.ctx, manifest, "apply", "-f", "-"); err != nil {
		return err
	}
	r.podOwned = true
	if _, err := r.kube(r.ctx, nil, "-n", "default", "wait", "--for=condition=Ready", "pod/kno-observer-experiment", "--timeout=90s"); err != nil {
		return err
	}
	status, err := r.kube(r.ctx, nil, "-n", "default", "get", "pod", "kno-observer-experiment", "-o", "json")
	if err != nil {
		return err
	}
	if err := r.write("workload-status.json", status); err != nil {
		return err
	}
	sum := sha256.Sum256(append(append([]byte(nil), r.original...), manifest...))
	if err := r.write("manifest-sha256.txt", []byte(hex.EncodeToString(sum[:])+"\n")); err != nil {
		return err
	}
	r.workForward, r.workEndpoint, err = r.forward("workload-port-forward.txt", "default", "pod/kno-observer-experiment", 8080)
	return err
}

func (r *runner) forward(logName, namespace, target string, port int) (*exec.Cmd, string, error) {
	logFile, err := os.Create(filepath.Join(r.dir, logName))
	if err != nil {
		return nil, "", err
	}
	command := exec.CommandContext(r.ctx, "kubectl", "--context", r.config.Context, "-n", namespace, "port-forward", "--address=127.0.0.1", target, ":"+strconv.Itoa(port))
	command.Stdout, command.Stderr = logFile, logFile
	if err := command.Start(); err != nil {
		logFile.Close()
		return nil, "", err
	}
	// Port 0 is allocated by kubectl, avoiding fixed-port collisions.
	for i := 0; i < 100; i++ {
		body, _ := os.ReadFile(logFile.Name())
		for _, line := range strings.Split(string(body), "\n") {
			if strings.HasPrefix(line, "Forwarding from 127.0.0.1:") {
				address := strings.Fields(line)[2]
				logFile.Close()
				return command, "http://" + address, nil
			}
		}
		if err := pause(r.ctx, 100*time.Millisecond); err != nil {
			break
		}
	}
	_ = command.Process.Kill()
	_ = command.Wait()
	logFile.Close()
	return nil, "", fmt.Errorf("port-forward did not start: %s", logName)
}

func (r *runner) stopForward(command **exec.Cmd) {
	if *command != nil {
		_ = (*command).Process.Kill()
		_ = (*command).Wait()
		*command = nil
	}
}

func pause(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (r *runner) get(ctx context.Context, endpoint, name string, target any) error {
	started := time.Now().UTC()
	defer func() {
		r.commandMu.Lock()
		_ = json.NewEncoder(r.commands).Encode(map[string]any{"at": started, "endedAt": time.Now().UTC(), "httpGet": endpoint, "artifact": name})
		r.commandMu.Unlock()
	}()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	response, err := (&http.Client{Timeout: 150 * time.Second}).Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 32<<20))
	if err != nil {
		return err
	}
	if err := r.write(name, body); err != nil {
		return err
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %s: %s", response.Status, body)
	}
	if target != nil {
		return json.Unmarshal(body, target)
	}
	return nil
}

func (r *runner) agent(mode, prefix string) error {
	r.stopForward(&r.debugForward)
	r.stopForward(&r.agentForward)
	if _, err := r.kube(r.ctx, nil, "-n", "kube-system", "delete", "ds", "kno-agent", "--ignore-not-found", "--wait=true", "--timeout=60s"); err != nil {
		return err
	}
	deleted := false
	for i := 0; i < 60; i++ {
		body, err := r.kube(r.ctx, nil, "-n", "kube-system", "get", "pods", "-l", "app.kubernetes.io/name=kno-agent", "-o", "name")
		if err != nil {
			return err
		}
		if len(bytes.TrimSpace(body)) == 0 {
			deleted = true
			break
		}
		if err := pause(r.ctx, time.Second); err != nil {
			return err
		}
	}
	if !deleted {
		return fmt.Errorf("Agent pods still present after deletion; refusing off measurement")
	}
	if mode == "off" {
		return nil
	}
	manifest := r.original
	if r.active != nil {
		manifest = r.active
	}
	if _, err := r.kube(r.ctx, manifest, "apply", "-f", "-"); err != nil {
		return err
	}
	if _, err := r.kube(r.ctx, nil, "-n", "kube-system", "rollout", "status", "daemonset/kno-agent", "--timeout=90s"); err != nil {
		return err
	}
	var err error
	r.agentForward, r.agentEndpoint, err = r.forward(prefix+"-agent-forward.txt", "kube-system", "daemonset/kno-agent", 18080)
	if err != nil {
		return err
	}
	if err := pause(r.ctx, r.config.Settle); err != nil {
		return err
	}
	return r.get(r.ctx, r.agentEndpoint+"/healthz", prefix+"-health.json", nil)
}

func (r *runner) podState(prefix string) ([]byte, error) {
	body, err := r.kube(r.ctx, nil, "-n", "kube-system", "get", "pods", "-l", "app.kubernetes.io/name=kno-agent", "-o", "json")
	if err != nil {
		return nil, err
	}
	if err := r.write(prefix+"-agent-state.json", body); err != nil {
		return nil, err
	}
	var state struct {
		Items []struct {
			Metadata struct {
				UID string `json:"uid"`
			} `json:"metadata"`
			Status struct {
				ContainerStatuses []struct {
					RestartCount int  `json:"restartCount"`
					Ready        bool `json:"ready"`
				} `json:"containerStatuses"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &state); err != nil {
		return nil, err
	}
	if len(state.Items) != 1 || len(state.Items[0].Status.ContainerStatuses) != 1 || !state.Items[0].Status.ContainerStatuses[0].Ready || state.Items[0].Status.ContainerStatuses[0].RestartCount != 0 {
		return nil, fmt.Errorf("Agent restarted or became unready")
	}
	return []byte(state.Items[0].Metadata.UID), nil
}

func (r *runner) workload(ctx context.Context, mode string, duration, warmup time.Duration, name string) (Workload, error) {
	var result Workload
	err := r.get(ctx, r.workEndpoint+"/run?mode="+mode+"&duration="+duration.String()+"&warmup="+warmup.String()+"&workers=8", name, &result)
	if err == nil && (result.Operations == 0 || result.Throughput <= 0 || result.P99US == 0) {
		err = fmt.Errorf("empty workload result")
	}
	return result, err
}

func (r *runner) paired() error {
	var throughput, latency []float64
	for pairNumber := 1; pairNumber <= r.config.Pairs; pairNumber++ {
		order := []string{"off", "on"}
		if pairNumber%2 == 0 {
			order = []string{"on", "off"}
		}
		pair := Pair{Number: pairNumber, Order: strings.Join(order, "/")}
		for _, mode := range order {
			prefix := fmt.Sprintf("pair-%02d-%s", pairNumber, mode)
			if err := r.agent(mode, prefix); err != nil {
				return err
			}
			var uid []byte
			if mode == "on" {
				var err error
				uid, err = r.podState(prefix + "-before")
				if err != nil {
					return err
				}
				if err := r.get(r.ctx, r.agentEndpoint+"/metrics", prefix+"-metrics-before.txt", nil); err != nil {
					return err
				}
			}
			result, err := r.workload(r.ctx, "steady", r.config.Duration, r.config.Warmup, prefix+"-workload.json")
			if err != nil {
				return err
			}
			if mode == "on" {
				if err := r.get(r.ctx, r.agentEndpoint+"/metrics", prefix+"-metrics-after.txt", nil); err != nil {
					return err
				}
				after, err := r.podState(prefix + "-after")
				if err != nil {
					return err
				}
				if !bytes.Equal(uid, after) {
					return fmt.Errorf("Agent UID changed during case")
				}
				pair.On = result
			} else {
				pair.Off = result
			}
			fmt.Printf("pair %d %s: %.2f ops/s, p99 %d us\n", pairNumber, mode, result.Throughput, result.P99US)
		}
		pair.ThroughputDelta = (pair.On.Throughput/pair.Off.Throughput - 1) * 100
		pair.P99Delta = (float64(pair.On.P99US)/float64(pair.Off.P99US) - 1) * 100
		throughput = append(throughput, pair.ThroughputDelta)
		latency = append(latency, pair.P99Delta)
		r.summary.Pairs = append(r.summary.Pairs, pair)
		if err := r.writeJSON("summary.json", r.summary); err != nil {
			return err
		}
	}
	t, p := pairedEffect(throughput), pairedEffect(latency)
	r.summary.ThroughputEffect, r.summary.P99Effect = &t, &p
	fmt.Printf("paired median throughput delta %.2f%% (exploratory 95%% bootstrap [%.2f, %.2f])\n", t.MedianPercent, t.BootstrapLowerPercent, t.BootstrapUpperPercent)
	return nil
}

func (r *runner) accuracy() error {
	if err := r.agent("on", "accuracy"); err != nil {
		return err
	}
	var topologyResponse topology.Response
	for i := 0; i < 20; i++ {
		if err := r.get(r.ctx, r.agentEndpoint+"/api/v1/topology?namespace=default&pod=kno-observer-experiment", "accuracy-topology.json", &topologyResponse); err != nil {
			return err
		}
		if topologyResponse.Snapshot != nil && len(topologyResponse.Snapshot.Containers) == 1 {
			break
		}
		if err := pause(r.ctx, time.Second); err != nil {
			return err
		}
	}
	if topologyResponse.Snapshot == nil || len(topologyResponse.Snapshot.Containers) != 1 {
		return fmt.Errorf("workload container topology not ready")
	}
	id := topologyResponse.Snapshot.Containers[0].CgroupID
	for repeat := 1; repeat <= r.config.Repeats; repeat++ {
		modes := []string{"steady", "churn"}
		intervals := append([]time.Duration(nil), r.intervals...)
		if repeat%2 == 0 {
			modes[0], modes[1] = modes[1], modes[0]
			for i, j := 0, len(intervals)-1; i < j; i, j = i+1, j-1 {
				intervals[i], intervals[j] = intervals[j], intervals[i]
			}
		}
		for _, mode := range modes {
			for _, interval := range intervals {
				prefix := fmt.Sprintf("accuracy-%s-%s-%02d", mode, interval, repeat)
				beforeUID, err := r.podState(prefix + "-before")
				if err != nil {
					return err
				}
				if err := r.get(r.ctx, r.agentEndpoint+"/metrics", prefix+"-metrics-before.txt", nil); err != nil {
					return err
				}
				workCtx, cancel := context.WithCancel(r.ctx)
				finished := make(chan error, 1)
				go func() {
					_, err := r.workload(workCtx, mode, r.config.Duration+r.config.Warmup+4*time.Second, 0, prefix+"-workload.json")
					finished <- err
				}()
				started := false
				for attempt := 0; attempt < 50; attempt++ {
					var status struct {
						Running bool `json:"running"`
					}
					if err := r.get(r.ctx, r.workEndpoint+"/status", prefix+"-start.json", &status); err != nil {
						cancel()
						<-finished
						return err
					}
					if status.Running {
						started = true
						break
					}
					if err := pause(r.ctx, 100*time.Millisecond); err != nil {
						cancel()
						<-finished
						return err
					}
				}
				if !started {
					cancel()
					<-finished
					return fmt.Errorf("accuracy workload did not start")
				}
				if err := pause(r.ctx, r.config.Warmup+time.Second); err != nil {
					cancel()
					<-finished
					return err
				}
				var measurement cpu.AccuracyResponse
				err = r.get(r.ctx, r.agentEndpoint+"/api/v1/cpu/accuracy?cgroupId="+strconv.FormatUint(id, 10)+"&duration="+r.config.Duration.String()+"&interval="+interval.String(), prefix+".json", &measurement)
				if err != nil {
					cancel()
					<-finished
					return err
				}
				var status struct {
					Running bool `json:"running"`
				}
				if err := r.get(r.ctx, r.workEndpoint+"/status", prefix+"-end.json", &status); err != nil {
					cancel()
					<-finished
					return err
				}
				if !status.Running {
					cancel()
					<-finished
					return fmt.Errorf("workload ended before accuracy window completed")
				}
				workErr := <-finished
				cancel()
				if workErr != nil {
					return workErr
				}
				if err := r.get(r.ctx, r.agentEndpoint+"/metrics", prefix+"-metrics-after.txt", nil); err != nil {
					return err
				}
				afterUID, err := r.podState(prefix + "-after")
				if err != nil {
					return err
				}
				if !bytes.Equal(beforeUID, afterUID) {
					return fmt.Errorf("Agent UID changed during accuracy window")
				}
				item := AccuracyCase{Mode: mode, Interval: interval.String(), Repeat: repeat, File: prefix + ".json", AbsoluteErrorPercent: measurement.AbsoluteErrorPercent, SignedErrorPercent: measurement.SignedErrorPercent, ReferenceSkewPercent: measurement.ReferenceSkewPercent}
				for _, sample := range measurement.Samples {
					item.MissingKeys += sample.MissingKeys
					item.ResetKeys += sample.ResetKeys
				}
				r.summary.Accuracy = append(r.summary.Accuracy, item)
				if err := r.writeJSON("summary.json", r.summary); err != nil {
					return err
				}
				fmt.Printf("accuracy %s %s repeat %d: error %+.2f%%, reference bracket %.2f%%, missing keys %d\n", mode, interval, repeat, item.SignedErrorPercent, item.ReferenceSkewPercent, item.MissingKeys)
			}
		}
	}
	return nil
}
