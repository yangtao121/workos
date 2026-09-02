// Reliability-side incident notification publication application service:
// the durable claim/complete authority the Core notification consumer calls
// over the private source RPC. Claims are database-arbitrated leases with
// opaque server-minted tokens; completions record terminal outcomes only
// for live claims. Lost completion responses replay safely because Core
// records its durable receipt before completing (ADR-0014).
package application

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/yangtao121/workos/internal/reliability/domain"
)

var ErrInvalidClaim = errors.New("incident notification publication claim is invalid")

// PublicationService composes the publication claim store.
type PublicationService struct {
	store domain.ClaimSource
	now   func() time.Time
}

func NewPublicationService(store domain.ClaimSource) (*PublicationService, error) {
	if store == nil {
		return nil, errors.New("publication service requires a claim store")
	}
	return &PublicationService{store: store, now: func() time.Time { return time.Now().UTC() }}, nil
}

// ClaimInput is one bounded claim request.
type ClaimInput struct {
	WorkerID     string
	MaxBatch     int32
	LeaseSeconds int32
}

// ClaimedPublication is one lease handed to the Core consumer.
type ClaimedPublication struct {
	Publication domain.IncidentNotificationPublication
	LeaseToken  string
	ExpiresAt   time.Time
}

// Claim leases up to MaxBatch pending publications.
func (s *PublicationService) Claim(ctx context.Context, input ClaimInput) ([]ClaimedPublication, error) {
	if !domain.ValidWorkerID(input.WorkerID) {
		return nil, ErrInvalidClaim
	}
	maxBatch := int(input.MaxBatch)
	if maxBatch <= 0 {
		maxBatch = 1
	}
	if maxBatch > domain.PublicationMaxClaimBatch {
		return nil, ErrInvalidClaim
	}
	lease := domain.ClampPublicationLeaseSeconds(input.LeaseSeconds)
	now := time.Now().UTC().Truncate(time.Microsecond)
	token, err := newClaimToken()
	if err != nil {
		return nil, fmt.Errorf("mint claim token: %w", err)
	}
	claimed, err := s.store.ClaimPendingIncidentPublications(ctx, input.WorkerID, token, now.Add(lease), now, maxBatch)
	if err != nil {
		return nil, err
	}
	out := make([]ClaimedPublication, 0, len(claimed))
	for _, publication := range claimed {
		out = append(out, ClaimedPublication{Publication: publication, LeaseToken: token, ExpiresAt: now.Add(lease)})
	}
	return out, nil
}

// CompleteInput is one bounded batch of locally recorded outcomes; a batch
// is exactly one claim, so one lease token covers it.
type CompleteInput struct {
	WorkerID       string
	LeaseToken     string
	PublicationIDs []string
}

// AckedResult reports whether this worker's claim was still live when the
// outcome was recorded. Stale entries stay false; Core's durable receipt
// turns the inevitable replay into a no-op.
type AckedResult struct {
	PublicationID string
	Acked         bool
}

func (s *PublicationService) Complete(ctx context.Context, input CompleteInput) ([]AckedResult, error) {
	if !domain.ValidWorkerID(input.WorkerID) || input.LeaseToken == "" {
		return nil, ErrInvalidClaim
	}
	if len(input.PublicationIDs) == 0 || len(input.PublicationIDs) > domain.PublicationMaxClaimBatch {
		return nil, ErrInvalidClaim
	}
	for _, id := range input.PublicationIDs {
		if !domain.ValidUUIDv7(id) {
			return nil, ErrInvalidClaim
		}
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	ackedCount, err := s.store.CompleteIncidentPublications(ctx, input.WorkerID, input.LeaseToken, input.PublicationIDs, now)
	if err != nil {
		return nil, err
	}
	results := make([]AckedResult, 0, len(input.PublicationIDs))
	remaining := ackedCount
	for _, id := range input.PublicationIDs {
		acked := false
		if remaining > 0 {
			acked = true
			remaining--
		}
		results = append(results, AckedResult{PublicationID: id, Acked: acked})
	}
	return results, nil
}

// CountPending reports publications still awaiting a terminal outcome.
func (s *PublicationService) CountPending(ctx context.Context) (int64, error) {
	return s.store.CountPendingIncidentPublications(ctx)
}

func newClaimToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
