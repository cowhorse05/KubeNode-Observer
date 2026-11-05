package host

import (
	"testing"
	"time"

	"github.com/cowhorse05/KubeNode-Observer/internal/probe"
	"github.com/cowhorse05/KubeNode-Observer/internal/topology"
)

func fixture() (ProcessView, ProcessView, probe.CPUSnapshot, probe.CPUSnapshot, []probe.RunQLatHistogram, []probe.RunQLatHistogram) {
	index := uint32(0)
	id := topology.HostProcess{PID: 100, StartTimeTicks: 50, CgroupID: 9, Command: "qemu-system-x86_64", CgroupPath: "/vm"}
	view := ProcessView{Identity: id, Threads: map[uint32]Task{100: {TID: 100, StartTimeTicks: 50, CgroupID: 9, Command: "qemu-system-x86", Role: "vmm-main"}, 101: {TID: 101, StartTimeTicks: 51, CgroupID: 10, Command: "CPU 0/KVM", Role: "vcpu", VCPUIndex: &index}}}
	end := view
	end.Threads = map[uint32]Task{100: view.Threads[100], 101: view.Threads[101]}
	mem := uint64(4096)
	end.RSSBytes = &mem
	view.IO = &IO{1, 2, 3, 4}
	end.IO = &IO{6, 7, 8, 9}
	at := time.Unix(10, 0)
	start := probe.CPUSnapshot{CapturedAt: at, DetailsCapturedAt: at, Processes: []probe.CPURuntime{
		{CgroupID: 9, TGID: 800, PID: 800, NamespaceTGID: 100, NamespacePID: 100, Runtime: 100},
		{CgroupID: 10, TGID: 800, PID: 801, NamespaceTGID: 100, NamespacePID: 101, Runtime: 100},
	}}
	finish := probe.CPUSnapshot{CapturedAt: at.Add(time.Second), DetailsCapturedAt: at.Add(time.Second), Processes: []probe.CPURuntime{
		{CgroupID: 9, TGID: 800, PID: 800, NamespaceTGID: 100, NamespacePID: 100, Runtime: 10000100},
		{CgroupID: 10, TGID: 800, PID: 801, NamespaceTGID: 100, NamespacePID: 101, Runtime: 600000100},
		// A naive local-TID/kernel-PID equality join would include this task.
		{CgroupID: 10, TGID: 100, PID: 101, Runtime: 9000000000},
		{CgroupID: 10, TGID: 900, PID: 901, NamespaceTGID: 200, NamespacePID: 101, Runtime: 9000000000},
	}}
	b := probe.RunQLatHistogram{CgroupID: 10, TGID: 800, PID: 801, TotalNS: 1000, Count: 10}
	b.Buckets[5] = 10
	a := b
	a.TotalNS += 96000
	a.Count += 3
	a.Buckets[5] += 3
	return view, end, start, finish, []probe.RunQLatHistogram{b}, []probe.RunQLatHistogram{a}
}

func TestVMMUsesNamespaceAliasAndThreadCgroupNotNumericalPID(t *testing.T) {
	b, a, c0, c1, l0, l1 := fixture()
	row := measureVMM(Item{Threads: []Thread{}}, b, a, c0, c1, l0, l1)
	if row.CPU == nil || row.CPU.RuntimeNS != 610000000 || row.CPU.Percent != 61 {
		t.Fatalf("wrong VMM total: %+v", row.CPU)
	}
	if len(row.Threads) != 2 || row.Threads[1].KernelTID != 801 || row.Threads[1].CPU.Percent != 60 {
		t.Fatalf("wrong namespace join: %+v", row.Threads)
	}
	if row.VCPURunQLat == nil || row.VCPURunQLat.Count != 3 || row.VCPURunQLat.P95US != 63 {
		t.Fatalf("wrong histogram: %+v", row.VCPURunQLat)
	}
	if row.Memory.Source != "proc-status-rss" || row.Memory.Bytes != 4096 || row.IO.ReadBytes != 5 {
		t.Fatalf("wrong procfs counters: %+v", row)
	}
}

func TestVMMDoesNotFabricateMissingResetOrReusedCPU(t *testing.T) {
	for _, scenario := range []string{"missing-alias", "counter-reset", "kernel-reuse", "namespace-collision", "thread-reuse", "new-thread", "deleted-thread", "migration"} {
		t.Run(scenario, func(t *testing.T) {
			b, a, c0, c1, l0, l1 := fixture()
			switch scenario {
			case "missing-alias":
				c1.Processes[1].NamespacePID = 0
			case "counter-reset":
				c1.Processes[1].Runtime = 50
			case "kernel-reuse":
				c1.Processes[1].PID = 999
			case "namespace-collision":
				c1.Processes = append(c1.Processes, c1.Processes[1])
			case "thread-reuse":
				x := a.Threads[101]
				x.StartTimeTicks++
				a.Threads[101] = x
			case "new-thread":
				a.Threads[102] = Task{TID: 102, StartTimeTicks: 52, CgroupID: 9, Role: "other"}
			case "deleted-thread":
				delete(a.Threads, 101)
			case "migration":
				x := a.Threads[101]
				x.CgroupID = 11
				a.Threads[101] = x
			}
			row := measureVMM(Item{}, b, a, c0, c1, l0, l1)
			if row.CPU != nil || len(row.Issues) == 0 {
				t.Fatalf("incomplete CPU presented as complete: %+v", row)
			}
		})
	}
}

func TestLatencyMissingIsNullButObservedZeroIsAValidZero(t *testing.T) {
	b, a, c0, c1, l0, l1 := fixture()
	row := measureVMM(Item{}, b, a, c0, c1, nil, nil)
	if row.VCPURunQLat != nil || row.Threads[1].RunQLat != nil {
		t.Fatal("missing histogram became zero")
	}
	row = measureVMM(Item{}, b, a, c0, c1, l0, l0)
	if row.VCPURunQLat == nil || row.VCPURunQLat.Count != 0 {
		t.Fatal("observed zero lost")
	}
	l1[0].Buckets[5] = 1
	row = measureVMM(Item{}, b, a, c0, c1, l0, l1)
	if row.VCPURunQLat != nil {
		t.Fatal("histogram reset accepted")
	}
}

func TestCgroupAndIOCountersFailClosed(t *testing.T) {
	at := time.Unix(10, 0)
	b := probe.CPUSnapshot{CapturedAt: at, Totals: []probe.CgroupRuntime{{CgroupID: 7, Runtime: 100}}}
	a := probe.CPUSnapshot{CapturedAt: at.Add(time.Second), Totals: []probe.CgroupRuntime{{CgroupID: 7, Runtime: 500000100}}}
	value, err := cgroupCPU(7, b, a)
	if err != nil || value.Percent != 50 {
		t.Fatal(value, err)
	}
	a.Totals[0].Runtime = 0
	if value, err := cgroupCPU(7, b, a); err == nil || value != nil {
		t.Fatal("reset accepted")
	}
	a.Totals = nil
	if _, err := cgroupCPU(7, b, a); err == nil {
		t.Fatal("missing accepted")
	}
	if value, err := ioDelta(&IO{ReadBytes: 2}, &IO{ReadBytes: 1}); value != nil || err == nil {
		t.Fatal("IO reset accepted")
	}
	if value, err := ioDelta(nil, &IO{}); value != nil || err == nil {
		t.Fatal("missing IO accepted")
	}
}

func TestVCPUSetMustRemainCompleteForAggregateLatency(t *testing.T) {
	for _, scenario := range []string{"disappeared", "renamed", "scan-incomplete"} {
		t.Run(scenario, func(t *testing.T) {
			b, a, c0, c1, l0, l1 := fixture()
			// Retain one valid vCPU so a partial set cannot appear complete.
			second := a.Threads[101]
			second.TID = 102
			b.Threads[102], a.Threads[102] = second, second
			alias := c0.Processes[1]
			alias.PID, alias.NamespacePID = 802, 102
			c0.Processes = append(c0.Processes, alias)
			c1.Processes = append(c1.Processes, alias)
			lat := l0[0]
			lat.PID = 802
			l0, l1 = append(l0, lat), append(l1, lat)
			switch scenario {
			case "disappeared":
				delete(a.Threads, 101)
			case "renamed":
				x := a.Threads[101]
				x.Command, x.Role = "renamed", "other"
				a.Threads[101] = x
			case "scan-incomplete":
				a.IncompleteThreads = true
			}
			row := measureVMM(Item{}, b, a, c0, c1, l0, l1)
			if row.VCPURunQLat != nil || row.CPU != nil {
				t.Fatal("incomplete set produced complete aggregate", row)
			}
		})
	}
}
