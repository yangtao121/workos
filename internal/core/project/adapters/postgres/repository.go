package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yangtao121/workos/internal/core/project/adapters/postgres/projectdb"
	"github.com/yangtao121/workos/internal/core/project/domain"
)

type Repository struct {
	pool    *pgxpool.Pool
	queries *projectdb.Queries
}

func New(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool, queries: projectdb.New(pool)}
}

func (r *Repository) Create(ctx context.Context, project domain.Project, idempotencyKey string) (domain.Project, error) {
	workspace, err := json.Marshal(project.WorkspaceRefs)
	if err != nil {
		return domain.Project{}, fmt.Errorf("encode workspace refs: %w", err)
	}
	binding, err := encodeBinding(project.HarnessBinding)
	if err != nil {
		return domain.Project{}, err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return domain.Project{}, fmt.Errorf("begin create project: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	queries := r.queries.WithTx(tx)
	rows, err := queries.InsertProject(ctx, projectdb.InsertProjectParams{
		ID: project.ID, OwnerUserID: project.OwnerUserID, IdempotencyKey: idempotencyKey,
		Name: project.Name, Icon: project.Icon, WorkspaceRefs: workspace, HarnessBinding: binding,
		InstalledAppIds: project.InstalledAppIDs, DefaultAgentRole: project.DefaultAgentRole,
		KnowledgeCollectionID: project.KnowledgeCollectionID, ArtifactCollectionID: project.ArtifactCollectionID,
		Revision: project.Revision, CreatedAt: timestamp(project.CreatedAt), UpdatedAt: timestamp(project.UpdatedAt),
	})
	if err != nil {
		return domain.Project{}, fmt.Errorf("insert project: %w", err)
	}
	if rows == 0 {
		if err := tx.Commit(ctx); err != nil {
			return domain.Project{}, fmt.Errorf("commit idempotent create: %w", err)
		}
		return r.getByIdempotency(ctx, project.OwnerUserID, idempotencyKey)
	}
	if err := appendProjectEvent(ctx, queries, project, "project.created.v1"); err != nil {
		return domain.Project{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Project{}, fmt.Errorf("commit create project: %w", err)
	}
	return project, nil
}

func (r *Repository) Get(ctx context.Context, ownerID, projectID string) (domain.Project, error) {
	value, err := r.queries.GetProject(ctx, projectdb.GetProjectParams{OwnerUserID: ownerID, ID: projectID})
	return projectFromDB(value, err)
}

func (r *Repository) getByIdempotency(ctx context.Context, ownerID, key string) (domain.Project, error) {
	value, err := r.queries.GetProjectByIdempotency(ctx, projectdb.GetProjectByIdempotencyParams{
		OwnerUserID: ownerID, IdempotencyKey: key,
	})
	return projectFromDB(value, err)
}

func (r *Repository) List(ctx context.Context, ownerID, cursor string, limit int, includeArchived bool) ([]domain.Project, error) {
	if cursor == "" {
		cursor = "00000000-0000-0000-0000-000000000000"
	}
	values, err := r.queries.ListProjects(ctx, projectdb.ListProjectsParams{
		OwnerUserID: ownerID, Cursor: cursor, IncludeArchived: includeArchived, RowLimit: int32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	projects := make([]domain.Project, 0, len(values))
	for _, value := range values {
		project, err := projectFromDB(value, nil)
		if err != nil {
			return nil, err
		}
		projects = append(projects, project)
	}
	return projects, nil
}

func (r *Repository) Update(ctx context.Context, project domain.Project, expectedRevision int64) (domain.Project, error) {
	workspace, err := json.Marshal(project.WorkspaceRefs)
	if err != nil {
		return domain.Project{}, fmt.Errorf("encode workspace refs: %w", err)
	}
	binding, err := encodeBinding(project.HarnessBinding)
	if err != nil {
		return domain.Project{}, err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return domain.Project{}, fmt.Errorf("begin update project: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	queries := r.queries.WithTx(tx)
	value, err := queries.UpdateProject(ctx, projectdb.UpdateProjectParams{
		Name: project.Name, Icon: project.Icon, WorkspaceRefs: workspace, HarnessBinding: binding,
		UpdatedAt: timestamp(project.UpdatedAt), ID: project.ID, OwnerUserID: project.OwnerUserID, ExpectedRevision: expectedRevision,
	})
	updated, err := projectFromDB(value, err)
	if errors.Is(err, domain.ErrNotFound) {
		return domain.Project{}, domain.ErrConflict
	}
	if err != nil {
		return domain.Project{}, err
	}
	if err := appendProjectEvent(ctx, queries, updated, "project.updated.v1"); err != nil {
		return domain.Project{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Project{}, fmt.Errorf("commit update project: %w", err)
	}
	return updated, nil
}

func (r *Repository) Archive(ctx context.Context, ownerID, projectID string, expectedRevision int64) (domain.Project, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return domain.Project{}, fmt.Errorf("begin archive project: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	queries := r.queries.WithTx(tx)
	value, err := queries.ArchiveProject(ctx, projectdb.ArchiveProjectParams{
		ArchivedAt: timestamp(time.Now().UTC()), ID: projectID, OwnerUserID: ownerID, ExpectedRevision: expectedRevision,
	})
	archived, err := projectFromDB(value, err)
	if errors.Is(err, domain.ErrNotFound) {
		return domain.Project{}, domain.ErrConflict
	}
	if err != nil {
		return domain.Project{}, err
	}
	if err := appendProjectEvent(ctx, queries, archived, "project.archived.v1"); err != nil {
		return domain.Project{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Project{}, fmt.Errorf("commit archive project: %w", err)
	}
	return archived, nil
}

func projectFromDB(value projectdb.WorkosCoreProject, err error) (domain.Project, error) {
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Project{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Project{}, fmt.Errorf("query project: %w", err)
	}
	project := domain.Project{
		ID: value.ID, OwnerUserID: value.OwnerUserID, Name: value.Name, Icon: value.Icon,
		InstalledAppIDs: value.InstalledAppIds, DefaultAgentRole: value.DefaultAgentRole,
		KnowledgeCollectionID: value.KnowledgeCollectionID, ArtifactCollectionID: value.ArtifactCollectionID,
		Revision: value.Revision, CreatedAt: value.CreatedAt.Time, UpdatedAt: value.UpdatedAt.Time,
	}
	if value.ArchivedAt.Valid {
		archived := value.ArchivedAt.Time
		project.ArchivedAt = &archived
	}
	if err := json.Unmarshal(value.WorkspaceRefs, &project.WorkspaceRefs); err != nil {
		return domain.Project{}, fmt.Errorf("decode workspace refs: %w", err)
	}
	if len(value.HarnessBinding) > 0 && string(value.HarnessBinding) != "null" {
		project.HarnessBinding = &domain.HarnessBinding{}
		if err := json.Unmarshal(value.HarnessBinding, project.HarnessBinding); err != nil {
			return domain.Project{}, fmt.Errorf("decode harness binding: %w", err)
		}
	}
	return project, nil
}

func appendProjectEvent(ctx context.Context, queries *projectdb.Queries, project domain.Project, eventType string) error {
	payload, err := json.Marshal(map[string]any{"projectId": project.ID, "revision": project.Revision, "name": project.Name})
	if err != nil {
		return fmt.Errorf("encode project event: %w", err)
	}
	eventID, err := uuid.NewV7()
	if err != nil {
		return fmt.Errorf("generate event id: %w", err)
	}
	if err := queries.InsertProjectEvent(ctx, projectdb.InsertProjectEventParams{
		ID: eventID.String(), StreamID: project.ID, Sequence: project.Revision, EventType: eventType,
		Payload: payload, OccurredAt: timestamp(project.UpdatedAt),
	}); err != nil {
		return fmt.Errorf("append project event: %w", err)
	}
	outboxID, err := uuid.NewV7()
	if err != nil {
		return fmt.Errorf("generate outbox id: %w", err)
	}
	if err := queries.InsertProjectOutbox(ctx, projectdb.InsertProjectOutboxParams{
		ID: outboxID.String(), AggregateID: project.ID, EventType: eventType,
		Payload: payload, OccurredAt: timestamp(project.UpdatedAt),
	}); err != nil {
		return fmt.Errorf("append project outbox: %w", err)
	}
	return nil
}

func encodeBinding(value *domain.HarnessBinding) ([]byte, error) {
	if value == nil {
		return nil, nil
	}
	result, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode harness binding: %w", err)
	}
	return result, nil
}

func timestamp(value time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: value, Valid: true}
}
