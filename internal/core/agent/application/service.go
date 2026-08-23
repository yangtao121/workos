package application

import (
	"context"
	"encoding/json"
	"time"

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
