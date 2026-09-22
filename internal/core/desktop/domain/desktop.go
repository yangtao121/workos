// Package domain owns the shared logical desktop. It carries references only,
// never access capabilities, provider content, device geometry or URLs.
package domain

import (
	"errors"
	"slices"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

var (
	ErrInvalid     = errors.New("invalid desktop operation")
	ErrNotFound    = errors.New("desktop target not found")
	ErrConflict    = errors.New("desktop operation key conflict")
	ErrLimit       = errors.New("desktop window limit reached")
	ErrUnavailable = errors.New("desktop dependency unavailable")
	ErrCorrupt     = errors.New("desktop state is corrupt")
)

const MaxWindows = 64
const EventRetention = 256

type Target struct {
	Kind                       string `json:"kind"`
	ProjectID                  string `json:"projectId,omitempty"`
	ResourceKind               string `json:"resourceKind,omitempty"`
	ResourceID                 string `json:"resourceId,omitempty"`
	ExpectedWorkloadID         string `json:"expectedWorkloadId,omitempty"`
	ExpectedWorkloadGeneration int64  `json:"expectedWorkloadGeneration,omitempty"`
}
type Window struct {
	ID     string `json:"id"`
	Target Target `json:"target"`
}
type State struct {
	ActiveProjectID string   `json:"activeProjectId,omitempty"`
	Windows         []Window `json:"windows"`
	FocusedWindowID string   `json:"focusedWindowId,omitempty"`
	Revision        int64    `json:"revision"`
}
type Operation struct {
	Kind      string   `json:"kind"`
	ProjectID string   `json:"projectId,omitempty"`
	WindowID  string   `json:"windowId,omitempty"`
	SessionID string   `json:"sessionId,omitempty"`
	Target    Target   `json:"target"`
	Windows   []Target `json:"windows,omitempty"`
}

func UUID(id string) bool {
	u, err := uuid.Parse(id)
	return err == nil && u.String() == id && u.Version() == 7 && u.Variant() == uuid.RFC4122
}
func Key(key string) bool {
	if !utf8.ValidString(key) || len(key) == 0 || utf8.RuneCountInString(key) > 128 {
		return false
	}
	for _, r := range key {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
func (t Target) Validate() error {
	if t.ExpectedWorkloadID != "" || t.ExpectedWorkloadGeneration != 0 {
		if (t.Kind != "app-surface" && t.Kind != "terminal" && t.Kind != "native") || !UUID(t.ExpectedWorkloadID) || t.ExpectedWorkloadGeneration <= 0 || ((t.Kind == "terminal" || t.Kind == "native") && t.ExpectedWorkloadID != t.ResourceID) {
			return ErrInvalid
		}
	}

	global := false
	switch t.Kind {
	case "home", "device-center", "notification-center", "system-monitor", "mission-control", "browser":
		global = true
	case "agent-center", "app-library", "settings", "files", "docs", "code", "artifact-center", "knowledge-center":
	case "agent-sessions":
		if t.ResourceKind != "" && t.ResourceKind != "session" {
			return ErrInvalid
		}
	case "workspace-previews":
		if t.ResourceKind != "" && t.ResourceKind != "preview" {
			return ErrInvalid
		}
	case "app-surface":
		if t.ResourceKind != "app" {
			return ErrInvalid
		}
	case "artifact-viewer":
		if t.ResourceKind != "artifact" {
			return ErrInvalid
		}
	case "terminal", "native":
		if t.ResourceKind != "workload" {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	if global {
		if t.ProjectID != "" {
			return ErrInvalid
		}
	} else if !UUID(t.ProjectID) {
		return ErrInvalid
	}
	if t.ResourceKind != "" {
		if !UUID(t.ResourceID) {
			return ErrInvalid
		}
		switch t.Kind {
		case "agent-sessions", "workspace-previews", "app-surface", "artifact-viewer", "terminal", "native":
		default:
			return ErrInvalid
		}
	} else if t.ResourceID != "" {
		return ErrInvalid
	}
	return nil
}
func (o Operation) Validate() error {
	switch o.Kind {
	case "switch":
		if o.ProjectID != "" && !UUID(o.ProjectID) {
			return ErrInvalid
		}
	case "open":
		return o.Target.Validate()
	case "close", "focus":
		if !UUID(o.WindowID) {
			return ErrInvalid
		}
	case "session":
		if !UUID(o.WindowID) || (o.SessionID != "" && !UUID(o.SessionID)) {
			return ErrInvalid
		}
	case "initialize":
		if o.ProjectID != "" && !UUID(o.ProjectID) {
			return ErrInvalid
		}
		if len(o.Windows) > MaxWindows {
			return ErrLimit
		}
		for _, t := range o.Windows {
			if err := t.Validate(); err != nil {
				return err
			}
		}
	default:
		return ErrInvalid
	}
	return nil
}
func (s State) Clone() State { s.Windows = slices.Clone(s.Windows); return s }
func (s State) Validate() error {
	if s.Revision < 0 || len(s.Windows) > MaxWindows || (s.ActiveProjectID != "" && !UUID(s.ActiveProjectID)) {
		return ErrCorrupt
	}
	seen := map[string]bool{}
	focus := s.FocusedWindowID == ""
	for _, w := range s.Windows {
		if !UUID(w.ID) || seen[w.ID] || w.Target.Validate() != nil {
			return ErrCorrupt
		}
		seen[w.ID] = true
		if w.ID == s.FocusedWindowID {
			focus = w.Target.ProjectID == "" || w.Target.ProjectID == s.ActiveProjectID
		}
	}
	if !focus || (s.Revision == 0 && (len(s.Windows) != 0 || s.ActiveProjectID != "" || s.FocusedWindowID != "")) {
		return ErrCorrupt
	}
	return nil
}
func (s *State) RepairFocus() {
	for _, w := range s.Windows {
		if w.ID == s.FocusedWindowID && (w.Target.ProjectID == "" || w.Target.ProjectID == s.ActiveProjectID) {
			return
		}
	}
	s.FocusedWindowID = ""
	for i := len(s.Windows) - 1; i >= 0; i-- {
		w := s.Windows[i]
		if w.Target.ProjectID == "" || w.Target.ProjectID == s.ActiveProjectID {
			s.FocusedWindowID = w.ID
			return
		}
	}
}
func (s *State) Open(t Target, id func() string) error {
	for _, w := range s.Windows {
		if sameWindow(w.Target, t) {
			for i := range s.Windows {
				if s.Windows[i].ID == w.ID {
					// A delayed open of the same process generation cannot roll a
					// newer pin backwards or erase it through a legacy unpinned target.
					if t.ExpectedWorkloadID == "" && w.Target.ExpectedWorkloadID != "" {
						t.ExpectedWorkloadID = w.Target.ExpectedWorkloadID
						t.ExpectedWorkloadGeneration = w.Target.ExpectedWorkloadGeneration
					} else if t.ExpectedWorkloadID == w.Target.ExpectedWorkloadID && t.ExpectedWorkloadGeneration < w.Target.ExpectedWorkloadGeneration {
						t.ExpectedWorkloadGeneration = w.Target.ExpectedWorkloadGeneration
					}
					s.Windows[i].Target = t
					break
				}
			}
			s.Focus(w.ID)
			return nil
		}
	}
	if len(s.Windows) >= MaxWindows {
		return ErrLimit
	}
	if t.ProjectID != "" {
		s.ActiveProjectID = t.ProjectID
	}
	w := Window{ID: id(), Target: t}
	s.Windows = append(s.Windows, w)
	s.FocusedWindowID = w.ID
	return nil
}
func (s *State) Focus(id string) {
	for i, w := range s.Windows {
		if w.ID == id {
			s.Windows = append(append(s.Windows[:i:i], s.Windows[i+1:]...), w)
			s.FocusedWindowID = id
			if w.Target.ProjectID != "" {
				s.ActiveProjectID = w.Target.ProjectID
			}
			return
		}
	}
}
func (s *State) Close(id string) {
	s.Windows = slices.DeleteFunc(s.Windows, func(w Window) bool { return w.ID == id })
	s.RepairFocus()
}

func sameWindow(a, b Target) bool {
	if a.Kind != b.Kind || a.ProjectID != b.ProjectID {
		return false
	}
	switch a.Kind {
	case "agent-sessions", "workspace-previews":
		return true
	case "app-surface", "terminal", "native":
		return a.ResourceKind == b.ResourceKind && a.ResourceID == b.ResourceID
	default:
		return a == b
	}
}
