//go:build integration && repairbuildtest && p3delivery

package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"connectrpc.com/connect"
	workloadv1 "github.com/yangtao121/workos/gen/go/workos/workload/v1"
	"github.com/yangtao121/workos/gen/go/workos/workload/v1/workloadv1connect"
	"github.com/yangtao121/workos/internal/platform/ids"
)

func TestP3FinalRuntime(t *testing.T) {
	clients := newBuildtestClients(t)
	sentinels := p3UnrelatedContainers(t)
	for _, variant := range []string{"workload-id", "generation", "memory", "cpu", "pids", "restart-policy", "mounted-bytes", "entrypoint-args", "working-directory", "user", "writable-mount", "image"} {
		t.Run("F16_F21_runtime_restart_with_"+variant+"_drift", func(t *testing.T) {
			p3ResetFaults(t)
			t.Cleanup(func() {
				p3ResetFaults(t)
				p3EnsureServiceRunning(t, "runtime")
				p3EnsureServiceRunning(t, "reliability")
			})
			restartLimit := 0
			if variant == "generation" {
				restartLimit = 1
			}
			f := p3SeedProfileWithRestart(t, clients, "P3 runtime drift "+variant, false, nil, []string{"go", "test", "./..."}, restartLimit)
			p3Surface(t, clients, f, "P3-VALUE-0")
			p3ArmWait(t, "deployment-canary")
			p3StartRepair(t, clients, f)
			p3WaitArrived(t, "deployment-canary")
			rows := buildtestQuery(t, `SELECT w.id::text,w.container_id,w.generation FROM workos_runtime.workloads w JOIN workos_reliability.deployment_ledger d ON d.workload_id=w.id WHERE d.installation_id=$1 AND d.state='canary'`, f.Installation)
			if len(rows) != 1 {
				t.Fatalf("canary identity missing: %+v", rows)
			}
			workloadID, containerID := fmt.Sprint(rows[0]["id"]), fmt.Sprint(rows[0]["container_id"])
			p3ContainerAction(t, p3ServiceContainerID(t, "reliability"), "kill")
			if variant == "workload-id" {
				client := workloadv1connect.NewSupervisedWorkloadServiceClient(clients.http, clients.runtimeURL)
				if _, err := client.TerminateWorkload(context.Background(), connect.NewRequest(&workloadv1.TerminateWorkloadRequest{WorkloadId: workloadID, ActionKey: ids.UUIDv7{}.New(), Reason: "policy"})); err != nil {
					t.Fatal(err)
				}
				p3Surface(t, clients, f, "P3-VALUE-42")
				replacement := buildtestQuery(t, `SELECT id::text FROM workos_runtime.workloads WHERE app_instance_id=$1 AND state='running'`, f.Installation)
				if len(replacement) != 1 || replacement[0]["id"] == workloadID {
					t.Fatalf("canary did not acquire a different workload: %+v", replacement)
				}
			}
			if variant == "generation" {
				client := workloadv1connect.NewSupervisedWorkloadServiceClient(clients.http, clients.runtimeURL)
				response, err := client.RestartWorkload(context.Background(), connect.NewRequest(&workloadv1.RestartWorkloadRequest{WorkloadId: workloadID, ActionKey: ids.UUIDv7{}.New()}))
				if err != nil || response.Msg.GetGeneration() <= rows[0]["generation"].(int64) {
					t.Fatalf("real generation restart: response=%v err=%v", response, err)
				}
			}
			p3ContainerAction(t, p3ServiceContainerID(t, "runtime"), "kill")
			switch variant {
			case "mounted-bytes":
				var document struct {
					Mounts []struct{ Destination, Source string }
				}
				if err := json.Unmarshal(p3DockerDo(t, "GET", "/containers/"+containerID+"/json", nil), &document); err != nil {
					t.Fatal(err)
				}
				mount := ""
				for _, m := range document.Mounts {
					if m.Destination == "/app" {
						mount = m.Source
					}
				}
				if mount == "" {
					t.Fatal("candidate has no real /app mount")
				}
				// The actual extracted mount differs from its immutable bundle.
				// Adding one file avoids modifying the executable's busy inode.
				if err := os.WriteFile(mount+"/unexpected", []byte("fixture drift"), 0600); err != nil {
					t.Fatal(err)
				}
			case "memory", "cpu", "pids", "restart-policy":
				change := map[string]any{}
				switch variant {
				case "memory":
					change["Memory"] = 96 << 20
				case "cpu":
					change["NanoCpus"] = 500000000
				case "pids":
					change["PidsLimit"] = 32
				case "restart-policy":
					change["RestartPolicy"] = map[string]string{"Name": "on-failure"}
				}
				body, _ := json.Marshal(change)
				p3DockerDo(t, "POST", "/containers/"+containerID+"/update", bytes.NewReader(body))
			case "entrypoint-args", "working-directory", "user", "writable-mount", "image":
				p3ReplaceProfile(t, containerID, workloadID, variant)
			}
			p3ResetFaults(t)
			p3ContainerAction(t, p3ServiceContainerID(t, "runtime"), "start")
			p3WaitHTTPReady(t, clients.runtimeURL+"/workos.surface.v1.SurfaceService/CreateSurface")
			p3ContainerAction(t, p3ServiceContainerID(t, "reliability"), "start")
			p3Release(t, clients, f, "rolled_back")
			p3Surface(t, clients, f, "P3-VALUE-0")
			if rows := buildtestQuery(t, `SELECT incident_id FROM workos_reliability.deployment_ledger WHERE installation_id=$1 AND state='promoted'`, f.Installation); len(rows) != 0 {
				t.Fatal("drifted canary was promoted")
			}
			if count := p3OwnedContainerCount(t, f.Installation); count != 1 {
				t.Fatalf("drift rollback leaked %d containers", count)
			}
		})
	}
	t.Run("F25_reconciliation_preserves_unrelated_containers", func(t *testing.T) {
		for _, id := range sentinels {
			var document struct {
				ID    string `json:"Id"`
				State struct{ Running bool }
			}
			if err := json.Unmarshal(p3DockerDo(t, "GET", "/containers/"+id+"/json", nil), &document); err != nil || document.ID != id || !document.State.Running {
				t.Fatalf("unrelated container changed during reconciliation: id=%s err=%v", id, err)
			}
		}
	})
}

func p3UnrelatedContainers(t *testing.T) []string {
	t.Helper()
	var createdIDs []string
	for _, labels := range []map[string]string{
		{"workos.runtime": os.Getenv("WORKOS_P3_GATE_NAMESPACE"), "workos.purpose": "unrelated-fixture"},
		{"workos.runtime": "other-" + ids.UUIDv7{}.New(), "workos.purpose": "runtime-app"},
	} {
		labels["workos.acceptance"] = os.Getenv("WORKOS_P3_GATE_NAMESPACE")
		body, _ := json.Marshal(map[string]any{"Image": p3Image, "User": "65532:65532", "Entrypoint": []string{"sleep", "7200"}, "Cmd": []string{}, "Labels": labels, "HostConfig": map[string]any{"NetworkMode": "none", "ReadonlyRootfs": true, "CapDrop": []string{"ALL"}, "SecurityOpt": []string{"no-new-privileges:true"}, "Memory": 16 << 20, "PidsLimit": 8}})
		var created struct {
			ID string `json:"Id"`
		}
		if err := json.Unmarshal(p3DockerDo(t, "POST", "/containers/create?name=p3-sentinel-"+ids.UUIDv7{}.New(), bytes.NewReader(body)), &created); err != nil || created.ID == "" {
			t.Fatalf("create sentinel: %v", err)
		}
		id := created.ID
		t.Cleanup(func() { p3DockerDo(t, "DELETE", "/containers/"+id+"?force=1", nil) })
		p3DockerDo(t, "POST", "/containers/"+id+"/start", nil)
		createdIDs = append(createdIDs, id)
	}
	return createdIDs
}

// Docker does not allow editing immutable create fields. Replace only this
// gate's verified container and inject its receipt into the fixture ledger so
// each profile check is exercised independently of the container-ID fence.
func p3ReplaceProfile(t *testing.T, containerID, workloadID, variant string) {
	t.Helper()
	var document struct {
		Name       string
		Config     map[string]any
		HostConfig map[string]any
	}
	if err := json.Unmarshal(p3DockerDo(t, "GET", "/containers/"+containerID+"/json", nil), &document); err != nil {
		t.Fatal(err)
	}
	labels, ok := document.Config["Labels"].(map[string]any)
	if !ok || labels["workos.runtime"] != os.Getenv("WORKOS_P3_GATE_NAMESPACE") || labels["workos.purpose"] != "runtime-app" {
		t.Fatal("refuse profile replacement outside gate-owned app")
	}
	switch variant {
	case "entrypoint-args":
		document.Config["Cmd"] = []string{"unexpected"}
	case "working-directory":
		document.Config["WorkingDir"] = "/tmp"
	case "user":
		document.Config["User"] = "0:0"
	case "image":
		document.Config["Image"] = "golang:1.26.7-bookworm"
	case "writable-mount":
		binds := document.HostConfig["Binds"].([]any)
		for i, bind := range binds {
			binds[i] = strings.TrimSuffix(bind.(string), ":ro") + ":rw"
		}
	}
	document.Config["HostConfig"] = document.HostConfig
	body, err := json.Marshal(document.Config)
	if err != nil {
		t.Fatal(err)
	}
	p3DockerDo(t, "DELETE", "/containers/"+containerID+"?force=1", nil)
	var created struct {
		ID string `json:"Id"`
	}
	if err := json.Unmarshal(p3DockerDo(t, "POST", "/containers/create?name="+strings.TrimPrefix(document.Name, "/"), bytes.NewReader(body)), &created); err != nil || created.ID == "" {
		t.Fatalf("replacement creation: %v", err)
	}
	buildtestExec(t, `UPDATE workos_runtime.workloads SET container_id=$1 WHERE id=$2 AND container_id=$3`, created.ID, workloadID, containerID)
	p3DockerDo(t, "POST", "/containers/"+created.ID+"/start", nil)
}
