//go:build integration && repairbuildtest && p3delivery

package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	surfacev1 "github.com/yangtao121/workos/gen/go/workos/surface/v1"
	"github.com/yangtao121/workos/gen/go/workos/surface/v1/surfacev1connect"
	executionv1 "github.com/yangtao121/workos/gen/go/workos/taskexecution/v1"
	"github.com/yangtao121/workos/internal/platform/identity"
	"github.com/yangtao121/workos/internal/platform/ids"
)

// p3ServiceContainerID resolves one gate-owned compose service container by
// the exact (project, service) label pair; a foreign stack sharing the
// docker socket is never addressed.
func p3ServiceContainerID(t *testing.T, service string) string {
	t.Helper()
	namespace := os.Getenv("WORKOS_P3_GATE_NAMESPACE")
	filters, _ := json.Marshal(map[string][]string{
		"label": {
			"com.docker.compose.project=" + namespace,
			"com.docker.compose.service=" + service,
		},
	})
	raw := p3DockerDo(t, "GET", "/containers/json?all=1&filters="+url.QueryEscape(string(filters)), nil)
	var items []struct {
		ID string `json:"Id"`
	}
	if err := json.Unmarshal(raw, &items); err != nil || len(items) != 1 {
		t.Fatalf("expected one %s container in gate namespace %s, got %s", service, namespace, raw)
	}
	return items[0].ID
}

// p3ContainerAction drives kill/start/restart on a gate-owned container.
// Docker answers 304 for start on a running container; only real failures
// are fatal.
func p3ContainerAction(t *testing.T, containerID, action string) {
	t.Helper()
	path := "/containers/" + containerID + "/" + action
	if action == "restart" {
		path += "?t=10"
	}
	request, err := http.NewRequest("POST", "http://docker"+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := p3DockerClient().Do(request)
	if err != nil {
		t.Fatalf("docker %s: %v", action, err)
	}
	defer response.Body.Close()
	raw, _ := readAllLimited(response.Body)
	if response.StatusCode >= 500 {
		t.Fatalf("docker %s: %d %s", action, response.StatusCode, raw)
	}
}

func readAllLimited(body interface{ Read([]byte) (int, error) }) ([]byte, error) {
	buffer := make([]byte, 4096)
	total := []byte{}
	for {
		n, err := body.Read(buffer)
		total = append(total, buffer[:n]...)
		if err != nil || len(total) > 1<<20 {
			return total, err
		}
	}
}

// p3EnsureServiceRunning is the cleanup safety net: a window test that
// crashed a process must hand the stack back healthy for later gate steps.
func p3EnsureServiceRunning(t *testing.T, service string) {
	t.Helper()
	containerID := p3ServiceContainerID(t, service)
	raw := p3DockerDo(t, "GET", "/containers/"+containerID+"/json", nil)
	var state struct {
		State struct {
			Running bool `json:"Running"`
		} `json:"State"`
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	if !state.State.Running {
		p3ContainerAction(t, containerID, "start")
	}
}

// p3WaitHTTPReady polls until a restarted process answers HTTP again; any
// complete response proves the listener recovered.
func p3WaitHTTPReady(t *testing.T, target string) {
	t.Helper()
	client := &http.Client{Timeout: 2 * time.Second}
	defer client.CloseIdleConnections()
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		response, err := client.Post(target, "application/json", strings.NewReader("{}"))
		if err == nil {
			response.Body.Close()
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s to answer HTTP", target)
}

func p3OwnedContainerCount(t *testing.T, installation string) int {
	t.Helper()
	namespace := os.Getenv("WORKOS_P3_GATE_NAMESPACE")
	filters, _ := json.Marshal(map[string][]string{
		"label": {
			"workos.purpose=runtime-app",
			"workos.runtime=" + namespace,
			"workos.workload.instance=" + installation,
		},
	})
	raw := p3DockerDo(t, "GET", "/containers/json?all=1&filters="+url.QueryEscape(string(filters)), nil)
	var items []map[string]any
	if err := json.Unmarshal(raw, &items); err != nil {
		t.Fatal(err)
	}
	return len(items)
}

// The artifact files adapter is content-addressed per owner:
// <root>/<owner uuid>/<digest hex>.bundle. The test runner mounts the same
// gate artifact directory the runtime host writes.
func p3ArtifactBundlePath(t *testing.T, owner, digest string) string {
	t.Helper()
	return filepath.Join(os.Getenv("WORKOS_P3_GATE_DIR"), "artifacts", owner, strings.TrimPrefix(digest, "sha256:")+".bundle")
}

func p3OwnerBundleCount(t *testing.T, owner string) int {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(os.Getenv("WORKOS_P3_GATE_DIR"), "artifacts", owner))
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".bundle") {
			count++
		}
	}
	return count
}

// p3WaitJobTerminal is waitForBuildtestJob with a restart-sized deadline:
// after a crash the takeover re-runs the whole docker build.
func p3WaitJobTerminal(t *testing.T, clients *buildtestClients, taskID string, terminal func(state string) bool) buildtestTerminal {
	t.Helper()
	ctx := context.Background()
	deadline := time.Now().Add(5 * time.Minute)
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

func p3VersionHistorySources(t *testing.T, installation string) map[string]int64 {
	t.Helper()
	rows := buildtestQuery(t, `SELECT source, count(*) AS n FROM workos_core.project_app_installation_versions WHERE installation_id=$1 GROUP BY source`, installation)
	sources := map[string]int64{}
	for _, row := range rows {
		if n, ok := row["n"].(int64); ok {
			sources[fmt.Sprint(row["source"])] = n
		}
	}
	return sources
}

// TestP3CloseoutWindows covers the D04 persistence windows: each subtest
// stops a real process exactly at a committed boundary, restarts it, and
// asserts the recovered identity plus the bounded final result.
func TestP3CloseoutWindows(t *testing.T) {
	clients := newBuildtestClients(t)

	t.Run("F11_restart_between_bytes_and_metadata", func(t *testing.T) {
		p3ResetFaults(t)
		t.Cleanup(func() { p3EnsureServiceRunning(t, "runtime") })
		f := p3SeedNamed(t, clients, "P3 closeout F11 bytes", false)
		before := p3OwnerBundleCount(t, p3Owner)
		p3ArmWait(t, "artifact-bytes")
		taskID := ids.UUIDv7{}.New()
		incident := ids.UUIDv7{}.New()
		if _, err := p3SubmitVariant(t, clients, f, taskID, incident, "unique"); err != nil {
			t.Fatal(err)
		}
		p3WaitArrived(t, "artifact-bytes")
		// Window fact: the bundle bytes are durable while the metadata is not.
		if got := p3OwnerBundleCount(t, p3Owner); got != before+1 {
			t.Fatalf("orphan bundle bytes missing: before=%d after=%d", before, got)
		}
		if rows := buildtestQuery(t, `SELECT id FROM workos_runtime.artifacts WHERE task_id=$1`, taskID); len(rows) != 0 {
			t.Fatalf("metadata committed inside the crash window: %+v", rows)
		}
		p3ContainerAction(t, p3ServiceContainerID(t, "runtime"), "kill")
		p3ResetFaults(t)
		p3ContainerAction(t, p3ServiceContainerID(t, "runtime"), "start")
		p3WaitHTTPReady(t, clients.runtimeURL+"/workos.taskexecution.v1.BuildTestService/GetBuildTest")
		// A file without a row is never ready; the same idempotent submit
		// converges on a fresh lease instead of failing closed forever.
		if _, err := p3SubmitVariant(t, clients, f, taskID, incident, "unique"); err != nil {
			t.Fatal(err)
		}
		verdict := p3WaitJobTerminal(t, clients, taskID, func(state string) bool { return state == "succeeded" })
		t.Logf("F11 converged verdict job=%s attempts=%d", verdict.JobID, verdict.Attempts)
		rows := buildtestQuery(t, `SELECT id::text, digest FROM workos_runtime.artifacts WHERE task_id=$1`, taskID)
		if len(rows) != 1 {
			t.Fatalf("expected one artifact row after convergence: %+v", rows)
		}
		// Once the replayed verdict is durable the row may legitimately be
		// ready already (reconcile promotes from verdict + verified bytes) or
		// still preparing (GetBuildArtifact is the authoritative promoter);
		// the verdict gating itself was proven in the crash window above.
		reply, err := clients.builds.GetBuildArtifact(context.Background(), connect.NewRequest(&executionv1.GetBuildArtifactRequest{ArtifactId: fmt.Sprint(rows[0]["id"])}))
		if err != nil {
			t.Fatal(err)
		}
		if reply.Msg.GetArtifact().GetState() != "ready" || reply.Msg.GetArtifact().GetArtifactDigest() == "" {
			t.Fatalf("converged artifact is not ready: %+v", reply.Msg.GetArtifact())
		}
		if reply.Msg.GetArtifact().GetArtifactDigest() != fmt.Sprint(rows[0]["digest"]) {
			t.Fatalf("reply digest drifted from the durable row: %+v vs %+v", reply.Msg.GetArtifact(), rows[0])
		}
		// The retry deduplicated onto the orphan's content path: the owner
		// still holds exactly one bundle per digest, so quota accounting
		// stays bounded without cleaning foreign files.
		if got := p3OwnerBundleCount(t, p3Owner); got != before+1 {
			t.Fatalf("retry duplicated bundle bytes: before=%d after=%d", before, got)
		}
		if ledger := buildtestQuery(t, `SELECT incident_id FROM workos_reliability.deployment_ledger WHERE installation_id=$1`, f.Installation); len(ledger) != 0 {
			t.Fatalf("build convergence must not deploy: %+v", ledger)
		}
		if pin := buildtestQuery(t, `SELECT version FROM workos_core.project_app_installations WHERE id=$1`, f.Installation); len(pin) != 1 || fmt.Sprint(pin[0]["version"]) != "1.0.0" {
			t.Fatalf("installation pin drifted: %+v", pin)
		}
	})

	t.Run("F12_restart_between_preparing_and_verdict", func(t *testing.T) {
		p3ResetFaults(t)
		t.Cleanup(func() { p3EnsureServiceRunning(t, "runtime") })
		f := p3SeedNamed(t, clients, "P3 closeout F12 preparing", false)
		p3ArmWait(t, "before-verdict")
		taskID := ids.UUIDv7{}.New()
		incident := ids.UUIDv7{}.New()
		if _, err := p3SubmitVariant(t, clients, f, taskID, incident, "unique"); err != nil {
			t.Fatal(err)
		}
		p3WaitArrived(t, "before-verdict")
		rows := buildtestQuery(t, `SELECT id::text, state FROM workos_runtime.artifacts WHERE task_id=$1`, taskID)
		if len(rows) != 1 || fmt.Sprint(rows[0]["state"]) != "preparing" {
			t.Fatalf("preparing row missing in the crash window: %+v", rows)
		}
		// Pre-verdict the preparing row must answer "not ready"; a success
		// reply here would mean promotion without a durable verdict.
		if _, err := clients.builds.GetBuildArtifact(context.Background(), connect.NewRequest(&executionv1.GetBuildArtifactRequest{ArtifactId: fmt.Sprint(rows[0]["id"])})); err == nil || connect.CodeOf(err) != connect.CodeFailedPrecondition {
			t.Fatalf("pre-verdict artifact answered ready: %v", err)
		}
		p3ContainerAction(t, p3ServiceContainerID(t, "runtime"), "kill")
		p3ResetFaults(t)
		p3ContainerAction(t, p3ServiceContainerID(t, "runtime"), "start")
		p3WaitHTTPReady(t, clients.runtimeURL+"/workos.taskexecution.v1.BuildTestService/GetBuildTest")
		// The dead worker's lease expires; only a fresh claim may record the
		// verdict, and the replay converges instead of duplicating rows.
		if _, err := p3SubmitVariant(t, clients, f, taskID, incident, "unique"); err != nil {
			t.Fatal(err)
		}
		verdict := p3WaitJobTerminal(t, clients, taskID, func(state string) bool {
			return state == "succeeded" || state == "failed" || state == "cancelled"
		})
		if verdict.State != "succeeded" {
			t.Fatalf("lease takeover did not converge to success: %+v", verdict)
		}
		if jobs := buildtestQuery(t, `SELECT id FROM workos_runtime.build_jobs WHERE task_id=$1`, taskID); len(jobs) != 1 {
			t.Fatalf("takeover duplicated the job row: %+v", jobs)
		}
		after := buildtestQuery(t, `SELECT id::text FROM workos_runtime.artifacts WHERE task_id=$1`, taskID)
		if len(after) != 1 {
			t.Fatalf("takeover duplicated the artifact row: %+v", after)
		}
		ready, err := clients.builds.GetBuildArtifact(context.Background(), connect.NewRequest(&executionv1.GetBuildArtifactRequest{ArtifactId: fmt.Sprint(after[0]["id"])}))
		if err != nil {
			t.Fatal(err)
		}
		if ready.Msg.GetArtifact().GetState() != "ready" {
			t.Fatalf("completed takeover is not ready: %+v", ready.Msg.GetArtifact())
		}
	})

	t.Run("F13_verdict_promotion_gates_on_bytes", func(t *testing.T) {
		p3ResetFaults(t)
		f := p3SeedNamed(t, clients, "P3 closeout F13 promotion", false)
		taskID := ids.UUIDv7{}.New()
		if _, err := p3SubmitVariant(t, clients, f, taskID, ids.UUIDv7{}.New(), "success"); err != nil {
			t.Fatal(err)
		}
		verdict := p3WaitJobTerminal(t, clients, taskID, func(state string) bool { return state == "succeeded" })
		rows := buildtestQuery(t, `SELECT id::text, digest, job_id::text AS job_id FROM workos_runtime.artifacts WHERE task_id=$1`, taskID)
		if len(rows) != 1 {
			t.Fatalf("expected one artifact: %+v", rows)
		}
		if rows[0]["job_id"] != verdict.JobID {
			t.Fatalf("artifact is not bound to the succeeded job: %+v vs %s", rows[0], verdict.JobID)
		}
		artifactID := fmt.Sprint(rows[0]["id"])
		digest := fmt.Sprint(rows[0]["digest"])
		first, err := clients.builds.GetBuildArtifact(context.Background(), connect.NewRequest(&executionv1.GetBuildArtifactRequest{ArtifactId: artifactID}))
		if err != nil {
			t.Fatal(err)
		}
		if first.Msg.GetArtifact().GetState() != "ready" || first.Msg.GetArtifact().GetArtifactDigest() != digest {
			t.Fatalf("authoritative query did not promote the bound artifact: %+v", first.Msg.GetArtifact())
		}
		again, err := clients.builds.GetBuildArtifact(context.Background(), connect.NewRequest(&executionv1.GetBuildArtifactRequest{ArtifactId: artifactID}))
		if err != nil {
			t.Fatal(err)
		}
		if again.Msg.GetArtifact().GetArtifactId() != artifactID || again.Msg.GetArtifact().GetState() != "ready" {
			t.Fatalf("artifact identity drifted on replay: %+v", again.Msg.GetArtifact())
		}
		if _, err := clients.builds.GetBuildArtifact(context.Background(), connect.NewRequest(&executionv1.GetBuildArtifactRequest{ArtifactId: ids.UUIDv7{}.New()})); err == nil {
			t.Fatal("unknown artifact id must not resolve")
		}
		// Corrupted bytes must never be reported ready. The content path is
		// shared by digest, so restore before anything else reuses it.
		bundlePath := p3ArtifactBundlePath(t, p3Owner, digest)
		original, err := os.ReadFile(bundlePath)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.WriteFile(bundlePath, original, 0o600) })
		corrupted := append(append([]byte{}, original...), 0)
		if err := os.WriteFile(bundlePath, corrupted, 0o600); err != nil {
			t.Fatal(err)
		}
		degraded, err := clients.builds.GetBuildArtifact(context.Background(), connect.NewRequest(&executionv1.GetBuildArtifactRequest{ArtifactId: artifactID}))
		if err == nil && degraded.Msg.GetArtifact().GetState() == "ready" {
			t.Fatalf("corrupted bundle bytes were reported ready: %+v", degraded.Msg.GetArtifact())
		}
		if err := os.WriteFile(bundlePath, original, 0o600); err != nil {
			t.Fatal(err)
		}
		// A cancelled job never leaves a ready artifact behind.
		slowTask := ids.UUIDv7{}.New()
		if _, err := p3SubmitVariant(t, clients, f, slowTask, ids.UUIDv7{}.New(), "slow"); err != nil {
			t.Fatal(err)
		}
		if _, err := clients.builds.CancelBuildTest(context.Background(), connect.NewRequest(&executionv1.CancelBuildTestRequest{TaskId: slowTask})); err != nil {
			t.Fatal(err)
		}
		cancelled := p3WaitJobTerminal(t, clients, slowTask, func(state string) bool {
			return state == "cancelled" || state == "failed" || state == "succeeded"
		})
		if cancelled.State == "succeeded" {
			t.Fatalf("cancel lost to a late success: %+v", cancelled)
		}
		if leftovers := buildtestQuery(t, `SELECT id FROM workos_runtime.artifacts WHERE task_id=$1 AND state='ready'`, slowTask); len(leftovers) != 0 {
			t.Fatalf("cancelled job froze a ready artifact: %+v", leftovers)
		}
		// Recovery goes through a new legitimate build of the same source:
		// the bytes dedupe while the metadata stays scoped to its own task.
		retryTask := ids.UUIDv7{}.New()
		if _, err := p3SubmitVariant(t, clients, f, retryTask, ids.UUIDv7{}.New(), "success"); err != nil {
			t.Fatal(err)
		}
		p3WaitJobTerminal(t, clients, retryTask, func(state string) bool { return state == "succeeded" })
		retryRows := buildtestQuery(t, `SELECT id::text FROM workos_runtime.artifacts WHERE task_id=$1`, retryTask)
		if len(retryRows) != 1 {
			t.Fatalf("recovery build missing its artifact: %+v", retryRows)
		}
		recovered, err := clients.builds.GetBuildArtifact(context.Background(), connect.NewRequest(&executionv1.GetBuildArtifactRequest{ArtifactId: fmt.Sprint(retryRows[0]["id"])}))
		if err != nil {
			t.Fatal(err)
		}
		if recovered.Msg.GetArtifact().GetState() != "ready" || recovered.Msg.GetArtifact().GetArtifactDigest() != digest {
			t.Fatalf("same-bytes recovery did not converge ready: %+v", recovered.Msg.GetArtifact())
		}
	})

	t.Run("F14_staged_register_and_pin_cas_drop_reply", func(t *testing.T) {
		p3ResetFaults(t)
		f := p3SeedNamed(t, clients, "P3 closeout F14 drop", false)
		p3Surface(t, clients, f, "P3-VALUE-0")
		// Both drops fire after the real Core handler committed: the caller
		// loses the reply, not the write.
		p3ArmDrop(t, "RegisterRepairCandidateVersion")
		p3ArmDrop(t, "TransitionCandidateVersion")
		p3StartRepair(t, clients, f)
		status := p3Release(t, clients, f, "published")
		f.CandidateVersion = status.GetCandidateVersion()
		if status.GetCandidateArtifactDigest() == "" || status.GetCandidateArtifactDigest() == f.Digest {
			t.Fatalf("candidate did not bind distinct B bytes: %+v", status)
		}
		staged := buildtestQuery(t, `SELECT av.version, c.published_at IS NOT NULL AS published FROM workos_core.app_repair_candidate_versions c JOIN workos_core.app_versions av ON av.owner_user_id = c.owner_user_id AND av.id = c.app_version_id WHERE c.installation_id=$1`, f.Installation)
		if len(staged) != 1 || fmt.Sprint(staged[0]["version"]) != status.GetCandidateVersion() || staged[0]["published"] != true {
			t.Fatalf("staged registration did not converge exactly once: %+v", staged)
		}
		// A post-publication registration replay must preserve the original
		// task's CAS snapshot, never adopt the now-pinned candidate as base.
		facts := buildtestQuery(t, `SELECT c.task_id::text, c.build_job_id::text, c.source_digest,
 t.input->'repairTarget'->>'projectRevision' AS revision
 FROM workos_core.app_repair_candidate_versions c JOIN workos_core.agent_tasks t ON t.id=c.task_id WHERE c.installation_id=$1`, f.Installation)
		request := connect.NewRequest(&executionv1.RegisterRepairCandidateVersionRequest{
			TaskId: fmt.Sprint(facts[0]["task_id"]), ProjectId: f.Project, InstallationId: f.Installation,
			BuildJobId: fmt.Sprint(facts[0]["build_job_id"]), SourceDigest: fmt.Sprint(facts[0]["source_digest"]),
		})
		request.Header().Set(identity.UserHeader, p3Owner)
		request.Header().Set(identity.DeviceHeader, "01999999-9999-7999-8999-000000000b02")
		replay, err := clients.versions.RegisterRepairCandidateVersion(context.Background(), request)
		if err != nil {
			t.Fatal(err)
		}
		if replay.Msg.GetBaseVersion() != "1.0.0" || fmt.Sprint(replay.Msg.GetProjectRevision()) != fmt.Sprint(facts[0]["revision"]) || replay.Msg.GetCreated() {
			t.Fatalf("registration refreshed immutable preconditions: %+v", replay.Msg)
		}

		sources := p3VersionHistorySources(t, f.Installation)
		if sources["install"] != 1 || sources["transition"] != 1 || sources["rollback"] != 0 {
			t.Fatalf("version history drifted after drop-reply replays: %+v", sources)
		}
		p3Surface(t, clients, f, "P3-VALUE-42")
	})

	t.Run("F15_container_started_before_workload_receipt", func(t *testing.T) {
		p3ResetFaults(t)
		t.Cleanup(func() { p3EnsureServiceRunning(t, "runtime") })
		f := p3SeedNamed(t, clients, "P3 closeout F15 adopt", false)
		p3ArmWait(t, "workload-started")
		// The launch request stays connected for the whole window: a client
		// disconnect would cancel the drive and destroy the exact crash
		// boundary under test.
		// The gateway injects the owner identity; the long timeout only has
		// to survive the injected crash window.
		longHTTP := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 4 * time.Minute}
		t.Cleanup(longHTTP.CloseIdleConnections)
		launchSurfaces := surfacev1connect.NewSurfaceServiceClient(longHTTP, clients.gatewayURL)
		launchDone := make(chan error, 1)
		go func() {
			_, err := launchSurfaces.CreateSurface(context.Background(), connect.NewRequest(&surfacev1.CreateSurfaceRequest{
				IdempotencyKey: ids.UUIDv7{}.New(), ProjectId: f.Project, AppInstanceId: f.Installation,
				DeviceClass: surfacev1.DeviceClass_DEVICE_CLASS_DESKTOP,
				Viewport:    &surfacev1.Viewport{Width: 1280, Height: 800, PixelRatio: 1},
			}))
			launchDone <- err
		}()
		p3WaitArrived(t, "workload-started")
		if containers := p3OwnedContainerCount(t, f.Installation); containers != 1 {
			t.Fatalf("expected exactly the started container, got %d", containers)
		}
		rows := buildtestQuery(t, `SELECT state FROM workos_runtime.workloads WHERE app_instance_id=$1`, f.Installation)
		if len(rows) != 1 || fmt.Sprint(rows[0]["state"]) == "running" {
			t.Fatalf("workload receipt must still be missing in the window: %+v", rows)
		}
		p3ContainerAction(t, p3ServiceContainerID(t, "runtime"), "kill")
		p3ResetFaults(t)
		p3ContainerAction(t, p3ServiceContainerID(t, "runtime"), "start")
		p3WaitHTTPReady(t, clients.runtimeURL+"/workos.surface.v1.SurfaceService/CreateSurface")
		// Reconciliation adopts the surviving container by exact inspect
		// instead of leaking a second instance.
		p3Surface(t, clients, f, "P3-VALUE-0")
		if containers := p3OwnedContainerCount(t, f.Installation); containers != 1 {
			t.Fatalf("reconciliation duplicated the adopted container: %d", containers)
		}
		final := buildtestQuery(t, `SELECT state, generation FROM workos_runtime.workloads WHERE app_instance_id=$1`, f.Installation)
		if len(final) != 1 || fmt.Sprint(final[0]["state"]) != "running" || final[0]["generation"] != int64(1) {
			t.Fatalf("workload did not converge to running generation 1: %+v", final)
		}
	})

	t.Run("F16_canary_identity_survives_reliability_restart", func(t *testing.T) {
		p3ResetFaults(t)
		t.Cleanup(func() { p3EnsureServiceRunning(t, "reliability") })
		f := p3SeedNamed(t, clients, "P3 closeout F16 identity", false)
		p3Surface(t, clients, f, "P3-VALUE-0")
		p3StartRepair(t, clients, f)
		var workloadID string
		var generation int64
		p3Poll(t, "deployment enters canary", func() bool {
			rows := buildtestQuery(t, `SELECT w.id::text AS id, w.generation FROM workos_reliability.deployment_ledger d JOIN workos_runtime.workloads w ON w.id=d.workload_id WHERE d.installation_id=$1 AND d.state='canary'`, f.Installation)
			if len(rows) != 1 {
				return false
			}
			workloadID = fmt.Sprint(rows[0]["id"])
			generation, _ = rows[0]["generation"].(int64)
			return workloadID != "" && generation > 0
		})
		// A controller restart never touches the running candidate: the same
		// workload identity may legally finish the observation window.
		p3ContainerAction(t, p3ServiceContainerID(t, "reliability"), "restart")
		p3Release(t, clients, f, "published")
		promoted := buildtestQuery(t, `SELECT w.id::text AS id, w.generation FROM workos_reliability.deployment_ledger d JOIN workos_runtime.workloads w ON w.id=d.workload_id WHERE d.installation_id=$1 AND d.state='promoted'`, f.Installation)
		if len(promoted) != 1 || fmt.Sprint(promoted[0]["id"]) != workloadID || promoted[0]["generation"] != generation {
			t.Fatalf("promotion did not keep the verified workload identity: captured=%s/%d got=%+v", workloadID, generation, promoted)
		}
		p3Surface(t, clients, f, "P3-VALUE-42")
	})

	t.Run("F16_canary_failure_rolls_back", func(t *testing.T) {
		p3ResetFaults(t)
		f := p3SeedNamed(t, clients, "P3 closeout F16 canary kill", false)
		p3Surface(t, clients, f, "P3-VALUE-0")
		p3StartRepair(t, clients, f)
		var candidateDigest string
		p3Poll(t, "deployment enters canary with artifact identity", func() bool {
			rows := buildtestQuery(t, `SELECT artifact_digest FROM workos_reliability.deployment_ledger WHERE installation_id=$1 AND state='canary' AND artifact_digest IS NOT NULL`, f.Installation)
			if len(rows) != 1 {
				return false
			}
			candidateDigest = fmt.Sprint(rows[0]["artifact_digest"])
			return candidateDigest != ""
		})
		// A real supervised crash of the candidate must stop promotion; the
		// wall clock alone never publishes.
		p3KillOwnedApp(t, f.Installation, candidateDigest)
		p3Release(t, clients, f, "rolled_back")
		p3Surface(t, clients, f, "P3-VALUE-0")
		if promoted := buildtestQuery(t, `SELECT incident_id FROM workos_reliability.deployment_ledger WHERE installation_id=$1 AND state='promoted'`, f.Installation); len(promoted) != 0 {
			t.Fatalf("failed canary was promoted: %+v", promoted)
		}
	})

	t.Run("F17_publish_drop_reply_converges_once", func(t *testing.T) {
		p3ResetFaults(t)
		f := p3SeedNamed(t, clients, "P3 closeout F17 publish", false)
		p3Surface(t, clients, f, "P3-VALUE-0")
		p3StartRepair(t, clients, f)
		p3Poll(t, "deployment enters canary", func() bool {
			return len(buildtestQuery(t, `SELECT state FROM workos_reliability.deployment_ledger WHERE installation_id=$1 AND state='canary'`, f.Installation)) == 1
		})
		p3ArmDrop(t, "PublishRepairCandidateVersion")
		status := p3Release(t, clients, f, "published")
		f.CandidateVersion = status.GetCandidateVersion()
		staged := buildtestQuery(t, `SELECT count(*) AS n FROM workos_core.app_repair_candidate_versions WHERE installation_id=$1 AND published_at IS NOT NULL`, f.Installation)
		if len(staged) != 1 || staged[0]["n"] != int64(1) {
			t.Fatalf("publish did not converge to exactly one published flip: %+v", staged)
		}
		sources := p3VersionHistorySources(t, f.Installation)
		if sources["transition"] != 1 || sources["rollback"] != 0 {
			t.Fatalf("publish replay moved the pin again: %+v", sources)
		}
		p3Surface(t, clients, f, "P3-VALUE-42")
	})

	t.Run("F17_rollback_drop_reply_rolls_once", func(t *testing.T) {
		p3ResetFaults(t)
		f := p3SeedNamed(t, clients, "P3 closeout F17 rollback", true)
		p3Surface(t, clients, f, "P3-VALUE-0")
		p3ArmDrop(t, "RollbackAppVersion")
		p3StartRepair(t, clients, f)
		// Core committed the rollback pin while the reply was lost: the
		// replay must land on the same previous version, never one older.
		p3Release(t, clients, f, "rolled_back")
		p3Surface(t, clients, f, "P3-VALUE-0")
		sources := p3VersionHistorySources(t, f.Installation)
		if sources["rollback"] != 1 {
			t.Fatalf("dropped rollback reply rolled more than once: %+v", sources)
		}
		pin := buildtestQuery(t, `SELECT version FROM workos_core.project_app_installations WHERE id=$1`, f.Installation)
		if len(pin) != 1 || fmt.Sprint(pin[0]["version"]) != "1.0.0" {
			t.Fatalf("rollback did not land on the previous pinned version: %+v", pin)
		}
	})
}
