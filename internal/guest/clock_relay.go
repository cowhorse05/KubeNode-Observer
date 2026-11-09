package guest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

func parseCalibration(r *http.Request) (string, int, error) {
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return "", 0, err
	}
	id := values.Get("sandbox")
	n := 7
	if _, ok := values["samples"]; ok {
		n, err = strconv.Atoi(values.Get("samples"))
	}
	if err != nil || n < 3 || n > 15 {
		return "", 0, fmt.Errorf("samples must be 3..15")
	}
	for k, v := range values {
		if (k != "sandbox" && k != "samples") || len(v) != 1 {
			return "", 0, fmt.Errorf("unknown or repeated clock query")
		}
	}
	if !hexID(id, 64) {
		return "", 0, fmt.Errorf("full lowercase hex sandbox ID required")
	}
	return id, n, nil
}

func (h *Relay) CalibrateHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != "GET" {
		w.Header().Set("Allow", "GET")
		writeError(w, 405, "method_not_allowed", "only GET supported")
		return
	}
	id, n, err := parseCalibration(r)
	if err != nil {
		writeError(w, 400, "invalid_query", err.Error())
		return
	}
	if !h.mu.TryLock() {
		w.Header().Set("Retry-After", "1")
		writeError(w, 429, "measurement_busy", "another Guest operation is active")
		return
	}
	defer h.mu.Unlock()
	t, status, err := h.resolve(id)
	if err != nil {
		writeError(w, status, "guest_unavailable", err.Error())
		return
	}
	result, status, err := h.calibrate(r.Context(), t, n)
	if err != nil {
		if r.Context().Err() == nil {
			if status == 429 {
				w.Header().Set("Retry-After", "1")
			}
			writeError(w, status, "clock_unavailable", err.Error())
		}
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

func (h *Relay) calibrate(parent context.Context, t target, n int) (Calibration, int, error) {
	result := Calibration{Clock: ClockName, Scope: "exchange-only", SandboxID: t.sandbox.ID, PodUID: t.sandbox.PodUID, Samples: make([]ClockSample, 0, n)}
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	identity, err := h.clock.Identity()
	if err != nil || !validClockIdentity(identity) {
		return result, 503, fmt.Errorf("Host clock identity unavailable")
	}
	result.Host = identity
	for i := 0; i < n; i++ {
		if i > 0 {
			timer := time.NewTimer(20 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return result, 504, fmt.Errorf("calibration budget exceeded")
			case <-timer.C:
			}
		}
		reply, sample, status, err := h.clockExchange(ctx, t)
		if err != nil {
			return result, status, err
		}
		if i == 0 {
			result.Guest = reply.Identity
			result.ProbeInstanceID = reply.InstanceID
		} else {
			previous := result.Samples[i-1]
			if reply.Identity != result.Guest || reply.InstanceID != result.ProbeInstanceID || sample.HostSendNS < previous.HostReceiveNS || sample.GuestReceiveNS < previous.GuestSendNS {
				return result, 409, fmt.Errorf("clock/Probe generation changed or clock moved backwards")
			}
		}
		after, _, err := h.resolve(t.sandbox.ID)
		if err != nil || !sameTarget(t, after) {
			return result, 409, fmt.Errorf("Pod/Sandbox/VMM generation changed during calibration")
		}
		result.Samples = append(result.Samples, sample)
		best := result.Samples[result.SelectedSample]
		if sample.OffsetUpperNS-sample.OffsetLowerNS < best.OffsetUpperNS-best.OffsetLowerNS {
			result.SelectedSample = i
		}
	}
	after, err := h.clock.Identity()
	if err != nil {
		return result, 503, err
	}
	if after != identity {
		return result, 409, fmt.Errorf("Host clock generation changed")
	}
	return result, 200, nil
}

func (h *Relay) clockExchange(parent context.Context, t target) (ClockReply, ClockSample, int, error) {
	var reply ClockReply
	var sample ClockSample
	ctx, cancel := context.WithTimeout(parent, time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", "http://"+net.JoinHostPort(t.ip, strconv.Itoa(h.port))+ClockPath, nil)
	if err != nil {
		return reply, sample, 503, err
	}
	nonce, err := NewID()
	if err != nil {
		return reply, sample, 503, err
	}
	req.Header.Set(NonceHeader, nonce)
	h1, err := h.clock.Now()
	if err != nil {
		return reply, sample, 503, err
	}
	res, err := h.client.Do(req)
	status := 502
	if err != nil {
		var timeout net.Error
		if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &timeout) && timeout.Timeout()) {
			status = 504
		}
		return reply, sample, status, fmt.Errorf("clock upstream request failed")
	}
	defer res.Body.Close()
	if res.StatusCode == 429 {
		return reply, sample, 429, fmt.Errorf("Guest clock busy")
	}
	media, _, err := mime.ParseMediaType(res.Header.Get("Content-Type"))
	if res.StatusCode != 200 || err != nil || media != "application/json" {
		return reply, sample, 502, fmt.Errorf("clock upstream status/content type invalid")
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, 8193))
	h4, clockErr := h.clock.Now()
	if err != nil || len(body) > 8192 {
		if ctx.Err() != nil {
			status = 504
		}
		return reply, sample, status, fmt.Errorf("clock response read failed/exceeded bound")
	}
	if clockErr != nil {
		return reply, sample, 503, clockErr
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&reply); err != nil {
		return reply, sample, 502, fmt.Errorf("invalid clock response JSON")
	}
	if decoder.Decode(new(interface{})) != io.EOF {
		return reply, sample, 502, fmt.Errorf("trailing clock JSON")
	}
	if reply.ReceivedNS <= 0 || reply.SentNS <= 0 {
		return reply, sample, 502, fmt.Errorf("clock timestamps missing/nonpositive")
	}
	if reply.Domain != "guest" || reply.Clock != ClockName || reply.PodUID != t.sandbox.PodUID || reply.RequestID != nonce || !hexID(reply.InstanceID, 32) || !validClockIdentity(reply.Identity) {
		return reply, sample, 409, fmt.Errorf("clock identity/domain/nonce contradicted request")
	}
	sample, err = EstimateClock(h1, reply.ReceivedNS, reply.SentNS, h4)
	if err != nil {
		return reply, sample, 409, err
	}
	sample.RequestID = nonce
	return reply, sample, 200, nil
}
