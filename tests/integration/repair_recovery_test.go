//go:build integration && repairbuildtest

package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	appv1 "github.com/yangtao121/workos/gen/go/workos/app/v1"
	incidentv1 "github.com/yangtao121/workos/gen/go/workos/incident/v1"
	projectv1 "github.com/yangtao121/workos/gen/go/workos/project/v1"
	"github.com/yangtao121/workos/internal/platform/ids"
)

// seedRecoveryFixture mirrors seedBuildtestFixture with an MCP harness
// binding: a healthy provider that honestly lacks repair source candidates,
// so repair admission must exercise the configured Recovery layer
// (gate compose: WORKOS_AGENT_RECOVERY_PROVIDER=generic-cli).
func seedRecoveryFixture(t *testing.T, clients *buildtestClients, suffix string) buildtestFixture {
	t.Helper()
	ctx := context.Background()
	key := fmt.Sprintf("recovery-%s-%d", suffix, time.Now().UnixNano())
	appID := strings.ReplaceAll(fmt.Sprintf("bt-recovery-%s-%d", suffix, time.Now().UnixNano()%100000), "_", "-")
	created, err := clients.projects.CreateProject(ctx, connect.NewRequest(&projectv1.CreateProjectRequest{
		IdempotencyKey: key, Name: "Recovery fallback " + suffix,
		HarnessBinding: &projectv1.HarnessBinding{ProviderId: "mcp", InstancePolicy: projectv1.HarnessInstancePolicy_HARNESS_INSTANCE_POLICY_EPHEMERAL, ResourcePolicyId: "project-no-tools"},
	}))
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	project := created.Msg.GetProject()
	files := buildtestSourceFiles("success")
	source, err := clients.sources.CreateAppSourceBundle(ctx, connect.NewRequest(&appv1.CreateAppSourceBundleRequest{IdempotencyKey: key + "-source", Files: files}))
	if err != nil {
		t.Fatalf("upload source: %v", err)
	}
	manifest := map[string]any{
		"apiVersion": "workos.app/v1", "id": appID, "name": "Recovery fallback fixture", "version": "1.0.0", "scope": "project",
		"runtime":     map[string]any{"type": "container", "image": "localhost/app@sha256:" + strings.Repeat("a", 64), "command": []string{"/app/app"}, "port": 8080},
		"surfaces":    []any{map[string]any{"id": "main", "renderer": "web-service", "route": "/"}},
		"permissions": []string{},
		"resources":   map[string]any{"cpuHard": 1, "memoryHighMb": 64, "memoryMaxMb": 96, "pidsMax": 32},
		"health":      map[string]any{"httpPath": "/health", "startupSeconds": 10, "restartLimit": 1}, "maintainer": map[string]any{},
		"build": map[string]any{
			"sourceBundleId": source.Msg.GetBundle().GetId(), "sourceDigest": source.Msg.GetBundle().GetDigest(),
			"baseImage":    "localhost/toolchain@sha256:" + strings.Repeat("b", 64),
			"buildCommand": []string{"sh", "-c", "go build ./..."},
			"testCommand":  []string{"go", "test", "./..."},
		},
	}
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := clients.registry.RegisterApp(ctx, connect.NewRequest(&appv1.RegisterAppRequest{IdempotencyKey: key + "-register", ManifestYaml: manifestJSON})); err != nil {
		t.Fatalf("register app: %v", err)
	}
	installed, err := clients.install.InstallApp(ctx, connect.NewRequest(&appv1.InstallAppRequest{
		IdempotencyKey: key + "-install", ProjectId: project.GetId(), AppId: appID, Version: "1.0.0", ExpectedProjectRevision: project.GetRevision(),
	}))
	if err != nil {
		t.Fatalf("install app: %v", err)
	}
	incidentID := ids.UUIDv7{}.New()
	evidence := "sha256:" + strings.Repeat("a", 64)
	occurrence := fmt.Sprintf("sha256:%s%s", strings.Repeat("e", 32), strings.ReplaceAll(incidentID, "-", ""))
	buildtestExec(t, `INSERT INTO workos_reliability.incidents (
    id, owner_user_id, project_id, app_instance_id, app_id, workload_id, workload_generation,
    violation, severity, summary, occurrence_digest, evidence_digest, state, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, 1, 'unexpected_exit', 'critical', 'Recovery fallback fixture regression',
    $7, $8, 'open', now(), now())`,
		incidentID, project.GetOwnerUserId(), project.GetId(), installed.Msg.GetInstallation().GetId(), appID,
		ids.UUIDv7{}.New(), occurrence, evidence)
	return buildtestFixture{
		AppID: appID, ProjectID: project.GetId(), Owner: project.GetOwnerUserId(), IncidentID: incidentID,
		Installation: installed.Msg.GetInstallation().GetId(), BaseDigest: source.Msg.GetBundle().GetDigest(),
	}
}

// TestRepairRecoveryFallback proves the Recovery governance cross-process
// (ADR-0016 §5): the project's bound provider (MCP) is healthy but honestly
// lacks repair source candidates, so repair admission falls back to the
// configured Recovery provider (generic-cli) and the incident still resolves
// through the full verified chain — candidate, Build/Test, staged version,
// canary and promotion — with the task durably bound to the recovery layer.
func TestRepairRecoveryFallback(t *testing.T) {
	clients := newBuildtestClients(t)
	fixture := seedRecoveryFixture(t, clients, "fallback")
	taskID := ""
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) && taskID == "" {
		fetched, err := clients.incidents.GetIncident(context.Background(), connect.NewRequest(&incidentv1.GetIncidentRequest{IncidentId: fixture.IncidentID}))
		if err == nil && fetched.Msg.GetIncident().GetRepairTaskId() != "" {
			taskID = fetched.Msg.GetIncident().GetRepairTaskId()
			break
		}
		time.Sleep(400 * time.Millisecond)
	}
	if taskID == "" {
		t.Fatal("repair orchestrator did not project a repair task onto the incident")
	}
	// The admitted task is bound to the Recovery provider, never to the
	// incapable MCP binding.
	rows := waitForBuildtestRows(t, `SELECT provider_id FROM workos_core.agent_tasks WHERE id::text = $1`, taskID)
	if len(rows) == 0 || rows[0]["provider_id"] != "generic-cli" {
		t.Fatalf("repair task must bind the recovery provider, got %v", rows)
	}
	// The verified chain completes end to end on the fallback layer.
	ledger := waitForBuildtestRows(t, `SELECT state FROM workos_reliability.deployment_ledger WHERE incident_id::text = $1`, fixture.IncidentID)
	if len(ledger) != 1 || ledger[0]["state"] != "promoted" {
		t.Fatalf("recovery deployment must end promoted, got %v", ledger)
	}
}

// TestRepairRecoveryAwaitingManual proves the two-level-unavailable verdict:
// with the MCP binding still incapable and the recovery executable broken,
// admission terminates awaiting_manual in the ledger with zero task side
// effects instead of retrying forever.
func TestRepairRecoveryAwaitingManual(t *testing.T) {
	clients := newBuildtestClients(t)
	cliPath := filepath.Join(buildtestGateEnv(t, "DIR"), "cli", "run")
	if err := os.Chmod(cliPath, 0o000); err != nil {
		t.Fatalf("break recovery executable: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(cliPath, 0o700) })
	// The harness catalog health check runs on a timer; give it a beat to
	// observe the broken executable before the incident lands.
	time.Sleep(2 * time.Second)

	fixture := seedRecoveryFixture(t, clients, "manual")
	rows := waitForBuildtestRows(t, `SELECT state FROM workos_reliability.repair_ledger WHERE incident_id::text = $1`, fixture.IncidentID)
	if len(rows) == 0 {
		t.Fatal("orchestrator never recorded the awaiting_manual verdict")
	}
	states := map[string]bool{}
	for _, row := range rows {
		state, _ := row["state"].(string)
		states[state] = true
	}
	if !states["awaiting_manual"] {
		t.Fatalf("expected an awaiting_manual ledger row, got %v", rows)
	}
	// awaiting_manual is terminal for this incident and admits no task.
	time.Sleep(3 * time.Second)
	tasks := buildtestQuery(t, `SELECT count(*) AS c FROM workos_core.agent_tasks WHERE project_id::text = $1`, fixture.ProjectID)
	if tasks[0]["c"].(int64) != 0 {
		t.Fatalf("awaiting_manual must have zero task side effects, got %v", tasks[0]["c"])
	}
	ledger := buildtestQuery(t, `SELECT count(*) AS c FROM workos_reliability.deployment_ledger WHERE incident_id::text = $1`, fixture.IncidentID)
	if ledger[0]["c"].(int64) != 0 {
		t.Fatal("awaiting_manual incident produced a deployment")
	}
}
