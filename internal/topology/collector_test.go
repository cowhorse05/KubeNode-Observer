package topology

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

type fakeRuntime struct {
	containers []RuntimeContainer
	sandboxes  []Sandbox
	err        error
}

func (f fakeRuntime) Snapshot(context.Context) (RuntimeSnapshot, error) {
	return RuntimeSnapshot{Containers: f.containers, Sandboxes: f.sandboxes}, f.err
}

type fakeResolver struct{}

func (fakeResolver) Ready() bool { return true }
func (fakeResolver) Pod(namespace, name string) (PodInfo, bool) {
	if namespace == "default" && name == "demo-pod" {
		return PodInfo{UID: "pod-uid", RuntimeUID: "pod-uid", Phase: "Running", QoSClass: "Burstable"}, true
	}
	return PodInfo{}, false
}
func (fakeResolver) Node(name string) (NodeInfo, bool) {
	return NodeInfo{UID: "node-uid"}, name == "node-a"
}

func TestCollectorBuildsIdentity(t *testing.T) {
	root := t.TempDir()
	procRoot := filepath.Join(root, "proc")
	cgroupRoot := filepath.Join(root, "cgroup")
	cgroupPath := "/kubepods.slice/pod.scope/container.scope"
	if err := os.MkdirAll(filepath.Join(procRoot, "123"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(cgroupRoot, cgroupPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(procRoot, "123", "cgroup"), []byte("0::"+cgroupPath+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(procRoot, "123", "comm"), []byte("demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	runtime := fakeRuntime{containers: []RuntimeContainer{{
		ID: "container-id", SandboxID: "sandbox-id", Name: "demo", Image: "demo:dev",
		State: "CONTAINER_RUNNING", PID: 123,
		Labels: map[string]string{LabelPodNamespace: "default", LabelPodName: "demo-pod", LabelPodUID: "pod-uid"},
	}}}
	collector := NewCollector("node-a", runtime, NewProcFS(procRoot, cgroupRoot), fakeResolver{})
	snapshot, err := collector.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Containers) != 1 {
		t.Fatalf("got %d containers", len(snapshot.Containers))
	}
	container := snapshot.Containers[0]
	if container.PodName != "demo-pod" || container.PodPhase != "Running" || container.CgroupPath != cgroupPath || container.CgroupID == 0 {
		t.Fatalf("unexpected container: %#v", container)
	}
	if snapshot.NodeUID != "node-uid" {
		t.Fatalf("node uid = %q", snapshot.NodeUID)
	}
	if len(container.Processes) != 1 || container.Processes[0].Command != "demo" {
		t.Fatalf("unexpected processes: %#v", container.Processes)
	}
}
