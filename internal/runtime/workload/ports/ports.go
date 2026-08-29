// Package ports defines the Workload Manager's neutral engine, cgroup,
// health, and storage ports. Production engine implementations live in
// adapters (Podman only ever appears there); tests use deterministic fakes.
package ports

import (
	"context"
	"errors"
	"time"

	"github.com/yangtao121/workos/internal/runtime/workload/domain"
)

// Engine capability facts: the runtime refuses to run containers unless every
// enforced boundary is verified present. A missing piece is a hard
// unavailable, never a fallback trigger (ADR-0006 §3/§4).
type Capability struct {
	Available     bool
	Rootless      bool
	CgroupV2      bool
	Reason        string
	EngineVersion string
	CgroupRoot    string // this process's delegated cgroup v2 subtree
}

// ContainerSpec is the complete, server-owned launch specification. The
// engine adapter translates it into argv; nothing here comes from the client
// except the descriptor facts Core resolved from the pinned manifest.
type ContainerSpec struct {
	Name    string
	Image   string
	Command []string
	Port    int64
	Labels  map[string]string
	Policy  domain.EffectivePolicy
}

// ContainerFacts is one bounded engine inspection read.
type ContainerFacts struct {
	ID       string
	Name     string
	Running  bool
	ExitCode int
	PID      int
	Labels   map[string]string
	// HostIP/HostPort carry the published loopback endpoint when present.
	HostIP   string
	HostPort int32
	// OOMKilled reports the engine's own OOM verdict for the last exit.
	OOMKilled bool
}

// CgroupCounters is one bounded numeric read of the workload's cgroup.
type CgroupCounters struct {
	CPUUsageUSec  uint64
	MemoryCurrent uint64
	MemoryPeak    uint64
	MemoryOOMs    uint64
	PIDsCurrent   uint64
	PIDsPeak      uint64
}

// EffectiveFacts is the enforced-policy read-back: the values as the kernel
// actually applied them. Startup verification compares them with the
// effective policy and refuses to report running on any drift.
type EffectiveFacts struct {
	CPUMaxUSec int64 // quota portion of cpu.max (period fixed by policy)
	MemoryHigh int64
	MemoryMax  int64
	PIDsMax    int64
}

var (
	// ErrEngineUnavailable marks a temporarily unreachable engine.
	ErrEngineUnavailable = errors.New("container engine is unavailable")
	// ErrContainerNotFound marks an exact-name/ID inspection miss.
	ErrContainerNotFound = errors.New("container is not present")
	// ErrDrift marks a Core re-validation whose digest no longer matches the
	// workload's pinned digest: stored-fact corruption, never a verdict.
	ErrDrift = errors.New("workload installation facts drifted")
)

// Engine is the neutral container engine port. Implementations must invoke
// the engine through argv with bounded output and deadlines, never through a
// shell, and must never pull images.
type Engine interface {
	// Probe verifies rootless mode, cgroup v2, and the delegated subtree.
	Probe(ctx context.Context) (Capability, error)
	// ImageExists reports whether the exact digest-pinned reference exists
	// locally. It never pulls.
	ImageExists(ctx context.Context, image string) (bool, error)
	// CreateContainer creates the container without starting it. A name
	// conflict surfaces ErrContainerAlreadyExists.
	CreateContainer(ctx context.Context, spec ContainerSpec) (string, error)
	// StartContainer starts an existing (created or stopped) container.
	StartContainer(ctx context.Context, nameOrID string) error
	// StopContainer stops a running container within the bounded timeout.
	StopContainer(ctx context.Context, nameOrID string, timeout time.Duration) error
	// RemoveContainer removes a container (any state) by exact identity.
	RemoveContainer(ctx context.Context, nameOrID string) error
	// InspectContainer reads the bounded facts of one container.
	InspectContainer(ctx context.Context, nameOrID string) (ContainerFacts, error)
	// ListManagedContainers lists every container carrying the exact WorkOS
	// management label, in any state.
	ListManagedContainers(ctx context.Context) ([]ContainerFacts, error)
}

var ErrContainerAlreadyExists = errors.New("container already exists")

// CgroupReader reads the real cgroup v2 facts of one workload. Paths arrive
// from engine inspection and are validated by the domain before use.
type CgroupReader interface {
	// SelfSubtree returns this process's delegated cgroup v2 subtree root.
	SelfSubtree() (string, error)
	// CgroupPathForPID resolves the host cgroup v2 path of one process.
	CgroupPathForPID(pid int) (string, error)
	// ReadEffective reads cpu.max/memory.high/memory.max/pids.max.
	ReadEffective(ctx context.Context, path string) (EffectiveFacts, error)
	// ReadCounters reads the bounded numeric counters.
	ReadCounters(ctx context.Context, path string) (CgroupCounters, error)
}

// HealthVerdict is the bounded result of one startup/ongoing probe.
type HealthResult struct {
	Verdict string // domain.HealthOK | domain.HealthFailing
}

// HealthProber probes the workload's HTTP health endpoint. It follows no
// redirects and reads no body.
type HealthProber interface {
	Probe(ctx context.Context, endpoint, httpPath string, timeout time.Duration) (HealthResult, error)
}

// LaunchQuery identifies the installed instance a workload serves.
type LaunchQuery struct {
	OwnerUserID   string
	ProjectID     string
	AppInstanceID string
	// ManifestDigest is the digest the caller resolved; the verdict is
	// computed against the workload's pinned digest.
	ManifestDigest string
}

// LaunchVerdict classifies the Core re-validation outcome for a workload.
type LaunchVerdict string

const (
	LaunchInstalled LaunchVerdict = "installed"
	LaunchGone      LaunchVerdict = "gone"    // definitive NotFound
	LaunchUnknown   LaunchVerdict = "unknown" // transient Core failure
)

// InstallationVerifier re-validates the installation through Core. It is the
// only cross-module dependency of the Workload Manager, satisfied by the
// surface module's Core resolver client.
type InstallationVerifier interface {
	// VerifyLaunch maps: definitive NotFound → LaunchGone; transient
	// failures → LaunchUnknown; anything installed (with any digest) →
	// LaunchInstalled. A digest mismatch is stored-fact corruption and is
	// reported as ErrCorrupt, never folded into a verdict.
	VerifyLaunch(ctx context.Context, query LaunchQuery) (LaunchVerdict, error)
}

// SurfaceReferenceSource answers whether any open, unexpired surface session
// currently references the installed instance. It is implemented by the
// surface module and consumed for idle-TTL decisions.
type SurfaceReferenceSource interface {
	HasActiveSurface(ctx context.Context, ownerUserID, appInstanceID string) (bool, error)
}

// EnsureCommand is one validated ensure request.
type EnsureCommand struct {
	OwnerUserID    string
	ProjectID      string
	AppInstanceID  string
	AppID          string
	AppVersion     string
	ManifestDigest string
	Image          string
	Command        []string
	Port           int64
	Requested      domain.RequestedPolicy
	OperationKey   string
}

// RestartCommand is one validated restart request (reliability-driven).
type RestartCommand struct {
	WorkloadID   string
	OperationKey string
}

// TerminateCommand is one validated terminate request.
type TerminateCommand struct {
	WorkloadID   string
	OperationKey string
	Reason       string // fixed grammar: policy|restart_limit|uninstalled|idle|fail_safe
}

// Observation is the neutral, bounded read of one supervised workload. It
// carries no host endpoint, no cgroup path, no container ID, and no content:
// exactly what the reliability policy engine may see.
type Observation struct {
	WorkloadID     string
	OwnerUserID    string
	ProjectID      string
	AppInstanceID  string
	AppID          string
	ManifestDigest string
	Generation     int64
	State          domain.State
	RestartCount   int64
	HealthVerdict  string
	ExitCategory   string
	OOMKilled      bool
	Idle           bool
	CPUUsageUSec   uint64
	MemoryCurrent  uint64
	MemoryPeak     uint64
	MemoryOOMs     uint64
	PIDsCurrent    uint64
	PIDsPeak       uint64
	ObservedAt     time.Time
}

// StoredOperation is the persisted command record used for idempotency
// rulings.
type StoredOperation struct {
	WorkloadID       string
	OperationKey     string
	Operation        domain.Operation
	RequestDigest    string
	ResultState      domain.State
	ResultGeneration int64
	ErrorKind        domain.ErrorKind
}

// WorkloadRepository owns the durable workload facts. Implementations wrap
// transient driver failures with ErrStoreUnavailable and arbitrate same-key
// races through the operation primary key inside the create transaction.
type WorkloadRepository interface {
	// Get returns the workload by exact ID.
	Get(ctx context.Context, workloadID string) (domain.Workload, error)
	// GetActiveByInstance returns the active workload of one installed
	// instance, if any.
	GetActiveByInstance(ctx context.Context, ownerUserID, appInstanceID string) (domain.Workload, error)
	// List returns every workload, oldest first, bounded.
	List(ctx context.Context, limit int) ([]domain.Workload, error)
	// ReserveEnsure inserts the workload row (state starting) and the ensure
	// operation row in one transaction. If the active-slot unique index or
	// the operation key already exists it returns the existing facts with
	// reserved=false.
	ReserveEnsure(ctx context.Context, workload domain.Workload, op domain.WorkloadOperation) (reserved bool, err error)
	// LookupOperation returns the stored operation record, if any.
	LookupOperation(ctx context.Context, workloadID, operationKey string) (StoredOperation, error)
	// RecordOperation persists or updates an operation's final verdict.
	RecordOperation(ctx context.Context, op domain.WorkloadOperation) error
	// Transition applies a guarded state transition with fact updates; the
	// implementation must refuse to move a row out of a terminal state.
	Transition(ctx context.Context, workloadID string, from, to domain.State, facts WorkloadFacts, now time.Time) error
	// ClaimLease extends (or acquires) the reconcile lease of one workload.
	ClaimLease(ctx context.Context, workloadID, owner string, until time.Time) (bool, error)
}

// WorkloadFacts is the mutable fact bundle of one transition.
type WorkloadFacts struct {
	ContainerID   string
	Endpoint      string
	CgroupPath    string
	Generation    int64
	RestartCount  int64
	HealthVerdict string
	LastExit      string
	BaselineOOM   uint64
	BaselinePids  uint64
	StartedAt     *time.Time
	StoppedAt     *time.Time
	VerifiedAt    *time.Time // successful Core re-validation stamp
	ClearEngine   bool       // clear container facts (terminal transitions)
}

// ErrStoreUnavailable marks a temporarily unreachable workload store.
var ErrStoreUnavailable = errors.New("workload store is temporarily unavailable")

// Completed reports whether the stored operation recorded a final verdict.
func (o StoredOperation) Completed() bool {
	return o.ResultState != "" || o.ErrorKind != ""
}

// Retryable reports whether a completed-with-transient-failure operation may
// be re-driven under the same key (failures never consume the key).
func (o StoredOperation) Retryable() bool {
	return o.ErrorKind == domain.ErrorFailed || o.ErrorKind == domain.ErrorUnavailable
}
