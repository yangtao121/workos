package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yangtao121/workos/internal/core/project/adapters/postgres/projectdb"
	"github.com/yangtao121/workos/internal/core/project/domain"
	"github.com/yangtao121/workos/internal/core/project/ports"
)

// Repository implements the base Project aggregate port plus the shared
// installation command port. Every storage failure is classified by the
// shared storeError in store.go; create idempotency semantics are decided by
// the database inside one transaction (ADR-0004).
type Repository struct {
	pool    *pgxpool.Pool
	queries *projectdb.Queries
}

func New(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool, queries: projectdb.New(pool)}
}

// LookupCreateRequest returns the stored adjudication of a consumed create
// key. The result snapshot column is authoritative: it is decoded without
// ever consulting the mutable projects row.
func (r *Repository) LookupCreateRequest(ctx context.Context, ownerUserID, idempotencyKey string) (ports.StoredCreateRequest, bool, error) {
	stored, err := r.queries.GetCreateRequest(ctx, projectdb.GetCreateRequestParams{
		OwnerUserID: ownerUserID, IdempotencyKey: idempotencyKey,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ports.StoredCreateRequest{}, false, nil
	}
	if err != nil {
		return ports.StoredCreateRequest{}, false, storeError("query project create request", err)
	}
	result, err := decodeCreateResult(stored.Result)
	if err != nil {
		return ports.StoredCreateRequest{}, true, err
	}
	return ports.StoredCreateRequest{RequestDigest: stored.RequestDigest, Result: result}, true, nil
}

// CreateProject executes one create command as a single atomic verdict
// (ADR-0004). The projects (owner_user_id, idempotency_key) unique index is
// the physical insert arbiter: a concurrent command that wins the insert must
// commit before the loser's ON CONFLICT DO NOTHING returns, so the loser's
// re-read of the request mapping sees the committed adjudication. The
// mapping row, project event, and outbox row commit together with the
// project insert; any failure rolls the whole command back and the key stays
// unconsumed.
func (r *Repository) CreateProject(ctx context.Context, command ports.CreateCommand) (domain.Project, error) {
	snapshot, err := encodeCreateResult(command.Project)
	if err != nil {
		// Snapshot encoding is a program error over already-validated
		// domain values: it must never masquerade as an availability loss.
		return domain.Project{}, err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return domain.Project{}, storeError("begin create project", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	queries := r.queries.WithTx(tx)

	rows, err := queries.InsertProject(ctx, projectdb.InsertProjectParams{
		ID: command.Project.ID, OwnerUserID: command.Project.OwnerUserID, IdempotencyKey: command.IdempotencyKey,
		Name: command.Project.Name, Icon: command.Project.Icon, WorkspaceRefs: snapshot.WorkspaceRefs,
		HarnessBinding: snapshot.HarnessBinding, InstalledAppIds: command.Project.InstalledAppIDs,
		DefaultAgentRole:      command.Project.DefaultAgentRole,
		KnowledgeCollectionID: command.Project.KnowledgeCollectionID, ArtifactCollectionID: command.Project.ArtifactCollectionID,
		Revision: command.Project.Revision, CreatedAt: timestamp(command.Project.CreatedAt), UpdatedAt: timestamp(command.Project.UpdatedAt),
	})
	if err != nil {
		return domain.Project{}, storeError("insert project", err)
	}
	if rows == 0 {
		// The key (or its legacy row) already exists: re-read the mapping
		// inside this transaction and replay or conflict — never return the
		// current project row.
		return r.adjudicateLostCreate(ctx, queries, command)
	}
	if err := appendProjectEvent(ctx, queries, command.Project, "project.created.v1"); err != nil {
		return domain.Project{}, err
	}
	if _, err := queries.InsertCreateRequest(ctx, projectdb.InsertCreateRequestParams{
		OwnerUserID: command.Project.OwnerUserID, IdempotencyKey: command.IdempotencyKey,
		RequestDigest: command.RequestDigest, Result: snapshot.Result, CreatedAt: timestamp(command.Now),
	}); err != nil {
		return domain.Project{}, storeError("insert project create request", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Project{}, storeError("commit create project", err)
	}
	return command.Project, nil
}

// adjudicateLostCreate resolves a lost insert race for a consumed key. A
// mapping row with the identical digest replays the persisted first response;
// a different digest is a stable conflict. No mapping row means a legacy key
// from before migration 013: its original request is unknown and its first
// response is unrecoverable, so the command fails closed (ADR-0004 §5)
// instead of guessing.
func (r *Repository) adjudicateLostCreate(ctx context.Context, queries *projectdb.Queries, command ports.CreateCommand) (domain.Project, error) {
	stored, err := queries.GetCreateRequest(ctx, projectdb.GetCreateRequestParams{
		OwnerUserID: command.Project.OwnerUserID, IdempotencyKey: command.IdempotencyKey,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Project{}, domain.ErrIdempotencyConflict
	}
	if err != nil {
		return domain.Project{}, storeError("classify lost project create", err)
	}
	if stored.RequestDigest != command.RequestDigest {
		return domain.Project{}, domain.ErrIdempotencyConflict
	}
	return decodeCreateResult(stored.Result)
}

func (r *Repository) GetProject(ctx context.Context, ownerUserID, projectID string) (domain.Project, error) {
	value, err := r.queries.GetProject(ctx, projectdb.GetProjectParams{OwnerUserID: ownerUserID, ID: projectID})
	return projectFromDB(value, err)
}

func (r *Repository) ListProjects(ctx context.Context, ownerUserID, cursor string, limit int, includeArchived bool) ([]domain.Project, error) {
	if cursor == "" {
		cursor = "00000000-0000-0000-0000-000000000000"
	}
	values, err := r.queries.ListProjects(ctx, projectdb.ListProjectsParams{
		OwnerUserID: ownerUserID, Cursor: cursor, IncludeArchived: includeArchived, RowLimit: int32(limit),
	})
	if err != nil {
		return nil, storeError("list projects", err)
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

func (r *Repository) UpdateProject(ctx context.Context, project domain.Project, expectedRevision int64) (domain.Project, error) {
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
		return domain.Project{}, storeError("begin update project", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	queries := r.queries.WithTx(tx)
	value, err := queries.UpdateProject(ctx, projectdb.UpdateProjectParams{
		Name: project.Name, Icon: project.Icon, WorkspaceRefs: workspace, HarnessBinding: binding,
		UpdatedAt: timestamp(project.UpdatedAt), ID: project.ID, OwnerUserID: project.OwnerUserID, ExpectedRevision: expectedRevision,
	})
	updated, err := projectFromDB(value, err)
	if errors.Is(err, domain.ErrNotFound) {
		// The application read the project first, so a guarded miss here is
		// a lost revision race, not a missing project.
		return domain.Project{}, domain.ErrConflict
	}
	if err != nil {
		return domain.Project{}, err
	}
	if err := appendProjectEvent(ctx, queries, updated, "project.updated.v1"); err != nil {
		return domain.Project{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Project{}, storeError("commit update project", err)
	}
	return updated, nil
}

func (r *Repository) ArchiveProject(ctx context.Context, ownerID, projectID string, expectedRevision int64) (domain.Project, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return domain.Project{}, storeError("begin archive project", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	queries := r.queries.WithTx(tx)
	value, err := queries.ArchiveProject(ctx, projectdb.ArchiveProjectParams{
		ArchivedAt: timestamp(time.Now().UTC()), ID: projectID, OwnerUserID: ownerID, ExpectedRevision: expectedRevision,
	})
	archived, err := projectFromDB(value, err)
	if errors.Is(err, domain.ErrNotFound) {
		// Same as UpdateProject: the application read the project first, so
		// a guarded miss is a lost revision race.
		return domain.Project{}, domain.ErrConflict
	}
	if err != nil {
		return domain.Project{}, err
	}
	if err := appendProjectEvent(ctx, queries, archived, "project.archived.v1"); err != nil {
		return domain.Project{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Project{}, storeError("commit archive project", err)
	}
	return archived, nil
}

func projectFromDB(value projectdb.WorkosCoreProject, err error) (domain.Project, error) {
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Project{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Project{}, storeError("query project", err)
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
		// Corrupt persisted JSON is invariant damage, never an outage.
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

// appendProjectEvent appends the project stream event and outbox row with
// sequence equal to the Project revision. Identifier generation failures are
// program errors; every statement failure is classified at the port
// boundary.
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
		return storeError("append project event", err)
	}
	outboxID, err := uuid.NewV7()
	if err != nil {
		return fmt.Errorf("generate outbox id: %w", err)
	}
	if err := queries.InsertProjectOutbox(ctx, projectdb.InsertProjectOutboxParams{
		ID: outboxID.String(), AggregateID: project.ID, EventType: eventType,
		Payload: payload, OccurredAt: timestamp(project.UpdatedAt),
	}); err != nil {
		return storeError("append project outbox", err)
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

// createResultVersion marks the persisted first-response snapshot encoding
// (ADR-0004); the database CHECK in migration 013 refuses any other value.
const createResultVersion = "1"

type snapshotProject struct {
	ID                    string                 `json:"id"`
	OwnerUserID           string                 `json:"owner_user_id"`
	Name                  string                 `json:"name"`
	Icon                  string                 `json:"icon"`
	WorkspaceRefs         []domain.WorkspaceRef  `json:"workspace_refs"`
	HarnessBinding        *domain.HarnessBinding `json:"harness_binding"`
	InstalledAppIDs       []string               `json:"installed_app_ids"`
	DefaultAgentRole      string                 `json:"default_agent_role"`
	KnowledgeCollectionID string                 `json:"knowledge_collection_id"`
	ArtifactCollectionID  string                 `json:"artifact_collection_id"`
	Revision              int64                  `json:"revision"`
	CreatedAt             string                 `json:"created_at"`
	UpdatedAt             string                 `json:"updated_at"`
	ArchivedAt            *string                `json:"archived_at"`
}

type createResultSnapshot struct {
	ResultVersion string          `json:"result_version"`
	Project       snapshotProject `json:"project"`
}

// encodeCreateResult pins the exact first CreateProjectResponse projection
// (every public field, including the timestamps the response carried) as a
// versioned snapshot. Replays decode this snapshot verbatim and never read
// the mutable project row.
func encodeCreateResult(project domain.Project) (createSnapshotColumns, error) {
	snapshot := createResultSnapshot{
		ResultVersion: createResultVersion,
		Project: snapshotProject{
			ID: project.ID, OwnerUserID: project.OwnerUserID, Name: project.Name, Icon: project.Icon,
			WorkspaceRefs: project.WorkspaceRefs, HarnessBinding: project.HarnessBinding,
			InstalledAppIDs: project.InstalledAppIDs, DefaultAgentRole: project.DefaultAgentRole,
			KnowledgeCollectionID: project.KnowledgeCollectionID, ArtifactCollectionID: project.ArtifactCollectionID,
			Revision:  project.Revision,
			CreatedAt: project.CreatedAt.Format(time.RFC3339Nano),
			UpdatedAt: project.UpdatedAt.Format(time.RFC3339Nano),
		},
	}
	if project.ArchivedAt != nil {
		archived := project.ArchivedAt.Format(time.RFC3339Nano)
		snapshot.Project.ArchivedAt = &archived
	}
	workspace, err := json.Marshal(snapshot.Project.WorkspaceRefs)
	if err != nil {
		return createSnapshotColumns{}, fmt.Errorf("encode workspace refs: %w", err)
	}
	binding, err := encodeBinding(snapshot.Project.HarnessBinding)
	if err != nil {
		return createSnapshotColumns{}, err
	}
	result, err := json.Marshal(snapshot)
	if err != nil {
		return createSnapshotColumns{}, fmt.Errorf("encode project create result: %w", err)
	}
	return createSnapshotColumns{Result: result, WorkspaceRefs: workspace, HarnessBinding: binding}, nil
}

// createSnapshotColumns carries the encoded forms the create transaction
// needs: the result snapshot for the request mapping plus the project row's
// jsonb columns, so the stored project and the replayed snapshot encode the
// identical facts.
type createSnapshotColumns struct {
	Result         []byte
	WorkspaceRefs  []byte
	HarnessBinding []byte
}

// decodeCreateResult restores the first response from its persisted
// snapshot. A snapshot that fails to decode or violates the version marker
// is invariant corruption and stays an opaque internal error.
func decodeCreateResult(value []byte) (domain.Project, error) {
	var snapshot createResultSnapshot
	if err := json.Unmarshal(value, &snapshot); err != nil {
		return domain.Project{}, fmt.Errorf("decode project create result: %w", err)
	}
	if snapshot.ResultVersion != createResultVersion {
		return domain.Project{}, fmt.Errorf("decode project create result: unsupported result version %q", snapshot.ResultVersion)
	}
	stored := snapshot.Project
	project := domain.Project{
		ID: stored.ID, OwnerUserID: stored.OwnerUserID, Name: stored.Name, Icon: stored.Icon,
		WorkspaceRefs:   stored.WorkspaceRefs,
		InstalledAppIDs: stored.InstalledAppIDs, DefaultAgentRole: stored.DefaultAgentRole,
		KnowledgeCollectionID: stored.KnowledgeCollectionID, ArtifactCollectionID: stored.ArtifactCollectionID,
		Revision: stored.Revision,
	}
	if project.WorkspaceRefs == nil {
		project.WorkspaceRefs = []domain.WorkspaceRef{}
	}
	if project.InstalledAppIDs == nil {
		project.InstalledAppIDs = []string{}
	}
	created, err := parseSnapshotTime(stored.CreatedAt)
	if err != nil {
		return domain.Project{}, err
	}
	updated, err := parseSnapshotTime(stored.UpdatedAt)
	if err != nil {
		return domain.Project{}, err
	}
	project.CreatedAt, project.UpdatedAt = created, updated
	if stored.ArchivedAt != nil {
		archived, err := parseSnapshotTime(*stored.ArchivedAt)
		if err != nil {
			return domain.Project{}, err
		}
		project.ArchivedAt = &archived
	}
	if stored.HarnessBinding != nil {
		project.HarnessBinding = stored.HarnessBinding
	}
	return project, nil
}

func parseSnapshotTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("decode project create result timestamp: %w", err)
	}
	return parsed, nil
}
