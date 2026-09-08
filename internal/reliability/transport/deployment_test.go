package transport

import (
	"context"
	"errors"
	"google.golang.org/protobuf/types/known/timestamppb"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"connectrpc.com/connect"
	appv1 "github.com/yangtao121/workos/gen/go/workos/app/v1"
	"github.com/yangtao121/workos/gen/go/workos/app/v1/appv1connect"
	surfacev1 "github.com/yangtao121/workos/gen/go/workos/surface/v1"
	"github.com/yangtao121/workos/gen/go/workos/surface/v1/surfacev1connect"
	"github.com/yangtao121/workos/internal/platform/identity"
	"github.com/yangtao121/workos/internal/reliability/application"
)

type rollbackFixture struct {
	appv1connect.UnimplementedAppInstallationServiceHandler
	surfacev1connect.UnimplementedSurfaceServiceHandler
	calls                           []string
	coreFails, runtimeFails, closed bool
	candidate                       application.DeploymentCandidate
	t                               *testing.T
}

func (f *rollbackFixture) checkIdentity(header http.Header) {
	f.t.Helper()
	if header.Get(identity.UserHeader) != f.candidate.OwnerUserID || header.Get(identity.DeviceHeader) != "repair-device" {
		f.t.Fatal("rollback lost trusted identity")
	}
}
func (f *rollbackFixture) RollbackAppVersion(_ context.Context, req *connect.Request[appv1.RollbackAppVersionRequest]) (*connect.Response[appv1.RollbackAppVersionResponse], error) {
	f.checkIdentity(req.Header())
	if req.Msg.GetProjectId() != f.candidate.ProjectID || req.Msg.GetInstallationId() != f.candidate.InstallationID || req.Msg.GetExpectedProjectRevision() != f.candidate.ExpectedRevision+1 {
		f.t.Fatal("rollback changed candidate scope or revision")
	}
	f.calls = append(f.calls, "pin:"+req.Msg.GetIdempotencyKey())
	if f.coreFails {
		return nil, connect.NewError(connect.CodeAborted, errors.New("revision conflict"))
	}
	return connect.NewResponse(&appv1.RollbackAppVersionResponse{Installation: &appv1.AppInstallation{Id: f.candidate.InstallationID, ProjectId: f.candidate.ProjectID, Version: "1.0.0"}}), nil
}
func (f *rollbackFixture) CreateSurface(_ context.Context, req *connect.Request[surfacev1.CreateSurfaceRequest]) (*connect.Response[surfacev1.CreateSurfaceResponse], error) {
	f.checkIdentity(req.Header())
	if req.Msg.GetProjectId() != f.candidate.ProjectID || req.Msg.GetAppInstanceId() != f.candidate.InstallationID {
		f.t.Fatal("recovery surface changed scope")
	}
	if req.Msg.GetExpectedAppVersion() != "1.0.0" {
		f.t.Fatal("recovery did not pin the Core rollback version")
	}
	f.calls = append(f.calls, "start:"+req.Msg.GetIdempotencyKey())
	if f.runtimeFails {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("runtime unavailable"))
	}
	token := "synthetic-token"
	if f.closed {
		token = ""
	}
	return connect.NewResponse(&surfacev1.CreateSurfaceResponse{Session: &surfacev1.SurfaceSession{Id: "session", ProjectId: f.candidate.ProjectID, AppInstanceId: f.candidate.InstallationID, BridgeToken: token, ExpiresAt: timestamppb.New(time.Now().Add(time.Minute))}}), nil
}
func TestRollbackWaitsForRecoveredSurfaceAndReplaysStableCommands(t *testing.T) {
	candidate := application.DeploymentCandidate{OwnerUserID: "owner", ProjectID: "project", InstallationID: "installation", ExpectedRevision: 7}
	f := &rollbackFixture{candidate: candidate, t: t, coreFails: true}
	mux := http.NewServeMux()
	mux.Handle(appv1connect.NewAppInstallationServiceHandler(f))
	mux.Handle(surfacev1connect.NewSurfaceServiceHandler(f))
	server := httptest.NewServer(mux)
	defer server.Close()
	driver := NewDeploymentDriverClient(server.URL, server.URL, "repair-device")
	if err := driver.Rollback(context.Background(), candidate, "rollback-incident"); connect.CodeOf(err) != connect.CodeAborted || len(f.calls) != 1 {
		t.Fatalf("failed pin attempted launch: calls=%v err=%v", f.calls, err)
	}
	f.coreFails, f.runtimeFails = false, true
	if err := driver.Rollback(context.Background(), candidate, "rollback-incident"); connect.CodeOf(err) != connect.CodeUnavailable {
		t.Fatalf("failed recovery launch reported success: %v", err)
	}
	f.runtimeFails = false
	driver = NewDeploymentDriverClient(server.URL, server.URL, "repair-device")
	if err := driver.Rollback(context.Background(), candidate, "rollback-incident"); err != nil {
		t.Fatal(err)
	}
	want := []string{"pin:rollback-incident", "pin:rollback-incident", "start:rollback-incident-surface", "pin:rollback-incident", "start:rollback-incident-surface"}
	if !reflect.DeepEqual(f.calls, want) {
		t.Fatalf("commands=%v want=%v", f.calls, want)
	}
	f.closed = true
	if err := driver.Rollback(context.Background(), candidate, "rollback-incident"); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("closed replay reported active recovery: %v", err)
	}
}
