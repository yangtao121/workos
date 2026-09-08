// Package application drives the Remote Browser Pool (ADR-0027): durable
// owner-scoped sessions, one supervised real Chromium worker each, bounded
// restarts, idle expiry and a bounded screencast stream.
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
	"github.com/yangtao121/workos/internal/runtime/browserpool/domain"
	"github.com/yangtao121/workos/internal/runtime/browserpool/ports"
)

// Frame is one bounded screencast frame delivered to watchers.
type Frame struct {
	Sequence int64
	JPEG     []byte
	Width    int32
	Height   int32
}

// Event is a session state change delivered to watchers.
type Event struct {
	Session domain.Session
}

type watcher struct {
	ownerUserID string
	channels    []chan any
}

// Service owns the pool: the durable rows, the live workers, and watchers.
type Service struct {
	store     ports.SessionStore
	engine    ports.Engine
	launcher  ports.Launcher
	generator ids.Generator
	identity  string
	logger    *slog.Logger

	mu             sync.Mutex
	workers        map[string]ports.Worker
	restartContext map[string]func()
	restarts       map[string]int32
	recentFrame    map[string][]byte
	recentSeq      map[string]int64
}

func NewService(store ports.SessionStore, engine ports.Engine, launcher ports.Launcher, generator ids.Generator, identity string, logger *slog.Logger) (*Service, error) {
	if store == nil || engine == nil || launcher == nil || generator == nil || identity == "" || logger == nil {
		return nil, errors.New("browser pool requires store, engine, launcher, ids, identity and logger")
	}
	return &Service{
		store: store, engine: engine, launcher: launcher, generator: generator, identity: identity, logger: logger,
		workers:        map[string]ports.Worker{},
		restartContext: map[string]func(){},
		restarts:       map[string]int32{},
		recentFrame:    map[string][]byte{},
		recentSeq:      map[string]int64{},
	}, nil
}

// Facts reports the honest engine capabilities.
func (s *Service) Facts() ports.EngineFacts { return s.engine.Facts() }

// Available reports whether new sessions can start right now.
func (s *Service) Available(ctx context.Context) error { return s.engine.Available(ctx) }

func requestDigest(projectID, initialURL string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("browser:%s:%s", projectID, initialURL)))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Create admits one durable session per owner/key and starts its worker.
func (s *Service) Create(ctx context.Context, ownerUserID, projectID, idempotencyKey, initialURL string) (domain.Session, error) {
	if !domain.ValidUUIDv7(ownerUserID) || !domain.ValidUUIDv7(projectID) || idempotencyKey == "" || len(idempotencyKey) > 128 || !domain.ValidURL(initialURL) {
		return domain.Session{}, domain.ErrInvalid
	}
	digest := requestDigest(projectID, initialURL)
	now := time.Now().UTC().Truncate(time.Microsecond)
	session := domain.Session{
		SessionID: s.generator.New(), OwnerUserID: ownerUserID, ProjectID: projectID,
		IdempotencyKey: idempotencyKey, RequestDigest: digest,
		State: domain.StateQueued, CurrentURL: initialURL,
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
		_ = s.store.CloseSession(ctx, ownerUserID, session.SessionID, domain.StateFailed, 0, time.Now().UTC())
		return domain.Session{}, domain.ErrSessionLimit
	}
	if err := s.engine.Available(ctx); err != nil {
		_ = s.store.CloseSession(ctx, ownerUserID, session.SessionID, domain.StateFailed, 0, time.Now().UTC())
		return domain.Session{}, domain.ErrEngineUnavailable
	}
	release, err := s.engine.Reserve()
	if err != nil {
		_ = s.store.CloseSession(ctx, ownerUserID, session.SessionID, domain.StateFailed, 0, time.Now().UTC())
		if errors.Is(err, domain.ErrSessionLimit) {
			return domain.Session{}, domain.ErrSessionLimit
		}
		return domain.Session{}, domain.ErrEngineUnavailable
	}
	worker, _, err := s.launcher.LaunchWorker(ctx)
	if err != nil {
		release()
		s.logger.Warn("browser worker launch failed", "error", err)
		_ = s.store.CloseSession(ctx, ownerUserID, session.SessionID, domain.StateFailed, 0, time.Now().UTC())
		return domain.Session{}, domain.ErrEngineUnavailable
	}
	if initialURL != "" {
		if err := worker.Navigate(ctx, initialURL); err != nil {
			s.logger.Warn("browser worker initial navigation failed", "error", err)
			worker.Stop()
			release()
			_ = s.store.CloseSession(ctx, ownerUserID, session.SessionID, domain.StateFailed, 0, time.Now().UTC())
			return domain.Session{}, domain.ErrEngineUnavailable
		}
	}
	s.mu.Lock()
	s.workers[session.SessionID] = worker
	s.restartContext[session.SessionID] = release
	s.mu.Unlock()
	if err := s.store.UpdateRunning(ctx, ownerUserID, session.SessionID, domain.StateRunning, initialURL, 0, time.Now().UTC()); err != nil {
		return domain.Session{}, err
	}
	session.State = domain.StateRunning
	return session, nil
}

// Navigate drives the live worker to a new http(s) URL.
func (s *Service) Navigate(ctx context.Context, ownerUserID, sessionID, url string) (domain.Session, error) {
	if !domain.ValidUUIDv7(ownerUserID) || !domain.ValidUUIDv7(sessionID) || !domain.ValidURL(url) {
		return domain.Session{}, domain.ErrInvalid
	}
	session, err := s.store.GetSession(ctx, ownerUserID, sessionID)
	if err != nil {
		return domain.Session{}, err
	}
	if session.State.Terminal() {
		return domain.Session{}, domain.ErrInvalid
	}
	worker, ok := s.worker(sessionID)
	if !ok {
		return domain.Session{}, domain.ErrEngineUnavailable
	}
	if err := worker.Navigate(ctx, url); err != nil {
		return domain.Session{}, domain.ErrEngineUnavailable
	}
	if err := s.store.UpdateRunning(ctx, ownerUserID, sessionID, domain.StateRunning, url, session.RestartCount, time.Now().UTC()); err != nil {
		return domain.Session{}, err
	}
	session.State = domain.StateRunning
	session.CurrentURL = url
	return session, nil
}

// Close terminates the session and reaps its worker.
func (s *Service) Close(ctx context.Context, ownerUserID, sessionID string) (domain.Session, error) {
	if !domain.ValidUUIDv7(ownerUserID) || !domain.ValidUUIDv7(sessionID) {
		return domain.Session{}, domain.ErrInvalid
	}
	session, err := s.store.GetSession(ctx, ownerUserID, sessionID)
	if err != nil {
		return domain.Session{}, err
	}
	s.reap(sessionID)
	if err := s.store.CloseSession(ctx, ownerUserID, sessionID, domain.StateClosed, session.RestartCount, time.Now().UTC()); err != nil {
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

// Capture produces one bounded frame from the live worker, restarting the
// crashed child within bounds when needed (ADR-0027 recovery).
func (s *Service) Capture(ctx context.Context, ownerUserID, sessionID string) (Frame, error) {
	session, err := s.store.GetSession(ctx, ownerUserID, sessionID)
	if err != nil {
		return Frame{}, err
	}
	if session.State.Terminal() {
		return Frame{}, domain.ErrNotFound
	}
	// A missing or dead worker IS the crash signal: both route into the
	// bounded recovery instead of a bare unavailability verdict.
	s.mu.Lock()
	worker, present := s.workers[sessionID]
	s.mu.Unlock()
	if present && worker.Exited() {
		s.logger.Info("browser worker exit observed", "session", sessionID, "restarts", s.restartCount(sessionID))
	}
	if !present || worker.Exited() {
		s.mu.Lock()
		restarts := s.restarts[sessionID]
		s.mu.Unlock()
		s.logger.Info("browser worker exit observed", "session", sessionID, "restarts", restarts)
		if restarts >= domain.MaxRestarts {
			_ = s.store.CloseSession(ctx, ownerUserID, sessionID, domain.StateFailed, restarts, time.Now().UTC())
			s.reap(sessionID)
			return Frame{}, domain.ErrEngineUnavailable
		}
		_ = s.store.UpdateRunning(ctx, ownerUserID, sessionID, domain.StateRestarting, session.CurrentURL, restarts+1, time.Now().UTC())
		s.reapWorkerOnly(sessionID)
		replacement, _, err := s.launcher.LaunchWorker(ctx)
		if err != nil {
			_ = s.store.CloseSession(ctx, ownerUserID, sessionID, domain.StateFailed, restarts+1, time.Now().UTC())
			s.reap(sessionID)
			return Frame{}, domain.ErrEngineUnavailable
		}
		if session.CurrentURL != "" {
			if err := replacement.Navigate(ctx, session.CurrentURL); err != nil {
				if diagnostics, ok := replacement.(interface{ Diagnostics() string }); ok {
					s.logger.Warn("recovery navigation failed", "error", err, "child", diagnostics.Diagnostics())
				}
				replacement.Stop()
				_ = s.store.CloseSession(ctx, ownerUserID, sessionID, domain.StateFailed, restarts+1, time.Now().UTC())
				return Frame{}, domain.ErrEngineUnavailable
			}
		}
		s.mu.Lock()
		s.workers[sessionID] = replacement
		s.restarts[sessionID] = restarts + 1
		s.mu.Unlock()
		_ = s.store.UpdateRunning(ctx, ownerUserID, sessionID, domain.StateRunning, session.CurrentURL, restarts+1, time.Now().UTC())
		worker = replacement
	}
	jpeg, err := worker.Screenshot(ctx)
	if err != nil {
		// Mid-navigation captures surface as transient protocol errors; one
		// bounded retry after the settle window keeps the frame stream alive.
		select {
		case <-ctx.Done():
			return Frame{}, domain.ErrEngineUnavailable
		case <-time.After(700 * time.Millisecond):
		}
		jpeg, err = worker.Screenshot(ctx)
		if err != nil {
			s.logger.Warn("browser frame capture failed", "error", err)
			return Frame{}, domain.ErrEngineUnavailable
		}
	}
	if len(jpeg) > domain.MaxFrameBytes {
		return Frame{}, domain.ErrEngineUnavailable
	}
	s.mu.Lock()
	s.recentSeq[sessionID]++
	sequence := s.recentSeq[sessionID]
	s.mu.Unlock()
	return Frame{Sequence: sequence, JPEG: jpeg, Width: domain.FrameWidth, Height: domain.FrameHeight}, nil
}

func (s *Service) worker(sessionID string) (ports.Worker, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	worker, ok := s.workers[sessionID]
	return worker, ok && !worker.Exited()
}

func (s *Service) restartCount(sessionID string) int32 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.restarts[sessionID]
}

// reapWorkerOnly stops the worker but keeps the engine slot reserved for
// the replacement launch that follows immediately.
func (s *Service) reapWorkerOnly(sessionID string) {
	s.mu.Lock()
	worker, ok := s.workers[sessionID]
	if ok {
		delete(s.workers, sessionID)
	}
	s.mu.Unlock()
	if ok {
		worker.Stop()
	}
}

// reap stops the worker and releases its engine slot.
func (s *Service) reap(sessionID string) {
	s.mu.Lock()
	worker, hasWorker := s.workers[sessionID]
	release, hasRelease := s.restartContext[sessionID]
	delete(s.workers, sessionID)
	delete(s.restartContext, sessionID)
	delete(s.restarts, sessionID)
	delete(s.recentFrame, sessionID)
	delete(s.recentSeq, sessionID)
	s.mu.Unlock()
	if hasWorker {
		worker.Stop()
	}
	if hasRelease {
		release()
	}
}

// Sweep closes idle sessions whose viewers disappeared (ADR-0027 TTL).
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
