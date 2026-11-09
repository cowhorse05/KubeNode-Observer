// kno-clockcheck is an explicit, short-lived diagnostic. No HTTP, no attachment.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/cilium/ebpf/rlimit"
	"github.com/cowhorse05/KubeNode-Observer/internal/clockdomain"
	"golang.org/x/sys/unix"
)

type sample struct {
	BeforeNS    int64 `json:"beforeNs,string"`
	KernelNS    int64 `json:"kernelNs,string"`
	AfterNS     int64 `json:"afterNs,string"`
	RawBeforeNS int64 `json:"rawBeforeNs,string"`
	MonoNS      int64 `json:"monoNs,string"`
	RawAfterNS  int64 `json:"rawAfterNs,string"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	n := flag.Int("samples", 10, "samples (2..100)")
	interval := flag.Duration("interval", 100*time.Millisecond, "spacing (10ms..1s)")
	flag.Parse()
	if *n < 2 || *n > 100 || *interval < 10*time.Millisecond || *interval > time.Second {
		return fmt.Errorf("invalid sample count/spacing")
	}
	identity, err := (clockdomain.LinuxClock{}).Identity()
	if err != nil {
		return err
	}
	if err = rlimit.RemoveMemlock(); err != nil {
		return err
	}
	k, err := clockdomain.NewKernelClock()
	if err != nil {
		return err
	}
	defer k.Close()
	readFile := func(path string) string {
		data, err := os.ReadFile(path)
		if err != nil {
			return "unavailable: " + err.Error()
		}
		return strings.TrimSpace(string(data))
	}
	adjtimex := func() map[string]interface{} {
		var tx unix.Timex // Modes=0: QUERY ONLY, never ADJ_*.
		state, err := unix.Adjtimex(&tx)
		if err != nil {
			return map[string]interface{}{"error": err.Error()}
		}
		return map[string]interface{}{"state": state, "frequencyScaledPpm": tx.Freq, "tickUs": tx.Tick, "status": tx.Status, "offset": tx.Offset}
	}
	result := map[string]interface{}{"identity": identity, "clocksource": readFile("/sys/devices/system/clocksource/clocksource0/current_clocksource"), "availableClocksources": readFile("/sys/devices/system/clocksource/clocksource0/available_clocksource"), "timensOffsets": readFile("/proc/self/timens_offsets"), "kernelRelease": readFile("/proc/sys/kernel/osrelease"), "kernelCmdline": readFile("/proc/cmdline"), "adjtimexBefore": adjtimex()}
	samples := make([]sample, 0, *n)
	for i := 0; i < *n; i++ {
		if i > 0 {
			time.Sleep(*interval)
		}
		var s sample
		if s.BeforeNS, err = clockdomain.Read(unix.CLOCK_MONOTONIC); err != nil {
			return err
		}
		if s.KernelNS, err = k.Now(); err != nil {
			return err
		}
		if s.AfterNS, err = clockdomain.Read(unix.CLOCK_MONOTONIC); err != nil {
			return err
		}
		if s.RawBeforeNS, err = clockdomain.Read(unix.CLOCK_MONOTONIC_RAW); err != nil {
			return err
		}
		if s.MonoNS, err = clockdomain.Read(unix.CLOCK_MONOTONIC); err != nil {
			return err
		}
		if s.RawAfterNS, err = clockdomain.Read(unix.CLOCK_MONOTONIC_RAW); err != nil {
			return err
		}
		if s.AfterNS < s.BeforeNS || s.RawAfterNS < s.RawBeforeNS {
			return fmt.Errorf("clock moved backwards")
		}
		samples = append(samples, s)
	}
	after, err := (clockdomain.LinuxClock{}).Identity()
	if err != nil {
		return err
	}
	if after != identity {
		return fmt.Errorf("clock identity changed")
	}
	result["samples"] = samples
	result["adjtimexAfter"] = adjtimex()
	return json.NewEncoder(os.Stdout).Encode(result)
}
