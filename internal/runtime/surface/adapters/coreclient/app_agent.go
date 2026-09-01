// The Core App Agent client adapter: it forwards the trusted owner/device
// identity from the context on every call and maps transport verdicts to the
// port sentinels. It never imports Core packages and never logs payloads.
package coreclient

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"

	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
	agentv1connect "github.com/yangtao121/workos/gen/go/workos/agent/v1/agentv1connect"
	"github.com/yangtao121/workos/internal/platform/identity"
	"github.com/yangtao121/workos/internal/runtime/surface/ports"
)

// AppAgent talks to Core's private App Agent service. Core is expected on the
// private loopback listener; the identity headers are re-set on every call so
// a spoofed upstream value can never survive.
type AppAgent struct {
	client agentv1connect.AppAgentServiceClient
}

func NewAppAgent(client agentv1connect.AppAgentServiceClient) (*AppAgent, error) {
	if client == nil {
		return nil, errors.New("app agent client requires the Core app agent client")
	}
	return &AppAgent{client: client}, nil
}

func (a *AppAgent) RunAgentTask(ctx context.Context, query ports.AppAgentRunQuery) (ports.AppTaskSubmission, error) {
	identityValue, err := identity.FromContext(ctx)
	if err != nil {
		return ports.AppTaskSubmission{}, err
	}
	request := connect.NewRequest(&agentv1.RunAgentTaskRequest{
		ProjectId: query.ProjectID, AppInstanceId: query.AppInstanceID,
		// The grant epoch comes exclusively from the validated session
		// snapshot the application derived; a public bridge body can never
		// supply or override it (ADR-0003 §7).
		InstallationGrantRevision: query.InstallationGrantRevision,
		ClientIdempotencyKey:      query.ClientKey, Role: query.Role, Goal: query.Goal,
	})
	request.Header().Set(identity.UserHeader, identityValue.UserID)
	request.Header().Set(identity.DeviceHeader, identityValue.DeviceID)
	response, err := a.client.RunAgentTask(ctx, request)
	if err != nil {
		return ports.AppTaskSubmission{}, mapAppAgentError(err)
	}
	return ports.AppTaskSubmission{
		TaskID:            response.Msg.GetTaskId(),
		State:             response.Msg.GetState().String(),
		LastEventSequence: response.Msg.GetLastEventSequence(),
	}, nil
}

func (a *AppAgent) WatchAgentTaskEvents(ctx context.Context, query ports.AppAgentWatchQuery, onEvent func(*agentv1.AgentEvent) error) error {
	identityValue, err := identity.FromContext(ctx)
	if err != nil {
		return err
	}
	request := connect.NewRequest(&agentv1.WatchAgentTaskEventsRequest{
		ProjectId: query.ProjectID, AppInstanceId: query.AppInstanceID,
		TaskId: query.TaskID, AfterSequence: query.AfterSequence,
		// Same session-derived grant epoch as the run call: Core compares it
		// on every polling round and ends the stream on any mismatch.
		InstallationGrantRevision: query.InstallationGrantRevision,
	})
	request.Header().Set(identity.UserHeader, identityValue.UserID)
	request.Header().Set(identity.DeviceHeader, identityValue.DeviceID)
	stream, err := a.client.WatchAgentTaskEvents(ctx, request)
	if err != nil {
		return mapAppAgentError(err)
	}
	for stream.Receive() {
		if err := onEvent(stream.Msg().GetEvent()); err != nil {
			// Ending the local stream never cancels the durable task; the
			// error surfaces as the caller's stream teardown.
			return err
		}
	}
	if err := stream.Err(); err != nil {
		return mapAppAgentError(err)
	}
	return nil
}

// AuthorizeAppKnowledge re-verifies the per-call knowledge authorization with
// Core and returns the trusted binding. The runtime's own identity headers
// are overwritten on every call; the scope facts come from the validated
// session, never from public input.
func (a *AppAgent) AuthorizeAppKnowledge(ctx context.Context, query ports.AppKnowledgeAuthQuery) (ports.AppKnowledgeBinding, error) {
	identityValue, err := identity.FromContext(ctx)
	if err != nil {
		return ports.AppKnowledgeBinding{}, err
	}
	request := connect.NewRequest(&agentv1.AuthorizeAppKnowledgeRequest{
		ProjectId:                 query.ProjectID,
		AppInstanceId:             query.AppInstanceID,
		InstallationGrantRevision: query.InstallationGrantRevision,
	})
	request.Header().Set(identity.UserHeader, identityValue.UserID)
	request.Header().Set(identity.DeviceHeader, identityValue.DeviceID)
	response, err := a.client.AuthorizeAppKnowledge(ctx, request)
	if err != nil {
		return ports.AppKnowledgeBinding{}, mapAppAgentError(err)
	}
	return ports.AppKnowledgeBinding{
		OwnerUserID: response.Msg.GetOwnerUserId(),
		ProjectID:   response.Msg.GetProjectId(),
	}, nil
}

// mapAppAgentError converts Connect codes to the port sentinels. Denial
// codes collapse into one sanitized sentinel so no Core authorization detail
// survives the boundary; unknown codes stay opaque internal failures.
func mapAppAgentError(err error) error {
	switch connect.CodeOf(err) {
	case connect.CodeInvalidArgument:
		return fmt.Errorf("%w: %s", ports.ErrAppAgentDenied, "invalid bridge input")
	case connect.CodeNotFound:
		return fmt.Errorf("%w: %s", ports.ErrAppAgentDenied, "app task is not available")
	case connect.CodePermissionDenied:
		return fmt.Errorf("%w: %s", ports.ErrAppAgentDenied, "app capability is not granted")
	case connect.CodeFailedPrecondition:
		return fmt.Errorf("%w: %s", ports.ErrAppAgentDenied, "app request was rejected")
	case connect.CodeAborted:
		return fmt.Errorf("%w: %s", ports.ErrAppAgentConflict, "idempotency conflict")
	case connect.CodeResourceExhausted:
		return fmt.Errorf("%w: %s", ports.ErrAppAgentExhausted, "daily allowance is exhausted")
	case connect.CodeDataLoss:
		return fmt.Errorf("core app agent returned an invalid event stream: %w", err)
	case connect.CodeUnavailable, connect.CodeDeadlineExceeded, connect.CodeCanceled:
		return fmt.Errorf("%w: %s", ports.ErrAppAgentUnavailable, connect.CodeOf(err).String())
	default:
		return fmt.Errorf("core app agent call failed: %w", err)
	}
}
