package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/yangtao121/workos/internal/core/desktop/domain"
	"github.com/yangtao121/workos/internal/core/desktop/ports"
	"github.com/yangtao121/workos/internal/platform/ids"
)

type Service struct {
	store ports.Store
	refs  ports.References
	ids   ids.Generator
}

func New(store ports.Store, refs ports.References, generator ids.Generator) *Service {
	return &Service{store: store, refs: refs, ids: generator}
}
func (s *Service) transact(ctx context.Context, owner string, run func(context.Context, ports.Transaction) error) error {
	if !domain.UUID(owner) {
		return domain.ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return s.store.Locked(ctx, owner, func(tx ports.Transaction) error { return run(ctx, tx) })
}

// referenceCache lasts exactly one transaction. Catch-up history reuses
// authorization reads without retaining stale decisions across requests.
type referenceCache struct {
	source   ports.References
	projects map[string]error
	targets  map[domain.Target]error
}

func (s *Service) references() *referenceCache {
	return &referenceCache{source: s.refs, projects: map[string]error{}, targets: map[domain.Target]error{}}
}
func (r *referenceCache) Project(ctx context.Context, owner, id string) error {
	if e, ok := r.projects[id]; ok {
		return e
	}
	e := r.source.Project(ctx, owner, id)
	r.projects[id] = e
	return e
}
func (r *referenceCache) Target(ctx context.Context, owner string, t domain.Target) error {
	if e, ok := r.targets[t]; ok {
		return e
	}
	e := r.source.Target(ctx, owner, t)
	r.targets[t] = e
	return e
}
func (s *Service) prune(ctx context.Context, owner string, state *domain.State, refs *referenceCache) error {
	if state.ActiveProjectID != "" {
		err := refs.Project(ctx, owner, state.ActiveProjectID)
		if errors.Is(err, domain.ErrNotFound) {
			state.ActiveProjectID = ""
		} else if err != nil && !errors.Is(err, domain.ErrUnavailable) {
			return err
		}
	}
	kept := make([]domain.Window, 0, len(state.Windows))
	for _, w := range state.Windows {
		err := refs.Target(ctx, owner, w.Target)
		if errors.Is(err, domain.ErrNotFound) {
			continue
		}
		if err != nil && !errors.Is(err, domain.ErrUnavailable) {
			return err
		}
		kept = append(kept, w)
	}
	state.Windows = kept
	state.RepairFocus()
	return nil
}
func same(a, b domain.State) bool {
	a.Revision = 0
	b.Revision = 0
	if len(a.Windows) == 0 {
		a.Windows = nil
	}
	if len(b.Windows) == 0 {
		b.Windows = nil
	}
	return reflect.DeepEqual(a, b)
}
func (s *Service) Get(ctx context.Context, owner string) (domain.State, error) {
	state, _, _, err := s.Changes(ctx, owner, -1)
	return state, err
}

// Changes revalidates the current projection before returning history. If
// revoked references occur in catch-up history, replace it with one safe reset.
func (s *Service) Changes(ctx context.Context, owner string, after int64) (domain.State, []domain.State, bool, error) {
	var current domain.State
	var events []domain.State
	reset := false
	refs := s.references()
	err := s.transact(ctx, owner, func(ctx context.Context, tx ports.Transaction) error {
		before := tx.State()
		current = before.Clone()
		if err := s.prune(ctx, owner, &current, refs); err != nil {
			return err
		}
		if !same(before, current) {
			current.Revision++
			if err := tx.Save(ctx, current); err != nil {
				return err
			}
		}
		if after < 0 {
			return nil
		}
		if after > current.Revision {
			reset = true
			return nil
		}
		if after == current.Revision {
			return nil
		}
		var err error
		events, err = tx.Events(ctx, after)
		if err != nil {
			return err
		}
		if len(events) == 0 || events[0].Revision != after+1 {
			events = nil
			reset = true
			return nil
		}
		for i := range events {
			if events[i].Revision != after+int64(i)+1 || (i == len(events)-1 && events[i].Revision != current.Revision) {
				events = nil
				reset = true
				break
			}
			safe := events[i].Clone()
			if err := s.prune(ctx, owner, &safe, refs); err != nil {
				return err
			}
			if !same(safe, events[i]) {
				events = nil
				reset = true
				break
			}
		}
		return nil
	})
	return current, events, reset, err
}
func (s *Service) Apply(ctx context.Context, owner, key string, op domain.Operation) (domain.State, error) {
	if !domain.Key(key) {
		return domain.State{}, domain.ErrInvalid
	}
	if err := op.Validate(); err != nil {
		return domain.State{}, err
	}
	bytes, _ := json.Marshal(op)
	sum := sha256.Sum256(bytes)
	digest := hex.EncodeToString(sum[:])
	var state domain.State
	refs := s.references()
	err := s.transact(ctx, owner, func(ctx context.Context, tx ports.Transaction) error {
		old := tx.State()
		state = old.Clone()
		stored, exists, err := tx.Request(ctx, key)
		if err != nil {
			return err
		}
		if exists && stored != digest {
			return domain.ErrConflict
		}
		if err := s.prune(ctx, owner, &state, refs); err != nil {
			return err
		}
		if !exists {
			switch op.Kind {
			case "initialize":
				if state.Revision == 0 {
					if op.ProjectID != "" {
						err := refs.Project(ctx, owner, op.ProjectID)
						if err == nil {
							state.ActiveProjectID = op.ProjectID
						} else if !errors.Is(err, domain.ErrNotFound) {
							return err
						}
					}
					for _, t := range op.Windows {
						if err := refs.Target(ctx, owner, t); errors.Is(err, domain.ErrNotFound) {
							continue
						} else if err != nil {
							return err
						}
						if err := state.Open(t, s.ids.New); err != nil {
							return err
						}
					}
					state.ActiveProjectID = op.ProjectID
					if op.ProjectID != "" {
						if err := refs.Project(ctx, owner, op.ProjectID); errors.Is(err, domain.ErrNotFound) {
							state.ActiveProjectID = ""
						} else if err != nil {
							return err
						}
					}
					state.RepairFocus()
				}
			case "switch":
				if op.ProjectID != "" {
					if err := refs.Project(ctx, owner, op.ProjectID); err != nil {
						return err
					}
				}
				state.ActiveProjectID = op.ProjectID
				state.RepairFocus()
			case "open":
				if err := refs.Target(ctx, owner, op.Target); err != nil {
					return err
				}
				if err := state.Open(op.Target, s.ids.New); err != nil {
					return err
				}
			case "close":
				state.Close(op.WindowID)
			case "focus":
				state.Focus(op.WindowID)
			case "session":
				for i, w := range state.Windows {
					if w.ID == op.WindowID {
						if w.Target.Kind != "agent-sessions" {
							return domain.ErrInvalid
						}
						t := w.Target
						t.ResourceKind = ""
						t.ResourceID = ""
						if op.SessionID != "" {
							t.ResourceKind = "session"
							t.ResourceID = op.SessionID
						}
						if err := refs.Target(ctx, owner, t); err != nil {
							return err
						}
						state.Windows[i].Target = t
						state.Focus(w.ID)
						break
					}
				}
			}
			if err := tx.Remember(ctx, key, digest); err != nil {
				return err
			}
		}
		if state.Revision == 0 || !same(old, state) {
			state.Revision++
			return tx.Save(ctx, state)
		}
		return nil
	})
	return state, err
}
