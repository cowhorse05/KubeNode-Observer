package guest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cowhorse05/KubeNode-Observer/internal/topology"
)

type TopologySource interface {
	CurrentSnapshot() (*topology.Snapshot, error)
}
type Relay struct {
	topology TopologySource
	resolver topology.ResourceResolver
	port     int
	client   *http.Client
	mu       sync.Mutex
	clock    ClockSource
}
type target struct {
	sandbox topology.Sandbox
	ip      string
}

func NewRelay(t TopologySource, resolver topology.ResourceResolver, port int) (*Relay, error) {
	if port < 0 || port > 65535 {
		return nil, fmt.Errorf("guest-probe-port must be 0 (disabled) or 1..65535")
	}
	transport := &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 2 * time.Second}).DialContext, ResponseHeaderTimeout: 15 * time.Second, MaxConnsPerHost: 1, DisableKeepAlives: true}
	return &Relay{topology: t, resolver: resolver, port: port, clock: LinuxClock{}, client: &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (h *Relay) resolve(id string) (target, int, error) {
	if h.port == 0 || h.resolver == nil || !h.resolver.Ready() {
		return target{}, 503, fmt.Errorf("Guest relay disabled or Kubernetes informer unavailable")
	}
	snapshot, err := h.topology.CurrentSnapshot()
	if err != nil {
		return target{}, 503, err
	}
	for _, s := range snapshot.Sandboxes {
		if s.ID != id {
			continue
		}
		if s.Hypervisor == nil || s.Hypervisor.PID <= 0 || s.Hypervisor.StartTimeTicks == 0 || s.Hypervisor.CgroupID == 0 || len(s.Issues) > 0 {
			return target{}, 503, fmt.Errorf("verified VMM identity unavailable")
		}
		pod, ok := h.resolver.Pod(s.Namespace, s.PodName)
		if !ok || pod.Static || pod.UID != s.PodUID || pod.RuntimeUID != s.PodUID || pod.Phase != "Running" {
			return target{}, 409, fmt.Errorf("Kubernetes/CRI Pod identity does not match")
		}
		ip := net.ParseIP(pod.IP)
		if !pod.GuestProbe || pod.HostNetwork || ip == nil || !ip.IsGlobalUnicast() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
			return target{}, 503, fmt.Errorf("Pod is not opted in or has no eligible literal Pod IP")
		}
		return target{sandbox: s, ip: pod.IP}, 200, nil
	}
	return target{}, 404, fmt.Errorf("current Kata Sandbox not found")
}
func sameTarget(a, b target) bool {
	return a.ip == b.ip && a.sandbox.ID == b.sandbox.ID && a.sandbox.PodUID == b.sandbox.PodUID && a.sandbox.Hypervisor != nil && b.sandbox.Hypervisor != nil && *a.sandbox.Hypervisor == *b.sandbox.Hypervisor
}

func (h *Relay) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		w.Header().Set("Allow", "GET")
		writeError(w, 405, "method_not_allowed", "only GET supported")
		return
	}
	q, err := parse(r, true)
	if err != nil {
		writeError(w, 400, "invalid_query", err.Error())
		return
	}
	if !h.mu.TryLock() {
		w.Header().Set("Retry-After", "1")
		writeError(w, 429, "measurement_busy", "another Guest relay window is active")
		return
	}
	defer h.mu.Unlock()
	t, status, err := h.resolve(q.sandbox)
	if err != nil {
		writeError(w, status, "guest_unavailable", err.Error())
		return
	}
	qv := url.Values{"duration": {q.duration.String()}, "limit": {strconv.Itoa(q.limit)}}
	if q.comm != "" {
		qv.Set("comm", q.comm)
	}
	if q.timing {
		qv.Set("timing", "true")
	}
	ctx, cancel := context.WithTimeout(r.Context(), q.duration+5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, "GET", "http://"+net.JoinHostPort(t.ip, strconv.Itoa(h.port))+Path+"?"+qv.Encode(), nil)
	if err != nil {
		writeError(w, 503, "guest_unavailable", err.Error())
		return
	}
	nonce, err := NewID()
	if err != nil {
		writeError(w, 503, "guest_unavailable", "nonce generation failed")
		return
	}
	request.Header.Set(NonceHeader, nonce)
	response, err := h.client.Do(request)
	if err != nil {
		if r.Context().Err() != nil {
			return
		}
		status = 502
		var timeout net.Error
		if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &timeout) && timeout.Timeout()) {
			status = 504
		}
		writeError(w, status, "guest_upstream_failed", "Guest Probe request failed")
		return
	}
	defer response.Body.Close()
	if response.StatusCode == 429 {
		w.Header().Set("Retry-After", "1")
		writeError(w, 429, "measurement_busy", "Guest Probe is already measuring")
		return
	}
	if response.StatusCode != 200 || !strings.HasPrefix(response.Header.Get("Content-Type"), "application/json") {
		writeError(w, 502, "guest_upstream_failed", "Guest Probe returned unexpected status/content type")
		return
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, MaxResponseBytes+1))
	if err != nil || len(body) > MaxResponseBytes {
		status = 502
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			status = 504
		}
		if r.Context().Err() == nil {
			writeError(w, status, "guest_upstream_failed", "Guest response read failed/exceeded bound")
		}
		return
	}
	var sample Response
	if err = json.Unmarshal(body, &sample); err != nil {
		writeError(w, 502, "guest_upstream_failed", "invalid Guest JSON")
		return
	}
	if sample.Domain != "guest" || sample.PodUID != t.sandbox.PodUID || sample.RequestID != nonce || sample.SandboxID != "" || sample.Namespace != "" || sample.PodName != "" {
		writeError(w, 409, "guest_identity_changed", "Guest response identity/domain/nonce contradicted the request")
		return
	}
	if err = validate(sample, q); err != nil {
		writeError(w, 502, "guest_upstream_failed", err.Error())
		return
	}
	after, _, err := h.resolve(q.sandbox)
	if err != nil || !sameTarget(t, after) {
		writeError(w, 409, "guest_identity_changed", "Pod IP, Sandbox or VMM generation changed during window")
		return
	}
	sample.SandboxID = t.sandbox.ID
	sample.Namespace = t.sandbox.Namespace
	sample.PodName = t.sandbox.PodName
	writeResponse(w, sample)
}
func validate(s Response, q query) error {
	if q.timing != (s.Timing != nil) {
		return fmt.Errorf("Guest timing missing or unsolicited")
	}
	if s.Timing != nil {
		if err := s.Timing.Validate(); err != nil || s.Timing.Identity.BootID != s.BootID {
			return fmt.Errorf("Guest timing invalid or boot identity contradicted")
		}
	}
	if strings.TrimSpace(s.KernelRelease) == "" || len(s.KernelRelease) > 128 || len(s.BootID) != 36 || s.BootID[8] != '-' || s.BootID[13] != '-' || s.BootID[18] != '-' || s.BootID[23] != '-' || !hexID(strings.ReplaceAll(s.BootID, "-", ""), 32) || !hexID(s.InstanceID, 32) || s.StartedAt.IsZero() || s.DurationSeconds < q.duration.Seconds() || math.IsNaN(s.DurationSeconds) || math.IsInf(s.DurationSeconds, 0) || s.DurationSeconds > q.duration.Seconds()+5 || s.TotalItems < len(s.Processes) || s.TotalItems > 256 || s.Processes == nil || len(s.Processes) > q.limit {
		return fmt.Errorf("Guest envelope violates the bounded response contract")
	}
	seen := make(map[uint32]bool)
	for _, p := range s.Processes {
		if p.NamespacePID == 0 || p.StartTimeTicks == 0 || seen[p.NamespacePID] || p.Partial != (len(p.Issues) > 0) || ((p.CPU == nil || p.RunQLat == nil) && !p.Partial) {
			return fmt.Errorf("Guest process identity/partial contract invalid")
		}
		seen[p.NamespacePID] = true
		coverage := p.RunQLatCoverage
		if coverage.TotalThreads < 0 || coverage.TotalThreads > 512 || coverage.ObservedThreads < 0 || coverage.ObservedThreads > coverage.TotalThreads || (p.RunQLat == nil && coverage.ObservedThreads != 0) || (p.RunQLat != nil && (p.CPU == nil || coverage.ObservedThreads == 0)) || (coverage.ObservedThreads < coverage.TotalThreads && !p.Partial) {
			return fmt.Errorf("Guest runqlat coverage contract invalid")
		}
		if p.CPU != nil && (p.PID == 0 || p.CgroupID == 0 || p.CPU.WindowSeconds < q.duration.Seconds() || p.CPU.WindowSeconds > s.DurationSeconds || math.IsInf(p.CPU.Percent, 0) || math.IsNaN(p.CPU.Percent) || p.CPU.Percent < 0 || math.Abs(p.CPU.Percent-float64(p.CPU.RuntimeNS)/1e9/p.CPU.WindowSeconds*100) > 0.001) {
			return fmt.Errorf("Guest CPU contract invalid")
		}
		if p.RunQLat != nil && (len(p.RunQLat.Buckets) > 64 || p.RunQLat.AverageUS < 0 || p.RunQLat.P50US > p.RunQLat.P95US || p.RunQLat.P95US > p.RunQLat.P99US || p.RunQLat.P99US > p.RunQLat.MaxUS) {
			return fmt.Errorf("Guest histogram contract invalid")
		}
		if p.RunQLat != nil {
			var count uint64
			var previous uint64
			for i, b := range p.RunQLat.Buckets {
				if b.LowerUS > b.UpperUS || (i > 0 && b.LowerUS <= previous) || b.Count > ^uint64(0)-count {
					return fmt.Errorf("invalid Guest histogram buckets")
				}
				count += b.Count
				previous = b.UpperUS
			}
			if count != p.RunQLat.Count {
				return fmt.Errorf("Guest histogram count differs from bucket sum")
			}
		}
	}
	return nil
}
