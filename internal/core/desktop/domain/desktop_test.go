package domain

import (
	"testing"

	"github.com/google/uuid"
)

func TestTargetsRejectContentAndMalformedReferences(t *testing.T) {
	id := uuid.Must(uuid.NewV7()).String()
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
	generate := func() string { return uuid.Must(uuid.NewV7()).String() }
	p, q := generate(), generate()
	s := State{}
	a := Target{Kind: "agent-sessions", ProjectID: p}
	if err := s.Open(a, generate); err != nil {
		t.Fatal(err)
	}
	original := s.Windows[0].ID
	a.ResourceKind = "session"
	a.ResourceID = generate()
	_ = s.Open(a, generate)
	if len(s.Windows) != 1 || s.Windows[0].ID != original {
		t.Fatal("session selection duplicated window")
	}
	_ = s.Open(Target{Kind: "files", ProjectID: q}, generate)
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
	generate := func() string { return uuid.Must(uuid.NewV7()).String() }
	id := generate()
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
	generate := func() string { return uuid.Must(uuid.NewV7()).String() }
	target := Target{Kind: "app-surface", ProjectID: generate(), ResourceKind: "app", ResourceID: generate(), ExpectedWorkloadID: generate(), ExpectedWorkloadGeneration: 1}
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
	_ = state.Open(target, generate)
	id := state.Windows[0].ID
	target.ExpectedWorkloadGeneration = 2
	target.ExpectedWorkloadID = generate()
	_ = state.Open(target, generate)
	if len(state.Windows) != 1 || state.Windows[0].ID != id || state.Windows[0].Target.ExpectedWorkloadGeneration != 2 {
		t.Fatal("explicit reopened app did not update same logical window")
	}
}

func TestInteractiveGenerationPinsCannotBeRewound(t *testing.T) {
	generate := func() string { return uuid.Must(uuid.NewV7()).String() }
	workload := generate()
	target := Target{Kind: "terminal", ProjectID: generate(), ResourceKind: "workload", ResourceID: workload, ExpectedWorkloadID: workload, ExpectedWorkloadGeneration: 2}
	if err := target.Validate(); err != nil {
		t.Fatal(err)
	}
	state := State{}
	_ = state.Open(target, generate)
	original := state.Windows[0].ID
	target.ExpectedWorkloadGeneration = 1
	_ = state.Open(target, generate)
	if len(state.Windows) != 1 || state.Windows[0].ID != original || state.Windows[0].Target.ExpectedWorkloadGeneration != 2 {
		t.Fatal("late open rolled back generation")
	}
	target.ExpectedWorkloadID = generate()
	if target.Validate() == nil {
		t.Fatal("interactive pin points to another workload")
	}
}
