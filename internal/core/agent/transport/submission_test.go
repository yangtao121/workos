package transport

import (
	"context"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
	"github.com/yangtao121/workos/gen/go/workos/agent/v1/agentv1connect"
	"github.com/yangtao121/workos/internal/core/agent/application"
	"github.com/yangtao121/workos/internal/core/agent/domain"
	"github.com/yangtao121/workos/internal/platform/identity"
)

type recordingSubmitter struct{ calls int }

func (s *recordingSubmitter) Submit(context.Context, application.SubmitInput) (domain.Task, error) {
	s.calls++
	return domain.Task{}, nil
}
func TestPublicSubmissionRejectsPrivateIncidentLink(t *testing.T) {
	submitter := &recordingSubmitter{}
	_, handler := agentv1connect.NewAgentTaskServiceHandler(New(nil, submitter))
	server := httptest.NewServer(identity.Middleware(handler))
	defer server.Close()
	client := agentv1connect.NewAgentTaskServiceClient(server.Client(), server.URL)
	request := connect.NewRequest(&agentv1.SubmitTaskRequest{IdempotencyKey: "spoofed-repair", Input: &agentv1.AgentTaskInput{Goal: "repair", IncidentId: "0198d7ea-2110-7c42-b659-c5e4d73bc331", TargetScope: &agentv1.TargetScope{Scope: &agentv1.TargetScope_ProjectId{ProjectId: "0198d7ea-2110-7c42-b659-c5e4d73bc332"}}}})
	request.Header().Set(identity.UserHeader, "owner")
	request.Header().Set(identity.DeviceHeader, "device")
	if _, err := client.SubmitTask(context.Background(), request); connect.CodeOf(err) != connect.CodeInvalidArgument || submitter.calls != 0 {
		t.Fatalf("public incident provenance accepted: %v calls=%d", err, submitter.calls)
	}
}

func TestPublicSubmissionRejectsPrivateRepairTarget(t *testing.T) {
	submitter := &recordingSubmitter{}
	_, handler := agentv1connect.NewAgentTaskServiceHandler(New(nil, submitter))
	server := httptest.NewServer(identity.Middleware(handler))
	defer server.Close()
	client := agentv1connect.NewAgentTaskServiceClient(server.Client(), server.URL)
	request := connect.NewRequest(&agentv1.SubmitTaskRequest{IdempotencyKey: "spoofed-target", Input: &agentv1.AgentTaskInput{Goal: "repair", RepairTarget: &agentv1.RepairTarget{AppInstanceId: "0198d7ea-2110-7c42-b659-c5e4d73bc331"}}})
	request.Header().Set(identity.UserHeader, "owner")
	request.Header().Set(identity.DeviceHeader, "device")
	if _, err := client.SubmitTask(context.Background(), request); connect.CodeOf(err) != connect.CodeInvalidArgument || submitter.calls != 0 {
		t.Fatalf("public repair target accepted: %v calls=%d", err, submitter.calls)
	}
}

func TestUnhealthyProviderReturnsActionablePrecondition(t *testing.T) {
	err := mapError(domain.ErrProviderUnavailable)
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("health error: %v", err)
	}
}
