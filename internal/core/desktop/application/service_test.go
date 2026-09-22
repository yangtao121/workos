package application

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/yangtao121/workos/internal/core/desktop/domain"
	"github.com/yangtao121/workos/internal/core/desktop/testsupport"
	"github.com/yangtao121/workos/internal/platform/ids"
)

func TestInitializationReplayCloseAndRevocation(t *testing.T) {
	ctx := context.Background()
	g := ids.UUIDv7{}
	owner, p, denied := g.New(), g.New(), g.New()
	refs := &testsupport.References{Denied: map[string]bool{denied: true}}
	store := testsupport.NewStore()
	s := New(store, refs, g)
	init := domain.Operation{Kind: "initialize", ProjectID: p, Windows: []domain.Target{{Kind: "home"}, {Kind: "files", ProjectID: p}, {Kind: "files", ProjectID: denied}}}
	state, err := s.Apply(ctx, owner, "init", init)
	if err != nil || state.Revision != 1 || len(state.Windows) != 2 {
		t.Fatalf("init %+v %v", state, err)
	}
	file := state.Windows[1].ID
	state, err = s.Apply(ctx, owner, "init-2", domain.Operation{Kind: "initialize", ProjectID: denied})
	if err != nil || state.Revision != 1 || state.ActiveProjectID != p {
		t.Fatal("stale initialization overwrote state", err)
	}
	state, err = s.Apply(ctx, owner, "close", domain.Operation{Kind: "close", WindowID: file})
	if err != nil {
		t.Fatal(err)
	}
	revision := state.Revision
	state, err = s.Apply(ctx, owner, "focus", domain.Operation{Kind: "focus", WindowID: file})
	if err != nil || state.Revision != revision || len(state.Windows) != 1 {
		t.Fatal("late focus mutated closed window", err)
	}
	state, err = s.Apply(ctx, owner, "init", init)
	if err != nil || state.Revision != revision || len(state.Windows) != 1 {
		t.Fatal("replay restored historical snapshot", err)
	}
	_, err = s.Apply(ctx, owner, "init", domain.Operation{Kind: "open", Target: domain.Target{Kind: "home"}})
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatal("missing digest conflict", err)
	}
	refs.Denied[p] = true
	state, events, reset, err := s.Changes(ctx, owner, 0)
	if err != nil || state.ActiveProjectID != "" || !reset || len(events) != 0 {
		t.Fatalf("revocation state=%+v reset=%v events=%d err=%v", state, reset, len(events), err)
	}
	other, err := s.Get(ctx, g.New())
	if err != nil || other.Revision != 0 || len(other.Windows) != 0 {
		t.Fatal("owner leak", err)
	}
}
func TestCursorRetentionAndDependencyOutage(t *testing.T) {
	ctx := context.Background()
	g := ids.UUIDv7{}
	owner, p, q := g.New(), g.New(), g.New()
	refs := &testsupport.References{}
	s := New(testsupport.NewStore(), refs, g)
	for i := 0; i < 260; i++ {
		project := p
		if i%2 == 0 {
			project = q
		}
		if _, err := s.Apply(ctx, owner, fmt.Sprint(i), domain.Operation{Kind: "switch", ProjectID: project}); err != nil {
			t.Fatal(err)
		}
	}
	state, events, reset, err := s.Changes(ctx, owner, 1)
	if err != nil || !reset || len(events) != 0 {
		t.Fatal("missing retention reset", err)
	}
	_, events, reset, err = s.Changes(ctx, owner, state.Revision-2)
	if err != nil || reset || len(events) != 2 || events[0].Revision+1 != events[1].Revision {
		t.Fatal("catchup not ordered", err)
	}
	_, _, reset, err = s.Changes(ctx, owner, state.Revision+10)
	if err != nil || !reset {
		t.Fatal("future cursor must reset", err)
	}
	refs.Unavailable = true
	current, err := s.Get(ctx, owner)
	if err != nil || current.ActiveProjectID != state.ActiveProjectID {
		t.Fatal("outage pruned desktop", err)
	}
	if _, err := s.Apply(ctx, owner, "offline-open", domain.Operation{Kind: "open", Target: domain.Target{Kind: "files", ProjectID: p}}); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatal("outage authorized open", err)
	}
}
func TestSelectingMissingWindowDoesNotAuthorizeOrReopenSession(t *testing.T) {
	g := ids.UUIDv7{}
	refs := &testsupport.References{Unavailable: true}
	s := New(testsupport.NewStore(), refs, g)
	state, err := s.Apply(context.Background(), g.New(), "late", domain.Operation{Kind: "session", WindowID: g.New(), SessionID: g.New()})
	if err != nil || len(state.Windows) != 0 {
		t.Fatal("missing selection should no-op", err)
	}
}
