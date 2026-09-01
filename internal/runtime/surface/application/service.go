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
// authorization: every asset/proxy request revalidates through the Core
// resolver.
type Service struct {
	repository ports.SessionRepository
	resolver   ports.LaunchResolver
	workloads  ports.WorkloadRuntime
	ids        ids.Generator
	ttl        time.Duration
	now        func() time.Time
	// knowledgeConfigured reports whether the runtime holds a configured
	// indexer adapter. Only then can `knowledge.read` grants negotiate the
	// read-only knowledge.search bridge method (ADR-0013).
	knowledgeConfigured bool
}

func New(repository ports.SessionRepository, resolver ports.LaunchResolver, generator ids.Generator, sessionTTL time.Duration) (*Service, error) {
	return NewWithWorkloads(repository, resolver, nil, generator, sessionTTL)
}

// WithKnowledgeConfigured marks the runtime's knowledge.search executor as
// configured. Without it the method is never negotiated, no matter which
// grants the installation holds.
func (s *Service) WithKnowledgeConfigured() *Service {
	s.knowledgeConfigured = true
	return s
}

// NewWithWorkloads wires the broker with the runtime's Workload Manager.
// A nil WorkloadRuntime keeps the broker web-bundle only: web-service creates
// fail closed with the sanitized unsupported verdict, never with a fake
// launch.
func NewWithWorkloads(repository ports.SessionRepository, resolver ports.LaunchResolver, workloads ports.WorkloadRuntime, generator ids.Generator, sessionTTL time.Duration) (*Service, error) {
	if repository == nil || resolver == nil || generator == nil {
		return nil, errors.New("surface service requires repository, resolver, and id generator")
	}
	if sessionTTL < MinSessionTTL || sessionTTL > MaxSessionTTL {
		return nil, fmt.Errorf("surface session TTL must be between %s and %s", MinSessionTTL, MaxSessionTTL)
	}
	return &Service{
		repository: repository, resolver: resolver, workloads: workloads,
		ids: generator, ttl: sessionTTL,
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
	// The key ruling comes first: a store outage is an Unavailable, and a
	// failed resolution never consumes the key because nothing is persisted
	// on this path.
	stored, found, err := s.repository.LookupRequest(ctx, command.OwnerUserID, command.IdempotencyKey)
	if err != nil {
		return CreatedSurface{}, err
	}
	// The generic resolution is authoritative for both the renderer verdict
	// and the digest candidates: an auto request is server-selected, so its
	// canonical form carries the resolved kind, and an explicit request must
	// match the resolved kind exactly.
	resolved, err := s.resolveGeneric(ctx, command)
	if err != nil {
		return CreatedSurface{}, err
	}
	renderer, replayCandidates, err := s.rendererRuling(command.PreferredRenderer, resolved.Kind)
	if err != nil {
		return CreatedSurface{}, err
	}
	if found {
		// A different canonical request under the same key is a stable
		// abort. The candidate list carries the legacy v1 mapping so a key
		// consumed by a pre-auto create still replays exactly (ADR-0006).
		matched := false
		for _, candidate := range replayCandidates {
			if stored.RequestDigest == s.createDigest(command, candidate) {
				matched = true
				break
			}
		}
		if !matched {
			return CreatedSurface{}, domain.ErrIdempotencyConflict
		}
		session, err := s.repository.GetSession(ctx, command.OwnerUserID, command.DeviceID, stored.SessionID)
		if err != nil {
			return CreatedSurface{}, err
		}
		// The replay re-resolves the authoritative grant epoch: the stored
		// snapshot alone must never authorize rotating a credential that a
		// later SetAppGrants mutation has already superseded.
		return s.replayBridge(ctx, command, session, resolved.GrantRevision)
	}
	digest := s.createDigest(command, replayCandidates[0])
	now := s.now()
	session := domain.SurfaceSession{
		ID: s.ids.New(), OwnerUserID: command.OwnerUserID, DeviceID: command.DeviceID,
		IdempotencyKey: command.IdempotencyKey, RequestDigest: digest,
		ProjectID: command.ProjectID, AppInstanceID: command.AppInstanceID,
		Renderer: renderer,
		Descriptor: domain.LaunchDescriptor{
			AppID: resolved.AppID, Version: resolved.Version,
			ManifestDigest: resolved.ManifestDigest,
		},
		BridgeCapabilities: domain.EffectiveBridgeCapabilities(resolved.GrantedPermissions, s.knowledgeConfigured),
		// The pinned authorization epoch is exactly what Core resolved —
		// never a constant and never a client input (ADR-0003 §7).
		InstallationGrantRevision: resolved.GrantRevision,
		CreatedAt:                 now, ExpiresAt: now.Add(s.ttl),
	}
	switch resolved.Kind {
	case ports.LaunchKindWebBundle:
		session.Descriptor.ArtifactID = resolved.ArtifactID
		session.Descriptor.ArtifactDigest = resolved.ArtifactDigest
		session.Descriptor.Entrypoint = resolved.Entrypoint
	case ports.LaunchKindWebServiceContainer:
		// The session is only returned once the exact container is running
		// and its bounded startup health has passed. A failed launch never
		// consumes the create key: the ruling above ran before this, and the
		// created workload side effect is converged by reconciliation.
		handle, err := s.ensureWorkload(ctx, command, resolved)
		if err != nil {
			return CreatedSurface{}, err
		}
		session.WorkloadID = handle.ID
		session.WorkloadGeneration = handle.Generation
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
	return s.replayBridge(ctx, command, created, resolved.GrantRevision)
}

// rendererRuling decides the session renderer and the digest candidates for
// this create. Explicit renderers must match the resolved kind exactly —
// there is no silent fallback. An auto request carries the resolved kind in
// its canonical digest segment; its second candidate is the exact legacy v1
// mapping (raw renderer "" digested as "web-bundle"), so upgrade-time
// replays of historical keys stay byte-stable and no historical key can
// silently widen into the container path.
func (s *Service) rendererRuling(preferred string, kind ports.LaunchKind) (string, []string, error) {
	switch preferred {
	case domain.RendererWebBundle:
		if kind != ports.LaunchKindWebBundle {
			return "", nil, domain.ErrUnsupported
		}
		return domain.RendererWebBundle, []string{preferred}, nil
	case domain.RendererWebService:
		if kind != ports.LaunchKindWebServiceContainer {
			return "", nil, domain.ErrUnsupported
		}
		return domain.RendererWebService, []string{preferred}, nil
	default:
		switch kind {
		case ports.LaunchKindWebBundle:
			// Candidate order: the current auto form first (new creates),
			// the legacy v1 mapping second (only rows created before the
			// auto form existed can carry it).
			return domain.RendererWebBundle, []string{"auto:web-bundle", domain.RendererWebBundle}, nil
		case ports.LaunchKindWebServiceContainer:
			return domain.RendererWebService, []string{"auto:web-service"}, nil
		default:
			return "", nil, domain.ErrUnsupported
		}
	}
}

// createDigest digests the canonical create request under one renderer
// segment. The segment values are server facts: "auto:<kind>" for
// server-selected creates, the explicit renderer for explicit ones.
func (s *Service) createDigest(command CreateCommand, rendererSegment string) string {
	return domain.CreateRequestDigest(command.DeviceID, command.ProjectID, command.AppInstanceID,
		command.DeviceClass, command.ViewportWidth, command.ViewportHeight, command.ViewportRatio, rendererSegment)
}

// resolveGeneric resolves the renderer-neutral launch facts through Core and
// enforces the grant-epoch trust invariant. A resolution whose GrantRevision
// is below 1 is an untrusted resolution — a stored-fact invariant drift, not
// a client error — so the surface fails closed with the sanitized
// ErrResolverCorrupt verdict (Internal at transport) instead of persisting or
// comparing epoch 0. The database CHECK would also reject 0, but the
// application layer refuses first with a clean, fixed error.
func (s *Service) resolveGeneric(ctx context.Context, command CreateCommand) (ports.ResolvedLaunch, error) {
	resolved, err := s.resolver.ResolveSurfaceLaunch(ctx, ports.ResolveQuery{
		ProjectID: command.ProjectID, AppInstanceID: command.AppInstanceID,
	})
	if err != nil {
		return ports.ResolvedLaunch{}, mapResolverError(err)
	}
	if resolved.GrantRevision < 1 {
		return ports.ResolvedLaunch{}, fmt.Errorf("resolve installed instance: %w", ports.ErrResolverCorrupt)
	}
	return resolved, nil
}

// ensureWorkload launches (or attaches to) the supervised workload for a
// container-kind create and returns the verified handle. The operation key is
// derived from the create key, so an ensure is durable with the same
// lifecycle as the create itself. Workload verdicts map onto the sanitized
// surface errors: capability or image misses are unsupported (failed
// precondition), outages are unavailable, conflicts are idempotency aborts.
func (s *Service) ensureWorkload(ctx context.Context, command CreateCommand, resolved ports.ResolvedLaunch) (ports.WorkloadHandle, error) {
	if s.workloads == nil {
		return ports.WorkloadHandle{}, domain.ErrUnsupported
	}
	handle, err := s.workloads.EnsureSurfaceWorkload(ctx, ports.SurfaceWorkloadQuery{
		OwnerUserID: command.OwnerUserID, ProjectID: command.ProjectID,
		AppInstanceID: command.AppInstanceID, AppID: resolved.AppID,
		AppVersion: resolved.Version, ManifestDigest: resolved.ManifestDigest,
		Image: resolved.Image, Command: resolved.Command, Port: resolved.Port,
		Resources: resolved.Resources, Health: resolved.Health,
		OperationKey: "surface-create:" + command.IdempotencyKey,
	})
	if err != nil {
		switch {
		case errors.Is(err, ports.ErrWorkloadImageMissing), errors.Is(err, ports.ErrWorkloadRunnerUnavailable), errors.Is(err, ports.ErrWorkloadUnsupported):
			return ports.WorkloadHandle{}, domain.ErrUnsupported
		case errors.Is(err, ports.ErrWorkloadUnavailable):
			return ports.WorkloadHandle{}, domain.ErrUnavailable
		case errors.Is(err, ports.ErrWorkloadConflict):
			return ports.WorkloadHandle{}, domain.ErrIdempotencyConflict
		default:
			return ports.WorkloadHandle{}, fmt.Errorf("ensure workload: %w", err)
		}
	}
	return handle, nil
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

// ServeSurface resolves one read-only surface request for either renderer:
// web-bundle sessions serve one revalidated asset; web-service sessions
// resolve the verified proxy target. Every check fails closed before the
// backend is ever contacted. Uninstalled, stopped, drifted, or unknown
// sessions are sanitized NotFound verdicts — the boundary answers 404
// without leaking which check fired.
func (s *Service) ServeSurface(ctx context.Context, ownerUserID, deviceID, sessionID, rawPath string) (ports.SurfaceContent, error) {
	if ownerUserID == "" || deviceID == "" || !domain.ValidSessionUUID(sessionID) {
		return ports.SurfaceContent{}, domain.ErrNotFound
	}
	normalized, err := domain.NormalizeAssetPath(rawPath)
	if err != nil {
		return ports.SurfaceContent{}, domain.ErrNotFound
	}
	session, err := s.repository.GetActiveSession(ctx, ownerUserID, deviceID, sessionID, s.now())
	if err != nil {
		if errors.Is(err, ports.ErrStoreUnavailable) {
			return ports.SurfaceContent{}, domain.ErrUnavailable
		}
		return ports.SurfaceContent{}, domain.ErrNotFound
	}
	switch session.Renderer {
	case domain.RendererWebBundle:
		asset, err := s.resolver.ReadWebBundleAsset(ctx, ports.AssetQuery{
			ProjectID: session.ProjectID, AppInstanceID: session.AppInstanceID, AssetPath: normalized,
		})
		if err != nil {
			return ports.SurfaceContent{}, mapResolverError(err)
		}
		return ports.SurfaceContent{Kind: ports.ContentAsset, Asset: asset}, nil
	case domain.RendererWebService:
		target, err := s.proxyTargetForSession(ctx, session, normalized)
		if err != nil {
			return ports.SurfaceContent{}, err
		}
		return ports.SurfaceContent{Kind: ports.ContentProxy, Proxy: target}, nil
	default:
		return ports.SurfaceContent{}, domain.ErrNotFound
	}
}

// ResolveProxyTarget resolves the verified proxy backend for one web-service
// surface request.
func (s *Service) ResolveProxyTarget(ctx context.Context, ownerUserID, deviceID, sessionID, rawPath string) (ports.ProxyTarget, error) {
	if ownerUserID == "" || deviceID == "" || !domain.ValidSessionUUID(sessionID) {
		return ports.ProxyTarget{}, domain.ErrNotFound
	}
	normalized, err := domain.NormalizeAssetPath(rawPath)
	if err != nil {
		return ports.ProxyTarget{}, domain.ErrNotFound
	}
	_ = normalized
	session, err := s.repository.GetActiveSession(ctx, ownerUserID, deviceID, sessionID, s.now())
	if err != nil {
		if errors.Is(err, ports.ErrStoreUnavailable) {
			return ports.ProxyTarget{}, domain.ErrUnavailable
		}
		return ports.ProxyTarget{}, domain.ErrNotFound
	}
	return s.proxyTargetForSession(ctx, session, normalized)
}

// proxyTargetForSession revalidates Core and the workload for an
// already-loaded web-service session. Core revalidates the active
// installation on every request, exactly as the web-bundle asset path does;
// a digest mismatch is stored-fact corruption, an uninstalled or archived
// instance is a definitive miss, and a stopped or drifted workload fails
// closed as NotFound.
func (s *Service) proxyTargetForSession(ctx context.Context, session domain.SurfaceSession, normalized string) (ports.ProxyTarget, error) {
	if session.Renderer != domain.RendererWebService || session.WorkloadID == "" {
		return ports.ProxyTarget{}, domain.ErrNotFound
	}
	resolved, err := s.resolver.ResolveSurfaceLaunch(ctx, ports.ResolveQuery{
		ProjectID: session.ProjectID, AppInstanceID: session.AppInstanceID,
	})
	if err != nil {
		return ports.ProxyTarget{}, mapResolverError(err)
	}
	if resolved.Kind != ports.LaunchKindWebServiceContainer ||
		resolved.AppID != session.Descriptor.AppID ||
		resolved.Version != session.Descriptor.Version ||
		resolved.ManifestDigest != session.Descriptor.ManifestDigest {
		return ports.ProxyTarget{}, domain.ErrNotFound
	}
	if s.workloads == nil {
		return ports.ProxyTarget{}, domain.ErrNotFound
	}
	handle, err := s.workloads.LookupSurfaceWorkload(ctx, session.WorkloadID, session.WorkloadGeneration)
	if err != nil {
		// Drift, stopped workloads, and lookup misses all fail closed as
		// NotFound; a store outage stays a 503.
		if errors.Is(err, ports.ErrWorkloadUnavailable) {
			return ports.ProxyTarget{}, domain.ErrUnavailable
		}
		return ports.ProxyTarget{}, domain.ErrNotFound
	}
	return ports.ProxyTarget{
		SessionID: session.ID, Endpoint: handle.Endpoint, BackendPath: "/" + normalized,
	}, nil
}
