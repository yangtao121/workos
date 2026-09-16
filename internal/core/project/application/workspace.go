// Package application serves the project workspace binding commands
// (ADR-0030): Core owns the association and authorization facts; the runtime
// owns the actual directory.
package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/yangtao121/workos/internal/core/project/domain"
	"github.com/yangtao121/workos/internal/core/project/ports"
	"github.com/yangtao121/workos/internal/platform/ids"
)

// WorkspaceService binds operator-registered workspace sources to projects.
type WorkspaceService struct {
	projects   *Service
	repository ports.WorkspaceRepository
	directory  ports.SourceDirectory
	generator  ids.Generator
	now        func() time.Time
}

func NewWorkspaceService(repository ports.WorkspaceRepository, directory ports.SourceDirectory, generator ids.Generator) *WorkspaceService {
	return &WorkspaceService{repository: repository, directory: directory, generator: generator, now: time.Now}
}

func workspaceRequestDigest(sourceID, displayName string, readOnly bool) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("workspace:%s:%s:%t", sourceID, displayName, readOnly)))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Bind validates the source against the operator-registered directory and
// creates the project's single active binding. Replaying a consumed key
// returns the stored binding; the same key with different facts conflicts.
func (s *WorkspaceService) Bind(ctx context.Context, ownerUserID, projectID, workspaceSourceID, displayName, idempotencyKey string) (domain.WorkspaceBinding, error) {
	if !domain.ValidProjectUUID(ownerUserID) || !domain.ValidProjectUUID(projectID) || !domain.ValidWorkspaceSourceID(workspaceSourceID) || idempotencyKey == "" || len(idempotencyKey) > 128 || displayName == "" || len(displayName) > 128 {
		return domain.WorkspaceBinding{}, domain.ErrInvalid
	}
	sources, err := s.directory.Sources(ctx, ownerUserID)
	if err != nil {
		return domain.WorkspaceBinding{}, err
	}
	registered := false
	readOnly := false
	for _, source := range sources {
		if source.ID == workspaceSourceID && source.ProjectID == projectID {
			registered = true
			readOnly = source.ReadOnly
			break
		}
	}
	if !registered {
		return domain.WorkspaceBinding{}, domain.ErrWorkspaceSourceUnknown
	}
	now := s.now().UTC()
	digest := workspaceRequestDigest(workspaceSourceID, displayName, readOnly)
	binding := domain.WorkspaceBinding{
		ID:                s.generator.New(),
		OwnerUserID:       ownerUserID,
		ProjectID:         projectID,
		WorkspaceSourceID: workspaceSourceID,
		IdempotencyKey:    idempotencyKey,
		DisplayName:       displayName,
		ReadOnly:          readOnly,
		State:             domain.WorkspaceBindingActive,
		Revision:          1,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	storedDigest, created, err := s.repository.InsertWorkspaceBinding(ctx, binding, digest)
	if err != nil {
		return domain.WorkspaceBinding{}, err
	}
	if !created {
		if storedDigest != digest {
			return domain.WorkspaceBinding{}, domain.ErrWorkspaceConflict
		}
		return s.repository.GetWorkspaceBindingByIdempotency(ctx, ownerUserID, idempotencyKey)
	}
	return binding, nil
}

// Get reads one binding for its owner.
func (s *WorkspaceService) Get(ctx context.Context, ownerUserID, bindingID string) (domain.WorkspaceBinding, error) {
	if !domain.ValidProjectUUID(ownerUserID) || !domain.ValidProjectUUID(bindingID) {
		return domain.WorkspaceBinding{}, domain.ErrInvalid
	}
	return s.repository.GetWorkspaceBinding(ctx, ownerUserID, bindingID)
}

// ActiveForProject returns the project's active binding pinned by session
// and execution admission.
func (s *WorkspaceService) ActiveForProject(ctx context.Context, ownerUserID, projectID string) (domain.WorkspaceBinding, error) {
	if !domain.ValidProjectUUID(ownerUserID) || !domain.ValidProjectUUID(projectID) {
		return domain.WorkspaceBinding{}, domain.ErrInvalid
	}
	return s.repository.GetActiveWorkspaceBindingForProject(ctx, ownerUserID, projectID)
}

// List pages a project's bindings newest first.
func (s *WorkspaceService) List(ctx context.Context, ownerUserID, projectID string, includeArchived bool) ([]domain.WorkspaceBinding, error) {
	if !domain.ValidProjectUUID(ownerUserID) || !domain.ValidProjectUUID(projectID) {
		return nil, domain.ErrInvalid
	}
	return s.repository.ListWorkspaceBindings(ctx, ownerUserID, projectID, includeArchived)
}

// UpdateAccess flips the read/write mode under optimistic revision control;
// every revision bump invalidates authorizations pinned to the old revision.
func (s *WorkspaceService) UpdateAccess(ctx context.Context, ownerUserID, bindingID string, readOnly bool, expectedRevision int64) (domain.WorkspaceBinding, error) {
	if !domain.ValidProjectUUID(ownerUserID) || !domain.ValidProjectUUID(bindingID) || expectedRevision <= 0 {
		return domain.WorkspaceBinding{}, domain.ErrInvalid
	}
	binding, err := s.repository.UpdateWorkspaceAccess(ctx, ownerUserID, bindingID, readOnly, expectedRevision, s.now().UTC())
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return domain.WorkspaceBinding{}, domain.ErrWorkspaceRevision
		}
		return domain.WorkspaceBinding{}, err
	}
	return binding, nil
}

// Archive revokes this binding; active consumers recheck its revision.
func (s *WorkspaceService) Archive(ctx context.Context, ownerUserID, bindingID string, expectedRevision int64) (domain.WorkspaceBinding, error) {
	if !domain.ValidProjectUUID(ownerUserID) || !domain.ValidProjectUUID(bindingID) || expectedRevision <= 0 {
		return domain.WorkspaceBinding{}, domain.ErrInvalid
	}
	binding, err := s.repository.ArchiveWorkspaceBinding(ctx, ownerUserID, bindingID, expectedRevision, s.now().UTC())
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return domain.WorkspaceBinding{}, domain.ErrWorkspaceRevision
		}
		return domain.WorkspaceBinding{}, err
	}
	return binding, nil
}

// AvailableForProject reveals only operator-registered sources belonging to
// this owner and this project; the browser never supplies a host path.
func (s *WorkspaceService) AvailableForProject(ctx context.Context, owner, project string) ([]ports.WorkspaceSource, error) {
	if !domain.ValidProjectUUID(owner) || !domain.ValidProjectUUID(project) {
		return nil, domain.ErrInvalid
	}
	sources, err := s.directory.Sources(ctx, owner)
	if err != nil {
		return nil, err
	}
	out := []ports.WorkspaceSource{}
	for _, source := range sources {
		if source.ProjectID == project {
			out = append(out, source)
		}
	}
	return out, nil
}

func (s *WorkspaceService) WithProjects(projects *Service) *WorkspaceService {
	s.projects = projects
	return s
}

// ExecuteFile derives scope from the authenticated owner and current binding.
// Writes require both binding revision and the exact observed content digest.
func (s *WorkspaceService) ExecuteFile(ctx context.Context, owner, project, operation, path, content, etag string, revision int64) (map[string]any, domain.WorkspaceBinding, error) {
	empty := domain.WorkspaceBinding{}
	if s.projects == nil {
		return nil, empty, domain.ErrInvalid
	}
	facts, err := s.projects.Get(ctx, owner, project)
	if err != nil {
		return nil, empty, err
	}
	if facts.ArchivedAt != nil {
		return nil, empty, domain.ErrNotFound
	}
	binding, err := s.ActiveForProject(ctx, owner, project)
	if err != nil {
		return nil, empty, err
	}
	files, ok := s.directory.(ports.WorkspaceFiles)
	if !ok {
		return nil, empty, domain.ErrWorkspaceSourceUnknown
	}
	args := map[string]any{"path": path}
	switch operation {
	case "fs.list", "fs.read":
	case "fs.write":
		if binding.ReadOnly || revision != binding.Revision || etag == "" || len(content) > 256*1024 {
			return nil, empty, domain.ErrWorkspaceRevision
		}
		args["content"] = content
		args["guard"] = "replaceIfVersion"
		args["version"] = etag
	default:
		return nil, empty, domain.ErrInvalid
	}
	call, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	result, err := files.ExecuteFile(call, ports.FileExecution{BindingID: binding.ID, Revision: binding.Revision, ID: s.generator.New(), OwnerUserID: owner, ProjectID: project, SourceID: binding.WorkspaceSourceID, ReadOnly: binding.ReadOnly, Operation: operation, Arguments: args})
	if err != nil {
		return nil, empty, err
	}
	if failure, _ := result["error"].(string); failure != "" {
		if failure == "FS_STALE_VERSION" {
			return nil, empty, domain.ErrWorkspaceRevision
		}
		return nil, empty, domain.ErrInvalid
	}
	return result, binding, nil
}
