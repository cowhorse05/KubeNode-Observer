package guest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"

	"github.com/cowhorse05/KubeNode-Observer/internal/clockdomain"
)

const ClockPath = "/api/v1/guest/clock"
const ClockName = "CLOCK_MONOTONIC"

// ClockSource uses Linux CLOCK_MONOTONIC, not UnixNano or Go's serialized time.
type ClockSource interface {
	Now() (int64, error)
	Identity() (ClockIdentity, error)
}

type ClockIdentity = clockdomain.Identity

type ClockReply struct {
	Domain     string        `json:"domain"`
	Clock      string        `json:"clock"`
	PodUID     string        `json:"podUid"`
	InstanceID string        `json:"instanceId"`
	RequestID  string        `json:"requestId"`
	Identity   ClockIdentity `json:"identity"`
	ReceivedNS int64         `json:"receivedNs,string"`
	SentNS     int64         `json:"sentNs,string"`
}

// NS values are decimal strings on the wire to avoid JavaScript's 2^53 limit.
type ClockSample struct {
	RequestID        string `json:"requestId"`
	HostSendNS       int64  `json:"hostSendNs,string"`
	GuestReceiveNS   int64  `json:"guestReceiveNs,string"`
	GuestSendNS      int64  `json:"guestSendNs,string"`
	HostReceiveNS    int64  `json:"hostReceiveNs,string"`
	OffsetLowerNS    int64  `json:"offsetLowerNs,string"`
	OffsetUpperNS    int64  `json:"offsetUpperNs,string"`
	OffsetMidpointNS int64  `json:"offsetMidpointNs,string"`
}

type Calibration struct {
	Clock           string        `json:"clock"`
	Scope           string        `json:"scope"`
	SandboxID       string        `json:"sandboxId"`
	PodUID          string        `json:"podUid"`
	Host            ClockIdentity `json:"host"`
	Guest           ClockIdentity `json:"guest"`
	ProbeInstanceID string        `json:"probeInstanceId"`
	Samples         []ClockSample `json:"samples"`
	SelectedSample  int           `json:"selectedSample"`
}

// EstimateClock bounds Guest minus Host under a constant-offset assumption
// DURING THIS EXCHANGE ONLY. It assumes neither symmetric paths nor stability
// between exchanges. The midpoint is a convenience, not an exact offset.
func EstimateClock(h1, g2, g3, h4 int64) (ClockSample, error) {
	s := ClockSample{HostSendNS: h1, GuestReceiveNS: g2, GuestSendNS: g3, HostReceiveNS: h4}
	if h1 < 0 || g2 < 0 || g3 < g2 || h4 < h1 {
		return s, fmt.Errorf("negative/backwards clock timestamps")
	}
	// All operands nonnegative and ordered; no int64 overflow, even near MaxInt64.
	width := (h4 - h1) - (g3 - g2)
	if width < 0 {
		return s, fmt.Errorf("Guest processing exceeds Host round trip; incompatible clock sample")
	}
	s.OffsetLowerNS = g3 - h4
	s.OffsetUpperNS = g2 - h1
	s.OffsetMidpointNS = s.OffsetLowerNS + width/2
	return s, nil
}

var namespacePattern = regexp.MustCompile(`^time:\[[0-9]+\]$`)

func validClockIdentity(v ClockIdentity) bool {
	b := v.BootID
	return len(b) == 36 && b[8] == '-' && b[13] == '-' && b[18] == '-' && b[23] == '-' && hexID(b[:8]+b[9:13]+b[14:18]+b[19:23]+b[24:], 32) && (v.TimeNamespace == "unavailable" || namespacePattern.MatchString(v.TimeNamespace))
}

type ClockHandler struct {
	PodUID, InstanceID string
	Source             ClockSource
}

func (h *ClockHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != "GET" {
		w.Header().Set("Allow", "GET")
		writeError(w, 405, "method_not_allowed", "only GET supported")
		return
	}
	if r.URL.RawQuery != "" {
		writeError(w, 400, "invalid_query", "direct clock endpoint accepts no query")
		return
	}
	nonce := r.Header.Get(NonceHeader)
	var err error
	if nonce == "" {
		nonce, err = NewID()
	}
	if err != nil || !hexID(nonce, 32) {
		writeError(w, 400, "invalid_query", "invalid request nonce")
		return
	}
	if h.Source == nil {
		writeError(w, 503, "clock_unavailable", "clock source unavailable")
		return
	}
	g2, err := h.Source.Now()
	if err != nil {
		writeError(w, 503, "clock_unavailable", err.Error())
		return
	}
	identity, err := h.Source.Identity()
	if err != nil || !validClockIdentity(identity) || h.PodUID == "" || !hexID(h.InstanceID, 32) {
		writeError(w, 503, "clock_unavailable", "clock identity unavailable")
		return
	}
	g3, err := h.Source.Now()
	if err != nil || g2 <= 0 || g3 < g2 {
		writeError(w, 503, "clock_unavailable", "clock read failed or moved backwards")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(ClockReply{Domain: "guest", Clock: ClockName, PodUID: h.PodUID, InstanceID: h.InstanceID, RequestID: nonce, Identity: identity, ReceivedNS: g2, SentNS: g3})
}
