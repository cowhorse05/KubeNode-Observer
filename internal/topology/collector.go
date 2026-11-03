package topology

import (
	"context"
	"fmt"
	"time"

	"github.com/cowhorse05/KubeNode-Observer/internal/telemetry"
)

type Collector struct {
	Timing   *telemetry.Recorder // Set before Snapshot is called.
	nodeName string
	runtime  Runtime
	proc     *ProcFS
	resolver ResourceResolver
}

func NewCollector(nodeName string, runtime Runtime, proc *ProcFS, resolver ResourceResolver) *Collector {
	return &Collector{nodeName: nodeName, runtime: runtime, proc: proc, resolver: resolver}
}

func (c *Collector) Snapshot(ctx context.Context) (Snapshot, error) {
	started := time.Now()
	runtimeSnapshot, err := c.runtime.Snapshot(ctx)
	c.Timing.Observe(telemetry.TopologyCRI, started)
	if err != nil {
		return Snapshot{}, err
	}
	defer c.Timing.Observe(telemetry.TopologyEnrich, time.Now())
	runtimeContainers := runtimeSnapshot.Containers

	snapshot := Snapshot{
		GeneratedAt: time.Now().UTC(),
		NodeName:    c.nodeName,
		Containers:  make([]Container, 0, len(runtimeContainers)),
		Sandboxes:   []Sandbox{},
	}
	if c.resolver != nil && c.resolver.Ready() {
		if node, ok := c.resolver.Node(c.nodeName); ok {
			snapshot.NodeUID = node.UID
		}
	}
	for _, current := range runtimeContainers {
		container := Container{
			ID:                current.ID,
			SandboxID:         current.SandboxID,
			Name:              current.Name,
			Image:             current.Image,
			State:             current.State,
			RuntimeType:       current.RuntimeType,
			RuntimeHandler:    current.RuntimeHandler,
			ProcessVisibility: "host",
			Namespace:         current.Labels[LabelPodNamespace],
			PodName:           current.Labels[LabelPodName],
			PodUID:            current.Labels[LabelPodUID],
			PID:               current.PID,
			Processes:         []Process{},
		}
		if c.resolver != nil && c.resolver.Ready() {
			if pod, ok := c.resolver.Pod(container.Namespace, container.PodName); !ok {
				container.Issues = append(container.Issues, "pod is missing from the Kubernetes informer cache")
			} else {
				container.KubernetesPodUID = pod.UID
				container.StaticPod = pod.Static
				container.PodPhase = pod.Phase
				container.QoSClass = pod.QoSClass
				if container.PodUID != pod.RuntimeUID {
					container.Issues = append(container.Issues, fmt.Sprintf("pod uid mismatch: CRI=%s Kubernetes runtime identity=%s", container.PodUID, pod.RuntimeUID))
				}
			}
		}
		if isKata(current.RuntimeType, current.RuntimeHandler) {
			// A Kata CRI PID may be a shim or a guest PID. Never dereference it
			// in host procfs or attribute its cgroup to a guest container.
			container.PID = 0
			container.ProcessVisibility = "guest-unavailable"
			snapshot.Containers = append(snapshot.Containers, container)
			continue
		}
		if current.PID == 0 {
			container.Issues = append(container.Issues, "CRI did not return a container pid")
			snapshot.Containers = append(snapshot.Containers, container)
			continue
		}

		path, err := c.proc.CgroupPath(current.PID)
		if err != nil {
			container.Issues = append(container.Issues, fmt.Sprintf("read cgroup path: %v", err))
			snapshot.Containers = append(snapshot.Containers, container)
			continue
		}
		container.CgroupPath = path

		if id, err := c.proc.CgroupID(path); err != nil {
			container.Issues = append(container.Issues, fmt.Sprintf("read cgroup id: %v", err))
		} else {
			container.CgroupID = id
		}
		if processes, err := c.proc.Processes(path); err != nil {
			container.Issues = append(container.Issues, fmt.Sprintf("list processes: %v", err))
		} else {
			container.Processes = processes
		}
		snapshot.Containers = append(snapshot.Containers, container)
	}
	snapshot.Sandboxes = c.proc.KataSandboxes(runtimeSnapshot.Sandboxes)
	return snapshot, nil
}
