package application

import (
	"context"
	"time"

	"github.com/yangtao121/workos/internal/core/project/domain"
	"github.com/yangtao121/workos/internal/core/project/ports"
	"github.com/yangtao121/workos/internal/platform/ids"
)

type CreateInput struct {
	OwnerUserID    string
	IdempotencyKey string
	Name           string
	Icon           string
	WorkspaceRefs  []domain.WorkspaceRef
	HarnessBinding *domain.HarnessBinding
}

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

type Service struct {
	repository ports.Repository
	ids        ids.Generator
	now        func() time.Time
}

func New(repository ports.Repository, generator ids.Generator) *Service {
	return &Service{repository: repository, ids: generator, now: func() time.Time { return time.Now().UTC() }}
}

func (s *Service) Create(ctx context.Context, input CreateInput) (domain.Project, error) {
	name, err := domain.NormalizeName(input.Name)
	if err != nil || input.OwnerUserID == "" || input.IdempotencyKey == "" {
		return domain.Project{}, domain.ErrInvalid
	}
	if err := domain.ValidateBinding(input.HarnessBinding); err != nil {
		return domain.Project{}, err
	}
	now := s.now()
	project := domain.Project{
		ID: s.ids.New(), OwnerUserID: input.OwnerUserID, Name: name, Icon: input.Icon,
		WorkspaceRefs: input.WorkspaceRefs, HarnessBinding: input.HarnessBinding,
		InstalledAppIDs: []string{}, KnowledgeCollectionID: s.ids.New(), ArtifactCollectionID: s.ids.New(),
		Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	return s.repository.Create(ctx, project, input.IdempotencyKey)
}

func (s *Service) Get(ctx context.Context, ownerID, projectID string) (domain.Project, error) {
	if ownerID == "" || projectID == "" {
		return domain.Project{}, domain.ErrInvalid
	}
	return s.repository.Get(ctx, ownerID, projectID)
}

func (s *Service) List(ctx context.Context, ownerID, cursor string, limit int, includeArchived bool) ([]domain.Project, error) {
	if ownerID == "" {
		return nil, domain.ErrInvalid
	}
	if limit <= 0 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}
	return s.repository.List(ctx, ownerID, cursor, limit, includeArchived)
}

func (s *Service) Update(ctx context.Context, input UpdateInput) (domain.Project, error) {
	project, err := s.Get(ctx, input.OwnerUserID, input.ProjectID)
	if err != nil {
		return domain.Project{}, err
	}
	if input.ExpectedRevision <= 0 || project.ArchivedAt != nil {
		return domain.Project{}, domain.ErrConflict
	}
	if input.Name != nil {
		project.Name, err = domain.NormalizeName(*input.Name)
		if err != nil {
			return domain.Project{}, err
		}
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
		if err := domain.ValidateBinding(input.HarnessBinding); err != nil {
			return domain.Project{}, err
		}
		project.HarnessBinding = input.HarnessBinding
	}
	project.UpdatedAt = s.now()
	project.Revision = input.ExpectedRevision + 1
	return s.repository.Update(ctx, project, input.ExpectedRevision)
}

func (s *Service) Archive(ctx context.Context, ownerID, projectID string, expectedRevision int64) (domain.Project, error) {
	if ownerID == "" || projectID == "" || expectedRevision <= 0 {
		return domain.Project{}, domain.ErrInvalid
	}
	return s.repository.Archive(ctx, ownerID, projectID, expectedRevision)
}
