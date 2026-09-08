//go:build integration && repairdeployment

package integration_test

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"

	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
	"github.com/yangtao121/workos/gen/go/workos/agent/v1/agentv1connect"
	appv1 "github.com/yangtao121/workos/gen/go/workos/app/v1"
	"github.com/yangtao121/workos/gen/go/workos/app/v1/appv1connect"
	incidentv1 "github.com/yangtao121/workos/gen/go/workos/incident/v1"
	"github.com/yangtao121/workos/gen/go/workos/incident/v1/incidentv1connect"
	projectv1 "github.com/yangtao121/workos/gen/go/workos/project/v1"
	"github.com/yangtao121/workos/gen/go/workos/project/v1/projectv1connect"
	surfacev1 "github.com/yangtao121/workos/gen/go/workos/surface/v1"
	"github.com/yangtao121/workos/gen/go/workos/surface/v1/surfacev1connect"
)

// supervisionScenarioFile is the scenario file the supervision overlay
// bind-mounts into runtime-host.
const supervisionScenarioFile = "../../tmp/fake-engine-scenario.conf"

func setSupervisionScenario(t *testing.T, name, mode string) {
	t.Helper()
	content := fmt.Sprintf("# fake engine scenario (ADR-0016)\n%s=%s\n", name, mode)
	if err := os.WriteFile(supervisionScenarioFile, []byte(content), 0o644); err != nil {
		t.Fatalf("write scenario: %v", err)
	}
	t.Cleanup(func() {
		_ = os.WriteFile(supervisionScenarioFile, []byte("# fake engine scenario (ADR-0016)\n"), 0o644)
	})
}

// TestRepairOrchestratorTurnsIncidentIntoTask proves the ADR-0016 §5 chain
// across real processes: a fixture-engine workload incident (from the real
// supervision chain) is picked up by the repair orchestrator, submitted as
// an ordinary queued Agent task through the standard admission chain,
// completed by the harness, and the incident carries the repair task
// projection — idempotently across an orchestrator restart.
func TestRepairOrchestratorTurnsIncidentIntoTask(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	client := &http.Client{Transport: &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 2 * time.Second}).DialContext}}
	baseURL := "http://127.0.0.1:8080"
	projects := projectv1connect.NewProjectServiceClient(client, baseURL)
	registry := appv1connect.NewAppRegistryServiceClient(client, baseURL)
	installations := appv1connect.NewAppInstallationServiceClient(client, baseURL)
	surfaces := surfacev1connect.NewSurfaceServiceClient(client, baseURL)
	incidents := incidentv1connect.NewIncidentServiceClient(client, baseURL)
	tasks := agentv1connect.NewAgentTaskServiceClient(client, baseURL)

	key := fmt.Sprintf("repair-deploy-%d", time.Now().UnixNano())
	created, err := projects.CreateProject(ctx, connect.NewRequest(&projectv1.CreateProjectRequest{
		IdempotencyKey: key, Name: "Repair Deployment",
	}))
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	project := created.Msg.GetProject()

	appID := fmt.Sprintf("repair-fixture-%d", time.Now().UnixNano())
	// Since ADR-0025 every repair target needs a build recipe: the worker
	// resolves the pinned build input before running, and the fake provider
	// publishes an explicitly synthetic candidate against it.
	source, err := appv1connect.NewAppSourceBundleServiceClient(client, baseURL).CreateAppSourceBundle(ctx, connect.NewRequest(&appv1.CreateAppSourceBundleRequest{
		IdempotencyKey: key + "-source",
		Files: []*appv1.AppSourceFile{
			{Path: "go.mod", Content: []byte("module repair-fixture\n\ngo 1.26\n")},
			{Path: "main.go", Content: []byte("package main\nfunc answer() int { return 0 }\nfunc main() {}\n")},
			{Path: "main_test.go", Content: []byte("package main\nimport \"testing\"\nfunc TestAnswer(t *testing.T) { if answer() != 42 { t.Fatal(\"wrong answer\") } }\n")},
		},
	}))
	if err != nil {
		t.Fatalf("upload repair source: %v", err)
	}
	manifest := fmt.Sprintf(`apiVersion: workos.app/v1
id: %s
name: Repair Fixture
version: 1.0.0
scope: project
runtime:
  type: container
  image: localhost/workos-repair-fixture@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
  command: ["/workos-fixture", "serve"]
  port: 8080
surfaces:
  - id: main
    renderer: web-service
    route: /
permissions: [artifact.read]
resources:
  cpuHard: 1
  memoryHighMb: 64
  memoryMaxMb: 96
  pidsMax: 32
health:
  httpPath: /health
  startupSeconds: 10
  restartLimit: 1
build:
  sourceBundleId: %s
  sourceDigest: %s
  baseImage: localhost/toolchain@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
  buildCommand: ["go", "build", "./..."]
  testCommand: ["go", "test", "./..."]
maintainer: {}
`, appID, source.Msg.GetBundle().GetId(), source.Msg.GetBundle().GetDigest())
	if _, err := registry.RegisterApp(ctx, connect.NewRequest(&appv1.RegisterAppRequest{
		IdempotencyKey: key + "-register", ManifestYaml: []byte(manifest),
	})); err != nil {
		t.Fatalf("register container app: %v", err)
	}
	installed, err := installations.InstallApp(ctx, connect.NewRequest(&appv1.InstallAppRequest{
		IdempotencyKey: key + "-install", ProjectId: project.GetId(),
		AppId: appID, Version: "1.0.0", ExpectedProjectRevision: project.GetRevision(),
	}))
	if err != nil {
		t.Fatalf("install container app: %v", err)
	}
	// Gate hygiene: the canary workload outlives this test by design, but a
	// running fixture workload left in the shared store would poison later
	// gates' runtime observations (a podman-mode runtime cannot inspect a
	// fake-fixture cgroup). Remove the rows when the gate finishes.
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		admin, adminErr := pgx.Connect(cleanupCtx, scratchDatabaseURL())
		if adminErr != nil {
			return
		}
		defer admin.Close(cleanupCtx) //nolint:errcheck
		// FK-aware teardown: surface requests -> sessions -> operations ->
		// workload rows, so the canary workload can never blind a later
		// podman-mode runtime's whole-snapshot observation.
		_, _ = admin.Exec(cleanupCtx, `
DELETE FROM workos_runtime.surface_session_requests WHERE (owner_user_id, session_id) IN (
    SELECT owner_user_id, id FROM workos_runtime.surface_sessions
    WHERE workload_id IN (SELECT id FROM workos_runtime.workloads WHERE app_instance_id = $1));
DELETE FROM workos_runtime.surface_sessions WHERE workload_id IN (
    SELECT id FROM workos_runtime.workloads WHERE app_instance_id = $1);
DELETE FROM workos_runtime.workload_operations WHERE workload_id IN (
    SELECT id FROM workos_runtime.workloads WHERE app_instance_id = $1);
DELETE FROM workos_runtime.workloads WHERE app_instance_id = $1;`,
			installed.Msg.GetInstallation().GetId())
	})

	// Launch the fixture-engine workload.
	if _, err := surfaces.CreateSurface(ctx, connect.NewRequest(&surfacev1.CreateSurfaceRequest{
		IdempotencyKey: key + "-surface",
		AppInstanceId:  installed.Msg.GetInstallation().GetId(),
		ProjectId:      project.GetId(),
		DeviceClass:    surfacev1.DeviceClass_DEVICE_CLASS_DESKTOP,
		Viewport:       &surfacev1.Viewport{Width: 1280, Height: 800, PixelRatio: 2},
	})); err != nil {
		t.Fatalf("create surface: %v", err)
	}

	// Flip the fixture engine scenario to crash: the supervisor observes the
	// unexpected exit and applies the deterministic stop at the restart
	// limit (ADR-0016 §3), producing the incident the orchestrator repairs.
	setSupervisionScenario(t, "*", "crash")

	// The supervisor opens an incident and applies the deterministic stop at
	// the restart limit (proven by the real-supervision gate). Wait for the
	// incident, then for the orchestrator's repair task projection.
	var (
		incidentID   string
		repairTaskID string
	)
	deadline := time.Now().Add(120 * time.Second)
	for time.Now().Before(deadline) && incidentID == "" {
		listed, listErr := incidents.ListIncidents(ctx, connect.NewRequest(&incidentv1.ListIncidentsRequest{
			ProjectId: project.GetId(),
		}))
		if listErr == nil && len(listed.Msg.GetIncidents()) > 0 {
			incidentID = listed.Msg.GetIncidents()[0].GetId()
		}
		time.Sleep(500 * time.Millisecond)
	}
	if incidentID == "" {
		t.Fatal("supervisor did not open an incident for the crashing workload")
	}
	deadline = time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) && repairTaskID == "" {
		listed, listErr := incidents.GetIncident(ctx, connect.NewRequest(&incidentv1.GetIncidentRequest{
			IncidentId: incidentID,
		}))
		if listErr == nil && listed.Msg.GetIncident().GetRepairTaskId() != "" {
			repairTaskID = listed.Msg.GetIncident().GetRepairTaskId()
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if repairTaskID == "" {
		t.Fatal("repair orchestrator did not project a repair task onto the incident")
	}

	// The repair task is an ordinary Agent task: it must complete through
	// the standard chain (fake provider bound on the project).
	final, err := tasks.GetTask(ctx, connect.NewRequest(&agentv1.GetTaskRequest{TaskId: repairTaskID}))
	if err != nil {
		t.Fatalf("get repair task: %v", err)
	}
	if got := final.Msg.GetTask(); got.GetState() != agentv1.AgentTaskState_AGENT_TASK_STATE_COMPLETED {
		t.Fatalf("repair task not completed: %#v", got)
	}

	// Idempotency: the incident keeps exactly one repair task projection
	// across further orchestrator passes.
	time.Sleep(3 * time.Second)
	refetched, err := incidents.GetIncident(ctx, connect.NewRequest(&incidentv1.GetIncidentRequest{
		IncidentId: incidentID,
	}))
	if err != nil {
		t.Fatalf("re-read incident: %v", err)
	}
	if refetched.Msg.GetIncident().GetRepairTaskId() != repairTaskID {
		t.Fatalf("repair task projection drifted: %s -> %s",
			repairTaskID, refetched.Msg.GetIncident().GetRepairTaskId())
	}
}
