package topology

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	runtimeapi "k8s.io/cri-api/pkg/apis/runtime/v1"
)

type RuntimeContainer struct {
	ID             string
	SandboxID      string
	Name           string
	Image          string
	State          string
	PID            int
	RuntimeType    string
	RuntimeHandler string
	Labels         map[string]string
}

type Runtime interface {
	Snapshot(context.Context) (RuntimeSnapshot, error)
}

type RuntimeSnapshot struct {
	Containers []RuntimeContainer
	Sandboxes  []Sandbox
}

type CRIClient struct {
	connection *grpc.ClientConn
	client     runtimeapi.RuntimeServiceClient
}

func NewCRIClient(endpoint string) (*CRIClient, error) {
	connection, err := grpc.NewClient(endpoint, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("create CRI connection: %w", err)
	}
	return &CRIClient{
		connection: connection,
		client:     runtimeapi.NewRuntimeServiceClient(connection),
	}, nil
}

func (c *CRIClient) Close() error {
	return c.connection.Close()
}

func (c *CRIClient) Snapshot(ctx context.Context) (RuntimeSnapshot, error) {
	sandboxes, err := c.client.ListPodSandbox(ctx, &runtimeapi.ListPodSandboxRequest{
		Filter: &runtimeapi.PodSandboxFilter{State: &runtimeapi.PodSandboxStateValue{State: runtimeapi.PodSandboxState_SANDBOX_READY}},
	})
	if err != nil {
		return RuntimeSnapshot{}, fmt.Errorf("list CRI sandboxes: %w", err)
	}
	handlers := make(map[string]string, len(sandboxes.Items))
	for _, sandbox := range sandboxes.Items {
		handlers[sandbox.Id] = sandbox.RuntimeHandler
	}
	response, err := c.client.ListContainers(ctx, &runtimeapi.ListContainersRequest{
		Filter: &runtimeapi.ContainerFilter{State: &runtimeapi.ContainerStateValue{State: runtimeapi.ContainerState_CONTAINER_RUNNING}},
	})
	if err != nil {
		return RuntimeSnapshot{}, fmt.Errorf("list CRI containers: %w", err)
	}

	containers := make([]RuntimeContainer, 0, len(response.Containers))
	runtimeTypes := make(map[string]string)
	for _, listed := range response.Containers {
		statusResponse, err := c.client.ContainerStatus(ctx, &runtimeapi.ContainerStatusRequest{
			ContainerId: listed.Id,
			Verbose:     true,
		})
		if err != nil {
			return RuntimeSnapshot{}, fmt.Errorf("get CRI container %s status: %w", listed.Id, err)
		}
		status := statusResponse.Status
		if status == nil {
			continue
		}
		runtimeTypes[listed.PodSandboxId] = runtimeType(statusResponse.Info)

		containers = append(containers, RuntimeContainer{
			ID:             status.Id,
			SandboxID:      listed.PodSandboxId,
			Name:           status.Metadata.GetName(),
			Image:          status.Image.GetImage(),
			State:          status.State.String(),
			PID:            runtimePID(statusResponse.Info),
			RuntimeType:    runtimeType(statusResponse.Info),
			RuntimeHandler: handlers[listed.PodSandboxId],
			Labels:         status.Labels,
		})
	}

	sort.Slice(containers, func(i, j int) bool {
		left := containers[i].Labels[LabelPodNamespace] + "/" + containers[i].Labels[LabelPodName] + "/" + containers[i].Name
		right := containers[j].Labels[LabelPodNamespace] + "/" + containers[j].Labels[LabelPodName] + "/" + containers[j].Name
		return left < right
	})
	result := RuntimeSnapshot{Containers: containers, Sandboxes: []Sandbox{}}
	// Ready sandboxes remain visible even while all app containers are between
	// restarts. Do not derive VM existence from the running-container list.
	for _, sandbox := range sandboxes.Items {
		if !isKata(runtimeTypes[sandbox.Id], sandbox.RuntimeHandler) {
			continue
		}
		result.Sandboxes = append(result.Sandboxes, Sandbox{ID: sandbox.Id, Namespace: sandbox.Metadata.GetNamespace(), PodName: sandbox.Metadata.GetName(), PodUID: sandbox.Metadata.GetUid(), RuntimeHandler: sandbox.RuntimeHandler})
	}
	return result, nil
}

func runtimeType(info map[string]string) string {
	var payload struct {
		RuntimeType string `json:"runtimeType"`
	}
	_ = json.Unmarshal([]byte(info["info"]), &payload)
	return payload.RuntimeType
}

func runtimePID(info map[string]string) int {
	var payload struct {
		PID int `json:"pid"`
	}
	if err := json.Unmarshal([]byte(info["info"]), &payload); err != nil {
		return 0
	}
	return payload.PID
}
