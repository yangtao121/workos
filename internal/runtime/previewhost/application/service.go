// Package application owns bounded development-server lifecycle and requests.
package application

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/yangtao121/workos/internal/platform/ids"
	"github.com/yangtao121/workos/internal/runtime/previewhost/domain"
	"github.com/yangtao121/workos/internal/runtime/previewhost/ports"
	"sync"
	"time"
)

type liveProcess struct {
	process    ports.Process
	grant      ports.WorkspaceGrant
	generation int64
}
type Service struct {
	mu            sync.Mutex
	store         ports.Store
	authorization ports.WorkspaceAuthorizer
	engine        ports.Engine
	ids           ids.Generator
	live          map[string]liveProcess
	now           func() time.Time
}

func New(store ports.Store, authorization ports.WorkspaceAuthorizer, engine ports.Engine, generator ids.Generator) (*Service, error) {
	if store == nil || generator == nil {
		return nil, errors.New("preview persistence and ids required")
	}
	return &Service{store: store, authorization: authorization, engine: engine, ids: generator, live: map[string]liveProcess{}, now: func() time.Time { return time.Now().UTC() }}, nil
}
func digest(project, command string, port int32, modes ...domain.LifecycleMode) string {
	mode, _ := domain.NormalizeLifecycle(modes...)
	values := []any{project, command, port}
	if mode == domain.LifecycleManualStop {
		values = append(values, mode)
	}
	data, _ := json.Marshal(values)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
func (s *Service) Start(ctx context.Context, owner, project, key, command string, port int32, modes ...domain.LifecycleMode) (ports.PreviewRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !domain.ValidPreviewUUID(owner) || !domain.ValidPreviewUUID(project) || !domain.ValidIdempotencyKey(key) || command == "" || len(command) > 4096 || port < 1024 || port > 65535 {
		return ports.PreviewRecord{}, domain.ErrInvalid
	}
	mode, err := domain.NormalizeLifecycle(modes...)
	if err != nil {
		return ports.PreviewRecord{}, err
	}
	hash := digest(project, command, port, mode)
	if existing, found, err := s.store.FindByOwnerKey(ctx, owner, key); err != nil {
		return ports.PreviewRecord{}, err
	} else if found {
		if existing.RequestDigest != hash {
			return ports.PreviewRecord{}, domain.ErrConflict
		}
		return s.reconcile(ctx, existing)
	}
	if s.authorization == nil || s.engine == nil {
		return ports.PreviewRecord{}, domain.ErrUnavailable
	}
	grant, err := s.authorization.AuthorizeWorkspace(ctx, owner, project)
	if err != nil {
		return ports.PreviewRecord{}, domain.ErrNoWorkspace
	}
	if len(s.live) >= 4 {
		return ports.PreviewRecord{}, domain.ErrUnavailable
	}
	token := make([]byte, 32)
	if _, err := rand.Read(token); err != nil {
		return ports.PreviewRecord{}, err
	}
	now := s.now()
	record := ports.PreviewRecord{LifecycleMode: mode, PreviewID: s.ids.New(), OwnerUserID: owner, ProjectID: project, IdempotencyKey: key, WorkspaceSourceID: grant.SourceID, BindingID: grant.BindingID, BindingRevision: grant.Revision, ReadOnly: grant.ReadOnly, Command: command, Port: port, RequestDigest: hash, AccessToken: hex.EncodeToString(token), Generation: 1, State: domain.StateQueued, CreatedAt: now, UpdatedAt: now, ExpiresAt: mode.Expiry(now)}
	inserted, err := s.store.Insert(ctx, record)
	if err != nil {
		return ports.PreviewRecord{}, err
	}
	if !inserted {
		return ports.PreviewRecord{}, domain.ErrConflict
	}
	return s.launch(ctx, record, grant)
}
func (s *Service) launch(ctx context.Context, r ports.PreviewRecord, g ports.WorkspaceGrant) (ports.PreviewRecord, error) {
	failed := true
	defer func() {
		if failed {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			s.reap(r.PreviewID)
			_ = s.store.UpdateState(cleanup, r.OwnerUserID, r.PreviewID, domain.StateFailed, s.now())
		}
	}()
	process, err := s.engine.Launch(ctx, r, g)
	if err != nil {
		return ports.PreviewRecord{}, domain.ErrUnavailable
	}
	s.live[r.PreviewID] = liveProcess{process, g, r.Generation}
	r.State = domain.StateRunning
	r.WorkspaceSourceID = g.SourceID
	r.BindingID = g.BindingID
	r.BindingRevision = g.Revision
	r.ReadOnly = g.ReadOnly
	r.UpdatedAt = s.now()
	if err := s.store.Activate(ctx, r); err != nil {
		return ports.PreviewRecord{}, err
	}
	failed = false
	return r, nil
}
func (s *Service) reap(id string) {
	if live, ok := s.live[id]; ok {
		delete(s.live, id)
		live.process.Stop()
	}
}
func (s *Service) reconcile(ctx context.Context, r ports.PreviewRecord) (ports.PreviewRecord, error) {
	if r.State != domain.StateRunning && r.State != domain.StateQueued {
		return r, nil
	}
	state := r.EffectiveState(s.now())
	live, ok := s.live[r.PreviewID]
	if state != domain.StateExpired {
		if !ok || live.process.Exited() || live.generation != r.Generation {
			state = domain.StateFailed
		} else if live.grant.Validate != nil && live.grant.Validate(ctx) != nil {
			state = domain.StateFailed
		}
	}
	if state != r.State {
		s.reap(r.PreviewID)
		if err := s.store.UpdateState(ctx, r.OwnerUserID, r.PreviewID, state, s.now()); err != nil {
			return ports.PreviewRecord{}, err
		}
		r.State = state
	}
	return r, nil
}
func (s *Service) owner(ctx context.Context, owner, id string) (ports.PreviewRecord, error) {
	if !domain.ValidPreviewUUID(owner) || !domain.ValidPreviewUUID(id) {
		return ports.PreviewRecord{}, domain.ErrInvalid
	}
	r, err := s.store.Get(ctx, id)
	if err != nil {
		return r, err
	}
	if r.OwnerUserID != owner {
		return ports.PreviewRecord{}, domain.ErrNotFound
	}
	return r, nil
}
func (s *Service) Get(ctx context.Context, owner, id string) (ports.PreviewRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.owner(ctx, owner, id)
	if err != nil {
		return r, err
	}
	return s.reconcile(ctx, r)
}
func (s *Service) List(ctx context.Context, owner, project string) ([]ports.PreviewRecord, error) {
	if !domain.ValidPreviewUUID(owner) || !domain.ValidPreviewUUID(project) {
		return nil, domain.ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	records, err := s.store.ListByProject(ctx, owner, project, 50)
	if err != nil {
		return nil, err
	}
	for i, r := range records {
		records[i], err = s.reconcile(ctx, r)
		if err != nil {
			return nil, err
		}
	}
	return records, nil
}
func (s *Service) Stop(ctx context.Context, owner, id, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !domain.ValidIdempotencyKey(key) {
		return domain.ErrInvalid
	}
	if _, err := s.owner(ctx, owner, id); err != nil {
		return err
	}
	_, fresh, err := s.store.Action(ctx, owner, id, key, "stop", s.now())
	if err == nil && fresh {
		s.reap(id)
	}
	return err
}
func (s *Service) Restart(ctx context.Context, owner, id, key string, modes ...domain.LifecycleMode) (ports.PreviewRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !domain.ValidIdempotencyKey(key) {
		return ports.PreviewRecord{}, domain.ErrInvalid
	}
	mode, err := domain.NormalizeLifecycle(modes...)
	if err != nil {
		return ports.PreviewRecord{}, err
	}
	r, err := s.owner(ctx, owner, id)
	if err != nil {
		return r, err
	}
	if s.authorization == nil || s.engine == nil {
		return ports.PreviewRecord{}, domain.ErrUnavailable
	}
	grant, err := s.authorization.AuthorizeWorkspace(ctx, owner, r.ProjectID)
	if err != nil {
		return ports.PreviewRecord{}, domain.ErrNoWorkspace
	}
	if _, ok := s.live[id]; !ok && len(s.live) >= 4 {
		return ports.PreviewRecord{}, domain.ErrUnavailable
	}
	r, fresh, err := s.store.Action(ctx, owner, id, key, "restart", s.now(), mode)
	if err != nil || !fresh {
		return r, err
	}
	s.reap(id)
	return s.launch(ctx, r, grant)
}

// Request is capability scoped. Only the unpredictable preview token grants
// this isolated server; it does not authorize any Core or Runtime RPC.
func (s *Service) Request(ctx context.Context, id, token string, request ports.Request) (ports.Response, error) {
	s.mu.Lock()
	r, err := s.store.Get(ctx, id)
	if err != nil || len(token) != 64 || subtle.ConstantTimeCompare([]byte(r.AccessToken), []byte(token)) != 1 {
		s.mu.Unlock()
		return ports.Response{}, domain.ErrNotFound
	}
	r, err = s.reconcile(ctx, r)
	live, ok := s.live[id]
	s.mu.Unlock()
	if err != nil || !ok || !r.Live(s.now()) {
		return ports.Response{}, domain.ErrNotFound
	}
	return live.process.Request(ctx, request)
}
func (s *Service) Sweep(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	records, err := s.store.ListActive(ctx)
	if err != nil {
		return err
	}
	for _, r := range records {
		if _, err := s.reconcile(ctx, r); err != nil {
			return err
		}
	}
	return nil
}
func (s *Service) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id := range s.live {
		s.reap(id)
	}
}
