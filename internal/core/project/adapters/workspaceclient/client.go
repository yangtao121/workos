// Package workspaceclient reads the runtime host's workspace sources over
// the private WorkspaceHostService (ADR-0030). Core validates bindings
// against it; the operator registers roots in runtime deployment config.
package workspaceclient

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"
	workloadv1 "github.com/yangtao121/workos/gen/go/workos/workload/v1"
	"github.com/yangtao121/workos/gen/go/workos/workload/v1/workloadv1connect"
	"github.com/yangtao121/workos/internal/core/project/ports"
	"github.com/yangtao121/workos/internal/platform/identity"
	"github.com/yangtao121/workos/internal/platform/telemetry"
)

var ErrDirectoryUnavailable = errors.New("workspace source directory is unavailable")

type Client struct {
	client   workloadv1connect.WorkspaceHostServiceClient
	deviceID string
}

func New(runtimeURL, deviceID string) *Client {
	return &Client{
		client:   workloadv1connect.NewWorkspaceHostServiceClient(telemetry.HTTPClient(), runtimeURL),
		deviceID: deviceID,
	}
}

// Sources lists the operator-registered sources of one owner. Host paths
// never cross this boundary; only bounded descriptive facts return.
func (c *Client) Sources(ctx context.Context, ownerUserID string) ([]ports.WorkspaceSource, error) {
	request := connect.NewRequest(&workloadv1.DescribeWorkspaceSourcesRequest{})
	request.Header().Set(identity.UserHeader, ownerUserID)
	request.Header().Set(identity.DeviceHeader, c.deviceID)
	response, err := c.client.DescribeWorkspaceSources(ctx, request)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrDirectoryUnavailable, connect.CodeOf(err))
	}
	sources := make([]ports.WorkspaceSource, 0, len(response.Msg.GetSources()))
	for _, source := range response.Msg.GetSources() {
		registered := source.GetRegisteredAt().AsTime()
		sources = append(sources, ports.WorkspaceSource{
			ID: source.GetId(), Kind: source.GetKind(), DisplayName: source.GetDisplayName(),
			ReadOnly: source.GetReadOnly(), Registered: registered,
		})
	}
	return sources, nil
}

var _ ports.SourceDirectory = (*Client)(nil)
