package topology

import "time"

const (
	LabelPodName      = "io.kubernetes.pod.name"
	LabelPodNamespace = "io.kubernetes.pod.namespace"
	LabelPodUID       = "io.kubernetes.pod.uid"
)

type Process struct {
	PID     int    `json:"pid"`
	Command string `json:"command"`
}

type Container struct {
	ID                string    `json:"id"`
	SandboxID         string    `json:"sandboxId"`
	Name              string    `json:"name"`
	Image             string    `json:"image"`
	State             string    `json:"state"`
	RuntimeType       string    `json:"runtimeType,omitempty"`
	RuntimeHandler    string    `json:"runtimeHandler,omitempty"`
	ProcessVisibility string    `json:"processVisibility"`
	Namespace         string    `json:"namespace"`
	PodName           string    `json:"podName"`
	PodUID            string    `json:"podUid"`
	KubernetesPodUID  string    `json:"kubernetesPodUid,omitempty"`
	StaticPod         bool      `json:"staticPod,omitempty"`
	PodPhase          string    `json:"podPhase,omitempty"`
	QoSClass          string    `json:"qosClass,omitempty"`
	PID               int       `json:"pid"`
	CgroupPath        string    `json:"cgroupPath,omitempty"`
	CgroupID          uint64    `json:"cgroupId,omitempty"`
	Processes         []Process `json:"processes"`
	Issues            []string  `json:"issues,omitempty"`
}

type Snapshot struct {
	GeneratedAt time.Time   `json:"generatedAt"`
	NodeName    string      `json:"nodeName"`
	NodeUID     string      `json:"nodeUid,omitempty"`
	Containers  []Container `json:"containers"`
	Sandboxes   []Sandbox   `json:"sandboxes"`
}

// HostProcess is a host PID identity, never a CRI/guest PID. StartTimeTicks
// disambiguates PID reuse between topology snapshots (procfs stat field 22).
type HostProcess struct {
	PID            int    `json:"pid"`
	StartTimeTicks uint64 `json:"startTimeTicks"`
	Command        string `json:"command"`
	CgroupPath     string `json:"cgroupPath"`
	CgroupID       uint64 `json:"cgroupId"`
}

// Sandbox owns the VM once per Pod, not once per container. It is deliberately
// not inserted into Container's cgroup index: a VMM is not a guest container.
type Sandbox struct {
	ID             string       `json:"id"`
	Namespace      string       `json:"namespace"`
	PodName        string       `json:"podName"`
	PodUID         string       `json:"podUid"`
	RuntimeHandler string       `json:"runtimeHandler"`
	Shim           *HostProcess `json:"shim,omitempty"`
	Hypervisor     *HostProcess `json:"hypervisor,omitempty"`
	Issues         []string     `json:"issues,omitempty"`
}

type Response struct {
	Snapshot *Snapshot `json:"snapshot,omitempty"`
	Error    string    `json:"error,omitempty"`
}
