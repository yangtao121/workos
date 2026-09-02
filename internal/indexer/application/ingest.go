// Ingestion application service: the at-least-once worker use cases over the
// Core feed and the local projection. The fixed order (ADR-0013 §4): claim
// from Core, resolve the canonical source, commit the local effect + receipt
// + cursor in one transaction, and only then complete Core. A lost complete
// response replays safely — the durable receipt makes it a no-op.
package application

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"github.com/yangtao121/workos/internal/indexer/domain"
	"github.com/yangtao121/workos/internal/indexer/ports"
)

var ErrInvalidIngestion = errors.New("ingestion request is invalid")

// IngestionService composes the Core feed client with the projection.
type IngestionService struct {
	feed     ports.CoreFeedClient
	proj     ports.ProjectionRepository
	now      func() time.Time
	workerID string
}

func NewIngestionService(feed ports.CoreFeedClient, projection ports.ProjectionRepository, workerID string) (*IngestionService, error) {
	if feed == nil || projection == nil || !domain.ValidWorkerID(workerID) {
		return nil, errors.New("ingestion service requires feed client, projection, and a valid worker id")
	}
	return &IngestionService{feed: feed, proj: projection, workerID: workerID, now: func() time.Time { return time.Now().UTC() }}, nil
}

// PublicationOutcome is the result of consuming one claimed publication.
type PublicationOutcome struct {
	PublicationID string
	Acked         bool
	Outcome       string
	// Retryable marks transient failures: the claim stays and the worker
	// retries with backoff instead of completing.
	Retryable bool
}

// IngestOne consumes exactly one claimed publication through the fixed
// order. It returns the outcome to complete Core with; acked=false means the
// lease was stale and the publication must be re-claimed (the receipt turns
// the inevitable replay into a no-op).
func (s *IngestionService) IngestOne(ctx context.Context, claimed ports.ClaimedPublication) (PublicationOutcome, error) {
	if !domain.ValidUUID(claimed.PublicationID) || claimed.LeaseToken == "" {
		return PublicationOutcome{}, ErrInvalidIngestion
	}
	resolved, err := s.feed.Resolve(ctx, s.workerID, claimed.PublicationID, claimed.LeaseToken)
	if err != nil {
		switch {
		case errors.Is(err, ports.ErrCoreUnavailable):
			return PublicationOutcome{PublicationID: claimed.PublicationID, Retryable: true}, err
		case errors.Is(err, ports.ErrLeaseStale):
			return PublicationOutcome{PublicationID: claimed.PublicationID, Outcome: "stale"}, nil
		default:
			return PublicationOutcome{}, err
		}
	}
	if resolved.PublicationID != claimed.PublicationID {
		return PublicationOutcome{}, domain.ErrCorrupt
	}

	outcome, effectErr := s.classify(resolved)
	if effectErr != nil {
		return PublicationOutcome{}, effectErr
	}
	if err := s.proj.ApplyResolvedSource(ctx, resolved, outcome, publicationRequestDigest(resolved), canonical(s.now())); err != nil {
		if errors.Is(err, ports.ErrStoreUnavailable) {
			return PublicationOutcome{PublicationID: claimed.PublicationID, Retryable: true}, err
		}
		return PublicationOutcome{}, err
	}

	acks, err := s.feed.Complete(ctx, s.workerID, []ports.ConsumptionResult{{
		PublicationID: claimed.PublicationID,
		LeaseToken:    claimed.LeaseToken,
		Outcome:       coreOutcome(outcome),
	}})
	if err != nil {
		if errors.Is(err, ports.ErrCoreUnavailable) {
			// The local effect is committed; the Core complete response was
			// lost. The lease will expire and the safe replay will no-op.
			return PublicationOutcome{PublicationID: claimed.PublicationID, Outcome: outcome, Retryable: true}, err
		}
		return PublicationOutcome{}, err
	}
	acked := len(acks) == 1 && acks[0]
	return PublicationOutcome{PublicationID: claimed.PublicationID, Acked: acked, Outcome: outcome}, nil
}

// classify maps a resolved verdict onto the local effect outcome. Content is
// only applied for resolved upserts; every other verdict is a bounded,
// observable effect without content.
func (s *IngestionService) classify(resolved ports.ResolvedSource) (string, error) {
	if !domain.ValidUUID(resolved.PublicationID) || !domain.ValidUUID(resolved.OwnerUserID) ||
		!domain.ValidUUID(resolved.ProjectID) || resolved.OccurredAt.IsZero() {
		return "", domain.ErrCorrupt
	}
	if resolved.Operation != "review-artifact.upsert" && resolved.Operation != "project.tombstone" {
		return "", domain.ErrCorrupt
	}
	switch resolved.Verdict {
	case "resolved":
		if validateResolvedForApply(resolved) != nil {
			return "", domain.ErrCorrupt
		}
		return domain.OutcomeApplied, nil
	case "tombstoned":
		return domain.OutcomeTombstoned, nil
	case "unsupported":
		return domain.OutcomeUnsupported, nil
	case "corrupt":
		return domain.OutcomeCorrupt, nil
	default:
		return "", domain.ErrCorrupt
	}
}

func validateResolvedForApply(resolved ports.ResolvedSource) error {
	if resolved.Verdict != "resolved" || resolved.Operation != "review-artifact.upsert" {
		return domain.ErrCorrupt
	}
	document := domain.Document{
		OwnerUserID: resolved.OwnerUserID, ProjectID: resolved.ProjectID,
		SourceID: resolved.ArtifactID, SourceDigest: resolved.Digest,
		ArtifactType: resolved.ArtifactType, Title: resolved.Title,
		Content: string(resolved.Content), SourceCreatedAt: resolved.CreatedAt,
		LastPublication: resolved.PublicationID, IndexedAt: resolved.OccurredAt,
	}
	return domain.ValidStoredDocument(document)
}

func coreOutcome(local string) string {
	switch local {
	case domain.OutcomeApplied:
		return "completed"
	case domain.OutcomeTombstoned:
		return "tombstoned"
	case domain.OutcomeUnsupported:
		return "unsupported"
	case domain.OutcomeCorrupt:
		return "corrupt"
	default:
		return "completed"
	}
}

// publicationRequestDigest derives the canonical request digest bound into
// the receipt: the publication identity, operation, and the exact resolved
// source facts. A replay resolves to the same facts and the same digest.
func publicationRequestDigest(resolved ports.ResolvedSource) string {
	h := sha256.New()
	fmt.Fprintf(h, "workos.index.receipt.v1\n%s\n%s\n%s\n%s\n%s\n%s\n%s\n%s\n%d\n",
		resolved.PublicationID, resolved.Operation, resolved.OwnerUserID, resolved.ProjectID,
		resolved.ArtifactID, resolved.ArtifactType, resolved.Digest,
		resolved.Title, resolved.CreatedAt.UnixMicro())
	return fmt.Sprintf("sha256:%x", h.Sum(nil))
}

// Freshness reads the bounded freshness projection through the projection.
func (s *IngestionService) Freshness(ctx context.Context) (domain.Freshness, error) {
	pending, err := s.feed.CountPending(ctx)
	if err != nil {
		if errors.Is(err, ports.ErrCoreUnavailable) {
			return domain.Freshness{}, err
		}
		return domain.Freshness{}, err
	}
	return s.proj.Freshness(ctx, pending)
}

func canonical(value time.Time) time.Time {
	return domain.CanonicalUTCTime(value)
}

func timeNow() time.Time { return time.Now().UTC() }

func digestOf(parts ...string) string {
	h := sha256.New()
	for _, part := range parts {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	return fmt.Sprintf("sha256:%x", h.Sum(nil))
}
