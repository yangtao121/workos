//go:build integration && repairbuildtest && p3delivery

package integration_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"connectrpc.com/connect"
	executionv1 "github.com/yangtao121/workos/gen/go/workos/taskexecution/v1"
	"github.com/yangtao121/workos/internal/platform/ids"
	"google.golang.org/protobuf/proto"
)

// Each case exercises the existing real six-process fixture. SQL is used for
// fault/evidence inspection only; product commands still cross their RPC seam.
func TestP3FinalMatrix(t *testing.T) {
	clients := newBuildtestClients(t)
	t.Run("F04_repair_test_failure_has_no_delivery_effect", func(t *testing.T) {
		p3ResetFaults(t)
		f := p3SeedProfile(t, clients, "P3 final test failure", false, nil, []string{"sh", "-c", "go test ./... && exit 37"})
		p3Surface(t, clients, f, "P3-VALUE-0")
		incident := p3StartRepair(t, clients, f)
		p3WaitRepairConsumed(t, incident)
		jobs := buildtestQuery(t, `SELECT state, failure_reason FROM workos_runtime.build_jobs WHERE incident_id=$1`, incident)
		if len(jobs) != 1 || jobs[0]["state"] != "failed" || jobs[0]["failure_reason"] != "test-failed" {
			t.Fatalf("expected a real failed test verdict: %+v", jobs)
		}
		p3NoPublishSideEffects(t, f.Installation, "1.0.0")
		if rows := buildtestQuery(t, `SELECT id FROM workos_core.app_versions WHERE owner_user_id=$1 AND app_id=$2 AND version<>'1.0.0'`, p3Owner, f.App); len(rows) != 0 {
			t.Fatalf("failed test registered versions: %+v", rows)
		}
		if rows := buildtestQuery(t, `SELECT incident_id FROM workos_reliability.deployment_ledger WHERE installation_id=$1`, f.Installation); len(rows) != 0 {
			t.Fatalf("failed test created a deployment row: %+v", rows)
		}
		p3Surface(t, clients, f, "P3-VALUE-0")
	})

	t.Run("F09_result_queries_replay_after_response_loss", func(t *testing.T) {
		p3ResetFaults(t)
		f := p3SeedNamed(t, clients, "P3 final query replay", false)
		task := p3FailingJob(t, clients, f, "fast")
		p3WaitJobTerminal(t, clients, task, func(state string) bool { return state == "succeeded" })
		get := func() *executionv1.GetBuildTestResponse {
			r, err := clients.builds.GetBuildTest(context.Background(), connect.NewRequest(&executionv1.GetBuildTestRequest{TaskId: task}))
			if err != nil {
				t.Fatal(err)
			}
			return r.Msg
		}
		before := get()
		p3ArmDrop(t, "GetBuildTest")
		if _, err := clients.builds.GetBuildTest(context.Background(), connect.NewRequest(&executionv1.GetBuildTestRequest{TaskId: task})); err == nil {
			t.Fatal("query response was not dropped")
		}
		if after := get(); !proto.Equal(before, after) {
			t.Fatalf("terminal verdict changed across query loss: %v -> %v", before, after)
		}
		artifactID := before.GetArtifact().GetArtifactId()
		if artifactID == "" {
			t.Fatal("successful bundle job omitted its artifact")
		}
		artifact, err := clients.builds.GetBuildArtifact(context.Background(), connect.NewRequest(&executionv1.GetBuildArtifactRequest{ArtifactId: artifactID}))
		if err != nil {
			t.Fatal(err)
		}
		p3ArmDrop(t, "GetBuildArtifact")
		if _, err := clients.builds.GetBuildArtifact(context.Background(), connect.NewRequest(&executionv1.GetBuildArtifactRequest{ArtifactId: artifactID})); err == nil {
			t.Fatal("artifact response was not dropped")
		}
		replay, err := clients.builds.GetBuildArtifact(context.Background(), connect.NewRequest(&executionv1.GetBuildArtifactRequest{ArtifactId: artifactID}))
		if err != nil || !proto.Equal(artifact.Msg, replay.Msg) {
			t.Fatalf("artifact query replay changed facts: %v", err)
		}
		p3ContainerAction(t, p3ServiceContainerID(t, "runtime"), "restart")
		p3WaitHTTPReady(t, clients.runtimeURL+"/workos.taskexecution.v1.BuildTestService/GetBuildTest")
		if after := get(); !proto.Equal(before, after) {
			t.Fatal("query loss/restart changed the terminal verdict")
		}
	})

	t.Run("F10_owner_scoped_bytes_and_import_provenance", func(t *testing.T) {
		p3ResetFaults(t)
		bundle, digest := p3TestBundle(t)
		owners := []string{ids.UUIDv7{}.New(), ids.UUIDv7{}.New()}
		key := ids.UUIDv7{}.New()
		idsSeen := map[string]bool{}
		for _, owner := range owners {
			id, err := p3ImportArtifact(t, owner, "same-app", key, bundle, digest)
			if err != nil || idsSeen[id] {
				t.Fatalf("owner scopes share metadata: id=%s err=%v", id, err)
			}
			idsSeen[id] = true
			replay, err := p3ImportArtifact(t, owner, "same-app", key, bundle, digest)
			if err != nil || replay != id {
				t.Fatalf("owner replay drifted: %s %s %v", id, replay, err)
			}
			if _, err := os.Stat(p3ArtifactBundlePath(t, owner, digest)); err != nil {
				t.Fatal(err)
			}
		}
		// Corruption in one owner's namespace must not poison the other's
		// dedupe path even though both have exactly the same content digest.
		if err := os.WriteFile(p3ArtifactBundlePath(t, owners[0], digest), []byte("corrupt fixture"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := p3ImportArtifact(t, owners[0], "same-app", ids.UUIDv7{}.New(), bundle, digest); err == nil {
			t.Fatal("corrupt owner dedupe accepted")
		}
		if _, err := p3ImportArtifact(t, owners[1], "same-app", ids.UUIDv7{}.New(), bundle, digest); err != nil {
			t.Fatalf("another owner's corruption crossed namespaces: %v", err)
		}
	})

	for _, tc := range []struct {
		stage, result string
		failed        bool
	}{
		{"repair-before-register", "published", false},
		{"deployment-before-commit-starting", "published", false},
		{"deployment-before-commit-canary", "published", false},
		{"deployment-before-commit-promoted", "published", false},
		{"deployment-before-commit-rolled_back", "rolled_back", true},
	} {
		t.Run("F14_F17_crash_"+tc.stage, func(t *testing.T) {
			p3ResetFaults(t)
			t.Cleanup(func() { p3ResetFaults(t); p3EnsureServiceRunning(t, "reliability") })
			f := p3SeedNamed(t, clients, "P3 final crash "+tc.stage, tc.failed)
			p3Surface(t, clients, f, "P3-VALUE-0")
			p3ArmWait(t, tc.stage)
			p3StartRepair(t, clients, f)
			p3WaitArrived(t, tc.stage)
			if tc.stage == "repair-before-register" {
				if rows := buildtestQuery(t, `SELECT task_id FROM workos_core.app_repair_candidate_versions WHERE installation_id=$1`, f.Installation); len(rows) != 0 {
					t.Fatal("candidate registered before the injected boundary")
				}
			}
			p3ContainerAction(t, p3ServiceContainerID(t, "reliability"), "kill")
			p3ResetFaults(t)
			p3ContainerAction(t, p3ServiceContainerID(t, "reliability"), "start")
			p3Release(t, clients, f, tc.result)
			expected := "P3-VALUE-42"
			if tc.failed {
				expected = "P3-VALUE-0"
			}
			p3Surface(t, clients, f, expected)
			sources := p3VersionHistorySources(t, f.Installation)
			if sources["transition"] != 1 || sources["rollback"] > 1 {
				t.Fatalf("replay duplicated version history: %+v", sources)
			}
			if rows := buildtestQuery(t, `SELECT task_id FROM workos_core.app_repair_candidate_versions WHERE installation_id=$1`, f.Installation); len(rows) != 1 {
				t.Fatalf("replay duplicated candidate: %+v", rows)
			}
			if count := p3OwnedContainerCount(t, f.Installation); count != 1 {
				t.Fatalf("replay left %d app containers", count)
			}
		})
	}

	t.Run("F09_candidate_query_drop_does_not_duplicate_delivery", func(t *testing.T) {
		p3ResetFaults(t)
		f := p3SeedNamed(t, clients, "P3 final candidate query", false)
		p3ArmDrop(t, "GetRepairSourceCandidate")
		p3StartRepair(t, clients, f)
		p3Release(t, clients, f, "published")
		if _, err := os.Stat(filepath.Join(p3FaultDir(t), "drop-once-GetRepairSourceCandidate")); !os.IsNotExist(err) {
			t.Fatalf("candidate query fault not consumed: %v", err)
		}
		if rows := buildtestQuery(t, `SELECT id FROM workos_runtime.build_jobs WHERE installation_id=$1`, f.Installation); len(rows) != 1 {
			t.Fatalf("query loss duplicated build: %s", fmt.Sprint(rows))
		}
		p3Surface(t, clients, f, "P3-VALUE-42")
	})
}
