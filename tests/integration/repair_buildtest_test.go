//go:build integration && repairbuildtest

package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	reliabilitypostgres "github.com/yangtao121/workos/internal/reliability/adapters/postgres"
	reliabilityapp "github.com/yangtao121/workos/internal/reliability/application"

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
	executionv1 "github.com/yangtao121/workos/gen/go/workos/taskexecution/v1"
	"github.com/yangtao121/workos/gen/go/workos/taskexecution/v1/taskexecutionv1connect"
	"github.com/yangtao121/workos/internal/platform/identity"
	"github.com/yangtao121/workos/internal/platform/ids"
	reliabilitytransport "github.com/yangtao121/workos/internal/reliability/transport"
)

func buildtestGateEnv(t *testing.T, name string) string {
	t.Helper()
	value := os.Getenv("WORKOS_REPAIR_BUILDTEST_" + name)
	if value == "" {
		t.Fatalf("run through tools/repair-buildtest/gate.sh (missing %s)", name)
	}
	return value
}

type buildtestClients struct {
	http       *http.Client
	gatewayURL string
	coreURL    string
	runtimeURL string
	projects   projectv1connect.ProjectServiceClient
	registry   appv1connect.AppRegistryServiceClient
	sources    appv1connect.AppSourceBundleServiceClient
	install    appv1connect.AppInstallationServiceClient
	tasks      agentv1connect.AgentTaskServiceClient
	incidents  incidentv1connect.IncidentServiceClient
	surfaces   surfacev1connect.SurfaceServiceClient
	builds     taskexecutionv1connect.BuildTestServiceClient
	versions   taskexecutionv1connect.RepairVersionServiceClient
}

func newBuildtestClients(t *testing.T) *buildtestClients {
	t.Helper()
	client := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 20 * time.Second}
	t.Cleanup(client.CloseIdleConnections)
	clients := &buildtestClients{
		http:       client,
		gatewayURL: buildtestGateEnv(t, "GATEWAY_URL"),
		coreURL:    buildtestGateEnv(t, "CORE_URL"),
		runtimeURL: buildtestGateEnv(t, "RUNTIME_URL"),
	}
	clients.projects = projectv1connect.NewProjectServiceClient(client, clients.gatewayURL)
	clients.registry = appv1connect.NewAppRegistryServiceClient(client, clients.gatewayURL)
	clients.sources = appv1connect.NewAppSourceBundleServiceClient(client, clients.gatewayURL)
	clients.install = appv1connect.NewAppInstallationServiceClient(client, clients.gatewayURL)
	clients.tasks = agentv1connect.NewAgentTaskServiceClient(client, clients.gatewayURL)
	clients.incidents = incidentv1connect.NewIncidentServiceClient(client, clients.gatewayURL)
	clients.surfaces = surfacev1connect.NewSurfaceServiceClient(client, clients.runtimeURL)
	clients.builds = taskexecutionv1connect.NewBuildTestServiceClient(client, clients.runtimeURL)
	clients.versions = taskexecutionv1connect.NewRepairVersionServiceClient(client, clients.coreURL)
	return clients
}

func buildtestScenarioPath(t *testing.T) string {
	return filepath.Join(buildtestGateEnv(t, "DIR"), "scenario.conf")
}

func setBuildtestScenario(t *testing.T, name, mode string) {
	t.Helper()
	content := fmt.Sprintf("# gate-local fake engine scenario (ADR-0016)\n%s=%s\n", name, mode)
	if err := os.WriteFile(buildtestScenarioPath(t), []byte(content), 0o644); err != nil {
		t.Fatalf("write scenario: %v", err)
	}
}

// buildtestFixture describes one gate fixture app: its candidate shape and
// the fixed build/test argv the manifest pins.
type buildtestFixture struct {
	AppID        string `json:"app"`
	ProjectID    string `json:"project"`
	Owner        string `json:"owner"`
	IncidentID   string `json:"incident"`
	Installation string `json:"installation"`
	BaseDigest   string `json:"base_digest"`
	StagedLabel  string `json:"staged_label"`
	// DirectTaskID carries the directly-admitted repair task id for the
	// fault-matrix seeding path (no orchestrator ledger row).
	DirectTaskID string `json:"direct_task,omitempty"`
}

// seedBuildtestFixture registers and installs one container app whose source
// bundle matches the requested variant, then drives the real incident →
// repair task → CLI fixture candidate chain and waits for completion.
func seedBuildtestFixture(t *testing.T, clients *buildtestClients, variant, suffix string) buildtestFixture {
	t.Helper()
	ctx := context.Background()
	key := fmt.Sprintf("bt-%s-%d", suffix, time.Now().UnixNano())
	appID := fmt.Sprintf("bt-%s-%s", suffix, strings.Repeat("0", 0)+fmt.Sprint(time.Now().UnixNano()%100000))
	appID = strings.ReplaceAll(appID, "_", "-")
	created, err := clients.projects.CreateProject(ctx, connect.NewRequest(&projectv1.CreateProjectRequest{
		IdempotencyKey: key, Name: "Build test " + suffix,
		HarnessBinding: &projectv1.HarnessBinding{ProviderId: "generic-cli", InstancePolicy: projectv1.HarnessInstancePolicy_HARNESS_INSTANCE_POLICY_EPHEMERAL, ResourcePolicyId: "project-no-tools"},
	}))
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	project := created.Msg.GetProject()
	files := buildtestSourceFiles(variant)
	source, err := clients.sources.CreateAppSourceBundle(ctx, connect.NewRequest(&appv1.CreateAppSourceBundleRequest{IdempotencyKey: key + "-source", Files: files}))
	if err != nil {
		t.Fatalf("upload source: %v", err)
	}
	manifest := map[string]any{
		"apiVersion": "workos.app/v1", "id": appID, "name": "Build test fixture", "version": "1.0.0", "scope": "project",
		"runtime":     map[string]any{"type": "container", "image": "localhost/app@sha256:" + strings.Repeat("a", 64), "command": []string{"/app/app"}, "port": 8080},
		"surfaces":    []any{map[string]any{"id": "main", "renderer": "web-service", "route": "/"}},
		"permissions": []string{},
		"resources":   map[string]any{"cpuHard": 1, "memoryHighMb": 64, "memoryMaxMb": 96, "pidsMax": 32},
		"health":      map[string]any{"httpPath": "/health", "startupSeconds": 10, "restartLimit": 1}, "maintainer": map[string]any{},
		"build": map[string]any{
			"sourceBundleId": source.Msg.GetBundle().GetId(), "sourceDigest": source.Msg.GetBundle().GetDigest(),
			"baseImage":    "localhost/toolchain@sha256:" + strings.Repeat("b", 64),
			"buildCommand": []string{"sh", "-c", buildtestBuildCommand(variant)},
			"testCommand":  []string{"go", "test", "./..."},
		},
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := clients.registry.RegisterApp(ctx, connect.NewRequest(&appv1.RegisterAppRequest{IdempotencyKey: key + "-register", ManifestYaml: raw})); err != nil {
		t.Fatalf("register app: %v", err)
	}
	installed, err := clients.install.InstallApp(ctx, connect.NewRequest(&appv1.InstallAppRequest{
		IdempotencyKey: key + "-install", ProjectId: project.GetId(), AppId: appID, Version: "1.0.0", ExpectedProjectRevision: project.GetRevision(),
	}))
	if err != nil {
		t.Fatalf("install app: %v", err)
	}
	installation := installed.Msg.GetInstallation()
	// The incident row is seeded directly: the supervisor-driven origin is
	// proven by the real-supervision and repair-deployment gates, while this
	// gate drives the verified chain from the incident onward. Seeding also
	// keeps the canary's incident scope clean (a stopped fixture workload
	// keeps opening health occurrences that would roll every canary back).
	incidentID := ids.UUIDv7{}.New()
	seeded := buildtestFixture{
		AppID: appID, ProjectID: project.GetId(), Owner: project.GetOwnerUserId(), IncidentID: incidentID,
		Installation: installation.GetId(), BaseDigest: source.Msg.GetBundle().GetDigest(),
	}
	insertIncidentRow := func() {
		evidence := fmt.Sprintf("sha256:%s", strings.Repeat("a", 64))
		occurrence := fmt.Sprintf("sha256:%s%s", strings.Repeat("e", 32), strings.ReplaceAll(incidentID, "-", ""))
		buildtestExec(t, `INSERT INTO workos_reliability.incidents (
    id, owner_user_id, project_id, app_instance_id, app_id, workload_id, workload_generation,
    violation, severity, summary, occurrence_digest, evidence_digest, state, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, 1, 'unexpected_exit', 'critical', 'Build test fixture regression',
    $7, $8, 'open', now(), now())`,
			incidentID, project.GetOwnerUserId(), project.GetId(), installation.GetId(), appID,
			ids.UUIDv7{}.New(), occurrence, evidence)
	}
	if suffix == "direct" {
		// Direct admission happens before the incident row exists, and the
		// terminal ledger row lands with it: the real orchestrator never
		// sees an open incident without a ledger row.
		request := connect.NewRequest(&agentv1.CreateRepairTaskRequest{
			IdempotencyKey: "matrix-" + key, ProjectId: project.GetId(), AppInstanceId: installation.GetId(),
			IncidentId: incidentID, ViolationSummary: "Build test fault matrix fixture",
		})
		request.Header().Set(identity.UserHeader, project.GetOwnerUserId())
		request.Header().Set(identity.DeviceHeader, "01999999-9999-7999-8999-000000000b02")
		task, err := agentv1connect.NewAgentRepairTaskServiceClient(clients.http, clients.coreURL).CreateRepairTask(ctx, request)
		if err != nil {
			t.Fatalf("create repair task: %v", err)
		}
		insertIncidentRow()
		buildtestExec(t, `INSERT INTO workos_reliability.repair_ledger (
    incident_id, project_id, task_id, state, attempts, created_at, updated_at)
VALUES ($1, $2, $3, 'terminal', 1, now(), now()) ON CONFLICT (incident_id) DO NOTHING`,
			incidentID, project.GetId(), task.Msg.GetTaskId())
		buildtestExec(t, `UPDATE workos_reliability.incidents SET repair_task_id = $2 WHERE id::text = $1`,
			incidentID, task.Msg.GetTaskId())
		waitForBuildtestTaskCompletion(t, clients, task.Msg.GetTaskId())
		direct := seeded
		direct.DirectTaskID = task.Msg.GetTaskId()
		return direct
	}
	insertIncidentRow()
	// The incident-driven path is the honest chain: the reliability
	// orchestrator submits the repair task and its ledger row, so the build
	// coordinator sees the completion. Wait for the projected task id.
	taskID := ""
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) && taskID == "" {
		fetched, err := clients.incidents.GetIncident(ctx, connect.NewRequest(&incidentv1.GetIncidentRequest{IncidentId: incidentID}))
		if err == nil && fetched.Msg.GetIncident().GetRepairTaskId() != "" {
			taskID = fetched.Msg.GetIncident().GetRepairTaskId()
			break
		}
		time.Sleep(400 * time.Millisecond)
	}
	if taskID == "" {
		t.Fatal("repair orchestrator did not project a repair task onto the incident")
	}
	waitForBuildtestTaskCompletion(t, clients, taskID)
	return buildtestFixture{
		AppID: appID, ProjectID: project.GetId(), Owner: project.GetOwnerUserId(), IncidentID: incidentID,
		Installation: installation.GetId(), BaseDigest: source.Msg.GetBundle().GetDigest(),
	}
}

// seedDirectFixture mirrors seedBuildtestFixture with direct repair-task
// admission (no orchestrator ledger row): the verified chain is driven by
// the caller, invisible to the real reliability loop.
func seedDirectFixture(t *testing.T, clients *buildtestClients, variant, suffix string) buildtestFixture {
	t.Helper()
	return seedBuildtestFixture(t, clients, variant, "direct")
}

func buildtestSourceFiles(variant string) []*appv1.AppSourceFile {
	main := "package main\nfunc answer() int { return 0 }\nfunc main() {}\n"
	test := "package main\nimport \"testing\"\nfunc TestAnswer(t *testing.T) { if answer() != 42 { t.Fatal(\"wrong answer\") } }\n"
	switch variant {
	case "testfail":
		test = "package main\nimport \"testing\"\nfunc TestAnswer(t *testing.T) { if answer() != 0 { t.Fatal(\"wrong answer\") } }\n"
	case "buildfail":
		main = "package main\nfunc missing() int { return 7 }\nvar _ = undefinedSymbol\nfunc answer() int { return 0 }\nfunc main() {}\n"
	}
	return []*appv1.AppSourceFile{
		{Path: "go.mod", Content: []byte("module repair-fixture\n\ngo 1.26\n")},
		{Path: "main.go", Content: []byte(main)},
		{Path: "main_test.go", Content: []byte(test)},
	}
}

func buildtestBuildCommand(variant string) string {
	if variant == "slowbuild" {
		return "sleep 25 && go build ./..."
	}
	return "go build ./..."
}

func waitForBuildtestIncident(t *testing.T, clients *buildtestClients, projectID string) string {
	t.Helper()
	ctx := context.Background()
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		listed, err := clients.incidents.ListIncidents(ctx, connect.NewRequest(&incidentv1.ListIncidentsRequest{ProjectId: projectID}))
		if err == nil && len(listed.Msg.GetIncidents()) > 0 {
			return listed.Msg.GetIncidents()[0].GetId()
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatal("supervisor did not open the incident")
	return ""
}

func waitForBuildtestTaskCompletion(t *testing.T, clients *buildtestClients, taskID string) {
	t.Helper()
	ctx := context.Background()
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		state, err := clients.tasks.GetTask(ctx, connect.NewRequest(&agentv1.GetTaskRequest{TaskId: taskID}))
		if err != nil {
			t.Fatalf("get task: %v", err)
		}
		switch state.Msg.GetTask().GetState() {
		case agentv1.AgentTaskState_AGENT_TASK_STATE_COMPLETED:
			return
		case agentv1.AgentTaskState_AGENT_TASK_STATE_FAILED, agentv1.AgentTaskState_AGENT_TASK_STATE_CANCELLED:
			t.Fatalf("repair task terminal failure: %s", state.Msg.GetTask().GetState())
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Fatal("repair task did not complete")
}

// waitForBuildtestJob polls Runtime's durable build verdict and returns the
// terminal state plus the source digest bound to it.
type buildtestTerminal struct {
	State        string
	Failure      string
	JobID        string
	SourceDigest string
	Attempts     int32
	Engine       *executionv1.BuildEngineFacts
}

func waitForBuildtestJob(t *testing.T, clients *buildtestClients, taskID string, terminal func(state string) bool) buildtestTerminal {
	t.Helper()
	ctx := context.Background()
	deadline := time.Now().Add(120 * time.Second)
	var lastErr error
	var lastState string
	for time.Now().Before(deadline) {
		verdict, err := clients.builds.GetBuildTest(ctx, connect.NewRequest(&executionv1.GetBuildTestRequest{TaskId: taskID}))
		if err == nil {
			lastState = verdict.Msg.GetState()
			if terminal(lastState) {
				return buildtestTerminal{
					State: verdict.Msg.GetState(), Failure: verdict.Msg.GetFailureReason(),
					JobID: verdict.Msg.GetJobId(), SourceDigest: verdict.Msg.GetSourceDigest(),
					Attempts: verdict.Msg.GetAttempts(), Engine: verdict.Msg.GetEngine(),
				}
			}
		} else {
			lastErr = err
		}
		time.Sleep(400 * time.Millisecond)
	}
	t.Fatalf("build job %s did not reach the expected terminal state (last=%s err=%v)", taskID, lastState, lastErr)
	return buildtestTerminal{}
}

func buildtestQuery(t *testing.T, sql string, args ...any) []map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, buildtestGateEnv(t, "DATABASE_URL"))
	if err != nil {
		t.Fatalf("connect scratch db: %v", err)
	}
	defer conn.Close(context.Background()) //nolint:errcheck
	rows, err := conn.Query(ctx, sql, args...)
	if err != nil {
		t.Fatalf("query %s: %v", sql, err)
	}
	defer rows.Close()
	fieldNames := rows.FieldDescriptions()
	records := []map[string]any{}
	for rows.Next() {
		values, err := rows.Values()
		if err != nil {
			t.Fatalf("scan: %v", err)
		}
		record := map[string]any{}
		for index, name := range fieldNames {
			record[string(name.Name)] = normalizeBuildtestValue(values[index])
		}
		records = append(records, record)
	}
	return records
}

// normalizeBuildtestValue renders pgx uuid byte arrays as canonical strings
// so callers can assert on plain text.
func normalizeBuildtestValue(value any) any {
	if raw, ok := value.([16]byte); ok {
		return uuid.UUID(raw).String()
	}
	return value
}

func waitForBuildtestRows(t *testing.T, sql string, args ...any) []map[string]any {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	var rows []map[string]any
	for time.Now().Before(deadline) {
		rows = buildtestQuery(t, sql, args...)
		if len(rows) > 0 {
			return rows
		}
		time.Sleep(400 * time.Millisecond)
	}
	return rows
}

func currentInstallationVersion(t *testing.T, clients *buildtestClients, fixture buildtestFixture) string {
	t.Helper()
	ctx := context.Background()
	listed, err := clients.install.ListInstalledApps(ctx, connect.NewRequest(&appv1.ListInstalledAppsRequest{ProjectId: fixture.ProjectID}))
	if err != nil {
		t.Fatalf("list installations: %v", err)
	}
	for _, installation := range listed.Msg.GetInstallations() {
		if installation.GetId() == fixture.Installation {
			return installation.GetVersion()
		}
	}
	t.Fatal("installation disappeared")
	return ""
}

func currentAppVersion(t *testing.T, clients *buildtestClients, fixture buildtestFixture) string {
	t.Helper()
	app, err := clients.registry.GetApp(context.Background(), connect.NewRequest(&appv1.GetAppRequest{AppId: fixture.AppID}))
	if err != nil {
		t.Fatalf("get app: %v", err)
	}
	return app.Msg.GetApp().GetVersion()
}

// TestRepairBuildTestChain proves the full verified chain across real
// processes: incident → repair task → CLI fixture candidate → Runtime
// build/test (real go toolchain) → staged registration → canary → publish;
// plus the failure variants that must never produce a deployable version.
func TestRepairBuildTestChain(t *testing.T) {
	clients := newBuildtestClients(t)

	t.Run("SuccessPublishesVerifiedCandidate", func(t *testing.T) {
		fixture := seedBuildtestFixture(t, clients, "success", "ok")
		// The reliability loop submits the build as soon as the completed row
		// rotates in; the durable job drives to success under the real
		// toolchain image.
		rows := waitForBuildtestRows(t, `SELECT b.task_id FROM workos_runtime.build_jobs b
JOIN workos_reliability.repair_ledger l ON l.task_id = b.task_id
WHERE l.incident_id::text = $1`, fixture.IncidentID)
		if len(rows) == 0 {
			t.Fatal("reliability did not submit the build job")
		}
		taskID, _ := rows[0]["task_id"].(string)
		verdict := waitForBuildtestJob(t, clients, taskID, func(state string) bool { return state == "succeeded" || state == "failed" })
		if verdict.State != "succeeded" {
			t.Fatalf("expected build success, got %s %s", verdict.State, verdict.Failure)
		}
		if verdict.Engine.GetEngine() != "process" || verdict.Engine.GetNetworkIsolated() {
			t.Fatalf("engine facts must be honest: %+v", verdict.Engine)
		}
		// Duplicate submit replays the same job without creating a second one.
		duplicate := connect.NewRequest(&executionv1.SubmitBuildTestRequest{Job: &executionv1.BuildTestJob{TaskId: taskID}, InputFacts: &executionv1.BuildTestInputFacts{}})
		duplicate.Msg.Job.OwnerUserId = fixture.Owner
		if _, err := clients.builds.SubmitBuildTest(context.Background(), duplicate); connect.CodeOf(err) != connect.CodeInvalidArgument && err != nil {
			// An invalid replay is fine (facts must match); a created second
			// job is the failure this assertion guards against.
			t.Fatalf("duplicate submit: %v", err)
		}
		count := buildtestQuery(t, `SELECT count(*) AS c FROM workos_runtime.build_jobs WHERE task_id IN (
    SELECT id FROM workos_core.agent_tasks WHERE project_id::text = $1)`, fixture.ProjectID)
		if count[0]["c"].(int64) != 1 {
			t.Fatalf("duplicate submit created extra jobs: %v", count[0]["c"])
		}
		// The staged version exists in the ledger but stays invisible to the
		// owner-facing default selection until the canary publishes it.
		staged := waitForBuildtestRows(t, `SELECT v.version, v.state, m.published_at IS NOT NULL AS published
FROM workos_core.app_repair_candidate_versions m
JOIN workos_core.app_versions v ON v.id = m.app_version_id
WHERE m.project_id::text = $1`, fixture.ProjectID)
		if len(staged) == 0 {
			t.Fatal("staged candidate version was never registered")
		}
		label, _ := staged[0]["version"].(string)
		fixture.StagedLabel = label
		// The canary: installation transitions to the staged label, the
		// surface starts, the observation window passes, promote publishes.
		deadline := time.Now().Add(90 * time.Second)
		for time.Now().Before(deadline) {
			if currentInstallationVersion(t, clients, fixture) == label {
				break
			}
			time.Sleep(500 * time.Millisecond)
		}
		if got := currentInstallationVersion(t, clients, fixture); got != label {
			t.Fatalf("canary did not pin the staged version: %s != %s", got, label)
		}
		published := false
		deadline = time.Now().Add(60 * time.Second)
		for time.Now().Before(deadline) {
			if currentAppVersion(t, clients, fixture) == label {
				published = true
				break
			}
			time.Sleep(500 * time.Millisecond)
		}
		if !published {
			t.Fatalf("staged candidate never became the published current version: %s", currentAppVersion(t, clients, fixture))
		}
		ledger := buildtestQuery(t, `SELECT state FROM workos_reliability.deployment_ledger WHERE incident_id::text = $1`, fixture.IncidentID)
		if len(ledger) != 1 || ledger[0]["state"] != "promoted" {
			t.Fatalf("deployment ledger must end promoted, got %v", ledger)
		}
		sessions := buildtestQuery(t, `SELECT count(*) AS c FROM workos_runtime.surface_sessions WHERE app_instance_id::text = $1`, fixture.Installation)
		if sessions[0]["c"].(int64) < 1 {
			t.Fatalf("canary surface was never started: %v", sessions[0]["c"])
		}
		// Duplicate registration replays the same staged facts.
		register := connect.NewRequest(&executionv1.RegisterRepairCandidateVersionRequest{
			TaskId: taskID, ProjectId: fixture.ProjectID, InstallationId: fixture.Installation,
			BuildJobId: "0198d7ea-2110-7c42-b659-c5e4d73bc341", SourceDigest: verdict.SourceDigest,
		})
		register.Header().Set(identity.UserHeader, fixture.Owner)
		register.Header().Set(identity.DeviceHeader, "01999999-9999-7999-8999-000000000b02")
		replay, err := clients.versions.RegisterRepairCandidateVersion(context.Background(), register)
		if err != nil || replay.Msg.GetVersion() != label || replay.Msg.GetCreated() {
			t.Fatalf("registration replay must be idempotent: %v %+v", err, replay.Msg)
		}
	})

	failureVariant := func(t *testing.T, variant, reason string) {
		fixture := seedBuildtestFixture(t, clients, variant, variant)
		rows := waitForBuildtestRows(t, `SELECT b.task_id FROM workos_runtime.build_jobs b
JOIN workos_reliability.repair_ledger l ON l.task_id = b.task_id
WHERE l.incident_id::text = $1`, fixture.IncidentID)
		if len(rows) == 0 {
			t.Fatal("reliability did not submit the failing build job")
		}
		taskID, _ := rows[0]["task_id"].(string)
		verdict := waitForBuildtestJob(t, clients, taskID, func(state string) bool { return state == "succeeded" || state == "failed" })
		if verdict.State != "failed" || verdict.Failure != reason {
			t.Fatalf("expected %s verdict, got %s/%s", reason, verdict.State, verdict.Failure)
		}
		time.Sleep(3 * time.Second)
		if staged := buildtestQuery(t, `SELECT count(*) AS c FROM workos_core.app_repair_candidate_versions WHERE project_id::text = $1`, fixture.ProjectID); staged[0]["c"].(int64) != 0 {
			t.Fatal("failed build produced a staged version")
		}
		if ledger := buildtestQuery(t, `SELECT count(*) AS c FROM workos_reliability.deployment_ledger WHERE incident_id::text = $1`, fixture.IncidentID); ledger[0]["c"].(int64) != 0 {
			t.Fatal("failed build produced a deployment")
		}
		if got := currentInstallationVersion(t, clients, fixture); got != "1.0.0" {
			t.Fatalf("failed build changed the installation: %s", got)
		}
		if got := currentAppVersion(t, clients, fixture); got != "1.0.0" {
			t.Fatalf("failed build changed the default version: %s", got)
		}
	}
	t.Run("TestFailureNeverDeploys", func(t *testing.T) { failureVariant(t, "testfail", "test-failed") })
	t.Run("BuildFailureNeverDeploys", func(t *testing.T) { failureVariant(t, "buildfail", "build-failed") })

	t.Run("UserVersionChangeIsNotOverridden", func(t *testing.T) {
		fixture := seedBuildtestFixture(t, clients, "success", "userchange")
		// Register a competing owner-driven upgrade target up front.
		upgrade := map[string]any{
			"apiVersion": "workos.app/v1", "id": fixture.AppID, "name": "Build test fixture", "version": "2.0.0", "scope": "project",
			"runtime":     map[string]any{"type": "container", "image": "localhost/app@sha256:" + strings.Repeat("a", 64), "command": []string{"/app/app"}, "port": 8080},
			"surfaces":    []any{map[string]any{"id": "main", "renderer": "web-service", "route": "/"}},
			"permissions": []string{},
			"resources":   map[string]any{"cpuHard": 1, "memoryHighMb": 64, "memoryMaxMb": 96, "pidsMax": 32},
			"health":      map[string]any{"httpPath": "/health", "startupSeconds": 10, "restartLimit": 1}, "maintainer": map[string]any{},
		}
		raw, err := json.Marshal(upgrade)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := clients.registry.RegisterApp(context.Background(), connect.NewRequest(&appv1.RegisterAppRequest{IdempotencyKey: fmt.Sprintf("upgrade-%d", time.Now().UnixNano()), ManifestYaml: raw})); err != nil {
			t.Fatalf("register upgrade: %v", err)
		}
		// Wait for the staged registration, then race the canary with an
		// owner-driven version change: the deployment preconditions must
		// reject instead of overriding the user.
		staged := waitForBuildtestRows(t, `SELECT v.version FROM workos_core.app_repair_candidate_versions m
JOIN workos_core.app_versions v ON v.id = m.app_version_id
WHERE m.project_id::text = $1`, fixture.ProjectID)
		if len(staged) == 0 {
			t.Fatal("staged candidate never registered")
		}
		stagedLabel, _ := staged[0]["version"].(string)
		// Deterministic ordering: let the canary pin first, then change the
		// user's version. The promote precondition must then refuse to
		// publish what the user replaced.
		userDeadline := time.Now().Add(90 * time.Second)
		for time.Now().Before(userDeadline) {
			if currentInstallationVersion(t, clients, fixture) == stagedLabel {
				break
			}
			time.Sleep(400 * time.Millisecond)
		}
		if currentInstallationVersion(t, clients, fixture) != stagedLabel {
			t.Fatal("canary never pinned the staged candidate")
		}
		transition := connect.NewRequest(&appv1.TransitionAppVersionRequest{
			IdempotencyKey: fmt.Sprintf("user-upgrade-%d", time.Now().UnixNano()), ProjectId: fixture.ProjectID,
			InstallationId: fixture.Installation, Version: "2.0.0", ExpectedProjectRevision: buildtestProjectRevision(t, fixture),
		})
		if _, err := clients.install.TransitionAppVersion(context.Background(), transition); err != nil {
			t.Fatalf("owner upgrade: %v", err)
		}
		// The canary transition must fail its revision precondition; the
		// ledger converges to a terminal state without publishing.
		deadline := time.Now().Add(90 * time.Second)
		terminal := ""
		for time.Now().Before(deadline) {
			ledger := buildtestQuery(t, `SELECT state FROM workos_reliability.deployment_ledger WHERE incident_id::text = $1`, fixture.IncidentID)
			if len(ledger) > 0 {
				state, _ := ledger[0]["state"].(string)
				if state == "promoted" || state == "rolled_back" || state == "failed" {
					terminal = state
					break
				}
			}
			time.Sleep(500 * time.Millisecond)
		}
		if terminal == "promoted" {
			t.Fatal("deployment overrode the user's version change")
		}
		if got := currentInstallationVersion(t, clients, fixture); got != "2.0.0" {
			t.Fatalf("user version change was overridden: %s", got)
		}
		if got := currentAppVersion(t, clients, fixture); got == "2.0.0" {
			// The default selection may legitimately show the owner's own 2.0.0.
			_ = got
		}
		published := buildtestQuery(t, `SELECT v.state FROM workos_core.app_repair_candidate_versions m
JOIN workos_core.app_versions v ON v.id = m.app_version_id
WHERE m.project_id::text = $1`, fixture.ProjectID)
		for _, row := range published {
			if state, _ := row["state"].(string); state == "published" {
				t.Fatal("rejected canary published the staged version anyway")
			}
		}
	})
}

// TestRepairBuildTestRestartSeed leaves a slow build running behind a runtime
// lease; gate.sh restarts the runtime container between Seed and Restore.
func TestRepairBuildTestRestartSeed(t *testing.T) {
	clients := newBuildtestClients(t)
	fixture := seedBuildtestFixture(t, clients, "slowbuild", "restart")
	rows := waitForBuildtestRows(t, `SELECT b.task_id, b.state FROM workos_runtime.build_jobs b
JOIN workos_reliability.repair_ledger l ON l.task_id = b.task_id
WHERE l.incident_id::text = $1`, fixture.IncidentID)
	if len(rows) == 0 {
		t.Fatal("reliability did not submit the slow build job")
	}
	if state, _ := rows[0]["state"].(string); state == "succeeded" || state == "failed" {
		t.Fatalf("slow build already terminal before restart: %s", state)
	}
	// Wait until the executor has actually claimed the job: the restart must
	// interrupt a live run so the lease takeover is a real second attempt.
	taskID, _ := rows[0]["task_id"].(string)
	running := waitForBuildtestJob(t, clients, taskID, func(state string) bool { return state == "running" })
	_ = running
	// Persist the fixture identity for the restore phase.
	state := restartGateState{Project: fixture.ProjectID, Incident: fixture.IncidentID, App: fixture.AppID, Installation: fixture.Installation}
	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(buildtestGateEnv(t, "DIR"), "restart-state.json"), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestRepairBuildTestRestartRestore proves the crashed executor's running job
// is taken over through the expired lease and still reaches the verified
// chain's terminal outcome on real processes.
type restartGateState struct {
	Project      string `json:"project"`
	Incident     string `json:"incident"`
	App          string `json:"app"`
	Installation string `json:"installation"`
}

func TestRepairBuildTestRestartRestore(t *testing.T) {
	clients := newBuildtestClients(t)
	encoded, err := os.ReadFile(filepath.Join(buildtestGateEnv(t, "DIR"), "restart-state.json"))
	if err != nil {
		t.Fatal(err)
	}
	var state restartGateState
	if err := json.Unmarshal(encoded, &state); err != nil {
		t.Fatal(err)
	}
	fixture := buildtestFixture{ProjectID: state.Project, IncidentID: state.Incident, AppID: state.App, Installation: state.Installation}
	rows := waitForBuildtestRows(t, `SELECT b.task_id FROM workos_runtime.build_jobs b
JOIN workos_reliability.repair_ledger l ON l.task_id = b.task_id
WHERE l.incident_id::text = $1`, fixture.IncidentID)
	if len(rows) == 0 {
		t.Fatal("build job vanished across the restart")
	}
	taskID, _ := rows[0]["task_id"].(string)
	verdict := waitForBuildtestJob(t, clients, taskID, func(state string) bool { return state == "succeeded" || state == "failed" })
	if verdict.State != "succeeded" {
		t.Fatalf("restart recovery did not complete the build: %s/%s", verdict.State, verdict.Failure)
	}
	if verdict.Attempts < 2 {
		t.Fatal("lease takeover must count as a new attempt")
	}
	published := false
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		if currentAppVersion(t, clients, fixture) != "1.0.0" {
			published = true
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !published {
		t.Fatal("restarted chain never published the verified candidate")
	}
	ledger := buildtestQuery(t, `SELECT state FROM workos_reliability.deployment_ledger WHERE incident_id::text = $1`, fixture.IncidentID)
	if len(ledger) != 1 || ledger[0]["state"] != "promoted" {
		t.Fatalf("restarted deployment must end promoted, got %v", ledger)
	}
}

var _ = ids.UUIDv7{}

// scriptedDriver drives the real DeploymentController against real Core and
// Runtime surfaces with fault injection only where the matrix demands it.
type scriptedDriver struct {
	clients       *buildtestClients
	failStarts    int
	failRollbacks int
	transitionKey string
}

func (d *scriptedDriver) Transition(ctx context.Context, candidate reliabilityapp.DeploymentCandidate, key string) error {
	d.transitionKey = key
	request := connect.NewRequest(&executionv1.TransitionCandidateVersionRequest{
		IdempotencyKey: key, ProjectId: candidate.ProjectID, InstallationId: candidate.InstallationID,
		Version: candidate.TargetVersion, ManifestDigest: candidate.ManifestDigest,
		ExpectedProjectRevision: candidate.ExpectedRevision,
	})
	request.Header().Set(identity.UserHeader, candidate.OwnerUserID)
	request.Header().Set(identity.DeviceHeader, "01999999-9999-7999-8999-000000000b02")
	_, err := d.clients.versions.TransitionCandidateVersion(ctx, request)
	return err
}

func (d *scriptedDriver) StartSurface(ctx context.Context, candidate reliabilityapp.DeploymentCandidate, key string) error {
	if d.failStarts > 0 {
		d.failStarts--
		return errors.New("runtime surface unavailable (scripted startup fault)")
	}
	surface := connect.NewRequest(&surfacev1.CreateSurfaceRequest{
		IdempotencyKey: key, ProjectId: candidate.ProjectID, AppInstanceId: candidate.InstallationID,
		DeviceClass:        surfacev1.DeviceClass_DEVICE_CLASS_DESKTOP,
		Viewport:           &surfacev1.Viewport{Width: 1280, Height: 800, PixelRatio: 1},
		ExpectedAppVersion: candidate.TargetVersion,
	})
	surface.Header().Set(identity.UserHeader, candidate.OwnerUserID)
	surface.Header().Set(identity.DeviceHeader, "01999999-9999-7999-8999-000000000b02")
	response, err := d.clients.surfaces.CreateSurface(ctx, surface)
	if err != nil {
		return err
	}
	session := response.Msg.GetSession()
	if session.GetId() == "" || session.GetAppInstanceId() != candidate.InstallationID {
		return errors.New("deployment surface is not active")
	}
	return nil
}

func (d *scriptedDriver) Rollback(ctx context.Context, candidate reliabilityapp.DeploymentCandidate, key string) error {
	if d.failRollbacks > 0 {
		d.failRollbacks--
		return errors.New("core rollback unavailable (scripted)")
	}
	request := connect.NewRequest(&appv1.RollbackAppVersionRequest{
		IdempotencyKey: key, ProjectId: candidate.ProjectID, InstallationId: candidate.InstallationID,
		ExpectedProjectRevision: candidate.ExpectedRevision + 1,
	})
	request.Header().Set(identity.UserHeader, candidate.OwnerUserID)
	request.Header().Set(identity.DeviceHeader, "01999999-9999-7999-8999-000000000b02")
	response, err := d.clients.install.RollbackAppVersion(ctx, request)
	if err != nil {
		return err
	}
	// Mirrors the real driver: adopt the restored pin, then recovery
	// completes only after that pin's surface starts.
	restored := candidate
	restored.TargetVersion = response.Msg.GetInstallation().GetVersion()
	return d.StartSurface(ctx, restored, key)
}

func (d *scriptedDriver) Publish(ctx context.Context, candidate reliabilityapp.DeploymentCandidate) error {
	request := connect.NewRequest(&executionv1.PublishRepairCandidateVersionRequest{
		TaskId: candidate.TaskID, ProjectId: candidate.ProjectID, InstallationId: candidate.InstallationID,
		Version: candidate.TargetVersion, ManifestDigest: candidate.ManifestDigest,
	})
	request.Header().Set(identity.UserHeader, candidate.OwnerUserID)
	request.Header().Set(identity.DeviceHeader, "01999999-9999-7999-8999-000000000b02")
	_, err := d.clients.versions.PublishRepairCandidateVersion(ctx, request)
	return err
}

// matrixStage records everything the fault phase needs to drive one
// verified staged candidate through the deployment controller.
type matrixStage struct {
	Fixture   buildtestFixture `json:"fixture"`
	TaskID    string           `json:"task_id"`
	Version   string           `json:"version"`
	Digest    string           `json:"digest"`
	Revision  int64            `json:"revision"`
	BuildJob  string           `json:"build_job"`
	SourceRef string           `json:"source_digest"`
}

// seedMatrixStage drives one fixture through the verified chain up to a
// registered staged version while the reliability loop is still running, and
// persists the facts for the fault-injection phase.
func seedMatrixStage(t *testing.T, clients *buildtestClients, suffix string) matrixStage {
	t.Helper()
	// The fault phase must own the deployment exclusively, so the seed
	// bypasses the orchestrator ledger (direct admission) and drives the
	// verified chain itself: candidate read, build submission, registration.
	fixture := seedDirectFixture(t, clients, "success", suffix)
	reader := taskexecutionv1connect.NewRepairCandidateServiceClient(clients.http, clients.coreURL, connect.WithReadMaxBytes(2*1024*1024))
	read := connect.NewRequest(&executionv1.GetRepairSourceCandidateRequest{TaskId: fixture.DirectTaskID})
	read.Header().Set(identity.UserHeader, fixture.Owner)
	read.Header().Set(identity.DeviceHeader, "01999999-9999-7999-8999-000000000b02")
	candidate, err := reader.GetRepairSourceCandidate(context.Background(), read)
	if err != nil {
		t.Fatalf("read candidate: %v", err)
	}
	files := make([]reliabilityapp.CandidateFile, 0, len(candidate.Msg.GetCandidateSource().GetFiles()))
	for _, file := range candidate.Msg.GetCandidateSource().GetFiles() {
		files = append(files, reliabilityapp.CandidateFile{Path: file.GetPath(), Content: file.GetContent(), Executable: file.GetExecutable()})
	}
	facts := reliabilityapp.CandidateFacts{
		TaskID: fixture.DirectTaskID, IncidentID: fixture.IncidentID, ProjectID: fixture.ProjectID,
		OwnerUserID: fixture.Owner, AppInstanceID: fixture.Installation, AppID: fixture.AppID,
		BaseVersion: candidate.Msg.GetInput().GetTarget().GetVersion(), ManifestDigest: candidate.Msg.GetInput().GetTarget().GetManifestDigest(),
		SourceBundleID: candidate.Msg.GetCandidateSource().GetId(), SourceDigest: candidate.Msg.GetCandidateSource().GetDigest(),
		BaseImage: candidate.Msg.GetInput().GetBaseImage(), BuildCommand: candidate.Msg.GetInput().GetBuildCommand(), TestCommand: candidate.Msg.GetInput().GetTestCommand(),
		Files: files,
	}
	gateway := reliabilitytransport.NewBuildTestServiceClient(clients.runtimeURL)
	jobID, _, err := gateway.Submit(context.Background(), facts)
	if err != nil {
		t.Fatalf("submit build: %v", err)
	}
	_ = jobID
	taskID := fixture.DirectTaskID
	verdict := waitForBuildtestJob(t, clients, taskID, func(state string) bool { return state == "succeeded" })
	register := connect.NewRequest(&executionv1.RegisterRepairCandidateVersionRequest{
		TaskId: taskID, ProjectId: fixture.ProjectID, InstallationId: fixture.Installation,
		BuildJobId: verdict.JobID, SourceDigest: verdict.SourceDigest,
	})
	register.Header().Set(identity.UserHeader, fixture.Owner)
	register.Header().Set(identity.DeviceHeader, "01999999-9999-7999-8999-000000000b02")
	registered, err := clients.versions.RegisterRepairCandidateVersion(context.Background(), register)
	if err != nil {
		t.Fatalf("register staged: %v", err)
	}
	fixture.StagedLabel = registered.Msg.GetVersion()
	return matrixStage{
		Fixture: fixture, TaskID: taskID, Version: registered.Msg.GetVersion(),
		Digest: registered.Msg.GetManifestDigest(), Revision: registered.Msg.GetProjectRevision(),
		BuildJob: verdict.JobID, SourceRef: verdict.SourceDigest,
	}
}

// TestRepairBuildTestMatrixSeed stages the three fault-matrix candidates and
// persists their state; gate.sh stops the reliability loop afterwards so the
// scripted fault driver owns the ledger exclusively.
func TestRepairBuildTestMatrixSeed(t *testing.T) {
	clients := newBuildtestClients(t)
	stages := map[string]matrixStage{
		"startup":  seedMatrixStage(t, clients, "startup"),
		"canary":   seedMatrixStage(t, clients, "canary"),
		"rollback": seedMatrixStage(t, clients, "rollback"),
	}
	encoded, err := json.Marshal(stages)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(buildtestGateEnv(t, "DIR"), "matrix-state.json"), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
}

func loadMatrixStages(t *testing.T) map[string]matrixStage {
	t.Helper()
	encoded, err := os.ReadFile(filepath.Join(buildtestGateEnv(t, "DIR"), "matrix-state.json"))
	if err != nil {
		t.Fatal(err)
	}
	var stages map[string]matrixStage
	if err := json.Unmarshal(encoded, &stages); err != nil {
		t.Fatal(err)
	}
	return stages
}

func matrixCandidate(stage matrixStage) reliabilityapp.DeploymentCandidate {
	return reliabilityapp.DeploymentCandidate{
		IncidentID: stage.Fixture.IncidentID, OwnerUserID: stage.Fixture.Owner, ProjectID: stage.Fixture.ProjectID,
		InstallationID: stage.Fixture.Installation, TargetVersion: stage.Version,
		ExpectedRevision: stage.Revision, TaskID: stage.TaskID, ManifestDigest: stage.Digest,
	}
}

func newGateDeploymentController(t *testing.T, driver reliabilityapp.DeploymentDriver, window time.Duration) *reliabilityapp.DeploymentController {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), buildtestGateEnv(t, "DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	repo, err := reliabilitypostgres.New(pool)
	if err != nil {
		t.Fatal(err)
	}
	controller, err := reliabilityapp.NewDeploymentController(repo, driver, window)
	if err != nil {
		t.Fatal(err)
	}
	return controller
}

// TestRepairBuildTestDeploymentMatrix covers the deployment-phase failure
// matrix against the real Core staged lifecycle, real surfaces and the real
// durable ledger: startup faults, canary incidents, bounded rollback retries
// and duplicate offers.
func TestRepairBuildTestDeploymentMatrix(t *testing.T) {
	clients := newBuildtestClients(t)
	ctx := context.Background()
	stages := loadMatrixStages(t)

	t.Run("StartupFaultRollsBackPreviousPin", func(t *testing.T) {
		stage := stages["startup"]
		fixture, candidate := stage.Fixture, matrixCandidate(stage)
		driver := &scriptedDriver{clients: clients, failStarts: 8}
		controller := newGateDeploymentController(t, driver, 2*time.Second)
		if err := controller.Offer(ctx, candidate); err != nil {
			t.Fatal(err)
		}
		// Duplicate offer is a stable no-op on the same incident identity.
		if err := controller.Offer(ctx, candidate); err != nil {
			t.Fatalf("duplicate offer: %v", err)
		}
		deadline := time.Now().Add(60 * time.Second)
		state := ""
		for time.Now().Before(deadline) {
			_, _ = controller.Pass(ctx, time.Now(), 4)
			rows := buildtestQuery(t, `SELECT state FROM workos_reliability.deployment_ledger WHERE incident_id::text = $1`, fixture.IncidentID)
			if len(rows) > 0 {
				state, _ = rows[0]["state"].(string)
				if state == "rolled_back" || state == "failed" || state == "promoted" {
					break
				}
			}
			time.Sleep(300 * time.Millisecond)
		}
		if state != "rolled_back" {
			t.Fatalf("startup fault must roll back, got %s", state)
		}
		if got := currentInstallationVersion(t, clients, fixture); got != "1.0.0" {
			t.Fatalf("rollback did not restore the previous pin: %s", got)
		}
		if got := currentAppVersion(t, clients, fixture); got != "1.0.0" {
			t.Fatalf("rolled-back candidate was published: %s", got)
		}
		count := buildtestQuery(t, `SELECT count(*) AS c FROM workos_reliability.deployment_ledger WHERE incident_id::text = $1`, fixture.IncidentID)
		if count[0]["c"].(int64) != 1 {
			t.Fatal("duplicate offer created a second ledger row")
		}
	})

	t.Run("CanaryIncidentRollsBack", func(t *testing.T) {
		stage := stages["canary"]
		fixture, candidate := stage.Fixture, matrixCandidate(stage)
		driver := &scriptedDriver{clients: clients}
		controller := newGateDeploymentController(t, driver, 30*time.Second)
		if err := controller.Offer(ctx, candidate); err != nil {
			t.Fatal(err)
		}
		// Drive to canary, then open a fresh incident for the installation:
		// the ledger's next reconcile must observe it and roll back.
		deadline := time.Now().Add(60 * time.Second)
		for time.Now().Before(deadline) {
			_, _ = controller.Pass(ctx, time.Now(), 4)
			if got := currentInstallationVersion(t, clients, fixture); got == fixture.StagedLabel {
				break
			}
			time.Sleep(300 * time.Millisecond)
		}
		if got := currentInstallationVersion(t, clients, fixture); got != fixture.StagedLabel {
			t.Fatalf("canary never started: %s", got)
		}
		buildtestExec(t, `INSERT INTO workos_reliability.incidents (
    id, owner_user_id, project_id, app_instance_id, app_id, workload_id, workload_generation,
    violation, severity, summary, occurrence_digest, evidence_digest, state, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, 1, 'unexpected_exit', 'critical', 'canary fault fixture',
    $7, $8, 'open', now(), now())`,
			ids.UUIDv7{}.New(), fixture.Owner, fixture.ProjectID, fixture.Installation, fixture.AppID,
			ids.UUIDv7{}.New(), "sha256:"+strings.Repeat("c", 64), "sha256:"+strings.Repeat("d", 64))
		deadline = time.Now().Add(60 * time.Second)
		state := ""
		for time.Now().Before(deadline) {
			_, _ = controller.Pass(ctx, time.Now(), 4)
			rows := buildtestQuery(t, `SELECT state FROM workos_reliability.deployment_ledger WHERE incident_id::text = $1`, fixture.IncidentID)
			if len(rows) > 0 {
				state, _ = rows[0]["state"].(string)
				if state == "rolled_back" {
					break
				}
			}
			time.Sleep(300 * time.Millisecond)
		}
		if state != "rolled_back" {
			t.Fatalf("canary incident must roll back, got %s", state)
		}
		if got := currentInstallationVersion(t, clients, fixture); got != "1.0.0" {
			t.Fatalf("canary rollback did not restore the pin: %s", got)
		}
		if got := currentAppVersion(t, clients, fixture); got != "1.0.0" {
			t.Fatal("failed canary published the staged version")
		}
	})

	t.Run("RollbackRetriesAreBounded", func(t *testing.T) {
		stage := stages["rollback"]
		fixture, candidate := stage.Fixture, matrixCandidate(stage)
		driver := &scriptedDriver{clients: clients, failRollbacks: 2}
		controller := newGateDeploymentController(t, driver, 30*time.Second)
		if err := controller.Offer(ctx, candidate); err != nil {
			t.Fatal(err)
		}
		// Drive into the canary window, then open the incident that forces
		// the rollback whose first two attempts fail.
		enterCanary := time.Now().Add(60 * time.Second)
		for time.Now().Before(enterCanary) {
			_, _ = controller.Pass(ctx, time.Now(), 4)
			if got := currentInstallationVersion(t, clients, fixture); got == stage.Version {
				break
			}
			time.Sleep(300 * time.Millisecond)
		}
		buildtestExec(t, `INSERT INTO workos_reliability.incidents (
    id, owner_user_id, project_id, app_instance_id, app_id, workload_id, workload_generation,
    violation, severity, summary, occurrence_digest, evidence_digest, state, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, 1, 'unexpected_exit', 'critical', 'rollback retry fixture',
    $7, $8, 'open', now(), now())`,
			ids.UUIDv7{}.New(), fixture.Owner, fixture.ProjectID, fixture.Installation, fixture.AppID,
			ids.UUIDv7{}.New(), "sha256:"+strings.Repeat("f", 64), "sha256:"+strings.Repeat("1", 64))
		deadline := time.Now().Add(90 * time.Second)
		state := ""
		for time.Now().Before(deadline) {
			_, _ = controller.Pass(ctx, time.Now(), 4)
			rows := buildtestQuery(t, `SELECT state, attempts FROM workos_reliability.deployment_ledger WHERE incident_id::text = $1`, fixture.IncidentID)
			if len(rows) > 0 {
				state, _ = rows[0]["state"].(string)
				if state == "rolled_back" || state == "failed" {
					break
				}
			}
			time.Sleep(300 * time.Millisecond)
		}
		if state != "rolled_back" {
			t.Fatalf("transient rollback failures must retry to rolled_back, got %s", state)
		}
		if got := currentInstallationVersion(t, clients, fixture); got != "1.0.0" {
			t.Fatalf("retry rollback did not restore: %s", got)
		}
	})
}

func buildtestExec(t *testing.T, sql string, args ...any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, buildtestGateEnv(t, "DATABASE_URL"))
	if err != nil {
		t.Fatalf("connect scratch db: %v", err)
	}
	defer conn.Close(context.Background()) //nolint:errcheck
	if _, err := conn.Exec(ctx, sql, args...); err != nil {
		t.Fatalf("exec: %v", err)
	}
}

func buildtestProjectRevision(t *testing.T, fixture buildtestFixture) int64 {
	t.Helper()
	rows := buildtestQuery(t, `SELECT revision FROM workos_core.projects WHERE id::text = $1`, fixture.ProjectID)
	revision, _ := rows[0]["revision"].(int64)
	return revision
}
