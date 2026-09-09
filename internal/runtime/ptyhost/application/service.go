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
	store     ports.SessionStore
	engine    ports.Engine
	generator ids.Generator
	logger    *slog.Logger

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

func requestDigest(projectID string, columns, rows int32) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("pty:%s:%d:%d", projectID, columns, rows)))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func (s *Service) Create(ctx context.Context, ownerUserID, projectID, idempotencyKey string, columns, rows int32) (domain.Session, error) {
	if !domain.ValidUUIDv7(ownerUserID) || !domain.ValidUUIDv7(projectID) || idempotencyKey == "" || len(idempotencyKey) > 128 || !domain.ValidSize(columns, rows) {
		return domain.Session{}, domain.ErrInvalid
	}
	digest := requestDigest(projectID, columns, rows)
	now := time.Now().UTC().Truncate(time.Microsecond)
	session := domain.Session{
		SessionID: s.generator.New(), OwnerUserID: ownerUserID, ProjectID: projectID,
		IdempotencyKey: idempotencyKey, RequestDigest: digest,
		State: domain.StateQueued, CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(domain.SessionTTL),
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
	terminal, err := s.engine.Launch(ctx, columns, rows)
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

func (s *Service) Write(ctx context.Context, ownerUserID, sessionID string, input []byte) (domain.Session, error) {
	if !domain.ValidUUIDv7(ownerUserID) || !domain.ValidUUIDv7(sessionID) || !domain.ValidInput(input) {
		return domain.Session{}, domain.ErrInvalid
	}
	terminal, ok := s.terminal(ownerUserID, sessionID)
	if !ok {
		return domain.Session{}, domain.ErrNotFound
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

func (s *Service) Resize(ctx context.Context, ownerUserID, sessionID string, columns, rows int32) (domain.Session, error) {
	if !domain.ValidUUIDv7(ownerUserID) || !domain.ValidUUIDv7(sessionID) || !domain.ValidSize(columns, rows) {
		return domain.Session{}, domain.ErrInvalid
	}
	terminal, ok := s.terminal(ownerUserID, sessionID)
	if !ok {
		return domain.Session{}, domain.ErrNotFound
	}
	if err := terminal.Resize(ctx, columns, rows); err != nil {
		return domain.Session{}, domain.ErrEngineUnavailable
	}
	return s.store.GetSession(ctx, ownerUserID, sessionID)
}

func (s *Service) Close(ctx context.Context, ownerUserID, sessionID string) (domain.Session, error) {
	if !domain.ValidUUIDv7(ownerUserID) || !domain.ValidUUIDv7(sessionID) {
		return domain.Session{}, domain.ErrInvalid
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
	expired, err := s.store.ExpireIdle(ctx, time.Now().UTC())
	if err != nil {
		return err
	}
	for _, sessionID := range expired {
		s.reap(sessionID)
	}
	return nil
}
