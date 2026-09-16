// Package transport serves Runtime's private Build/Test RPCs (ADR-0026).
// The listener is Reliability-facing only; Gateway never routes it.
package transport

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"connectrpc.com/connect"

	executionv1 "github.com/yangtao121/workos/gen/go/workos/taskexecution/v1"
	"github.com/yangtao121/workos/gen/go/workos/taskexecution/v1/taskexecutionv1connect"
	artifactapp "github.com/yangtao121/workos/internal/runtime/artifactstore/application"
	artifactdomain "github.com/yangtao121/workos/internal/runtime/artifactstore/domain"
	"github.com/yangtao121/workos/internal/runtime/buildtest/application"
	"github.com/yangtao121/workos/internal/runtime/buildtest/domain"
)

func unmarshalFacts(encoded []byte, facts *executionv1.BuildEngineFacts) error {
	var raw struct {
		Engine         string   `json:"engine"`
		NetworkIsolate bool     `json:"network_isolated"`
		ImagePinned    bool     `json:"image_pinned"`
		Enforced       []string `json:"enforced_limits"`
	}
	if err := json.Unmarshal(encoded, &raw); err != nil {
		return err
	}
	facts.Engine = raw.Engine
	facts.NetworkIsolated = raw.NetworkIsolate
	facts.ImagePinned = raw.ImagePinned
	facts.EnforcedLimits = raw.Enforced
	return nil
}

// ArtifactFacts is the artifact repository query surface the handler needs
// for Core's independent verification (ADR-0033). It is satisfied by the
// artifactstore application service.
type ArtifactFacts interface {
	Facts(ctx context.Context, query artifactapp.FactsQuery) (artifactdomain.Artifact, error)
}

type BuildTestHandler struct {
	service   *application.Service
	artifacts ArtifactFacts
}

// NewBuildTestHandler returns the mux path and handler. The read budget
// covers the ADR-0024 bounded candidate payload with headroom. artifacts may
// be nil when the artifact repository is not configured: bundle queries then
// report Unimplemented instead of fabricating facts.
func NewBuildTestHandler(service *application.Service, artifacts ArtifactFacts) (string, http.Handler) {
	return taskexecutionv1connect.NewBuildTestServiceHandler(&BuildTestHandler{service: service, artifacts: artifacts}, connect.WithReadMaxBytes(2<<20))
}

func (h *BuildTestHandler) SubmitBuildTest(ctx context.Context, req *connect.Request[executionv1.SubmitBuildTestRequest]) (*connect.Response[executionv1.SubmitBuildTestResponse], error) {
	if err := h.service.Available(ctx); err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("build engine is unavailable"))
	}
	job := req.Msg.GetJob()
	facts := req.Msg.GetInputFacts()
	files := make([]domain.File, 0, len(job.GetCandidateFiles()))
	for _, file := range job.GetCandidateFiles() {
		files = append(files, domain.File{Path: file.GetPath(), Content: file.GetContent(), Executable: file.GetExecutable()})
	}
	domainJob := domain.Job{
		TaskID: job.GetTaskId(), IncidentID: job.GetIncidentId(), OwnerUserID: job.GetOwnerUserId(),
		ProjectID: job.GetProjectId(), InstallationID: job.GetInstallationId(),
		SourceBundleID: facts.GetSourceBundleId(), SourceDigest: facts.GetSourceDigest(),
		ManifestDigest: facts.GetManifestDigest(), BaseImage: facts.GetBaseImage(),
		Payload: domain.Payload{
			AppID:           job.GetInput().GetTarget().GetAppId(),
			BaseImage:       job.GetInput().GetBaseImage(),
			BuildCmd:        job.GetInput().GetBuildCommand(),
			TestCmd:         job.GetInput().GetTestCommand(),
			Files:           files,
			OutputDirectory: job.GetInput().GetOutputDirectory(),
			RuntimeCommand:  job.GetInput().GetRuntimeCommand(),
		},
	}
	if domainJob.BaseImage == "" {
		domainJob.BaseImage = facts.GetBaseImage()
	}
	_, inputDigest, err := domain.CanonicalPayload(domainJob.Payload)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("build payload is invalid"))
	}
	domainJob.InputDigest = inputDigest
	stored, created, err := h.service.Submit(ctx, domainJob)
	if err != nil {
		return nil, buildTestError(err)
	}
	return connect.NewResponse(&executionv1.SubmitBuildTestResponse{JobId: stored.ID, Created: created}), nil
}

func (h *BuildTestHandler) GetBuildTest(ctx context.Context, req *connect.Request[executionv1.GetBuildTestRequest]) (*connect.Response[executionv1.GetBuildTestResponse], error) {
	job, err := h.service.Get(ctx, req.Msg.GetTaskId())
	if err != nil {
		return nil, buildTestError(err)
	}
	response := &executionv1.GetBuildTestResponse{
		JobId: job.ID, TaskId: job.TaskID, State: string(job.State), Stage: string(job.Stage),
		SourceDigest: job.SourceDigest, Attempts: job.Attempts,
		FailureReason: failureReasonString(job.FailureReason),
	}
	if job.BuildExitCode != nil {
		response.BuildExitCode = *job.BuildExitCode
	}
	if job.TestExitCode != nil {
		response.TestExitCode = *job.TestExitCode
	}
	if len(job.EngineFacts) > 0 {
		facts := &executionv1.BuildEngineFacts{}
		if unmarshalFacts(job.EngineFacts, facts) == nil && facts.GetEngine() != "" {
			response.Engine = facts
		}
	}
	// A success verdict carries its frozen bundle only when the repository
	// confirms a ready artifact right now; no facts, no bundle claim.
	if job.State == domain.StateSucceeded && job.ArtifactID != "" && h.artifacts != nil {
		if artifact, err := h.artifacts.Facts(ctx, artifactapp.FactsQuery{ArtifactID: job.ArtifactID}); err == nil && artifact.State == artifactdomain.StateReady {
			response.Artifact = &executionv1.BuildArtifactFacts{
				ArtifactId: artifact.ID, ArtifactDigest: artifact.Digest, Format: artifact.Format,
				Origin: artifact.Origin, SizeBytes: artifact.SizeBytes, FileCount: artifact.FileCount,
				JobId: artifact.JobID, TaskId: artifact.TaskID, IncidentId: artifact.IncidentID,
				OwnerUserId: artifact.OwnerUserID, ProjectId: artifact.ProjectID,
				InstallationId: artifact.InstallationID, SourceBundleId: artifact.SourceBundleID,
				SourceDigest: artifact.SourceDigest, ManifestDigest: artifact.ManifestDigest,
				BaseImage: artifact.BaseImage, BuildCommand: artifact.BuildCommand,
				TestCommand: artifact.TestCommand, OutputDirectory: artifact.OutputDirectory,
				State: artifact.State, AppId: artifact.AppID,
			}
		}
	}
	return connect.NewResponse(response), nil
}

func (h *BuildTestHandler) CancelBuildTest(ctx context.Context, req *connect.Request[executionv1.CancelBuildTestRequest]) (*connect.Response[executionv1.CancelBuildTestResponse], error) {
	state, err := h.service.Cancel(ctx, req.Msg.GetTaskId())
	if err != nil {
		return nil, buildTestError(err)
	}
	return connect.NewResponse(&executionv1.CancelBuildTestResponse{State: string(state)}), nil
}

// GetBuildArtifact serves Core's independent verification query (ADR-0033
// section 4): authoritative job/bundle metadata, never bundle bytes.
func (h *BuildTestHandler) GetBuildArtifact(ctx context.Context, req *connect.Request[executionv1.GetBuildArtifactRequest]) (*connect.Response[executionv1.GetBuildArtifactResponse], error) {
	if h.artifacts == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("artifact repository is not wired on this runtime host"))
	}
	artifact, err := h.artifacts.Facts(ctx, artifactapp.FactsQuery{
		TaskID:     req.Msg.GetTaskId(),
		ArtifactID: req.Msg.GetArtifactId(),
	})
	if err != nil {
		return nil, buildTestError(artifactError(err))
	}
	if artifact.State != artifactdomain.StateReady {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("release bundle is not ready"))
	}
	return connect.NewResponse(&executionv1.GetBuildArtifactResponse{
		Artifact: &executionv1.BuildArtifactFacts{
			ArtifactId: artifact.ID, ArtifactDigest: artifact.Digest, Format: artifact.Format,
			Origin: artifact.Origin, SizeBytes: artifact.SizeBytes, FileCount: artifact.FileCount,
			JobId: artifact.JobID, TaskId: artifact.TaskID, IncidentId: artifact.IncidentID,
			OwnerUserId: artifact.OwnerUserID, ProjectId: artifact.ProjectID,
			InstallationId: artifact.InstallationID, SourceBundleId: artifact.SourceBundleID,
			SourceDigest: artifact.SourceDigest, ManifestDigest: artifact.ManifestDigest,
			BaseImage: artifact.BaseImage, BuildCommand: artifact.BuildCommand,
			TestCommand: artifact.TestCommand, OutputDirectory: artifact.OutputDirectory,
			State: artifact.State, AppId: artifact.AppID,
		},
	}), nil
}

func artifactError(err error) error {
	switch {
	case errors.Is(err, artifactdomain.ErrNotFound):
		return domain.ErrNotFound
	case errors.Is(err, artifactdomain.ErrInvalidRequest):
		return domain.ErrInvalid
	case errors.Is(err, artifactdomain.ErrStoreUnavailable):
		return domain.ErrStoreUnavailable
	default:
		return domain.ErrStoreUnavailable
	}
}

func failureReasonString(reason domain.FailureReason) string {
	if reason == domain.FailureNone {
		return "none"
	}
	return string(reason)
}

func buildTestError(err error) error {
	code := connect.CodeInternal
	switch {
	case errors.Is(err, domain.ErrInvalid):
		code = connect.CodeInvalidArgument
	case errors.Is(err, domain.ErrNotFound):
		code = connect.CodeNotFound
	case errors.Is(err, domain.ErrIdempotencyDrift):
		code = connect.CodeAborted
	case errors.Is(err, domain.ErrStoreUnavailable):
		code = connect.CodeUnavailable
	}
	return connect.NewError(code, errors.New("build test request failed"))
}
