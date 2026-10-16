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
	ID               string    `json:"id"`
	SandboxID        string    `json:"sandboxId"`
	Name             string    `json:"name"`
	Image            string    `json:"image"`
	State            string    `json:"state"`
	Namespace        string    `json:"namespace"`
	PodName          string    `json:"podName"`
	PodUID           string    `json:"podUid"`
	KubernetesPodUID string    `json:"kubernetesPodUid,omitempty"`
	StaticPod        bool      `json:"staticPod,omitempty"`
	PodPhase         string    `json:"podPhase,omitempty"`
	QoSClass         string    `json:"qosClass,omitempty"`
	PID              int       `json:"pid"`
	CgroupPath       string    `json:"cgroupPath,omitempty"`
	CgroupID         uint64    `json:"cgroupId,omitempty"`
	Processes        []Process `json:"processes"`
	Issues           []string  `json:"issues,omitempty"`
}

type Snapshot struct {
	GeneratedAt time.Time   `json:"generatedAt"`
	NodeName    string      `json:"nodeName"`
	NodeUID     string      `json:"nodeUid,omitempty"`
	Containers  []Container `json:"containers"`
}

type Response struct {
	Snapshot *Snapshot `json:"snapshot,omitempty"`
	Error    string    `json:"error,omitempty"`
}
