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

// Create resolves the installed instance through Core and persists the
// owner/device-bound session. The idempotency ruling happens before
// resolution so failed resolutions never consume the key; a replay returns
// the first session snapshot even after it closed or expired.
func (s *Service) Create(ctx context.Context, command CreateCommand) (domain.SurfaceSession, error) {
	if command.OwnerUserID == "" || command.DeviceID == "" ||
		!domain.ValidSessionIdempotencyKey(command.IdempotencyKey) ||
		!domain.ValidSessionUUID(command.ProjectID) || !domain.ValidSessionUUID(command.AppInstanceID) ||
		!domain.ValidDeviceClass(command.DeviceClass) ||
		!domain.ValidViewport(command.ViewportWidth, command.ViewportHeight, command.ViewportRatio) ||
		!domain.ValidPreferredRenderer(command.PreferredRenderer) {
		return domain.SurfaceSession{}, domain.ErrInvalid
	}
	renderer := command.PreferredRenderer
	if renderer == "" {
		renderer = domain.RendererWebBundle
	}
	digest := domain.CreateRequestDigest(command.DeviceID, command.ProjectID, command.AppInstanceID,
		command.DeviceClass, command.ViewportWidth, command.ViewportHeight, command.ViewportRatio, renderer)
	if stored, found, err := s.repository.LookupRequest(ctx, command.OwnerUserID, command.IdempotencyKey); found || err != nil {
		if err != nil {
			return domain.SurfaceSession{}, err
		}
		if stored.RequestDigest != digest {
			// A different trusted device (or any other canonical field)
			// under the same key is a stable abort, decided by the key's
			// stored digest rather than a session device lookup.
			return domain.SurfaceSession{}, domain.ErrIdempotencyConflict
		}
		return s.repository.GetSession(ctx, command.OwnerUserID, command.DeviceID, stored.SessionID)
	}
	descriptor, err := s.resolver.ResolveWebBundle(ctx, ports.ResolveQuery{
		ProjectID: command.ProjectID, AppInstanceID: command.AppInstanceID,
	})
	if err != nil {
		return domain.SurfaceSession{}, mapResolverError(err)
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
		CreatedAt: now, ExpiresAt: now.Add(s.ttl),
	}
	session.Path = domain.SessionPath(session.ID)
	return s.repository.Create(ctx, ports.CreateSessionCommand{
		Session: session, IdempotencyKey: command.IdempotencyKey, RequestDigest: digest,
	})
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
