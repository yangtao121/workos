package domain

import (
	"testing"

	"github.com/yangtao121/workos/internal/platform/ids"
)

func TestTargetsRejectContentAndMalformedReferences(t *testing.T) {
	id := ids.UUIDv7{}.New()
	for _, target := range []Target{
		{Kind: "unknown"}, {Kind: "home", ProjectID: id}, {Kind: "terminal", ProjectID: id}, {Kind: "native", ProjectID: id, ResourceKind: "session", ResourceID: id}, {Kind: "agent-center", ProjectID: id, ResourceKind: "session", ResourceID: id}, {Kind: "agent-sessions", ProjectID: id, ResourceKind: "session", ResourceID: "https://secret"}, {Kind: "files", ProjectID: "not-uuid"}, {Kind: "browser", ResourceID: id},
	} {
		if target.Validate() == nil {
			t.Errorf("accepted unsafe target %+v", target)
		}
	}
	for _, target := range []Target{{Kind: "home"}, {Kind: "files", ProjectID: id}, {Kind: "native", ProjectID: id, ResourceKind: "workload", ResourceID: id}, {Kind: "agent-sessions", ProjectID: id}, {Kind: "workspace-previews", ProjectID: id, ResourceKind: "preview", ResourceID: id}} {
		if err := target.Validate(); err != nil {
			t.Errorf("valid target %+v: %v", target, err)
		}
	}
}
func TestDesktopFocusAndSingletons(t *testing.T) {
	g := ids.UUIDv7{}
	p, q := g.New(), g.New()
	s := State{}
	a := Target{Kind: "agent-sessions", ProjectID: p}
	if err := s.Open(a, g.New); err != nil {
		t.Fatal(err)
	}
	original := s.Windows[0].ID
	a.ResourceKind = "session"
	a.ResourceID = g.New()
	_ = s.Open(a, g.New)
	if len(s.Windows) != 1 || s.Windows[0].ID != original {
		t.Fatal("session selection duplicated window")
	}
	_ = s.Open(Target{Kind: "files", ProjectID: q}, g.New)
	s.Focus(original)
	if s.ActiveProjectID != p || s.FocusedWindowID != original || s.Windows[1].ID != original {
		t.Fatal("focus did not follow logical window")
	}
	s.Close(original)
	s.Focus(original)
	if len(s.Windows) != 1 || s.FocusedWindowID == original {
		t.Fatal("late focus resurrected close")
	}
}
func TestCorruptDesktopRejected(t *testing.T) {
	g := ids.UUIDv7{}
	id := g.New()
	for _, state := range []State{{Revision: -1}, {Revision: 1, FocusedWindowID: id}, {Revision: 0, ActiveProjectID: id}, {Revision: 1, Windows: []Window{{ID: id, Target: Target{Kind: "home"}}, {ID: id, Target: Target{Kind: "home"}}}}} {
		if state.Validate() == nil {
			t.Errorf("accepted %+v", state)
		}
	}
	for _, key := range []string{"", "bad\nkey", string([]byte{255})} {
		if Key(key) {
			t.Errorf("accepted key %q", key)
		}
	}
}

func TestAppWorkloadPinValidationAndReplacement(t *testing.T) {
	g := ids.UUIDv7{}
	target := Target{Kind: "app-surface", ProjectID: g.New(), ResourceKind: "app", ResourceID: g.New(), ExpectedWorkloadID: g.New(), ExpectedWorkloadGeneration: 1}
	if err := target.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Target){func(t *Target) { t.ExpectedWorkloadGeneration = 0 }, func(t *Target) { t.ExpectedWorkloadID = "" }, func(t *Target) { t.ExpectedWorkloadGeneration = -1 }, func(t *Target) { t.Kind = "agent-sessions"; t.ResourceKind = "session" }} {
		invalid := target
		mutate(&invalid)
		if invalid.Validate() == nil {
			t.Fatal("invalid workload pin accepted")
		}
	}
	state := State{}
	_ = state.Open(target, g.New)
	id := state.Windows[0].ID
	target.ExpectedWorkloadGeneration = 2
	target.ExpectedWorkloadID = g.New()
	_ = state.Open(target, g.New)
	if len(state.Windows) != 1 || state.Windows[0].ID != id || state.Windows[0].Target.ExpectedWorkloadGeneration != 2 {
		t.Fatal("explicit reopened app did not update same logical window")
	}
}

func TestInteractiveGenerationPinsCannotBeRewound(t *testing.T) {
	g := ids.UUIDv7{}
	workload := g.New()
	target := Target{Kind: "terminal", ProjectID: g.New(), ResourceKind: "workload", ResourceID: workload, ExpectedWorkloadID: workload, ExpectedWorkloadGeneration: 2}
	if err := target.Validate(); err != nil {
		t.Fatal(err)
	}
	state := State{}
	_ = state.Open(target, g.New)
	original := state.Windows[0].ID
	target.ExpectedWorkloadGeneration = 1
	_ = state.Open(target, g.New)
	if len(state.Windows) != 1 || state.Windows[0].ID != original || state.Windows[0].Target.ExpectedWorkloadGeneration != 2 {
		t.Fatal("late open rolled back generation")
	}
	target.ExpectedWorkloadID = g.New()
	if target.Validate() == nil {
		t.Fatal("interactive pin points to another workload")
	}
}
