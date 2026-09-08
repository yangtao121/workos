package transport

import (
	"context"
	"errors"
	"time"

	"connectrpc.com/connect"
	appv1 "github.com/yangtao121/workos/gen/go/workos/app/v1"
	appv1connect "github.com/yangtao121/workos/gen/go/workos/app/v1/appv1connect"
	surfacev1 "github.com/yangtao121/workos/gen/go/workos/surface/v1"
	surfacev1connect "github.com/yangtao121/workos/gen/go/workos/surface/v1/surfacev1connect"
	"github.com/yangtao121/workos/internal/platform/identity"
	"github.com/yangtao121/workos/internal/platform/telemetry"
	"github.com/yangtao121/workos/internal/reliability/application"
)

type DeploymentDriverClient struct {
	client   appv1connect.AppInstallationServiceClient
	surfaces surfacev1connect.SurfaceServiceClient
	deviceID string
}

func NewDeploymentDriverClient(coreURL, runtimeURL, deviceID string) *DeploymentDriverClient {
	return &DeploymentDriverClient{
		client:   appv1connect.NewAppInstallationServiceClient(telemetry.HTTPClient(), coreURL),
		surfaces: surfacev1connect.NewSurfaceServiceClient(telemetry.HTTPClient(), runtimeURL),
		deviceID: deviceID,
	}
}

func (c *DeploymentDriverClient) Transition(ctx context.Context, candidate application.DeploymentCandidate, key string) error {
	request := connect.NewRequest(&appv1.TransitionAppVersionRequest{
		IdempotencyKey: key, ProjectId: candidate.ProjectID,
		InstallationId: candidate.InstallationID, Version: candidate.TargetVersion,
		ExpectedProjectRevision: candidate.ExpectedRevision,
	})
	request.Header().Set(identity.UserHeader, candidate.OwnerUserID)
	request.Header().Set(identity.DeviceHeader, c.deviceID)
	_, err := c.client.TransitionAppVersion(ctx, request)
	return err
}

func (c *DeploymentDriverClient) StartSurface(ctx context.Context, candidate application.DeploymentCandidate, key string) error {
	if candidate.TargetVersion == "" {
		return application.ErrDeploymentCandidateRequired
	}
	surface := connect.NewRequest(&surfacev1.CreateSurfaceRequest{
		IdempotencyKey: key, ProjectId: candidate.ProjectID,
		AppInstanceId: candidate.InstallationID, DeviceClass: surfacev1.DeviceClass_DEVICE_CLASS_DESKTOP,
		Viewport:           &surfacev1.Viewport{Width: 1280, Height: 800, PixelRatio: 1},
		ExpectedAppVersion: candidate.TargetVersion,
	})
	surface.Header().Set(identity.UserHeader, candidate.OwnerUserID)
	surface.Header().Set(identity.DeviceHeader, c.deviceID)
	response, err := c.surfaces.CreateSurface(ctx, surface)
	if err != nil {
		return err
	}
	session := response.Msg.GetSession()
	if session.GetId() == "" || session.GetProjectId() != candidate.ProjectID || session.GetAppInstanceId() != candidate.InstallationID || session.GetBridgeToken() == "" || session.GetExpiresAt() == nil || !time.Now().Before(session.GetExpiresAt().AsTime()) {
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("deployment surface is not active"))
	}
	return nil
}

func (c *DeploymentDriverClient) Rollback(ctx context.Context, candidate application.DeploymentCandidate, key string) error {
	request := connect.NewRequest(&appv1.RollbackAppVersionRequest{
		IdempotencyKey: key, ProjectId: candidate.ProjectID,
		InstallationId:          candidate.InstallationID,
		ExpectedProjectRevision: candidate.ExpectedRevision + 1,
	})
	request.Header().Set(identity.UserHeader, candidate.OwnerUserID)
	request.Header().Set(identity.DeviceHeader, c.deviceID)
	response, err := c.client.RollbackAppVersion(ctx, request)
	if err != nil {
		return err
	}
	installation := response.Msg.GetInstallation()
	if installation.GetId() != candidate.InstallationID || installation.GetProjectId() != candidate.ProjectID || installation.GetVersion() == "" {
		return connect.NewError(connect.CodeInternal, errors.New("rollback installation is invalid"))
	}
	candidate.TargetVersion = installation.GetVersion()
	// Replaying the Core command returns its original pin without rolling
	// back again. Recovery is complete only after Runtime starts that pin.
	return c.StartSurface(ctx, candidate, key+"-surface")
}
