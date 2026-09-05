// Repair submitter client (ADR-0016 §5): the reliability-host side of the
// private Core repair admission RPC. It presents the incident's owner as the
// trusted identity (the same forwarded-identity pattern every internal
// caller uses on the private loopback) and surfaces task facts only.
package transport

import (
	"context"

	"connectrpc.com/connect"

	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
	agentv1connect "github.com/yangtao121/workos/gen/go/workos/agent/v1/agentv1connect"
	"github.com/yangtao121/workos/internal/platform/identity"
	"github.com/yangtao121/workos/internal/platform/telemetry"
)

// RepairSubmitterClient submits repair tasks to Core's private repair RPC.
type RepairSubmitterClient struct {
	client   agentv1connect.AgentRepairTaskServiceClient
	tasks    agentv1connect.AgentTaskServiceClient
	deviceID string
}

// NewRepairSubmitterClient wires the Core repair admission client plus the
// task state reader (the terminal check drives the deployment hand-off).
func NewRepairSubmitterClient(coreURL string, deviceID string) *RepairSubmitterClient {
	return &RepairSubmitterClient{
		client:   agentv1connect.NewAgentRepairTaskServiceClient(telemetry.HTTPClient(), coreURL),
		tasks:    agentv1connect.NewAgentTaskServiceClient(telemetry.HTTPClient(), coreURL),
		deviceID: deviceID,
	}
}

// SubmitRepair admits one repair task. The reliability orchestrator derives
// every fact from its own incident row; the identity headers carry the
// incident's owner exactly like the forwarded trusted identity on every
// other internal call.
func (c *RepairSubmitterClient) SubmitRepair(ctx context.Context, ownerUserID, projectID, incidentID, idempotencyKey, violationSummary string) (taskID, providerID string, err error) {
	request := connect.NewRequest(&agentv1.CreateRepairTaskRequest{
		IncidentId:       incidentID,
		ProjectId:        projectID,
		ViolationSummary: violationSummary,
		IdempotencyKey:   idempotencyKey,
	})
	request.Header().Set(identity.UserHeader, ownerUserID)
	request.Header().Set(identity.DeviceHeader, "reliability-host-repair")
	response, err := c.client.CreateRepairTask(ctx, request)
	if err != nil {
		return "", "", err
	}
	return response.Msg.GetTaskId(), response.Msg.GetProviderId(), nil
}

// RepairTaskCompleted reports whether one repair task reached the terminal
// COMPLETED state on Core (ADR-0016 §5-6 hand-off).
func (c *RepairSubmitterClient) RepairTaskCompleted(ctx context.Context, ownerUserID, taskID string) (bool, error) {
	request := connect.NewRequest(&agentv1.GetTaskRequest{TaskId: taskID})
	request.Header().Set(identity.UserHeader, ownerUserID)
	request.Header().Set(identity.DeviceHeader, c.deviceID)
	response, err := c.tasks.GetTask(ctx, request)
	if err != nil {
		return false, err
	}
	return response.Msg.GetTask().GetState() == agentv1.AgentTaskState_AGENT_TASK_STATE_COMPLETED, nil
}
