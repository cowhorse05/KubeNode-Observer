package host

import (
	"fmt"
	"sort"
	"time"

	"github.com/cowhorse05/KubeNode-Observer/internal/probe"
	"github.com/cowhorse05/KubeNode-Observer/internal/runqlat"
)

func cpuDelta(accounting string, before, after uint64, window time.Duration) (*CPU, error) {
	if window <= 0 || after < before {
		return nil, fmt.Errorf("CPU window invalid or counter reset")
	}
	delta := after - before
	return &CPU{Accounting: accounting, RuntimeNS: delta, Percent: float64(delta) / float64(window.Nanoseconds()) * 100, WindowSeconds: window.Seconds()}, nil
}
func cgroupCPU(id uint64, before, after probe.CPUSnapshot) (*CPU, error) {
	var start, end uint64
	var startOK, endOK bool
	for _, v := range before.Totals {
		if v.CgroupID == id {
			start = v.Runtime
			startOK = true
		}
	}
	for _, v := range after.Totals {
		if v.CgroupID == id {
			end = v.Runtime
			endOK = true
		}
	}
	if !startOK || !endOK {
		return nil, fmt.Errorf("cgroup CPU counter missing at a window endpoint")
	}
	return cpuDelta("cgroup", start, end, after.CapturedAt.Sub(before.CapturedAt))
}
func taskCPU(snapshot probe.CPUSnapshot, pid int, task Task) (probe.CPURuntime, error) {
	var found probe.CPURuntime
	count := 0
	for _, v := range snapshot.Processes {
		if v.NamespaceTGID == uint32(pid) && v.NamespacePID == task.TID && v.CgroupID == task.CgroupID {
			found = v
			count++
		}
	}
	if count != 1 {
		return found, fmt.Errorf("expected one namespace/cgroup CPU alias, found %d", count)
	}
	return found, nil
}
func latencyFor(values []probe.RunQLatHistogram, key probe.CPURuntime) (probe.RunQLatHistogram, bool) {
	for _, value := range values {
		if value.CgroupID == key.CgroupID && value.TGID == key.TGID && value.PID == key.PID {
			return value, true
		}
	}
	return probe.RunQLatHistogram{}, false
}
func latencyDelta(before, after probe.RunQLatHistogram) (uint64, [probe.RunQLatBuckets]uint64, error) {
	var buckets [probe.RunQLatBuckets]uint64
	if after.TotalNS < before.TotalNS || after.Count < before.Count {
		return 0, buckets, fmt.Errorf("runqlat counter reset")
	}
	for i := range buckets {
		if after.Buckets[i] < before.Buckets[i] {
			return 0, buckets, fmt.Errorf("runqlat bucket reset")
		}
		buckets[i] = after.Buckets[i] - before.Buckets[i]
	}
	return after.TotalNS - before.TotalNS, buckets, nil
}
func ioDelta(before, after *IO) (*IO, error) {
	if before == nil || after == nil {
		return nil, fmt.Errorf("IO endpoint unavailable")
	}
	if after.ReadBytes < before.ReadBytes || after.WriteBytes < before.WriteBytes || after.ReadChars < before.ReadChars || after.WriteChars < before.WriteChars {
		return nil, fmt.Errorf("IO counter reset")
	}
	return &IO{after.ReadBytes - before.ReadBytes, after.WriteBytes - before.WriteBytes, after.ReadChars - before.ReadChars, after.WriteChars - before.WriteChars}, nil
}

func measureVMM(row Item, before, after ProcessView, cpu0, cpu1 probe.CPUSnapshot, lat0, lat1 []probe.RunQLatHistogram) Item {
	row.PID = after.Identity.PID
	row.Issues = append(row.Issues, before.Issues...)
	row.Issues = append(row.Issues, after.Issues...)
	if after.RSSBytes != nil {
		row.Memory = &Memory{Bytes: *after.RSSBytes, Source: "proc-status-rss"}
	}
	var err error
	row.IO, err = ioDelta(before.IO, after.IO)
	if err != nil {
		row.Issues = append(row.Issues, err.Error())
	}
	window := cpu1.DetailsCapturedAt.Sub(cpu0.DetailsCapturedAt)
	completeCPU := len(before.Threads) == len(after.Threads) && !before.IncompleteThreads && !after.IncompleteThreads
	var sumRuntime, totalWait uint64
	var totalBuckets [probe.RunQLatBuckets]uint64
	vcpuCount := 0
	completeLatency := !before.IncompleteThreads && !after.IncompleteThreads
	for tid, task := range after.Threads {
		thread := Thread{TID: tid, StartTimeTicks: task.StartTimeTicks, CgroupID: task.CgroupID, Command: task.Command, Role: task.Role, VCPUIndex: task.VCPUIndex}
		old, exists := before.Threads[tid]
		if !exists || old.StartTimeTicks != task.StartTimeTicks || old.CgroupID != task.CgroupID || old.Command != task.Command {
			thread.Issues = append(thread.Issues, "thread new/reused/migrated/renamed during window")
		} else {
			start, e0 := taskCPU(cpu0, before.Identity.PID, old)
			end, e1 := taskCPU(cpu1, after.Identity.PID, task)
			if e0 != nil || e1 != nil {
				thread.Issues = append(thread.Issues, fmt.Sprintf("CPU alias unavailable: start=%v end=%v", e0, e1))
			} else if start.PID != end.PID || start.TGID != end.TGID {
				thread.Issues = append(thread.Issues, "kernel thread identity changed")
			} else {
				thread.KernelTID = end.PID
				thread.KernelTGID = end.TGID
				thread.CPU, err = cpuDelta("vmm-threads", start.Runtime, end.Runtime, window)
				if err != nil {
					thread.Issues = append(thread.Issues, err.Error())
				}
				b, ok0 := latencyFor(lat0, start)
				a, ok1 := latencyFor(lat1, end)
				if !ok0 || !ok1 {
					thread.Issues = append(thread.Issues, "runqlat endpoint missing (no zero inferred)")
				} else {
					ns, buckets, e := latencyDelta(b, a)
					if e != nil {
						thread.Issues = append(thread.Issues, e.Error())
					} else {
						value := runqlat.SummarizeHistogram(ns, buckets)
						thread.RunQLat = &value
						if task.Role == "vcpu" {
							totalWait += ns
							for i := range buckets {
								totalBuckets[i] += buckets[i]
							}
						}
					}
				}
			}
		}
		if thread.CPU == nil {
			completeCPU = false
		} else {
			sumRuntime += thread.CPU.RuntimeNS
		}
		if task.Role == "vcpu" {
			vcpuCount++
			if thread.RunQLat == nil {
				completeLatency = false
			}
		}
		row.Threads = append(row.Threads, thread)
	}
	for tid := range before.Threads {
		if old := before.Threads[tid]; old.Role == "vcpu" {
			current, ok := after.Threads[tid]
			if !ok || current.Role != "vcpu" || current.StartTimeTicks != old.StartTimeTicks || current.CgroupID != old.CgroupID || current.Command != old.Command {
				completeLatency = false
			}
		}
		if _, ok := after.Threads[tid]; !ok {
			row.Issues = append(row.Issues, fmt.Sprintf("thread %d disappeared during window", tid))
			completeCPU = false
		}
	}
	if completeCPU {
		row.CPU, err = cpuDelta("vmm-threads", 0, sumRuntime, window)
		if err != nil {
			row.Issues = append(row.Issues, err.Error())
		}
	} else {
		row.Issues = append(row.Issues, "VMM CPU total unavailable: incomplete stable thread set/counters")
	}
	if vcpuCount == 0 {
		row.Issues = append(row.Issues, "no QEMU/KVM vCPU thread name identified")
	} else if completeLatency {
		value := runqlat.SummarizeHistogram(totalWait, totalBuckets)
		row.VCPURunQLat = &value
	}
	sort.Slice(row.Threads, func(i, j int) bool { return row.Threads[i].TID < row.Threads[j].TID })
	return row
}
