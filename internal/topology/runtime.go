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
	ID        string
	SandboxID string
	Name      string
	Image     string
	State     string
	PID       int
	Labels    map[string]string
}

type Runtime interface {
	Containers(context.Context) ([]RuntimeContainer, error)
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

func (c *CRIClient) Containers(ctx context.Context) ([]RuntimeContainer, error) {
	response, err := c.client.ListContainers(ctx, &runtimeapi.ListContainersRequest{
		Filter: &runtimeapi.ContainerFilter{State: &runtimeapi.ContainerStateValue{State: runtimeapi.ContainerState_CONTAINER_RUNNING}},
	})
	if err != nil {
		return nil, fmt.Errorf("list CRI containers: %w", err)
	}

	containers := make([]RuntimeContainer, 0, len(response.Containers))
	for _, listed := range response.Containers {
		statusResponse, err := c.client.ContainerStatus(ctx, &runtimeapi.ContainerStatusRequest{
			ContainerId: listed.Id,
			Verbose:     true,
		})
		if err != nil {
			return nil, fmt.Errorf("get CRI container %s status: %w", listed.Id, err)
		}
		status := statusResponse.Status
		if status == nil {
			continue
		}

		containers = append(containers, RuntimeContainer{
			ID:        status.Id,
			SandboxID: listed.PodSandboxId,
			Name:      status.Metadata.GetName(),
			Image:     status.Image.GetImage(),
			State:     status.State.String(),
			PID:       runtimePID(statusResponse.Info),
			Labels:    status.Labels,
		})
	}

	sort.Slice(containers, func(i, j int) bool {
		left := containers[i].Labels[LabelPodNamespace] + "/" + containers[i].Labels[LabelPodName] + "/" + containers[i].Name
		right := containers[j].Labels[LabelPodNamespace] + "/" + containers[j].Labels[LabelPodName] + "/" + containers[j].Name
		return left < right
	})
	return containers, nil
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
