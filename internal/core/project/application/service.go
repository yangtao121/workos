package application

import (
	"context"
	"time"

	"github.com/yangtao121/workos/internal/core/project/domain"
	"github.com/yangtao121/workos/internal/core/project/ports"
	"github.com/yangtao121/workos/internal/platform/ids"
)

// CreateInput is one validated-boundary create command request.
type CreateInput struct {
	OwnerUserID    string
	IdempotencyKey string
	Name           string
	Icon           string
	WorkspaceRefs  []domain.WorkspaceRef
	HarnessBinding *domain.HarnessBinding
}

// UpdateInput is one validated-boundary update command request.
type UpdateInput struct {
	OwnerUserID          string
	ProjectID            string
	ExpectedRevision     int64
	Name                 *string
	Icon                 *string
	WorkspaceRefs        []domain.WorkspaceRef
	ReplaceWorkspaceRefs bool
	HarnessBinding       *domain.HarnessBinding
	ClearHarnessBinding  bool
}

// Page is the explicit paging contract: the application owns the effective
// page size and the next-page probe, so the transport never guesses a token
// from the raw request page size.
type Page struct {
	Items     []domain.Project
	NextToken string
}

const (
	defaultPageSize = 50
	maxPageSize     = 100
)

type Service struct {
	repository ports.Repository
	ids        ids.Generator
	now        func() time.Time
}

func New(repository ports.Repository, generator ids.Generator) *Service {
	return &Service{repository: repository, ids: generator, now: func() time.Time { return time.Now().UTC() }}
}

// Create executes one idempotent create command (ADR-0004). Validation and
// normalization happen first and never consume the key; the canonical request
// digest is computed from the normalized inputs; an already-consumed key with
// the identical digest replays the persisted first response exactly, and a
// different digest is a stable conflict. The authoritative same-key race
// adjudication lives inside the repository's single transaction.
func (s *Service) Create(ctx context.Context, input CreateInput) (domain.Project, error) {
	if input.OwnerUserID == "" || !domain.ValidIdempotencyKey(input.IdempotencyKey) {
		return domain.Project{}, domain.ErrInvalid
	}
	name, err := domain.NormalizeName(input.Name)
	if err != nil {
		return domain.Project{}, err
	}
	if err := domain.ValidateIcon(input.Icon); err != nil {
		return domain.Project{}, err
	}
	if err := domain.ValidateWorkspaceRefs(input.WorkspaceRefs); err != nil {
		return domain.Project{}, err
	}
	if err := domain.ValidateBinding(input.HarnessBinding); err != nil {
		return domain.Project{}, err
	}
	digest := domain.CreateRequestDigest(name, input.Icon, input.WorkspaceRefs, input.HarnessBinding)
	if stored, found, err := s.repository.LookupCreateRequest(ctx, input.OwnerUserID, input.IdempotencyKey); found || err != nil {
		if err != nil {
			return domain.Project{}, err
		}
		if stored.RequestDigest != digest {
			return domain.Project{}, domain.ErrIdempotencyConflict
		}
		return stored.Result, nil
	}
	now := s.now()
	project := domain.Project{
		ID: s.ids.New(), OwnerUserID: input.OwnerUserID, Name: name, Icon: input.Icon,
		WorkspaceRefs: input.WorkspaceRefs, HarnessBinding: input.HarnessBinding,
		InstalledAppIDs: []string{}, KnowledgeCollectionID: s.ids.New(), ArtifactCollectionID: s.ids.New(),
		Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	return s.repository.CreateProject(ctx, ports.CreateCommand{
		Project: project, IdempotencyKey: input.IdempotencyKey,
		RequestDigest: digest, Now: now,
	})
}

// Get reads one owner-scoped project. The identifier is validated at this
// boundary so malformed input is a sanitized invalid-argument verdict rather
// than a store round trip.
func (s *Service) Get(ctx context.Context, ownerID, projectID string) (domain.Project, error) {
	if ownerID == "" || !domain.ValidProjectUUID(projectID) {
		return domain.Project{}, domain.ErrInvalid
	}
	return s.repository.GetProject(ctx, ownerID, projectID)
}

// ListProjects returns one page of projects. The page size is normalized
// exactly once here — zero means the default, values above the maximum clamp
// to it, negative values are rejected — and the repository probes
// effective+1 so the next token is only issued when a next page truly
// exists.
func (s *Service) ListProjects(ctx context.Context, ownerID, cursor string, pageSize int, includeArchived bool) (Page, error) {
	if ownerID == "" {
		return Page{}, domain.ErrInvalid
	}
	switch {
	case pageSize < 0:
		return Page{}, domain.ErrInvalid
	case pageSize == 0:
		pageSize = defaultPageSize
	case pageSize > maxPageSize:
		pageSize = maxPageSize
	}
	if cursor != "" && !domain.ValidProjectUUID(cursor) {
		return Page{}, domain.ErrInvalid
	}
	items, err := s.repository.ListProjects(ctx, ownerID, cursor, pageSize+1, includeArchived)
	if err != nil {
		return Page{}, err
	}
	if len(items) <= pageSize {
		return Page{Items: items, NextToken: ""}, nil
	}
	page := items[:pageSize]
	return Page{Items: page, NextToken: page[len(page)-1].ID}, nil
}

// Update mutates one project under optimistic concurrency. All input is
// validated before any read — a positive expected revision is required,
// clearing the binding and providing one are contradictory, workspace refs
// without the replace flag would be silently ignored, and field content has
// to pass its grammar — so ambiguous input is an InvalidArgument that never
// touches the store. Existence and revision arbitration stay with the
// owner-scoped guarded update.
func (s *Service) Update(ctx context.Context, input UpdateInput) (domain.Project, error) {
	if input.OwnerUserID == "" || !domain.ValidProjectUUID(input.ProjectID) {
		return domain.Project{}, domain.ErrInvalid
	}
	if input.ExpectedRevision <= 0 {
		return domain.Project{}, domain.ErrInvalid
	}
	if input.ClearHarnessBinding && input.HarnessBinding != nil {
		return domain.Project{}, domain.ErrInvalid
	}
	if !input.ReplaceWorkspaceRefs && len(input.WorkspaceRefs) > 0 {
		return domain.Project{}, domain.ErrInvalid
	}
	var name *string
	if input.Name != nil {
		normalized, err := domain.NormalizeName(*input.Name)
		if err != nil {
			return domain.Project{}, err
		}
		name = &normalized
	}
	if input.Icon != nil {
		if err := domain.ValidateIcon(*input.Icon); err != nil {
			return domain.Project{}, err
		}
	}
	if input.ReplaceWorkspaceRefs {
		if err := domain.ValidateWorkspaceRefs(input.WorkspaceRefs); err != nil {
			return domain.Project{}, err
		}
	}
	if input.HarnessBinding != nil && !input.ClearHarnessBinding {
		if err := domain.ValidateBinding(input.HarnessBinding); err != nil {
			return domain.Project{}, err
		}
	}
	project, err := s.Get(ctx, input.OwnerUserID, input.ProjectID)
	if err != nil {
		return domain.Project{}, err
	}
	if project.ArchivedAt != nil {
		return domain.Project{}, domain.ErrConflict
	}
	if name != nil {
		project.Name = *name
	}
	if input.Icon != nil {
		project.Icon = *input.Icon
	}
	if input.ReplaceWorkspaceRefs {
		project.WorkspaceRefs = input.WorkspaceRefs
	}
	if input.ClearHarnessBinding {
		project.HarnessBinding = nil
	} else if input.HarnessBinding != nil {
		project.HarnessBinding = input.HarnessBinding
	}
	project.UpdatedAt = s.now()
	project.Revision = input.ExpectedRevision + 1
	return s.repository.UpdateProject(ctx, project, input.ExpectedRevision)
}

// Archive tombstones one project under optimistic concurrency. The
// existence/ownership read happens first so a missing or foreign project is
// a sanitized NotFound — the guarded UPDATE alone could not distinguish that
// from a stale revision.
func (s *Service) Archive(ctx context.Context, ownerID, projectID string, expectedRevision int64) (domain.Project, error) {
	if ownerID == "" || !domain.ValidProjectUUID(projectID) {
		return domain.Project{}, domain.ErrInvalid
	}
	if expectedRevision <= 0 {
		return domain.Project{}, domain.ErrInvalid
	}
	project, err := s.Get(ctx, ownerID, projectID)
	if err != nil {
		return domain.Project{}, err
	}
	if project.ArchivedAt != nil {
		return domain.Project{}, domain.ErrConflict
	}
	return s.repository.ArchiveProject(ctx, ownerID, projectID, expectedRevision)
}

// ReconcileArchivedProjectsPage pages archived project scopes in stable
// (archived_at, id) order for the index-feed tombstone convergence
// (ADR-0013). Internal read over this module's own table only.
func (s *Service) ReconcileArchivedProjects(ctx context.Context, cursor string, pageSize int) ([]ports.ArchivedProjectRef, string, error) {
	limit := pageSize
	if limit <= 0 {
		limit = 100
	}
	if limit > 200 {
		return nil, "", domain.ErrInvalid
	}
	return s.repository.ReconcileArchivedProjectsPage(ctx, cursor, limit)
}
