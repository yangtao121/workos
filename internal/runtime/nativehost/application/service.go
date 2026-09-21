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
	connectivity  ports.ConnectivityIssuer
	authorization ports.WorkspaceAuthorizer
	store         ports.SessionStore
	engine        ports.Engine
	generator     ids.Generator
	logger        *slog.Logger
	workspace     ports.WorkspaceResolver
	// control gates the input path on the server-side single-controller
	// lease (ADR-0031 §4). nil keeps the plain owner-scoped path for hosts
	// without the surface continuity service.
	control ports.ControlAuthorizer

	opMu        sync.Mutex
	mu          sync.Mutex
	displays    map[string]ports.Display
	peerDevices map[string]string
	releases    map[string]func()
}

func NewService(store ports.SessionStore, engine ports.Engine, generator ids.Generator, logger *slog.Logger) (*Service, error) {
	if store == nil || engine == nil || generator == nil || logger == nil {
		return nil, errors.New("native runner requires store, engine, ids and logger")
	}
	return &Service{
		store: store, engine: engine, generator: generator, logger: logger,
		displays:    map[string]ports.Display{},
		peerDevices: map[string]string{},
		releases:    map[string]func(){},
	}, nil
}

// Facts reports the honest engine capabilities.
func (s *Service) Facts() ports.EngineFacts { return s.engine.Facts() }

// Available reports whether new sessions can start right now.
func (s *Service) Available(ctx context.Context) error { return s.engine.Available(ctx) }

// WithWorkspace binds the operator-registered project workspace resolver.
// Without one the X client starts in the display scratch directory.
func (s *Service) WithWorkspace(workspace ports.WorkspaceResolver) *Service {
	s.workspace = workspace
	return s
}

// WithControlAuthorization binds the surface continuity control gate
// (ADR-0031): with it, every input event of a newly connected peer consults
// the workload's current control lease at enqueue time, and the superseded
// device's queued input is dropped.
func (s *Service) WithControlAuthorization(control ports.ControlAuthorizer) *Service {
	s.control = control
	return s
}

// ListProject returns the owner's non-terminal sessions of one project — the
// surface continuity discovery view (ADR-0031).
func (s *Service) ListProject(ctx context.Context, ownerUserID, projectID string) ([]domain.Session, error) {
	if !domain.ValidUUIDv7(ownerUserID) || !domain.ValidUUIDv7(projectID) {
		return nil, domain.ErrInvalid
	}
	return s.store.ListProjectSessions(ctx, ownerUserID, projectID)
}

func requestDigest(projectID string, width, height int32, workingDirectory string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("native:%s:%d:%d:%s", projectID, width, height, workingDirectory)))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Create admits one durable session per owner/key and starts its display.
func (s *Service) Create(ctx context.Context, ownerUserID, projectID, idempotencyKey string, width, height int32) (domain.Session, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	if !domain.ValidUUIDv7(ownerUserID) || !domain.ValidUUIDv7(projectID) || idempotencyKey == "" || len(idempotencyKey) > 128 || !domain.ValidSize(width, height) {
		return domain.Session{}, domain.ErrInvalid
	}
	grant, err := s.workspaceGrant(ctx, ownerUserID, projectID)
	if err != nil {
		return domain.Session{}, err
	}
	workingDirectory := grant.Directory

	digest := requestDigest(projectID, width, height, workingDirectory)
	now := time.Now().UTC().Truncate(time.Microsecond)
	session := domain.Session{
		Generation: 1, SessionID: s.generator.New(), OwnerUserID: ownerUserID, ProjectID: projectID,
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
		replay, err := s.store.GetSessionByKey(ctx, ownerUserID, idempotencyKey)
		if err != nil {
			return domain.Session{}, err
		}
		return s.reconcile(ctx, replay)
	}
	completed := false
	defer func() {
		if completed {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		s.reap(session.SessionID)
		_ = s.store.CloseSession(cleanupCtx, ownerUserID, session.SessionID, domain.StateFailed, time.Now().UTC())
	}()
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
	display, err := s.launch(ctx, width, height, grant)
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
	completed = true
	s.watchWorkspace(session, grant)
	return session, nil
}

// Connect exchanges one complete WebRTC offer for the answer of the session's
// live display. A dead display is an honest engine failure, not a restart.
// The connecting device identity gates input (ADR-0031 §4): with a control
// authorizer bound, every input event of this peer consults the CURRENT
// control lease at enqueue time.
func (s *Service) Connect(ctx context.Context, ownerUserID, deviceID, sessionID, offerSDP string, epochs ...int64) (domain.Session, string, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	if !domain.ValidUUIDv7(ownerUserID) || !domain.ValidUUIDv7(sessionID) || !domain.ValidUUIDv7(deviceID) || offerSDP == "" || len(offerSDP) > domain.MaxSDPBytes {
		return domain.Session{}, "", domain.ErrInvalid
	}
	session, err := s.store.GetSession(ctx, ownerUserID, sessionID)
	if err != nil {
		return domain.Session{}, "", err
	}
	session, err = s.reconcile(ctx, session)
	if err != nil {
		return domain.Session{}, "", err
	}
	if session.State == domain.StateFailed {
		return domain.Session{}, "", domain.ErrEngineUnavailable
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

	epoch := int64(0)
	if len(epochs) > 0 {
		epoch = epochs[0]
	}
	authorized := func() bool {
		if gate, ok := s.control.(ports.EpochControlAuthorizer); ok {
			return gate.AuthorizeInputGeneration(context.Background(), ownerUserID, sessionID, deviceID, epoch) == nil
		}
		return s.control == nil || s.control.AuthorizeInput(context.Background(), ownerUserID, sessionID, deviceID) == nil
	}
	if !authorized() {
		return domain.Session{}, "", domain.ErrControlDenied
	}
	display.GuardInput(func() bool { return false })
	answer, err := display.Connect(ctx, offerSDP)
	if err != nil {
		return domain.Session{}, "", domain.ErrEngineUnavailable
	}
	display.GuardInput(authorized)
	s.peerDevices[sessionID] = deviceID
	return session, answer, nil
}

// Detach releases only this device's media peer and input subscription; the
// supervised display session keeps running under its bounded policy
// (ADR-0031). Closing the display stays with Close.
func (s *Service) Detach(ctx context.Context, ownerUserID, sessionID, deviceID string) (domain.Session, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	if !domain.ValidUUIDv7(ownerUserID) || !domain.ValidUUIDv7(sessionID) {
		return domain.Session{}, domain.ErrInvalid
	}
	session, err := s.store.GetSession(ctx, ownerUserID, sessionID)
	if err != nil {
		return domain.Session{}, err
	}
	if session.State.Terminal() {
		return domain.Session{}, domain.ErrInvalid
	}
	s.mu.Lock()
	display, ok := s.displays[sessionID]
	s.mu.Unlock()
	if ok && s.peerDevices[sessionID] == deviceID {
		display.Detach()
		delete(s.peerDevices, sessionID)
	}
	return session, nil
}

// Close terminates the session and reaps its display.
func (s *Service) Close(ctx context.Context, ownerUserID, sessionID string) (domain.Session, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
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
	return s.store.GetSession(ctx, ownerUserID, session.SessionID)
}

// Get reads one session for its owner.
func (s *Service) Get(ctx context.Context, ownerUserID, sessionID string) (domain.Session, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	if !domain.ValidUUIDv7(ownerUserID) || !domain.ValidUUIDv7(sessionID) {
		return domain.Session{}, domain.ErrInvalid
	}
	session, err := s.store.GetSession(ctx, ownerUserID, sessionID)
	if err != nil {
		return domain.Session{}, err
	}
	return s.reconcile(ctx, session)
}

func (s *Service) reap(sessionID string) {
	s.mu.Lock()
	display, hasDisplay := s.displays[sessionID]
	release, hasRelease := s.releases[sessionID]
	delete(s.displays, sessionID)
	delete(s.peerDevices, sessionID)
	delete(s.releases, sessionID)
	s.mu.Unlock()
	if hasDisplay {
		display.Stop()
	}
	if hasRelease {
		release()
	}
}

// reconcile makes reads and replay agree with live resources and the absolute TTL.
// A process restart cannot resurrect a display: its persisted row becomes failed.
func (s *Service) reconcile(ctx context.Context, session domain.Session) (domain.Session, error) {
	if session.State.Terminal() {
		return session, nil
	}
	s.mu.Lock()
	display := s.displays[session.SessionID]
	s.mu.Unlock()
	now := time.Now().UTC()
	state := session.State
	if !session.ExpiresAt.After(now) {
		state = domain.StateClosed
	} else if display == nil || display.Exited() {
		state = domain.StateFailed
	}
	if state == session.State {
		return session, nil
	}
	s.reap(session.SessionID)
	if err := s.store.CloseSession(ctx, session.OwnerUserID, session.SessionID, state, now); err != nil {
		return domain.Session{}, err
	}
	return s.store.GetSession(ctx, session.OwnerUserID, session.SessionID)
}

// Sweep runs at startup and periodically to reclaim dead or expired sessions.
func (s *Service) Sweep(ctx context.Context) error {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	sessions, err := s.store.ListActive(ctx)
	if err != nil {
		return err
	}
	for _, session := range sessions {
		if _, err := s.reconcile(ctx, session); err != nil {
			return err
		}
	}
	return nil
}

// Shutdown closes all process-owned displays before the host returns.
func (s *Service) Shutdown() {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	s.mu.Lock()
	ids := make([]string, 0, len(s.displays))
	for id := range s.displays {
		ids = append(ids, id)
	}
	s.mu.Unlock()
	for _, id := range ids {
		s.reap(id)
	}
}

// Restart preserves workload identity, reserves a durable new generation,
// and starts only on the first delivery of this action key.
func (s *Service) Restart(ctx context.Context, owner, id, key string, fence func() error) (domain.Session, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	if !domain.ValidUUIDv7(owner) || !domain.ValidUUIDv7(id) || key == "" || len(key) > 128 {
		return domain.Session{}, domain.ErrInvalid
	}
	session, err := s.store.GetSession(ctx, owner, id)
	if err != nil {
		return domain.Session{}, err
	}
	restarts, ok := s.store.(ports.RestartStore)
	if !ok {
		return domain.Session{}, domain.ErrEngineUnavailable
	}
	generation, fresh, err := restarts.BeginRestart(ctx, owner, id, key, time.Now().UTC())
	if err != nil {
		return domain.Session{}, err
	}
	if !fresh {
		return s.store.GetSession(ctx, owner, id)
	}
	s.reap(id)
	completed := false
	defer func() {
		if !completed {
			s.reap(id)
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			_ = s.store.CloseSession(cleanup, owner, id, domain.StateFailed, time.Now().UTC())
		}
	}()
	if fence != nil {
		if err := fence(); err != nil {
			return domain.Session{}, err
		}
	}
	count, err := s.store.CountActive(ctx, owner)
	if err != nil {
		return domain.Session{}, err
	}
	if count > domain.MaxSessions {
		return domain.Session{}, domain.ErrSessionLimit
	}
	if err := s.engine.Available(ctx); err != nil {
		return domain.Session{}, domain.ErrEngineUnavailable
	}
	grant, err := s.workspaceGrant(ctx, owner, session.ProjectID)
	if err != nil {
		return domain.Session{}, err
	}

	release, err := s.engine.Reserve()
	if err != nil {
		return domain.Session{}, err
	}
	display, err := s.launch(ctx, session.Width, session.Height, grant)
	if err != nil {
		release()
		return domain.Session{}, domain.ErrEngineUnavailable
	}
	s.mu.Lock()
	s.displays[id] = display
	s.releases[id] = release
	s.mu.Unlock()
	if err := s.store.UpdateState(ctx, owner, id, domain.StateRunning, time.Now().UTC()); err != nil {
		return domain.Session{}, err
	}
	completed = true
	result, err := s.store.GetSession(ctx, owner, id)
	if err == nil && result.Generation != generation {
		return domain.Session{}, domain.ErrStoreUnavailable
	}
	if err == nil {
		s.watchWorkspace(result, grant)
	}
	return result, err
}

// Stop records the action before reaping. A delayed replay after restart
// cannot stop the new generation or detach its devices.
func (s *Service) Stop(ctx context.Context, owner, id, key string, fence func() error) (domain.Session, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	if !domain.ValidUUIDv7(owner) || !domain.ValidUUIDv7(id) || key == "" || len(key) > 128 {
		return domain.Session{}, domain.ErrInvalid
	}
	store, ok := s.store.(ports.StopStore)
	if !ok {
		return domain.Session{}, domain.ErrEngineUnavailable
	}
	fresh, err := store.BeginStop(ctx, owner, id, key, time.Now().UTC())
	if err != nil {
		return domain.Session{}, err
	}
	if fresh {
		s.reap(id)
		if fence != nil {
			if err := fence(); err != nil {
				return domain.Session{}, err
			}
		}
	}
	return s.store.GetSession(ctx, owner, id)
}
