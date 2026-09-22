// Package testsupport provides explicitly synthetic stores for desktop unit
// tests. Production always uses the PostgreSQL adapter.
package testsupport

import (
	"context"
	"sync"

	"github.com/yangtao121/workos/internal/core/desktop/domain"
	"github.com/yangtao121/workos/internal/core/desktop/ports"
)

type Record struct {
	Snapshot domain.State
	Keys     map[string]string
	History  []domain.State
}

func (r *Record) State() domain.State { return r.Snapshot.Clone() }
func (r *Record) Request(_ context.Context, k string) (string, bool, error) {
	s, ok := r.Keys[k]
	return s, ok, nil
}
func (r *Record) Remember(_ context.Context, k, d string) error { r.Keys[k] = d; return nil }
func (r *Record) Save(_ context.Context, s domain.State) error {
	r.Snapshot = s.Clone()
	r.History = append(r.History, s.Clone())
	if len(r.History) > domain.EventRetention {
		r.History = r.History[len(r.History)-domain.EventRetention:]
	}
	return nil
}
func (r *Record) Events(_ context.Context, after int64) ([]domain.State, error) {
	var result []domain.State
	for _, s := range r.History {
		if s.Revision > after {
			result = append(result, s.Clone())
		}
	}
	return result, nil
}

type Store struct {
	mu      sync.Mutex
	Records map[string]*Record
}

func NewStore() *Store { return &Store{Records: map[string]*Record{}} }
func (s *Store) Locked(_ context.Context, owner string, f func(ports.Transaction) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	copy := &Record{Keys: map[string]string{}}
	if old := s.Records[owner]; old != nil {
		copy.Snapshot = old.Snapshot.Clone()
		copy.History = append(copy.History, old.History...)
		for k, v := range old.Keys {
			copy.Keys[k] = v
		}
	}
	if err := f(copy); err != nil {
		return err
	}
	s.Records[owner] = copy
	return nil
}

type References struct {
	Denied      map[string]bool
	Unavailable bool
}

func (r *References) Project(_ context.Context, _, id string) error {
	if r.Unavailable {
		return domain.ErrUnavailable
	}
	if r.Denied[id] {
		return domain.ErrNotFound
	}
	return nil
}
func (r *References) Target(ctx context.Context, owner string, t domain.Target) error {
	if err := r.Project(ctx, owner, t.ProjectID); err != nil {
		return err
	}
	return r.Project(ctx, owner, t.ResourceID)
}
