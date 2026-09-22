package runtimeclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	surfacev1 "github.com/yangtao121/workos/gen/go/workos/surface/v1"
	"github.com/yangtao121/workos/gen/go/workos/surface/v1/surfacev1connect"
	"github.com/yangtao121/workos/internal/core/desktop/domain"
	"github.com/yangtao121/workos/internal/platform/identity"
	"github.com/yangtao121/workos/internal/platform/ids"
)

type runtimeFixture struct {
	surfacev1connect.UnimplementedSurfaceContinuityServiceHandler
	surfacev1connect.UnimplementedWorkspacePreviewServiceHandler
	owner, device, project, workload string
	renderer                         surfacev1.SurfaceRenderer
	err                              error
	t                                *testing.T
}

func (f *runtimeFixture) GetSurfaceWorkload(_ context.Context, r *connect.Request[surfacev1.GetSurfaceWorkloadRequest]) (*connect.Response[surfacev1.GetSurfaceWorkloadResponse], error) {
	if r.Header().Get(identity.UserHeader) != f.owner || r.Header().Get(identity.DeviceHeader) != f.device {
		f.t.Fatal("missing trusted identity")
	}
	if f.err != nil {
		return nil, f.err
	}
	return connect.NewResponse(&surfacev1.GetSurfaceWorkloadResponse{Workload: &surfacev1.SurfaceWorkloadView{WorkloadId: f.workload, ProjectId: f.project, Renderer: f.renderer, State: "failed", Generation: 3}}), nil
}
func (f *runtimeFixture) GetWorkspacePreview(_ context.Context, r *connect.Request[surfacev1.GetWorkspacePreviewRequest]) (*connect.Response[surfacev1.GetWorkspacePreviewResponse], error) {
	return connect.NewResponse(&surfacev1.GetWorkspacePreviewResponse{Preview: &surfacev1.WorkspacePreview{Id: r.Msg.GetPreviewId(), OwnerUserId: f.owner, ProjectId: f.project, State: "stopped"}}), nil
}
func TestExactRuntimeReferenceSurvivesStoppedButRejectsForeignAndWrongRenderer(t *testing.T) {
	g := ids.UUIDv7{}
	f := &runtimeFixture{owner: g.New(), device: g.New(), project: g.New(), workload: g.New(), t: t}
	mux := http.NewServeMux()
	path, h := surfacev1connect.NewSurfaceContinuityServiceHandler(f)
	mux.Handle(path, h)
	path, h = surfacev1connect.NewWorkspacePreviewServiceHandler(f)
	mux.Handle(path, h)
	server := httptest.NewServer(mux)
	defer server.Close()
	r := New(server.URL)
	ctx := identity.WithContext(context.Background(), identity.Identity{UserID: f.owner, DeviceID: f.device})
	target := domain.Target{Kind: "terminal", ProjectID: f.project, ResourceKind: "workload", ResourceID: f.workload}
	if err := r.Target(ctx, f.owner, target); err != nil {
		t.Fatal("failed workload ref should survive", err)
	}
	target.ProjectID = g.New()
	if err := r.Target(ctx, f.owner, target); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("foreign project allowed", err)
	}
	target.ProjectID = f.project
	target.Kind = "native"
	if err := r.Target(ctx, f.owner, target); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("wrong renderer allowed", err)
	}
	target.Kind = "terminal"
	target.ResourceID = g.New()
	if err := r.Target(ctx, f.owner, target); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("wrong workload allowed", err)
	}
	target.ResourceID = f.workload
	f.err = connect.NewError(connect.CodeUnavailable, errors.New("fixture outage"))
	if err := r.Target(ctx, f.owner, target); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatal("outage misclassified", err)
	}
	preview := domain.Target{Kind: "workspace-previews", ProjectID: f.project, ResourceKind: "preview", ResourceID: g.New()}
	if err := r.Target(ctx, f.owner, preview); err != nil {
		t.Fatal("stopped preview ref lost", err)
	}
	preview.ProjectID = g.New()
	if err := r.Target(ctx, f.owner, preview); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("foreign preview project allowed", err)
	}
}
