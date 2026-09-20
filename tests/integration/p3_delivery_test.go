//go:build integration && repairbuildtest && p3delivery

package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	appv1 "github.com/yangtao121/workos/gen/go/workos/app/v1"
	projectv1 "github.com/yangtao121/workos/gen/go/workos/project/v1"
	releasev1 "github.com/yangtao121/workos/gen/go/workos/release/v1"
	"github.com/yangtao121/workos/gen/go/workos/release/v1/releasev1connect"
	surfacev1 "github.com/yangtao121/workos/gen/go/workos/surface/v1"
	"github.com/yangtao121/workos/gen/go/workos/surface/v1/surfacev1connect"
	executionv1 "github.com/yangtao121/workos/gen/go/workos/taskexecution/v1"
	"github.com/yangtao121/workos/internal/platform/appbundle"
	"github.com/yangtao121/workos/internal/platform/ids"
)

const p3Image = "golang@sha256:e8c859f5632dcfde7b32d2012b4351728f6437930887c2f6a91ea242459e5514"
const p3Owner = "01999999-9999-7999-8999-000000000c01"

type p3Fixture struct {
	CandidateVersion string
	Project          string
	ProjectName      string
	Installation     string
	App              string
	ArtifactID       string
	Digest           string
}

func p3Poll(t *testing.T, description string, predicate func() bool) {
	t.Helper()
	// Repair chains can queue behind idempotent duplicate-repair builds on
	// the single-flight engine, so release/deployment waits need slack.
	deadline := time.Now().Add(6 * time.Minute)
	for time.Now().Before(deadline) {
		if predicate() {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("timed out: %s", description)
}
func p3Seed(t *testing.T, clients *buildtestClients, startupFailure bool) p3Fixture {
	t.Helper()
	return p3SeedNamed(t, clients, "P3 deterministic delivery", startupFailure)
}

func p3SeedNamed(t *testing.T, clients *buildtestClients, projectName string, startupFailure bool) p3Fixture {
	return p3SeedNamedWithPermissions(t, clients, projectName, startupFailure, nil)
}

func p3SeedNamedWithPermissions(t *testing.T, clients *buildtestClients, projectName string, startupFailure bool, permissions []string) p3Fixture {
	return p3SeedProfile(t, clients, projectName, startupFailure, permissions, []string{"go", "test", "./..."})
}

func p3SeedProfile(t *testing.T, clients *buildtestClients, projectName string, startupFailure bool, permissions, testCommand []string) p3Fixture {
	return p3SeedProfileWithRestart(t, clients, projectName, startupFailure, permissions, testCommand, 0)
}

func p3SeedProfileWithRestart(t *testing.T, clients *buildtestClients, projectName string, startupFailure bool, permissions, testCommand []string, restartLimit int) p3Fixture {
	t.Helper()
	if permissions == nil {
		permissions = []string{}
	}
	ctx := context.Background()
	key := ids.UUIDv7{}.New()
	appID := "p3-" + strings.ReplaceAll(key, "-", "")
	project, err := clients.projects.CreateProject(ctx, connect.NewRequest(&projectv1.CreateProjectRequest{
		IdempotencyKey: key, Name: projectName, HarnessBinding: &projectv1.HarnessBinding{ProviderId: "generic-cli", InstancePolicy: projectv1.HarnessInstancePolicy_HARNESS_INSTANCE_POLICY_EPHEMERAL, ResourcePolicyId: "project-no-tools"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	crash := ""
	if startupFailure {
		crash = "if value()==42 { os.Exit(42) };"
	}
	mainSource := `package main
import("fmt";"net/http";"os")
func value() int { return 0 }
func main() { _ = os.Args; ` + crash + `
http.HandleFunc("/health",func(w http.ResponseWriter,r *http.Request){w.Header().Set("X-P3-Fixture","` + appID + `"); fmt.Fprint(w,"ok")})
http.HandleFunc("/",func(w http.ResponseWriter,r *http.Request){w.Header().Set("Content-Type","text/html; charset=utf-8"); fmt.Fprintf(w,"<html><body>P3-VALUE-%d</body></html>",value())})
if err:=http.ListenAndServe(":8080",nil); err!=nil { os.Exit(1) }
}`
	files := []*appv1.AppSourceFile{
		{Path: "go.mod", Content: []byte("module fixture\n\ngo 1.26\n")},
		{Path: "main.go", Content: []byte(mainSource)},
		{Path: "main_test.go", Content: []byte("package main\nimport \"testing\"\nfunc TestValue(t *testing.T){if value()!=42{t.Fatal(\"expected repair\")}}\n")},
	}
	source, err := clients.sources.CreateAppSourceBundle(ctx, connect.NewRequest(&appv1.CreateAppSourceBundleRequest{IdempotencyKey: key + "-source", Files: files}))
	if err != nil {
		t.Fatal(err)
	}
	// Build baseline A independently. Only the repair candidate is required to
	// pass the pinned test command; the broken baseline is an explicit import.
	src := t.TempDir()
	output := t.TempDir()
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(src, f.Path), f.Content, 0600); err != nil {
			t.Fatal(err)
		}
	}
	build := exec.Command("go", "build", "-trimpath", "-o", filepath.Join(output, "server"), ".")
	build.Dir = src
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOPROXY=off", "GOMAXPROCS=2")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("baseline build: %v %s", err, out)
	}
	var bundle bytes.Buffer
	stats, err := appbundle.EncodeDirectory(output, &bundle)
	if err != nil {
		t.Fatal(err)
	}
	bundlePath := filepath.Join(t.TempDir(), "baseline.bundle")
	if err := os.WriteFile(bundlePath, bundle.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	dir := os.Getenv("WORKOS_P3_GATE_DIR")
	command := exec.Command(filepath.Join(dir, "bin/workosctl"), "runtime", "import-artifact", "--socket", filepath.Join(dir, "run/artifact-admin.sock"), "--owner", p3Owner, "--app", appID, "--key", key, "--digest", stats.Digest, "--bundle", bundlePath)
	imported, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("real streaming import: %v %s", err, imported)
	}
	artifactID := ""
	for _, line := range strings.Split(string(imported), "\n") {
		if strings.HasPrefix(line, "artifact_id=") {
			artifactID = strings.TrimPrefix(line, "artifact_id=")
		}
	}
	if artifactID == "" {
		t.Fatalf("missing import identity: %s", imported)
	}
	manifest := map[string]any{
		"apiVersion": "workos.app/v1", "id": appID, "name": "P3 fixture", "version": "1.0.0", "scope": "project",
		"runtime":  map[string]any{"type": "container", "image": p3Image, "command": []string{"/app/server"}, "port": 8080, "artifact": map[string]any{"id": artifactID, "digest": stats.Digest, "format": "app-bundle.v1"}},
		"surfaces": []any{map[string]any{"id": "main", "renderer": "web-service", "route": "/"}}, "permissions": permissions,
		"resources": map[string]any{"cpuHard": 1, "memoryHighMb": 64, "memoryMaxMb": 128, "pidsMax": 64},
		"health":    map[string]any{"httpPath": "/health", "startupSeconds": 3, "restartLimit": restartLimit}, "maintainer": map[string]any{},
		"build": map[string]any{"sourceBundleId": source.Msg.GetBundle().GetId(), "sourceDigest": source.Msg.GetBundle().GetDigest(), "baseImage": p3Image, "buildCommand": []string{"sh", "-c", "mkdir -p dist && CGO_ENABLED=0 go build -trimpath -o dist/server ."}, "testCommand": testCommand, "output": map[string]any{"directory": "dist", "format": "app-bundle.v1"}},
	}
	raw, _ := json.Marshal(manifest)
	if _, err := clients.registry.RegisterApp(ctx, connect.NewRequest(&appv1.RegisterAppRequest{IdempotencyKey: key + "-register", ManifestYaml: raw})); err != nil {
		t.Fatalf("register imported A: %v", err)
	}
	installed, err := clients.install.InstallApp(ctx, connect.NewRequest(&appv1.InstallAppRequest{IdempotencyKey: key + "-install", ProjectId: project.Msg.GetProject().GetId(), AppId: appID, Version: "1.0.0", ExpectedProjectRevision: project.Msg.GetProject().GetRevision(), GrantedPermissions: permissions}))
	if err != nil {
		t.Fatal(err)
	}
	fixture := p3Fixture{Project: project.Msg.GetProject().GetId(), ProjectName: projectName, Installation: installed.Msg.GetInstallation().GetId(), App: appID, ArtifactID: artifactID, Digest: stats.Digest}
	t.Cleanup(func() { p3CleanupFixture(t, clients, fixture) })
	return fixture
}

// Each fault case retires its own installation through the public API. An
// intentionally broken candidate must not keep spawning repairs that consume
// later cases' build queue or global fault barriers. Only explicit browser/
// restart fixtures survive until the gate tears down its private namespace.
func p3CleanupFixture(t *testing.T, clients *buildtestClients, f p3Fixture) {
	t.Helper()
	for _, name := range []string{"replay.json", "closeout-f02.json"} {
		raw, err := os.ReadFile(filepath.Join(os.Getenv("WORKOS_P3_GATE_DIR"), name))
		var saved p3Fixture
		if err == nil && json.Unmarshal(raw, &saved) == nil && saved.Installation == f.Installation {
			return
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	// Retire through public commands; concurrent canary CAS may require a
	// fresh project revision. Never silently leave a broken fixture alive.
	for attempt := 0; attempt < 3; attempt++ {
		active := buildtestQuery(t, `SELECT i.id FROM workos_core.project_app_installations i JOIN workos_core.projects p ON p.id=i.project_id WHERE i.id=$1 AND i.uninstalled_at IS NULL AND p.archived_at IS NULL`, f.Installation)
		if len(active) == 0 {
			break
		}
		project, err := clients.projects.GetProject(ctx, connect.NewRequest(&projectv1.GetProjectRequest{ProjectId: f.Project}))
		if err != nil {
			t.Errorf("cleanup read project: %v", err)
			break
		}
		_, err = clients.install.UninstallApp(ctx, connect.NewRequest(&appv1.UninstallAppRequest{
			IdempotencyKey: fmt.Sprintf("cleanup-%s-%d", f.Installation, attempt), ProjectId: f.Project,
			InstallationId: f.Installation, ExpectedProjectRevision: project.Msg.GetProject().GetRevision(),
		}))
		if err == nil {
			break
		}
		if connect.CodeOf(err) != connect.CodeAborted && connect.CodeOf(err) != connect.CodeFailedPrecondition {
			t.Errorf("cleanup uninstall: %v", err)
			break
		}
	}
	if active := buildtestQuery(t, `SELECT i.id FROM workos_core.project_app_installations i JOIN workos_core.projects p ON p.id=i.project_id WHERE i.id=$1 AND i.uninstalled_at IS NULL AND p.archived_at IS NULL`, f.Installation); len(active) > 0 {
		t.Error("cleanup left the test installation active")
	}
	// A repair already admitted before uninstall can submit its build just
	// after the first query. Drain across several coordinator ticks so that
	// this retired fixture cannot consume the next case's worker or barrier.
	quietSince := time.Now()
	for ctx.Err() == nil {
		rows := buildtestQuery(t, `SELECT task_id::text FROM workos_runtime.build_jobs WHERE installation_id=$1 AND state IN ('queued','running')`, f.Installation)
		if len(rows) > 0 {
			quietSince = time.Now()
		}
		for _, row := range rows {
			if _, err := clients.builds.CancelBuildTest(ctx, connect.NewRequest(&executionv1.CancelBuildTestRequest{TaskId: fmt.Sprint(row["task_id"])})); err != nil {
				t.Errorf("cleanup cancel build: %v", err)
				return
			}
		}
		if time.Since(quietSince) >= 3*time.Second {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Error("cleanup build queue did not become quiet")
}

func p3Surface(t *testing.T, clients *buildtestClients, f p3Fixture, expected string) {
	t.Helper()
	surfaces := surfacev1connect.NewSurfaceServiceClient(clients.http, clients.gatewayURL)
	key := ids.UUIDv7{}.New()
	var last string
	p3Poll(t, "gateway serves "+expected, func() bool {
		response, err := surfaces.CreateSurface(context.Background(), connect.NewRequest(&surfacev1.CreateSurfaceRequest{IdempotencyKey: key, ProjectId: f.Project, AppInstanceId: f.Installation, DeviceClass: surfacev1.DeviceClass_DEVICE_CLASS_DESKTOP, Viewport: &surfacev1.Viewport{Width: 1280, Height: 800, PixelRatio: 1}}))
		if err != nil {
			last = err.Error()
			t.Logf("surface pending: %s", last)
			return false
		}
		reply, err := clients.http.Get(clients.gatewayURL + response.Msg.GetSession().GetUrl())
		if err != nil {
			return false
		}
		defer reply.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(reply.Body, 16384))
		last = fmt.Sprintf("%d %s", reply.StatusCode, body)
		if reply.StatusCode == 200 && strings.Contains(string(body), expected) {
			t.Logf("gateway Surface: %s", expected)
			return true
		}
		t.Logf("surface pending: %s", last)
		return false
	})
}

// p3StartRepair seeds an Incident row so the existing main chain can start
// without a live Docker crash. It does not prove Runtime observation created
// the Incident; TestP3Closeout covers that path.
func p3StartRepair(t *testing.T, clients *buildtestClients, f p3Fixture) string {
	t.Helper()
	incident := ids.UUIDv7{}.New()
	p3InsertIncident(t, f, incident)
	return incident
}

func p3InsertIncident(t *testing.T, f p3Fixture, incident string) {
	t.Helper()
	buildtestExec(t, `INSERT INTO workos_reliability.incidents
(id,owner_user_id,project_id,app_instance_id,app_id,workload_id,workload_generation,violation,severity,summary,occurrence_digest,evidence_digest,state,created_at,updated_at)
VALUES($1,$2,$3,$4,$5,$6,1,'unexpected_exit','critical','P3 deterministic baseline regression',$7,$8,'open',now(),now())`, incident, p3Owner, f.Project, f.Installation, f.App, ids.UUIDv7{}.New(), "sha256:"+strings.Repeat("e", 32)+strings.ReplaceAll(incident, "-", ""), "sha256:"+strings.Repeat("a", 64))
}
func p3Release(t *testing.T, clients *buildtestClients, f p3Fixture, expected string) *releasev1.ReleaseStatus {
	t.Helper()
	releases := releasev1connect.NewReleaseServiceClient(clients.http, clients.gatewayURL)
	var status *releasev1.ReleaseStatus
	last := ""
	p3Poll(t, "release "+expected, func() bool {
		response, err := releases.GetReleaseStatus(context.Background(), connect.NewRequest(&releasev1.GetReleaseStatusRequest{ProjectId: f.Project, InstallationId: f.Installation}))
		if err != nil {
			if err.Error() != last {
				last = err.Error()
				t.Log(last)
			}
			return false
		}
		status = response.Msg.GetStatus()
		if status.GetState() != last {
			last = status.GetState()
			t.Logf("release state: %s", last)
		}
		if last == "failed" || last == "superseded" {
			t.Fatalf("unexpected release verdict: %+v", status)
		}
		return last == expected
	})
	return status
}
func TestP3RealDelivery(t *testing.T) {
	clients := newBuildtestClients(t)
	t.Run("publish_and_manual_rollback", func(t *testing.T) {
		f := p3Seed(t, clients, false)
		p3Surface(t, clients, f, "P3-VALUE-0")
		p3StartRepair(t, clients, f)
		status := p3Release(t, clients, f, "published")
		f.CandidateVersion = status.GetCandidateVersion()
		if status.GetCandidateArtifactDigest() == "" || status.GetCandidateArtifactDigest() == f.Digest {
			t.Fatalf("candidate did not bind distinct B bytes: %+v", status)
		}
		p3Surface(t, clients, f, "P3-VALUE-42")
		rows := buildtestQuery(t, `SELECT d.workload_id::text, d.workload_generation, w.generation, w.artifact_digest FROM workos_reliability.deployment_ledger d JOIN workos_runtime.workloads w ON w.id=d.workload_id WHERE d.installation_id=$1 AND d.state='promoted'`, f.Installation)
		if len(rows) != 1 || rows[0]["workload_generation"] != rows[0]["generation"] || rows[0]["artifact_digest"] != status.GetCandidateArtifactDigest() {
			t.Fatalf("published generation does not bind B: %+v", rows)
		}
		t.Logf("verified B workload facts: %+v", rows[0])

		project, err := clients.projects.GetProject(context.Background(), connect.NewRequest(&projectv1.GetProjectRequest{ProjectId: f.Project}))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := clients.install.RollbackAppVersion(context.Background(), connect.NewRequest(&appv1.RollbackAppVersionRequest{IdempotencyKey: ids.UUIDv7{}.New(), ProjectId: f.Project, InstallationId: f.Installation, ExpectedProjectRevision: project.Msg.GetProject().GetRevision()})); err != nil {
			t.Fatal(err)
		}
		p3Surface(t, clients, f, "P3-VALUE-0")
		incidents := buildtestQuery(t, `SELECT id FROM workos_reliability.incidents WHERE app_instance_id=$1`, f.Installation)
		if len(incidents) != 1 {
			t.Fatalf("healthy version replacement created false incidents: %+v", incidents)
		}
		raw, _ := json.Marshal(f)
		if err := os.WriteFile(filepath.Join(os.Getenv("WORKOS_P3_GATE_DIR"), "replay.json"), raw, 0600); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("startup_failure_automatically_restores_A", func(t *testing.T) {
		f := p3Seed(t, clients, true)
		p3Surface(t, clients, f, "P3-VALUE-0")
		p3StartRepair(t, clients, f)
		p3Release(t, clients, f, "rolled_back")
		p3Surface(t, clients, f, "P3-VALUE-0")
	})
}
func TestP3RestartReplay(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(os.Getenv("WORKOS_P3_GATE_DIR"), "replay.json"))
	if err != nil {
		t.Fatal(err)
	}
	var f p3Fixture
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	clients := newBuildtestClients(t)
	p3Surface(t, clients, f, "P3-VALUE-0")
	response, err := clients.builds.GetBuildArtifact(context.Background(), connect.NewRequest(&executionv1.GetBuildArtifactRequest{ArtifactId: f.ArtifactID}))
	if err != nil {
		t.Fatal(err)
	}
	if response.Msg.GetArtifact().GetArtifactDigest() != f.Digest || response.Msg.GetArtifact().GetState() != "ready" {
		t.Fatalf("import changed after restart: %+v", response.Msg)
	}
}
