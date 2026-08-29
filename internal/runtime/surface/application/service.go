// Package application holds the Surface Broker's use cases: durable
// idempotent session creation, owner/device-scoped close, and per-request
// asset serving that revalidates the installation through Core.
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

// TTL bounds: the configured session lifetime must stay within them.
const (
	MinSessionTTL = time.Minute
	MaxSessionTTL = 24 * time.Hour
)

// Service owns surface session lifecycle. It never trusts the snapshot for
// authorization: every asset request revalidates through the Core resolver.
type Service struct {
	repository ports.SessionRepository
	resolver   ports.LaunchResolver
	ids        ids.Generator
	ttl        time.Duration
	now        func() time.Time
}

func New(repository ports.SessionRepository, resolver ports.LaunchResolver, generator ids.Generator, sessionTTL time.Duration) (*Service, error) {
	if repository == nil || resolver == nil || generator == nil {
		return nil, errors.New("surface service requires repository, resolver, and id generator")
	}
	if sessionTTL < MinSessionTTL || sessionTTL > MaxSessionTTL {
		return nil, fmt.Errorf("surface session TTL must be between %s and %s", MinSessionTTL, MaxSessionTTL)
	}
	return &Service{
		repository: repository, resolver: resolver, ids: generator, ttl: sessionTTL,
		now: func() time.Time { return time.Now().UTC() },
	}, nil
}

// CreateCommand is one validated-boundary create request. Owner and device
// come exclusively from the trusted gateway identity, never the client body.
type CreateCommand struct {
	OwnerUserID       string
	DeviceID          string
	IdempotencyKey    string
	ProjectID         string
	AppInstanceID     string
	DeviceClass       string
	ViewportWidth     int32
	ViewportHeight    int32
	ViewportRatio     float64
	PreferredRenderer string
}

// CreatedSurface is the create result: the session snapshot plus the bridge
// credentials the trusted host only. BridgeToken is empty when no credential
// is valid (a replay of a closed or expired session), and BridgeCapabilities
// mirrors the session's effective capability list.
type CreatedSurface struct {
	Session            domain.SurfaceSession
	BridgeToken        string
	BridgeCapabilities []string
}

// Create resolves the installed instance through Core and persists the
// owner/device-bound session together with its effective bridge capabilities,
// the Core-resolved installation grant epoch, and the first bridge token. The
// idempotency ruling happens before resolution so failed resolutions never
// consume the key; a replay returns the first session snapshot even after it
// closed or expired — with a freshly rotated token only while the session is
// still open and unexpired, because a replayed create means a new trusted-host
// page load and the previous credential must stop working (ADR-0002).
// Every replay re-resolves through Core and requires the current installation
// grant epoch to equal the session's persisted epoch (ADR-0003 §3): a grant
// mutation since the first create fails the replay closed without rotating
// out a token bound to the superseded epoch — the caller must use a new
// create key.
func (s *Service) Create(ctx context.Context, command CreateCommand) (CreatedSurface, error) {
	if command.OwnerUserID == "" || command.DeviceID == "" ||
		!domain.ValidSessionIdempotencyKey(command.IdempotencyKey) ||
		!domain.ValidSessionUUID(command.ProjectID) || !domain.ValidSessionUUID(command.AppInstanceID) ||
		!domain.ValidDeviceClass(command.DeviceClass) ||
		!domain.ValidViewport(command.ViewportWidth, command.ViewportHeight, command.ViewportRatio) ||
		!domain.ValidPreferredRenderer(command.PreferredRenderer) {
		return CreatedSurface{}, domain.ErrInvalid
	}
	renderer := command.PreferredRenderer
	if renderer == "" {
		renderer = domain.RendererWebBundle
	}
	digest := domain.CreateRequestDigest(command.DeviceID, command.ProjectID, command.AppInstanceID,
		command.DeviceClass, command.ViewportWidth, command.ViewportHeight, command.ViewportRatio, renderer)
	if stored, found, err := s.repository.LookupRequest(ctx, command.OwnerUserID, command.IdempotencyKey); found || err != nil {
		if err != nil {
			return CreatedSurface{}, err
		}
		if stored.RequestDigest != digest {
			// A different trusted device (or any other canonical field)
			// under the same key is a stable abort, decided by the key's
			// stored digest rather than a session device lookup.
			return CreatedSurface{}, domain.ErrIdempotencyConflict
		}
		session, err := s.repository.GetSession(ctx, command.OwnerUserID, command.DeviceID, stored.SessionID)
		if err != nil {
			return CreatedSurface{}, err
		}
		// The replay re-resolves the authoritative grant epoch: the stored
		// snapshot alone must never authorize rotating a credential that a
		// later SetAppGrants mutation has already superseded.
		descriptor, err := s.resolveLaunch(ctx, command)
		if err != nil {
			return CreatedSurface{}, err
		}
		return s.replayBridge(ctx, command, session, descriptor.GrantRevision)
	}
	descriptor, err := s.resolveLaunch(ctx, command)
	if err != nil {
		return CreatedSurface{}, err
	}
	now := s.now()
	session := domain.SurfaceSession{
		ID: s.ids.New(), OwnerUserID: command.OwnerUserID, DeviceID: command.DeviceID,
		IdempotencyKey: command.IdempotencyKey, RequestDigest: digest,
		ProjectID: command.ProjectID, AppInstanceID: command.AppInstanceID,
		Renderer: domain.RendererWebBundle,
		Descriptor: domain.LaunchDescriptor{
			AppID: descriptor.AppID, Version: descriptor.Version,
			ManifestDigest: descriptor.ManifestDigest, ArtifactID: descriptor.ArtifactID,
			ArtifactDigest: descriptor.ArtifactDigest, Entrypoint: descriptor.Entrypoint,
		},
		BridgeCapabilities: domain.EffectiveBridgeCapabilities(descriptor.GrantedPermissions),
		// The pinned authorization epoch is exactly what Core resolved —
		// never a constant and never a client input (ADR-0003 §7).
		InstallationGrantRevision: descriptor.GrantRevision,
		CreatedAt:                 now, ExpiresAt: now.Add(s.ttl),
	}
	session.Path = domain.SessionPath(session.ID)
	token, err := domain.NewBridgeToken()
	if err != nil {
		return CreatedSurface{}, err
	}
	created, err := s.repository.Create(ctx, ports.CreateSessionCommand{
		Session: session, BridgeTokenHash: domain.HashBridgeToken(token),
		IdempotencyKey: command.IdempotencyKey, RequestDigest: digest,
	})
	if err != nil {
		return CreatedSurface{}, err
	}
	if created.BridgeTokenHash == domain.HashBridgeToken(token) {
		return CreatedSurface{Session: created, BridgeToken: token, BridgeCapabilities: created.BridgeCapabilities}, nil
	}
	// A concurrent same-key create won the linearization race inside the
	// repository and returned its session: the local token above was never
	// persisted, so returning it would hand out an unverifiable credential.
	// Rotate instead — the returned token becomes a real, recorded rotation
	// on the winning session (and stays empty for a closed/expired winner).
	// The same epoch check guards this concurrent-replay form: if the winner
	// persisted a different grant epoch than this request resolved, the
	// response fails closed instead of minting a superseded-epoch credential.
	return s.replayBridge(ctx, command, created, descriptor.GrantRevision)
}

// resolveLaunch resolves the launch facts through Core and enforces the
// grant-epoch trust invariant. A resolution whose GrantRevision is below 1 is
// an untrusted resolution — a stored-fact invariant drift, not a client
// error — so the surface fails closed with the sanitized ErrResolverCorrupt
// verdict (Internal at transport) instead of persisting or comparing epoch 0.
// The database CHECK would also reject 0, but the application layer refuses
// first with a clean, fixed error.
func (s *Service) resolveLaunch(ctx context.Context, command CreateCommand) (ports.LaunchDescriptor, error) {
	descriptor, err := s.resolver.ResolveWebBundle(ctx, ports.ResolveQuery{
		ProjectID: command.ProjectID, AppInstanceID: command.AppInstanceID,
	})
	if err != nil {
		return ports.LaunchDescriptor{}, mapResolverError(err)
	}
	if descriptor.GrantRevision < 1 {
		return ports.LaunchDescriptor{}, fmt.Errorf("resolve installed instance: %w", ports.ErrResolverCorrupt)
	}
	return descriptor, nil
}

// replayBridge rotates the bridge credential for an open, unexpired replayed
// session and clears it for anything else: a closed or expired session never
// regains a working credential through replay. The rotation and the read are
// one atomic UPDATE ... RETURNING — the operation's linearization point — so
// the returned snapshot is the persisted fact backing exactly the returned
// token. Concurrent rotations serialize on the row and each response pairs
// its credential with the hash it was stored under; a later rotation
// invalidates an earlier response's credential through a recorded rotation,
// never through an inconsistent pairing. A replay whose freshly resolved
// grant epoch no longer equals the session's persisted epoch fails closed
// before any rotation (ADR-0003 §3): no token bound to the superseded epoch
// is ever minted, and the caller must open a new surface under a new key.
func (s *Service) replayBridge(ctx context.Context, command CreateCommand, session domain.SurfaceSession, resolvedGrantRevision int64) (CreatedSurface, error) {
	if resolvedGrantRevision != session.InstallationGrantRevision {
		return CreatedSurface{}, domain.ErrGrantEpochStale
	}
	result := CreatedSurface{Session: session, BridgeCapabilities: session.BridgeCapabilities}
	now := s.now()
	if session.ClosedAt != nil || !now.Before(session.ExpiresAt) {
		return result, nil
	}
	token, err := domain.NewBridgeToken()
	if err != nil {
		return CreatedSurface{}, err
	}
	stored, err := s.repository.RotateBridgeToken(ctx, ports.RotateBridgeTokenCommand{
		OwnerUserID: command.OwnerUserID, DeviceID: command.DeviceID, SessionID: session.ID,
		TokenHash: domain.HashBridgeToken(token), Now: now,
	})
	if err != nil {
		return CreatedSurface{}, err
	}
	result.Session = stored
	result.BridgeToken = token
	return result, nil
}

// Close tombstones the owner/device-bound session. Repeated closes by the
// same owner/device succeed without changing the first result.
func (s *Service) Close(ctx context.Context, ownerUserID, deviceID, sessionID string) (domain.SurfaceSession, error) {
	if ownerUserID == "" || deviceID == "" || !domain.ValidSessionUUID(sessionID) {
		return domain.SurfaceSession{}, domain.ErrInvalid
	}
	return s.repository.Close(ctx, ownerUserID, deviceID, sessionID, s.now())
}

// ServeAsset returns one bounded asset of an open, unexpired session. The
// session check is owner/device-bound, and Core revalidates the installation
// on every call, so closed/expired/uninstalled instances fail closed.
func (s *Service) ServeAsset(ctx context.Context, ownerUserID, deviceID, sessionID, assetPath string) (ports.Asset, error) {
	if ownerUserID == "" || deviceID == "" || !domain.ValidSessionUUID(sessionID) {
		return ports.Asset{}, domain.ErrNotFound
	}
	normalized, err := domain.NormalizeAssetPath(assetPath)
	if err != nil {
		return ports.Asset{}, domain.ErrNotFound
	}
	session, err := s.repository.GetActiveSession(ctx, ownerUserID, deviceID, sessionID, s.now())
	if err != nil {
		if errors.Is(err, ports.ErrStoreUnavailable) {
			// The session store is temporarily unreachable: this is a 503,
			// never a "resource not found" 404.
			return ports.Asset{}, domain.ErrUnavailable
		}
		return ports.Asset{}, err
	}
	asset, err := s.resolver.ReadWebBundleAsset(ctx, ports.AssetQuery{
		ProjectID: session.ProjectID, AppInstanceID: session.AppInstanceID, AssetPath: normalized,
	})
	if err != nil {
		return ports.Asset{}, mapResolverError(err)
	}
	return asset, nil
}

// mapResolverError converts resolver port verdicts into surface domain
// errors; anything else is an infrastructure failure surfaced as Internal by
// transport, never as a domain verdict.
func mapResolverError(err error) error {
	switch {
	case errors.Is(err, ports.ErrResolverNotFound):
		return domain.ErrNotFound
	case errors.Is(err, ports.ErrResolverUnsupported):
		return domain.ErrUnsupported
	case errors.Is(err, ports.ErrResolverUnavailable):
		return domain.ErrUnavailable
	default:
		return fmt.Errorf("resolve installed instance: %w", err)
	}
}
