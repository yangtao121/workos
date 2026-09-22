// Surface continuity ports (ADR-0031): the storage boundary of per-device
// attachments and single-controller leases, and the narrow interactive
// workload runtime surface the continuity application drives. The repository
// is implemented by the runtime-owned PostgreSQL adapter; the workload
// runtime by the composition root over the PTY and native runner services.
package ports

import (
	"context"
	"errors"
	"time"

	"github.com/yangtao121/workos/internal/runtime/surface/domain"
)

// WorkloadKind labels the interactive workload families that exist as
// continuous surfaces today. App workloads join through the supervised
// workload manager in a later phase; unknown kinds fail closed.
type WorkloadKind string

const (
	// WorkloadKindPty is a supervised terminal session (ADR-0028). Its
	// workload id is the PTY session id.
	WorkloadKindPty WorkloadKind = "pty"
	WorkloadKindApp WorkloadKind = "app"
	// WorkloadKindNative is a supervised virtual-display native session
	// (ADR-0029). Its workload id is the native session id.
	WorkloadKindNative WorkloadKind = "native"
)

// InteractiveWorkload is the continuity projection of one running interactive
// program: the session facts the surface continuity service lists, attaches
// to, detaches from, and stops.
type InteractiveWorkload struct {
	AppInstanceID, AppID, AppVersion string
	IdleStopSeconds                  int64
	LifecycleMode                    int32
	UpdatedAt                        time.Time
	Generation                       int64
	WorkloadID                       string
	Kind                             WorkloadKind
	OwnerUserID                      string
	ProjectID                        string
	State                            string
	Terminal                         bool
	CreatedAt                        time.Time
	ExpiresAt                        time.Time
	// Width/Height carry the native display geometry; PTY workloads keep 0.
	Width  int32
	Height int32
}

// LiveAttachmentWorkload pairs a workload that still has live attachments
// with its owner, for the bounded expiry sweep.
type LiveAttachmentWorkload struct {
	WorkloadID  string
	OwnerUserID string
}

// InteractiveWorkloadRuntime resolves and drives the owner's interactive
// workloads. Every method is owner-scoped; an unknown or foreign workload is
// ErrContinuityNotFound so callers fail closed.
type InteractiveWorkloadRuntime interface {
	// Resolve returns the owner's interactive workload by id, including
	// terminal ones so Attach can report the true stopped state.
	Resolve(ctx context.Context, ownerUserID, workloadID string) (InteractiveWorkload, error)
	// ListProject returns the owner's non-terminal interactive workloads of
	// one project.
	ListProject(ctx context.Context, ownerUserID, projectID string) ([]InteractiveWorkload, error)
	// DetachWorkload releases only the connection resources of the workload
	// (the native media peer; PTY sessions keep their attachment row as the
	// only connection state). The program keeps running.
	DetachWorkload(ctx context.Context, kind WorkloadKind, ownerUserID, workloadID, deviceID string) error
	// StopWorkload deterministically stops and reaps the program (the legacy
	// Close semantics: process group, media workers, private scratch).
	StopWorkload(ctx context.Context, kind WorkloadKind, ownerUserID, workloadID string) (InteractiveWorkload, error)
}

// AttachCommand opens one device's access relation to an existing workload.
// Until is the bounded control expiry the first-control grant stores.
type AttachCommand struct {
	Attachment domain.SurfaceAttachment
	Until      time.Time
}

// ControlVerdict carries the store's atomic control decision.
type ControlVerdict struct {
	Attachment domain.SurfaceAttachment
	Lease      domain.ControlLease
	// Renewed marks the no-generation-bump path: the same attachment already
	// held a live control lease and only its expiry moved forward.
	Renewed bool
}

// ContinuityStore owns the surface attachment and control lease rows
// (migration 059). The two command methods are transactional: no reader can
// observe a lease without its attachment flags or vice versa.
type ContinuityStore interface {
	// Attach persists the attachment (idempotent per owner/key) and, when no
	// lease exists yet, grants the first control epoch to it in the same
	// transaction. An existing lease is never altered by an attach.
	Attach(ctx context.Context, command AttachCommand) (domain.SurfaceAttachment, domain.ControlLease, error)
	// RequestControl renews the exact current controller's lease or performs
	// the explicit takeover: the generation advances atomically, the previous
	// controller attachment loses its control flag in the same transaction.
	RequestControl(ctx context.Context, attachment domain.SurfaceAttachment, now, until time.Time) (ControlVerdict, error)
	// Detach marks one attachment detached and clears its control flag. The
	// lease is untouched: it expires or is taken over.
	Detach(ctx context.Context, ownerUserID, attachmentID string, now time.Time) (domain.SurfaceAttachment, error)
	// GetAttachment reads one owner-scoped attachment row.
	GetAttachment(ctx context.Context, ownerUserID, attachmentID, deviceID string) (domain.SurfaceAttachment, error)
	// LiveAttachmentBySurfaceSession returns the device's newest live
	// attachment of one surface session (workload) - the target of Detach and
	// RequestControl by surface_session_id.
	LiveAttachmentBySurfaceSession(ctx context.Context, ownerUserID, surfaceSessionID, deviceID string) (domain.SurfaceAttachment, error)
	// Lease reads the workload's current control lease; found reports whether
	// one exists.
	Lease(ctx context.Context, workloadID string) (domain.ControlLease, bool, error)
	// CountLiveAttachments maps workload id to live attachment count for one
	// owner's project.
	CountLiveAttachments(ctx context.Context, ownerUserID, projectID string) (map[string]int32, error)
	// ListLiveAttachmentWorkloads returns every workload id and owner that
	// still has a live attachment.
	ListLiveAttachmentWorkloads(ctx context.Context) ([]LiveAttachmentWorkload, error)
	// ExpireElapsedAttachments marks attachments whose control expiry passed
	// as expired.
	ExpireElapsedAttachments(ctx context.Context, now time.Time) ([]string, error)
	// ExpireAttachmentsForWorkloads marks every live attachment of terminal
	// workloads expired.
	ExpireAttachmentsForWorkloads(ctx context.Context, workloadIDs []string, now time.Time) error
}

// Continuity sentinels. They carry no storage internals; the transport maps
// them to sanitized Connect codes.
var (
	// ErrContinuityNotFound marks an unknown, foreign, or non-live attachment
	// or workload.
	ErrContinuityNotFound = errors.New("surface continuity target is not available")
	// ErrContinuityDenied marks an input-path control check failure: the
	// device's attachment does not hold the current control epoch.
	ErrContinuityDenied = errors.New("surface control is held by another device")
	// ErrContinuityStoreUnavailable marks a transient store failure.
	ErrContinuityStoreUnavailable = errors.New("surface continuity store is temporarily unavailable")
)

// InteractiveRestarter is implemented by Runtime backends with durable
// restart receipts. Unsupported backends stay explicitly unavailable.
type InteractiveRestarter interface {
	RestartWorkload(context.Context, WorkloadKind, string, string, string, func() error, ...int32) (InteractiveWorkload, error)
}

type InteractiveStopper interface {
	StopWorkloadAction(context.Context, WorkloadKind, string, string, string, func() error) (InteractiveWorkload, error)
}

// AppDeviceCounter counts per-device web-service views of the exact generation.
// It reads only Surface-owned rows; old generations never inflate discovery.
type AppDeviceCounter interface {
	CountAppDevices(context.Context, string, string, string, int64, time.Time) (int32, error)
}
