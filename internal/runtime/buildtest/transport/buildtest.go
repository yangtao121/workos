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

type BuildTestHandler struct{ service *application.Service }

// NewBuildTestHandler returns the mux path and handler. The read budget
// covers the ADR-0024 bounded candidate payload with headroom.
func NewBuildTestHandler(service *application.Service) (string, http.Handler) {
	return taskexecutionv1connect.NewBuildTestServiceHandler(&BuildTestHandler{service: service}, connect.WithReadMaxBytes(2<<20))
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
			BaseImage: job.GetInput().GetBaseImage(),
			BuildCmd:  job.GetInput().GetBuildCommand(),
			TestCmd:   job.GetInput().GetTestCommand(),
			Files:     files,
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
	return connect.NewResponse(response), nil
}

func (h *BuildTestHandler) CancelBuildTest(ctx context.Context, req *connect.Request[executionv1.CancelBuildTestRequest]) (*connect.Response[executionv1.CancelBuildTestResponse], error) {
	state, err := h.service.Cancel(ctx, req.Msg.GetTaskId())
	if err != nil {
		return nil, buildTestError(err)
	}
	return connect.NewResponse(&executionv1.CancelBuildTestResponse{State: string(state)}), nil
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
