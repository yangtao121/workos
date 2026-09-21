package domain

import (
	"encoding/json"
	"testing"
	"time"
)

func TestNativeDirectivesValidateAndSeparateIdempotency(t *testing.T) {
	create := SessionDirective{Kind: "create_goal", Objective: "Complete the fixture", MaxRounds: 3}
	for _, d := range []SessionDirective{create, {Kind: "resume_goal", GoalRef: "native-ref", ExpectedRevision: 2}, {Kind: "pause_goal", GoalRef: "native-ref", ExpectedRevision: 2}} {
		if err := d.Validate(); err != nil {
			t.Fatal(err)
		}
		session := newTestSession()
		input, _, err := session.AcceptSessionDirective(time.Now().UTC(), "input", "key", d)
		if err != nil || input.Text != "" || input.Directive == nil || input.RequestDigest == InputRequestDigest("key", "") {
			t.Fatalf("invalid directive input: %+v %v", input, err)
		}
		changed := d
		changed.Objective = "changed"
		if DirectiveRequestDigest("key", d) == DirectiveRequestDigest("key", changed) {
			t.Fatal("directive replay not content bound")
		}
	}
	for _, d := range []SessionDirective{{}, {Kind: "create_goal", Objective: " ", MaxRounds: 3}, {Kind: "create_goal", Objective: "fixture", MaxRounds: 33}, {Kind: "resume_goal", GoalRef: "native-ref"}, {Kind: "pause_goal", GoalRef: "other\nref", ExpectedRevision: 1}, {Kind: "resume_goal", GoalRef: "ref", ExpectedRevision: 1, Objective: "replace"}} {
		if d.Validate() == nil {
			t.Fatalf("invalid directive accepted: %+v", d)
		}
	}
}

func TestNativeGoalProjectionRejectsInvalidDurableFacts(t *testing.T) {
	valid := GoalProjection{Ref: "opaque-native-ref", Revision: 1, Objective: "fixture", Phase: "active", MaxRounds: 3, Armed: true}
	raw, _ := json.Marshal(valid)
	if goal, err := DecodeGoalProjection(raw); err != nil || *goal != valid {
		t.Fatalf("roundtrip: %v %v", goal, err)
	}
	for _, mutate := range []func(*GoalProjection){func(g *GoalProjection) { g.RoundsStarted = 4 }, func(g *GoalProjection) { g.Phase = "unknown" }, func(g *GoalProjection) { g.Phase = "paused" }, func(g *GoalProjection) { g.Revision = 0 }} {
		value := valid
		mutate(&value)
		raw, _ := json.Marshal(value)
		if _, err := DecodeGoalProjection(raw); err == nil {
			t.Fatalf("invalid projection accepted: %+v", value)
		}
	}
}
