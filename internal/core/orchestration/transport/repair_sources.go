package transport

import (
	"context"
	"errors"
	"net/http"

	"connectrpc.com/connect"
	appv1 "github.com/yangtao121/workos/gen/go/workos/app/v1"
	executionv1 "github.com/yangtao121/workos/gen/go/workos/taskexecution/v1"
	"github.com/yangtao121/workos/gen/go/workos/taskexecution/v1/taskexecutionv1connect"
	agentdomain "github.com/yangtao121/workos/internal/core/agent/domain"
	agentports "github.com/yangtao121/workos/internal/core/agent/ports"
	registryapp "github.com/yangtao121/workos/internal/core/appregistry/application"
	registrydomain "github.com/yangtao121/workos/internal/core/appregistry/domain"
	registryports "github.com/yangtao121/workos/internal/core/appregistry/ports"
	"github.com/yangtao121/workos/internal/core/orchestration"
	"github.com/yangtao121/workos/internal/platform/dbtransient"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type RepairSourceHandler struct{ service *orchestration.RepairSources }

func NewRepairSourceHandler(service *orchestration.RepairSources) (string, http.Handler) {
	return taskexecutionv1connect.NewRepairExecutionServiceHandler(&RepairSourceHandler{service: service}, connect.WithReadMaxBytes(1024*1024))
}
func (h *RepairSourceHandler) ResolveRepairBuildInput(ctx context.Context, req *connect.Request[executionv1.ResolveRepairBuildInputRequest]) (*connect.Response[executionv1.ResolveRepairBuildInputResponse], error) {
	input, err := h.service.Resolve(ctx, req.Msg.GetLeaseId(), req.Msg.GetWorkerId())
	if err != nil {
		return nil, repairSourceError(err)
	}
	source := input.Build.Source
	bundle := &appv1.AppSourceBundle{Id: source.ID, Digest: source.Digest, TotalSizeBytes: source.TotalSizeBytes, CreatedAt: timestamppb.New(source.CreatedAt)}
	for _, file := range source.Files {
		bundle.Files = append(bundle.Files, &appv1.AppSourceFile{Path: file.Path, Content: file.Content, Executable: file.Executable})
	}
	return connect.NewResponse(&executionv1.ResolveRepairBuildInputResponse{Input: &executionv1.RepairBuildInput{TaskId: input.TaskID, Target: input.Target, Source: bundle, BaseImage: input.Build.Recipe.BaseImage, BuildCommand: input.Build.Recipe.BuildCommand, TestCommand: input.Build.Recipe.TestCommand}}), nil
}
func (h *RepairSourceHandler) SubmitRepairSourceCandidate(ctx context.Context, req *connect.Request[executionv1.SubmitRepairSourceCandidateRequest]) (*connect.Response[executionv1.SubmitRepairSourceCandidateResponse], error) {
	files := make([]registrydomain.SourceFile, 0, len(req.Msg.GetFiles()))
	for _, file := range req.Msg.GetFiles() {
		files = append(files, registrydomain.SourceFile{Path: file.GetPath(), Content: file.GetContent(), Executable: file.GetExecutable()})
	}
	taskID, source, err := h.service.Submit(ctx, req.Msg.GetLeaseId(), req.Msg.GetWorkerId(), files)
	if err != nil {
		return nil, repairSourceError(err)
	}
	return connect.NewResponse(&executionv1.SubmitRepairSourceCandidateResponse{Candidate: &executionv1.RepairSourceCandidate{TaskId: taskID, SourceBundleId: source.ID, SourceDigest: source.Digest, CreatedAt: timestamppb.New(source.CreatedAt)}}), nil
}
func repairSourceError(err error) error {
	code := connect.CodeInternal
	switch {
	case errors.Is(err, agentdomain.ErrInvalid), errors.Is(err, registrydomain.ErrInvalid):
		code = connect.CodeInvalidArgument
	case errors.Is(err, agentdomain.ErrLeaseLost), errors.Is(err, agentdomain.ErrTerminal), errors.Is(err, registryapp.ErrBuildUnavailable):
		code = connect.CodeFailedPrecondition
	case errors.Is(err, registrydomain.ErrNotFound):
		code = connect.CodeNotFound
	case errors.Is(err, registrydomain.ErrIdempotencyConflict):
		code = connect.CodeAborted
	case errors.Is(err, agentports.ErrStoreUnavailable), errors.Is(err, registryports.ErrStoreUnavailable), dbtransient.IsTransient(err):
		code = connect.CodeUnavailable
	}
	return connect.NewError(code, errors.New("repair source request failed"))
}
