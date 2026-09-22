// Package application drives supervised PTY sessions (ADR-0028): durable
// owner-scoped sessions, real login shells, bounded IO, idle expiry.
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
	"github.com/yangtao121/workos/internal/runtime/ptyhost/domain"
	"github.com/yangtao121/workos/internal/runtime/ptyhost/ports"
)

type Service struct {
	opMu          sync.Mutex
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

	mu        sync.Mutex
	terminals map[string]ports.Terminal
	slots     map[string]func()
}

func NewService(store ports.SessionStore, engine ports.Engine, generator ids.Generator, logger *slog.Logger) (*Service, error) {
	if store == nil || engine == nil || generator == nil || logger == nil {
		return nil, errors.New("pty service requires store, engine, ids and logger")
	}
	return &Service{store: store, engine: engine, generator: generator, logger: logger, terminals: map[string]ports.Terminal{}, slots: map[string]func(){}}, nil
}

func (s *Service) Facts() ports.EngineFacts { return s.engine.Facts() }

// WithWorkspace binds the operator-registered project workspace resolver.
// Without one the shell starts in its default scratch directory.
func (s *Service) WithWorkspace(workspace ports.WorkspaceResolver) *Service {
	s.workspace = workspace
	return s
}

// WithControlAuthorization binds the surface continuity control gate
// (ADR-0031): with it, every write and resize consults the workload's
// current control lease and a non-controlling device is refused.
func (s *Service) WithControlAuthorization(control ports.ControlAuthorizer) *Service {
	s.control = control
	return s
}

func requestDigest(projectID string, columns, rows int32, workingDirectory string, modes ...domain.LifecycleMode) string {
	mode, _ := domain.NormalizeLifecycle(modes...)
	if mode == domain.LifecycleManualStop {
		workingDirectory += "\x00lifecycle:manual_stop"
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("pty:%s:%d:%d:%s", projectID, columns, rows, workingDirectory)))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func (s *Service) Create(ctx context.Context, ownerUserID, projectID, idempotencyKey string, columns, rows int32, modes ...domain.LifecycleMode) (domain.Session, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	if !domain.ValidUUIDv7(ownerUserID) || !domain.ValidUUIDv7(projectID) || idempotencyKey == "" || len(idempotencyKey) > 128 || !domain.ValidSize(columns, rows) {
		return domain.Session{}, domain.ErrInvalid
	}
	mode, err := domain.NormalizeLifecycle(modes...)
	if err != nil {
		return domain.Session{}, err
	}
	grant, err := s.workspaceGrant(ctx, ownerUserID, projectID)
	if err != nil {
		return domain.Session{}, err
	}
	workingDirectory := grant.Directory

	digest := requestDigest(projectID, columns, rows, workingDirectory, mode)
	now := time.Now().UTC().Truncate(time.Microsecond)
	session := domain.Session{
		LifecycleMode: mode, Generation: 1, SessionID: s.generator.New(), OwnerUserID: ownerUserID, ProjectID: projectID,
		IdempotencyKey: idempotencyKey, RequestDigest: digest,
		State: domain.StateQueued, CreatedAt: now, UpdatedAt: now, ExpiresAt: mode.Expiry(now),
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
	terminal, err := s.launch(ctx, columns, rows, grant, mode)
	if err != nil {
		s.logger.Warn("pty launch failed", "error", err)
		release()
		_ = s.store.CloseSession(ctx, ownerUserID, session.SessionID, domain.StateFailed, time.Now().UTC())
		return domain.Session{}, domain.ErrEngineUnavailable
	}
	s.mu.Lock()
	s.terminals[session.SessionID] = terminal
	s.slots[session.SessionID] = release
	s.mu.Unlock()
	if err := s.store.UpdateState(ctx, ownerUserID, session.SessionID, domain.StateRunning, time.Now().UTC()); err != nil {
		return domain.Session{}, err
	}
	session.State = domain.StateRunning
	s.watchWorkspace(session, grant)
	return session, nil
}

func (s *Service) terminal(ownerUserID, sessionID string) (ports.Terminal, bool) {
	session, err := s.store.GetSession(context.Background(), ownerUserID, sessionID)
	if err != nil || session.State.Terminal() {
		return nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	terminal, ok := s.terminals[sessionID]
	return terminal, ok
}

// authorizeInput enforces the server-side control epoch on the input path.
// A workload without attachments (the direct session flow) keeps the plain
// owner-scoped path; once a continuity lease exists, only the live
// controlling device may drive the session. Gate failures surface as the
// sanitized ErrControlDenied, never the store's internals.
func (s *Service) authorizeInput(ctx context.Context, ownerUserID, deviceID, sessionID string) error {
	if s.control == nil {
		return nil
	}
	if !domain.ValidUUIDv7(deviceID) {
		return domain.ErrControlDenied
	}
	if err := s.control.AuthorizeInput(ctx, ownerUserID, sessionID, deviceID); err != nil {
		return domain.ErrControlDenied
	}
	return nil
}

// Get reads one session for its owner.
func (s *Service) Get(ctx context.Context, ownerUserID, sessionID string) (domain.Session, error) {
	if !domain.ValidUUIDv7(ownerUserID) || !domain.ValidUUIDv7(sessionID) {
		return domain.Session{}, domain.ErrInvalid
	}
	s.opMu.Lock()
	defer s.opMu.Unlock()
	row, err := s.store.GetSession(ctx, ownerUserID, sessionID)
	if err != nil {
		return domain.Session{}, err
	}
	return s.reconcileSession(ctx, row)
}

// ListProject returns the owner's non-terminal sessions of one project — the
// surface continuity discovery view (ADR-0031).
func (s *Service) ListProject(ctx context.Context, ownerUserID, projectID string) ([]domain.Session, error) {
	if !domain.ValidUUIDv7(ownerUserID) || !domain.ValidUUIDv7(projectID) {
		return nil, domain.ErrInvalid
	}
	return s.store.ListProjectSessions(ctx, ownerUserID, projectID)
}

func (s *Service) Write(ctx context.Context, ownerUserID, deviceID, sessionID string, input []byte, epochs ...int64) (domain.Session, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	if !domain.ValidUUIDv7(ownerUserID) || !domain.ValidUUIDv7(sessionID) || !domain.ValidInput(input) {
		return domain.Session{}, domain.ErrInvalid
	}
	// The session-state lookup precedes the control gate: a stopped or
	// unknown workload is NotFound whatever the control lease says, while a
	// live session enforces the current controller on the input path.
	terminal, ok := s.terminal(ownerUserID, sessionID)
	if !ok {
		return domain.Session{}, domain.ErrNotFound
	}
	if err := s.authorizeInput(ctx, ownerUserID, deviceID, sessionID); err != nil {
		return domain.Session{}, err
	}
	if authorizer, ok := s.control.(ports.EpochControlAuthorizer); ok {
		epoch := int64(0)
		if len(epochs) > 0 {
			epoch = epochs[0]
		}
		if err := authorizer.AuthorizeInputGeneration(ctx, ownerUserID, sessionID, deviceID, epoch); err != nil {
			return domain.Session{}, domain.ErrControlDenied
		}
	}

	if err := terminal.Write(ctx, input); err != nil {
		return domain.Session{}, domain.ErrEngineUnavailable
	}
	return s.store.GetSession(ctx, ownerUserID, sessionID)
}

func (s *Service) Read(ctx context.Context, ownerUserID, sessionID string, after int64, maxBytes int32) (cursor int64, output []byte, closed bool, err error) {
	if !domain.ValidUUIDv7(ownerUserID) || !domain.ValidUUIDv7(sessionID) || after < 0 {
		return 0, nil, false, domain.ErrInvalid
	}
	terminal, ok := s.terminal(ownerUserID, sessionID)
	if !ok {
		return 0, nil, false, domain.ErrNotFound
	}
	cursor, output, err = terminal.Read(ctx, after, maxBytes)
	if err != nil {
		return 0, nil, false, domain.ErrEngineUnavailable
	}
	return cursor, output, terminal.Exited(), nil
}

func (s *Service) Resize(ctx context.Context, ownerUserID, deviceID, sessionID string, columns, rows int32, epochs ...int64) (domain.Session, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	if !domain.ValidUUIDv7(ownerUserID) || !domain.ValidUUIDv7(sessionID) || !domain.ValidSize(columns, rows) {
		return domain.Session{}, domain.ErrInvalid
	}
	// Same ordering as Write: NotFound for terminal sessions first, then
	// the control gate for the live one.
	terminal, ok := s.terminal(ownerUserID, sessionID)
	if !ok {
		return domain.Session{}, domain.ErrNotFound
	}
	if err := s.authorizeInput(ctx, ownerUserID, deviceID, sessionID); err != nil {
		return domain.Session{}, err
	}
	if authorizer, ok := s.control.(ports.EpochControlAuthorizer); ok {
		epoch := int64(0)
		if len(epochs) > 0 {
			epoch = epochs[0]
		}
		if err := authorizer.AuthorizeInputGeneration(ctx, ownerUserID, sessionID, deviceID, epoch); err != nil {
			return domain.Session{}, domain.ErrControlDenied
		}
	}

	if err := terminal.Resize(ctx, columns, rows); err != nil {
		return domain.Session{}, domain.ErrEngineUnavailable
	}
	return s.store.GetSession(ctx, ownerUserID, sessionID)
}

func (s *Service) Close(ctx context.Context, ownerUserID, sessionID string) (domain.Session, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	if !domain.ValidUUIDv7(ownerUserID) || !domain.ValidUUIDv7(sessionID) {
		return domain.Session{}, domain.ErrInvalid
	}
	if _, err := s.store.GetSession(ctx, ownerUserID, sessionID); err != nil {
		return domain.Session{}, err
	}
	s.reap(sessionID)
	if err := s.store.CloseSession(ctx, ownerUserID, sessionID, domain.StateClosed, time.Now().UTC()); err != nil {
		return domain.Session{}, err
	}
	session, err := s.store.GetSession(ctx, ownerUserID, sessionID)
	if err != nil {
		return domain.Session{}, err
	}
	session.State = domain.StateClosed
	return session, nil
}

// Detach releases only this device's access relation (ADR-0031). The shell
// keeps running under its bounded session policy and output keeps
// accumulating for a later Read from any authorized device. Stopping the
// shell stays with Close.
func (s *Service) Detach(ctx context.Context, ownerUserID, sessionID string) (domain.Session, error) {
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
	return session, nil
}

func (s *Service) reap(sessionID string) {
	s.mu.Lock()
	terminal, hasTerminal := s.terminals[sessionID]
	release, hasSlot := s.slots[sessionID]
	delete(s.terminals, sessionID)
	delete(s.slots, sessionID)
	s.mu.Unlock()
	if hasTerminal {
		terminal.Stop()
	}
	if hasSlot {
		release()
	}
}

// Sweep expires idle sessions and reaps their children.
func (s *Service) Sweep(ctx context.Context) error {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	expired, err := s.store.ExpireIdle(ctx, time.Now().UTC())
	if err != nil {
		return err
	}
	for _, sessionID := range expired {
		s.reap(sessionID)
	}
	return s.reconcileActive(ctx)
}

// Reconcile finalizes durable rows whose process is gone (A13): the runtime
// host owns every PTY child (setsid + Pdeathsig inside its PID namespace),
// so after a host restart any non-terminal row without a live in-memory
// terminal is honestly failed — never listed as running again. It runs at
// startup before the listener serves traffic, mirroring the native runner's
// startup sweep; the interactive IO path already refuses such rows (the
// terminal lookup is NotFound), this makes the discovery view agree.
func (s *Service) Reconcile(ctx context.Context) error {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	return s.reconcileActive(ctx)
}
func (s *Service) reconcileActive(ctx context.Context) error {
	sessions, err := s.store.ListActive(ctx)
	if err != nil {
		return err
	}
	for _, session := range sessions {
		if _, err := s.reconcileSession(ctx, session); err != nil {
			return err
		}
	}
	return nil
}
func (s *Service) reconcileSession(ctx context.Context, session domain.Session) (domain.Session, error) {
	if session.State.Terminal() {
		return session, nil
	}
	s.mu.Lock()
	terminal := s.terminals[session.SessionID]
	s.mu.Unlock()
	state := session.State
	if session.LifecycleMode.Expired(session.ExpiresAt, time.Now().UTC()) {
		state = domain.StateClosed
	} else if terminal == nil || terminal.Exited() {
		state = domain.StateFailed
	}
	if state == session.State {
		return session, nil
	}
	s.reap(session.SessionID)
	if err := s.store.CloseSession(ctx, session.OwnerUserID, session.SessionID, state, time.Now().UTC()); err != nil {
		return domain.Session{}, err
	}
	return s.store.GetSession(ctx, session.OwnerUserID, session.SessionID)
}

// Restart preserves workload identity, reserves a durable new generation,
// and starts only on the first delivery of this action key.
func (s *Service) Restart(ctx context.Context, owner, id, key string, fence func() error, modes ...domain.LifecycleMode) (domain.Session, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	if !domain.ValidUUIDv7(owner) || !domain.ValidUUIDv7(id) || key == "" || len(key) > 128 {
		return domain.Session{}, domain.ErrInvalid
	}
	mode, err := domain.NormalizeLifecycle(modes...)
	if err != nil {
		return domain.Session{}, err
	}
	session, err := s.store.GetSession(ctx, owner, id)
	if err != nil {
		return domain.Session{}, err
	}
	restarts, ok := s.store.(ports.RestartStore)
	if !ok {
		return domain.Session{}, domain.ErrEngineUnavailable
	}
	generation, fresh, err := restarts.BeginRestart(ctx, owner, id, key, time.Now().UTC(), mode)
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
	terminal, err := s.launch(ctx, 80, 24, grant, mode)
	if err != nil {
		release()
		return domain.Session{}, domain.ErrEngineUnavailable
	}
	s.mu.Lock()
	s.terminals[id] = terminal
	s.slots[id] = release
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
