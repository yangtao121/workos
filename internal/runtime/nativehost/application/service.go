// Package application drives the virtual-display native runner (ADR-0029):
// durable owner-scoped sessions, one supervised Xvfb + native client + ffmpeg
// capture each, WebRTC signaling, idle expiry and bounded concurrency.
package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/yangtao121/workos/internal/platform/ids"
	"github.com/yangtao121/workos/internal/runtime/nativehost/domain"
	"github.com/yangtao121/workos/internal/runtime/nativehost/ports"
)

type Service struct {
	store     ports.SessionStore
	engine    ports.Engine
	generator ids.Generator
	logger    *slog.Logger

	mu       sync.Mutex
	displays map[string]ports.Display
	releases map[string]func()
}

func NewService(store ports.SessionStore, engine ports.Engine, generator ids.Generator, logger *slog.Logger) (*Service, error) {
	if store == nil || engine == nil || generator == nil || logger == nil {
		return nil, errors.New("native runner requires store, engine, ids and logger")
	}
	return &Service{
		store: store, engine: engine, generator: generator, logger: logger,
		displays: map[string]ports.Display{},
		releases: map[string]func(){},
	}, nil
}

// Facts reports the honest engine capabilities.
func (s *Service) Facts() ports.EngineFacts { return s.engine.Facts() }

// Available reports whether new sessions can start right now.
func (s *Service) Available(ctx context.Context) error { return s.engine.Available(ctx) }

func requestDigest(projectID string, width, height int32) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("native:%s:%d:%d", projectID, width, height)))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Create admits one durable session per owner/key and starts its display.
func (s *Service) Create(ctx context.Context, ownerUserID, projectID, idempotencyKey string, width, height int32) (domain.Session, error) {
	if !domain.ValidUUIDv7(ownerUserID) || !domain.ValidUUIDv7(projectID) || idempotencyKey == "" || len(idempotencyKey) > 128 || !domain.ValidSize(width, height) {
		return domain.Session{}, domain.ErrInvalid
	}
	digest := requestDigest(projectID, width, height)
	now := time.Now().UTC().Truncate(time.Microsecond)
	session := domain.Session{
		SessionID: s.generator.New(), OwnerUserID: ownerUserID, ProjectID: projectID,
		IdempotencyKey: idempotencyKey, RequestDigest: digest,
		State: domain.StateQueued, Width: width, Height: height,
		CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(domain.SessionTTL),
	}
	storedDigest, created, err := s.store.InsertSession(ctx, session)
	if err != nil {
		return domain.Session{}, err
	}
	if !created {
		if storedDigest != digest {
			return domain.Session{}, domain.ErrIdempotencyDrift
		}
		return s.store.GetSessionByKey(ctx, ownerUserID, idempotencyKey)
	}
	count, err := s.store.CountActive(ctx, ownerUserID)
	if err != nil {
		return domain.Session{}, err
	}
	if count > domain.MaxSessions {
		_ = s.store.CloseSession(ctx, ownerUserID, session.SessionID, domain.StateFailed, time.Now().UTC())
		return domain.Session{}, domain.ErrSessionLimit
	}
	if err := s.engine.Available(ctx); err != nil {
		_ = s.store.CloseSession(ctx, ownerUserID, session.SessionID, domain.StateFailed, time.Now().UTC())
		return domain.Session{}, domain.ErrEngineUnavailable
	}
	release, err := s.engine.Reserve()
	if err != nil {
		_ = s.store.CloseSession(ctx, ownerUserID, session.SessionID, domain.StateFailed, time.Now().UTC())
		if errors.Is(err, domain.ErrSessionLimit) {
			return domain.Session{}, domain.ErrSessionLimit
		}
		return domain.Session{}, domain.ErrEngineUnavailable
	}
	display, err := s.engine.Launch(ctx, width, height)
	if err != nil {
		release()
		s.logger.Warn("native display launch failed", "error", err)
		_ = s.store.CloseSession(ctx, ownerUserID, session.SessionID, domain.StateFailed, time.Now().UTC())
		return domain.Session{}, domain.ErrEngineUnavailable
	}
	s.mu.Lock()
	s.displays[session.SessionID] = display
	s.releases[session.SessionID] = release
	s.mu.Unlock()
	if err := s.store.UpdateState(ctx, ownerUserID, session.SessionID, domain.StateRunning, time.Now().UTC()); err != nil {
		s.reap(session.SessionID)
		return domain.Session{}, err
	}
	session.State = domain.StateRunning
	return session, nil
}

// Connect exchanges one complete WebRTC offer for the answer of the session's
// live display. A dead display is an honest engine failure, not a restart.
func (s *Service) Connect(ctx context.Context, ownerUserID, sessionID, offerSDP string) (domain.Session, string, error) {
	if !domain.ValidUUIDv7(ownerUserID) || !domain.ValidUUIDv7(sessionID) || offerSDP == "" || len(offerSDP) > domain.MaxSDPBytes {
		return domain.Session{}, "", domain.ErrInvalid
	}
	session, err := s.store.GetSession(ctx, ownerUserID, sessionID)
	if err != nil {
		return domain.Session{}, "", err
	}
	if session.State.Terminal() {
		return domain.Session{}, "", domain.ErrInvalid
	}
	s.mu.Lock()
	display, ok := s.displays[sessionID]
	s.mu.Unlock()
	if !ok || display.Exited() {
		_ = s.store.CloseSession(ctx, ownerUserID, sessionID, domain.StateFailed, time.Now().UTC())
		s.reap(sessionID)
		return domain.Session{}, "", domain.ErrEngineUnavailable
	}
	answer, err := display.Connect(ctx, offerSDP)
	if err != nil {
		return domain.Session{}, "", domain.ErrEngineUnavailable
	}
	return session, answer, nil
}

// Close terminates the session and reaps its display.
func (s *Service) Close(ctx context.Context, ownerUserID, sessionID string) (domain.Session, error) {
	if !domain.ValidUUIDv7(ownerUserID) || !domain.ValidUUIDv7(sessionID) {
		return domain.Session{}, domain.ErrInvalid
	}
	session, err := s.store.GetSession(ctx, ownerUserID, sessionID)
	if err != nil {
		return domain.Session{}, err
	}
	s.reap(sessionID)
	if err := s.store.CloseSession(ctx, ownerUserID, sessionID, domain.StateClosed, time.Now().UTC()); err != nil {
		return domain.Session{}, err
	}
	session.State = domain.StateClosed
	return session, nil
}

// Get reads one session for its owner.
func (s *Service) Get(ctx context.Context, ownerUserID, sessionID string) (domain.Session, error) {
	if !domain.ValidUUIDv7(ownerUserID) || !domain.ValidUUIDv7(sessionID) {
		return domain.Session{}, domain.ErrInvalid
	}
	return s.store.GetSession(ctx, ownerUserID, sessionID)
}

func (s *Service) reap(sessionID string) {
	s.mu.Lock()
	display, hasDisplay := s.displays[sessionID]
	release, hasRelease := s.releases[sessionID]
	delete(s.displays, sessionID)
	delete(s.releases, sessionID)
	s.mu.Unlock()
	if hasDisplay {
		display.Stop()
	}
	if hasRelease {
		release()
	}
}

// Sweep closes idle sessions whose peers disappeared (ADR-0029 TTL).
func (s *Service) Sweep(ctx context.Context) error {
	expired, err := s.store.ExpireIdle(ctx, time.Now().UTC())
	if err != nil {
		return err
	}
	for _, sessionID := range expired {
		s.reap(sessionID)
	}
	return nil
}
