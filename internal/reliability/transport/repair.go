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
	client agentv1connect.AgentRepairTaskServiceClient
}

// NewRepairSubmitterClient wires the Core repair admission client.
func NewRepairSubmitterClient(coreURL string) *RepairSubmitterClient {
	return &RepairSubmitterClient{client: agentv1connect.NewAgentRepairTaskServiceClient(telemetry.HTTPClient(), coreURL)}
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
