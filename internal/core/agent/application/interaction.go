package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/yangtao121/workos/internal/core/agent/domain"
	"github.com/yangtao121/workos/internal/core/agent/ports"
	"github.com/yangtao121/workos/internal/platform/ids"
	"time"
)

type InteractionService struct {
	store     ports.InteractionStore
	authority ports.InteractionAuthority
	ids       ids.Generator
}

func NewInteractionService(store ports.InteractionStore, authority ports.InteractionAuthority, generator ids.Generator) *InteractionService {
	return &InteractionService{store, authority, generator}
}
func validateQuestions(questions []domain.ExecutionQuestion) error {
	raw, err := json.Marshal(questions)
	if err != nil || len(raw) > 32768 || len(questions) == 0 || len(questions) > 3 {
		return domain.ErrInvalid
	}
	seen := map[string]bool{}
	for _, q := range questions {
		if q.ID == "" || len(q.ID) > 128 || seen[q.ID] || q.Text == "" || len(q.Text) > 4096 || len(q.Detail) > 16384 || len(q.Choices) > 8 {
			return domain.ErrInvalid
		}
		seen[q.ID] = true
		labels := map[string]bool{}
		for _, choice := range q.Choices {
			if choice.Label == "" || len(choice.Label) > 256 || len(choice.Description) > 1024 || labels[choice.Label] {
				return domain.ErrInvalid
			}
			labels[choice.Label] = true
		}
	}
	return nil
}

// Ask persists before waiting and never holds a database transaction while
// the user thinks. Lease, cancellation and workspace access are rechecked.
func (s *InteractionService) Ask(ctx context.Context, owner, project, task, lease, worker, key string, questions []domain.ExecutionQuestion) (domain.ExecutionInteraction, error) {
	if err := validateQuestions(questions); err != nil {
		return domain.ExecutionInteraction{}, err
	}
	if !domain.ValidAppClientIdempotencyKey(key) || s.authority == nil {
		return domain.ExecutionInteraction{}, domain.ErrInvalid
	}
	if err := s.authority.ValidateInteraction(ctx, lease, worker); err != nil {
		return domain.ExecutionInteraction{}, err
	}
	now := time.Now().UTC()
	record, err := s.store.CreateInteraction(ctx, domain.ExecutionInteraction{ID: s.ids.New(), OwnerUserID: owner, ProjectID: project, TaskID: task, LeaseID: lease, WorkerID: worker, RequestKey: key, Questions: questions, State: "pending", CreatedAt: now, ExpiresAt: now.Add(2 * time.Minute)})
	if err != nil {
		return record, err
	}
	timer := time.NewTicker(250 * time.Millisecond)
	defer timer.Stop()
	for {
		if err := s.authority.ValidateInteraction(ctx, lease, worker); err != nil {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
			_ = s.store.ExpireInteraction(cleanup, owner, record.ID)
			cancel()
			return record, err
		}
		record, err = s.store.GetInteraction(ctx, owner, record.ID)
		if err != nil {
			return record, err
		}
		if !time.Now().Before(record.ExpiresAt) {
			_ = s.store.ExpireInteraction(ctx, owner, record.ID)
			return record, domain.ErrApprovalNotPending
		}
		if record.State != "pending" {
			return record, nil
		}
		select {
		case <-ctx.Done():
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
			_ = s.store.ExpireInteraction(cleanup, owner, record.ID)
			cancel()
			return record, ctx.Err()
		case <-timer.C:
		}
	}
}
func (s *InteractionService) List(ctx context.Context, owner, task string) ([]domain.ExecutionInteraction, error) {
	if !domain.ValidAppTaskUUID(task) {
		return nil, domain.ErrInvalid
	}
	records, err := s.store.ListInteractions(ctx, owner, task)
	if err != nil {
		return nil, err
	}
	for i, r := range records {
		if r.State == "pending" && (!time.Now().Before(r.ExpiresAt) || s.authority.ValidateInteraction(ctx, r.LeaseID, r.WorkerID) != nil) {
			if err := s.store.ExpireInteraction(ctx, owner, r.ID); err != nil {
				return nil, err
			}
			records[i].State = "expired"
		}
	}
	return records, nil
}
func (s *InteractionService) Respond(ctx context.Context, owner, id, key string, reject bool, answers []domain.ExecutionAnswer) (domain.ExecutionInteraction, error) {
	if !domain.ValidAppTaskUUID(id) || !domain.ValidAppClientIdempotencyKey(key) {
		return domain.ExecutionInteraction{}, domain.ErrInvalid
	}
	r, err := s.store.GetInteraction(ctx, owner, id)
	if err != nil {
		return r, err
	}
	if r.State == "pending" {
		if s.authority == nil || s.authority.ValidateInteraction(ctx, r.LeaseID, r.WorkerID) != nil {
			_ = s.store.ExpireInteraction(ctx, owner, id)
			return r, domain.ErrApprovalNotPending
		}
	}
	if reject {
		if len(answers) != 0 {
			return r, domain.ErrInvalid
		}
		r.State = "rejected"
	} else {
		if len(answers) != len(r.Questions) {
			return r, domain.ErrInvalid
		}
		seen := map[string]bool{}
		for _, answer := range answers {
			if seen[answer.QuestionID] || len(answer.Text) > 8192 {
				return r, domain.ErrInvalid
			}
			seen[answer.QuestionID] = true
			var question *domain.ExecutionQuestion
			for i := range r.Questions {
				if r.Questions[i].ID == answer.QuestionID {
					question = &r.Questions[i]
					break
				}
			}
			if question == nil || (!question.Multiple && len(answer.Selected) > 1) || (len(answer.Selected) == 0 && answer.Text == "") {
				return r, domain.ErrInvalid
			}
			choices := map[string]bool{}
			for _, choice := range question.Choices {
				choices[choice.Label] = true
			}
			selected := map[string]bool{}
			for _, label := range answer.Selected {
				if !choices[label] || selected[label] {
					return r, domain.ErrInvalid
				}
				selected[label] = true
			}
		}
		r.State = "answered"
	}
	r.Answers = answers
	r.DecisionKey = key
	data, err := json.Marshal([]any{r.State, answers})
	if err != nil || len(data) > 32768 {
		return r, domain.ErrInvalid
	}
	sum := sha256.Sum256(data)
	r.DecisionDigest = hex.EncodeToString(sum[:])
	result, err := s.store.DecideInteraction(ctx, r, time.Now().UTC())
	if errors.Is(err, domain.ErrApprovalNotPending) {
		_ = s.store.ExpireInteraction(ctx, owner, id)
	}
	return result, err
}
