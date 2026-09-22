// Surface continuity application (ADR-0031): program execution and device
// access are separate lifecycles. Attachments are per-device access relations
// to the owner's running interactive workloads (PTY and native sessions
// first); control is a single server-side epoch switched only by the explicit
// takeover RPC. Detach releases the connection, never the program; Stop is
// the legacy deterministic reclaim. Program policy may be manual-stop;
// short-lived control leases and device access remain independently bounded.
package application

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/yangtao121/workos/internal/platform/ids"
	"github.com/yangtao121/workos/internal/runtime/surface/domain"
	"github.com/yangtao121/workos/internal/runtime/surface/ports"
)

// Control lease bounds are independent from the program lifetime.
const (
	MinControlLeaseTTL = time.Minute
	MaxControlLeaseTTL = 30 * time.Minute
	// Older stored projections without generation start at one.
	SessionWorkloadGeneration = 1
)

// ContinuitySummary is the application's projection of one continuous
// workload for ListProjectSurfaces and the stop response.
type ContinuitySummary struct {
	Workload         ports.InteractiveWorkload
	Generation       int64
	AttachmentCount  int32
	KeepAliveSeconds int64
}

// AttachResult is the AttachSurface verdict: the workload facts plus the
// device's persisted access relation and the workload's control lease.
type AttachResult struct {
	Workload    ports.InteractiveWorkload
	Attachment  domain.SurfaceAttachment
	Lease       domain.ControlLease
	LeaseExists bool
}

// ControlResult is the RequestSurfaceControl verdict.
type ControlResult struct {
	Attachment domain.SurfaceAttachment
	Lease      domain.ControlLease
	// Renewed reports the no-generation-bump path: the same attachment
	// already held a live lease and only the expiry moved forward.
	Renewed bool
}

type ContinuityService struct {
	store      ports.ContinuityStore
	workloads  ports.InteractiveWorkloadRuntime
	generator  ids.Generator
	controlTTL time.Duration
	now        func() time.Time
}

// NewContinuityService validates the bounded policy and wires the store, the
// interactive workload runtime, and the id generator.
func NewContinuityService(store ports.ContinuityStore, workloads ports.InteractiveWorkloadRuntime, generator ids.Generator, controlTTL time.Duration) (*ContinuityService, error) {
	if store == nil || workloads == nil || generator == nil {
		return nil, errors.New("continuity service requires store, workload runtime and ids")
	}
	if controlTTL < MinControlLeaseTTL || controlTTL > MaxControlLeaseTTL {
		return nil, errors.New("control lease TTL must stay within the 30-minute authorization ceiling")
	}
	return &ContinuityService{
		store: store, workloads: workloads, generator: generator,
		controlTTL: controlTTL, now: func() time.Time { return time.Now().UTC() },
	}, nil
}

// ListProjectSurfaces discovers the owner's running interactive workloads of
// one project with their live attachment counts. It never lists another
// owner's rows and never invents a running instance.
func (s *ContinuityService) ListProjectSurfaces(ctx context.Context, ownerUserID, projectID string) ([]ContinuitySummary, error) {
	if !domain.ValidSessionUUID(ownerUserID) || !domain.ValidSessionUUID(projectID) {
		return nil, domain.ErrInvalid
	}
	workloads, err := s.workloads.ListProject(ctx, ownerUserID, projectID)
	if err != nil {
		return nil, err
	}
	counts, err := s.store.CountLiveAttachments(ctx, ownerUserID, projectID)
	if err != nil {
		return nil, err
	}
	summaries := make([]ContinuitySummary, 0, len(workloads))
	for _, workload := range workloads {
		summaries = append(summaries, ContinuitySummary{
			Workload:         workload,
			Generation:       max(SessionWorkloadGeneration, workload.Generation),
			AttachmentCount:  counts[workload.WorkloadID],
			KeepAliveSeconds: workloadKeepAlive(workload),
		})
	}
	return summaries, nil
}

// AttachSurface opens this device's access relation to an existing workload.
// It never starts a program: a terminal workload fails with its true state so
// the caller decides to start a new session. The first attach of a workload
// with no lease becomes controller of generation 1; every later attach is a
// plain observer until its device explicitly requests control.
func (s *ContinuityService) AttachSurface(ctx context.Context, ownerUserID, deviceID, workloadID, idempotencyKey string, generations ...int64) (AttachResult, error) {
	if !domain.ValidSessionUUID(ownerUserID) || !domain.ValidSessionUUID(workloadID) || !domain.ValidSessionUUID(deviceID) || !domain.ValidSessionIdempotencyKey(idempotencyKey) {
		return AttachResult{}, domain.ErrInvalid
	}
	workload, err := s.workloads.Resolve(ctx, ownerUserID, workloadID)
	if err != nil {
		return AttachResult{}, err
	}
	if len(generations) > 0 && (generations[0] < 0 || (generations[0] > 0 && generations[0] != max(SessionWorkloadGeneration, workload.Generation))) {
		return AttachResult{}, domain.ErrWorkloadNotRunning
	}
	if workload.Kind == ports.WorkloadKindApp {
		return AttachResult{}, domain.ErrUnsupported
	}
	if workload.Terminal || workload.State != "running" {
		// Honest stopped-state verdict: attaching never resurrects a program.
		return AttachResult{Workload: workload}, fmt.Errorf("%w (state: %s)", domain.ErrWorkloadNotRunning, workload.State)
	}
	now := s.now().Truncate(time.Microsecond)
	attachment := domain.SurfaceAttachment{
		ID:               s.generator.New(),
		WorkloadID:       workload.WorkloadID,
		SurfaceSessionID: workload.WorkloadID,
		OwnerUserID:      ownerUserID,
		ProjectID:        workload.ProjectID,
		DeviceID:         deviceID,
		IdempotencyKey:   idempotencyKey,
		State:            domain.AttachmentStateAttached,
		AttachedAt:       now,
	}
	stored, lease, err := s.store.Attach(ctx, ports.AttachCommand{Attachment: attachment, Until: now.Add(s.controlTTL)})
	if err != nil {
		return AttachResult{}, err
	}
	return AttachResult{Workload: workload, Attachment: stored, Lease: lease, LeaseExists: lease.ControlGeneration > 0}, nil
}

// DetachSurface releases only this device's connection resources: the native
// media peer (when the workload is native) and the attachment row. The
// program keeps running under its persisted policy and output keeps
// accumulating. A detaching controller does not free the lease: it expires or
// another device takes over.
func (s *ContinuityService) DetachSurface(ctx context.Context, ownerUserID, deviceID, surfaceSessionID string) error {
	if !domain.ValidSessionUUID(ownerUserID) || !domain.ValidSessionUUID(deviceID) || !domain.ValidSessionUUID(surfaceSessionID) {
		return domain.ErrInvalid
	}
	attachment, err := s.store.LiveAttachmentBySurfaceSession(ctx, ownerUserID, surfaceSessionID, deviceID)
	if err != nil {
		return err
	}
	if workload, resolveErr := s.workloads.Resolve(ctx, ownerUserID, attachment.WorkloadID); resolveErr == nil && !workload.Terminal {
		// The native runner releases the media peer and queued input of the
		// current connection; PTY sessions carry no per-device connection
		// state beyond the attachment row.
		if detachErr := s.workloads.DetachWorkload(ctx, workload.Kind, ownerUserID, attachment.WorkloadID, deviceID); detachErr != nil && !errors.Is(detachErr, ports.ErrContinuityNotFound) {
			return detachErr
		}
	}
	_, err = s.store.Detach(ctx, ownerUserID, attachment.ID, s.now())
	return err
}

// RequestSurfaceControl is the explicit single-controller switch. The exact
// current controller renews (expiry moves, generation stays — it can never
// steal from another holder); any other attachment takes over, advancing the
// generation atomically and invalidating the previous controller's input,
// queued input, resize, and late renewals.
func (s *ContinuityService) RequestSurfaceControl(ctx context.Context, ownerUserID, deviceID, surfaceSessionID string) (ControlResult, error) {
	if !domain.ValidSessionUUID(ownerUserID) || !domain.ValidSessionUUID(deviceID) || !domain.ValidSessionUUID(surfaceSessionID) {
		return ControlResult{}, domain.ErrInvalid
	}
	attachment, err := s.store.LiveAttachmentBySurfaceSession(ctx, ownerUserID, surfaceSessionID, deviceID)
	if err != nil {
		return ControlResult{}, err
	}
	verdict, err := s.store.RequestControl(ctx, attachment, s.now(), s.now().Add(s.controlTTL))
	if err != nil {
		return ControlResult{}, err
	}
	return ControlResult{Attachment: verdict.Attachment, Lease: verdict.Lease, Renewed: verdict.Renewed}, nil
}

// ControlFacts is the GetSurfaceControl verdict.
type ControlFacts struct {
	Lease   domain.ControlLease
	Found   bool
	Running bool
}

// GetSurfaceControl reports the workload's current control lease facts and
// whether the workload is still running. Unknown workloads fail closed.
func (s *ContinuityService) GetSurfaceControl(ctx context.Context, ownerUserID, workloadID string) (ControlFacts, error) {
	if !domain.ValidSessionUUID(ownerUserID) || !domain.ValidSessionUUID(workloadID) {
		return ControlFacts{}, domain.ErrInvalid
	}
	workload, err := s.workloads.Resolve(ctx, ownerUserID, workloadID)
	if err != nil {
		return ControlFacts{}, err
	}
	lease, found, err := s.store.Lease(ctx, workloadID)
	if err != nil {
		return ControlFacts{}, err
	}
	return ControlFacts{Lease: lease, Found: found, Running: !workload.Terminal && workload.State == "running"}, nil
}

// StopSurfaceWorkload deterministically stops and reclaims the program (the
// legacy Close semantics: process group, media workers, private scratch) and
// expires every live attachment of the workload. The underlying session close
// is idempotent, so replaying a stop converges to the stored terminal state.
func (s *ContinuityService) StopSurfaceWorkload(ctx context.Context, ownerUserID, workloadID, actionKey string) (ContinuitySummary, error) {
	if !domain.ValidSessionUUID(ownerUserID) || !domain.ValidSessionUUID(workloadID) || !domain.ValidSessionIdempotencyKey(actionKey) {
		return ContinuitySummary{}, domain.ErrInvalid
	}
	workload, err := s.workloads.Resolve(ctx, ownerUserID, workloadID)
	if err != nil {
		return ContinuitySummary{}, err
	}

	if stopper, ok := s.workloads.(ports.InteractiveStopper); ok {
		workload, err = stopper.StopWorkloadAction(ctx, workload.Kind, ownerUserID, workloadID, actionKey, func() error { return s.store.ExpireAttachmentsForWorkloads(ctx, []string{workloadID}, s.now()) })
		if err != nil {
			return ContinuitySummary{}, err
		}
	} else {
		return ContinuitySummary{}, domain.ErrWorkloadNotRestartable
	}

	return ContinuitySummary{Workload: workload, Generation: max(SessionWorkloadGeneration, workload.Generation), KeepAliveSeconds: workloadKeepAlive(workload)}, nil
}

// RestartSurfaceWorkload starts a durable generation and fences old attachments.
func (s *ContinuityService) RestartSurfaceWorkload(ctx context.Context, ownerUserID, workloadID, actionKey string, modes ...int32) (ContinuitySummary, error) {
	if !domain.ValidSessionUUID(ownerUserID) || !domain.ValidSessionUUID(workloadID) || !domain.ValidSessionIdempotencyKey(actionKey) {
		return ContinuitySummary{}, domain.ErrInvalid
	}
	mode := int32(1)
	if len(modes) > 0 && modes[0] != 0 {
		mode = modes[0]
	}
	if mode != 1 && mode != 2 {
		return ContinuitySummary{}, domain.ErrInvalid
	}
	workload, err := s.workloads.Resolve(ctx, ownerUserID, workloadID)
	if err != nil {
		return ContinuitySummary{}, err
	}
	restarter, ok := s.workloads.(ports.InteractiveRestarter)
	if !ok {
		return ContinuitySummary{}, domain.ErrWorkloadNotRestartable
	}
	// Only a fresh generation fences connections; replaying a restart must
	// not detach devices that already attached to the resulting generation.
	restarted, err := restarter.RestartWorkload(ctx, workload.Kind, ownerUserID, workloadID, actionKey, func() error {
		return s.store.ExpireAttachmentsForWorkloads(ctx, []string{workloadID}, s.now())
	}, mode)
	if err != nil {
		return ContinuitySummary{}, err
	}
	return ContinuitySummary{Workload: restarted, Generation: max(SessionWorkloadGeneration, restarted.Generation), KeepAliveSeconds: workloadKeepAlive(restarted)}, nil
}

// Sweep applies the bounded policy to the access relations: attachments
// whose control expiry elapsed expire, and attachments of workloads that
// became terminal expire with them. Access expiry never stops a program.
func (s *ContinuityService) Sweep(ctx context.Context) error {
	now := s.now()
	if _, err := s.store.ExpireElapsedAttachments(ctx, now); err != nil {
		return err
	}
	live, err := s.store.ListLiveAttachmentWorkloads(ctx)
	if err != nil {
		return err
	}
	terminal := make([]string, 0, len(live))
	for _, entry := range live {
		workload, resolveErr := s.workloads.Resolve(ctx, entry.OwnerUserID, entry.WorkloadID)
		if errors.Is(resolveErr, ports.ErrContinuityNotFound) || (resolveErr == nil && workload.Terminal) {
			// A workload that no longer resolves or already reached a
			// terminal state has no access path left to offer.
			terminal = append(terminal, entry.WorkloadID)
			continue
		}
		if resolveErr != nil {
			return resolveErr
		}
	}
	return s.store.ExpireAttachmentsForWorkloads(ctx, terminal, now)
}

// AuthorizeInput is the data-path control gate (ADR-0031 §4): PTY writes and
// resizes and native input events consult the CURRENT lease on every request
// — no caching staleness beyond one call. Workloads without any lease (the
// pre-attachment direct session path) stay owner-scoped as before; once a
// workload has attachments, only the live controlling device may drive it.
func (s *ContinuityService) AuthorizeInput(ctx context.Context, ownerUserID, workloadID, deviceID string) error {
	if !domain.ValidSessionUUID(ownerUserID) || !domain.ValidSessionUUID(workloadID) || !domain.ValidSessionUUID(deviceID) {
		return domain.ErrInvalid
	}
	lease, found, err := s.store.Lease(ctx, workloadID)
	if err != nil {
		// A store outage on the control path fails closed: input is refused
		// rather than trusted stale.
		return err
	}
	if !found {
		return nil
	}
	if lease.OwnerUserID != ownerUserID {
		return ports.ErrContinuityDenied
	}
	if !lease.ControlValid(s.now()) {
		// The epoch elapsed; only an explicit takeover reopens input.
		return ports.ErrContinuityDenied
	}
	if lease.ControllerDeviceID != deviceID {
		return ports.ErrContinuityDenied
	}
	controller, err := s.store.GetAttachment(ctx, ownerUserID, lease.ControllerAttachmentID, deviceID)
	if err != nil {
		return ports.ErrContinuityDenied
	}
	if !controller.State.Live() || !controller.Controls || controller.DeviceID != deviceID {
		return ports.ErrContinuityDenied
	}
	return nil
}

// AuthorizeInputGeneration pins a request or peer to the control epoch it
// acquired. An old A connection remains invalid even after A later retakes B.
func (s *ContinuityService) AuthorizeInputGeneration(ctx context.Context, owner, workload, device string, generation int64) error {
	if err := s.AuthorizeInput(ctx, owner, workload, device); err != nil {
		return err
	}
	lease, found, err := s.store.Lease(ctx, workload)
	if err != nil {
		return err
	}
	if found && lease.ControlGeneration != generation {
		return ports.ErrContinuityDenied
	}
	if !found && generation != 0 {
		return ports.ErrContinuityDenied
	}
	return nil
}

func workloadKeepAlive(workload ports.InteractiveWorkload) int64 {
	if workload.LifecycleMode == 2 || workload.Kind == ports.WorkloadKindApp {
		return 0
	}
	return int64((30 * time.Minute).Seconds())
}

// GetSurfaceWorkload resolves exact owner-scoped facts, including terminal
// generations. It never starts a program or creates a device attachment.
func (s *ContinuityService) GetSurfaceWorkload(ctx context.Context, owner, id string) (ContinuitySummary, error) {
	if !domain.ValidSessionUUID(owner) || !domain.ValidSessionUUID(id) {
		return ContinuitySummary{}, domain.ErrInvalid
	}
	workload, err := s.workloads.Resolve(ctx, owner, id)
	if err != nil {
		return ContinuitySummary{}, err
	}
	counts, err := s.store.CountLiveAttachments(ctx, owner, workload.ProjectID)
	if err != nil {
		return ContinuitySummary{}, err
	}
	return ContinuitySummary{Workload: workload, Generation: max(SessionWorkloadGeneration, workload.Generation), AttachmentCount: counts[id], KeepAliveSeconds: workloadKeepAlive(workload)}, nil
}
