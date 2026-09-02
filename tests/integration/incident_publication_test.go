//go:build integration

package integration_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	notificationpostgres "github.com/yangtao121/workos/internal/core/notification/adapters/postgres"
	notificationapp "github.com/yangtao121/workos/internal/core/notification/application"
	notificationdomain "github.com/yangtao121/workos/internal/core/notification/domain"
	notificationports "github.com/yangtao121/workos/internal/core/notification/ports"
	"github.com/yangtao121/workos/internal/platform/ids"
	reliabilitypostgres "github.com/yangtao121/workos/internal/reliability/adapters/postgres"
	reliabilityapp "github.com/yangtao121/workos/internal/reliability/application"
	reliabilitydomain "github.com/yangtao121/workos/internal/reliability/domain"
)

// directIncidentSource drives the real reliability claim/complete service
// in-process. The full cross-process HTTP chain is covered by the dedicated
// incident notification gate; this proves the durable software semantics.
type directIncidentSource struct {
	service *reliabilityapp.PublicationService
}

func (s directIncidentSource) ClaimIncidentPublications(ctx context.Context, workerID string, maxBatch int32, leaseSeconds int32) ([]notificationapp.IncidentPublication, error) {
	claimed, err := s.service.Claim(ctx, reliabilityapp.ClaimInput{
		WorkerID: workerID, MaxBatch: maxBatch, LeaseSeconds: leaseSeconds,
	})
	if err != nil {
		return nil, err
	}
	publications := make([]notificationapp.IncidentPublication, 0, len(claimed))
	for _, item := range claimed {
		publications = append(publications, notificationapp.IncidentPublication{
			PublicationID: item.Publication.ID,
			IncidentID:    item.Publication.IncidentID,
			OwnerUserID:   item.Publication.OwnerUserID,
			ProjectID:     item.Publication.ProjectID,
			Severity:      item.Publication.Severity,
			ActionOutcome: item.Publication.ActionOutcome,
			Digest:        item.Publication.Digest,
			OccurredAt:    item.Publication.OccurredAt,
			LeaseToken:    item.LeaseToken,
		})
	}
	return publications, nil
}

func (s directIncidentSource) CompleteIncidentPublications(ctx context.Context, workerID, leaseToken string, publicationIDs []string) error {
	_, err := s.service.Complete(ctx, reliabilityapp.CompleteInput{
		WorkerID: workerID, LeaseToken: leaseToken, PublicationIDs: publicationIDs,
	})
	return err
}

// fakeDigest builds a syntactically valid unique digest for fixture rows.
func fakeDigest(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// seedPublicationIncident inserts one incident through the real repository,
// so the notification publication joins the incident transaction.
func seedPublicationIncident(t *testing.T, pool *pgxpool.Pool, repo *reliabilitypostgres.Repository, owner, projectID string, suffix string) reliabilitydomain.Incident {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	incident := reliabilitydomain.Incident{
		ID: ids.UUIDv7{}.New(), OwnerUserID: owner, ProjectID: projectID,
		AppInstanceID: ids.UUIDv7{}.New(), AppID: "fixture-app", WorkloadID: ids.UUIDv7{}.New(),
		WorkloadGeneration: 1, Violation: reliabilitydomain.ViolationUnexpectedExit,
		Summary:          "Workload exited unexpectedly",
		OccurrenceDigest: fakeDigest("occurrence-" + suffix + ids.UUIDv7{}.New()),
		EvidenceDigest:   fakeDigest("evidence-" + ids.UUIDv7{}.New()),
		State:            reliabilitydomain.StateOpen, RestartOutcome: reliabilitydomain.OutcomePending,
		Revision:  1,
		CreatedAt: time.Now().UTC().Truncate(time.Microsecond), UpdatedAt: time.Now().UTC().Truncate(time.Microsecond),
	}
	created, err := repo.CreateIncident(ctx, incident)
	if err != nil || !created {
		t.Fatalf("create incident: created=%v err=%v", created, err)
	}
	return incident
}

// TestIncidentPublicationClaimApplyComplete proves the Reliability→Core
// at-least-once chain over real PostgreSQL: the publication joins the
// incident transaction, claims are leased and exclusive, the Core apply
// projects exactly one notification, and a lost completion replays as a
// receipt no-op without a second notification or change.
func TestIncidentPublicationClaimApplyComplete(t *testing.T) {
	service, _, pool, owner := notificationFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	reliabilityRepo, err := reliabilitypostgres.New(pool)
	if err != nil {
		t.Fatal(err)
	}
	publicationService, err := reliabilityapp.NewPublicationService(reliabilityRepo)
	if err != nil {
		t.Fatal(err)
	}
	projectID := ids.UUIDv7{}.New()
	if _, err := pool.Exec(ctx,
		`INSERT INTO workos_core.projects (id, owner_user_id, idempotency_key, name, knowledge_collection_id, artifact_collection_id, created_at, updated_at)
		 VALUES ($1, $2, 'incident-publication-project', 'Incident Publication', $3, $4, now(), now())`,
		projectID, owner, ids.UUIDv7{}.New(), ids.UUIDv7{}.New()); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	incident := seedPublicationIncident(t, pool, reliabilityRepo, owner, projectID, "")

	// Claim exclusivity: a live lease blocks a second consumer.
	first, err := publicationService.Claim(ctx, reliabilityapp.ClaimInput{WorkerID: "core-consumer-a", MaxBatch: 16, LeaseSeconds: 60})
	if err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if len(first) != 1 || first[0].Publication.IncidentID != incident.ID {
		t.Fatalf("unexpected first claim: %#v", first)
	}
	blocked, err := publicationService.Claim(ctx, reliabilityapp.ClaimInput{WorkerID: "core-consumer-b", MaxBatch: 16, LeaseSeconds: 60})
	if err != nil {
		t.Fatal(err)
	}
	if len(blocked) != 0 {
		t.Fatalf("live lease must block a second claimant: %d", len(blocked))
	}

	// Apply through the real consumer path (claim response arrives, the
	// completion response is "lost" so the claim is never completed).
	consumer, err := notificationapp.NewIncidentConsumer(directIncidentSource{publicationService}, notificationpostgres.New(pool), pool, "core-consumer-a")
	if err != nil {
		t.Fatal(err)
	}
	firstFacts := make([]notificationapp.IncidentPublication, 0, len(first))
	for _, item := range first {
		firstFacts = append(firstFacts, notificationapp.IncidentPublication{
			PublicationID: item.Publication.ID,
			IncidentID:    item.Publication.IncidentID,
			OwnerUserID:   item.Publication.OwnerUserID,
			ProjectID:     item.Publication.ProjectID,
			Severity:      item.Publication.Severity,
			ActionOutcome: item.Publication.ActionOutcome,
			Digest:        item.Publication.Digest,
			OccurredAt:    item.Publication.OccurredAt,
			LeaseToken:    item.LeaseToken,
		})
	}
	if err := consumer.ApplyClaims(ctx, firstFacts); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got := countScratchRows(t, pool, `SELECT count(*) FROM workos_core.notifications WHERE kind = 'reliability.incident.opened' AND target_id = $1`, incident.ID); got != 1 {
		t.Fatalf("expected exactly one incident notification, got %d", got)
	}
	changes, err := service.Watch(ctx, owner, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	created := 0
	for _, change := range changes {
		if change.Notification.TargetID == incident.ID && change.ChangeType == notificationdomain.ChangeCreated {
			created++
		}
	}
	if created != 1 {
		t.Fatalf("expected exactly one CREATED change, got %d", created)
	}

	// Simulate complete-response loss: expire the lease, replay as another
	// consumer. The Core receipt makes the apply a no-op and the completion
	// retires the publication; nothing duplicates.
	if _, err := pool.Exec(ctx,
		`UPDATE workos_reliability.notification_publications SET claim_locked_until = now() - interval '1 second' WHERE incident_id = $1`,
		incident.ID); err != nil {
		t.Fatal(err)
	}
	replay, err := notificationapp.NewIncidentConsumer(directIncidentSource{publicationService}, notificationpostgres.New(pool), pool, "core-consumer-replay")
	if err != nil {
		t.Fatal(err)
	}
	if err := replay.Poll(ctx); err != nil {
		t.Fatalf("replay poll: %v", err)
	}
	if got := countScratchRows(t, pool, `SELECT count(*) FROM workos_core.notifications WHERE kind = 'reliability.incident.opened' AND target_id = $1`, incident.ID); got != 1 {
		t.Fatalf("replay duplicated the notification: %d", got)
	}
	pending, err := publicationService.CountPending(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if pending != 0 {
		t.Fatalf("replay must complete the publication: %d pending", pending)
	}
}

// TestIncidentPublicationDigestDriftFailsClosed proves same-source /
// different-digest on replay is contract violation: the replayed apply
// fails closed, the notification set is unchanged, and the claim stays
// incomplete until the honest fact is restored.
func TestIncidentPublicationDigestDriftFailsClosed(t *testing.T) {
	_, _, pool, owner := notificationFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	reliabilityRepo, err := reliabilitypostgres.New(pool)
	if err != nil {
		t.Fatal(err)
	}
	publicationService, err := reliabilityapp.NewPublicationService(reliabilityRepo)
	if err != nil {
		t.Fatal(err)
	}
	projectID := ids.UUIDv7{}.New()
	if _, err := pool.Exec(ctx,
		`INSERT INTO workos_core.projects (id, owner_user_id, idempotency_key, name, knowledge_collection_id, artifact_collection_id, created_at, updated_at)
		 VALUES ($1, $2, 'incident-drift-project', 'Incident Drift', $3, $4, now(), now())`,
		projectID, owner, ids.UUIDv7{}.New(), ids.UUIDv7{}.New()); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	incident := seedPublicationIncident(t, pool, reliabilityRepo, owner, projectID, "drift")

	// First honest apply: the Core receipt records the honest digest, but
	// the completion response is "lost", so the publication stays pending.
	honest, err := notificationapp.NewIncidentConsumer(directIncidentSource{publicationService}, notificationpostgres.New(pool), pool, "core-consumer-drift")
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := honest.ClaimBatch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := honest.ApplyClaims(ctx, claimed); err != nil {
		t.Fatalf("honest apply: %v", err)
	}
	if got := countScratchRows(t, pool, `SELECT count(*) FROM workos_core.notifications WHERE target_id = $1`, incident.ID); got != 1 {
		t.Fatalf("expected exactly one notification: %d", got)
	}

	// Tamper with the durable publication fact, expire the lease, and
	// replay: the receipt detects same-source/different-digest.
	if _, err := pool.Exec(ctx,
		`UPDATE workos_reliability.notification_publications SET digest = $2 WHERE incident_id = $1`,
		incident.ID, fakeDigest("tampered-"+incident.ID)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE workos_reliability.notification_publications SET claim_locked_until = now() - interval '1 second' WHERE incident_id = $1`,
		incident.ID); err != nil {
		t.Fatal(err)
	}
	driftConsumer, err := notificationapp.NewIncidentConsumer(directIncidentSource{publicationService}, notificationpostgres.New(pool), pool, "core-consumer-tamper")
	if err != nil {
		t.Fatal(err)
	}
	err = driftConsumer.Poll(ctx)
	if !errors.Is(err, notificationports.ErrSourceDigestDrift) {
		t.Fatalf("drifted replay must fail closed with digest drift, got %v", err)
	}
	if got := countScratchRows(t, pool, `SELECT count(*) FROM workos_core.notifications WHERE target_id = $1`, incident.ID); got != 1 {
		t.Fatalf("drifted replay must not project a second notification: %d", got)
	}
	pending, err := publicationService.CountPending(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if pending != 1 {
		t.Fatalf("drifted claim must stay incomplete: %d pending", pending)
	}

	// Restoring the honest fact converges: receipt no-op, completion lands.
	if _, err := pool.Exec(ctx,
		`UPDATE workos_reliability.notification_publications SET digest = $2, claim_locked_until = now() - interval '1 second' WHERE incident_id = $1`,
		incident.ID, reliabilitydomain.IncidentNotificationDigest(incident.ID, "critical", "pending", incident.CreatedAt)); err != nil {
		t.Fatal(err)
	}
	if err := driftConsumer.Poll(ctx); err != nil {
		t.Fatalf("restored replay: %v", err)
	}
	if got := countScratchRows(t, pool, `SELECT count(*) FROM workos_core.notifications WHERE target_id = $1`, incident.ID); got != 1 {
		t.Fatalf("restored replay duplicated the notification: %d", got)
	}
	pending, err = publicationService.CountPending(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if pending != 0 {
		t.Fatalf("restored replay must complete: %d pending", pending)
	}
}
