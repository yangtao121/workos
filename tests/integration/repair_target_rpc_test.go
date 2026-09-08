//go:build integration && repairtarget

package integration_test

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
	"github.com/yangtao121/workos/gen/go/workos/agent/v1/agentv1connect"
	appv1 "github.com/yangtao121/workos/gen/go/workos/app/v1"
	projectv1 "github.com/yangtao121/workos/gen/go/workos/project/v1"
	"github.com/yangtao121/workos/internal/platform/identity"
	reliabilitytransport "github.com/yangtao121/workos/internal/reliability/transport"
	"google.golang.org/protobuf/proto"
)

// Tests the actual Reliability adapter against Core and the public Project,
// Registry and Task APIs. It proves admission, not candidate build or deploy.
func TestRepairTargetPrivateRPCPreservesFirstVersion(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	httpClient := &http.Client{Transport: &http.Transport{Proxy: nil}}
	registry, projects, installations := appRegistryClients(t), integrationProjectClients(t), installationClients(t)
	key := fmt.Sprintf("repair-target-%d", time.Now().UnixNano())
	appID := key
	digest := registerApp(t, ctx, registry, appID, "Repair target fixture", "1.0.0", "project")
	project := createIntegrationProject(t, ctx, projects, "Repair target fixture", key)
	installed := installApp(t, ctx, installations, key+"-install", project.GetId(), appID, "1.0.0", 1)
	owner, device := project.GetOwnerUserId(), "0198d7ea-2110-7c42-b659-c5e4d73bc338"
	incident := uuid.Must(uuid.NewV7()).String()
	private := agentv1connect.NewAgentRepairTaskServiceClient(httpClient, "http://127.0.0.1:8081")
	tasks := agentv1connect.NewAgentTaskServiceClient(httpClient, "http://127.0.0.1:8080")
	input := &agentv1.CreateRepairTaskRequest{ProjectId: project.GetId(), AppInstanceId: installed.GetId(), IncidentId: incident, ViolationSummary: "Repair startup failure", IdempotencyKey: key + "-repair"}
	submit := func(payload *agentv1.CreateRepairTaskRequest) (*connect.Response[agentv1.CreateRepairTaskResponse], error) {
		request := connect.NewRequest(payload)
		request.Header().Set(identity.UserHeader, owner)
		request.Header().Set(identity.DeviceHeader, device)
		return private.CreateRepairTask(ctx, request)
	}
	first, err := submit(input)
	if err != nil || first.Msg.GetReplay() {
		t.Fatalf("initial admission: %v %v", first, err)
	}
	get, err := tasks.GetTask(ctx, connect.NewRequest(&agentv1.GetTaskRequest{TaskId: first.Msg.GetTaskId()}))
	if err != nil {
		t.Fatal(err)
	}
	target := get.Msg.GetTask().GetInput().GetRepairTarget()
	want := &agentv1.RepairTarget{AppInstanceId: installed.GetId(), AppId: appID, Version: "1.0.0", ManifestDigest: digest, ProjectRevision: 2}
	if !proto.Equal(target, want) {
		t.Fatalf("target=%v want=%v", target, want)
	}
	registerApp(t, ctx, registry, appID, "Repair target fixture", "2.0.0", "project")
	transitioned, err := installations.TransitionAppVersion(ctx, connect.NewRequest(&appv1.TransitionAppVersionRequest{IdempotencyKey: key + "-upgrade", ProjectId: project.GetId(), InstallationId: installed.GetId(), ExpectedProjectRevision: 2, Version: "2.0.0"}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := projects.ArchiveProject(ctx, connect.NewRequest(&projectv1.ArchiveProjectRequest{ProjectId: project.GetId(), ExpectedRevision: transitioned.Msg.GetProjectRevision()})); err != nil {
		t.Fatal(err)
	}
	// The actual producer adapter supplies installation and trusted identity.
	producer := reliabilitytransport.NewRepairSubmitterClient("http://127.0.0.1:8081", device)
	taskID, providerID, err := producer.SubmitRepair(ctx, owner, project.GetId(), installed.GetId(), incident, input.GetIdempotencyKey(), input.GetViolationSummary())
	if err != nil || taskID != first.Msg.GetTaskId() || providerID != first.Msg.GetProviderId() {
		t.Fatalf("producer replay: task=%s provider=%s err=%v", taskID, providerID, err)
	}
	replay, err := submit(input)
	if err != nil || !replay.Msg.GetReplay() {
		t.Fatalf("replay flag: %v %v", replay, err)
	}
	get, err = tasks.GetTask(ctx, connect.NewRequest(&agentv1.GetTaskRequest{TaskId: taskID}))
	if err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(get.Msg.GetTask().GetInput().GetRepairTarget(), want) {
		t.Fatal("upgrade/archive changed target")
	}
	changed := proto.Clone(input).(*agentv1.CreateRepairTaskRequest)
	changed.AppInstanceId = uuid.Must(uuid.NewV7()).String()
	if _, err := submit(changed); connect.CodeOf(err) != connect.CodeAborted {
		t.Fatalf("changed installation: %v", err)
	}
	changed = proto.Clone(input).(*agentv1.CreateRepairTaskRequest)
	changed.IdempotencyKey += "-new"
	if _, err := submit(changed); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("fresh archived target: %v", err)
	}
	changed.AppInstanceId = ""
	if _, err := submit(changed); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("missing installation: %v", err)
	}
	// Private admission remains absent from Gateway routing.
	publicPrivate := agentv1connect.NewAgentRepairTaskServiceClient(httpClient, "http://127.0.0.1:8080")
	if _, err := publicPrivate.CreateRepairTask(ctx, connect.NewRequest(input)); err == nil {
		t.Fatal("private admission exposed by gateway")
	}
}
