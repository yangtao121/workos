// Core-side consumption of reliability incident notification publications
// (ADR-0014): the at-least-once projection loop. Core claims a leased
// batch from the reliability-host private source, applies each publication
// inside one Core transaction (source receipt + notification + CREATED
// change), and only then completes the claim. Lost completions, lease
// expiry, concurrent consumers, and restarts replay safely because the
// receipt makes the second projection a physical no-op.
package application

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/yangtao121/workos/internal/core/notification/domain"
	"github.com/yangtao121/workos/internal/core/notification/ports"
)

// IncidentPublication is one claimed publication fact.
type IncidentPublication struct {
	PublicationID string
	IncidentID    string
	OwnerUserID   string
	ProjectID     string
	Severity      string // info | warning | critical
	ActionOutcome string // pending | restarted | stopped | failed
	Digest        string
	OccurredAt    time.Time
	LeaseToken    string
}

// IncidentPublicationSource is the private reliability-host claim/complete
// surface, adapted by the composition root.
type IncidentPublicationSource interface {
	ClaimIncidentPublications(ctx context.Context, workerID string, maxBatch int32, leaseSeconds int32) ([]IncidentPublication, error)
	CompleteIncidentPublications(ctx context.Context, workerID, leaseToken string, publicationIDs []string) error
}

// Consumer loop bounds.
const (
	IncidentConsumerBatch        = 16
	IncidentConsumerLeaseSeconds = 60
	IncidentConsumerPollInterval = 2 * time.Second
)

// IncidentConsumer drives the at-least-once projection.
type IncidentConsumer struct {
	source   IncidentPublicationSource
	store    ports.NotificationStore
	pool     TxSource
	workerID string
}

func NewIncidentConsumer(source IncidentPublicationSource, store ports.NotificationStore, pool TxSource, workerID string) (*IncidentConsumer, error) {
	if source == nil || store == nil || pool == nil {
		return nil, errors.New("incident consumer requires source, store, and tx source")
	}
	if !domain.ValidWorkerIdentity(workerID) {
		return nil, errors.New("incident consumer requires a bounded worker identity")
	}
	return &IncidentConsumer{source: source, store: store, pool: pool, workerID: workerID}, nil
}

// Poll runs one claim/apply/complete cycle. Transient failures return an
// error and stay retryable; digest drift and stored corruption are
// observable terminal failures for that publication only.
func (c *IncidentConsumer) Poll(ctx context.Context) error {
	claimed, err := c.source.ClaimIncidentPublications(ctx, c.workerID, IncidentConsumerBatch, IncidentConsumerLeaseSeconds)
	if err != nil {
		return fmt.Errorf("claim incident publications: %w", err)
	}
	if err := c.ApplyClaims(ctx, claimed); err != nil {
		return err
	}
	return c.CompleteClaims(ctx, claimed)
}

// ClaimBatch exposes one claim round for drivers that separate the claim
// from apply/complete.
func (c *IncidentConsumer) ClaimBatch(ctx context.Context) ([]IncidentPublication, error) {
	return c.source.ClaimIncidentPublications(ctx, c.workerID, IncidentConsumerBatch, IncidentConsumerLeaseSeconds)
}

// ApplyClaims projects a batch of claimed publications inside their own Core
// transactions. Drivers that must model a lost completion response call
// ApplyClaims and CompleteClaims separately; the production Poll path
// always applies and then completes.
func (c *IncidentConsumer) ApplyClaims(ctx context.Context, claimed []IncidentPublication) error {
	for _, publication := range claimed {
		if err := c.apply(ctx, publication); err != nil {
			return err
		}
	}
	return nil
}

// CompleteClaims completes previously claimed publications against the
// reliability source. Stale claims ack false; the Core receipt makes the
// inevitable replay a no-op.
func (c *IncidentConsumer) CompleteClaims(ctx context.Context, claimed []IncidentPublication) error {
	for _, publication := range claimed {
		if err := c.source.CompleteIncidentPublications(ctx, c.workerID, publication.LeaseToken, []string{publication.PublicationID}); err != nil {
			return fmt.Errorf("complete incident publication: %w", err)
		}
	}
	return nil
}

// apply projects one publication inside one Core transaction. The receipt
// arbitration makes replays and concurrent consumers no-ops; a same-source
// different-digest replay is contract violation, never an update.
func (c *IncidentConsumer) apply(ctx context.Context, publication IncidentPublication) error {
	notification, err := domain.PrepareIncidentPublication(domain.IncidentPublicationFact{
		OwnerUserID: publication.OwnerUserID, ProjectID: publication.ProjectID,
		IncidentID: publication.IncidentID, Severity: publication.Severity,
		ActionOutcome: publication.ActionOutcome, Digest: publication.Digest,
		SourceID: publication.PublicationID,
	}, publication.OccurredAt)
	if err != nil {
		return err
	}
	tx, err := c.pool.Begin(ctx)
	if err != nil {
		return storeFailure("begin incident notification apply", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, _, err := c.store.AppendTx(ctx, tx, notification); err != nil {
		// Contract violation on this publication is terminal for the batch:
		// the loop surfaces it instead of silently completing the claim.
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return storeFailure("commit incident notification apply", err)
	}
	return nil
}

// Run polls until the context is cancelled. Every failure is logged and
// retryable; correctness never relies on the loop's cadence.
func (c *IncidentConsumer) Run(ctx context.Context, logger *slog.Logger) {
	ticker := time.NewTicker(IncidentConsumerPollInterval)
	defer ticker.Stop()
	for {
		if err := c.Poll(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			logger.Warn("incident notification consumer cycle failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
