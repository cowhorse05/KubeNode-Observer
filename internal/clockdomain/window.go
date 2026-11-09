package clockdomain

import (
	"fmt"
	"regexp"
)

const Name = "CLOCK_MONOTONIC"

type Source interface {
	Now() (int64, error)
	Identity() (Identity, error)
}

type Span struct {
	BeforeNS int64 `json:"beforeNs,string"`
	AfterNS  int64 `json:"afterNs,string"`
}
type Snapshots struct {
	CPU     Span `json:"cpu"`
	RunQLat Span `json:"runqlat"`
}
type Window struct {
	Clock    string    `json:"clock"`
	Identity Identity  `json:"identity"`
	Start    Snapshots `json:"start"`
	End      Snapshots `json:"end"`
}

var bootPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var nsPattern = regexp.MustCompile(`^time:\[[0-9]+\]$`)

func (id Identity) Valid() bool {
	return bootPattern.MatchString(id.BootID) && (id.TimeNamespace == "unavailable" || nsPattern.MatchString(id.TimeNamespace))
}

// Validate checks read order only, not kernel/user offset or measurement quality.
func (w Window) Validate() error {
	if w.Clock != Name || !w.Identity.Valid() {
		return fmt.Errorf("invalid window clock identity")
	}
	previous := int64(0)
	for _, span := range []Span{w.Start.CPU, w.Start.RunQLat, w.End.CPU, w.End.RunQLat} {
		if span.BeforeNS <= 0 || span.BeforeNS < previous || span.AfterNS < span.BeforeNS {
			return fmt.Errorf("missing/backwards/out-of-order snapshot brackets")
		}
		previous = span.AfterNS
	}
	return nil
}

type Boundary int

const (
	CPUStart Boundary = iota
	RunQLatStart
	CPUEnd
	RunQLatEnd
)

// Recorder is request-local; nil preserves the original read path without clocks.
type Recorder struct {
	source Source
	window Window
	next   Boundary
}

func Begin(enabled bool, source Source) (*Recorder, error) {
	if !enabled {
		return nil, nil
	}
	if source == nil {
		source = LinuxClock{}
	}
	id, err := source.Identity()
	if err != nil || !id.Valid() {
		return nil, fmt.Errorf("snapshot clock identity unavailable: %v", err)
	}
	return &Recorder{source: source, window: Window{Clock: Name, Identity: id}}, nil
}

// Capture brackets the actual reader call, not procfs, topology or computation.
// It never retries, resets or adds a map snapshot.
func Capture[T any](r *Recorder, boundary Boundary, read func() (T, error)) (T, error) {
	if r == nil {
		return read()
	}
	var zero T
	if boundary != r.next || boundary > RunQLatEnd {
		return zero, fmt.Errorf("unexpected snapshot order")
	}
	before, err := r.source.Now()
	if err != nil {
		return zero, err
	}
	value, err := read()
	if err != nil {
		return zero, err
	}
	after, err := r.source.Now()
	if err != nil || before <= 0 || after < before {
		return zero, fmt.Errorf("snapshot clock failed/backwards: %v", err)
	}
	spans := []*Span{&r.window.Start.CPU, &r.window.Start.RunQLat, &r.window.End.CPU, &r.window.End.RunQLat}
	*spans[boundary] = Span{BeforeNS: before, AfterNS: after}
	r.next++
	return value, nil
}

func (r *Recorder) Finish() (*Window, error) {
	if r == nil {
		return nil, nil
	}
	id, err := r.source.Identity()
	if err != nil || id != r.window.Identity || r.next != RunQLatEnd+1 {
		return nil, fmt.Errorf("snapshot clock identity changed or incomplete window: %v", err)
	}
	if err := r.window.Validate(); err != nil {
		return nil, err
	}
	result := r.window
	return &result, nil
}
