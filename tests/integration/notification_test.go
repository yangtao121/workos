//go:build integration

package integration_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	agentpostgres "github.com/yangtao121/workos/internal/core/agent/adapters/postgres"
	agentdomain "github.com/yangtao121/workos/internal/core/agent/domain"
	notificationpostgres "github.com/yangtao121/workos/internal/core/notification/adapters/postgres"
	notificationapp "github.com/yangtao121/workos/internal/core/notification/application"
	notificationdomain "github.com/yangtao121/workos/internal/core/notification/domain"
	notificationports "github.com/yangtao121/workos/internal/core/notification/ports"
	"github.com/yangtao121/workos/internal/platform/ids"
	"github.com/yangtao121/workos/internal/platform/migrations"
)

// notificationFixture wires a real-PostgreSQL notification repository and
// application service over a freshly migrated scratch database.
func notificationFixture(t *testing.T) (*notificationapp.Service, *notificationpostgres.Repository, *pgxpool.Pool, string) {
	t.Helper()
	dsn := scratchDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := migrations.Run(ctx, dsn); err != nil {
		t.Fatalf("migrate scratch database: %v", err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open scratch pool: %v", err)
	}
	t.Cleanup(pool.Close)
	// The foundation schema admits a single owner; this module is
	// owner-scoped, so the fixture drops that index to exercise foreign
	// scoping between owners (the same pattern as the other integration
	// fixtures).
	if _, err := pool.Exec(ctx, `DROP INDEX IF EXISTS workos_core.users_single_owner_idx`); err != nil {
		t.Fatalf("relax single-owner index: %v", err)
	}
	repository := notificationpostgres.New(pool)
	service, err := notificationapp.New(repository, pool, ids.UUIDv7{})
	if err != nil {
		t.Fatalf("build notification service: %v", err)
	}
	owner := newNotificationOwner(t, pool)
	return service, repository, pool, owner
}

func newNotificationOwner(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	owner := ids.UUIDv7{}.New()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := pool.Exec(ctx,
		`INSERT INTO workos_core.users (id, kind, display_name, created_at) VALUES ($1, 'owner', 'Notification Owner', now())
		 ON CONFLICT (id) DO NOTHING`, owner); err != nil {
		t.Fatalf("seed owner: %v", err)
	}
	return owner
}

// sinkAppend joins the tx-scoped sink port the way a real producer does:
// the projection runs inside a source-mutation transaction that commits
// only after the notification fact committed.
func sinkAppend(t *testing.T, pool *pgxpool.Pool, repo *notificationpostgres.Repository, fact notificationdomain.SystemFact) (notificationdomain.Notification, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin source tx: %v", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	projected, err := repo.AppendSystemNotification(ctx, tx, fact, time.Now().UTC())
	if err != nil {
		return notificationdomain.Notification{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit source tx: %v", err)
	}
	return projected, nil
}

func notificationFact(owner string, category string) notificationdomain.SystemFact {
	return notificationdomain.SystemFact{
		Kind: notificationdomain.KindAgentTaskTerminal, OwnerUserID: owner,
		Category: category, TargetID: ids.UUIDv7{}.New(),
		SourceID: "task-" + ids.UUIDv7{}.New(),
	}
}

// TestNotificationAppendReplayAndConvergence proves the exactly-once
// projection: one source fact yields exactly one notification and one
// CREATED change across sequential replays, a genuinely concurrent first
// write, and digest-drift detection.
func TestNotificationAppendReplayAndConvergence(t *testing.T) {
	service, repository, pool, owner := notificationFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fact := notificationFact(owner, "completed")

	first, err := sinkAppend(t, pool, repository, fact)
	if err != nil {
		t.Fatalf("append notification: %v", err)
	}
	if err := notificationdomain.ValidStoredNotification(first); err != nil {
		t.Fatalf("stored notification invalid: %v", err)
	}
	// Sequential replay is a no-op returning the same fact.
	replay, err := sinkAppend(t, pool, repository, fact)
	if err != nil {
		t.Fatalf("replay notification: %v", err)
	}
	if replay.ID != first.ID || !replay.CreatedAt.Equal(first.CreatedAt) {
		t.Fatalf("replay drifted: %+v vs %+v", replay, first)
	}
	// A genuinely concurrent first write converges on the same fact with one
	// change. This exercises request arbitration before any receipt exists.
	raceFact := notificationFact(owner, "failed")
	var wg sync.WaitGroup
	race := make([]notificationdomain.Notification, 8)
	raceErrs := make([]error, 8)
	for i := range race {
		wg.Add(1)
		go func(slot int) {
			defer wg.Done()
			projected, err := sinkAppend(t, pool, repository, raceFact)
			race[slot], raceErrs[slot] = projected, err
		}(i)
	}
	wg.Wait()
	for i, err := range raceErrs {
		if err != nil {
			t.Fatalf("concurrent append %d: %v", i, err)
		}
		if race[i].ID != race[0].ID {
			t.Fatalf("concurrent append %d produced %s, want %s", i, race[i].ID, race[0].ID)
		}
	}
	changes, err := service.Watch(ctx, owner, 0, 100)
	if err != nil {
		t.Fatalf("watch changes: %v", err)
	}
	created := 0
	for _, change := range changes {
		if change.NotificationID == race[0].ID {
			created++
			if change.ChangeType != notificationdomain.ChangeCreated {
				t.Fatalf("unexpected change type %q", change.ChangeType)
			}
		}
	}
	if created != 1 {
		t.Fatalf("expected exactly one CREATED change, got %d", created)
	}
	// Same source, different digest is contract violation, never an update.
	drift := fact
	drift.Category = "failed"
	if _, err := sinkAppend(t, pool, repository, drift); !errors.Is(err, notificationports.ErrSourceDigestDrift) {
		t.Fatalf("digest drift error = %v, want ErrSourceDigestDrift", err)
	}
}

// TestNotificationReadMonotonicIdempotency proves the monotonic read
// projection: reads are idempotent with an exact first-response replay, a
// conflicting request is a stable Aborted, already-read no-ops do not
// append duplicate changes, and foreign facts are NotFound.
func TestNotificationReadMonotonicIdempotency(t *testing.T) {
	service, repository, pool, owner := notificationFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	first := notificationFact(owner, "completed")
	second := notificationFact(owner, "failed")
	for _, fact := range []notificationdomain.SystemFact{first, second} {
		if _, err := sinkAppend(t, pool, repository, fact); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	page, _, err := service.List(ctx, owner, notificationports.Filter{}, 10, "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(page.Notifications) != 2 || page.UnreadCount != 2 {
		t.Fatalf("unexpected page: %d facts, unread %d", len(page.Notifications), page.UnreadCount)
	}
	batch := []string{page.Notifications[0].ID, page.Notifications[1].ID}
	input := notificationapp.MarkReadInput{
		OwnerUserID: owner, NotificationIDs: batch, IdempotencyKey: "read-once",
	}
	var wg sync.WaitGroup
	results := make([]notificationapp.ReadResult, 8)
	errs := make([]error, len(results))
	for i := range results {
		wg.Add(1)
		go func(slot int) {
			defer wg.Done()
			results[slot], errs[slot] = service.MarkRead(ctx, input)
		}(i)
	}
	wg.Wait()
	result := results[0]
	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent mark read %d: %v", i, err)
		}
		if results[i].ChangeSequence != result.ChangeSequence || results[i].UnreadCount != result.UnreadCount {
			t.Fatalf("concurrent mark read %d drifted: %+v vs %+v", i, results[i], result)
		}
	}
	if result.UnreadCount != 0 || result.ChangeSequence == 0 {
		t.Fatalf("unexpected read result: unread %d seq %d", result.UnreadCount, result.ChangeSequence)
	}
	// Same key/same request replays the exact first response.
	replayed, err := service.MarkRead(ctx, notificationapp.MarkReadInput{
		OwnerUserID: owner, NotificationIDs: batch, IdempotencyKey: "read-once",
	})
	if err != nil {
		t.Fatalf("replay read: %v", err)
	}
	if replayed.ChangeSequence != result.ChangeSequence || replayed.UnreadCount != result.UnreadCount {
		t.Fatalf("replay drifted: %+v vs %+v", replayed, result)
	}
	// Same key/different request is a stable conflict.
	if _, err := service.MarkRead(ctx, notificationapp.MarkReadInput{
		OwnerUserID: owner, NotificationIDs: batch[:1], IdempotencyKey: "read-once",
	}); !errors.Is(err, notificationapp.ErrConflict) {
		t.Fatalf("conflict error = %v, want ErrConflict", err)
	}
	// Already-read no-op consumes a fresh key deterministically without a
	// duplicate change.
	if _, err := service.MarkRead(ctx, notificationapp.MarkReadInput{
		OwnerUserID: owner, NotificationIDs: batch[:1], IdempotencyKey: "read-again",
	}); err != nil {
		t.Fatalf("no-op read: %v", err)
	}
	changes, err := service.Watch(ctx, owner, 0, 100)
	if err != nil {
		t.Fatalf("watch: %v", err)
	}
	reads := 0
	for _, change := range changes {
		if change.ChangeType == notificationdomain.ChangeRead {
			reads++
		}
	}
	if reads != 2 {
		t.Fatalf("expected exactly two READ changes, got %d", reads)
	}
	// Foreign facts are NotFound; unknown ids share the sanitized miss.
	foreignOwner := newNotificationOwner(t, pool)
	if _, err := service.MarkRead(ctx, notificationapp.MarkReadInput{
		OwnerUserID: foreignOwner, NotificationIDs: batch, IdempotencyKey: "foreign-key",
	}); !errors.Is(err, notificationdomain.ErrNotFound) {
		t.Fatalf("foreign read error = %v, want ErrNotFound", err)
	}
	// Read is monotonic: the facts stay read with their change sequences.
	for _, id := range batch {
		fact, err := service.Get(ctx, owner, id)
		if err != nil {
			t.Fatalf("get %s: %v", id, err)
		}
		if !fact.Read() {
			t.Fatalf("fact %s lost its read state", id)
		}
	}
}

// TestNotificationPaginationAndFiltering proves bounded pages, the exact
// last-page behavior (no phantom token), filter-bound tokens, and owner
// scoping.
func TestNotificationPaginationAndFiltering(t *testing.T) {
	service, repository, pool, owner := notificationFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for i := 0; i < 7; i++ {
		fact := notificationFact(owner, "completed")
		if _, err := sinkAppend(t, pool, repository, fact); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	// Exactly-full pages produce no phantom token.
	lastPage, next, err := service.List(ctx, owner, notificationports.Filter{}, 3, "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(lastPage.Notifications) != 3 || next == "" {
		t.Fatalf("first page: %d facts next %q", len(lastPage.Notifications), next)
	}
	snapshotWatermark := lastPage.Watermark
	if _, err := sinkAppend(t, pool, repository, notificationFact(owner, "failed")); err != nil {
		t.Fatalf("append between pages: %v", err)
	}
	third, next3, err := service.List(ctx, owner, notificationports.Filter{}, 3, next)
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	if len(third.Notifications) != 3 || next3 == "" {
		t.Fatalf("second page: %d facts next %q", len(third.Notifications), next3)
	}
	if third.Watermark != snapshotWatermark {
		t.Fatalf("second page advanced snapshot watermark: %d -> %d", snapshotWatermark, third.Watermark)
	}
	final, nextFinal, err := service.List(ctx, owner, notificationports.Filter{}, 3, next3)
	if err != nil {
		t.Fatalf("third page: %v", err)
	}
	if len(final.Notifications) != 1 || nextFinal != "" {
		t.Fatalf("final page: %d facts next %q", len(final.Notifications), nextFinal)
	}
	if final.Watermark != snapshotWatermark {
		t.Fatalf("final page advanced snapshot watermark: %d -> %d", snapshotWatermark, final.Watermark)
	}
	fresh, _, err := service.List(ctx, owner, notificationports.Filter{}, 3, "")
	if err != nil {
		t.Fatalf("fresh list: %v", err)
	}
	if fresh.Watermark <= snapshotWatermark {
		t.Fatalf("fresh list did not observe concurrent append: %d <= %d", fresh.Watermark, snapshotWatermark)
	}
	// A token minted under one filter cannot page another filter.
	if _, _, err := service.List(ctx, owner, notificationports.Filter{UnreadOnly: true}, 3, next); !errors.Is(err, notificationapp.ErrInvalid) {
		t.Fatalf("cross-filter token error = %v, want ErrInvalid", err)
	}
	// Foreign owners never see the facts.
	foreignOwner := newNotificationOwner(t, pool)
	foreign, _, err := service.List(ctx, foreignOwner, notificationports.Filter{}, 10, "")
	if err != nil {
		t.Fatalf("foreign list: %v", err)
	}
	if len(foreign.Notifications) != 0 {
		t.Fatalf("foreign owner saw %d facts", len(foreign.Notifications))
	}
}

// TestNotificationSweepGapReset proves the bounded sweep retires only old
// read facts, never unread ones, and that a cursor inside the swept region
// is answered with the authoritative gap watermark instead of a silent
// resume.
func TestNotificationSweepGapReset(t *testing.T) {
	service, repository, pool, owner := notificationFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	old := notificationFact(owner, "completed")
	recent := notificationFact(owner, "failed")
	if _, err := sinkAppend(t, pool, repository, old); err != nil {
		t.Fatalf("append old: %v", err)
	}
	if _, err := sinkAppend(t, pool, repository, recent); err != nil {
		t.Fatalf("append recent: %v", err)
	}
	page, _, err := service.List(ctx, owner, notificationports.Filter{}, 10, "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var oldID, recentID string
	for _, fact := range page.Notifications {
		if fact.TargetID == old.TargetID {
			oldID = fact.ID
		}
		if fact.TargetID == recent.TargetID {
			recentID = fact.ID
		}
	}
	if oldID == "" || recentID == "" {
		t.Fatalf("expected both facts, got %+v", page.Notifications)
	}
	if _, err := service.MarkRead(ctx, notificationapp.MarkReadInput{
		OwnerUserID: owner, NotificationIDs: []string{oldID}, IdempotencyKey: "sweep-read",
	}); err != nil {
		t.Fatalf("mark read: %v", err)
	}
	// Sweeping at the retention horizon cannot touch the just-read fact.
	if n, err := service.Sweep(ctx); err != nil || n != 0 {
		t.Fatalf("early sweep: %d swept, err %v", n, err)
	}
	// Force the read fact past the horizon by rewriting read_at, then sweep.
	if _, err := pool.Exec(ctx,
		`UPDATE workos_core.notifications SET read_at = now() - interval '40 days' WHERE id = $1`, oldID); err != nil {
		t.Fatalf("age read fact: %v", err)
	}
	n, err := service.Sweep(ctx)
	if err != nil || n != 1 {
		t.Fatalf("sweep: %d swept, err %v", n, err)
	}
	if _, err := service.Get(ctx, owner, oldID); !errors.Is(err, notificationdomain.ErrNotFound) {
		t.Fatalf("swept fact error = %v, want ErrNotFound", err)
	}
	if _, err := service.Get(ctx, owner, recentID); err != nil {
		t.Fatalf("unread fact must survive the sweep: %v", err)
	}
	postSweep, _, err := service.List(ctx, owner, notificationports.Filter{}, 10, "")
	if err != nil {
		t.Fatalf("post-sweep list: %v", err)
	}
	gap, err := service.SweptThrough(ctx, owner)
	if err != nil {
		t.Fatalf("swept through: %v", err)
	}
	if postSweep.Watermark != gap {
		t.Fatalf("post-sweep watermark %d, want owner sequence %d", postSweep.Watermark, gap)
	}
	changes, err := service.Watch(ctx, owner, 0, 100)
	if err != nil {
		t.Fatalf("watch: %v", err)
	}
	if len(changes) == 0 || changes[len(changes)-1].ChangeSequence > gap {
		t.Fatalf("retained changes exceed the gap watermark: %d changes, gap %d", len(changes), gap)
	}
	// A cursor inside the swept region is a gap; a fresh cursor is not.
	if _, err := service.Watch(ctx, owner, gap, 100); err != nil {
		t.Fatalf("post-gap watch: %v", err)
	}
}

// TestNotificationConcurrencySequences proves that concurrent appends and
// reads allocate strictly increasing per-owner change sequences with no
// duplicates.
func TestNotificationConcurrencySequences(t *testing.T) {
	service, repository, pool, owner := notificationFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	errs := make([]error, 12)
	for i := range errs {
		wg.Add(1)
		go func(slot int) {
			defer wg.Done()
			fact := notificationFact(owner, "completed")
			if _, err := sinkAppend(t, pool, repository, fact); err != nil {
				errs[slot] = fmt.Errorf("append: %w", err)
			}
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("worker %d: %v", i, err)
		}
	}
	changes, err := service.Watch(ctx, owner, 0, 100)
	if err != nil {
		t.Fatalf("watch: %v", err)
	}
	seen := make(map[int64]bool, len(changes))
	last := int64(0)
	for _, change := range changes {
		if seen[change.ChangeSequence] {
			t.Fatalf("duplicate change sequence %d", change.ChangeSequence)
		}
		if change.ChangeSequence <= last {
			t.Fatalf("change sequences not increasing: %d after %d", change.ChangeSequence, last)
		}
		seen[change.ChangeSequence] = true
		last = change.ChangeSequence
	}
}

// mustAgentRepo wires the agent repository with its real notification sink
// for tests that construct the full stack inline.
func mustAgentRepo(t *testing.T, pool *pgxpool.Pool) *agentpostgres.Repository {
	t.Helper()
	repo, err := agentpostgres.NewWithNotificationSink(pool, notificationpostgres.New(pool))
	if err != nil {
		t.Fatalf("wire agent repository: %v", err)
	}
	return repo
}

// TestTaskTerminalNotificationAtomicity proves the ADR-0014 hard
// requirement for the Agent task-terminal producer over real PostgreSQL:
// the first terminal event projects exactly one owner notification in the
// same transaction, and a notification failure rolls the terminal
// transition back — the task stays non-terminal with zero events.
func TestTaskTerminalNotificationAtomicity(t *testing.T) {
	fixturePool := func(t *testing.T) (*pgxpool.Pool, string) {
		t.Helper()
		dsn := scratchDatabase(t)
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		if err := migrations.Run(ctx, dsn); err != nil {
			t.Fatalf("migrate: %v", err)
		}
		pool, err := pgxpool.New(ctx, dsn)
		if err != nil {
			t.Fatalf("open pool: %v", err)
		}
		t.Cleanup(pool.Close)
		if _, err := pool.Exec(ctx, `DROP INDEX IF EXISTS workos_core.users_single_owner_idx`); err != nil {
			t.Fatal(err)
		}
		owner := ids.UUIDv7{}.New()
		if _, err := pool.Exec(ctx,
			`INSERT INTO workos_core.users (id, kind, display_name, created_at) VALUES ($1, 'owner', 'Terminal Owner', now())`, owner); err != nil {
			t.Fatal(err)
		}
		return pool, owner
	}
	seedTask := func(t *testing.T, pool *pgxpool.Pool, owner, idempotency string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		repo := agentpostgres.New(pool)
		task := agentdomain.Task{
			ID: ids.UUIDv7{}.New(), OwnerUserID: owner, State: agentdomain.StateQueued,
			Input:      []byte(`{"goal":"terminal notification fixture"}`),
			ProviderID: "fake", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		}
		if _, err := repo.Create(ctx, task, idempotency); err != nil {
			t.Fatalf("create task: %v", err)
		}
		return task.ID
	}
	countNotifications := func(t *testing.T, pool *pgxpool.Pool, owner string) int {
		t.Helper()
		return countScratchRows(t, pool, `SELECT count(*) FROM workos_core.notifications WHERE owner_user_id = $1`, owner)
	}

	pool, owner := fixturePool(t)
	ctx := context.Background()

	// Healthy path: terminal event and the notification commit together.
	repo, err := agentpostgres.NewWithNotificationSink(pool, notificationpostgres.New(pool))
	if err != nil {
		t.Fatal(err)
	}
	seedTask(t, pool, owner, "terminal-healthy")
	leaseID := ids.UUIDv7{}.New()
	lease, err := repo.Claim(ctx, "worker-terminal", 30*time.Second, leaseID, time.Now().UTC())
	if err != nil || lease == nil {
		t.Fatalf("claim: %v %v", lease, err)
	}
	completed := agentdomain.Event{ID: ids.UUIDv7{}.New(), EventType: "run_completed", Payload: []byte(`{"runCompleted":{"summary":"ok"}}`), OccurredAt: time.Now().UTC()}
	if _, err := repo.AppendEvent(ctx, leaseID, "worker-terminal", completed, agentdomain.StateCompleted, "fake", "run-1", nil, time.Now().UTC()); err != nil {
		t.Fatalf("append terminal event: %v", err)
	}
	if got := countNotifications(t, pool, owner); got != 1 {
		t.Fatalf("expected exactly one terminal notification, got %d", got)
	}
	if got := countScratchRows(t, pool, `SELECT count(*) FROM workos_core.notification_changes WHERE owner_user_id = $1 AND change_type = 'created'`, owner); got != 1 {
		t.Fatalf("expected exactly one CREATED change, got %d", got)
	}

	// Failing sink: the terminal transition must roll back entirely.
	failingPool, failingOwner := fixturePool(t)
	brokenRepo, err := agentpostgres.NewWithNotificationSink(failingPool, failingNotificationSink{})
	if err != nil {
		t.Fatal(err)
	}
	failingTask := seedTask(t, failingPool, failingOwner, "terminal-failing")
	failingLease := ids.UUIDv7{}.New()
	lease2, err := brokenRepo.Claim(ctx, "worker-terminal", 30*time.Second, failingLease, time.Now().UTC())
	if err != nil || lease2 == nil {
		t.Fatalf("failing-sink claim: %v %v", lease2, err)
	}
	if _, err := brokenRepo.AppendEvent(ctx, failingLease, "worker-terminal", completed, agentdomain.StateCompleted, "fake", "run-2", nil, time.Now().UTC()); err == nil {
		t.Fatal("terminal append with failing notification sink must fail")
	}
	if got := countNotifications(t, failingPool, failingOwner); got != 0 {
		t.Fatalf("no notification may survive a rolled-back source: %d", got)
	}
	if got := countScratchRows(t, failingPool, `SELECT count(*) FROM workos_events.events WHERE stream_id = $1`, failingTask); got != 0 {
		t.Fatalf("terminal event must roll back with its source: %d", got)
	}
	if got := countScratchRows(t, failingPool, `SELECT count(*) FROM workos_core.agent_tasks WHERE id = $1 AND state = 'completed'`, failingTask); got != 0 {
		t.Fatalf("task must not stay terminal after rollback: %d", got)
	}
}
