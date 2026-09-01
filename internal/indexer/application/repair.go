// Owner-triggered repair/reindex jobs (IndexContext, ADR-0013): durable,
// idempotent jobs that re-project exact review artifacts from the same Core
// authority. This is a repair path only — never a second ingestion entry
// point for arbitrary text: every source is digest-pinned and re-resolved
// from Core before any effect.
package application

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/yangtao121/workos/internal/indexer/domain"
	"github.com/yangtao121/workos/internal/indexer/ports"
)

// MaxJobSources caps one repair job (ADR-0013: at most 32 typed refs).
const MaxJobSources = 32

var ErrJobConflict = errors.New("repair job idempotency key was already used for a different request")

// RepairJobStore is the durable job fact owner.
type RepairJobStore interface {
	// CreateJob adjudicates the idempotency key and persists job + sources +
	// the versioned first-response snapshot in one transaction. It returns
	// the stored job view and whether this call created it.
	CreateJob(ctx context.Context, command RepairJobCommand) (JobView, bool, error)
	// NextRunnableJob claims one pending/running job for execution
	// (database-arbitrated, bounded to this process).
	NextRunnableJob(ctx context.Context, now time.Time) (JobView, []JobSourceView, bool, error)
	// RecordSourceOutcome persists one source outcome and advances counters.
	RecordSourceOutcome(ctx context.Context, jobID, artifactID, state, outcome string, now time.Time) error
	// FinishJob transitions the job to a terminal state.
	FinishJob(ctx context.Context, jobID, state, failureCategory string, now time.Time) error
	// GetJobView reads one owner-scoped job projection.
	GetJobView(ctx context.Context, ownerUserID, jobID string) (JobView, []JobSourceView, error)
}

// RepairSourceRef is one typed artifact.review.v1 ref.
type RepairSourceRef struct {
	ArtifactID string
	Digest     string
}

// RepairJobCommand is one adjudicated create command.
type RepairJobCommand struct {
	OwnerUserID    string
	ProjectID      string
	IdempotencyKey string
	RequestDigest  string
	Sources        []RepairSourceRef
	Now            time.Time
}

// JobView is the durable job projection.
type JobView struct {
	ID               string
	OwnerUserID      string
	ProjectID        string
	State            string
	FailureCategory  string
	TotalSources     int
	CompletedSources int
	FailedSources    int
	CreatedAt        time.Time
	UpdatedAt        time.Time
	// FirstResponse is the versioned snapshot replayed for the same key.
	FirstResponse []byte
}

// JobSourceView is one job source projection.
type JobSourceView struct {
	ArtifactID string
	Digest     string
	State      string
	Outcome    string
}

// RepairService creates and executes repair jobs.
type RepairService struct {
	store RepairJobStore
	feed  ports.CoreFeedClient
	proj  ports.ProjectionRepository
	ids   idsGenerator
	now   func() time.Time
}

type idsGenerator interface{ New() string }

func NewRepairService(store RepairJobStore, feed ports.CoreFeedClient, projection ports.ProjectionRepository, generator idsGenerator) (*RepairService, error) {
	if store == nil || feed == nil || projection == nil || generator == nil {
		return nil, errors.New("repair service requires job store, feed client, projection, and ids")
	}
	return &RepairService{store: store, feed: feed, proj: projection, ids: generator, now: func() time.Time { return time.Now().UTC() }}, nil
}

// JobRequestInput is one validated-enough IndexContext request.
type JobRequestInput struct {
	OwnerUserID    string
	ProjectID      string
	IdempotencyKey string
	Sources        []RepairSourceRef
}

// RequestDigest derives the canonical job request digest: the versioned
// marker, project, and the ordered typed refs. Owner and key are the mapping
// identity; time and server state are excluded.
func (i JobRequestInput) RequestDigest() string {
	h := sha256.New()
	fmt.Fprintf(h, "workos.index.repair-job.v1\n%s\n", i.ProjectID)
	for _, source := range i.Sources {
		fmt.Fprintf(h, "%s\n%s\n", source.ArtifactID, source.Digest)
	}
	return fmt.Sprintf("sha256:%x", h.Sum(nil))
}

// CreateJob validates the request grammar and adjudicates the durable key.
func (s *RepairService) CreateJob(ctx context.Context, input JobRequestInput) (JobView, bool, error) {
	if !domain.ValidUUID(input.OwnerUserID) || !domain.ValidUUID(input.ProjectID) {
		return JobView{}, false, domain.ErrInvalid
	}
	if len(input.Sources) == 0 || len(input.Sources) > MaxJobSources {
		return JobView{}, false, domain.ErrInvalid
	}
	seen := make(map[string]bool, len(input.Sources))
	for _, source := range input.Sources {
		if !domain.ValidUUID(source.ArtifactID) || !domain.ValidDigest(source.Digest) {
			return JobView{}, false, domain.ErrInvalid
		}
		if seen[source.ArtifactID+source.Digest] {
			return JobView{}, false, domain.ErrInvalid
		}
		seen[source.ArtifactID+source.Digest] = true
	}
	return s.store.CreateJob(ctx, RepairJobCommand{
		OwnerUserID:    input.OwnerUserID,
		ProjectID:      input.ProjectID,
		IdempotencyKey: input.IdempotencyKey,
		RequestDigest:  input.RequestDigest(),
		Sources:        input.Sources,
		Now:            domain.CanonicalUTCTime(s.now()),
	})
}

// ExecuteOne runs one runnable job to completion: every source is
// re-resolved from Core authority (digest-pinned) and re-projected through
// the same apply path as live publications. A crash mid-job leaves the job
// runnable from durable facts; replays of already-applied sources are
// receipt no-ops.
func (s *RepairService) ExecuteOne(ctx context.Context) (bool, error) {
	job, sources, found, err := s.store.NextRunnableJob(ctx, domain.CanonicalUTCTime(s.now()))
	if err != nil || !found {
		return false, err
	}
	failed := 0
	for _, source := range sources {
		if source.State == "completed" || source.State == "skipped" {
			continue
		}
		resolved, err := s.feed.ResolveSourceContent(ctx, job.OwnerUserID, job.ProjectID, source.ArtifactID, source.Digest)
		if err != nil {
			switch {
			case errors.Is(err, ports.ErrCoreUnavailable):
				return true, nil // retry later from the durable job row
			case errors.Is(err, ports.ErrNotFound):
				if err := s.store.RecordSourceOutcome(ctx, job.ID, source.ArtifactID, "skipped", "source-unavailable", domain.CanonicalUTCTime(s.now())); err != nil {
					return true, err
				}
				continue
			default:
				failed++
				_ = s.store.RecordSourceOutcome(ctx, job.ID, source.ArtifactID, "failed", "resolve-failed", domain.CanonicalUTCTime(s.now()))
				continue
			}
		}
		applySource := ports.ResolvedSource{
			Verdict:       "resolved",
			OwnerUserID:   resolved.OwnerUserID,
			ProjectID:     resolved.ProjectID,
			ArtifactID:    resolved.ArtifactID,
			SourceTaskID:  resolved.SourceTaskID,
			ArtifactType:  resolved.ArtifactType,
			Digest:        resolved.Digest,
			Title:         resolved.Title,
			Content:       resolved.Content,
			CreatedAt:     resolved.CreatedAt,
			PublicationID: s.ids.New(),
			OccurredAt:    domain.CanonicalUTCTime(s.now()),
		}
		if err := s.proj.ApplyResolvedSource(ctx, applySource, domain.OutcomeApplied, jobRequestDigest(job.ID, source), domain.CanonicalUTCTime(s.now())); err != nil {
			failed++
			_ = s.store.RecordSourceOutcome(ctx, job.ID, source.ArtifactID, "failed", "apply-failed", domain.CanonicalUTCTime(s.now()))
			continue
		}
		if err := s.store.RecordSourceOutcome(ctx, job.ID, source.ArtifactID, "completed", "", domain.CanonicalUTCTime(s.now())); err != nil {
			return true, err
		}
	}
	terminal, category := "completed", ""
	if failed == len(sources) {
		terminal, category = "failed", "all-sources-failed"
	}
	if err := s.store.FinishJob(ctx, job.ID, terminal, category, domain.CanonicalUTCTime(s.now())); err != nil {
		return true, err
	}
	return true, nil
}

func jobRequestDigest(jobID string, source JobSourceView) string {
	h := sha256.New()
	fmt.Fprintf(h, "workos.index.repair-apply.v1\n%s\n%s\n%s\n", jobID, source.ArtifactID, source.Digest)
	return fmt.Sprintf("sha256:%x", h.Sum(nil))
}

// jobSnapshotJSON renders the versioned first-response snapshot.
func jobSnapshotJSON(view JobView, sources []JobSourceView) ([]byte, error) {
	return json.Marshal(struct {
		Version int             `json:"result_version"`
		Job     JobView         `json:"job"`
		Sources []JobSourceView `json:"sources"`
	}{Version: 1, Job: view, Sources: sources})
}

// GetJob reads one owner-scoped job view through the durable store.
func (s *RepairService) GetJob(ctx context.Context, ownerUserID, jobID string) (JobView, []JobSourceView, error) {
	return s.store.GetJobView(ctx, ownerUserID, jobID)
}
