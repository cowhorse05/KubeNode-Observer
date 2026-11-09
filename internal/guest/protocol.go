// Package guest keeps Guest process measurements separate from Host accounting.
package guest

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/cowhorse05/KubeNode-Observer/internal/runqlat"
)

const Path = "/api/v1/guest/resources"
const NonceHeader = "X-Kno-Request-ID"
const Annotation = "observer.kubenode.io/guest-probe"
const MaxResponseBytes = 2 << 20

type CPU struct {
	RuntimeNS     uint64  `json:"runtimeNs"`
	Percent       float64 `json:"percent"`
	WindowSeconds float64 `json:"windowSeconds"`
}
type Process struct {
	NamespacePID    uint32           `json:"namespacePid"`
	PID             uint32           `json:"pid,omitempty"`
	CgroupID        uint64           `json:"cgroupId,omitempty"`
	StartTimeTicks  uint64           `json:"startTimeTicks"`
	Command         string           `json:"command"`
	CPU             *CPU             `json:"cpu"`
	RunQLat         *runqlat.Latency `json:"runqlat"`
	RunQLatCoverage Coverage         `json:"runqlatCoverage"`
	Partial         bool             `json:"partial"`
	Issues          []string         `json:"issues,omitempty"`
}
type Coverage struct {
	ObservedThreads int `json:"observedThreads"`
	TotalThreads    int `json:"totalThreads"`
}
type Response struct {
	Domain          string    `json:"domain"`
	PodUID          string    `json:"podUid"`
	SandboxID       string    `json:"sandboxId,omitempty"`
	Namespace       string    `json:"namespace,omitempty"`
	PodName         string    `json:"podName,omitempty"`
	KernelRelease   string    `json:"kernelRelease"`
	BootID          string    `json:"bootId"`
	InstanceID      string    `json:"instanceId"`
	RequestID       string    `json:"requestId"`
	StartedAt       time.Time `json:"startedAt"`
	DurationSeconds float64   `json:"durationSeconds"`
	TotalItems      int       `json:"totalItems"`
	Processes       []Process `json:"processes"`
}
type query struct {
	duration      time.Duration
	limit         int
	comm, sandbox string
}

func parse(r *http.Request, relay bool) (query, error) {
	q := query{duration: 5 * time.Second, limit: 20}
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return q, err
	}
	for key, v := range values {
		if len(v) != 1 {
			return q, fmt.Errorf("repeated query parameter: %s", key)
		}
		switch key {
		case "duration":
			q.duration, err = time.ParseDuration(v[0])
			if err != nil || q.duration < 100*time.Millisecond || q.duration > 10*time.Second {
				return q, fmt.Errorf("duration must be 100ms..10s")
			}
		case "limit":
			q.limit, err = strconv.Atoi(v[0])
			if err != nil || q.limit < 1 || q.limit > 100 {
				return q, fmt.Errorf("limit must be 1..100")
			}
		case "comm":
			q.comm = v[0]
			if len(q.comm) > 15 {
				return q, fmt.Errorf("comm must be <=15 bytes")
			}
		case "sandbox":
			if !relay {
				return q, fmt.Errorf("sandbox is assigned only by the Node relay")
			}
			q.sandbox = v[0]
		default:
			return q, fmt.Errorf("unknown query parameter: %s", key)
		}
	}
	if relay && !hexID(q.sandbox, 64) {
		return q, fmt.Errorf("full lowercase hex sandbox ID required")
	}
	return q, nil
}
func hexID(value string, size int) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(value) == size && len(decoded)*2 == size && hex.EncodeToString(decoded) == value
}
func NewID() (string, error) {
	var b [16]byte
	_, err := rand.Read(b[:])
	return hex.EncodeToString(b[:]), err
}
func writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}{code, message})
}
func writeResponse(w http.ResponseWriter, result Response) {
	data, err := json.Marshal(result)
	if err != nil || len(data) > MaxResponseBytes {
		writeError(w, 503, "measurement_unavailable", "Guest response exceeded bound or could not encode")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(data)
}
