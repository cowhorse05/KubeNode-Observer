package topology

import (
	"context"
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	runtimeapi "k8s.io/cri-api/pkg/apis/runtime/v1"
)

type snapshotRuntimeServer struct {
	runtimeapi.UnimplementedRuntimeServiceServer
	t *testing.T
}

func (s *snapshotRuntimeServer) ListPodSandbox(_ context.Context, req *runtimeapi.ListPodSandboxRequest) (*runtimeapi.ListPodSandboxResponse, error) {
	if req.GetFilter().GetState().GetState() != runtimeapi.PodSandboxState_SANDBOX_READY {
		s.t.Error("sandbox request must select Ready")
	}
	return &runtimeapi.ListPodSandboxResponse{Items: []*runtimeapi.PodSandbox{
		{Id: "vm", RuntimeHandler: "kata-qemu", Metadata: &runtimeapi.PodSandboxMetadata{Name: "pod", Namespace: "ns", Uid: "uid"}},
		{Id: "vm-restarting", RuntimeHandler: "kata-qemu", Metadata: &runtimeapi.PodSandboxMetadata{Name: "restarting", Namespace: "ns", Uid: "uid2"}},
		{Id: "runc", RuntimeHandler: "runc", Metadata: &runtimeapi.PodSandboxMetadata{Name: "ordinary"}},
	}}, nil
}
func (s *snapshotRuntimeServer) ListContainers(context.Context, *runtimeapi.ListContainersRequest) (*runtimeapi.ListContainersResponse, error) {
	return &runtimeapi.ListContainersResponse{Containers: []*runtimeapi.Container{{Id: "one", PodSandboxId: "vm"}, {Id: "two", PodSandboxId: "vm"}}}, nil
}
func (s *snapshotRuntimeServer) ContainerStatus(_ context.Context, r *runtimeapi.ContainerStatusRequest) (*runtimeapi.ContainerStatusResponse, error) {
	return &runtimeapi.ContainerStatusResponse{Status: &runtimeapi.ContainerStatus{Id: r.ContainerId, Metadata: &runtimeapi.ContainerMetadata{Name: r.ContainerId}}, Info: map[string]string{"info": `{"pid":123,"runtimeType":"io.containerd.kata.v2"}`}}, nil
}

func TestCRISnapshotIncludesSandboxWithoutRunningAppAndDoesNotDuplicateVM(t *testing.T) {
	listener := bufconn.Listen(1 << 20)
	defer listener.Close()
	server := grpc.NewServer()
	runtimeapi.RegisterRuntimeServiceServer(server, &snapshotRuntimeServer{t: t})
	go func() { _ = server.Serve(listener) }()
	defer server.Stop()
	conn, err := grpc.NewClient("passthrough:///buf", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	c := &CRIClient{client: runtimeapi.NewRuntimeServiceClient(conn)}
	got, err := c.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Containers) != 2 || len(got.Sandboxes) != 2 {
		t.Fatalf("wrong CRI snapshot: %+v", got)
	}
	if got.Sandboxes[1].ID != "vm-restarting" || got.Sandboxes[1].PodUID != "uid2" {
		t.Fatal("Ready VM lost between app restarts")
	}
	for _, c := range got.Containers {
		if c.RuntimeType != "io.containerd.kata.v2" || c.RuntimeHandler != "kata-qemu" {
			t.Fatal(c)
		}
	}
}
