// Local-only IndexAdminService transport: bound to the indexer's Unix admin
// socket, never to the gateway or any TCP listener. Responses carry safe
// operational facts only (ADR-0013 §8).
package transport

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"

	indexv1 "github.com/yangtao121/workos/gen/go/workos/index/v1"
	indexv1connect "github.com/yangtao121/workos/gen/go/workos/index/v1/indexv1connect"
	indexerapp "github.com/yangtao121/workos/internal/indexer/application"
	indexerdomain "github.com/yangtao121/workos/internal/indexer/domain"
	"github.com/yangtao121/workos/internal/indexer/ports"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// AdminService is the application surface the admin handler serves.
type AdminService interface {
	Status(ctx context.Context) (indexerapp.IndexStatus, error)
	StartRebuild(ctx context.Context, request indexerapp.RebuildRequest) (indexerapp.RebuildJobView, bool, error)
	GetRebuildJob(ctx context.Context, jobID string) (indexerapp.RebuildJobView, error)
	CancelRebuildJob(ctx context.Context, jobID string) (bool, error)
	// RegisterWorkspaceSource binds (or rebinds) the owner's local mount.
	RegisterWorkspaceSource(ctx context.Context, ownerUserID, projectID, rootPath string) (ports.WorkspaceSource, error)
	// ListWorkspaceSources reads every bound mount.
	ListWorkspaceSources(ctx context.Context) ([]ports.WorkspaceSource, error)
	// SyncWorkspaceSource runs one bounded ingestion pass.
	SyncWorkspaceSource(ctx context.Context, sourceID string) (indexerapp.SyncResult, error)
	// PutArchiveObject stores one bounded content-addressed object.
	PutArchiveObject(ctx context.Context, ownerUserID, mediaType string, content []byte) (indexerapp.ArchivePutResult, error)
	// GetArchiveObject reads one object's metadata and bytes.
	GetArchiveObject(ctx context.Context, ownerUserID, objectID string) (ports.ArchiveObject, []byte, error)
	// ListArchiveObjects reads one bounded metadata page.
	ListArchiveObjects(ctx context.Context, ownerUserID string, limit int) ([]ports.ArchiveObject, error)
}

type AdminHandler struct {
	service AdminService
}

func NewAdminHandler(service AdminService) *AdminHandler {
	return &AdminHandler{service: service}
}

// NewAdminConnectHandler wires the local admin transport. Requests are tiny
// operator commands; 16 KiB bounds them far above the legal maximum.
func NewAdminConnectHandler(service AdminService) (string, http.Handler) {
	return indexv1connect.NewIndexAdminServiceHandler(
		NewAdminHandler(service),
		connect.WithReadMaxBytes(16*1024),
	)
}

func (h *AdminHandler) GetIndexAdminStatus(ctx context.Context, _ *connect.Request[indexv1.GetIndexAdminStatusRequest]) (*connect.Response[indexv1.GetIndexAdminStatusResponse], error) {
	status, err := h.service.Status(ctx)
	if err != nil {
		return nil, mapAdminError(err)
	}
	response := &indexv1.GetIndexAdminStatusResponse{
		CatchingUp:          status.CatchingUp,
		PendingPublications: status.Pending,
		IndexedThrough:      formatMicros(status.IndexedThrough),
		LastIndexedAt:       formatMicros(status.LastIndexedAt),
	}
	if status.ActiveRebuild != nil {
		response.ActiveRebuild = rebuildJobProto(*status.ActiveRebuild)
	}
	if status.ActiveGeneration.ID != "" {
		// Generation facts are read from the projection view; the status
		// carries the pointer identity plus the live rebuild summary.
		response.ActiveGeneration = &indexv1.IndexAdminGeneration{
			GenerationId:   status.ActiveGeneration.ID,
			Scope:          status.ActiveGeneration.Scope,
			Status:         status.ActiveGeneration.Status,
			DocumentCount:  status.ActiveGeneration.DocumentCount,
			TombstoneCount: status.ActiveGeneration.TombstoneCount,
			CreatedAt:      formatMicros(status.ActiveGeneration.CreatedAt),
			PromotedAt:     formatMicros(status.ActiveGeneration.PromotedAt),
		}
	}
	return connect.NewResponse(response), nil
}

func (h *AdminHandler) StartIndexRebuild(ctx context.Context, req *connect.Request[indexv1.StartIndexRebuildRequest]) (*connect.Response[indexv1.StartIndexRebuildResponse], error) {
	request := indexerapp.RebuildRequest{IdempotencyKey: req.Msg.GetIdempotencyKey()}
	switch scope := req.Msg.GetScope().(type) {
	case *indexv1.StartIndexRebuildRequest_All:
		request.Scope = "all"
	case *indexv1.StartIndexRebuildRequest_Project:
		request.Scope = "project"
		request.OwnerUserID = scope.Project.GetOwnerUserId()
		request.ProjectID = scope.Project.GetProjectId()
	default:
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("rebuild request is invalid"))
	}
	job, _, err := h.service.StartRebuild(ctx, request)
	if err != nil {
		return nil, mapAdminError(err)
	}
	return connect.NewResponse(&indexv1.StartIndexRebuildResponse{Job: rebuildJobProto(job)}), nil
}

func (h *AdminHandler) GetIndexRebuildJob(ctx context.Context, req *connect.Request[indexv1.GetIndexRebuildJobRequest]) (*connect.Response[indexv1.GetIndexRebuildJobResponse], error) {
	job, err := h.service.GetRebuildJob(ctx, req.Msg.GetJobId())
	if err != nil {
		return nil, mapAdminError(err)
	}
	return connect.NewResponse(&indexv1.GetIndexRebuildJobResponse{Job: rebuildJobProto(job)}), nil
}

func (h *AdminHandler) CancelIndexRebuildJob(ctx context.Context, req *connect.Request[indexv1.CancelIndexRebuildJobRequest]) (*connect.Response[indexv1.CancelIndexRebuildJobResponse], error) {
	job, err := h.service.GetRebuildJob(ctx, req.Msg.GetJobId())
	if err != nil {
		return nil, mapAdminError(err)
	}
	accepted, err := h.service.CancelRebuildJob(ctx, req.Msg.GetJobId())
	if err != nil {
		return nil, mapAdminError(err)
	}
	if !accepted {
		// Promoting jobs are never canceled; the caller sees the unchanged
		// job and decides.
		return connect.NewResponse(&indexv1.CancelIndexRebuildJobResponse{Job: rebuildJobProto(job)}), nil
	}
	updated, err := h.service.GetRebuildJob(ctx, req.Msg.GetJobId())
	if err != nil {
		return nil, mapAdminError(err)
	}
	return connect.NewResponse(&indexv1.CancelIndexRebuildJobResponse{Job: rebuildJobProto(updated)}), nil
}

func rebuildJobProto(job indexerapp.RebuildJobView) *indexv1.IndexAdminRebuildJob {
	created := job.CreatedAt
	updated := job.UpdatedAt
	return &indexv1.IndexAdminRebuildJob{
		JobId: job.ID, Scope: job.Scope,
		State: job.State, PhaseCursor: job.PhaseCursor,
		SnapshotBoundary: job.SnapshotBoundary,
		SourceCount:      job.SourceCount, AppliedCount: job.AppliedCount, TombstoneCount: job.TombstoneCount,
		FailureCategory: job.FailureCategory, TargetGenerationId: job.TargetGeneration,
		CreatedAt: timestampOf(created), UpdatedAt: timestampOf(updated),
	}
}

func formatMicros(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format("2006-01-02T15:04:05.000000Z07:00")
}

func timestampOf(value time.Time) *timestamppb.Timestamp {
	return timestamppb.New(value)
}

func (h *AdminHandler) RegisterWorkspaceSource(ctx context.Context, req *connect.Request[indexv1.RegisterWorkspaceSourceRequest]) (*connect.Response[indexv1.RegisterWorkspaceSourceResponse], error) {
	source, err := h.service.RegisterWorkspaceSource(ctx, req.Msg.GetOwnerUserId(), req.Msg.GetProjectId(), req.Msg.GetRootPath())
	if err != nil {
		return nil, mapAdminError(err)
	}
	return connect.NewResponse(&indexv1.RegisterWorkspaceSourceResponse{Source: workspaceSourceProto(source)}), nil
}

func (h *AdminHandler) ListWorkspaceSources(ctx context.Context, _ *connect.Request[indexv1.ListWorkspaceSourcesRequest]) (*connect.Response[indexv1.ListWorkspaceSourcesResponse], error) {
	sources, err := h.service.ListWorkspaceSources(ctx)
	if err != nil {
		return nil, mapAdminError(err)
	}
	views := make([]*indexv1.IndexWorkspaceSource, 0, len(sources))
	for _, source := range sources {
		views = append(views, workspaceSourceProto(source))
	}
	return connect.NewResponse(&indexv1.ListWorkspaceSourcesResponse{Sources: views}), nil
}

func (h *AdminHandler) SyncWorkspaceSource(ctx context.Context, req *connect.Request[indexv1.SyncWorkspaceSourceRequest]) (*connect.Response[indexv1.SyncWorkspaceSourceResponse], error) {
	result, err := h.service.SyncWorkspaceSource(ctx, req.Msg.GetSourceId())
	if err != nil {
		return nil, mapAdminError(err)
	}
	return connect.NewResponse(&indexv1.SyncWorkspaceSourceResponse{
		Source:          workspaceSourceProto(result.Source),
		AppliedCount:    result.Applied,
		TombstonedCount: result.Tombstoned,
		SkippedCount:    result.Skipped,
		SkippedReasons:  result.SkipReasons,
	}), nil
}

func (h *AdminHandler) PutArchiveObject(ctx context.Context, req *connect.Request[indexv1.PutArchiveObjectRequest]) (*connect.Response[indexv1.PutArchiveObjectResponse], error) {
	result, err := h.service.PutArchiveObject(ctx, req.Msg.GetOwnerUserId(), req.Msg.GetMediaType(), req.Msg.GetContent())
	if err != nil {
		return nil, mapAdminError(err)
	}
	object, inserted := result.Object, result.Inserted
	if err != nil {
		return nil, mapAdminError(err)
	}
	return connect.NewResponse(&indexv1.PutArchiveObjectResponse{
		ObjectId: object.ID, Sha256: object.Sha256,
		ByteCount: object.ByteCount, Deduplicated: !inserted,
	}), nil
}

func (h *AdminHandler) GetArchiveObject(ctx context.Context, req *connect.Request[indexv1.GetArchiveObjectRequest]) (*connect.Response[indexv1.GetArchiveObjectResponse], error) {
	object, content, err := h.service.GetArchiveObject(ctx, req.Msg.GetOwnerUserId(), req.Msg.GetObjectId())
	if err != nil {
		return nil, mapAdminError(err)
	}
	return connect.NewResponse(&indexv1.GetArchiveObjectResponse{
		Object: &indexv1.ArchiveObjectMetadata{
			ObjectId: object.ID, Sha256: object.Sha256,
			MediaType: object.MediaType, ByteCount: object.ByteCount,
			CreatedAt: formatMicros(object.CreatedAt),
		},
		Content: content,
	}), nil
}

func (h *AdminHandler) ListArchiveObjects(ctx context.Context, req *connect.Request[indexv1.ListArchiveObjectsRequest]) (*connect.Response[indexv1.ListArchiveObjectsResponse], error) {
	objects, err := h.service.ListArchiveObjects(ctx, req.Msg.GetOwnerUserId(), int(req.Msg.GetLimit()))
	if err != nil {
		return nil, mapAdminError(err)
	}
	metadata := make([]*indexv1.ArchiveObjectMetadata, 0, len(objects))
	for _, object := range objects {
		metadata = append(metadata, &indexv1.ArchiveObjectMetadata{
			ObjectId: object.ID, Sha256: object.Sha256,
			MediaType: object.MediaType, ByteCount: object.ByteCount,
			CreatedAt: formatMicros(object.CreatedAt),
		})
	}
	return connect.NewResponse(&indexv1.ListArchiveObjectsResponse{Objects: metadata}), nil
}

func workspaceSourceProto(source ports.WorkspaceSource) *indexv1.IndexWorkspaceSource {
	return &indexv1.IndexWorkspaceSource{
		SourceId: source.ID, OwnerUserId: source.OwnerUserID, ProjectId: source.ProjectID,
		RootPath: source.RootPath, Status: source.Status, DegradedReason: source.DegradedReason,
		IndexedCount: source.IndexedCount, SkippedCount: source.SkippedCount, TombstonedCount: source.TombstonedCount,
		LastSyncedAt: formatMicros(source.LastSyncedAt), CreatedAt: formatMicros(source.CreatedAt),
	}
}

// mapAdminError keeps the operator surface sanitized: conflicts are
// Aborted, malformed input is InvalidArgument, misses are NotFound.
func mapAdminError(err error) error {
	switch {
	case errors.Is(err, indexerapp.ErrRebuildConflict), errors.Is(err, indexerapp.ErrRebuildLiveScope):
		return connect.NewError(connect.CodeAborted, errors.New("rebuild conflicts with an existing key or live scope"))
	case errors.Is(err, indexerapp.ErrInvalidRebuild):
		return connect.NewError(connect.CodeInvalidArgument, errors.New("rebuild request is invalid"))
	case errors.Is(err, indexerdomain.ErrWorkspaceConflict):
		return connect.NewError(connect.CodeAborted, errors.New("workspace source changed during scan; retry sync"))
	case errors.Is(err, indexerapp.ErrWorkspaceStopped):
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("workspace source is stopped"))
	case errors.Is(err, indexerdomain.ErrWorkspaceSymlinkEscape):
		return connect.NewError(connect.CodeInvalidArgument, errors.New("workspace root resolves through a symlink"))
	case errors.Is(err, indexerdomain.ErrWorkspaceDegraded):
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("workspace mount is degraded"))
	case errors.Is(err, indexerdomain.ErrWorkspaceInvalidPath), errors.Is(err, indexerdomain.ErrInvalid):
		return connect.NewError(connect.CodeInvalidArgument, errors.New("workspace source request is invalid"))
	case errors.Is(err, indexerdomain.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, errors.New("index job is not available"))
	default:
		if strings.Contains(err.Error(), "temporarily unavailable") {
			return connect.NewError(connect.CodeUnavailable, errors.New("indexer is temporarily unavailable"))
		}
		return connect.NewError(connect.CodeInternal, errors.New("index admin operation failed"))
	}
}
