//go:build integration && shareddesktopapps

package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	appv1 "github.com/yangtao121/workos/gen/go/workos/app/v1"
	"github.com/yangtao121/workos/gen/go/workos/app/v1/appv1connect"
	projectv1 "github.com/yangtao121/workos/gen/go/workos/project/v1"
	"github.com/yangtao121/workos/gen/go/workos/project/v1/projectv1connect"
	runtimev1 "github.com/yangtao121/workos/gen/go/workos/runtime/v1"
	"github.com/yangtao121/workos/gen/go/workos/runtime/v1/runtimev1connect"
	surfacev1 "github.com/yangtao121/workos/gen/go/workos/surface/v1"
	"github.com/yangtao121/workos/gen/go/workos/surface/v1/surfacev1connect"
	"github.com/yangtao121/workos/internal/platform/appbundle"
	"github.com/yangtao121/workos/internal/platform/ids"
)

const sharedDesktopAppImage = "golang@sha256:e8c859f5632dcfde7b32d2012b4351728f6437930887c2f6a91ea242459e5514"

// TestSharedDesktopInstalledAppContinuity uses a real imported binary, Docker,
// gateway, Core, Runtime and PostgreSQL. Run only in the isolated V2 fixture via
// tools/shared-desktop/apps.sh (30s legacy idle TTL, 1s reconciliation). The two
// private Runtime identities exercise its trusted gateway boundary; production
// callers cannot supply these trusted headers through the public gateway.
func TestSharedDesktopInstalledAppContinuity(t *testing.T) {
	required := func(name string) string {
		t.Helper()
		value := os.Getenv(name)
		if value == "" {
			t.Fatalf("%s required; run tools/shared-desktop/apps.sh in the isolated fixture", name)
		}
		return value
	}
	gateway := required("WORKOS_TEST_URL")
	runtimeURL := required("WORKOS_TEST_RUNTIME_URL")
	database := required("WORKOS_SHARED_DESKTOP_DATABASE_URL")
	adminSocket := required("WORKOS_SHARED_DESKTOP_ARTIFACT_SOCKET")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	client := &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{Proxy: nil}}
	t.Cleanup(client.CloseIdleConnections)
	projects := projectv1connect.NewProjectServiceClient(client, gateway)
	install := appv1connect.NewAppInstallationServiceClient(client, gateway)
	registry := appv1connect.NewAppRegistryServiceClient(client, gateway)
	surfaces := surfacev1connect.NewSurfaceServiceClient(client, gateway)
	continuity := surfacev1connect.NewSurfaceContinuityServiceClient(client, gateway)
	db, err := pgx.Connect(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close(context.Background()) })
	// Facts only: setup and cleanup go through the authoritative APIs.
	if _, err := db.Exec(ctx, "SET default_transaction_read_only = on"); err != nil {
		t.Fatal(err)
	}

	type fixture struct {
		project, installation, workload string
		generation                      int64
	}
	seedProject := func(name string) *fixture {
		t.Helper()
		created, err := projects.CreateProject(ctx, connect.NewRequest(&projectv1.CreateProjectRequest{
			IdempotencyKey: ids.UUIDv7{}.New(), Name: name,
		}))
		if err != nil {
			t.Fatal(err)
		}
		f := &fixture{project: created.Msg.GetProject().GetId()}
		t.Cleanup(func() {
			cleanup, done := context.WithTimeout(context.Background(), 20*time.Second)
			defer done()
			if f.workload != "" {
				if _, err := continuity.StopSurfaceWorkload(cleanup, connect.NewRequest(&surfacev1.StopSurfaceWorkloadRequest{WorkloadId: f.workload, ActionKey: ids.UUIDv7{}.New()})); err != nil {
					t.Errorf("cleanup stop: %v", err)
				}
			}
			project, err := projects.GetProject(cleanup, connect.NewRequest(&projectv1.GetProjectRequest{ProjectId: f.project}))
			if err != nil {
				t.Errorf("cleanup read project: %v", err)
				return
			}
			revision := project.Msg.GetProject().GetRevision()
			if f.installation != "" {
				removed, err := install.UninstallApp(cleanup, connect.NewRequest(&appv1.UninstallAppRequest{
					IdempotencyKey: ids.UUIDv7{}.New(), ProjectId: f.project, InstallationId: f.installation, ExpectedProjectRevision: revision,
				}))
				if err != nil {
					t.Errorf("cleanup uninstall: %v", err)
					return
				}
				revision = removed.Msg.GetProjectRevision()
			}
			if _, err := projects.ArchiveProject(cleanup, connect.NewRequest(&projectv1.ArchiveProjectRequest{ProjectId: f.project, ExpectedRevision: revision})); err != nil {
				t.Errorf("cleanup archive: %v", err)
			}
		})
		return f
	}
	manual := seedProject("Shared desktop app manual stop fixture")
	bounded := seedProject("Shared desktop app legacy idle control")
	project, err := projects.GetProject(ctx, connect.NewRequest(&projectv1.GetProjectRequest{ProjectId: manual.project}))
	if err != nil {
		t.Fatal(err)
	}
	owner := project.Msg.GetProject().GetOwnerUserId()
	secondHTTP := &identityHTTPClient{client: client, userID: owner, deviceID: ids.UUIDv7{}.New()}
	second := surfacev1connect.NewSurfaceServiceClient(secondHTTP, runtimeURL)
	secondContinuity := surfacev1connect.NewSurfaceContinuityServiceClient(secondHTTP, runtimeURL)
	foreignHTTP := &identityHTTPClient{client: client, userID: ids.UUIDv7{}.New(), deviceID: ids.UUIDv7{}.New()}
	foreign := surfacev1connect.NewSurfaceServiceClient(foreignHTTP, runtimeURL)
	foreignContinuity := surfacev1connect.NewSurfaceContinuityServiceClient(foreignHTTP, runtimeURL)
	appID := "shared-" + strings.ReplaceAll(ids.UUIDv7{}.New(), "-", "")
	artifactID, digest := sharedDesktopImportApp(t, ctx, adminSocket, owner, appID)
	manifest := map[string]any{
		"apiVersion": "workos.app/v1", "id": appID, "name": "Shared desktop process fixture", "version": "1.0.0", "scope": "project",
		"runtime":  map[string]any{"type": "container", "image": sharedDesktopAppImage, "command": []string{"/app/server"}, "port": 8080, "artifact": map[string]any{"id": artifactID, "digest": digest, "format": "app-bundle.v1"}},
		"surfaces": []any{map[string]any{"id": "main", "renderer": "web-service", "route": "/"}}, "permissions": []string{},
		"resources": map[string]any{"cpuHard": 1, "memoryHighMb": 64, "memoryMaxMb": 128, "pidsMax": 64},
		"health":    map[string]any{"httpPath": "/health", "startupSeconds": 3, "restartLimit": 0}, "maintainer": map[string]any{},
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.RegisterApp(ctx, connect.NewRequest(&appv1.RegisterAppRequest{IdempotencyKey: ids.UUIDv7{}.New(), ManifestYaml: raw})); err != nil {
		t.Fatal(err)
	}
	for _, f := range []*fixture{manual, bounded} {
		p, err := projects.GetProject(ctx, connect.NewRequest(&projectv1.GetProjectRequest{ProjectId: f.project}))
		if err != nil {
			t.Fatal(err)
		}
		installed, err := install.InstallApp(ctx, connect.NewRequest(&appv1.InstallAppRequest{IdempotencyKey: ids.UUIDv7{}.New(), ProjectId: f.project, AppId: appID, Version: "1.0.0", ExpectedProjectRevision: p.Msg.GetProject().GetRevision()}))
		if err != nil {
			t.Fatal(err)
		}
		f.installation = installed.Msg.GetInstallation().GetId()
	}
	create := func(f *fixture, mode surfacev1.LifecycleMode) *surfacev1.SurfaceSession {
		t.Helper()
		response, err := surfaces.CreateSurface(ctx, connect.NewRequest(&surfacev1.CreateSurfaceRequest{
			ProjectId: f.project, AppInstanceId: f.installation, IdempotencyKey: ids.UUIDv7{}.New(), ExpectedAppVersion: "1.0.0",
			DeviceClass: surfacev1.DeviceClass_DEVICE_CLASS_DESKTOP, PreferredRenderer: surfacev1.SurfaceRenderer_SURFACE_RENDERER_WEB_SERVICE,
			Viewport: &surfacev1.Viewport{Width: 1280, Height: 800, PixelRatio: 1}, LifecycleMode: mode,
		}))
		if err != nil {
			t.Fatalf("launch app: %v", err)
		}
		session := response.Msg.GetSession()
		f.workload, f.generation = session.GetWorkloadId(), session.GetWorkloadGeneration()
		if f.workload == "" || f.generation < 1 {
			t.Fatal("launch omitted exact workload identity")
		}
		t.Cleanup(func() {
			cleanup, done := context.WithTimeout(context.Background(), 5*time.Second)
			defer done()
			_, _ = surfaces.CloseSurface(cleanup, connect.NewRequest(&surfacev1.CloseSurfaceRequest{SurfaceSessionId: session.GetId()}))
		})
		return session
	}
	a := create(manual, surfacev1.LifecycleMode_LIFECYCLE_MODE_MANUAL_STOP)
	legacy := create(bounded, surfacev1.LifecycleMode_LIFECYCLE_MODE_UNSPECIFIED)
	if a.GetLifecycleMode() != surfacev1.LifecycleMode_LIFECYCLE_MODE_MANUAL_STOP || legacy.GetLifecycleMode() != surfacev1.LifecycleMode_LIFECYCLE_MODE_BOUNDED {
		t.Fatal("launch did not report persisted actual policy")
	}
	readProcess := func(c interface {
		Do(*http.Request) (*http.Response, error)
	}, base string, session *surfacev1.SurfaceSession) string {
		t.Helper()
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+session.GetUrl(), nil)
		if err != nil {
			t.Fatal(err)
		}
		response, err := c.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, err := io.ReadAll(io.LimitReader(response.Body, 4096))
		if err != nil || response.StatusCode != http.StatusOK {
			t.Fatalf("app proxy: status=%d err=%v", response.StatusCode, err)
		}
		marker := string(body)
		if !strings.HasPrefix(marker, "shared-desktop-process:") || len(marker) != len("shared-desktop-process:")+32 {
			t.Fatal("app did not return its process-local nonce")
		}
		return marker
	}
	nonce := readProcess(client, gateway, a)
	_ = readProcess(client, gateway, legacy)
	get := func(f *fixture) *surfacev1.SurfaceWorkloadView {
		t.Helper()
		result, err := continuity.GetSurfaceWorkload(ctx, connect.NewRequest(&surfacev1.GetSurfaceWorkloadRequest{WorkloadId: f.workload}))
		if err != nil {
			t.Fatal(err)
		}
		return result.Msg.GetWorkload()
	}
	assertRunning := func(f *fixture) {
		t.Helper()
		w := get(f)
		if w.GetState() != "running" || w.GetGeneration() != f.generation || w.GetPolicy().GetLifecycleMode() != surfacev1.LifecycleMode_LIFECYCLE_MODE_MANUAL_STOP {
			t.Fatalf("manual program changed: state=%s generation=%d policy=%v", w.GetState(), w.GetGeneration(), w.GetPolicy())
		}
	}
	type facts struct {
		workloads, ensures int
		container          string
	}
	snapshot := func(f *fixture) facts {
		t.Helper()
		var result facts
		if err := db.QueryRow(ctx, `SELECT count(*) FROM workos_runtime.workloads WHERE owner_user_id=$1 AND app_instance_id=$2`, owner, f.installation).Scan(&result.workloads); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(ctx, `SELECT count(*) FROM workos_runtime.workload_operations WHERE workload_id=$1 AND operation='ensure'`, f.workload).Scan(&result.ensures); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(ctx, `SELECT COALESCE(container_id,'') FROM workos_runtime.workloads WHERE id=$1`, f.workload).Scan(&result.container); err != nil {
			t.Fatal(err)
		}
		return result
	}
	before := snapshot(manual)
	if before.workloads != 1 || before.ensures != 1 || before.container == "" {
		t.Fatalf("initial engine facts: %+v", before)
	}
	closeSurface := func(c surfacev1connect.SurfaceServiceClient, session *surfacev1.SurfaceSession) {
		t.Helper()
		if _, err := c.CloseSurface(ctx, connect.NewRequest(&surfacev1.CloseSurfaceRequest{SurfaceSessionId: session.GetId()})); err != nil {
			t.Fatal(err)
		}
	}
	closeSurface(surfaces, a)
	closeSurface(surfaces, legacy)
	closedAt := time.Now()
	deadline := time.Now().Add(75 * time.Second)
	for get(bounded).GetState() != "stopped" {
		assertRunning(manual)
		if time.Now().After(deadline) {
			t.Fatal("legacy app did not idle-stop; gate requires 30s idle TTL and live reconciliation")
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(time.Second):
		}
	}
	var idleStops int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM workos_runtime.workload_operations WHERE workload_id=$1 AND operation_key='reconcile:idle' AND result_state='stopped'`, bounded.workload).Scan(&idleStops); err != nil {
		t.Fatal(err)
	}
	if idleStops != 1 {
		t.Fatal("legacy control stopped without the expected idle-reclamation receipt")
	}
	boundedBeforeRestore := snapshot(bounded)
	// Waiting for the control's actual idle stop proves reconciliation has run;
	// process identity then rules out recreating the manual program in that gap.
	assertRunning(manual)
	if w := get(manual); w.GetAttachmentCount() != 0 {
		t.Fatalf("closed app still reports %d device views", w.GetAttachmentCount())
	}
	t.Logf("all windows closed for %s: legacy stopped, manual remains running", time.Since(closedAt).Round(time.Second))
	discovered, err := secondContinuity.ListProjectSurfaces(ctx, connect.NewRequest(&surfacev1.ListProjectSurfacesRequest{ProjectId: manual.project}))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, w := range discovered.Msg.GetWorkloads() {
		if w.GetWorkloadId() == manual.workload && w.GetGeneration() == manual.generation && w.GetAppInstanceId() == manual.installation && w.GetAppId() == appID && w.GetVersion() == "1.0.0" {
			found = true
		}
	}
	if !found {
		t.Fatal("second device could not discover exact installed app program")
	}
	attachRequest := func() *surfacev1.CreateSurfaceRequest {
		return &surfacev1.CreateSurfaceRequest{ProjectId: manual.project, AppInstanceId: manual.installation, ExpectedAppVersion: "1.0.0", IdempotencyKey: ids.UUIDv7{}.New(), AttachOnly: true, ExpectedWorkloadId: manual.workload, ExpectedWorkloadGeneration: manual.generation, DeviceClass: surfacev1.DeviceClass_DEVICE_CLASS_PHONE, Viewport: &surfacev1.Viewport{Width: 390, Height: 844, PixelRatio: 2}}
	}
	stale := attachRequest()
	stale.ExpectedWorkloadGeneration++
	if _, err := second.CreateSurface(ctx, connect.NewRequest(stale)); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("stale generation verdict: %v", err)
	}
	wrong := attachRequest()
	wrong.ProjectId = bounded.project
	wrong.AppInstanceId = bounded.installation
	if _, err := second.CreateSurface(ctx, connect.NewRequest(wrong)); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("foreign installation target verdict: %v", err)
	}
	if _, err := foreign.CreateSurface(ctx, connect.NewRequest(attachRequest())); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("foreign owner attach verdict: %v", err)
	}
	if _, err := foreignContinuity.GetSurfaceWorkload(ctx, connect.NewRequest(&surfacev1.GetSurfaceWorkloadRequest{WorkloadId: manual.workload})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("foreign owner read verdict: %v", err)
	}
	restored, err := second.CreateSurface(ctx, connect.NewRequest(attachRequest()))
	if err != nil {
		t.Fatalf("second device attach-only: %v", err)
	}
	b := restored.Msg.GetSession()
	if b.GetWorkloadId() != manual.workload || b.GetWorkloadGeneration() != manual.generation || b.GetLifecycleMode() != a.GetLifecycleMode() {
		t.Fatal("attach-only changed identity or policy")
	}
	if got := readProcess(secondHTTP, runtimeURL, b); got != nonce {
		t.Fatal("attach-only replaced process-local state")
	}
	if after := snapshot(manual); after != before {
		t.Fatalf("attach-only caused Ensure/container mutation: before=%+v after=%+v", before, after)
	}
	if after := snapshot(bounded); after != boundedBeforeRestore {
		t.Fatalf("rejected cross-installation attach mutated the other workload: before=%+v after=%+v", boundedBeforeRestore, after)
	}
	if w := get(manual); w.GetAttachmentCount() != 1 {
		t.Fatalf("restored app reports %d device views, want 1", w.GetAttachmentCount())
	}
	closeSurface(second, b)
	stopped, err := continuity.StopSurfaceWorkload(ctx, connect.NewRequest(&surfacev1.StopSurfaceWorkloadRequest{WorkloadId: manual.workload, ActionKey: ids.UUIDv7{}.New()}))
	if err != nil || stopped.Msg.GetWorkload().GetState() != "stopped" {
		t.Fatalf("explicit stop: %v", err)
	}
	// Passive observers may reopen views, but restoration must never restart.
	// The same owner can separately choose the explicit Restart command.
	for attempt := 0; attempt < 3; attempt++ {
		if _, err := second.CreateSurface(ctx, connect.NewRequest(attachRequest())); connect.CodeOf(err) != connect.CodeFailedPrecondition {
			t.Fatalf("observer restored stopped program: %v", err)
		}
		result, err := secondContinuity.GetSurfaceWorkload(ctx, connect.NewRequest(&surfacev1.GetSurfaceWorkloadRequest{WorkloadId: manual.workload}))
		if err != nil {
			t.Fatal(err)
		}
		if w := result.Msg.GetWorkload(); w.GetState() != "stopped" || w.GetGeneration() != manual.generation || w.GetPolicy().GetLifecycleMode() != surfacev1.LifecycleMode_LIFECYCLE_MODE_MANUAL_STOP {
			t.Fatalf("observer mutated terminal program: %v", w)
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(time.Second):
		}
	}
	afterStop := snapshot(manual)
	if afterStop.workloads != before.workloads || afterStop.ensures != before.ensures {
		t.Fatalf("observer ensured a replacement after stop: before=%+v after=%+v", before, afterStop)
	}
	t.Log("second device retained exact process; stale/foreign targets refused; explicit stop stayed terminal without an Ensure")
}

func sharedDesktopImportApp(t *testing.T, ctx context.Context, socket, owner, app string) (string, string) {
	t.Helper()
	src, output := t.TempDir(), t.TempDir()
	// The random nonce exists only in process memory: a replacement binary
	// cannot reconstruct it from disk, requests, or workload identity.
	files := map[string]string{
		"go.mod": "module fixture\n\ngo 1.26\n",
		"main.go": `package main
import("crypto/rand";"encoding/hex";"fmt";"net/http";"os")
func main(){ var nonce [16]byte;if _,err:=rand.Read(nonce[:]);err!=nil{os.Exit(1)}
http.HandleFunc("/health",func(w http.ResponseWriter,r *http.Request){fmt.Fprint(w,"ok")})
http.HandleFunc("/",func(w http.ResponseWriter,r *http.Request){w.Header().Set("Content-Type","text/plain");fmt.Fprint(w,"shared-desktop-process:"+hex.EncodeToString(nonce[:]))})
if http.ListenAndServe(":8080",nil)!=nil{os.Exit(1)}}`,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(src, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	build := exec.CommandContext(ctx, "go", "build", "-trimpath", "-o", filepath.Join(output, "server"), ".")
	build.Dir = src
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOPROXY=off", "GOMAXPROCS=2")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build fixture: %v %s", err, out)
	}
	var bundle bytes.Buffer
	stats, err := appbundle.EncodeDirectory(output, &bundle)
	if err != nil {
		t.Fatal(err)
	}
	transport := &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "unix", socket)
	}}
	defer transport.CloseIdleConnections()
	admin := runtimev1connect.NewArtifactAdminServiceClient(&http.Client{Timeout: 30 * time.Second, Transport: transport}, "http://unix")
	stream := admin.ImportArtifact(ctx)
	if err := stream.Send(&runtimev1.ImportArtifactRequest{Part: &runtimev1.ImportArtifactRequest_Start{Start: &runtimev1.ImportArtifactStart{OwnerUserId: owner, AppId: app, IdempotencyKey: ids.UUIDv7{}.New(), ExpectedDigest: stats.Digest}}}); err != nil {
		t.Fatal(err)
	}
	for bundle.Len() > 0 {
		chunk := append([]byte(nil), bundle.Next(256<<10)...)
		if err := stream.Send(&runtimev1.ImportArtifactRequest{Part: &runtimev1.ImportArtifactRequest_Data{Data: chunk}}); err != nil {
			t.Fatal(err)
		}
	}
	response, err := stream.CloseAndReceive()
	if err != nil {
		t.Fatal(err)
	}
	if response.Msg.GetDigest() != stats.Digest || response.Msg.GetArtifactId() == "" {
		t.Fatal("artifact import did not preserve exact bytes")
	}
	return response.Msg.GetArtifactId(), stats.Digest
}
