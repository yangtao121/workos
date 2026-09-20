//go:build integration && repairbuildtest && p3delivery

package integration_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	appv1 "github.com/yangtao121/workos/gen/go/workos/app/v1"
	bridgev1 "github.com/yangtao121/workos/gen/go/workos/bridge/v1"
	"github.com/yangtao121/workos/gen/go/workos/bridge/v1/bridgev1connect"
	projectv1 "github.com/yangtao121/workos/gen/go/workos/project/v1"
	surfacev1 "github.com/yangtao121/workos/gen/go/workos/surface/v1"
	"github.com/yangtao121/workos/gen/go/workos/surface/v1/surfacev1connect"
	"github.com/yangtao121/workos/internal/platform/appbundle"
	"github.com/yangtao121/workos/internal/platform/identity"
	"github.com/yangtao121/workos/internal/platform/ids"
	reliabilitytransport "github.com/yangtao121/workos/internal/reliability/transport"
	artifactdomain "github.com/yangtao121/workos/internal/runtime/artifactstore/domain"
)

func p3TestBundle(t *testing.T) (string, string) {
	t.Helper()
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "server"), []byte("#!/bin/sh\nexec /app/server\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	var bundle bytes.Buffer
	stats, err := appbundle.EncodeDirectory(source, &bundle)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "fixture.bundle")
	if err := os.WriteFile(path, bundle.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return path, stats.Digest
}

func p3ImportArtifact(t *testing.T, owner, appID, key, path, digest string) (string, error) {
	t.Helper()
	dir := os.Getenv("WORKOS_P3_GATE_DIR")
	output, err := exec.Command(filepath.Join(dir, "bin/workosctl"), "runtime", "import-artifact",
		"--socket", filepath.Join(dir, "run/artifact-admin.sock"), "--owner", owner,
		"--app", appID, "--key", key, "--digest", digest, "--bundle", path).CombinedOutput()
	if err != nil {
		return string(output), err
	}
	for _, line := range strings.Split(string(output), "\n") {
		if strings.HasPrefix(line, "artifact_id=") {
			return strings.TrimPrefix(line, "artifact_id="), nil
		}
	}
	return string(output), fmt.Errorf("import output omitted artifact identity")
}

// p3RegisterVariant imports and registers one more immutable version of the
// fixture app so an owner-driven transition has a real third target.
func p3RegisterVariant(t *testing.T, clients *buildtestClients, f p3Fixture, version string, value int) string {
	t.Helper()
	src := t.TempDir()
	output := t.TempDir()
	mainSource := fmt.Sprintf(`package main
import("fmt";"net/http";"os")
func value() int { return %d }
func main() { _ = os.Args
http.HandleFunc("/health",func(w http.ResponseWriter,r *http.Request){fmt.Fprint(w,"ok")})
http.HandleFunc("/",func(w http.ResponseWriter,r *http.Request){w.Header().Set("Content-Type","text/html; charset=utf-8"); fmt.Fprintf(w,"<html><body>P3-VALUE-%d</body></html>",value())})
if err:=http.ListenAndServe(":8080",nil); err!=nil { os.Exit(1) }
}`, value, value)
	files := []*appv1.AppSourceFile{
		{Path: "go.mod", Content: []byte("module fixture\n\ngo 1.26\n")},
		{Path: "main.go", Content: []byte(mainSource)},
	}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(src, f.Path), f.Content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// The schema requires a full build recipe whenever runtime carries an
	// artifact, so the variant also publishes its real source bundle.
	source, err := clients.sources.CreateAppSourceBundle(context.Background(), connect.NewRequest(&appv1.CreateAppSourceBundleRequest{IdempotencyKey: ids.UUIDv7{}.New() + "-source", Files: files}))
	if err != nil {
		t.Fatal(err)
	}
	build := exec.Command("go", "build", "-trimpath", "-o", filepath.Join(output, "server"), ".")
	build.Dir = src
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOPROXY=off", "GOMAXPROCS=2")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("variant build: %v %s", err, out)
	}
	var bundle bytes.Buffer
	stats, err := appbundle.EncodeDirectory(output, &bundle)
	if err != nil {
		t.Fatal(err)
	}
	bundlePath := filepath.Join(t.TempDir(), "variant.bundle")
	if err := os.WriteFile(bundlePath, bundle.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	key := ids.UUIDv7{}.New()
	dir := os.Getenv("WORKOS_P3_GATE_DIR")
	imported, err := exec.Command(filepath.Join(dir, "bin/workosctl"), "runtime", "import-artifact", "--socket", filepath.Join(dir, "run/artifact-admin.sock"), "--owner", p3Owner, "--app", f.App, "--key", key, "--digest", stats.Digest, "--bundle", bundlePath).CombinedOutput()
	if err != nil {
		t.Fatalf("variant import: %v %s", err, imported)
	}
	artifactID := ""
	for _, line := range strings.Split(string(imported), "\n") {
		if strings.HasPrefix(line, "artifact_id=") {
			artifactID = strings.TrimPrefix(line, "artifact_id=")
		}
	}
	if artifactID == "" {
		t.Fatalf("missing variant import identity: %s", imported)
	}
	manifest := map[string]any{
		"apiVersion": "workos.app/v1", "id": f.App, "name": "P3 fixture", "version": version, "scope": "project",
		"runtime":  map[string]any{"type": "container", "image": p3Image, "command": []string{"/app/server"}, "port": 8080, "artifact": map[string]any{"id": artifactID, "digest": stats.Digest, "format": "app-bundle.v1"}},
		"surfaces": []any{map[string]any{"id": "main", "renderer": "web-service", "route": "/"}}, "permissions": []string{},
		"resources": map[string]any{"cpuHard": 1, "memoryHighMb": 64, "memoryMaxMb": 128, "pidsMax": 64},
		"health":    map[string]any{"httpPath": "/health", "startupSeconds": 3, "restartLimit": 0}, "maintainer": map[string]any{},
		"build": map[string]any{"sourceBundleId": source.Msg.GetBundle().GetId(), "sourceDigest": source.Msg.GetBundle().GetDigest(), "baseImage": p3Image, "buildCommand": []string{"sh", "-c", "mkdir -p dist && CGO_ENABLED=0 go build -trimpath -o dist/server ."}, "testCommand": []string{"go", "test", "./..."}, "output": map[string]any{"directory": "dist", "format": "app-bundle.v1"}},
	}
	raw, _ := json.Marshal(manifest)
	if _, err := clients.registry.RegisterApp(context.Background(), connect.NewRequest(&appv1.RegisterAppRequest{IdempotencyKey: key + "-register", ManifestYaml: raw})); err != nil {
		t.Fatalf("register variant %s: %v", version, err)
	}
	return stats.Digest
}

// p3WaitIncidentState waits for one incident's ledger row to reach any of the
// accepted states; per-incident waits keep multi-incident serialization
// assertions independent of which row the aggregate query sees first.
func p3WaitIncidentState(t *testing.T, installation, incident string, accept []string, description string) string {
	t.Helper()
	deadline := time.Now().Add(4 * time.Minute)
	var last string
	for time.Now().Before(deadline) {
		rows := buildtestQuery(t, `SELECT state FROM workos_reliability.deployment_ledger WHERE installation_id=$1 AND incident_id=$2`, installation, incident)
		if len(rows) == 1 {
			last = fmt.Sprint(rows[0]["state"])
			for _, want := range accept {
				if last == want {
					return last
				}
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("incident %s never reached %s (last=%s)", incident, description, last)
	return ""
}

func p3WaitRepairConsumed(t *testing.T, incident string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		rows := buildtestQuery(t, `SELECT state FROM workos_reliability.repair_ledger WHERE incident_id=$1`, incident)
		if len(rows) == 1 && fmt.Sprint(rows[0]["state"]) == "terminal" {
			return
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Fatal("repair did not reach its terminal hand-off")
}

func p3WaitLedgerState(t *testing.T, installation string, predicate func(state string) bool, description string) string {
	t.Helper()
	deadline := time.Now().Add(4 * time.Minute)
	var last string
	for time.Now().Before(deadline) {
		rows := buildtestQuery(t, `SELECT state FROM workos_reliability.deployment_ledger WHERE installation_id=$1`, installation)
		state := ""
		for _, row := range rows {
			if predicate(fmt.Sprint(row["state"])) {
				return fmt.Sprint(row["state"])
			}
			state = fmt.Sprint(row["state"])
		}
		last = state
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("deployment never reached %s (last=%s)", description, last)
	return ""
}

func p3ProjectRevision(t *testing.T, clients *buildtestClients, projectID string) int64 {
	t.Helper()
	project, err := clients.projects.GetProject(context.Background(), connect.NewRequest(&projectv1.GetProjectRequest{ProjectId: projectID}))
	if err != nil {
		t.Fatal(err)
	}
	return project.Msg.GetProject().GetRevision()
}

func p3RunningContainerCount(t *testing.T, installation string) int {
	t.Helper()
	namespace := os.Getenv("WORKOS_P3_GATE_NAMESPACE")
	filters, _ := json.Marshal(map[string][]string{
		"label": {
			"workos.purpose=runtime-app",
			"workos.runtime=" + namespace,
			"workos.workload.instance=" + installation,
		},
	})
	raw := p3DockerDo(t, "GET", "/containers/json?filters="+url.QueryEscape(string(filters)), nil)
	var items []struct {
		State string `json:"State"`
	}
	if err := json.Unmarshal(raw, &items); err != nil {
		t.Fatal(err)
	}
	running := 0
	for _, item := range items {
		if item.State == "running" {
			running++
		}
	}
	return running
}

// p3GatewaySurfaces builds a surface client through the gateway so every
// request carries the trusted owner identity the production path injects.
func p3GatewaySurfaces(t *testing.T, clients *buildtestClients) surfacev1connect.SurfaceServiceClient {
	t.Helper()
	return surfacev1connect.NewSurfaceServiceClient(clients.http, clients.gatewayURL)
}

func p3CreateSurfaceErr(t *testing.T, clients *buildtestClients, f p3Fixture) error {
	t.Helper()
	surfaces := p3GatewaySurfaces(t, clients)
	_, err := surfaces.CreateSurface(context.Background(), connect.NewRequest(&surfacev1.CreateSurfaceRequest{
		IdempotencyKey: ids.UUIDv7{}.New(), ProjectId: f.Project, AppInstanceId: f.Installation,
		DeviceClass: surfacev1.DeviceClass_DEVICE_CLASS_DESKTOP,
		Viewport:    &surfacev1.Viewport{Width: 1280, Height: 800, PixelRatio: 1},
	}))
	return err
}

// TestP3CloseoutMatrix covers the D05 authorization, corruption and rollback
// failure scenarios. Every user action goes through the public API; SQL only
// reads this gate's scratch database to assert durable facts.
func TestP3CloseoutMatrix(t *testing.T) {
	clients := newBuildtestClients(t)

	t.Run("F18_user_upgrade_before_registration", func(t *testing.T) {
		p3ResetFaults(t)
		f := p3SeedNamed(t, clients, "P3 closeout F18 late repair", false)
		p3Surface(t, clients, f, "P3-VALUE-0")
		p3RegisterVariant(t, clients, f, "1.2.0", 7)
		p3ArmWait(t, "before-verdict")
		incident := p3StartRepair(t, clients, f)
		p3WaitArrived(t, "before-verdict")
		if _, err := clients.install.TransitionAppVersion(context.Background(), connect.NewRequest(&appv1.TransitionAppVersionRequest{
			IdempotencyKey: ids.UUIDv7{}.New(), ProjectId: f.Project, InstallationId: f.Installation, Version: "1.2.0",
			ExpectedProjectRevision: p3ProjectRevision(t, clients, f.Project),
		})); err != nil {
			t.Fatal(err)
		}
		p3ResetFaults(t)
		p3WaitRepairConsumed(t, incident)
		if rows := buildtestQuery(t, `SELECT task_id FROM workos_core.app_repair_candidate_versions WHERE installation_id=$1`, f.Installation); len(rows) != 0 {
			t.Fatalf("late repair staged after owner change: %+v", rows)
		}
		if rows := buildtestQuery(t, `SELECT incident_id FROM workos_reliability.deployment_ledger WHERE installation_id=$1`, f.Installation); len(rows) != 0 {
			t.Fatalf("late repair deployed after owner change: %+v", rows)
		}
		pin := buildtestQuery(t, `SELECT version FROM workos_core.project_app_installations WHERE id=$1`, f.Installation)
		if len(pin) != 1 || fmt.Sprint(pin[0]["version"]) != "1.2.0" {
			t.Fatalf("owner choice overwritten: %+v", pin)
		}
		p3Surface(t, clients, f, "P3-VALUE-7")
	})

	t.Run("F18_user_upgrade_supersedes_canary", func(t *testing.T) {
		p3ResetFaults(t)
		f := p3SeedNamed(t, clients, "P3 closeout F18 upgrade", false)
		p3Surface(t, clients, f, "P3-VALUE-0")
		p3RegisterVariant(t, clients, f, "1.2.0", 7)
		p3StartRepair(t, clients, f)
		p3WaitLedgerState(t, f.Installation, func(state string) bool { return state == "canary" }, "canary")
		// The owner moves the installation to a third version mid-canary:
		// automation must report superseded, never fight the new pin.
		if _, err := clients.install.TransitionAppVersion(context.Background(), connect.NewRequest(&appv1.TransitionAppVersionRequest{
			IdempotencyKey: ids.UUIDv7{}.New(), ProjectId: f.Project, InstallationId: f.Installation,
			Version: "1.2.0", ExpectedProjectRevision: p3ProjectRevision(t, clients, f.Project),
		})); err != nil {
			t.Fatal(err)
		}
		p3WaitLedgerState(t, f.Installation, func(state string) bool { return state == "superseded" }, "superseded after user upgrade")
		pin := buildtestQuery(t, `SELECT version FROM workos_core.project_app_installations WHERE id=$1`, f.Installation)
		if len(pin) != 1 || fmt.Sprint(pin[0]["version"]) != "1.2.0" {
			t.Fatalf("automation overrode the user's pin: %+v", pin)
		}
		sources := p3VersionHistorySources(t, f.Installation)
		if sources["transition"] != 2 || sources["rollback"] != 0 {
			t.Fatalf("history drifted after supersede: %+v", sources)
		}
		p3Surface(t, clients, f, "P3-VALUE-7")
	})

	t.Run("F18_manual_rollback_supersedes_canary", func(t *testing.T) {
		p3ResetFaults(t)
		f := p3SeedNamed(t, clients, "P3 closeout F18 rollback", false)
		p3Surface(t, clients, f, "P3-VALUE-0")
		p3StartRepair(t, clients, f)
		p3WaitLedgerState(t, f.Installation, func(state string) bool { return state == "canary" }, "canary")
		if _, err := clients.install.RollbackAppVersion(context.Background(), connect.NewRequest(&appv1.RollbackAppVersionRequest{
			IdempotencyKey: ids.UUIDv7{}.New(), ProjectId: f.Project, InstallationId: f.Installation,
			ExpectedProjectRevision: p3ProjectRevision(t, clients, f.Project),
		})); err != nil {
			t.Fatal(err)
		}
		p3WaitLedgerState(t, f.Installation, func(state string) bool { return state == "superseded" }, "superseded after manual rollback")
		// The late automated flow must not roll the installation back to an
		// even older version: the only rollback in history is the owner's.
		sources := p3VersionHistorySources(t, f.Installation)
		if sources["rollback"] != 1 {
			t.Fatalf("automated rollback landed after supersede: %+v", sources)
		}
		pin := buildtestQuery(t, `SELECT version FROM workos_core.project_app_installations WHERE id=$1`, f.Installation)
		if len(pin) != 1 || fmt.Sprint(pin[0]["version"]) != "1.0.0" {
			t.Fatalf("pin did not stay on the owner's rollback target: %+v", pin)
		}
		p3Surface(t, clients, f, "P3-VALUE-0")
	})

	t.Run("F19_uninstall_mid_canary_no_revival", func(t *testing.T) {
		p3ResetFaults(t)
		f := p3SeedNamed(t, clients, "P3 closeout F19 uninstall", false)
		p3Surface(t, clients, f, "P3-VALUE-0")
		p3StartRepair(t, clients, f)
		p3WaitLedgerState(t, f.Installation, func(state string) bool { return state == "canary" }, "canary")
		if _, err := clients.install.UninstallApp(context.Background(), connect.NewRequest(&appv1.UninstallAppRequest{
			IdempotencyKey: ids.UUIDv7{}.New(), ProjectId: f.Project, InstallationId: f.Installation,
			ExpectedProjectRevision: p3ProjectRevision(t, clients, f.Project),
		})); err != nil {
			t.Fatal(err)
		}
		// The old flow keeps replaying: it must stay bounded and terminal
		// without resurrecting the installation or rolling anything back.
		state := p3WaitLedgerState(t, f.Installation, func(state string) bool {
			return state == "failed"
		}, "bounded failure after uninstall")
		if state == "promoted" || state == "rolled_back" {
			t.Fatalf("uninstalled installation was revived: %s", state)
		}
		if err := p3CreateSurfaceErr(t, clients, f); err == nil {
			t.Fatal("surface creation must refuse an uninstalled app")
		}
		installed, err := clients.install.ListInstalledApps(context.Background(), connect.NewRequest(&appv1.ListInstalledAppsRequest{ProjectId: f.Project}))
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range installed.Msg.GetInstallations() {
			if item.GetId() == f.Installation {
				t.Fatalf("uninstalled installation still listed: %+v", item)
			}
		}
		if sources := p3VersionHistorySources(t, f.Installation); sources["rollback"] != 0 {
			t.Fatalf("automation rolled back an uninstalled app: %+v", sources)
		}
		// Runtime reconciliation retires the workload of the gone installation.
		deadline := time.Now().Add(90 * time.Second)
		for p3RunningContainerCount(t, f.Installation) != 0 && time.Now().Before(deadline) {
			time.Sleep(time.Second)
		}
		if running := p3RunningContainerCount(t, f.Installation); running != 0 {
			t.Fatalf("workload of the uninstalled installation kept running: %d", running)
		}
	})

	t.Run("F19_archive_mid_canary_no_revival", func(t *testing.T) {
		p3ResetFaults(t)
		f := p3SeedNamed(t, clients, "P3 closeout F19 archive", false)
		p3Surface(t, clients, f, "P3-VALUE-0")
		p3StartRepair(t, clients, f)
		p3WaitLedgerState(t, f.Installation, func(state string) bool { return state == "canary" }, "canary")
		if _, err := clients.projects.ArchiveProject(context.Background(), connect.NewRequest(&projectv1.ArchiveProjectRequest{
			ProjectId: f.Project, ExpectedRevision: p3ProjectRevision(t, clients, f.Project),
		})); err != nil {
			t.Fatal(err)
		}
		p3WaitLedgerState(t, f.Installation, func(state string) bool {
			return state == "failed" || state == "rollback_pending"
		}, "bounded state after archive")
		if err := p3CreateSurfaceErr(t, clients, f); err == nil {
			t.Fatal("surface creation must refuse an archived project")
		}
		if sources := p3VersionHistorySources(t, f.Installation); sources["rollback"] != 0 {
			t.Fatalf("automation mutated an archived project: %+v", sources)
		}
		promoted := buildtestQuery(t, `SELECT incident_id FROM workos_reliability.deployment_ledger WHERE installation_id=$1 AND state='promoted'`, f.Installation)
		if len(promoted) != 0 {
			t.Fatalf("archived project was published: %+v", promoted)
		}
	})

	t.Run("F19_grant_epoch_revokes_old_bridge_and_regrant_uses_fresh_surface", func(t *testing.T) {
		p3ResetFaults(t)
		f := p3SeedNamedWithPermissions(t, clients, "P3 closeout grants", false, []string{"project.read"})
		surfaces := p3GatewaySurfaces(t, clients)
		create := func(key string) *surfacev1.SurfaceSession {
			t.Helper()
			response, err := surfaces.CreateSurface(context.Background(), connect.NewRequest(&surfacev1.CreateSurfaceRequest{
				IdempotencyKey: key, ProjectId: f.Project, AppInstanceId: f.Installation,
				DeviceClass: surfacev1.DeviceClass_DEVICE_CLASS_DESKTOP,
				Viewport:    &surfacev1.Viewport{Width: 1280, Height: 800, PixelRatio: 1},
			}))
			if err != nil {
				t.Fatalf("create grant-bound surface: %v", err)
			}
			return response.Msg.GetSession()
		}
		bridge := bridgev1connect.NewAppBridgeServiceClient(clients.http, clients.runtimeURL)
		authorize := func(token, deviceID string) error {
			t.Helper()
			request := connect.NewRequest(&bridgev1.AuthorizeShellActionRequest{Method: "project.current"})
			request.Header().Set("X-WorkOS-Bridge-Token", token)
			request.Header().Set(identity.UserHeader, p3Owner)
			request.Header().Set(identity.DeviceHeader, deviceID)
			_, err := bridge.AuthorizeShellAction(context.Background(), request)
			return err
		}
		deviceFor := func(session *surfacev1.SurfaceSession) string {
			t.Helper()
			rows := buildtestQuery(t, `SELECT device_id FROM workos_runtime.surface_sessions WHERE id=$1`, session.GetId())
			if len(rows) != 1 {
				t.Fatalf("surface session device identity missing: %s => %+v", session.GetId(), rows)
			}
			return fmt.Sprint(rows[0]["device_id"])
		}
		oldKey := ids.UUIDv7{}.New()
		oldSession := create(oldKey)
		oldDevice := deviceFor(oldSession)
		if err := authorize(oldSession.GetBridgeToken(), oldDevice); err != nil {
			t.Fatalf("initial project.read grant was not usable: %v", err)
		}

		empty, err := clients.install.SetAppGrants(context.Background(), connect.NewRequest(&appv1.SetAppGrantsRequest{
			IdempotencyKey: ids.UUIDv7{}.New(), ProjectId: f.Project, InstallationId: f.Installation,
			ExpectedProjectRevision: p3ProjectRevision(t, clients, f.Project),
		}))
		if err != nil || empty.Msg.GetInstallation().GetGrantRevision() != 2 || len(empty.Msg.GetInstallation().GetGrantedPermissions()) != 0 {
			t.Fatalf("grant revoke did not advance epoch to 2: response=%v err=%v", empty, err)
		}
		if err := authorize(oldSession.GetBridgeToken(), oldDevice); connect.CodeOf(err) != connect.CodePermissionDenied {
			t.Fatalf("revoked old bridge must be PermissionDenied, got %v", err)
		}
		if _, err := surfaces.CreateSurface(context.Background(), connect.NewRequest(&surfacev1.CreateSurfaceRequest{
			IdempotencyKey: oldKey, ProjectId: f.Project, AppInstanceId: f.Installation,
			DeviceClass: surfacev1.DeviceClass_DEVICE_CLASS_DESKTOP,
			Viewport:    &surfacev1.Viewport{Width: 1280, Height: 800, PixelRatio: 1},
		})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
			t.Fatalf("old surface create key must not mint a post-revocation token, got %v", err)
		}

		regranted, err := clients.install.SetAppGrants(context.Background(), connect.NewRequest(&appv1.SetAppGrantsRequest{
			IdempotencyKey: ids.UUIDv7{}.New(), ProjectId: f.Project, InstallationId: f.Installation,
			ExpectedProjectRevision: p3ProjectRevision(t, clients, f.Project), GrantedPermissions: []string{"project.read"},
		}))
		if err != nil || regranted.Msg.GetInstallation().GetGrantRevision() != 3 {
			t.Fatalf("regrant did not advance epoch to 3: response=%v err=%v", regranted, err)
		}
		if err := authorize(oldSession.GetBridgeToken(), oldDevice); connect.CodeOf(err) != connect.CodePermissionDenied {
			t.Fatalf("old token must remain stale after regrant, got %v", err)
		}
		fresh := create(ids.UUIDv7{}.New())
		freshDevice := deviceFor(fresh)
		if got := fresh.GetBridgeCapabilities(); len(got) != 1 || got[0] != "project.current" {
			t.Fatalf("fresh surface did not receive the exact regranted capability: %v", got)
		}
		if err := authorize(fresh.GetBridgeToken(), freshDevice); err != nil {
			t.Fatalf("new surface could not use regranted capability: %v", err)
		}
		p3Surface(t, clients, f, "P3-VALUE-0")
	})

	t.Run("F20_second_incident_serializes_behind_active_deployment", func(t *testing.T) {
		p3ResetFaults(t)
		f := p3SeedNamed(t, clients, "P3 closeout F20 serialize", false)
		p3Surface(t, clients, f, "P3-VALUE-0")
		first := p3StartRepair(t, clients, f)
		p3WaitLedgerState(t, f.Installation, func(state string) bool { return state == "canary" }, "first canary")
		// Freeze the first canary while a real private Core admission captures
		// B. The incident is then delivered to Reliability before releasing
		// the canary, making the superseded target deterministic.
		p3ArmWait(t, "deployment-canary")
		p3WaitArrived(t, "deployment-canary")
		second := ids.UUIDv7{}.New()
		submitter := reliabilitytransport.NewRepairSubmitterClient(clients.coreURL, "01999999-9999-7999-8999-000000000b02")
		secondTask, _, err := submitter.SubmitRepair(context.Background(), p3Owner, f.Project, f.Installation, second, "repair-"+second, "P3 deterministic baseline regression")
		if err != nil {
			t.Fatal(err)
		}
		p3InsertIncident(t, f, second)
		p3ResetFaults(t)
		if state := p3WaitIncidentState(t, f.Installation, first, []string{"rolled_back", "failed", "superseded"}, "older canary preempted"); state != "rolled_back" {
			t.Fatalf("older incident ended without a bounded rollback: %s", state)
		}
		p3WaitRepairConsumed(t, second)
		if rows := buildtestQuery(t, `SELECT task_id FROM workos_core.app_repair_candidate_versions WHERE task_id=$1`, secondTask); len(rows) != 0 {
			t.Fatalf("repair of retired B was rebased onto A: %+v", rows)
		}
		if rows := buildtestQuery(t, `SELECT incident_id FROM workos_reliability.deployment_ledger WHERE incident_id=$1`, second); len(rows) != 0 {
			t.Fatalf("superseded repair created a deployment: %+v", rows)
		}
		p3Surface(t, clients, f, "P3-VALUE-0")
		// A fresh repair of the restored A may proceed after the prior row
		// is terminal. This proves the refusal does not wedge the installation.
		third := p3StartRepair(t, clients, f)
		p3WaitIncidentState(t, f.Installation, third, []string{"promoted"}, "fresh repair promoted")
		rows := buildtestQuery(t, `SELECT incident_id::text, state FROM workos_reliability.deployment_ledger WHERE installation_id=$1`, f.Installation)
		if len(rows) != 2 {
			t.Fatalf("repeated offers duplicated ledger rows: %+v", rows)
		}
		for _, row := range rows {
			want := "rolled_back"
			if fmt.Sprint(row["incident_id"]) == third {
				want = "promoted"
			}
			if fmt.Sprint(row["state"]) != want {
				t.Fatalf("unexpected serialized outcome: %+v", rows)
			}
		}
		p3Release(t, clients, f, "published")
		p3Surface(t, clients, f, "P3-VALUE-42")
	})

	t.Run("F20_second_installation_publishes_independently", func(t *testing.T) {
		p3ResetFaults(t)
		first := p3SeedNamed(t, clients, "P3 closeout F20 parallel one", false)
		p3Surface(t, clients, first, "P3-VALUE-0")
		p3StartRepair(t, clients, first)
		// A second installation publishes while the first is mid-chain; the
		// build engine serializes jobs, the deployments must not interfere.
		second := p3SeedNamed(t, clients, "P3 closeout F20 parallel two", false)
		p3Surface(t, clients, second, "P3-VALUE-0")
		p3StartRepair(t, clients, second)
		firstStatus := p3Release(t, clients, first, "published")
		secondStatus := p3Release(t, clients, second, "published")
		if firstStatus.GetCandidateVersion() == secondStatus.GetCandidateVersion() {
			t.Fatalf("independent installations shared a candidate version: %+v", secondStatus)
		}
		p3Surface(t, clients, first, "P3-VALUE-42")
		p3Surface(t, clients, second, "P3-VALUE-42")
	})

	t.Run("F21_missing_base_image_fails_closed", func(t *testing.T) {
		p3ResetFaults(t)
		f := p3SeedNamed(t, clients, "P3 closeout F21 image", false)
		taskID := p3FailingJob(t, clients, f, "missing-image")
		verdict := waitForBuildtestJob(t, clients, taskID, func(state string) bool {
			return state == "failed" || state == "cancelled"
		})
		if verdict.State != "failed" {
			t.Fatalf("absent base image must fail the build: %+v", verdict)
		}
		p3NoPublishSideEffects(t, f.Installation, "1.0.0")
		p3Surface(t, clients, f, "P3-VALUE-0")
	})

	t.Run("F21_tampered_package_never_launches", func(t *testing.T) {
		p3ResetFaults(t)
		f := p3SeedNamed(t, clients, "P3 closeout F21 tamper A", false)
		bundlePath := p3ArtifactBundlePath(t, p3Owner, f.Digest)
		original, err := os.ReadFile(bundlePath)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.WriteFile(bundlePath, original, 0o600) })
		if err := os.WriteFile(bundlePath, append(append([]byte{}, original...), 0), 0o600); err != nil {
			t.Fatal(err)
		}
		// No amount of retries may launch stale or wrong bytes.
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			if err := p3CreateSurfaceErr(t, clients, f); err == nil {
				t.Fatal("tampered package launched a surface")
			}
			time.Sleep(time.Second)
		}
		if running := p3RunningContainerCount(t, f.Installation); running != 0 {
			t.Fatalf("tampered package is running: %d", running)
		}
		// Restoring the bytes recovers through the honest verified path.
		if err := os.WriteFile(bundlePath, original, 0o600); err != nil {
			t.Fatal(err)
		}
		p3Surface(t, clients, f, "P3-VALUE-0")
	})

	t.Run("F21_canary_package_drift_stops_promotion", func(t *testing.T) {
		p3ResetFaults(t)
		f := p3SeedNamed(t, clients, "P3 closeout F21 drift", false)
		p3Surface(t, clients, f, "P3-VALUE-0")
		p3StartRepair(t, clients, f)
		var candidateDigest string
		p3Poll(t, "canary with candidate identity", func() bool {
			rows := buildtestQuery(t, `SELECT artifact_digest FROM workos_reliability.deployment_ledger WHERE installation_id=$1 AND state='canary' AND artifact_digest IS NOT NULL`, f.Installation)
			if len(rows) != 1 {
				return false
			}
			candidateDigest = fmt.Sprint(rows[0]["artifact_digest"])
			return candidateDigest != ""
		})
		candidatePath := p3ArtifactBundlePath(t, p3Owner, candidateDigest)
		original, err := os.ReadFile(candidatePath)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.WriteFile(candidatePath, original, 0o600) })
		if err := os.WriteFile(candidatePath, append(append([]byte{}, original...), 1), 0o600); err != nil {
			t.Fatal(err)
		}
		// The drifted candidate dies for real: promotion must stop and roll
		// back to A's intact bytes, never relaunch B from the tampered file.
		p3KillOwnedApp(t, f.Installation, candidateDigest)
		p3Release(t, clients, f, "rolled_back")
		p3Surface(t, clients, f, "P3-VALUE-0")
		if promoted := buildtestQuery(t, `SELECT incident_id FROM workos_reliability.deployment_ledger WHERE installation_id=$1 AND state='promoted'`, f.Installation); len(promoted) != 0 {
			t.Fatalf("drifted candidate was promoted: %+v", promoted)
		}
	})

	t.Run("F22_rollback_with_missing_A_package_stays_honest", func(t *testing.T) {
		p3ResetFaults(t)
		f := p3SeedNamed(t, clients, "P3 closeout F22 missing A", true)
		p3Surface(t, clients, f, "P3-VALUE-0")
		bundlePath := p3ArtifactBundlePath(t, p3Owner, f.Digest)
		original, err := os.ReadFile(bundlePath)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.WriteFile(bundlePath, original, 0o600) })
		if err := os.Remove(bundlePath); err != nil {
			t.Fatal(err)
		}
		p3StartRepair(t, clients, f)
		// B fails to start and the rollback cannot prove A healthy: the
		// ledger must stay in its honest bounded states, and rolled_back is
		// reserved for a verified restoration.
		state := p3WaitLedgerState(t, f.Installation, func(state string) bool {
			return state == "failed" || state == "rollback_pending"
		}, "bounded rollback failure with A missing")
		if state == "rolled_back" {
			t.Fatalf("rolled_back was written without a running A: %s", state)
		}
		if err := p3CreateSurfaceErr(t, clients, f); err == nil {
			t.Fatal("surface served an app whose package is gone")
		}
		pin := buildtestQuery(t, `SELECT version FROM workos_core.project_app_installations WHERE id=$1`, f.Installation)
		if len(pin) != 1 {
			t.Fatalf("installation vanished: %+v", pin)
		}
	})

	t.Run("F23_operator_streaming_rejects_malicious_bundle", func(t *testing.T) {
		p3ResetFaults(t)
		// Start from a genuinely valid bundle, then flip the first entry's
		// typeflag to fifo. The declared digest stays the file's real digest,
		// so the rejection can only come from the format decoder.
		source := t.TempDir()
		if err := os.WriteFile(filepath.Join(source, "server"), []byte("#!/bin/sh\nexec /app/server\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		var bundle bytes.Buffer
		if _, err := appbundle.EncodeDirectory(source, &bundle); err != nil {
			t.Fatal(err)
		}
		malicious := bundle.Bytes()
		malicious[156] = '6'
		digest := sha256.Sum256(malicious)
		maliciousPath := filepath.Join(t.TempDir(), "malicious.bundle")
		if err := os.WriteFile(maliciousPath, malicious, 0o600); err != nil {
			t.Fatal(err)
		}
		key := ids.UUIDv7{}.New()
		declared := "sha256:" + hex.EncodeToString(digest[:])
		dir := os.Getenv("WORKOS_P3_GATE_DIR")
		imported, err := exec.Command(filepath.Join(dir, "bin/workosctl"), "runtime", "import-artifact", "--socket", filepath.Join(dir, "run/artifact-admin.sock"), "--owner", p3Owner, "--app", "p3-malicious-fixture", "--key", key, "--digest", declared, "--bundle", maliciousPath).CombinedOutput()
		if err == nil {
			t.Fatalf("malicious bundle was imported: %s", imported)
		}
		if rows := buildtestQuery(t, `SELECT id FROM workos_runtime.artifacts WHERE idempotency_key=$1`, key); len(rows) != 0 {
			t.Fatalf("rejected import left metadata behind: %+v", rows)
		}
		if strings.Contains(string(imported), "artifact_id=") {
			t.Fatalf("rejected import printed an identity: %s", imported)
		}
	})

	t.Run("F23_owner_quota_counts_orphaned_disk_bytes", func(t *testing.T) {
		p3ResetFaults(t)
		path, digest := p3TestBundle(t)
		owner := ids.UUIDv7{}.New()
		ownerDir := filepath.Join(os.Getenv("WORKOS_P3_GATE_DIR"), "artifacts", owner)
		if err := os.MkdirAll(ownerDir, 0o700); err != nil {
			t.Fatal(err)
		}
		orphan := filepath.Join(ownerDir, "orphaned-after-commit.bundle")
		file, err := os.Create(orphan)
		if err != nil {
			t.Fatal(err)
		}
		if err := file.Truncate(artifactdomain.OwnerQuotaBytes); err != nil {
			_ = file.Close()
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(ownerDir) })
		key := ids.UUIDv7{}.New()
		if output, err := p3ImportArtifact(t, owner, "p3-quota-fixture", key, path, digest); err == nil {
			t.Fatalf("owner at the real 2 GiB disk quota imported another bundle: %s", output)
		}
		if rows := buildtestQuery(t, `SELECT id FROM workos_runtime.artifacts WHERE owner_user_id=$1 AND idempotency_key=$2`, owner, key); len(rows) != 0 {
			t.Fatalf("quota-refused import left artifact metadata: %+v", rows)
		}
	})

	t.Run("F24_registry_rejects_foreign_owner_and_wrong_app_artifacts", func(t *testing.T) {
		p3ResetFaults(t)
		f := p3SeedNamed(t, clients, "P3 closeout F24 artifact provenance", false)
		rows := buildtestQuery(t, `SELECT canonical_manifest FROM workos_core.app_versions WHERE owner_user_id=$1 AND app_id=$2 AND version='1.0.0'`, p3Owner, f.App)
		if len(rows) != 1 {
			t.Fatalf("fixture canonical manifest missing: %+v", rows)
		}
		base, err := json.Marshal(rows[0]["canonical_manifest"])
		if err != nil {
			t.Fatalf("encode canonical manifest fixture: %v", err)
		}
		registerVariant := func(version, artifactID, digest string) error {
			t.Helper()
			var manifest map[string]any
			if err := json.Unmarshal(base, &manifest); err != nil {
				t.Fatal(err)
			}
			manifest["version"] = version
			runtime, ok := manifest["runtime"].(map[string]any)
			if !ok {
				t.Fatalf("fixture runtime is malformed: %#v", manifest["runtime"])
			}
			artifact, ok := runtime["artifact"].(map[string]any)
			if !ok {
				t.Fatalf("fixture artifact is malformed: %#v", runtime["artifact"])
			}
			artifact["id"], artifact["digest"] = artifactID, digest
			raw, err := json.Marshal(manifest)
			if err != nil {
				t.Fatal(err)
			}
			_, err = clients.registry.RegisterApp(context.Background(), connect.NewRequest(&appv1.RegisterAppRequest{
				IdempotencyKey: ids.UUIDv7{}.New(), ManifestYaml: raw,
			}))
			return err
		}

		bundlePath, digest := p3TestBundle(t)
		foreignOwner := ids.UUIDv7{}.New()
		foreignID, err := p3ImportArtifact(t, foreignOwner, f.App, ids.UUIDv7{}.New(), bundlePath, digest)
		if err != nil {
			t.Fatalf("seed foreign-owner artifact: %v", err)
		}
		if err := registerVariant("1.0.1", foreignID, digest); err == nil {
			t.Fatal("Core registered an artifact owned by a different user")
		}
		if rows := buildtestQuery(t, `SELECT id FROM workos_core.app_versions WHERE owner_user_id=$1 AND app_id=$2 AND version='1.0.1'`, p3Owner, f.App); len(rows) != 0 {
			t.Fatalf("foreign artifact registration left an app version: %+v", rows)
		}

		wrongAppID, err := p3ImportArtifact(t, p3Owner, "p3-not-the-fixture-app", ids.UUIDv7{}.New(), bundlePath, digest)
		if err != nil {
			t.Fatalf("seed wrong-app artifact: %v", err)
		}
		if err := registerVariant("1.0.2", wrongAppID, digest); err == nil {
			t.Fatal("Core registered a ready artifact whose app identity does not match the manifest")
		}
		if rows := buildtestQuery(t, `SELECT id FROM workos_core.app_versions WHERE owner_user_id=$1 AND app_id=$2 AND version='1.0.2'`, p3Owner, f.App); len(rows) != 0 {
			t.Fatalf("wrong-app artifact registration left an app version: %+v", rows)
		}
	})

	t.Run("F25_late_surface_receipt_cannot_touch_new_generation", func(t *testing.T) {
		p3ResetFaults(t)
		f := p3SeedNamed(t, clients, "P3 closeout F25 late receipt", false)
		p3Surface(t, clients, f, "P3-VALUE-0")
		p3StartRepair(t, clients, f)
		p3Release(t, clients, f, "published")
		// One live B session exists before the owner rolls back.
		surfaces := p3GatewaySurfaces(t, clients)
		session, err := surfaces.CreateSurface(context.Background(), connect.NewRequest(&surfacev1.CreateSurfaceRequest{
			IdempotencyKey: ids.UUIDv7{}.New(), ProjectId: f.Project, AppInstanceId: f.Installation,
			DeviceClass: surfacev1.DeviceClass_DEVICE_CLASS_DESKTOP,
			Viewport:    &surfacev1.Viewport{Width: 1280, Height: 800, PixelRatio: 1},
		}))
		if err != nil {
			t.Fatal(err)
		}
		oldSessionID := session.Msg.GetSession().GetId()
		if _, err := clients.install.RollbackAppVersion(context.Background(), connect.NewRequest(&appv1.RollbackAppVersionRequest{
			IdempotencyKey: ids.UUIDv7{}.New(), ProjectId: f.Project, InstallationId: f.Installation,
			ExpectedProjectRevision: p3ProjectRevision(t, clients, f.Project),
		})); err != nil {
			t.Fatal(err)
		}
		p3Surface(t, clients, f, "P3-VALUE-0")
		if count := p3OwnedContainerCount(t, f.Installation); count != 1 {
			t.Fatalf("rollback must clean the retired B container while retaining exactly A: got %d containers", count)
		}
		// The stale B session's late receipt must neither stop nor downgrade
		// the running A workload.
		_, _ = surfaces.CloseSurface(context.Background(), connect.NewRequest(&surfacev1.CloseSurfaceRequest{SurfaceSessionId: oldSessionID}))
		p3Surface(t, clients, f, "P3-VALUE-0")
		if count := p3OwnedContainerCount(t, f.Installation); count != 1 {
			t.Fatalf("late stale-session close created or retained an extra generation: got %d containers", count)
		}
		rows := buildtestQuery(t, `SELECT generation, artifact_digest FROM workos_runtime.workloads WHERE app_instance_id=$1 AND state='running'`, f.Installation)
		if len(rows) != 1 || fmt.Sprint(rows[0]["artifact_digest"]) != f.Digest {
			t.Fatalf("late old-session receipt disturbed the new workload: %+v", rows)
		}
	})
}
