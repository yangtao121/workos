//go:build integration && repairsource

package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"
	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
	"github.com/yangtao121/workos/gen/go/workos/agent/v1/agentv1connect"
	appv1 "github.com/yangtao121/workos/gen/go/workos/app/v1"
	"github.com/yangtao121/workos/gen/go/workos/app/v1/appv1connect"
	projectv1 "github.com/yangtao121/workos/gen/go/workos/project/v1"
	"github.com/yangtao121/workos/gen/go/workos/project/v1/projectv1connect"
	executionv1 "github.com/yangtao121/workos/gen/go/workos/taskexecution/v1"
	"github.com/yangtao121/workos/gen/go/workos/taskexecution/v1/taskexecutionv1connect"
	registrydomain "github.com/yangtao121/workos/internal/core/appregistry/domain"
	"github.com/yangtao121/workos/internal/platform/identity"
	"github.com/yangtao121/workos/internal/platform/ids"
	"github.com/yangtao121/workos/internal/platform/privatetls"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
)

const repairSourceWorker = "repair-source-gate"

type repairSourceRPCState struct {
	LeaseID, TaskID, ProjectID, InstallationID, SourceID, SourceDigest string
	Input, Candidate                                                   json.RawMessage
}

func repairSourceGateEnv(t *testing.T, name string) string {
	t.Helper()
	value := os.Getenv("WORKOS_REPAIR_SOURCE_" + name)
	if value == "" {
		t.Fatal("run through make test-repair-sources")
	}
	return value
}
func repairSourceTLSClient(t *testing.T) *http.Client {
	t.Helper()
	dir := filepath.Join(repairSourceGateEnv(t, "DIR"), "harness-execution")
	config, err := privatetls.ClientConfig(privatetls.Identity{CAFile: filepath.Join(dir, "ca.crt"), CertFile: filepath.Join(dir, "harness.crt"), KeyFile: filepath.Join(dir, "harness.key"), PeerIdentity: privatetls.IdentityCore})
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: &http.Transport{Proxy: nil, TLSClientConfig: config}, Timeout: 10 * time.Second}
	t.Cleanup(client.CloseIdleConnections)
	return client
}
func repairSourceStatePath(t *testing.T) string {
	return filepath.Join(repairSourceGateEnv(t, "DIR"), "repair-state.json")
}
func candidateFiles() []*appv1.AppSourceFile {
	return []*appv1.AppSourceFile{{Path: "main.go", Content: []byte("package main\nfunc main() {} // 候选\n")}, {Path: "run", Content: []byte("#!/bin/sh\nexec ./app\n"), Executable: true}}
}

func TestRepairSourceRPCSeed(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	publicURL := repairSourceGateEnv(t, "GATEWAY_URL")
	coreURL := repairSourceGateEnv(t, "CORE_URL")
	executionURL := repairSourceGateEnv(t, "EXECUTION_URL")
	publicHTTP := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 10 * time.Second}
	defer publicHTTP.CloseIdleConnections()
	projects := projectv1connect.NewProjectServiceClient(publicHTTP, publicURL)
	registry := appv1connect.NewAppRegistryServiceClient(publicHTTP, publicURL)
	sources := appv1connect.NewAppSourceBundleServiceClient(publicHTTP, publicURL)
	installations := appv1connect.NewAppInstallationServiceClient(publicHTTP, publicURL)
	project, err := projects.CreateProject(ctx, connect.NewRequest(&projectv1.CreateProjectRequest{IdempotencyKey: "repair-source-project", Name: "Repair source fixture"}))
	if err != nil {
		t.Fatal(err)
	}
	source, err := sources.CreateAppSourceBundle(ctx, connect.NewRequest(&appv1.CreateAppSourceBundleRequest{IdempotencyKey: "repair-base", Files: []*appv1.AppSourceFile{{Path: "main.go", Content: []byte("package main\nfunc main() {}\n")}}}))
	if err != nil {
		t.Fatal(err)
	}
	base := registrydomain.SourceBundle{ID: source.Msg.GetBundle().GetId(), Digest: source.Msg.GetBundle().GetDigest()}
	registered, err := registry.RegisterApp(ctx, connect.NewRequest(&appv1.RegisterAppRequest{IdempotencyKey: "repair-base-version", ManifestYaml: repairSourceManifest(t, base, "1.0.0")}))
	if err != nil {
		t.Fatal(err)
	}
	installed, err := installations.InstallApp(ctx, connect.NewRequest(&appv1.InstallAppRequest{IdempotencyKey: "repair-install", ProjectId: project.Msg.GetProject().GetId(), AppId: registered.Msg.GetApp().GetId(), Version: "1.0.0", ExpectedProjectRevision: 1}))
	if err != nil {
		t.Fatal(err)
	}
	admission := agentv1connect.NewAgentRepairTaskServiceClient(publicHTTP, coreURL)
	request := connect.NewRequest(&agentv1.CreateRepairTaskRequest{IdempotencyKey: "repair-source-task", ProjectId: project.Msg.GetProject().GetId(), AppInstanceId: installed.Msg.GetInstallation().GetId(), IncidentId: ids.UUIDv7{}.New(), ViolationSummary: "Repair bounded startup fixture"})
	request.Header().Set(identity.UserHeader, project.Msg.GetProject().GetOwnerUserId())
	request.Header().Set(identity.DeviceHeader, "01999999-9999-7999-8999-000000000b02")
	admitted, err := admission.CreateRepairTask(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	privateHTTP := repairSourceTLSClient(t)
	execution := taskexecutionv1connect.NewTaskExecutionServiceClient(privateHTTP, executionURL)
	repair := taskexecutionv1connect.NewRepairExecutionServiceClient(privateHTTP, executionURL)
	claimed, err := execution.ClaimTask(ctx, connect.NewRequest(&executionv1.ClaimTaskRequest{WorkerId: repairSourceWorker, LeaseDuration: durationpb.New(5 * time.Minute)}))
	if err != nil {
		t.Fatal(err)
	}
	lease := claimed.Msg.GetLease()
	if lease.GetTask().GetId() != admitted.Msg.GetTaskId() {
		t.Fatal("gate did not claim its repair task")
	}
	input, err := repair.ResolveRepairBuildInput(ctx, connect.NewRequest(&executionv1.ResolveRepairBuildInputRequest{LeaseId: lease.GetLeaseId(), WorkerId: repairSourceWorker}))
	if err != nil {
		t.Fatal(err)
	}
	if input.Msg.GetInput().GetTarget().GetVersion() != "1.0.0" || !proto.Equal(input.Msg.GetInput().GetSource(), source.Msg.GetBundle()) {
		t.Fatal("resolved wrong base source")
	}
	_, err = repair.ResolveRepairBuildInput(ctx, connect.NewRequest(&executionv1.ResolveRepairBuildInputRequest{LeaseId: lease.GetLeaseId(), WorkerId: "foreign-worker"}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("foreign worker: %v", err)
	}
	_, err = repair.SubmitRepairSourceCandidate(ctx, connect.NewRequest(&executionv1.SubmitRepairSourceCandidateRequest{LeaseId: lease.GetLeaseId(), WorkerId: repairSourceWorker, Files: []*appv1.AppSourceFile{{Path: "../escape"}}}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("unsafe path: %v", err)
	}
	candidate, err := repair.SubmitRepairSourceCandidate(ctx, connect.NewRequest(&executionv1.SubmitRepairSourceCandidateRequest{LeaseId: lease.GetLeaseId(), WorkerId: repairSourceWorker, Files: candidateFiles()}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = repair.SubmitRepairSourceCandidate(ctx, connect.NewRequest(&executionv1.SubmitRepairSourceCandidateRequest{LeaseId: lease.GetLeaseId(), WorkerId: repairSourceWorker, Files: []*appv1.AppSourceFile{{Path: "main.go", Content: []byte("different")}}}))
	if connect.CodeOf(err) != connect.CodeAborted {
		t.Fatalf("changed candidate: %v", err)
	}
	_, err = repair.SubmitRepairSourceCandidate(ctx, connect.NewRequest(&executionv1.SubmitRepairSourceCandidateRequest{LeaseId: lease.GetLeaseId(), WorkerId: repairSourceWorker, Files: []*appv1.AppSourceFile{{Path: "huge", Content: bytes.Repeat([]byte{'x'}, 2*1024*1024)}}}))
	if connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("oversize: %v", err)
	}
	// Both public and private loopback listeners exclude this mTLS-only service.
	for _, url := range []string{publicURL, coreURL} {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url+taskexecutionv1connect.RepairExecutionServiceResolveRepairBuildInputProcedure, bytes.NewBufferString("{}"))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		response, err := publicHTTP.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusNotFound {
			t.Fatalf("unexpected public execution status %d", response.StatusCode)
		}
	}
	// A certificate-free caller cannot reach the RPC, even with a valid lease ID.
	config := privateHTTP.Transport.(*http.Transport).TLSClientConfig.Clone()
	config.Certificates = nil
	untrusted := &http.Client{Transport: &http.Transport{Proxy: nil, TLSClientConfig: config}, Timeout: 3 * time.Second}
	defer untrusted.CloseIdleConnections()
	if _, err := taskexecutionv1connect.NewRepairExecutionServiceClient(untrusted, executionURL).ResolveRepairBuildInput(ctx, connect.NewRequest(&executionv1.ResolveRepairBuildInputRequest{LeaseId: lease.GetLeaseId(), WorkerId: repairSourceWorker})); err == nil {
		t.Fatal("missing mTLS client certificate accepted")
	}
	// Upgrade after admission. The repair input must retain v1 and its original test command.
	changed := bytes.Replace(repairSourceManifest(t, base, "2.0.0"), []byte(`"test"`), []byte(`"vet"`), 1)
	if _, err := registry.RegisterApp(ctx, connect.NewRequest(&appv1.RegisterAppRequest{IdempotencyKey: "repair-new-version", ManifestYaml: changed})); err != nil {
		t.Fatal(err)
	}
	if _, err := installations.TransitionAppVersion(ctx, connect.NewRequest(&appv1.TransitionAppVersionRequest{IdempotencyKey: "repair-upgrade", ProjectId: project.Msg.GetProject().GetId(), InstallationId: installed.Msg.GetInstallation().GetId(), ExpectedProjectRevision: 2, Version: "2.0.0"})); err != nil {
		t.Fatal(err)
	}
	inputJSON, err := protojson.Marshal(input.Msg.GetInput())
	if err != nil {
		t.Fatal(err)
	}
	candidateJSON, err := protojson.Marshal(candidate.Msg.GetCandidate())
	if err != nil {
		t.Fatal(err)
	}
	state := repairSourceRPCState{LeaseID: lease.GetLeaseId(), TaskID: admitted.Msg.GetTaskId(), ProjectID: project.Msg.GetProject().GetId(), InstallationID: installed.Msg.GetInstallation().GetId(), SourceID: base.ID, SourceDigest: base.Digest, Input: inputJSON, Candidate: candidateJSON}
	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(repairSourceStatePath(t), encoded, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestRepairSourceRPCRestore(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	encoded, err := os.ReadFile(repairSourceStatePath(t))
	if err != nil {
		t.Fatal(err)
	}
	var state repairSourceRPCState
	if err := json.Unmarshal(encoded, &state); err != nil {
		t.Fatal(err)
	}
	private := repairSourceTLSClient(t)
	repair := taskexecutionv1connect.NewRepairExecutionServiceClient(private, repairSourceGateEnv(t, "EXECUTION_URL"))
	input, err := repair.ResolveRepairBuildInput(ctx, connect.NewRequest(&executionv1.ResolveRepairBuildInputRequest{LeaseId: state.LeaseID, WorkerId: repairSourceWorker}))
	if err != nil {
		t.Fatal(err)
	}
	expectedInput := &executionv1.RepairBuildInput{}
	if err := protojson.Unmarshal(state.Input, expectedInput); err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(input.Msg.GetInput(), expectedInput) {
		t.Fatal("Core restart or installation upgrade changed build input")
	}
	files := candidateFiles()
	files[0], files[1] = files[1], files[0]
	candidate, err := repair.SubmitRepairSourceCandidate(ctx, connect.NewRequest(&executionv1.SubmitRepairSourceCandidateRequest{LeaseId: state.LeaseID, WorkerId: repairSourceWorker, Files: files}))
	if err != nil {
		t.Fatal(err)
	}
	expectedCandidate := &executionv1.RepairSourceCandidate{}
	if err := protojson.Unmarshal(state.Candidate, expectedCandidate); err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(candidate.Msg.GetCandidate(), expectedCandidate) {
		t.Fatal("candidate replay changed identity/time/digest")
	}
	publicHTTP := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 10 * time.Second}
	defer publicHTTP.CloseIdleConnections()
	registry := appv1connect.NewAppRegistryServiceClient(publicHTTP, repairSourceGateEnv(t, "GATEWAY_URL"))
	app, err := registry.GetApp(ctx, connect.NewRequest(&appv1.GetAppRequest{AppId: "repair-source-fixture"}))
	if err != nil || app.Msg.GetApp().GetVersion() != "2.0.0" {
		t.Fatalf("candidate affected default Registry version: %v", err)
	}
	tasks := agentv1connect.NewAgentTaskServiceClient(publicHTTP, repairSourceGateEnv(t, "GATEWAY_URL"))
	if _, err := tasks.CancelTask(ctx, connect.NewRequest(&agentv1.CancelTaskRequest{TaskId: state.TaskID, Reason: "fixture finished"})); err != nil {
		t.Fatal(err)
	}
	if _, err := repair.ResolveRepairBuildInput(ctx, connect.NewRequest(&executionv1.ResolveRepairBuildInputRequest{LeaseId: state.LeaseID, WorkerId: repairSourceWorker})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("cancelled resolve: %v", err)
	}
	if _, err := repair.SubmitRepairSourceCandidate(ctx, connect.NewRequest(&executionv1.SubmitRepairSourceCandidateRequest{LeaseId: state.LeaseID, WorkerId: repairSourceWorker, Files: files})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("cancelled replay: %v", err)
	}
}

func repairProducerInputFiles() []*appv1.AppSourceFile {
	return []*appv1.AppSourceFile{
		{Path: "go.mod", Content: []byte("module repair-fixture\n\ngo 1.26\n")},
		{Path: "main.go", Content: []byte("package main\nfunc answer() int { return 0 }\nfunc main() {}\n")},
		{Path: "main_test.go", Content: []byte("package main\nimport \"testing\"\nfunc TestAnswer(t *testing.T) { if answer() != 42 { t.Fatal(\"wrong answer\") } }\n")},
	}
}
func TestRepairSourceRPCProducer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	publicURL := repairSourceGateEnv(t, "GATEWAY_URL")
	coreURL := repairSourceGateEnv(t, "CORE_URL")
	client := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 10 * time.Second}
	defer client.CloseIdleConnections()
	project, err := projectv1connect.NewProjectServiceClient(client, publicURL).CreateProject(ctx, connect.NewRequest(&projectv1.CreateProjectRequest{IdempotencyKey: "producer-project", Name: "Repair producer fixture", HarnessBinding: &projectv1.HarnessBinding{ProviderId: "generic-cli", InstancePolicy: projectv1.HarnessInstancePolicy_HARNESS_INSTANCE_POLICY_EPHEMERAL, ResourcePolicyId: "project-no-tools"}}))
	if err != nil {
		t.Fatal(err)
	}
	source, err := appv1connect.NewAppSourceBundleServiceClient(client, publicURL).CreateAppSourceBundle(ctx, connect.NewRequest(&appv1.CreateAppSourceBundleRequest{IdempotencyKey: "producer-base", Files: repairProducerInputFiles()}))
	if err != nil {
		t.Fatal(err)
	}
	base := registrydomain.SourceBundle{ID: source.Msg.GetBundle().GetId(), Digest: source.Msg.GetBundle().GetDigest()}
	registry := appv1connect.NewAppRegistryServiceClient(client, publicURL)
	if _, err := registry.RegisterApp(ctx, connect.NewRequest(&appv1.RegisterAppRequest{IdempotencyKey: "producer-version", ManifestYaml: repairSourceManifest(t, base, "1.0.0")})); err != nil {
		t.Fatal(err)
	}
	installed, err := appv1connect.NewAppInstallationServiceClient(client, publicURL).InstallApp(ctx, connect.NewRequest(&appv1.InstallAppRequest{IdempotencyKey: "producer-install", ProjectId: project.Msg.GetProject().GetId(), AppId: "repair-source-fixture", Version: "1.0.0", ExpectedProjectRevision: 1}))
	if err != nil {
		t.Fatal(err)
	}
	owner := project.Msg.GetProject().GetOwnerUserId()
	incident := ids.UUIDv7{}.New()
	request := connect.NewRequest(&agentv1.CreateRepairTaskRequest{IdempotencyKey: "producer-repair", ProjectId: project.Msg.GetProject().GetId(), AppInstanceId: installed.Msg.GetInstallation().GetId(), IncidentId: incident, ViolationSummary: "Repair deterministic answer regression"})
	request.Header().Set(identity.UserHeader, owner)
	request.Header().Set(identity.DeviceHeader, "01999999-9999-7999-8999-000000000b02")
	task, err := agentv1connect.NewAgentRepairTaskServiceClient(client, coreURL).CreateRepairTask(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if task.Msg.GetProviderId() != "generic-cli" {
		t.Fatal("repair was not routed to the real CLI adapter")
	}
	tasks := agentv1connect.NewAgentTaskServiceClient(client, publicURL)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		state, err := tasks.GetTask(ctx, connect.NewRequest(&agentv1.GetTaskRequest{TaskId: task.Msg.GetTaskId()}))
		if err != nil {
			t.Fatal(err)
		}
		if state.Msg.GetTask().GetState() == agentv1.AgentTaskState_AGENT_TASK_STATE_COMPLETED {
			break
		}
		if state.Msg.GetTask().GetState() == agentv1.AgentTaskState_AGENT_TASK_STATE_FAILED || state.Msg.GetTask().GetState() == agentv1.AgentTaskState_AGENT_TASK_STATE_CANCELLED {
			t.Fatal("repair producer did not complete")
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-ticker.C:
		}
	}
	reader := taskexecutionv1connect.NewRepairCandidateServiceClient(client, coreURL, connect.WithReadMaxBytes(2*1024*1024))
	read := connect.NewRequest(&executionv1.GetRepairSourceCandidateRequest{TaskId: task.Msg.GetTaskId()})
	read.Header().Set(identity.UserHeader, owner)
	read.Header().Set(identity.DeviceHeader, "01999999-9999-7999-8999-000000000b02")
	candidate, err := reader.GetRepairSourceCandidate(ctx, read)
	if err != nil {
		t.Fatal(err)
	}
	if candidate.Msg.GetProjectId() != project.Msg.GetProject().GetId() || candidate.Msg.GetIncidentId() != incident || !proto.Equal(candidate.Msg.GetInput().GetSource(), source.Msg.GetBundle()) || candidate.Msg.GetCandidateSource().GetDigest() == base.Digest {
		t.Fatal("candidate provenance or base source drift")
	}
	expected := repairProducerInputFiles()
	expected[1].Content = bytes.ReplaceAll(expected[1].Content, []byte("return 0"), []byte("return 42"))
	got := candidate.Msg.GetCandidateSource().GetFiles()
	if len(got) != len(expected) {
		t.Fatal("candidate lost source files")
	}
	for i, file := range got {
		if !proto.Equal(file, expected[i]) {
			t.Fatal("candidate modified unexpected source or test content")
		}
	}
	read.Header().Set(identity.UserHeader, ids.UUIDv7{}.New())
	if _, err := reader.GetRepairSourceCandidate(ctx, read); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("foreign candidate owner: %v", err)
	}
	publicReader := taskexecutionv1connect.NewRepairCandidateServiceClient(client, publicURL)
	if _, err := publicReader.GetRepairSourceCandidate(ctx, connect.NewRequest(&executionv1.GetRepairSourceCandidateRequest{TaskId: task.Msg.GetTaskId()})); err == nil {
		t.Fatal("candidate reader exposed through Gateway")
	}
	app, err := registry.GetApp(ctx, connect.NewRequest(&appv1.GetAppRequest{AppId: "repair-source-fixture"}))
	if err != nil || app.Msg.GetApp().GetVersion() != "1.0.0" {
		t.Fatalf("source proposal became installable: %v", err)
	}
	encoded, err := protojson.Marshal(candidate.Msg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repairSourceGateEnv(t, "DIR"), "producer-state.json"), encoded, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestRepairSourceRPCProducerRestore(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	raw, err := os.ReadFile(filepath.Join(repairSourceGateEnv(t, "DIR"), "producer-state.json"))
	if err != nil {
		t.Fatal(err)
	}
	expected := &executionv1.GetRepairSourceCandidateResponse{}
	if err := protojson.Unmarshal(raw, expected); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 10 * time.Second}
	defer client.CloseIdleConnections()
	request := connect.NewRequest(&executionv1.GetRepairSourceCandidateRequest{TaskId: expected.GetInput().GetTaskId()})
	request.Header().Set(identity.UserHeader, "01999999-9999-7999-8999-000000000b01")
	request.Header().Set(identity.DeviceHeader, "01999999-9999-7999-8999-000000000b02")
	response, err := taskexecutionv1connect.NewRepairCandidateServiceClient(client, repairSourceGateEnv(t, "CORE_URL"), connect.WithReadMaxBytes(2*1024*1024)).GetRepairSourceCandidate(ctx, request)
	if err != nil || !proto.Equal(response.Msg, expected) {
		t.Fatalf("Core/Harness restart changed candidate: %v", err)
	}
}
