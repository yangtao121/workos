package transport

import (
	"context"
	"os"
	"os/exec"

	"connectrpc.com/connect"

	workloadv1 "github.com/yangtao121/workos/gen/go/workos/workload/v1"
	"github.com/yangtao121/workos/gen/go/workos/workload/v1/workloadv1connect"
)

type WorkloadHandler struct {
	workloadv1connect.UnimplementedWorkloadServiceHandler
}

func NewWorkloadHandler() *WorkloadHandler { return &WorkloadHandler{} }

func (h *WorkloadHandler) InspectNode(context.Context, *connect.Request[workloadv1.InspectNodeRequest]) (*connect.Response[workloadv1.InspectNodeResponse], error) {
	_, cgroupErr := os.Stat("/sys/fs/cgroup/cgroup.controllers")
	_, podmanErr := exec.LookPath("podman")
	reason := "container and native runners are not implemented"
	return connect.NewResponse(&workloadv1.InspectNodeResponse{Capabilities: &workloadv1.NodeCapabilities{
		CgroupV2: cgroupErr == nil, RootlessPodman: podmanErr == nil,
		ContainerRunner: false, NativeRunner: false, Reason: reason,
	}}), nil
}
