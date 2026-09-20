//go:build integration && repairbuildtest && p3delivery

package integration_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"connectrpc.com/connect"
	appv1 "github.com/yangtao121/workos/gen/go/workos/app/v1"
	projectv1 "github.com/yangtao121/workos/gen/go/workos/project/v1"
	executionv1 "github.com/yangtao121/workos/gen/go/workos/taskexecution/v1"
	workloadv1 "github.com/yangtao121/workos/gen/go/workos/workload/v1"
	"github.com/yangtao121/workos/gen/go/workos/workload/v1/workloadv1connect"
	"github.com/yangtao121/workos/internal/platform/bundleformat"
	"github.com/yangtao121/workos/internal/platform/identity"
	"github.com/yangtao121/workos/internal/platform/ids"
	artifactdomain "github.com/yangtao121/workos/internal/runtime/artifactstore/domain"
)

func TestP3FinalAuthority(t *testing.T) {
	clients := newBuildtestClients(t)
	for _, category := range []string{"ready", "preparing", "staging"} {
		t.Run("F23_quota_includes_"+category, func(t *testing.T) {
			p3ResetFaults(t)
			owner := ids.UUIDv7{}.New()
			ownerDir := filepath.Join(os.Getenv("WORKOS_P3_GATE_DIR"), "artifacts", owner)
			t.Cleanup(func() { _ = os.RemoveAll(ownerDir) })
			bundle, digest := p3TestBundle(t)
			var measured string
			switch category {
			case "ready":
				if _, err := p3ImportArtifact(t, owner, "quota-app", ids.UUIDv7{}.New(), bundle, digest); err != nil {
					t.Fatal(err)
				}
				measured = p3ArtifactBundlePath(t, owner, digest)
			case "preparing":
				f := p3SeedNamed(t, clients, "P3 quota preparing", false)
				task := ids.UUIDv7{}.New()
				req, err := p3BuildTestRequest(t, clients, f, task, ids.UUIDv7{}.New(), "fast")
				if err != nil {
					t.Fatal(err)
				}
				req.Msg.Job.OwnerUserId = owner
				p3ArmWait(t, "before-verdict")
				t.Cleanup(func() { p3ResetFaults(t) })
				if _, err := clients.builds.SubmitBuildTest(context.Background(), req); err != nil {
					t.Fatal(err)
				}
				p3WaitArrived(t, "before-verdict")
				rows := buildtestQuery(t, `SELECT digest,state FROM workos_runtime.artifacts WHERE task_id=$1`, task)
				if len(rows) != 1 || rows[0]["state"] != "preparing" {
					t.Fatalf("expected persisted preparing bytes: %+v", rows)
				}
				measured = p3ArtifactBundlePath(t, owner, fmt.Sprint(rows[0]["digest"]))
				t.Cleanup(func() {
					_, err := clients.builds.CancelBuildTest(context.Background(), connect.NewRequest(&executionv1.CancelBuildTestRequest{TaskId: task}))
					if err != nil {
						t.Error(err)
					}
					p3ReleaseFault(t, "before-verdict")
				})
			case "staging":
				if err := os.MkdirAll(filepath.Join(ownerDir, "tmp"), 0700); err != nil {
					t.Fatal(err)
				}
				measured = filepath.Join(ownerDir, "tmp", "inflight.part")
				if err := os.WriteFile(measured, make([]byte, 4096), 0600); err != nil {
					t.Fatal(err)
				}
			}
			info, err := os.Stat(measured)
			if err != nil {
				t.Fatal(err)
			}
			// At this boundary ignoring this category alone would admit the
			// import. Sparse padding avoids allocating two GiB of test data.
			padding := filepath.Join(ownerDir, "quota-padding")
			file, err := os.OpenFile(padding, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if err != nil {
				t.Fatal(err)
			}
			err = file.Truncate(artifactdomain.OwnerQuotaBytes - bundleformat.MaxEncodedBundleBytes - info.Size() + 1)
			closeErr := file.Close()
			if err != nil || closeErr != nil {
				t.Fatalf("padding: %v %v", err, closeErr)
			}
			key := ids.UUIDv7{}.New()
			if _, err := p3ImportArtifact(t, owner, "quota-next", key, bundle, digest); err == nil {
				t.Fatalf("quota omitted %s bytes", category)
			}
			if rows := buildtestQuery(t, `SELECT id FROM workos_runtime.artifacts WHERE owner_user_id=$1 AND idempotency_key=$2`, owner, key); len(rows) != 0 {
				t.Fatalf("quota refusal wrote metadata: %+v", rows)
			}
			if err := os.Remove(padding); err != nil {
				t.Fatal(err)
			}
			if _, err := p3ImportArtifact(t, owner, "quota-next", key, bundle, digest); err != nil {
				t.Fatalf("same key did not recover after quota release: %v", err)
			}
		})
	}

	t.Run("F24_registration_checks_complete_runtime_provenance", func(t *testing.T) {
		p3ResetFaults(t)
		f := p3SeedNamed(t, clients, "P3 complete provenance", false)
		p3ArmWait(t, "repair-before-register")
		t.Cleanup(func() { p3ResetFaults(t) })
		p3StartRepair(t, clients, f)
		p3WaitArrived(t, "repair-before-register")
		rows := buildtestQuery(t, `SELECT id::text,task_id::text,job_id::text,incident_id::text,project_id::text,installation_id::text,source_bundle_id::text,source_digest,origin FROM workos_runtime.artifacts WHERE installation_id=$1 AND origin='build_job' AND state='ready'`, f.Installation)
		if len(rows) != 1 {
			t.Fatalf("ready artifact missing: %+v", rows)
		}
		a := rows[0]
		register := func(project, installation, job string) error {
			req := connect.NewRequest(&executionv1.RegisterRepairCandidateVersionRequest{TaskId: fmt.Sprint(a["task_id"]), ProjectId: project, InstallationId: installation, BuildJobId: job, SourceDigest: fmt.Sprint(a["source_digest"])})
			req.Header().Set(identity.UserHeader, p3Owner)
			req.Header().Set(identity.DeviceHeader, "01999999-9999-7999-8999-000000000b02")
			_, err := clients.versions.RegisterRepairCandidateVersion(context.Background(), req)
			return err
		}
		other := p3SeedNamed(t, clients, "P3 other project", false)
		for _, tc := range []struct{ name, project, installation, job string }{
			{"project", other.Project, f.Installation, fmt.Sprint(a["job_id"])},
			{"installation", f.Project, other.Installation, fmt.Sprint(a["job_id"])},
			{"import-as-job", f.Project, f.Installation, f.ArtifactID},
		} {
			t.Run(tc.name, func(t *testing.T) {
				if err := register(tc.project, tc.installation, tc.job); err == nil {
					t.Fatal("mismatched registration accepted")
				}
			})
		}
		for _, field := range []string{"project_id", "installation_id", "incident_id", "source_bundle_id", "origin"} {
			t.Run("runtime-"+field, func(t *testing.T) {
				value := ids.UUIDv7{}.New()
				if field == "origin" {
					value = "operator_import"
				}
				// These fixed columns inject corrupt private Runtime facts;
				// Core must independently refuse them, including import origin.
				buildtestExec(t, `UPDATE workos_runtime.artifacts SET `+field+`=$1 WHERE id=$2`, value, a["id"])
				t.Cleanup(func() {
					buildtestExec(t, `UPDATE workos_runtime.artifacts SET `+field+`=$1 WHERE id=$2`, a[field], a["id"])
				})
				if err := register(f.Project, f.Installation, fmt.Sprint(a["job_id"])); err == nil {
					t.Fatalf("Core accepted altered %s", field)
				}
				if found := buildtestQuery(t, `SELECT task_id FROM workos_core.app_repair_candidate_versions WHERE installation_id=$1`, f.Installation); len(found) != 0 {
					t.Fatalf("refusal created a candidate: %+v", found)
				}
			})
		}
		p3ResetFaults(t)
		p3Release(t, clients, f, "published")
		p3Surface(t, clients, f, "P3-VALUE-42")
	})

	for _, action := range []string{"uninstall", "archive"} {
		t.Run("F19_retired_target_before_registration_"+action, func(t *testing.T) {
			p3ResetFaults(t)
			t.Cleanup(func() { p3ResetFaults(t) })
			f := p3SeedNamed(t, clients, "P3 retire before registration "+action, false)
			p3ArmWait(t, "repair-before-register")
			incident := p3StartRepair(t, clients, f)
			p3WaitArrived(t, "repair-before-register")
			revision := p3ProjectRevision(t, clients, f.Project)
			if action == "uninstall" {
				if _, err := clients.install.UninstallApp(context.Background(), connect.NewRequest(&appv1.UninstallAppRequest{IdempotencyKey: ids.UUIDv7{}.New(), ProjectId: f.Project, InstallationId: f.Installation, ExpectedProjectRevision: revision})); err != nil {
					t.Fatal(err)
				}
			} else if _, err := clients.projects.ArchiveProject(context.Background(), connect.NewRequest(&projectv1.ArchiveProjectRequest{ProjectId: f.Project, ExpectedRevision: revision})); err != nil {
				t.Fatal(err)
			}
			p3ResetFaults(t)
			p3WaitRepairConsumed(t, incident)
			if rows := buildtestQuery(t, `SELECT task_id FROM workos_core.app_repair_candidate_versions WHERE installation_id=$1`, f.Installation); len(rows) != 0 {
				t.Fatalf("retired target acquired a candidate: %+v", rows)
			}
			if rows := buildtestQuery(t, `SELECT incident_id FROM workos_reliability.deployment_ledger WHERE installation_id=$1`, f.Installation); len(rows) != 0 {
				t.Fatalf("retired target acquired a deployment: %+v", rows)
			}
		})
	}

	t.Run("F25_old_stop_replay_preserves_new_workload", func(t *testing.T) {
		p3ResetFaults(t)
		f := p3SeedNamed(t, clients, "P3 stale stop", false)
		p3Surface(t, clients, f, "P3-VALUE-0")
		rows := buildtestQuery(t, `SELECT id::text,generation FROM workos_runtime.workloads WHERE app_instance_id=$1 AND state='running'`, f.Installation)
		if len(rows) != 1 {
			t.Fatalf("workload missing: %+v", rows)
		}
		id := fmt.Sprint(rows[0]["id"])
		workloads := workloadv1connect.NewSupervisedWorkloadServiceClient(clients.http, clients.runtimeURL)
		stop := &workloadv1.TerminateWorkloadRequest{WorkloadId: id, ActionKey: ids.UUIDv7{}.New(), Reason: "policy"}
		if _, err := workloads.TerminateWorkload(context.Background(), connect.NewRequest(stop)); err != nil {
			t.Fatal(err)
		}
		p3Surface(t, clients, f, "P3-VALUE-0")
		before := buildtestQuery(t, `SELECT id::text,generation,container_id FROM workos_runtime.workloads WHERE app_instance_id=$1 AND state='running'`, f.Installation)
		if len(before) != 1 || (before[0]["id"] == rows[0]["id"] && before[0]["generation"] == rows[0]["generation"]) {
			t.Fatalf("opening a stopped application reused the old execution identity: %+v", before)
		}
		if _, err := workloads.TerminateWorkload(context.Background(), connect.NewRequest(stop)); err != nil {
			t.Fatal(err)
		}
		p3Surface(t, clients, f, "P3-VALUE-0")
		after := buildtestQuery(t, `SELECT id::text,generation,container_id FROM workos_runtime.workloads WHERE app_instance_id=$1 AND state='running'`, f.Installation)
		if fmt.Sprint(before) != fmt.Sprint(after) {
			t.Fatalf("old stop touched new generation: %+v -> %+v", before, after)
		}
	})
}
