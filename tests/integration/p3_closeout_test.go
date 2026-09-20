//go:build integration && repairbuildtest && p3delivery

package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
	appv1 "github.com/yangtao121/workos/gen/go/workos/app/v1"
	incidentv1 "github.com/yangtao121/workos/gen/go/workos/incident/v1"
	"github.com/yangtao121/workos/gen/go/workos/incident/v1/incidentv1connect"
	projectv1 "github.com/yangtao121/workos/gen/go/workos/project/v1"
	surfacev1 "github.com/yangtao121/workos/gen/go/workos/surface/v1"
	"github.com/yangtao121/workos/gen/go/workos/surface/v1/surfacev1connect"
	executionv1 "github.com/yangtao121/workos/gen/go/workos/taskexecution/v1"
	"github.com/yangtao121/workos/internal/platform/ids"
	"google.golang.org/protobuf/proto"
)

func p3FaultDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(os.Getenv("WORKOS_P3_GATE_DIR"), "faults")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func p3ResetFaults(t *testing.T) {
	t.Helper()
	dir := p3FaultDir(t)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		_ = os.Remove(filepath.Join(dir, entry.Name()))
	}
}

func p3ArmWait(t *testing.T, stage string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(p3FaultDir(t), "wait-"+stage), []byte(ids.UUIDv7{}.New()), 0o600); err != nil {
		t.Fatal(err)
	}
}

func p3ReleaseFault(t *testing.T, stage string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(p3FaultDir(t), "release-"+stage), []byte(stage+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func p3WaitArrived(t *testing.T, stage string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	path := filepath.Join(p3FaultDir(t), "arrived-"+stage)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for fault stage %s", stage)
}

func p3ArmDrop(t *testing.T, method string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(p3FaultDir(t), "drop-once-"+method), []byte("1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func p3DockerClient() *http.Client {
	socket := os.Getenv("WORKOS_RUNTIME_DOCKER_SOCKET")
	if socket == "" {
		socket = "/var/run/docker.sock"
	}
	return &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socket)
		},
	}}
}

func p3DockerDo(t *testing.T, method, path string, body io.Reader) []byte {
	t.Helper()
	request, err := http.NewRequest(method, "http://docker"+path, body)
	if err != nil {
		t.Fatal(err)
	}
	response, err := p3DockerClient().Do(request)
	if err != nil {
		t.Fatalf("docker %s %s: %v", method, path, err)
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if response.StatusCode >= 300 {
		t.Fatalf("docker %s %s: %d %s", method, path, response.StatusCode, raw)
	}
	return raw
}

func p3KillOwnedApp(t *testing.T, installation, digest string) string {
	t.Helper()
	namespace := os.Getenv("WORKOS_P3_GATE_NAMESPACE")
	filters, _ := json.Marshal(map[string][]string{
		"label": {
			"workos.purpose=runtime-app",
			"workos.runtime=" + namespace,
			"workos.workload.instance=" + installation,
			"workos.artifact.digest=" + digest,
		},
	})
	raw := p3DockerDo(t, "GET", "/containers/json?all=1&filters="+url.QueryEscape(string(filters)), nil)
	var items []struct {
		ID     string            `json:"Id"`
		Labels map[string]string `json:"Labels"`
		State  string            `json:"State"`
	}
	if err := json.Unmarshal(raw, &items); err != nil || len(items) != 1 {
		t.Fatalf("expected one owned A container, got %s", raw)
	}
	item := items[0]
	if item.Labels["workos.workload.instance"] != installation || item.Labels["workos.artifact.digest"] != digest {
		t.Fatalf("refusing to kill unmatched container labels: %+v", item.Labels)
	}
	if item.State != "running" {
		t.Fatalf("A container was not running: %+v", item)
	}
	p3DockerDo(t, "POST", "/containers/"+item.ID+"/kill?signal=SIGKILL", nil)
	return item.ID
}

func p3NoPublishSideEffects(t *testing.T, installation, pinBefore string) {
	t.Helper()
	ready := buildtestQuery(t, `SELECT id FROM workos_runtime.artifacts WHERE installation_id=$1 AND state='ready' AND origin='build_job'`, installation)
	if len(ready) != 0 {
		t.Fatalf("failed build produced a ready candidate: %+v", ready)
	}
	promoted := buildtestQuery(t, `SELECT incident_id FROM workos_reliability.deployment_ledger WHERE installation_id=$1 AND state IN ('promoted','canary','starting')`, installation)
	if len(promoted) != 0 {
		t.Fatalf("failed build created a deployment: %+v", promoted)
	}
	pin := buildtestQuery(t, `SELECT version FROM workos_core.project_app_installations WHERE id=$1`, installation)
	if len(pin) != 1 || fmt.Sprint(pin[0]["version"]) != pinBefore {
		t.Fatalf("installation pin drifted: before=%s after=%+v", pinBefore, pin)
	}
}

func TestP3Closeout(t *testing.T) {
	clients := newBuildtestClients(t)
	incidents := incidentv1connect.NewIncidentServiceClient(clients.http, clients.gatewayURL)

	t.Run("F01_supervised_docker_fault_publishes_B", func(t *testing.T) {
		p3ResetFaults(t)
		f := p3SeedNamed(t, clients, "P3 closeout supervised", false)
		p3Surface(t, clients, f, "P3-VALUE-0")
		rows := buildtestQuery(t, `SELECT id::text AS id, generation, artifact_digest FROM workos_runtime.workloads WHERE app_instance_id=$1 AND state='running'`, f.Installation)
		if len(rows) != 1 {
			t.Fatalf("expected one running A workload: %+v", rows)
		}
		workloadID := fmt.Sprint(rows[0]["id"])
		p3KillOwnedApp(t, f.Installation, f.Digest)
		var incidentID, occurrence, violation string
		p3Poll(t, "real supervision incident", func() bool {
			rows := buildtestQuery(t, `SELECT id::text AS id, occurrence_digest, violation, workload_id::text AS workload_id FROM workos_reliability.incidents WHERE app_instance_id=$1 AND workload_id=$2`, f.Installation, workloadID)
			if len(rows) == 0 {
				return false
			}
			incidentID = fmt.Sprint(rows[0]["id"])
			occurrence = fmt.Sprint(rows[0]["occurrence_digest"])
			violation = fmt.Sprint(rows[0]["violation"])
			return occurrence != ""
		})
		if occurrence == "" || incidentID == "" {
			t.Fatal("incident missing occurrence identity")
		}
		t.Logf("supervised incident %s violation=%s occurrence=%s workload=%s", incidentID, violation, occurrence, workloadID)
		listed, err := incidents.ListIncidents(context.Background(), connect.NewRequest(&incidentv1.ListIncidentsRequest{ProjectId: f.Project}))
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, item := range listed.Msg.GetIncidents() {
			if item.GetId() == incidentID && item.GetWorkloadId() == workloadID {
				found = true
			}
		}
		if !found {
			t.Fatalf("public IncidentService did not project the supervised incident: %+v", listed.Msg.GetIncidents())
		}
		time.Sleep(2 * time.Second)
		again := buildtestQuery(t, `SELECT id FROM workos_reliability.incidents WHERE occurrence_digest=$1`, occurrence)
		if len(again) != 1 {
			t.Fatalf("replayed observation created duplicate occurrence: %+v", again)
		}
		status := p3Release(t, clients, f, "published")
		f.CandidateVersion = status.GetCandidateVersion()
		if status.GetCandidateArtifactDigest() == "" || status.GetCandidateArtifactDigest() == f.Digest {
			t.Fatalf("supervised repair did not bind distinct B: %+v", status)
		}
		p3Surface(t, clients, f, "P3-VALUE-42")
		raw, _ := json.Marshal(f)
		if err := os.WriteFile(filepath.Join(os.Getenv("WORKOS_P3_GATE_DIR"), "closeout-f02.json"), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("F02_manual_rollback_after_supervised_kill", func(t *testing.T) {
		p3ResetFaults(t)
		f := p3SeedNamed(t, clients, "P3 closeout kill then rollback", false)
		p3Surface(t, clients, f, "P3-VALUE-0")
		// The supervised crash leaves A's engine object behind while the
		// repair publishes B; the user's rollback must then relaunch A by
		// converging that stale object, not by bouncing on reconcile backoff
		// past a desktop open's patience.
		p3KillOwnedApp(t, f.Installation, f.Digest)
		p3Poll(t, "supervised incident after kill", func() bool {
			return len(buildtestQuery(t, `SELECT id FROM workos_reliability.incidents WHERE app_instance_id=$1`, f.Installation)) > 0
		})
		status := p3Release(t, clients, f, "published")
		if status.GetCandidateArtifactDigest() == f.Digest {
			t.Fatalf("repair did not bind distinct B: %+v", status)
		}
		p3Surface(t, clients, f, "P3-VALUE-42")
		project, err := clients.projects.GetProject(context.Background(), connect.NewRequest(&projectv1.GetProjectRequest{ProjectId: f.Project}))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := clients.install.RollbackAppVersion(context.Background(), connect.NewRequest(&appv1.RollbackAppVersionRequest{
			IdempotencyKey: ids.UUIDv7{}.New(), ProjectId: f.Project, InstallationId: f.Installation,
			ExpectedProjectRevision: project.Msg.GetProject().GetRevision(),
		})); err != nil {
			t.Fatal(err)
		}
		// The gateway injects the trusted owner identity; a direct runtime
		// client would be unauthenticated and never reach Ensure at all.
		surfaces := surfacev1connect.NewSurfaceServiceClient(clients.http, clients.gatewayURL)
		key := ids.UUIDv7{}.New()
		lastFailure := ""
		lastLogged := ""
		started := time.Now()
		deadline := started.Add(90 * time.Second)
		for time.Now().Before(deadline) {
			response, surfaceErr := surfaces.CreateSurface(context.Background(), connect.NewRequest(&surfacev1.CreateSurfaceRequest{
				IdempotencyKey: key, ProjectId: f.Project, AppInstanceId: f.Installation,
				DeviceClass: surfacev1.DeviceClass_DEVICE_CLASS_DESKTOP,
				Viewport:    &surfacev1.Viewport{Width: 1280, Height: 800, PixelRatio: 1},
			}))
			if surfaceErr == nil {
				reply, getErr := clients.http.Get(clients.gatewayURL + response.Msg.GetSession().GetUrl())
				if getErr == nil {
					body, _ := io.ReadAll(io.LimitReader(reply.Body, 16384))
					reply.Body.Close()
					if reply.StatusCode == 200 && strings.Contains(string(body), "P3-VALUE-0") {
						t.Logf("rolled-back A relaunch converged in %s", time.Since(started))
						return
					}
					lastFailure = fmt.Sprintf("surface body: %d %.80s", reply.StatusCode, body)
				} else {
					lastFailure = "surface fetch: " + getErr.Error()
				}
			} else {
				lastFailure = "surface create: " + surfaceErr.Error()
			}
			if lastFailure != "" && lastFailure != lastLogged {
				lastLogged = lastFailure
				t.Log(lastLogged)
			}
			time.Sleep(500 * time.Millisecond)
		}
		t.Fatalf("rolled-back A did not relaunch within the bounded window: %s", lastFailure)
	})

	t.Run("F03_docker_build_failure_zero_publish", func(t *testing.T) {
		p3ResetFaults(t)
		f := p3SeedNamed(t, clients, "P3 closeout buildfail", false)
		p3Surface(t, clients, f, "P3-VALUE-0")
		pin := "1.0.0"
		taskID := p3FailingJob(t, clients, f, "buildfail")
		verdict := waitForBuildtestJob(t, clients, taskID, func(state string) bool { return state == "failed" || state == "cancelled" })
		if verdict.State != "failed" || verdict.Failure != "build-failed" {
			t.Fatalf("expected docker build-failed, got %+v", verdict)
		}
		p3NoPublishSideEffects(t, f.Installation, pin)
		p3Surface(t, clients, f, "P3-VALUE-0")
	})

	t.Run("F04_docker_test_failure_keeps_preparing_unready", func(t *testing.T) {
		p3ResetFaults(t)
		f := p3SeedNamed(t, clients, "P3 closeout testfail", false)
		taskID := p3FailingJob(t, clients, f, "testfail")
		verdict := waitForBuildtestJob(t, clients, taskID, func(state string) bool { return state == "failed" || state == "cancelled" })
		if verdict.State != "failed" {
			t.Fatalf("expected failed docker test, got %+v", verdict)
		}
		p3NoPublishSideEffects(t, f.Installation, "1.0.0")
		ready := buildtestQuery(t, `SELECT id, state FROM workos_runtime.artifacts WHERE task_id=$1`, taskID)
		for _, row := range ready {
			if fmt.Sprint(row["state"]) == "ready" {
				t.Fatalf("test failure froze a ready bundle: %+v", ready)
			}
		}
	})

	t.Run("F05_missing_output_rejected", func(t *testing.T) {
		p3ResetFaults(t)
		f := p3SeedNamed(t, clients, "P3 closeout missing-output", false)
		taskID := p3FailingJob(t, clients, f, "missing-output")
		verdict := waitForBuildtestJob(t, clients, taskID, func(state string) bool { return state == "failed" || state == "cancelled" })
		if verdict.State != "failed" {
			t.Fatalf("missing output must fail, got %+v", verdict)
		}
		p3NoPublishSideEffects(t, f.Installation, "1.0.0")
	})

	t.Run("F06_cancel_before_success", func(t *testing.T) {
		p3ResetFaults(t)
		f := p3SeedNamed(t, clients, "P3 closeout cancel", false)
		p3ArmWait(t, "after-engine")
		t.Cleanup(func() {
			p3ReleaseFault(t, "after-engine")
			_ = os.Remove(filepath.Join(p3FaultDir(t), "wait-after-engine"))
		})
		taskID := p3FailingJob(t, clients, f, "slow")
		p3WaitArrived(t, "after-engine")
		if _, err := clients.builds.CancelBuildTest(context.Background(), connect.NewRequest(&executionv1.CancelBuildTestRequest{TaskId: taskID})); err != nil {
			t.Fatal(err)
		}
		p3ReleaseFault(t, "after-engine")
		verdict := waitForBuildtestJob(t, clients, taskID, func(state string) bool {
			return state == "cancelled" || state == "failed" || state == "succeeded"
		})
		if verdict.State != "cancelled" {
			t.Fatalf("committed cancel did not own the terminal verdict: %+v", verdict)
		}
		p3NoPublishSideEffects(t, f.Installation, "1.0.0")
	})

	for _, stage := range []string{"artifact-bytes", "artifact-preparing", "before-verdict"} {
		stage := stage
		t.Run("F06_cancel_wins_at_"+stage, func(t *testing.T) {
			p3ResetFaults(t)
			f := p3SeedNamed(t, clients, "P3 closeout cancel "+stage, false)
			taskID := ids.UUIDv7{}.New()
			p3ArmWait(t, stage)
			t.Cleanup(func() {
				p3ReleaseFault(t, stage)
				_ = os.Remove(filepath.Join(p3FaultDir(t), "wait-"+stage))
			})
			if _, err := p3SubmitVariant(t, clients, f, taskID, ids.UUIDv7{}.New(), "fast"); err != nil {
				t.Fatal(err)
			}
			p3WaitArrived(t, stage)
			if _, err := clients.builds.CancelBuildTest(context.Background(), connect.NewRequest(&executionv1.CancelBuildTestRequest{TaskId: taskID})); err != nil {
				t.Fatalf("cancel at %s: %v", stage, err)
			}
			p3ReleaseFault(t, stage)
			verdict := p3WaitJobTerminal(t, clients, taskID, func(state string) bool {
				return state == "cancelled" || state == "failed" || state == "succeeded"
			})
			if verdict.State != "cancelled" {
				t.Fatalf("cancel committed before the success verdict but lost at %s: %+v", stage, verdict)
			}
			p3NoPublishSideEffects(t, f.Installation, "1.0.0")
			ready := buildtestQuery(t, `SELECT id FROM workos_runtime.artifacts WHERE task_id=$1 AND state='ready'`, taskID)
			if len(ready) != 0 {
				t.Fatalf("cancel at %s produced a ready artifact: %+v", stage, ready)
			}
		})
	}

	t.Run("F07_idempotent_submit_and_drift", func(t *testing.T) {
		p3ResetFaults(t)
		f := p3SeedNamed(t, clients, "P3 closeout replay", false)
		taskID := ids.UUIDv7{}.New()
		incident := ids.UUIDv7{}.New()
		base, err := p3BuildTestRequest(t, clients, f, taskID, incident, "buildfail")
		if err != nil {
			t.Fatal(err)
		}
		type submitResult struct {
			response *connect.Response[executionv1.SubmitBuildTestResponse]
			err      error
		}
		start := make(chan struct{})
		results := make(chan submitResult, 4)
		for range 4 {
			go func() {
				<-start
				copy := proto.Clone(base.Msg).(*executionv1.SubmitBuildTestRequest)
				response, err := clients.builds.SubmitBuildTest(context.Background(), connect.NewRequest(copy))
				results <- submitResult{response: response, err: err}
			}()
		}
		close(start)
		winner := ""
		createdCount := 0
		for range 4 {
			result := <-results
			if result.err != nil {
				t.Fatalf("same-input concurrent submit failed: %v", result.err)
			}
			if winner == "" {
				winner = result.response.Msg.GetJobId()
			}
			if result.response.Msg.GetJobId() != winner {
				t.Fatalf("concurrent submits returned different jobs: winner=%s got=%s", winner, result.response.Msg.GetJobId())
			}
			if result.response.Msg.GetCreated() {
				createdCount++
			}
		}
		if createdCount != 1 {
			t.Fatalf("same-input concurrency created %d jobs, want exactly one", createdCount)
		}

		otherDigest := "sha256:" + strings.Repeat("a", 64)
		mutations := []struct {
			name   string
			mutate func(*executionv1.SubmitBuildTestRequest)
		}{
			{"owner", func(r *executionv1.SubmitBuildTestRequest) { r.Job.OwnerUserId = ids.UUIDv7{}.New() }},
			{"project", func(r *executionv1.SubmitBuildTestRequest) { r.Job.ProjectId = ids.UUIDv7{}.New() }},
			{"installation", func(r *executionv1.SubmitBuildTestRequest) { r.Job.InstallationId = ids.UUIDv7{}.New() }},
			{"incident", func(r *executionv1.SubmitBuildTestRequest) { r.Job.IncidentId = ids.UUIDv7{}.New() }},
			{"app", func(r *executionv1.SubmitBuildTestRequest) { r.Job.Input.Target.AppId += "-drift" }},
			{"source-bundle-id", func(r *executionv1.SubmitBuildTestRequest) { r.InputFacts.SourceBundleId = ids.UUIDv7{}.New() }},
			{"source-digest", func(r *executionv1.SubmitBuildTestRequest) { r.InputFacts.SourceDigest = otherDigest }},
			{"manifest-digest", func(r *executionv1.SubmitBuildTestRequest) { r.InputFacts.ManifestDigest = otherDigest }},
			{"build-command", func(r *executionv1.SubmitBuildTestRequest) {
				r.Job.Input.BuildCommand = append(r.Job.Input.BuildCommand, "#drift")
			}},
			{"test-command", func(r *executionv1.SubmitBuildTestRequest) {
				r.Job.Input.TestCommand = append(r.Job.Input.TestCommand, "#drift")
			}},
			{"output-directory", func(r *executionv1.SubmitBuildTestRequest) { r.Job.Input.OutputDirectory = "other-dist" }},
			{"runtime-command", func(r *executionv1.SubmitBuildTestRequest) { r.Job.Input.RuntimeCommand = []string{"/app/other"} }},
			{"candidate-bytes", func(r *executionv1.SubmitBuildTestRequest) {
				r.Job.CandidateFiles[0].Content = append(r.Job.CandidateFiles[0].Content, '\n')
			}},
			{"base-image", func(r *executionv1.SubmitBuildTestRequest) {
				r.Job.Input.BaseImage = p3Image + "-drift"
				r.InputFacts.BaseImage = p3Image + "-drift"
			}},
		}
		for _, mutation := range mutations {
			t.Run(mutation.name, func(t *testing.T) {
				request := proto.Clone(base.Msg).(*executionv1.SubmitBuildTestRequest)
				mutation.mutate(request)
				_, err := clients.builds.SubmitBuildTest(context.Background(), connect.NewRequest(request))
				if connect.CodeOf(err) != connect.CodeAborted {
					t.Fatalf("same-task %s drift must abort without changing the winner, got %v", mutation.name, err)
				}
			})
		}
		inconsistentFacts := proto.Clone(base.Msg).(*executionv1.SubmitBuildTestRequest)
		inconsistentFacts.InputFacts.BaseImage = "golang:latest"
		if _, err := clients.builds.SubmitBuildTest(context.Background(), connect.NewRequest(inconsistentFacts)); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Fatalf("facts disagreeing with the recipe must be rejected, got %v", err)
		}
		if rows := buildtestQuery(t, `SELECT id FROM workos_runtime.build_jobs WHERE task_id=$1`, taskID); len(rows) != 1 {
			t.Fatalf("drifted requests replaced or duplicated the original winner: %+v", rows)
		}
	})

	t.Run("F08_lease_handoff", func(t *testing.T) {
		p3ResetFaults(t)
		f := p3SeedNamed(t, clients, "P3 closeout lease", false)
		if err := os.WriteFile(filepath.Join(p3FaultDir(t), "expire-lease"), []byte("1\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		p3ArmWait(t, "before-verdict")
		t.Cleanup(func() { p3ResetFaults(t) })
		taskID := p3FailingJob(t, clients, f, "fast")
		p3WaitArrived(t, "before-verdict")
		p3CompetingWorker(t, clients, taskID)
		p3ReleaseFault(t, "before-verdict")
		_ = os.Remove(filepath.Join(p3FaultDir(t), "expire-lease"))

	})

	t.Run("F09_drop_reply_submit_replays", func(t *testing.T) {
		p3ResetFaults(t)
		f := p3SeedNamed(t, clients, "P3 closeout drop-reply", false)
		taskID := ids.UUIDv7{}.New()
		incident := ids.UUIDv7{}.New()
		p3ArmDrop(t, "SubmitBuildTest")
		_, firstErr := p3SubmitVariant(t, clients, f, taskID, incident, "buildfail")
		if firstErr == nil {
			t.Fatal("first submit should not deliver a response after drop-reply")
		}
		second, err := p3SubmitVariant(t, clients, f, taskID, incident, "buildfail")
		if err != nil {
			t.Fatalf("replay after committed drop-reply: %v", err)
		}
		if second.Msg.GetJobId() == "" {
			t.Fatal("replay missing job identity")
		}
		rows := buildtestQuery(t, `SELECT id FROM workos_runtime.build_jobs WHERE task_id=$1`, taskID)
		if len(rows) != 1 {
			t.Fatalf("drop-reply duplicated jobs: %+v", rows)
		}
	})

	t.Run("F24_gateway_hides_private_buildtest", func(t *testing.T) {
		response, err := clients.http.Post(clients.gatewayURL+"/workos.taskexecution.v1.BuildTestService/GetBuildTest", "application/json", strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if response.StatusCode == 200 {
			t.Fatal("gateway must not serve the private BuildTest service")
		}
	})

	t.Run("F09_operator_import_drop_reply_replays_one_identity", func(t *testing.T) {
		p3ResetFaults(t)
		path, digest := p3TestBundle(t)
		key := ids.UUIDv7{}.New()
		p3ArmDrop(t, "ImportArtifact")
		if output, err := p3ImportArtifact(t, p3Owner, "p3-import-replay", key, path, digest); err == nil {
			t.Fatalf("first import should commit but lose its reply, output=%s", output)
		}
		first := buildtestQuery(t, `SELECT id::text AS id, digest, app_id, origin FROM workos_runtime.artifacts WHERE owner_user_id=$1 AND idempotency_key=$2`, p3Owner, key)
		if len(first) != 1 || first[0]["digest"] != digest || first[0]["app_id"] != "p3-import-replay" || first[0]["origin"] != "operator_import" {
			t.Fatalf("dropped reply did not leave one precise committed import: %+v", first)
		}
		replayID, err := p3ImportArtifact(t, p3Owner, "p3-import-replay", key, path, digest)
		if err != nil || replayID != fmt.Sprint(first[0]["id"]) {
			t.Fatalf("same-key retry did not recover first artifact identity: id=%s rows=%+v err=%v", replayID, first, err)
		}
		if count := buildtestQuery(t, `SELECT id FROM workos_runtime.artifacts WHERE owner_user_id=$1 AND idempotency_key=$2`, p3Owner, key); len(count) != 1 {
			t.Fatalf("drop-reply duplicated import metadata: %+v", count)
		}
	})

	t.Run("F10_same_bytes_keep_distinct_source_metadata", func(t *testing.T) {
		p3ResetFaults(t)
		path, digest := p3TestBundle(t)
		firstKey, secondKey := ids.UUIDv7{}.New(), ids.UUIDv7{}.New()
		firstID, err := p3ImportArtifact(t, p3Owner, "p3-source-one", firstKey, path, digest)
		if err != nil {
			t.Fatal(err)
		}
		secondID, err := p3ImportArtifact(t, p3Owner, "p3-source-two", secondKey, path, digest)
		if err != nil {
			t.Fatal(err)
		}
		if firstID == secondID {
			t.Fatalf("separate import sources shared metadata identity %s", firstID)
		}
		rows := buildtestQuery(t, `SELECT id::text AS id, idempotency_key, app_id FROM workos_runtime.artifacts WHERE owner_user_id=$1 AND digest=$2 AND idempotency_key IN ($3,$4) ORDER BY idempotency_key`, p3Owner, digest, firstKey, secondKey)
		if len(rows) != 2 || rows[0]["id"] == rows[1]["id"] || rows[0]["app_id"] == rows[1]["app_id"] {
			t.Fatalf("identical bytes collapsed independent source metadata: %+v", rows)
		}
		storedPath := p3ArtifactBundlePath(t, p3Owner, digest)
		if info, err := os.Stat(storedPath); err != nil || !info.Mode().IsRegular() {
			t.Fatalf("shared content-addressed bytes are missing: info=%v err=%v", info, err)
		}
		original, err := os.ReadFile(storedPath)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.WriteFile(storedPath, original, 0o600) })
		if err := os.WriteFile(storedPath, append(append([]byte(nil), original...), 0), 0o600); err != nil {
			t.Fatal(err)
		}
		corruptKey := ids.UUIDv7{}.New()
		if _, err := p3ImportArtifact(t, p3Owner, "p3-source-corrupt-reuse", corruptKey, path, digest); err == nil {
			t.Fatal("corrupted content-addressed bytes were accepted as a dedupe hit")
		}
		if rows := buildtestQuery(t, `SELECT id FROM workos_runtime.artifacts WHERE owner_user_id=$1 AND idempotency_key=$2`, p3Owner, corruptKey); len(rows) != 0 {
			t.Fatalf("corrupt dedupe refusal left metadata: %+v", rows)
		}
		if err := os.WriteFile(storedPath, original, 0o600); err != nil {
			t.Fatal(err)
		}
	})
}

func p3FailingJob(t *testing.T, clients *buildtestClients, f p3Fixture, variant string) string {
	t.Helper()
	taskID := ids.UUIDv7{}.New()
	response, err := p3SubmitVariant(t, clients, f, taskID, ids.UUIDv7{}.New(), variant)
	if err != nil {
		t.Fatal(err)
	}
	if response.Msg.GetJobId() == "" {
		t.Fatal("missing job id")
	}
	return taskID
}

func p3SubmitVariant(t *testing.T, clients *buildtestClients, f p3Fixture, taskID, incidentID, variant string) (*connect.Response[executionv1.SubmitBuildTestResponse], error) {
	request, err := p3BuildTestRequest(t, clients, f, taskID, incidentID, variant)
	if err != nil {
		return nil, err
	}
	return clients.builds.SubmitBuildTest(context.Background(), request)
}

func p3BuildTestRequest(t *testing.T, clients *buildtestClients, f p3Fixture, taskID, incidentID, variant string) (*connect.Request[executionv1.SubmitBuildTestRequest], error) {
	t.Helper()
	ctx := context.Background()
	files := []*appv1.AppSourceFile{
		{Path: "go.mod", Content: []byte("module fixture\n\ngo 1.26\n")},
		{Path: "main.go", Content: []byte("package main\nfunc value() int { return 0 }\nfunc main() {}\n")},
		{Path: "main_test.go", Content: []byte("package main\nimport \"testing\"\nfunc TestValue(t *testing.T){if value()!=0{t.Fatal(1)}}\n")},
	}
	buildCmd := []string{"sh", "-c", "mkdir -p dist && CGO_ENABLED=0 go build -trimpath -o dist/server ."}
	testCmd := []string{"go", "test", "./..."}
	outDir := "dist"
	baseImage := p3Image
	switch variant {
	case "fast":
		buildCmd = []string{"sh", "-c", "mkdir -p dist; printf '#!/bin/sh\\nexit 0\\n' > dist/server; chmod 755 dist/server"}
		testCmd = []string{"sh", "-c", "test -x dist/server"}
	case "buildfail":
		files[1].Content = []byte("package main\nvar _ = undefined\nfunc main() {}\n")
	case "testfail":
		files[2].Content = []byte("package main\nimport \"testing\"\nfunc TestValue(t *testing.T){t.Fatal(\"fail\")}\n")
	case "missing-output":
		buildCmd = []string{"sh", "-c", "echo ok"}
		outDir = "dist"
	case "slow":
		buildCmd = []string{"sh", "-c", "sleep 20 && mkdir -p dist && CGO_ENABLED=0 go build -trimpath -o dist/server ."}
	case "unique":
		// Content-addressed storage dedupes identical builds across tasks:
		// window subtests that assert per-task rows or fresh bundle bytes
		// embed the task id so every digest is genuinely new.
		files[1].Content = []byte("package main\nvar unique = \"" + taskID + "\"\nfunc value() int { return 0 }\nfunc main() { _ = unique }\n")
	case "missing-image":
		// A well-formed digest that no registry can serve: the engine must
		// fail closed instead of falling back to a mutable tag.
		baseImage = "golang@sha256:" + strings.Repeat("0", 64)
	}
	source, err := clients.sources.CreateAppSourceBundle(ctx, connect.NewRequest(&appv1.CreateAppSourceBundleRequest{IdempotencyKey: taskID + "-src-" + variant, Files: files}))
	if err != nil {
		return nil, err
	}
	listed, err := clients.install.ListInstalledApps(ctx, connect.NewRequest(&appv1.ListInstalledAppsRequest{ProjectId: f.Project}))
	if err != nil {
		return nil, err
	}
	manifest := ""
	for _, item := range listed.Msg.GetInstallations() {
		if item.GetId() == f.Installation {
			manifest = item.GetManifestDigest()
		}
	}
	if manifest == "" {
		return nil, fmt.Errorf("missing installation manifest digest")
	}
	request := connect.NewRequest(&executionv1.SubmitBuildTestRequest{
		Job: &executionv1.BuildTestJob{
			TaskId: taskID, IncidentId: incidentID, OwnerUserId: p3Owner,
			ProjectId: f.Project, InstallationId: f.Installation,
			CandidateFiles: files,
			Input: &executionv1.RepairBuildInput{
				Target:          &agentv1.RepairTarget{AppId: f.App, AppInstanceId: f.Installation},
				BaseImage:       baseImage,
				BuildCommand:    buildCmd,
				TestCommand:     testCmd,
				OutputDirectory: outDir,
				RuntimeCommand:  []string{"/app/server"},
			},
		},
		InputFacts: &executionv1.BuildTestInputFacts{
			SourceBundleId: source.Msg.GetBundle().GetId(), SourceDigest: source.Msg.GetBundle().GetDigest(),
			ManifestDigest: manifest, BaseImage: baseImage,
		},
	})
	return request, nil
}
