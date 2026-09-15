// Real-PostgreSQL continuity store tests: the attach/takeover/detach/sweep
// state machine against migration 059 in a scratch database. The test is
// gated on WORKOS_SURFACE_CONTINUITY_TEST_DATABASE_URL (created and migrated
// by tools/terminal-sessions style gates); without it the package stays
// hermetic and skips. The go test binary itself applies the migrations, so
// any drift between the schema files and these queries fails loudly.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yangtao121/workos/internal/platform/migrations"
	"github.com/yangtao121/workos/internal/runtime/surface/domain"
	"github.com/yangtao121/workos/internal/runtime/surface/ports"
)

func continuityTestStore(t *testing.T) *ContinuityRepository {
	t.Helper()
	databaseURL := os.Getenv("WORKOS_SURFACE_CONTINUITY_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("requires WORKOS_SURFACE_CONTINUITY_TEST_DATABASE_URL (run through tools/surface-continuity/gate.sh)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := migrations.Run(ctx, databaseURL); err != nil {
		t.Fatalf("migrations: %v", err)
	}
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return NewContinuity(pool)
}

type continuityIDs struct {
	owner, project, workload string
	deviceA, deviceB         string
}

// continuityID builds a canonical lowercase UUIDv7-shaped id: 24-char fixed
// prefix plus a 12-hex-digit per-test suffix. The migration CHECK enforces
// the exact grammar, so the shape must be real.
func continuityID(stamp string, n int) string {
	return "01999999-9999-7999-8999-" + stamp + fmt.Sprintf("%06d", n)
}

func newContinuityIDs(stamp string) continuityIDs {
	return continuityIDs{
		owner:    continuityID(stamp, 1),
		project:  continuityID(stamp, 2),
		workload: continuityID(stamp, 3),
		deviceA:  continuityID(stamp, 4),
		deviceB:  continuityID(stamp, 5),
	}
}

// attachmentFor builds the per-device attachment; the id derives from the
// per-test stamp and a per-key counter, keeping the UUIDv7 grammar.
func attachmentFor(stamp string, ids continuityIDs, device, key string, n int) domain.SurfaceAttachment {
	now := time.Now().UTC().Truncate(time.Microsecond)
	return domain.SurfaceAttachment{
		ID: continuityID(stamp, n), WorkloadID: ids.workload,
		SurfaceSessionID: ids.workload, OwnerUserID: ids.owner, ProjectID: ids.project,
		DeviceID: device, IdempotencyKey: key, State: domain.AttachmentStateAttached,
		AttachedAt: now,
	}
}

func TestContinuityStoreStateMachineOnPostgres(t *testing.T) {
	store := continuityTestStore(t)
	ids := newContinuityIDs("aaaaaa")
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	until := now.Add(30 * time.Minute)

	attachA := attachmentFor("aaaaaa", ids, ids.deviceA, "a", 11)
	stored, lease, err := store.Attach(ctx, ports.AttachCommand{Attachment: attachA, Until: until})
	if err != nil {
		t.Fatalf("attach A: %v", err)
	}
	if !stored.Controls || stored.ControlGeneration != 1 || !lease.Matches(&stored) {
		t.Fatalf("first attach must grant generation 1 to A: %+v %+v", stored, lease)
	}
	if !stored.ControlExpiresAt.Equal(until.Truncate(time.Microsecond)) && !stored.ControlExpiresAt.After(now) {
		t.Fatalf("controller attachment must carry the bounded expiry: %v", stored.ControlExpiresAt)
	}

	// Idempotent replay returns the stored row and never re-grants.
	replay, replayLease, err := store.Attach(ctx, ports.AttachCommand{Attachment: attachA, Until: until.Add(time.Hour)})
	if err != nil || replay.ID != stored.ID || replayLease.ControlGeneration != 1 {
		t.Fatalf("replay drifted: %v %+v %+v", err, replay, replayLease)
	}
	if !replay.ControlExpiresAt.Equal(*stored.ControlExpiresAt) {
		t.Fatal("replay must not extend the lease; only RequestControl renews")
	}

	// Second device attaches as an observer.
	attachB := attachmentFor("aaaaaa", ids, ids.deviceB, "b", 12)
	observer, leaseAfterB, err := store.Attach(ctx, ports.AttachCommand{Attachment: attachB, Until: until})
	if err != nil {
		t.Fatalf("attach B: %v", err)
	}
	if observer.Controls || leaseAfterB.ControllerDeviceID != ids.deviceA || leaseAfterB.ControlGeneration != 1 {
		t.Fatalf("observer attach disturbed the lease: %+v %+v", observer, leaseAfterB)
	}

	// Renewal by the exact controller: same generation, extended expiry.
	renewed, err := store.RequestControl(ctx, stored, now, until.Add(10*time.Minute))
	if err != nil || !renewed.Renewed || renewed.Lease.ControlGeneration != 1 {
		t.Fatalf("renewal: %v %+v", err, renewed)
	}
	if !renewed.Lease.ExpiresAt.After(until) {
		t.Fatal("renewal must extend the expiry")
	}

	// Explicit takeover by B: generation advances, A's flags clear atomically.
	takeover, err := store.RequestControl(ctx, observer, now, until.Add(20*time.Minute))
	if err != nil || takeover.Renewed || takeover.Lease.ControlGeneration != 2 {
		t.Fatalf("takeover: %v %+v", err, takeover)
	}
	controllerA, err := store.GetAttachment(ctx, ids.owner, stored.ID, ids.deviceA)
	if err != nil {
		t.Fatalf("read A after takeover: %v", err)
	}
	if controllerA.Controls || controllerA.ControlGeneration != 1 {
		t.Fatalf("previous controller must lose its flag without history rewrite: %+v", controllerA)
	}

	// Detach releases only the attachment row; the lease survives.
	detached, err := store.Detach(ctx, ids.owner, observer.ID, now)
	if err != nil || detached.State != domain.AttachmentStateDetached || detached.Controls {
		t.Fatalf("detach: %v %+v", err, detached)
	}
	survivingLease, found, err := store.Lease(ctx, ids.workload)
	if err != nil || !found || survivingLease.ControllerAttachmentID != observer.ID {
		t.Fatalf("detach must not disturb the lease: %v %+v", err, survivingLease)
	}

	// Live counts and workload listing see only the still-attached rows.
	counts, err := store.CountLiveAttachments(ctx, ids.owner, ids.project)
	if err != nil || counts[ids.workload] != 1 {
		t.Fatalf("live count after detach: %v %+v", err, counts)
	}
	live, err := store.ListLiveAttachmentWorkloads(ctx)
	if err != nil {
		t.Fatalf("list live workloads: %v", err)
	}
	foundWorkload := false
	for _, entry := range live {
		if entry.WorkloadID == ids.workload && entry.OwnerUserID == ids.owner {
			foundWorkload = true
		}
	}
	if !foundWorkload {
		t.Fatal("live workload listing lost the attached row")
	}

	// The sweep expires elapsed controller attachments and terminal-workload
	// attachments; the lease row itself remains the ledger.
	if _, err := store.ExpireElapsedAttachments(ctx, now.Add(time.Hour)); err != nil {
		t.Fatalf("expire elapsed: %v", err)
	}
	if err := store.ExpireAttachmentsForWorkloads(ctx, []string{ids.workload}, now.Add(time.Hour)); err != nil {
		t.Fatalf("expire for workloads: %v", err)
	}
	afterSweep, err := store.GetAttachment(ctx, ids.owner, stored.ID, ids.deviceA)
	if err != nil || afterSweep.State != domain.AttachmentStateExpired {
		t.Fatalf("sweep must expire remaining attachments: %v %+v", err, afterSweep)
	}
	counts, err = store.CountLiveAttachments(ctx, ids.owner, ids.project)
	if err != nil || len(counts) != 0 {
		t.Fatalf("no live attachments may survive the sweep: %v %+v", err, counts)
	}
}

// TestContinuityStoreTakeoverConvergesConcurrently: parallel takeover
// attempts converge — exactly one winner per generation order and the final
// lease always matches exactly one live attachment's flags.
func TestContinuityStoreTakeoverConvergesConcurrently(t *testing.T) {
	store := continuityTestStore(t)
	ids := newContinuityIDs("bbbbbb")
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	until := now.Add(30 * time.Minute)

	seed := attachmentFor("bbbbbb", ids, ids.deviceA, "seed", 11)
	first, _, err := store.Attach(ctx, ports.AttachCommand{Attachment: seed, Until: until})
	if err != nil {
		t.Fatalf("seed attach: %v", err)
	}
	rival := attachmentFor("bbbbbb", ids, ids.deviceB, "rival", 12)
	second, _, err := store.Attach(ctx, ports.AttachCommand{Attachment: rival, Until: until})
	if err != nil {
		t.Fatalf("rival attach: %v", err)
	}

	type verdict struct {
		generation int64
		err        error
	}
	results := make(chan verdict, 2)
	go func() {
		out, err := store.RequestControl(ctx, first, now, until)
		results <- verdict{generation: out.Lease.ControlGeneration, err: err}
	}()
	go func() {
		out, err := store.RequestControl(ctx, second, now, until)
		results <- verdict{generation: out.Lease.ControlGeneration, err: err}
	}()
	firstOutcome, secondOutcome := <-results, <-results
	if firstOutcome.err != nil || secondOutcome.err != nil {
		t.Fatalf("concurrent takeovers must both succeed deterministically: %v %v", firstOutcome.err, secondOutcome.err)
	}
	lease, found, err := store.Lease(ctx, ids.workload)
	if err != nil || !found {
		t.Fatalf("final lease: %v", err)
	}
	if lease.ControlGeneration != 3 {
		t.Fatalf("two takeovers from generation 1 must end at 3, got %d", lease.ControlGeneration)
	}
	// Exactly the final controller's row carries the control flag.
	flagged := 0
	for _, attachment := range []domain.SurfaceAttachment{first, second} {
		stored, err := store.GetAttachment(ctx, ids.owner, attachment.ID, attachment.DeviceID)
		if err != nil {
			t.Fatalf("read %s: %v", attachment.ID, err)
		}
		if stored.Controls {
			flagged++
			if !lease.Matches(&stored) {
				t.Fatalf("flagged attachment is not the lease holder: %+v vs %+v", stored, lease)
			}
		}
	}
	if flagged != 1 {
		t.Fatalf("exactly one attachment may hold control, got %d", flagged)
	}
	if _, err := store.LiveAttachmentBySurfaceSession(ctx, ids.owner, ids.workload, "01999999-9999-7999-8999-000000cccc01"); !errors.Is(err, ports.ErrContinuityNotFound) {
		t.Fatalf("unknown device attachment lookup must be not found: %v", err)
	}
}
