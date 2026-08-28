package application

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
	"github.com/yangtao121/workos/internal/core/agent/domain"
	"github.com/yangtao121/workos/internal/core/agent/ports"
	"github.com/yangtao121/workos/internal/platform/ids"
)

type SubmitInput struct {
	OwnerUserID    string
	IdempotencyKey string
	ProjectID      string
	ProviderID     string
	Payload        json.RawMessage
}

// AppSubmitInput is one bridge-submitted project task. The caller (the
// orchestration App Agent service) has already validated the active
// installation and its grant and pinned the project scope; this layer owns
// the canonical bounded payload and the durable App provenance mapping.
type AppSubmitInput struct {
	OwnerUserID          string
	AppInstanceID        string
	ClientIdempotencyKey string
	// RequestDigest is the canonical client-request identity computed by the
	// caller from (role, goal); the repository adjudicates replay/conflict on
	// it inside the same transaction that creates the task.
	RequestDigest string
	ProjectID     string
	ProviderID    string
	Role          string
	Goal          string
}

type Service struct {
	repository ports.Repository
	ids        ids.Generator
	now        func() time.Time
}

func New(repository ports.Repository, generator ids.Generator) *Service {
	return &Service{repository: repository, ids: generator, now: func() time.Time { return time.Now().UTC() }}
}

func (s *Service) Submit(ctx context.Context, input SubmitInput) (domain.Task, error) {
	if input.OwnerUserID == "" || input.IdempotencyKey == "" || input.ProviderID == "" || len(input.Payload) == 0 {
		return domain.Task{}, domain.ErrInvalid
	}
	now := s.now()
	task := domain.Task{
		ID: s.ids.New(), OwnerUserID: input.OwnerUserID, ProjectID: input.ProjectID,
		Input: input.Payload, State: domain.StateQueued, ProviderID: input.ProviderID,
		CreatedAt: now, UpdatedAt: now,
	}
	return s.repository.Create(ctx, task, input.IdempotencyKey)
}

func (s *Service) GetByIdempotency(ctx context.Context, ownerID, key string) (domain.Task, error) {
	if ownerID == "" || key == "" {
		return domain.Task{}, domain.ErrInvalid
	}
	return s.repository.GetByIdempotency(ctx, ownerID, key)
}

// SubmitForApp creates one App-principal project task plus its durable
// provenance mapping in the repository's single transaction. The task row's
// own idempotency_key column deliberately carries an unrelated unique value:
// the durable (owner, app instance, client key) mapping is the App idempotency
// authority, so two apps of the same owner can reuse one client key.
func (s *Service) SubmitForApp(ctx context.Context, input AppSubmitInput) (domain.Task, error) {
	if input.OwnerUserID == "" || input.ProviderID == "" ||
		!domain.ValidAppClientIdempotencyKey(input.ClientIdempotencyKey) ||
		!domain.ValidAppTaskUUID(input.AppInstanceID) || !domain.ValidAppTaskUUID(input.ProjectID) ||
		!domain.ValidAppTaskRole(input.Role) || !domain.ValidAppTaskGoal(input.Goal) ||
		len(input.RequestDigest) != len("sha256:")+64 {
		return domain.Task{}, domain.ErrInvalid
	}
	payload, err := canonicalAppTaskPayload(input.ProjectID, input.Role, input.Goal)
	if err != nil {
		return domain.Task{}, domain.ErrInvalid
	}
	now := s.now()
	task := domain.Task{
		ID: s.ids.New(), OwnerUserID: input.OwnerUserID, ProjectID: input.ProjectID,
		Input: payload, State: domain.StateQueued, ProviderID: input.ProviderID,
		CreatedAt: now, UpdatedAt: now,
	}
	return s.repository.CreateForApp(ctx, task, ports.AppTaskProvenance{
		// Opaque unique value for the task row's own key column: the durable
		// mapping below is the App idempotency authority, so two apps of the
		// same owner can reuse one client key.
		TaskIdempotencyKey:   s.ids.New(),
		AppInstanceID:        input.AppInstanceID,
		ClientIdempotencyKey: input.ClientIdempotencyKey,
		RequestDigest:        input.RequestDigest,
	})
}

// GetAppTaskByIdempotency is the replay projection source for bridge runs: it
// returns the stored task and the consumed request digest, or found=false.
func (s *Service) GetAppTaskByIdempotency(ctx context.Context, ownerID, appInstanceID, clientKey string) (domain.Task, string, bool, error) {
	if ownerID == "" || !domain.ValidAppTaskUUID(appInstanceID) || clientKey == "" {
		return domain.Task{}, "", false, domain.ErrInvalid
	}
	record, found, err := s.repository.GetAppTaskRequest(ctx, ownerID, appInstanceID, clientKey)
	if err != nil || !found {
		return domain.Task{}, "", false, err
	}
	task, err := s.repository.Get(ctx, ownerID, record.TaskID)
	if err != nil {
		return domain.Task{}, "", true, err
	}
	return task, record.RequestDigest, true, nil
}

// GetAppTask resolves one App-created task with its provenance fact. The
// mapping must point at this app instance under this owner; project is the
// snapshot the mapping recorded. Anything else is a sanitized NotFound, so
// knowing a foreign task ID string grants nothing.
func (s *Service) GetAppTask(ctx context.Context, ownerID, appInstanceID, taskID string) (domain.Task, string, error) {
	if ownerID == "" || !domain.ValidAppTaskUUID(appInstanceID) || !domain.ValidAppTaskUUID(taskID) {
		return domain.Task{}, "", domain.ErrInvalid
	}
	record, found, err := s.repository.GetAppTaskByTask(ctx, ownerID, appInstanceID, taskID)
	if err != nil {
		return domain.Task{}, "", err
	}
	if !found {
		return domain.Task{}, "", domain.ErrNotFound
	}
	task, err := s.repository.Get(ctx, ownerID, taskID)
	if err != nil {
		return domain.Task{}, "", err
	}
	return task, record.ProjectID, nil
}

// AppTaskEvents lists one App-created task's persisted events after the
// provenance check above has passed.
func (s *Service) AppTaskEvents(ctx context.Context, ownerID, appInstanceID, taskID string, after int64, limit int) ([]domain.Event, error) {
	if _, _, err := s.GetAppTask(ctx, ownerID, appInstanceID, taskID); err != nil {
		return nil, err
	}
	if after < 0 {
		return nil, domain.ErrInvalid
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 100 {
		limit = 100
	}
	return s.repository.ListEvents(ctx, ownerID, taskID, after, limit)
}

// canonicalAppTaskPayload builds the canonical AgentTaskInput the harness
// worker consumes: the project scope is forced to the installation's project
// and nothing else is settable — requested capabilities, output artifact
// types, budget, parent/incident references, and global scope cannot be
// smuggled through a bridge request.
func canonicalAppTaskPayload(projectID, role, goal string) (json.RawMessage, error) {
	payload, err := protojson.Marshal(&agentv1.AgentTaskInput{
		TargetScope: &agentv1.TargetScope{
			Scope: &agentv1.TargetScope_ProjectId{ProjectId: projectID},
		},
		Role: role,
		Goal: goal,
	})
	if err != nil {
		return nil, fmt.Errorf("canonical app task payload: %w", err)
	}
	return payload, nil
}

func (s *Service) Get(ctx context.Context, ownerID, taskID string) (domain.Task, error) {
	if ownerID == "" || taskID == "" {
		return domain.Task{}, domain.ErrInvalid
	}
	return s.repository.Get(ctx, ownerID, taskID)
}

func (s *Service) List(ctx context.Context, ownerID, projectID, cursor string, limit int) ([]domain.Task, error) {
	if ownerID == "" {
		return nil, domain.ErrInvalid
	}
	if limit <= 0 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}
	return s.repository.List(ctx, ownerID, projectID, cursor, limit)
}

func (s *Service) Cancel(ctx context.Context, ownerID, taskID, reason string) (domain.Task, *domain.Event, error) {
	if ownerID == "" || taskID == "" {
		return domain.Task{}, nil, domain.ErrInvalid
	}
	return s.repository.Cancel(ctx, ownerID, taskID, reason, s.now())
}

func (s *Service) Events(ctx context.Context, ownerID, taskID string, after int64, limit int) ([]domain.Event, error) {
	if _, err := s.Get(ctx, ownerID, taskID); err != nil {
		return nil, err
	}
	return s.repository.ListEvents(ctx, ownerID, taskID, after, limit)
}

func (s *Service) Claim(ctx context.Context, workerID string, duration time.Duration) (*domain.Lease, error) {
	if workerID == "" || duration < time.Second || duration > 10*time.Minute {
		return nil, domain.ErrInvalid
	}
	return s.repository.Claim(ctx, workerID, duration, s.ids.New(), s.now())
}

func (s *Service) Renew(ctx context.Context, leaseID, workerID string, duration time.Duration) (time.Time, bool, error) {
	return s.repository.Renew(ctx, leaseID, workerID, duration, s.now())
}

func (s *Service) AppendEvent(ctx context.Context, leaseID, workerID, eventType string, payload json.RawMessage, state domain.State, providerID, runID string) (domain.Event, error) {
	event := domain.Event{ID: s.ids.New(), EventType: eventType, Payload: payload, OccurredAt: s.now()}
	return s.repository.AppendEvent(ctx, leaseID, workerID, event, state, providerID, runID, s.now())
}

func (s *Service) Finish(ctx context.Context, leaseID, workerID string) error {
	return s.repository.FinishLease(ctx, leaseID, workerID, s.now())
}
