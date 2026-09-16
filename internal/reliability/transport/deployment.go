package transport

import (
	"context"
	"errors"
	"fmt"

	"time"

	"connectrpc.com/connect"
	appv1 "github.com/yangtao121/workos/gen/go/workos/app/v1"
	appv1connect "github.com/yangtao121/workos/gen/go/workos/app/v1/appv1connect"
	surfacev1 "github.com/yangtao121/workos/gen/go/workos/surface/v1"
	surfacev1connect "github.com/yangtao121/workos/gen/go/workos/surface/v1/surfacev1connect"
	executionv1 "github.com/yangtao121/workos/gen/go/workos/taskexecution/v1"
	"github.com/yangtao121/workos/gen/go/workos/taskexecution/v1/taskexecutionv1connect"
	"github.com/yangtao121/workos/internal/platform/identity"
	"github.com/yangtao121/workos/internal/platform/telemetry"
	"github.com/yangtao121/workos/internal/reliability/application"
	"github.com/yangtao121/workos/internal/reliability/ports"
)

type DeploymentDriverClient struct {
	client   appv1connect.AppInstallationServiceClient
	surfaces surfacev1connect.SurfaceServiceClient
	versions taskexecutionv1connect.RepairVersionServiceClient
	resolver surfacev1connect.SurfaceLaunchResolverServiceClient
	deviceID string
	observer observationLister
}

type observationLister interface {
	ListObservations(context.Context) ([]ports.Observation, error)
}

func NewDeploymentDriverClient(coreURL, runtimeURL, deviceID string) *DeploymentDriverClient {
	return &DeploymentDriverClient{
		client:   appv1connect.NewAppInstallationServiceClient(telemetry.HTTPClient(), coreURL),
		surfaces: surfacev1connect.NewSurfaceServiceClient(telemetry.HTTPClient(), runtimeURL),
		versions: taskexecutionv1connect.NewRepairVersionServiceClient(telemetry.HTTPClient(), coreURL),
		deviceID: deviceID,
		resolver: surfacev1connect.NewSurfaceLaunchResolverServiceClient(telemetry.HTTPClient(), coreURL),
	}
}

func (c *DeploymentDriverClient) WithObserver(observer observationLister) *DeploymentDriverClient {
	c.observer = observer
	return c
}

// Transition pins the candidate version. Staged repair candidates (ADR-0026)
// go through Core's private staged canary transition; ordinary candidates
// keep the public exact-version transition.
func (c *DeploymentDriverClient) Transition(ctx context.Context, candidate application.DeploymentCandidate, key string) error {
	if candidate.Staged() {
		request := connect.NewRequest(&executionv1.TransitionCandidateVersionRequest{
			IdempotencyKey: key, ProjectId: candidate.ProjectID,
			InstallationId: candidate.InstallationID, Version: candidate.TargetVersion,
			ManifestDigest: candidate.ManifestDigest, ExpectedProjectRevision: candidate.ExpectedRevision,
		})
		request.Header().Set(identity.UserHeader, candidate.OwnerUserID)
		request.Header().Set(identity.DeviceHeader, c.deviceID)
		response, err := c.versions.TransitionCandidateVersion(ctx, request)
		if err != nil {
			return fmt.Errorf("transition staged candidate: %w", err)
		}
		if response.Msg.GetVersion() != candidate.TargetVersion {
			return connect.NewError(connect.CodeInternal, errors.New("staged transition returned a different version"))
		}
		return nil
	}
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

// Publish flips the staged candidate to a published version (ADR-0026).
func (c *DeploymentDriverClient) Publish(ctx context.Context, candidate application.DeploymentCandidate) error {
	if !candidate.Staged() {
		// Ordinary candidates were published versions from the start.
		return nil
	}
	if err := c.Verify(ctx, &candidate); err != nil {
		return err
	}
	request := connect.NewRequest(&executionv1.PublishRepairCandidateVersionRequest{
		TaskId: candidate.TaskID, ProjectId: candidate.ProjectID,
		InstallationId: candidate.InstallationID, Version: candidate.TargetVersion,
		ManifestDigest: candidate.ManifestDigest,
	})
	request.Header().Set(identity.UserHeader, candidate.OwnerUserID)
	request.Header().Set(identity.DeviceHeader, c.deviceID)
	_, err := c.versions.PublishRepairCandidateVersion(ctx, request)
	return err
}

func (c *DeploymentDriverClient) StartSurface(ctx context.Context, candidate application.DeploymentCandidate, key string) error {
	if candidate.TargetVersion == "" {
		return application.ErrDeploymentCandidateRequired
	}
	// Resolve the live Core pin before stopping any existing workload.
	// A replay after a user's version change must have no stop side effects.
	if c.resolver != nil {
		current, err := c.resolveCurrent(ctx, candidate)
		if err != nil {
			return err
		}
		if current.GetVersion() != candidate.TargetVersion || (candidate.ManifestDigest != "" && current.GetManifestDigest() != candidate.ManifestDigest) ||
			(candidate.ArtifactDigest != "" && current.GetArtifact().GetArtifactDigest() != candidate.ArtifactDigest) {
			return application.ErrDeploymentSuperseded
		}
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
	if candidate.ArtifactDigest == "" {
		return nil
	}
	if c.observer == nil {
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("bundle-profile launch facts are unavailable"))
	}
	observations, err := c.observer.ListObservations(ctx)
	if err != nil {
		return err
	}
	for _, observation := range observations {
		if observation.AppInstanceID != candidate.InstallationID || observation.OwnerUserID != candidate.OwnerUserID {
			continue
		}
		if observation.State != ports.StateRunning || observation.HealthVerdict != "ok" {
			continue
		}
		if candidate.ManifestDigest != "" && observation.ManifestDigest != candidate.ManifestDigest {
			continue
		}
		if observation.ArtifactDigest != candidate.ArtifactDigest || !observation.IdentityVerified {
			continue
		}
		return nil
	}
	return connect.NewError(connect.CodeFailedPrecondition, errors.New("running workload facts do not match the candidate artifact"))
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
	candidate.ManifestDigest = installation.GetManifestDigest()
	// Resolve the restored immutable version, never reuse the failed candidate's bundle.
	current, err := c.resolveCurrent(ctx, candidate)
	if err != nil {
		return err
	}
	if current.GetVersion() != candidate.TargetVersion || (candidate.ManifestDigest != "" && current.GetManifestDigest() != candidate.ManifestDigest) {
		return application.ErrDeploymentSuperseded
	}
	candidate.ArtifactID = current.GetArtifact().GetArtifactId()
	candidate.ArtifactDigest = current.GetArtifact().GetArtifactDigest()
	// Replaying the Core command returns its original pin without rolling
	// back again. Recovery is complete only after Runtime starts that pin.
	return c.StartSurface(ctx, candidate, key+"-surface")
}

func (c *DeploymentDriverClient) resolveCurrent(ctx context.Context, candidate application.DeploymentCandidate) (*surfacev1.ContainerLaunchDescriptor, error) {
	if c.resolver == nil {
		return nil, errors.New("launch resolver unavailable")
	}
	req := connect.NewRequest(&surfacev1.ResolveSurfaceLaunchRequest{ProjectId: candidate.ProjectID, AppInstanceId: candidate.InstallationID})
	req.Header().Set(identity.UserHeader, candidate.OwnerUserID)
	req.Header().Set(identity.DeviceHeader, c.deviceID)
	response, err := c.resolver.ResolveSurfaceLaunch(ctx, req)
	if err != nil {
		return nil, err
	}
	descriptor := response.Msg.GetWebServiceContainer()
	if descriptor == nil {
		return nil, errors.New("container launch descriptor unavailable")
	}
	return descriptor, nil
}

// Verify requires live pin, identity and health evidence throughout canary.
func (c *DeploymentDriverClient) Verify(ctx context.Context, candidate *application.DeploymentCandidate) error {
	current, err := c.resolveCurrent(ctx, *candidate)
	if err != nil {
		return err
	}
	if current.GetVersion() != candidate.TargetVersion || (candidate.ManifestDigest != "" && current.GetManifestDigest() != candidate.ManifestDigest) {
		return application.ErrDeploymentSuperseded
	}
	if candidate.ArtifactDigest == "" {
		return nil
	}
	if current.GetArtifact().GetArtifactDigest() != candidate.ArtifactDigest || c.observer == nil {
		return errors.New("candidate artifact evidence unavailable")
	}
	observations, err := c.observer.ListObservations(ctx)
	if err != nil {
		return err
	}
	for _, observation := range observations {
		if observation.OwnerUserID == candidate.OwnerUserID && observation.AppInstanceID == candidate.InstallationID &&
			observation.State == ports.StateRunning && observation.HealthVerdict == "ok" &&
			observation.ManifestDigest == candidate.ManifestDigest && observation.ArtifactDigest == candidate.ArtifactDigest && observation.IdentityVerified {
			if observation.WorkloadID == "" || observation.Generation <= 0 {
				continue
			}
			if candidate.WorkloadID != "" && (candidate.WorkloadID != observation.WorkloadID || candidate.WorkloadGeneration != observation.Generation) {
				return errors.New("candidate workload generation changed during canary")
			}
			candidate.WorkloadID = observation.WorkloadID
			candidate.WorkloadGeneration = observation.Generation
			return nil
		}
	}
	return errors.New("candidate running identity or health is unavailable")
}
