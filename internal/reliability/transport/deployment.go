// Deployment driver client (ADR-0016 §6): the reliability-host side of
// Core's public ADR-0012 version semantics. The controller drives the same
// owner-triggered TransitionAppVersion and RollbackAppVersion an owner
// could, over the loopback with the incident's owner identity — it never
// writes Core tables directly.
package transport

import (
	"context"

	"connectrpc.com/connect"

	appv1 "github.com/yangtao121/workos/gen/go/workos/app/v1"
	appv1connect "github.com/yangtao121/workos/gen/go/workos/app/v1/appv1connect"
	"github.com/yangtao121/workos/internal/platform/identity"
	"github.com/yangtao121/workos/internal/platform/telemetry"
)

// DeploymentDriverClient drives Core's ADR-0012 version semantics.
type DeploymentDriverClient struct {
	client appv1connect.AppInstallationServiceClient
}

// NewDeploymentDriverClient wires the Core installation client.
func NewDeploymentDriverClient(coreURL string) *DeploymentDriverClient {
	return &DeploymentDriverClient{client: appv1connect.NewAppInstallationServiceClient(telemetry.HTTPClient(), coreURL)}
}

// Transition canaries the candidate target version (ADR-0012
// TransitionAppVersion). Grants must be coverable by the target's requested
// set or Core refuses with FailedPrecondition.
func (c *DeploymentDriverClient) Transition(ctx context.Context, ownerUserID, projectID, installationID, targetVersion, idempotencyKey string) error {
	// ExpectedProjectRevision: the ADR-0012 RPC re-verifies installation
	// facts server-side; a concurrent owner revision surfaces as Aborted and
	// the controller marks the attempt failed rather than retrying blind.
	request := connect.NewRequest(&appv1.TransitionAppVersionRequest{
		IdempotencyKey: idempotencyKey,
		ProjectId:      projectID,
		InstallationId: installationID,
		Version:        targetVersion,
	})
	request.Header().Set(identity.UserHeader, ownerUserID)
	request.Header().Set(identity.DeviceHeader, "reliability-host-deploy")
	_, err := c.client.TransitionAppVersion(ctx, request)
	return err
}

// Rollback returns the installation to its previous pinned version
// (ADR-0012 RollbackAppVersion).
func (c *DeploymentDriverClient) Rollback(ctx context.Context, ownerUserID, projectID, installationID, idempotencyKey string) error {
	request := connect.NewRequest(&appv1.RollbackAppVersionRequest{
		IdempotencyKey: idempotencyKey,
		ProjectId:      projectID,
		InstallationId: installationID,
	})
	request.Header().Set(identity.UserHeader, ownerUserID)
	request.Header().Set(identity.DeviceHeader, "reliability-host-deploy")
	_, err := c.client.RollbackAppVersion(ctx, request)
	return err
}
