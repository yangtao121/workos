package transport

import (
	"context"
	"errors"

	"connectrpc.com/connect"
	appv1 "github.com/yangtao121/workos/gen/go/workos/app/v1"
	executionv1 "github.com/yangtao121/workos/gen/go/workos/taskexecution/v1"
	"github.com/yangtao121/workos/gen/go/workos/taskexecution/v1/taskexecutionv1connect"
	"github.com/yangtao121/workos/internal/platform/identity"
	"github.com/yangtao121/workos/internal/platform/telemetry"
	"github.com/yangtao121/workos/internal/reliability/application"
)

// CandidateReaderClient reads Core's private completed-task candidate facts.
type CandidateReaderClient struct {
	client   taskexecutionv1connect.RepairCandidateServiceClient
	deviceID string
}

func NewCandidateReaderClient(coreURL, deviceID string) *CandidateReaderClient {
	return &CandidateReaderClient{client: taskexecutionv1connect.NewRepairCandidateServiceClient(telemetry.HTTPClient(), coreURL), deviceID: deviceID}
}

func (c *CandidateReaderClient) Read(ctx context.Context, ownerUserID, taskID string) (application.CandidateFacts, error) {
	request := connect.NewRequest(&executionv1.GetRepairSourceCandidateRequest{TaskId: taskID})
	request.Header().Set(identity.UserHeader, ownerUserID)
	request.Header().Set(identity.DeviceHeader, c.deviceID)
	response, err := c.client.GetRepairSourceCandidate(ctx, request)
	if err != nil {
		return application.CandidateFacts{}, err
	}
	message := response.Msg
	input := message.GetInput()
	target := input.GetTarget()
	if message.GetProjectId() == "" || message.GetIncidentId() == "" || target.GetAppInstanceId() == "" {
		return application.CandidateFacts{}, connect.NewError(connect.CodeInternal, errors.New("repair candidate facts are incomplete"))
	}
	files := make([]application.CandidateFile, 0, len(message.GetCandidateSource().GetFiles()))
	for _, file := range message.GetCandidateSource().GetFiles() {
		files = append(files, application.CandidateFile{Path: file.GetPath(), Content: file.GetContent(), Executable: file.GetExecutable()})
	}
	return application.CandidateFacts{
		TaskID: taskID, IncidentID: message.GetIncidentId(), ProjectID: message.GetProjectId(),
		OwnerUserID: ownerUserID, AppInstanceID: target.GetAppInstanceId(),
		AppID: target.GetAppId(), BaseVersion: target.GetVersion(),
		ManifestDigest: target.GetManifestDigest(),
		SourceBundleID: message.GetCandidateSource().GetId(),
		SourceDigest:   message.GetCandidateSource().GetDigest(),
		BaseImage:      input.GetBaseImage(), BuildCommand: input.GetBuildCommand(), TestCommand: input.GetTestCommand(),
		Files: files,
	}, nil
}

// BuildTestServiceClient drives Runtime's private Build/Test service.
type BuildTestServiceClient struct {
	client taskexecutionv1connect.BuildTestServiceClient
}

func NewBuildTestServiceClient(runtimeURL string) *BuildTestServiceClient {
	return &BuildTestServiceClient{client: taskexecutionv1connect.NewBuildTestServiceClient(telemetry.HTTPClient(), runtimeURL)}
}

func (c *BuildTestServiceClient) Submit(ctx context.Context, facts application.CandidateFacts) (string, bool, error) {
	files := make([]*appv1.AppSourceFile, 0, len(facts.Files))
	for _, file := range facts.Files {
		files = append(files, &appv1.AppSourceFile{Path: file.Path, Content: file.Content, Executable: file.Executable})
	}
	job := &executionv1.BuildTestJob{
		TaskId: facts.TaskID, IncidentId: facts.IncidentID, OwnerUserId: facts.OwnerUserID,
		ProjectId: facts.ProjectID, InstallationId: facts.AppInstanceID,
		Input: &executionv1.RepairBuildInput{
			TaskId: facts.TaskID, BaseImage: facts.BaseImage,
			BuildCommand: facts.BuildCommand, TestCommand: facts.TestCommand,
		},
		CandidateFiles: files,
	}
	inputFacts := &executionv1.BuildTestInputFacts{
		SourceBundleId: facts.SourceBundleID, SourceDigest: facts.SourceDigest,
		ManifestDigest: facts.ManifestDigest, BaseImage: facts.BaseImage,
	}
	response, err := c.client.SubmitBuildTest(ctx, connect.NewRequest(&executionv1.SubmitBuildTestRequest{Job: job, InputFacts: inputFacts}))
	if err != nil {
		return "", false, err
	}
	return response.Msg.GetJobId(), response.Msg.GetCreated(), nil
}

func (c *BuildTestServiceClient) Get(ctx context.Context, taskID string) (application.BuildTestVerdict, error) {
	response, err := c.client.GetBuildTest(ctx, connect.NewRequest(&executionv1.GetBuildTestRequest{TaskId: taskID}))
	if err != nil {
		return application.BuildTestVerdict{}, err
	}
	return application.BuildTestVerdict{
		JobID: response.Msg.GetJobId(), State: response.Msg.GetState(), Stage: response.Msg.GetStage(),
		FailureReason: response.Msg.GetFailureReason(), SourceDigest: response.Msg.GetSourceDigest(),
	}, nil
}

func (c *BuildTestServiceClient) Cancel(ctx context.Context, taskID string) error {
	_, err := c.client.CancelBuildTest(ctx, connect.NewRequest(&executionv1.CancelBuildTestRequest{TaskId: taskID}))
	return err
}

// RepairVersionClient drives Core's private staged candidate lifecycle.
type RepairVersionClient struct {
	client   taskexecutionv1connect.RepairVersionServiceClient
	deviceID string
}

func NewRepairVersionClient(coreURL, deviceID string) *RepairVersionClient {
	return &RepairVersionClient{client: taskexecutionv1connect.NewRepairVersionServiceClient(telemetry.HTTPClient(), coreURL), deviceID: deviceID}
}

func (c *RepairVersionClient) Register(ctx context.Context, ownerUserID, taskID, projectID, installationID, buildJobID, sourceDigest string) (application.RegisteredVersion, error) {
	request := connect.NewRequest(&executionv1.RegisterRepairCandidateVersionRequest{
		TaskId: taskID, ProjectId: projectID, InstallationId: installationID,
		BuildJobId: buildJobID, SourceDigest: sourceDigest,
	})
	request.Header().Set(identity.UserHeader, ownerUserID)
	request.Header().Set(identity.DeviceHeader, c.deviceID)
	response, err := c.client.RegisterRepairCandidateVersion(ctx, request)
	if err != nil {
		return application.RegisteredVersion{}, err
	}
	return application.RegisteredVersion{
		Version: response.Msg.GetVersion(), ManifestDigest: response.Msg.GetManifestDigest(),
		Created: response.Msg.GetCreated(), BaseVersion: response.Msg.GetBaseVersion(),
		ProjectRevision: response.Msg.GetProjectRevision(),
	}, nil
}

func (c *RepairVersionClient) Publish(ctx context.Context, ownerUserID, taskID, projectID, installationID, version, manifestDigest string) (bool, error) {
	request := connect.NewRequest(&executionv1.PublishRepairCandidateVersionRequest{
		TaskId: taskID, ProjectId: projectID, InstallationId: installationID,
		Version: version, ManifestDigest: manifestDigest,
	})
	request.Header().Set(identity.UserHeader, ownerUserID)
	request.Header().Set(identity.DeviceHeader, c.deviceID)
	response, err := c.client.PublishRepairCandidateVersion(ctx, request)
	if err != nil {
		return false, err
	}
	return response.Msg.GetPublished(), nil
}

var (
	_ application.CandidateReader  = (*CandidateReaderClient)(nil)
	_ application.BuildTestGateway = (*BuildTestServiceClient)(nil)
	_ application.VersionRegistry  = (*RepairVersionClient)(nil)
)
