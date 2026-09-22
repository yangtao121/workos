package runtimeclient

import (
	"context"
	"time"

	"connectrpc.com/connect"
	surfacev1 "github.com/yangtao121/workos/gen/go/workos/surface/v1"
	"github.com/yangtao121/workos/gen/go/workos/surface/v1/surfacev1connect"
	"github.com/yangtao121/workos/internal/core/desktop/domain"
	"github.com/yangtao121/workos/internal/platform/identity"
	"github.com/yangtao121/workos/internal/platform/telemetry"
)

type References struct {
	workloads surfacev1connect.SurfaceContinuityServiceClient
	previews  surfacev1connect.WorkspacePreviewServiceClient
}

func New(url string) *References {
	return &References{workloads: surfacev1connect.NewSurfaceContinuityServiceClient(telemetry.HTTPClient(), url), previews: surfacev1connect.NewWorkspacePreviewServiceClient(telemetry.HTTPClient(), url)}
}
func mapError(err error) error {
	switch connect.CodeOf(err) {
	case connect.CodeNotFound, connect.CodePermissionDenied:
		return domain.ErrNotFound
	case connect.CodeUnavailable, connect.CodeDeadlineExceeded, connect.CodeUnimplemented, connect.CodeCanceled:
		return domain.ErrUnavailable
	default:
		return err
	}
}
func authorize[T any](ctx context.Context, owner string, req *connect.Request[T]) error {
	id, err := identity.FromContext(ctx)
	if err != nil || id.UserID != owner || !domain.UUID(id.DeviceID) {
		return domain.ErrInvalid
	}
	req.Header().Set(identity.UserHeader, owner)
	req.Header().Set(identity.DeviceHeader, id.DeviceID)
	return nil
}
func (r *References) Target(ctx context.Context, owner string, t domain.Target) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	switch t.ResourceKind {
	case "workload":
		req := connect.NewRequest(&surfacev1.GetSurfaceWorkloadRequest{WorkloadId: t.ResourceID})
		if err := authorize(ctx, owner, req); err != nil {
			return err
		}
		resp, err := r.workloads.GetSurfaceWorkload(ctx, req)
		if err != nil {
			return mapError(err)
		}
		w := resp.Msg.GetWorkload()
		if w == nil || w.GetWorkloadId() != t.ResourceID || w.GetProjectId() != t.ProjectID || w.GetAppInstanceId() != "" {
			return domain.ErrNotFound
		}
		if (t.Kind == "native" && w.GetRenderer() != surfacev1.SurfaceRenderer_SURFACE_RENDERER_REMOTE_NATIVE) || (t.Kind == "terminal" && w.GetRenderer() != surfacev1.SurfaceRenderer_SURFACE_RENDERER_UNSPECIFIED) {
			return domain.ErrNotFound
		}
		return nil
	case "preview":
		req := connect.NewRequest(&surfacev1.GetWorkspacePreviewRequest{PreviewId: t.ResourceID})
		if err := authorize(ctx, owner, req); err != nil {
			return err
		}
		resp, err := r.previews.GetWorkspacePreview(ctx, req)
		if err != nil {
			return mapError(err)
		}
		p := resp.Msg.GetPreview()
		if p == nil || p.GetId() != t.ResourceID || p.GetOwnerUserId() != owner || p.GetProjectId() != t.ProjectID {
			return domain.ErrNotFound
		}
		return nil
	default:
		return domain.ErrInvalid
	}
}
