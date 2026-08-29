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
	// WatchAgentTaskEvents streams persisted events from Core until the task
	// reaches its terminal state or the context is canceled. Canceling only
	// ends the stream; the durable Agent task itself continues.
	WatchAgentTaskEvents(ctx context.Context, query AppAgentWatchQuery, onEvent func(*agentv1.AgentEvent) error) error
}
