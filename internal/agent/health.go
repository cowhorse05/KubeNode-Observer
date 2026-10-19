package agent

import (
	"encoding/json"
	"net/http"
	"sync/atomic"
	"time"
)

type Health struct {
	startedAt  time.Time
	eventsSeen atomic.Uint64
	eventsLost atomic.Uint64
}

type HealthResponse struct {
	Status     string `json:"status"`
	EBPFLoaded bool   `json:"ebpfLoaded"`
	EventsSeen uint64 `json:"eventsSeen"`
	EventsLost uint64 `json:"eventsLost"`
	Uptime     string `json:"uptime"`
	Component  string `json:"component"`
}

type HealthStats struct {
	EventsSeen uint64
	EventsLost uint64
}

func NewHealth() *Health {
	return &Health{startedAt: time.Now()}
}

func (h *Health) RecordEvent() {
	h.eventsSeen.Add(1)
}

func (h *Health) SetEventsLost(value uint64) {
	h.eventsLost.Store(value)
}

func (h *Health) Stats() HealthStats {
	return HealthStats{EventsSeen: h.eventsSeen.Load(), EventsLost: h.eventsLost.Load()}
}

func (h *Health) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(HealthResponse{
			Status:     "ok",
			EBPFLoaded: true,
			EventsSeen: h.eventsSeen.Load(),
			EventsLost: h.eventsLost.Load(),
			Uptime:     time.Since(h.startedAt).Round(time.Second).String(),
			Component:  "kno-agent",
		})
	})
	return mux
}
