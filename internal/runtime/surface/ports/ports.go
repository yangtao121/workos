// Package ports defines the Surface Broker's neutral storage and resolution
// ports. The Core resolver port is implemented by a Connect client adapter;
// the repository by the runtime-owned PostgreSQL adapter.
package ports

import (
	"context"
	"errors"
	"time"

	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"

	"github.com/yangtao121/workos/internal/runtime/surface/domain"
)

// Resolver sentinels classify Core verdicts for the application layer. They
// carry no Core internals: transport maps them to sanitized Connect codes or
// fixed HTTP statuses.
var (
	// ErrResolverUnavailable marks a temporarily unreachable Core resolver.
	ErrResolverUnavailable = errors.New("core surface resolver is unavailable")
	// ErrResolverNotFound marks an unknown, foreign, archived, tombstoned, or
	// otherwise not-launchable installation.
	ErrResolverNotFound = errors.New("installed instance is not launchable")
	// ErrResolverUnsupported marks an installed app whose pinned version has
	// no supported web bundle descriptor.
	ErrResolverUnsupported = errors.New("installed app has no supported web bundle")
	// ErrResolverCorrupt marks a Core resolution that violates a stored-fact
	// invariant — most importantly an installation grant revision below 1
	// (ADR-0003 §7: the revision is authoritative and must never reach storage
	// as an unusable epoch). It is a sanitized Internal, never persisted and
	// never compared: the surface fails closed instead of trusting it.
	ErrResolverCorrupt = errors.New("core surface resolver returned an inconsistent resolution")
	// ErrStoreUnavailable marks a temporarily unreachable session store. The
	// postgres adapter wraps transient driver failures with it at the port
	// boundary; transports map it to a sanitized Unavailable. Invariant
	// failures keep their own verdicts and stay Internal.
	ErrStoreUnavailable = errors.New("surface session store is temporarily unavailable")
)

// LaunchDescriptor mirrors the Core-resolved immutable launch fact.
// GrantedPermissions is the installation grant snapshot re-read from Core on
// every resolution; it feeds effective bridge capability computation only.
// GrantRevision is the authoritative installation grant epoch read from the
// same Core facts: the application persists it into the surface session and
// derives every private authorization comparison from that snapshot; public
// inputs can never supply or override it.
type LaunchDescriptor struct {
	AppID              string
	Version            string
	ManifestDigest     string
	ArtifactID         string
	ArtifactDigest     string
	Entrypoint         string
	GrantedPermissions []string
	GrantRevision      int64
}

// LaunchKind distinguishes the supported launch profiles of one installed
// instance; the server selects it from the exact pinned descriptor.
type LaunchKind string

const (
	// LaunchKindWebBundle marks the immutable web bundle profile.
	LaunchKindWebBundle LaunchKind = "web-bundle"
	// LaunchKindWebServiceContainer marks the supervised digest-pinned
	// container profile (ADR-0006).
	LaunchKindWebServiceContainer LaunchKind = "web-service-container"
)

// ContainerPolicy is the App's requested resource policy as Core resolved it.
// The runtime adjudicates it against server-owned maxima before any engine
// side effect; these values never size a container directly.
type ContainerPolicy struct {
	CPUHardCores float64
	MemoryHighMB int64
	MemoryMaxMB  int64
	PidsMax      int64
}

// HealthPolicy is the App's requested health policy as Core resolved it.
type HealthPolicy struct {
	HTTPPath       string
	StartupSeconds int64
	RestartLimit   int64
}

// ResolvedLaunch is the generic Core-resolved launch fact. Exactly one
// profile side is populated.
type ResolvedLaunch struct {
	Kind               LaunchKind
	AppID              string
	Version            string
	ManifestDigest     string
	GrantedPermissions []string
	GrantRevision      int64
	// Web bundle profile.
	ArtifactID     string
	ArtifactDigest string
	Entrypoint     string
	// Container profile.
	Image     string
	Command   []string
	Port      int64
	Resources ContainerPolicy
	Health    HealthPolicy
	Route     string
}

// ResolveQuery identifies one installed instance for launch resolution.
type ResolveQuery struct {
	ProjectID     string
	AppInstanceID string
}

// AssetQuery identifies one normalized asset of an installed instance.
type AssetQuery struct {
	ProjectID     string
	AppInstanceID string
	AssetPath     string
}

// Asset is one bounded asset read: exact bytes, the server-derived media
// type, and the digest etag.
type Asset struct {
	Content   []byte
	MediaType string
	Etag      string
}

// LaunchResolver resolves installed instances through the private Core
// service. Implementations must forward the trusted owner/device identity
// from the context and map transport failures to the sentinels above.
type LaunchResolver interface {
	ResolveWebBundle(ctx context.Context, query ResolveQuery) (LaunchDescriptor, error)
	ReadWebBundleAsset(ctx context.Context, query AssetQuery) (Asset, error)
	// ResolveSurfaceLaunch is the renderer-neutral resolution. The runtime
	// selects the supported renderer from the resolved kind; explicit client
	// renderers must match it exactly.
	ResolveSurfaceLaunch(ctx context.Context, query ResolveQuery) (ResolvedLaunch, error)
}

// CreateSessionCommand is one fully validated create command. The
// application has already resolved the launch descriptor, computed the
// effective bridge capabilities, and minted the initial bridge token; the
// repository persists the session (with its token digest) and the idempotency
// mapping in one transaction.
type CreateSessionCommand struct {
	Session         domain.SurfaceSession
	BridgeTokenHash string
	IdempotencyKey  string
	RequestDigest   string
}

// StoredSessionRequest is the persisted result of one consumed create key.
type StoredSessionRequest struct {
	RequestDigest string
	SessionID     string
}

// RotateBridgeTokenCommand stores the digest of one freshly minted bridge
// token, invalidating the previous credential atomically.
type RotateBridgeTokenCommand struct {
	OwnerUserID string
	DeviceID    string
	SessionID   string
	TokenHash   string
	Now         time.Time
}

// SessionRepository owns the surface session facts. Same-key races are
// arbitrated by the request-mapping primary key inside the create
// transaction.
type SessionRepository interface {
	// LookupRequest returns the stored result when the key was consumed.
	LookupRequest(ctx context.Context, ownerUserID, idempotencyKey string) (StoredSessionRequest, bool, error)
	// GetSession returns the owner/device-bound session in any state
	// (replay projection source).
	GetSession(ctx context.Context, ownerUserID, deviceID, sessionID string) (domain.SurfaceSession, error)
	// GetActiveSession returns the session only while it is open and
	// unexpired at the given instant.
	GetActiveSession(ctx context.Context, ownerUserID, deviceID, sessionID string, now time.Time) (domain.SurfaceSession, error)
	// Create persists one session plus its request mapping; a consumed key
	// replays or conflicts exactly like the install path.
	Create(ctx context.Context, command CreateSessionCommand) (domain.SurfaceSession, error)
	// Close tombstones the session on first close; a repeated same
	// owner/device close is a successful no-op; anything else is NotFound.
	Close(ctx context.Context, ownerUserID, deviceID, sessionID string, now time.Time) (domain.SurfaceSession, error)
	// RotateBridgeToken stores the digest of a freshly minted bridge token on
	// the open, unexpired, owner/device-bound session and returns that
	// session's row as of this rotation — one atomic UPDATE ... RETURNING is
	// the operation's linearization point, so the returned snapshot is the
	// persisted fact backing exactly this credential even when further
	// rotations follow. Anything else is NotFound and nothing changes.
	RotateBridgeToken(ctx context.Context, command RotateBridgeTokenCommand) (domain.SurfaceSession, error)
	// HasActiveSurface answers whether any open, unexpired session still
	// references the installed instance (the Workload Manager's idle source).
	HasActiveSurface(ctx context.Context, ownerUserID, appInstanceID string, now time.Time) (bool, error)
	// GetActiveSessionByBridgeToken resolves the open, unexpired session
	// currently carrying the token digest, owner-scoped.
	GetActiveSessionByBridgeToken(ctx context.Context, ownerUserID, tokenHash string, now time.Time) (domain.SurfaceSession, error)
}

// AppTaskSubmission is the canonical result of one bridge task creation,
// projected from the Core App Agent response.
type AppTaskSubmission struct {
	TaskID            string
	State             string
	LastEventSequence int64
}

// AppAgentRunQuery identifies one bridge task creation. Every field is
// derived server-side from the validated token and session — including the
// installation grant epoch, which comes exclusively from the persisted
// session snapshot and can never be submitted by a public bridge body,
// MessageChannel envelope, or iframe SDK.
type AppAgentRunQuery struct {
	ProjectID     string
	AppInstanceID string
	// InstallationGrantRevision is the session's create-time grant epoch.
	// Core compares it for exact equality against the active installation's
	// current revision on every run call (ADR-0003 §7).
	InstallationGrantRevision int64
	ClientKey                 string
	Role                      string
	Goal                      string
}

// AppAgentWatchQuery identifies one bridge event watch resume. As with the
// run query, every field is derived server-side from the validated session.
type AppAgentWatchQuery struct {
	ProjectID     string
	AppInstanceID string
	TaskID        string
	AfterSequence int64
	// InstallationGrantRevision is the session's create-time grant epoch,
	// compared by Core for exact equality on every watch polling round.
	InstallationGrantRevision int64
}

// AppAgent sentinels classify Core App Agent verdicts for the bridge path.
var (
	// ErrAppAgentUnavailable marks a temporarily unreachable Core service.
	ErrAppAgentUnavailable = errors.New("core app agent service is unavailable")
	// ErrAppAgentDenied marks a sanitized Core denial: the installation,
	// grant, or provenance re-validation failed server-side after the
	// runtime checks passed. It carries no Core internals.
	ErrAppAgentDenied = errors.New("core app agent denied the request")
	// ErrAppAgentExhausted marks the Core quota verdict: the installation's
	// UTC daily allowance is exhausted or circuit-broken (ADR-0005).
	ErrAppAgentExhausted = errors.New("core app agent reported the daily allowance as exhausted")
	// ErrAppAgentConflict marks the Core idempotency verdict: the same app
	// client key was already consumed by a different canonical request.
	ErrAppAgentConflict = errors.New("core app agent reported an idempotency conflict")
)

// AppAgentClient is the private Core App Agent transport port. Implementations
// forward the trusted owner/device identity from the context and map transport
// failures to the sentinels above. Event callbacks receive the canonical
// generated event type — the runtime never defines a second DTO.
type AppAgentClient interface {
	RunAgentTask(ctx context.Context, query AppAgentRunQuery) (AppTaskSubmission, error)
	// AuthorizeAppKnowledge re-verifies, per call, the active installation,
	// the project binding, and the exact current grant revision for the
	// read-only knowledge.search method. It returns the trusted owner/project
	// binding; every denial is one sanitized verdict (ADR-0013).
	AuthorizeAppKnowledge(ctx context.Context, query AppKnowledgeAuthQuery) (AppKnowledgeBinding, error)
	// WatchAgentTaskEvents streams persisted events from Core until the task
	// reaches its terminal state or the context is canceled. Canceling only
	// ends the stream; the durable Agent task itself continues.
	WatchAgentTaskEvents(ctx context.Context, query AppAgentWatchQuery, onEvent func(*agentv1.AgentEvent) error) error
}

var (
	// ErrKnowledgeUnavailable marks a temporarily unreachable indexer.
	ErrKnowledgeUnavailable = errors.New("knowledge index is temporarily unavailable")
	// ErrKnowledgeMalformed marks a malformed indexer response.
	ErrKnowledgeMalformed = errors.New("knowledge index response is malformed")
)

// AppKnowledgeAuthQuery carries the session-derived facts Core re-verifies.
type AppKnowledgeAuthQuery struct {
	ProjectID                 string
	AppInstanceID             string
	InstallationGrantRevision int64
}

// AppKnowledgeBinding is the authoritative allow binding returned by Core.
type AppKnowledgeBinding struct {
	OwnerUserID string
	ProjectID   string
}

// KnowledgeHit is one sanitized search hit the indexer returned.
type KnowledgeHit struct {
	ArtifactID   string
	Digest       string
	ArtifactType string
	Title        string
	Excerpt      string
	Score        float64
	CreatedAt    string
}

// KnowledgeSearchPage is one bounded page of hits plus continuation.
type KnowledgeSearchPage struct {
	Hits          []KnowledgeHit
	NextPageToken string
	CaughtUp      bool
}

// KnowledgeSearchQuery is the fully derived, scoped search call.
type KnowledgeSearchQuery struct {
	OwnerUserID string
	ProjectID   string
	Query       string
	PageSize    int32
	PageToken   string
}

// KnowledgeSearchClient is the scoped indexer adapter. The runtime derives
// owner/project exclusively from the validated surface session; the app can
// never influence the scope.
type KnowledgeSearchClient interface {
	Search(ctx context.Context, query KnowledgeSearchQuery) (KnowledgeSearchPage, error)
}

// SurfaceWorkloadQuery is the fully validated container launch input the
// surface hands the runtime's Workload Manager. The operation key is derived
// server-side from the create key so the ensure is idempotent with the same
// lifecycle.
type SurfaceWorkloadQuery struct {
	OwnerUserID    string
	ProjectID      string
	AppInstanceID  string
	AppID          string
	AppVersion     string
	ManifestDigest string
	Image          string
	Command        []string
	Port           int64
	Resources      ContainerPolicy
	Health         HealthPolicy
	OperationKey   string
}

// WorkloadHandle is the verified launch target of a web-service session: the
// durable workload identity, its exact generation, and the loopback endpoint
// the server verified at start. The endpoint is a host fact; it is consumed
// by the proxy boundary only and is never projected into any public response.
type WorkloadHandle struct {
	ID         string
	Generation int64
	Endpoint   string
}

// WorkloadRuntime is the narrow Workload Manager surface the broker needs.
// Composition roots adapt the concrete manager; tests use deterministic
// fakes.
type WorkloadRuntime interface {
	// EnsureSurfaceWorkload returns a running, startup-healthy workload for
	// the exact pinned descriptor, launching one when none is live.
	EnsureSurfaceWorkload(ctx context.Context, query SurfaceWorkloadQuery) (WorkloadHandle, error)
	// LookupSurfaceWorkload returns the workload's endpoint only when it is
	// running with the exact expected generation; any drift or terminal
	// state fails closed.
	LookupSurfaceWorkload(ctx context.Context, workloadID string, generation int64) (WorkloadHandle, error)
}

// ProxyTarget is the resolved backend of one proxy request: a server-owned,
// loopback-verified endpoint plus the normalized backend path and the owning
// session prefix for verified redirect rewrites.
type ProxyTarget struct {
	SessionID   string
	Endpoint    string
	BackendPath string
}

// Workload verdict sentinels at the surface boundary. The WorkloadRuntime
// adapter maps the manager's domain verdicts onto these; the application maps
// them onto surface domain errors. They carry no engine detail.
var (
	ErrWorkloadUnsupported       = errors.New("workload launch is not supported")
	ErrWorkloadImageMissing      = errors.New("pinned image is not available locally")
	ErrWorkloadRunnerUnavailable = errors.New("verified rootless container capability is unavailable")
	ErrWorkloadUnavailable       = errors.New("workload manager is temporarily unavailable")
	ErrWorkloadConflict          = errors.New("workload operation key was used for a different command")
)

// SurfaceContentKind distinguishes the two read-only serving paths of one
// session: the immutable web-bundle asset read and the revalidated
// web-service proxy round trip.
type SurfaceContentKind string

const (
	ContentAsset SurfaceContentKind = "asset"
	ContentProxy SurfaceContentKind = "proxy"
)

// SurfaceContent is the serving verdict for one surface request: exactly one
// side is populated.
type SurfaceContent struct {
	Kind  SurfaceContentKind
	Asset Asset
	Proxy ProxyTarget
}
