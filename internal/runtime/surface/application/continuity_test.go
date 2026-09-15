// Continuity application tests: the attach/detach/takeover/stop semantics
// over an in-memory store that mirrors the transactional guarantees the
// PostgreSQL repository enforces. The real-PostgreSQL state machine runs in
// the surface-continuity gate (tools/surface-continuity).
package application

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/yangtao121/workos/internal/runtime/surface/domain"
	"github.com/yangtao121/workos/internal/runtime/surface/ports"
)

const (
	continuityOwner   = "01999999-9999-7999-8999-000000000a01"
	continuityProject = "01999999-9999-7999-8999-000000000a02"
	deviceA           = "01999999-9999-7999-8999-000000000b01"
	deviceB           = "01999999-9999-7999-8999-000000000b02"
)

func continuityWorkload(id string, terminal bool) ports.InteractiveWorkload {
	state := "running"
	if terminal {
		state = "closed"
	}
	return ports.InteractiveWorkload{
		WorkloadID: id, Kind: ports.WorkloadKindPty, OwnerUserID: continuityOwner,
		ProjectID: continuityProject, State: state, Terminal: terminal,
		CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(30 * time.Minute),
	}
}

// fakeRuntime records the lifecycle calls and answers from a fixed table.
type fakeRuntime struct {
	workloads    map[string]ports.InteractiveWorkload
	detached     []string
	stopped      []string
	detachFailed bool
}

func (f *fakeRuntime) Resolve(_ context.Context, ownerUserID, workloadID string) (ports.InteractiveWorkload, error) {
	workload, ok := f.workloads[workloadID]
	if !ok || workload.OwnerUserID != ownerUserID {
		return ports.InteractiveWorkload{}, ports.ErrContinuityNotFound
	}
	return workload, nil
}

func (f *fakeRuntime) ListProject(_ context.Context, ownerUserID, projectID string) ([]ports.InteractiveWorkload, error) {
	var list []ports.InteractiveWorkload
	for _, workload := range f.workloads {
		if workload.OwnerUserID == ownerUserID && workload.ProjectID == projectID && !workload.Terminal {
			list = append(list, workload)
		}
	}
	return list, nil
}

func (f *fakeRuntime) DetachWorkload(_ context.Context, kind ports.WorkloadKind, _, workloadID string) error {
	if f.detachFailed {
		return errors.New("engine down")
	}
	f.detached = append(f.detached, string(kind)+":"+workloadID)
	return nil
}

func (f *fakeRuntime) StopWorkload(_ context.Context, kind ports.WorkloadKind, _, workloadID string) (ports.InteractiveWorkload, error) {
	workload := f.workloads[workloadID]
	workload.State = "closed"
	workload.Terminal = true
	f.workloads[workloadID] = workload
	f.stopped = append(f.stopped, string(kind)+":"+workloadID)
	return workload, nil
}

// memoryContinuityStore mirrors the repository's transactional semantics:
// attach grants first control atomically, request control renews or takes
// over atomically, and no intermediate state is observable.
type memoryContinuityStore struct {
	attachments map[string]*domain.SurfaceAttachment
	byKey       map[string]string
	leases      map[string]*domain.ControlLease
	now         time.Time
}

func newMemoryContinuityStore() *memoryContinuityStore {
	return &memoryContinuityStore{
		attachments: map[string]*domain.SurfaceAttachment{},
		byKey:       map[string]string{},
		leases:      map[string]*domain.ControlLease{},
		now:         time.Now().UTC(),
	}
}

func (m *memoryContinuityStore) clone(a *domain.SurfaceAttachment) domain.SurfaceAttachment {
	stored := *a
	return stored
}

func (m *memoryContinuityStore) Attach(_ context.Context, command ports.AttachCommand) (domain.SurfaceAttachment, domain.ControlLease, error) {
	attachment := command.Attachment
	if attachment.AttachedAt.IsZero() {
		attachment.AttachedAt = m.now
	}
	if id, ok := m.byKey[attachment.OwnerUserID+"/"+attachment.IdempotencyKey]; ok {
		lease := domain.ControlLease{WorkloadID: attachment.WorkloadID, OwnerUserID: attachment.OwnerUserID}
		if l, ok := m.leases[attachment.WorkloadID]; ok {
			lease = *l
		}
		return m.clone(m.attachments[id]), lease, nil
	}
	attachment.State = domain.AttachmentStateAttached
	attachment.Controls = false
	attachment.ControlGeneration = 0
	m.attachments[attachment.ID] = &attachment
	m.byKey[attachment.OwnerUserID+"/"+attachment.IdempotencyKey] = attachment.ID
	if lease, ok := m.leases[attachment.WorkloadID]; !ok {
		created := domain.ControlLease{
			WorkloadID: attachment.WorkloadID, OwnerUserID: attachment.OwnerUserID,
			ControlGeneration: 1, ControllerAttachmentID: attachment.ID, ControllerDeviceID: attachment.DeviceID,
			GrantedAt: m.now, ExpiresAt: command.Until,
		}
		m.leases[attachment.WorkloadID] = &created
		stored := m.attachments[attachment.ID]
		stored.Controls = true
		stored.ControlGeneration = 1
		expiry := command.Until
		stored.ControlExpiresAt = &expiry
		return m.clone(stored), created, nil
	} else {
		return m.clone(m.attachments[attachment.ID]), *lease, nil
	}
}

func (m *memoryContinuityStore) RequestControl(_ context.Context, attachment domain.SurfaceAttachment, now, until time.Time) (ports.ControlVerdict, error) {
	lease, ok := m.leases[attachment.WorkloadID]
	if !ok {
		lease = &domain.ControlLease{
			WorkloadID: attachment.WorkloadID, OwnerUserID: attachment.OwnerUserID,
			ControlGeneration: 1, ControllerAttachmentID: attachment.ID, ControllerDeviceID: attachment.DeviceID,
			GrantedAt: now, ExpiresAt: until,
		}
		m.leases[attachment.WorkloadID] = lease
		stored := m.attachments[attachment.ID]
		stored.Controls = true
		stored.ControlGeneration = 1
		expiry := until
		stored.ControlExpiresAt = &expiry
		return ports.ControlVerdict{Attachment: m.clone(stored), Lease: *lease}, nil
	}
	stored := m.attachments[attachment.ID]
	if lease.Matches(stored) && lease.ControlValid(now) {
		if err := lease.Renew(now, until, stored); err != nil {
			return ports.ControlVerdict{}, err
		}
		expiry := until
		stored.ControlExpiresAt = &expiry
		return ports.ControlVerdict{Attachment: m.clone(stored), Lease: *lease, Renewed: true}, nil
	}
	previous := *lease
	lease.Takeover(now, until, stored)
	if previous.ControllerAttachmentID != stored.ID {
		if old, ok := m.attachments[previous.ControllerAttachmentID]; ok {
			lease.InvalidateController(old)
		}
	}
	return ports.ControlVerdict{Attachment: m.clone(stored), Lease: *lease}, nil
}

func (m *memoryContinuityStore) Detach(_ context.Context, ownerUserID, attachmentID string, now time.Time) (domain.SurfaceAttachment, error) {
	stored, ok := m.attachments[attachmentID]
	if !ok || stored.OwnerUserID != ownerUserID {
		return domain.SurfaceAttachment{}, ports.ErrContinuityNotFound
	}
	if err := stored.Detach(now); err != nil {
		return domain.SurfaceAttachment{}, err
	}
	return m.clone(stored), nil
}

func (m *memoryContinuityStore) GetAttachment(_ context.Context, ownerUserID, attachmentID, deviceID string) (domain.SurfaceAttachment, error) {
	stored, ok := m.attachments[attachmentID]
	if !ok || stored.OwnerUserID != ownerUserID || stored.DeviceID != deviceID {
		return domain.SurfaceAttachment{}, ports.ErrContinuityNotFound
	}
	return m.clone(stored), nil
}

func (m *memoryContinuityStore) LiveAttachmentBySurfaceSession(_ context.Context, ownerUserID, surfaceSessionID, deviceID string) (domain.SurfaceAttachment, error) {
	var best *domain.SurfaceAttachment
	for _, stored := range m.attachments {
		if stored.OwnerUserID == ownerUserID && stored.SurfaceSessionID == surfaceSessionID &&
			stored.DeviceID == deviceID && stored.State.Live() {
			if best == nil || stored.AttachedAt.After(best.AttachedAt) {
				best = stored
			}
		}
	}
	if best == nil {
		return domain.SurfaceAttachment{}, ports.ErrContinuityNotFound
	}
	return m.clone(best), nil
}

func (m *memoryContinuityStore) Lease(_ context.Context, workloadID string) (domain.ControlLease, bool, error) {
	lease, ok := m.leases[workloadID]
	if !ok {
		return domain.ControlLease{}, false, nil
	}
	return *lease, true, nil
}

func (m *memoryContinuityStore) CountLiveAttachments(_ context.Context, ownerUserID, projectID string) (map[string]int32, error) {
	counts := map[string]int32{}
	for _, stored := range m.attachments {
		if stored.OwnerUserID == ownerUserID && stored.ProjectID == projectID && stored.State.Live() {
			counts[stored.WorkloadID]++
		}
	}
	return counts, nil
}

func (m *memoryContinuityStore) ListLiveAttachmentWorkloads(_ context.Context) ([]ports.LiveAttachmentWorkload, error) {
	seen := map[string]bool{}
	var list []ports.LiveAttachmentWorkload
	for _, stored := range m.attachments {
		if stored.State.Live() && !seen[stored.WorkloadID] {
			seen[stored.WorkloadID] = true
			list = append(list, ports.LiveAttachmentWorkload{WorkloadID: stored.WorkloadID, OwnerUserID: stored.OwnerUserID})
		}
	}
	return list, nil
}

func (m *memoryContinuityStore) ExpireElapsedAttachments(_ context.Context, now time.Time) ([]string, error) {
	var expired []string
	for _, stored := range m.attachments {
		if stored.State.Live() && stored.ControlExpiresAt != nil && stored.ControlExpiresAt.Before(now) {
			stored.State = domain.AttachmentStateExpired
			stored.Controls = false
			detached := now
			stored.DetachedAt = &detached
			expired = append(expired, stored.ID)
		}
	}
	return expired, nil
}

func (m *memoryContinuityStore) ExpireAttachmentsForWorkloads(_ context.Context, workloadIDs []string, now time.Time) error {
	for _, workloadID := range workloadIDs {
		for _, stored := range m.attachments {
			if stored.WorkloadID == workloadID && stored.State.Live() {
				stored.State = domain.AttachmentStateExpired
				stored.Controls = false
				detached := now
				stored.DetachedAt = &detached
			}
		}
	}
	return nil
}

type seqID struct{ counter int }

func (s *seqID) New() string {
	s.counter++
	return fmt.Sprintf("01999999-9999-7999-8999-%07d%03d0f", s.counter/1000, s.counter%1000)
}

func newContinuityFixture(_ *testing.T) (*ContinuityService, *fakeRuntime, *memoryContinuityStore) {
	runtime := &fakeRuntime{workloads: map[string]ports.InteractiveWorkload{
		"01999999-9999-7999-8999-000000000c01": continuityWorkload("01999999-9999-7999-8999-000000000c01", false),
		"01999999-9999-7999-8999-000000000c02": continuityWorkload("01999999-9999-7999-8999-000000000c02", true),
	}}
	store := newMemoryContinuityStore()
	service, err := NewContinuityService(store, runtime, &seqID{}, 30*time.Minute)
	if err != nil {
		panic(err)
	}
	return service, runtime, store
}

func TestContinuityAttachGrantsFirstControlThenObservers(t *testing.T) {
	service, _, _ := newContinuityFixture(nil)
	ctx := context.Background()
	const workload = "01999999-9999-7999-8999-000000000c01"

	first, err := service.AttachSurface(ctx, continuityOwner, deviceA, workload, "attach-a")
	if err != nil {
		t.Fatalf("first attach: %v", err)
	}
	if !first.Attachment.Controls || first.Attachment.ControlGeneration != 1 {
		t.Fatalf("first attach must grant control generation 1: %+v", first.Attachment)
	}
	if !first.LeaseExists || first.Lease.ControllerDeviceID != deviceA {
		t.Fatalf("first attach must create the lease for device A: %+v", first.Lease)
	}

	// The second device attaches as a plain observer: no control change.
	second, err := service.AttachSurface(ctx, continuityOwner, deviceB, workload, "attach-b")
	if err != nil {
		t.Fatalf("second attach: %v", err)
	}
	if second.Attachment.Controls {
		t.Fatal("second attach must not grant control")
	}
	if second.Lease.ControllerDeviceID != deviceA || second.Lease.ControlGeneration != 1 {
		t.Fatalf("second attach disturbed the lease: %+v", second.Lease)
	}

	// Idempotent replay returns the stored row without touching the lease.
	replay, err := service.AttachSurface(ctx, continuityOwner, deviceA, workload, "attach-a")
	if err != nil || replay.Attachment.ID != first.Attachment.ID {
		t.Fatalf("attach replay drifted: %v %+v", err, replay.Attachment)
	}
	if replay.Lease.ControlGeneration != 1 {
		t.Fatal("attach replay must never alter control")
	}
}

func TestContinuityAttachRefusesTerminalWorkloadWithTrueState(t *testing.T) {
	service, _, _ := newContinuityFixture(nil)
	ctx := context.Background()
	result, err := service.AttachSurface(ctx, continuityOwner, deviceA, "01999999-9999-7999-8999-000000000c02", "attach-closed")
	if !errors.Is(err, domain.ErrWorkloadNotRunning) {
		t.Fatalf("terminal attach must fail with the not-running verdict: %v", err)
	}
	if result.Workload.State != "closed" {
		t.Fatalf("verdict must carry the true state, got %q", result.Workload.State)
	}
	if _, err := service.AttachSurface(ctx, continuityOwner, deviceA, "01999999-9999-7999-8999-00000000ffff", "attach-unknown"); !errors.Is(err, ports.ErrContinuityNotFound) {
		t.Fatalf("unknown workload must be not found: %v", err)
	}
}

func TestContinuityRequestControlRenewsAndTakesOver(t *testing.T) {
	service, _, _ := newContinuityFixture(nil)
	ctx := context.Background()
	const workload = "01999999-9999-7999-8999-000000000c01"
	first, _ := service.AttachSurface(ctx, continuityOwner, deviceA, workload, "attach-a")
	second, _ := service.AttachSurface(ctx, continuityOwner, deviceB, workload, "attach-b")

	// The exact controller renews: generation stays, expiry moves.
	renewed, err := service.RequestSurfaceControl(ctx, continuityOwner, deviceA, first.Attachment.SurfaceSessionID)
	if err != nil {
		t.Fatalf("renew: %v", err)
	}
	if !renewed.Renewed || renewed.Lease.ControlGeneration != 1 {
		t.Fatalf("renewal must not bump the generation: %+v", renewed.Lease)
	}
	if !renewed.Lease.ExpiresAt.After(renewed.Lease.GrantedAt) {
		t.Fatal("renewal must extend the expiry")
	}

	// Device B takes over explicitly: generation advances, A loses control.
	takeover, err := service.RequestSurfaceControl(ctx, continuityOwner, deviceB, second.Attachment.SurfaceSessionID)
	if err != nil {
		t.Fatalf("takeover: %v", err)
	}
	if takeover.Renewed || takeover.Lease.ControlGeneration != 2 || takeover.Lease.ControllerDeviceID != deviceB {
		t.Fatalf("takeover must advance to generation 2 for B: %+v", takeover.Lease)
	}
	if !takeover.Attachment.Controls || takeover.Attachment.ControlGeneration != 2 {
		t.Fatalf("takeover must mark B's attachment controller: %+v", takeover.Attachment)
	}

	// A's input is refused on the data path; B's passes.
	if err := service.AuthorizeInput(ctx, continuityOwner, workload, deviceA); !errors.Is(err, ports.ErrContinuityDenied) {
		t.Fatalf("superseded device A must be denied: %v", err)
	}
	if err := service.AuthorizeInput(ctx, continuityOwner, workload, deviceB); err != nil {
		t.Fatalf("controller B must pass: %v", err)
	}
	// A re-attaching (a new access relation) never steals control back.
	reattach, err := service.AttachSurface(ctx, continuityOwner, deviceA, workload, "attach-a2")
	if err != nil {
		t.Fatalf("re-attach: %v", err)
	}
	if reattach.Attachment.Controls || reattach.Lease.ControllerDeviceID != deviceB {
		t.Fatal("re-attach must not alter control; only RequestSurfaceControl takes over")
	}
	if err := service.AuthorizeInput(ctx, continuityOwner, workload, deviceA); !errors.Is(err, ports.ErrContinuityDenied) {
		t.Fatalf("re-attached A still must not drive: %v", err)
	}
}

func TestContinuityDetachReleasesOnlyTheAttachment(t *testing.T) {
	service, runtime, _ := newContinuityFixture(nil)
	ctx := context.Background()
	const workload = "01999999-9999-7999-8999-000000000c01"
	first, _ := service.AttachSurface(ctx, continuityOwner, deviceA, workload, "attach-a")
	second, _ := service.AttachSurface(ctx, continuityOwner, deviceB, workload, "attach-b")

	if err := service.DetachSurface(ctx, continuityOwner, deviceB, second.Attachment.SurfaceSessionID); err != nil {
		t.Fatalf("detach: %v", err)
	}
	// The PTY workload has no per-device connection resources: the runtime
	// detach call is still issued and the attachment row is marked detached.
	still, err := service.AttachSurface(ctx, continuityOwner, deviceB, workload, "attach-b")
	if err != nil {
		t.Fatalf("replay after detach: %v", err)
	}
	if still.Attachment.State != domain.AttachmentStateDetached {
		t.Fatalf("attachment must be detached, got %q", still.Attachment.State)
	}
	// The controller keeps control: detach never steals.
	if err := service.AuthorizeInput(ctx, continuityOwner, workload, deviceA); err != nil {
		t.Fatalf("controller A must keep control after B's detach: %v", err)
	}
	if len(runtime.detached) != 1 {
		t.Fatalf("runtime detach calls: %v", runtime.detached)
	}
	// Detaching someone else's attachment is not found.
	if err := service.DetachSurface(ctx, continuityOwner, deviceB, first.Attachment.SurfaceSessionID); !errors.Is(err, ports.ErrContinuityNotFound) {
		t.Fatalf("foreign-surface detach must be not found: %v", err)
	}
}

func TestContinuityStopReclaimsAndExpiresAttachments(t *testing.T) {
	service, runtime, store := newContinuityFixture(nil)
	ctx := context.Background()
	const workload = "01999999-9999-7999-8999-000000000c01"
	first, _ := service.AttachSurface(ctx, continuityOwner, deviceA, workload, "attach-a")
	second, _ := service.AttachSurface(ctx, continuityOwner, deviceB, workload, "attach-b")

	summary, err := service.StopSurfaceWorkload(ctx, continuityOwner, workload, "stop-key-1")
	if err != nil {
		t.Fatalf("stop: %v", err)
	}
	if summary.Workload.State != "closed" || summary.Workload.Terminal != true {
		t.Fatalf("stop must report the closed workload: %+v", summary.Workload)
	}
	if len(runtime.stopped) != 1 {
		t.Fatalf("stop must drive the runner close once: %v", runtime.stopped)
	}
	counts, _ := store.CountLiveAttachments(ctx, continuityOwner, continuityProject)
	if len(counts) != 0 {
		t.Fatalf("stop must expire every live attachment: %v", counts)
	}
	// A replaying stop converges on the stored terminal state.
	if _, err := service.StopSurfaceWorkload(ctx, continuityOwner, workload, "stop-key-2"); err != nil {
		t.Fatalf("stop replay: %v", err)
	}
	if len(runtime.stopped) != 1 {
		t.Fatalf("stop replay must not close twice: %v", runtime.stopped)
	}
	_ = first
	_ = second
}

func TestContinuitySessionWorkloadsAreNotRestartable(t *testing.T) {
	service, _, _ := newContinuityFixture(nil)
	ctx := context.Background()
	const workload = "01999999-9999-7999-8999-000000000c01"
	if _, err := service.RestartSurfaceWorkload(ctx, continuityOwner, workload, "restart-key"); !errors.Is(err, domain.ErrWorkloadNotRestartable) {
		t.Fatalf("PTY workload restart must be refused honestly: %v", err)
	}
	if _, err := service.RestartSurfaceWorkload(ctx, continuityOwner, "01999999-9999-7999-8999-00000000ffff", "restart-key"); !errors.Is(err, ports.ErrContinuityNotFound) {
		t.Fatalf("unknown workload restart must be not found: %v", err)
	}
}

func TestContinuityAuthorizeInputFailsClosed(t *testing.T) {
	service, _, store := newContinuityFixture(nil)
	ctx := context.Background()
	const workload = "01999999-9999-7999-8999-000000000c01"
	// No lease yet: the direct session path stays owner-scoped.
	if err := service.AuthorizeInput(ctx, continuityOwner, workload, deviceA); err != nil {
		t.Fatalf("pre-attachment write must pass: %v", err)
	}
	first, _ := service.AttachSurface(ctx, continuityOwner, deviceA, workload, "attach-a")
	_ = first
	// A different owner probing the same workload id is denied.
	if err := service.AuthorizeInput(ctx, "01999999-9999-7999-8999-000000000a99", workload, deviceA); !errors.Is(err, ports.ErrContinuityDenied) {
		t.Fatalf("foreign owner must be denied: %v", err)
	}
	// An elapsed lease denies everyone until an explicit takeover.
	lease, _, _ := store.Lease(ctx, workload)
	expired := lease
	expired.ExpiresAt = time.Now().UTC().Add(-time.Second)
	store.leases[workload] = &expired
	if err := service.AuthorizeInput(ctx, continuityOwner, workload, deviceA); !errors.Is(err, ports.ErrContinuityDenied) {
		t.Fatalf("expired epoch must deny the former controller: %v", err)
	}
}

func TestContinuitySweepExpiresElapsedAndTerminalAttachments(t *testing.T) {
	service, runtime, store := newContinuityFixture(nil)
	ctx := context.Background()
	const workload = "01999999-9999-7999-8999-000000000c01"
	first, _ := service.AttachSurface(ctx, continuityOwner, deviceA, workload, "attach-a")

	// Elapsed controller expiry sweeps to expired.
	elapsed := time.Now().UTC().Add(-time.Second)
	stored := store.attachments[first.Attachment.ID]
	stored.ControlExpiresAt = &elapsed
	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if stored.State != domain.AttachmentStateExpired {
		t.Fatalf("elapsed attachment must be expired, got %q", stored.State)
	}

	// Attachments of terminal workloads expire with the program.
	second, _ := service.AttachSurface(ctx, continuityOwner, deviceA, workload, "attach-a2")
	if _, err := runtime.StopWorkload(ctx, ports.WorkloadKindPty, continuityOwner, workload); err != nil {
		t.Fatalf("stop workload: %v", err)
	}
	if err := service.Sweep(ctx); err != nil {
		t.Fatalf("sweep after stop: %v", err)
	}
	if store.attachments[second.Attachment.ID].State != domain.AttachmentStateExpired {
		t.Fatal("attachment of a terminal workload must be expired by the sweep")
	}
}

func TestContinuityListProjectSurfacesReportsFacts(t *testing.T) {
	service, _, _ := newContinuityFixture(nil)
	ctx := context.Background()
	const workload = "01999999-9999-7999-8999-000000000c01"
	if _, err := service.AttachSurface(ctx, continuityOwner, deviceA, workload, "attach-a"); err != nil {
		t.Fatalf("attach: %v", err)
	}
	summaries, err := service.ListProjectSurfaces(ctx, continuityOwner, continuityProject)
	if err != nil || len(summaries) != 1 {
		t.Fatalf("list: %v %d", err, len(summaries))
	}
	summary := summaries[0]
	if summary.Workload.WorkloadID != workload || summary.AttachmentCount != 1 || summary.Generation != 1 {
		t.Fatalf("summary facts drifted: %+v", summary)
	}
	if summary.KeepAliveSeconds != 1800 {
		t.Fatalf("bounded policy must report the 30-minute ceiling: %d", summary.KeepAliveSeconds)
	}
	// Foreign owners see nothing; invalid ids fail closed.
	foreign, err := service.ListProjectSurfaces(ctx, "01999999-9999-7999-8999-000000000a99", continuityProject)
	if err != nil || len(foreign) != 0 {
		t.Fatalf("foreign list must be empty: %v %d", err, len(foreign))
	}
	if _, err := service.ListProjectSurfaces(ctx, "not-a-uuid", continuityProject); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("invalid owner must fail closed: %v", err)
	}
}
