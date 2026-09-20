package orchestration

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"

	executionv1 "github.com/yangtao121/workos/gen/go/workos/taskexecution/v1"
	"github.com/yangtao121/workos/gen/go/workos/taskexecution/v1/taskexecutionv1connect"
	registryapp "github.com/yangtao121/workos/internal/core/appregistry/application"
	"github.com/yangtao121/workos/internal/platform/telemetry"
)

// RuntimeArtifactClient is Core's private Runtime GetBuildArtifact caller.
type RuntimeArtifactClient struct {
	client taskexecutionv1connect.BuildTestServiceClient
}

func NewRuntimeArtifactClient(runtimeURL string) *RuntimeArtifactClient {
	return &RuntimeArtifactClient{
		client: taskexecutionv1connect.NewBuildTestServiceClient(telemetry.HTTPClient(), runtimeURL),
	}
}

func (c *RuntimeArtifactClient) GetByTask(ctx context.Context, taskID string) (VerifiedReleaseBundle, error) {
	return c.get(ctx, &executionv1.GetBuildArtifactRequest{TaskId: taskID})
}

func (c *RuntimeArtifactClient) GetByID(ctx context.Context, artifactID string) (VerifiedReleaseBundle, error) {
	return c.get(ctx, &executionv1.GetBuildArtifactRequest{ArtifactId: artifactID})
}

func (c *RuntimeArtifactClient) get(ctx context.Context, request *executionv1.GetBuildArtifactRequest) (VerifiedReleaseBundle, error) {
	if c == nil || c.client == nil {
		return VerifiedReleaseBundle{}, ErrRuntimeArtifactUnavailable
	}
	response, err := c.client.GetBuildArtifact(ctx, connect.NewRequest(request))
	if err != nil {
		if connect.CodeOf(err) == connect.CodeUnavailable || connect.CodeOf(err) == connect.CodeUnimplemented {
			return VerifiedReleaseBundle{}, fmt.Errorf("%w: %v", ErrRuntimeArtifactUnavailable, err)
		}
		if connect.CodeOf(err) == connect.CodeNotFound {
			return VerifiedReleaseBundle{}, errors.New("release bundle is not found")
		}
		return VerifiedReleaseBundle{}, err
	}
	artifact := response.Msg.GetArtifact()
	if artifact == nil {
		return VerifiedReleaseBundle{}, errors.New("release bundle facts are empty")
	}
	return VerifiedReleaseBundle{
		ID: artifact.GetArtifactId(), Digest: artifact.GetArtifactDigest(), Format: artifact.GetFormat(),
		Origin: artifact.GetOrigin(), State: artifact.GetState(), OwnerUserID: artifact.GetOwnerUserId(),
		AppID: artifact.GetAppId(), TaskID: artifact.GetTaskId(), JobID: artifact.GetJobId(),
		IncidentID: artifact.GetIncidentId(), ProjectID: artifact.GetProjectId(),
		InstallationID: artifact.GetInstallationId(), SourceBundleID: artifact.GetSourceBundleId(),
		SourceDigest: artifact.GetSourceDigest(), ManifestDigest: artifact.GetManifestDigest(),
		BaseImage: artifact.GetBaseImage(), OutputDirectory: artifact.GetOutputDirectory(),
		BuildCommand: artifact.GetBuildCommand(), TestCommand: artifact.GetTestCommand(),
	}, nil
}

func (c *RuntimeArtifactClient) VerifyReady(ctx context.Context, ownerUserID, artifactID, digest, appID string) error {
	bundle, err := c.GetByID(ctx, artifactID)
	if err != nil {
		return err
	}
	if bundle.OwnerUserID != ownerUserID || bundle.ID != artifactID || bundle.Digest != digest ||
		bundle.State != "ready" || bundle.Format != "app-bundle.v1" {
		return registryapp.ErrArtifactDenied
	}
	if bundle.AppID != appID {
		return registryapp.ErrArtifactDenied
	}
	return nil
}

var (
	_ ReleaseBundleVerifier              = (*RuntimeArtifactClient)(nil)
	_ registryapp.ReleaseBundleDirectory = (*RuntimeArtifactClient)(nil)
)
