package transport

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	appv1 "github.com/yangtao121/workos/gen/go/workos/app/v1"
	"github.com/yangtao121/workos/gen/go/workos/app/v1/appv1connect"
	surfacev1 "github.com/yangtao121/workos/gen/go/workos/surface/v1"
	"github.com/yangtao121/workos/gen/go/workos/surface/v1/surfacev1connect"
	"github.com/yangtao121/workos/internal/reliability/application"
	"github.com/yangtao121/workos/internal/reliability/ports"
)

type restoredBundleFixture struct {
	rollbackFixture
	generation int64
}

func (f *restoredBundleFixture) RollbackAppVersion(ctx context.Context, req *connect.Request[appv1.RollbackAppVersionRequest]) (*connect.Response[appv1.RollbackAppVersionResponse], error) {
	response, err := f.rollbackFixture.RollbackAppVersion(ctx, req)
	if err == nil {
		response.Msg.Installation.ManifestDigest = "manifest-A"
	}
	return response, err
}
func (f *restoredBundleFixture) ResolveSurfaceLaunch(context.Context, *connect.Request[surfacev1.ResolveSurfaceLaunchRequest]) (*connect.Response[surfacev1.ResolveSurfaceLaunchResponse], error) {
	return connect.NewResponse(&surfacev1.ResolveSurfaceLaunchResponse{Launch: &surfacev1.ResolveSurfaceLaunchResponse_WebServiceContainer{WebServiceContainer: &surfacev1.ContainerLaunchDescriptor{
		Version: "1.0.0", ManifestDigest: "manifest-A", Artifact: &surfacev1.ArtifactBinding{ArtifactId: "artifact-A", ArtifactDigest: "digest-A", ArtifactFormat: "app-bundle.v1"},
	}}}), nil
}
func (f *restoredBundleFixture) ListObservations(context.Context) ([]ports.Observation, error) {
	generation := f.generation
	if generation == 0 {
		generation = 1
	}
	return []ports.Observation{{WorkloadID: "workload-A", Generation: generation, OwnerUserID: f.candidate.OwnerUserID, AppInstanceID: f.candidate.InstallationID, State: ports.StateRunning, HealthVerdict: "ok", ManifestDigest: "manifest-A", ArtifactDigest: "digest-A", IdentityVerified: true}}, nil
}
func TestRollbackUsesRestoredBundleIdentity(t *testing.T) {
	candidate := application.DeploymentCandidate{OwnerUserID: "owner", ProjectID: "project", InstallationID: "installation", ExpectedRevision: 7, TargetVersion: "2.0.0", ManifestDigest: "manifest-B", ArtifactDigest: "digest-B"}
	f := &restoredBundleFixture{rollbackFixture: rollbackFixture{candidate: candidate, t: t}}
	mux := http.NewServeMux()
	mux.Handle(appv1connect.NewAppInstallationServiceHandler(f))
	mux.Handle(surfacev1connect.NewSurfaceServiceHandler(f))
	mux.Handle(surfacev1connect.NewSurfaceLaunchResolverServiceHandler(f))
	server := httptest.NewServer(mux)
	defer server.Close()
	driver := NewDeploymentDriverClient(server.URL, server.URL, "repair-device").WithObserver(f)
	if err := driver.Rollback(context.Background(), candidate, "rollback"); err != nil {
		t.Fatalf("A must be verified using A's manifest and artifact, not B's: %v", err)
	}
	f.calls = nil
	if err := driver.StartSurface(context.Background(), candidate, "stale-B"); !errors.Is(err, application.ErrDeploymentSuperseded) {
		t.Fatalf("stale candidate: %v", err)
	}
	if len(f.calls) != 0 {
		t.Fatalf("stale replay launched a surface: %v", f.calls)
	}
}

func TestCanaryRejectsReplacementGeneration(t *testing.T) {
	candidate := application.DeploymentCandidate{OwnerUserID: "owner", ProjectID: "project", InstallationID: "installation", TargetVersion: "1.0.0", ManifestDigest: "manifest-A", ArtifactDigest: "digest-A"}
	f := &restoredBundleFixture{rollbackFixture: rollbackFixture{candidate: candidate, t: t}}
	mux := http.NewServeMux()
	mux.Handle(surfacev1connect.NewSurfaceLaunchResolverServiceHandler(f))
	server := httptest.NewServer(mux)
	defer server.Close()
	driver := NewDeploymentDriverClient(server.URL, server.URL, "repair-device").WithObserver(f)
	if err := driver.Verify(context.Background(), &candidate); err != nil {
		t.Fatal(err)
	}
	if candidate.WorkloadID != "workload-A" || candidate.WorkloadGeneration != 1 {
		t.Fatalf("missing canary fence: %+v", candidate)
	}
	f.generation = 2
	if err := driver.Verify(context.Background(), &candidate); err == nil {
		t.Fatal("replacement workload must not inherit the previous generation's canary window")
	}
}
