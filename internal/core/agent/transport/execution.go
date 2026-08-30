package transport

import (
	"context"
	"errors"
	"net/http"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
	artifactv1 "github.com/yangtao121/workos/gen/go/workos/artifact/v1"
	taskv1 "github.com/yangtao121/workos/gen/go/workos/taskexecution/v1"
	"github.com/yangtao121/workos/gen/go/workos/taskexecution/v1/taskexecutionv1connect"
	"github.com/yangtao121/workos/internal/core/agent/application"
	"github.com/yangtao121/workos/internal/core/agent/domain"
	"github.com/yangtao121/workos/internal/core/orchestration"
)

// MaxExecutionRequestBytes bounds every private TaskExecutionService request
// before the Connect stack decodes it. The legal artifact payload is at most
// 512 KiB of canonical content; protojson inflates bytes 4/3 through base64
// to ~683 KiB before keys and punctuation, so 768 KiB covers the legal
// maximum with headroom while capping gzip-bomb and oversize decode work.
// The library default is unlimited. The composition root applies this
// constant when constructing the handler.
const MaxExecutionRequestBytes = 768 * 1024

// TaskArtifactMaterializer is the orchestration-provided lease-bound
// materialization service. The handler defines the narrow contract it needs;
// the composition layer implements it by coordinating the Agent and Artifact
// modules inside one transaction.
type TaskArtifactMaterializer interface {
	MaterializeTaskArtifact(ctx context.Context, leaseID, workerID, outputKey, title, artifactType string, content []byte) (*artifactv1.Artifact, *agentv1.AgentEvent, error)
}

// TaskContextResolver is the orchestration-provided lease-bound context
// materialization contract (ADR-0010). The handler defines the narrow
// contract it needs; the composition layer implements it by coordinating the
// Agent and Artifact modules inside one transaction.
type TaskContextResolver interface {
	Resolve(ctx context.Context, taskLeaseID, workerID string) ([]orchestration.ResolvedDocument, error)
}

type ExecutionHandler struct {
	service     *application.Service
	materialize TaskArtifactMaterializer
	contexts    TaskContextResolver
}

func NewExecution(service *application.Service, materializer TaskArtifactMaterializer, contexts TaskContextResolver) *ExecutionHandler {
	return &ExecutionHandler{service: service, materialize: materializer, contexts: contexts}
}

// NewExecutionConnectHandler is the single construction path for the private
// TaskExecution service. Its decompressed request budget is enforced by
// Connect before protobuf/JSON decoding and therefore before a materializer
// can observe an oversized or compressed-bomb payload.
func NewExecutionConnectHandler(service *application.Service, materializer TaskArtifactMaterializer, contexts TaskContextResolver) (string, http.Handler) {
	return taskexecutionv1connect.NewTaskExecutionServiceHandler(
		NewExecution(service, materializer, contexts),
		connect.WithReadMaxBytes(MaxExecutionRequestBytes),
	)
}

func (h *ExecutionHandler) ClaimTask(ctx context.Context, req *connect.Request[taskv1.ClaimTaskRequest]) (*connect.Response[taskv1.ClaimTaskResponse], error) {
	duration := durationFromProto(req.Msg.GetLeaseDuration())
	lease, err := h.service.Claim(ctx, req.Msg.GetWorkerId(), duration)
	if err != nil {
		return nil, mapError(err)
	}
	response := &taskv1.ClaimTaskResponse{}
	if lease != nil {
		response.Lease = &taskv1.TaskLease{
			LeaseId: lease.ID, WorkerId: lease.WorkerID, Task: taskToProto(lease.Task), ExpiresAt: timestamppb.New(lease.ExpiresAt),
			// The flag mirrors the durable snapshot: secret material flows
			// only through AcquireTaskCredential on this same authenticated
			// channel (ADR-0009).
			RequiresTaskCredential: lease.Task.Credential != nil,
		}
	}
	return connect.NewResponse(response), nil
}

func (h *ExecutionHandler) RenewTaskLease(ctx context.Context, req *connect.Request[taskv1.RenewTaskLeaseRequest]) (*connect.Response[taskv1.RenewTaskLeaseResponse], error) {
	expires, cancelled, err := h.service.Renew(ctx, req.Msg.GetLeaseId(), req.Msg.GetWorkerId(), durationFromProto(req.Msg.GetLeaseDuration()))
	if err != nil {
		return nil, mapError(err)
	}
	return connect.NewResponse(&taskv1.RenewTaskLeaseResponse{ExpiresAt: timestamppb.New(expires), CancellationRequested: cancelled}), nil
}

func (h *ExecutionHandler) AppendTaskEvent(ctx context.Context, req *connect.Request[taskv1.AppendTaskEventRequest]) (*connect.Response[taskv1.AppendTaskEventResponse], error) {
	event := req.Msg.GetEvent()
	if event == nil || event.Event == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, domain.ErrInvalid)
	}
	// Fail closed: ArtifactCreated events are Core-minted facts published by
	// AppendTaskArtifact from the verified artifact projection. A
	// provider-built reference could name a foreign or nonexistent artifact
	// without ever proving ownership, type, or existence.
	if event.GetArtifactCreated() != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("artifact_created events are Core-minted via AppendTaskArtifact"))
	}
	event.Id, event.TaskId, event.Sequence, event.OccurredAt = "", "", 0, nil
	eventType, state, providerID, runID := classifyEvent(event)
	var usage *domain.UsageReport
	if recorded := event.GetUsageRecorded(); recorded != nil {
		report := domain.UsageReport{
			InputTokens: recorded.GetInputTokens(), OutputTokens: recorded.GetOutputTokens(),
			CostDecimal: recorded.GetCostDecimal(), Model: recorded.GetModel(),
		}
		if err := report.Validate(); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		usage = &report
	}
	payload, err := protojson.Marshal(event)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, domain.ErrInvalid)
	}
	stored, err := h.service.AppendEvent(ctx, req.Msg.GetLeaseId(), req.Msg.GetWorkerId(), eventType, payload, state, providerID, runID, usage)
	if err != nil {
		return nil, mapError(err)
	}
	var result agentv1.AgentEvent
	if err := protojson.Unmarshal(stored.Payload, &result); err != nil {
		return nil, connect.NewError(connect.CodeDataLoss, errors.New("stored task event is invalid"))
	}
	return connect.NewResponse(&taskv1.AppendTaskEventResponse{StoredEvent: &result}), nil
}

func (h *ExecutionHandler) FinishTaskLease(ctx context.Context, req *connect.Request[taskv1.FinishTaskLeaseRequest]) (*connect.Response[taskv1.FinishTaskLeaseResponse], error) {
	if err := h.service.Finish(ctx, req.Msg.GetLeaseId(), req.Msg.GetWorkerId()); err != nil {
		return nil, mapError(err)
	}
	return connect.NewResponse(&taskv1.FinishTaskLeaseResponse{}), nil
}

// AppendTaskArtifact materializes one provider artifact output under the
// active task lease. The request carries only the output key, title, and
// typed content — every identity fact is derived server-side from the lease.
func (h *ExecutionHandler) AppendTaskArtifact(ctx context.Context, req *connect.Request[taskv1.AppendTaskArtifactRequest]) (*connect.Response[taskv1.AppendTaskArtifactResponse], error) {
	output := req.Msg.GetArtifact()
	if output == nil || output.Content == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, domain.ErrInvalid)
	}
	var artifactType string
	var content []byte
	switch value := output.Content.(type) {
	case *taskv1.TaskArtifactOutput_Markdown:
		artifactType = "document.markdown.v1"
		content = value.Markdown.GetContent()
	case *taskv1.TaskArtifactOutput_UnifiedDiff:
		artifactType = "code.unified-diff.v1"
		content = value.UnifiedDiff.GetContent()
	default:
		return nil, connect.NewError(connect.CodeInvalidArgument, domain.ErrInvalid)
	}
	if h.materialize == nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("task artifact materialization is not configured"))
	}
	artifact, event, err := h.materialize.MaterializeTaskArtifact(
		ctx, req.Msg.GetLeaseId(), req.Msg.GetWorkerId(), output.GetOutputKey(), output.GetTitle(), artifactType, content,
	)
	if err != nil {
		return nil, mapError(err)
	}
	return connect.NewResponse(&taskv1.AppendTaskArtifactResponse{Artifact: artifact, Event: event}), nil
}

// ResolveTaskContext materializes the task's immutable context refs under
// the active lease. The request carries only the lease and worker
// identifiers — every identity fact is derived server-side (ADR-0010).
func (h *ExecutionHandler) ResolveTaskContext(ctx context.Context, req *connect.Request[taskv1.ResolveTaskContextRequest]) (*connect.Response[taskv1.ResolveTaskContextResponse], error) {
	if req.Msg.GetLeaseId() == "" || req.Msg.GetWorkerId() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, domain.ErrInvalid)
	}
	if h.contexts == nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("task context resolution is not configured"))
	}
	documents, err := h.contexts.Resolve(ctx, req.Msg.GetLeaseId(), req.Msg.GetWorkerId())
	if err != nil {
		return nil, mapError(err)
	}
	response := &taskv1.ResolveTaskContextResponse{}
	for _, document := range documents {
		response.Documents = append(response.Documents, &taskv1.ResolvedTaskContextDocument{
			RefType:      document.RefType,
			ArtifactType: document.ArtifactType,
			ArtifactId:   document.ArtifactID,
			Digest:       document.Digest,
			Title:        document.Title,
			MediaType:    document.MediaType,
			Content:      document.Content,
		})
	}
	return connect.NewResponse(response), nil
}

func classifyEvent(event *agentv1.AgentEvent) (string, domain.State, string, string) {
	switch value := event.Event.(type) {
	case *agentv1.AgentEvent_RunStarted:
		return "run_started", domain.StateRunning, value.RunStarted.GetProviderId(), value.RunStarted.GetRunId()
	case *agentv1.AgentEvent_RunWaiting:
		return "run_waiting", domain.StateWaiting, "", ""
	case *agentv1.AgentEvent_RunCompleted:
		return "run_completed", domain.StateCompleted, "", ""
	case *agentv1.AgentEvent_RunFailed:
		return "run_failed", domain.StateFailed, "", ""
	case *agentv1.AgentEvent_RunCancelled:
		return "run_cancelled", domain.StateCancelled, "", ""
	case *agentv1.AgentEvent_AssistantDelta:
		return "assistant_delta", domain.StateRunning, "", ""
	case *agentv1.AgentEvent_AssistantMessage:
		return "assistant_message", domain.StateRunning, "", ""
	case *agentv1.AgentEvent_ToolCallStarted:
		return "tool_call_started", domain.StateRunning, "", ""
	case *agentv1.AgentEvent_ToolCallCompleted:
		return "tool_call_completed", domain.StateRunning, "", ""
	case *agentv1.AgentEvent_ApprovalRequired:
		return "approval_required", domain.StateWaiting, "", ""
	case *agentv1.AgentEvent_ArtifactCreated:
		return "artifact_created", domain.StateRunning, "", ""
	case *agentv1.AgentEvent_UsageRecorded:
		return "usage_recorded", domain.StateRunning, "", ""
	default:
		return "unknown", domain.StateRunning, "", ""
	}
}

func durationFromProto(value *durationpb.Duration) time.Duration {
	if value == nil {
		return 0
	}
	return value.AsDuration()
}
