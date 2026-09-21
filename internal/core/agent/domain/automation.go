package domain

import (
	"encoding/json"
	"strings"
	"unicode/utf8"
)

const MaximumGoalRounds = 32

type SessionDirective struct {
	Kind             string `json:"kind"`
	GoalRef          string `json:"goalRef,omitempty"`
	ExpectedRevision int64  `json:"expectedRevision,omitempty"`
	Objective        string `json:"objective,omitempty"`
	MaxRounds        int32  `json:"maxRounds,omitempty"`
}

func (d SessionDirective) Validate() error {
	if d.Kind == "create_goal" {
		if strings.TrimSpace(d.Objective) == "" || !utf8.ValidString(d.Objective) || len(d.Objective) > 16384 || d.GoalRef != "" || d.ExpectedRevision != 0 || d.MaxRounds < 1 || d.MaxRounds > MaximumGoalRounds {
			return ErrSessionInputInvalid
		}
		return nil
	}
	if (d.Kind != "resume_goal" && d.Kind != "pause_goal") || !validGoalRef(d.GoalRef) || d.ExpectedRevision < 1 || d.Objective != "" || d.MaxRounds != 0 {
		return ErrSessionInputInvalid
	}
	return nil
}

func validGoalRef(ref string) bool {
	return len(ref) > 0 && len(ref) <= 128 && utf8.ValidString(ref) && strings.TrimSpace(ref) == ref && !strings.ContainsAny(ref, "\x00\r\n")
}

// GoalProjection is canonical display state, not a second goal scheduler.
type GoalProjection struct {
	Ref           string `json:"ref"`
	Revision      int64  `json:"revision"`
	Objective     string `json:"objective"`
	Phase         string `json:"phase"`
	RoundsStarted int32  `json:"roundsStarted"`
	MaxRounds     int32  `json:"maxRounds"`
	BlockedReason string `json:"blockedReason,omitempty"`
	Armed         bool   `json:"armed"`
}

func (g GoalProjection) Validate() error {
	if !validGoalRef(g.Ref) || g.Revision < 1 || strings.TrimSpace(g.Objective) == "" || !utf8.ValidString(g.Objective) || len(g.Objective) > 16384 || g.MaxRounds < 1 || g.MaxRounds > MaximumGoalRounds || g.RoundsStarted < 0 || g.RoundsStarted > g.MaxRounds || len(g.BlockedReason) > 2048 || !utf8.ValidString(g.BlockedReason) {
		return ErrInvalid
	}
	switch g.Phase {
	case "active":
	case "paused", "blocked", "complete":
		if g.Armed {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}

func DecodeGoalProjection(raw json.RawMessage) (*GoalProjection, error) {
	if len(raw) == 0 || string(raw) == "{}" {
		return nil, nil
	}
	var goal GoalProjection
	if json.Unmarshal(raw, &goal) != nil || goal.Validate() != nil {
		return nil, ErrInvalid
	}
	return &goal, nil
}

type Delegation struct {
	ID, OwnerUserID, SessionID, TaskID, Key, Title string
	BindingID, SourceID                            string
	BindingRevision                                int64
	State, WorktreeID, BaseCommit                  string
	ResultSummary, ResultArtifactID                string
}

func (d Delegation) Terminal() bool {
	return d.State == "completed" || d.State == "failed" || d.State == "cancelled" || d.State == "needs_review"
}
