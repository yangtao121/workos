package transport

import (
	"context"
	"errors"
	"net/http"

	"connectrpc.com/connect"
	executionv1 "github.com/yangtao121/workos/gen/go/workos/taskexecution/v1"
	"github.com/yangtao121/workos/gen/go/workos/taskexecution/v1/taskexecutionv1connect"
	agentdomain "github.com/yangtao121/workos/internal/core/agent/domain"
	agentports "github.com/yangtao121/workos/internal/core/agent/ports"
	registryapp "github.com/yangtao121/workos/internal/core/appregistry/application"
	registrydomain "github.com/yangtao121/workos/internal/core/appregistry/domain"
	registryports "github.com/yangtao121/workos/internal/core/appregistry/ports"
	"github.com/yangtao121/workos/internal/core/orchestration"
	projectapp "github.com/yangtao121/workos/internal/core/project/application"
	projectdomain "github.com/yangtao121/workos/internal/core/project/domain"
	projectports "github.com/yangtao121/workos/internal/core/project/ports"
	"github.com/yangtao121/workos/internal/platform/dbtransient"
	"github.com/yangtao121/workos/internal/platform/identity"
)

type RepairVersionHandler struct{ service *orchestration.RepairVersions }

// NewRepairVersionHandler serves Core's private Reliability-facing staged
// candidate lifecycle (ADR-0026). Gateway never routes this listener.
func NewRepairVersionHandler(service *orchestration.RepairVersions) (string, http.Handler) {
	return taskexecutionv1connect.NewRepairVersionServiceHandler(&RepairVersionHandler{service: service}, connect.WithReadMaxBytes(4096))
}

func (h *RepairVersionHandler) RegisterRepairCandidateVersion(ctx context.Context, req *connect.Request[executionv1.RegisterRepairCandidateVersionRequest]) (*connect.Response[executionv1.RegisterRepairCandidateVersionResponse], error) {
	owner, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	result, err := h.service.Register(ctx, owner.UserID, req.Msg.GetTaskId(), req.Msg.GetProjectId(), req.Msg.GetInstallationId(), req.Msg.GetBuildJobId(), req.Msg.GetSourceDigest())
	if err != nil {
		return nil, repairVersionError(err)
	}
	return connect.NewResponse(&executionv1.RegisterRepairCandidateVersionResponse{Version: result.Version, ManifestDigest: result.ManifestDigest, Created: result.Created, BaseVersion: result.BaseVersion, ProjectRevision: result.ProjectRevision}), nil
}

func (h *RepairVersionHandler) PublishRepairCandidateVersion(ctx context.Context, req *connect.Request[executionv1.PublishRepairCandidateVersionRequest]) (*connect.Response[executionv1.PublishRepairCandidateVersionResponse], error) {
	owner, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	published, err := h.service.Publish(ctx, owner.UserID, req.Msg.GetTaskId(), req.Msg.GetProjectId(), req.Msg.GetInstallationId(), req.Msg.GetVersion(), req.Msg.GetManifestDigest())
	if err != nil {
		return nil, repairVersionError(err)
	}
	return connect.NewResponse(&executionv1.PublishRepairCandidateVersionResponse{Published: published}), nil
}

func (h *RepairVersionHandler) TransitionCandidateVersion(ctx context.Context, req *connect.Request[executionv1.TransitionCandidateVersionRequest]) (*connect.Response[executionv1.TransitionCandidateVersionResponse], error) {
	owner, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	version, revision, err := h.service.TransitionCandidate(ctx, owner.UserID, req.Msg.GetIdempotencyKey(), req.Msg.GetProjectId(), req.Msg.GetInstallationId(), req.Msg.GetVersion(), req.Msg.GetManifestDigest(), req.Msg.GetExpectedProjectRevision())
	if err != nil {
		return nil, repairVersionError(err)
	}
	return connect.NewResponse(&executionv1.TransitionCandidateVersionResponse{Version: version, ProjectRevision: revision}), nil
}

func repairVersionError(err error) error {
	code := connect.CodeInternal
	switch {
	case errors.Is(err, agentdomain.ErrInvalid), errors.Is(err, projectdomain.ErrInvalid), errors.Is(err, registrydomain.ErrInvalid):
		code = connect.CodeInvalidArgument
	case errors.Is(err, agentdomain.ErrNotFound), errors.Is(err, registrydomain.ErrNotFound), errors.Is(err, projectapp.ErrAppNotInstallable):
		code = connect.CodeNotFound
	case errors.Is(err, registrydomain.ErrIdempotencyConflict), errors.Is(err, projectdomain.ErrConflict):
		code = connect.CodeAborted
	case errors.Is(err, orchestration.ErrRepairCandidateNotReady), errors.Is(err, registryapp.ErrBuildUnavailable), errors.Is(err, registryapp.ErrNotStaged), errors.Is(err, orchestration.ErrInstallationChanged):
		code = connect.CodeFailedPrecondition
	case errors.Is(err, agentports.ErrStoreUnavailable), errors.Is(err, registryports.ErrStoreUnavailable), errors.Is(err, projectports.ErrStoreUnavailable), dbtransient.IsTransient(err):
		code = connect.CodeUnavailable
	}
	return connect.NewError(code, errors.New("repair version request failed"))
}
