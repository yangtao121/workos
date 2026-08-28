// Package ports defines the Surface Broker's neutral storage and resolution
// ports. The Core resolver port is implemented by a Connect client adapter;
// the repository by the runtime-owned PostgreSQL adapter.
package ports

import (
	"context"
	"errors"
	"time"

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
	// ErrStoreUnavailable marks a temporarily unreachable session store. The
	// postgres adapter wraps transient driver failures with it at the port
	// boundary; transports map it to a sanitized Unavailable. Invariant
	// failures keep their own verdicts and stay Internal.
	ErrStoreUnavailable = errors.New("surface session store is temporarily unavailable")
)

// LaunchDescriptor mirrors the Core-resolved immutable launch fact.
type LaunchDescriptor struct {
	AppID          string
	Version        string
	ManifestDigest string
	ArtifactID     string
	ArtifactDigest string
	Entrypoint     string
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
// application has already resolved the launch descriptor; the repository
// persists the session and the idempotency mapping in one transaction.
type CreateSessionCommand struct {
	Session        domain.SurfaceSession
	IdempotencyKey string
	RequestDigest  string
}

// StoredSessionRequest is the persisted result of one consumed create key.
type StoredSessionRequest struct {
	RequestDigest string
	SessionID     string
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
}
