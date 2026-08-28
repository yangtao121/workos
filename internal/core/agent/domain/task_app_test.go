package domain

import (
	"strings"
	"testing"
)

func TestAppTaskRequestDigestIsStableAndSensitive(t *testing.T) {
	t.Parallel()
	base := AppTaskRequestDigest("role", "goal")
	if !strings.HasPrefix(base, "sha256:") || len(base) != len("sha256:")+64 {
		t.Fatalf("digest shape invalid: %s", base)
	}
	if base != AppTaskRequestDigest("role", "goal") {
		t.Fatal("digest not stable")
	}
	if AppTaskRequestDigest("role", "other goal") == base {
		t.Fatal("goal change not covered")
	}
	if AppTaskRequestDigest("other role", "goal") == base {
		t.Fatal("role change not covered")
	}
	if AppTaskRequestDigest("", "") == base {
		t.Fatal("empty request must differ")
	}
}

func TestBoundedAppInputGrammar(t *testing.T) {
	t.Parallel()
	if !ValidAppClientIdempotencyKey("client-key-1") {
		t.Fatal("valid key rejected")
	}
	if ValidAppClientIdempotencyKey("") || ValidAppClientIdempotencyKey(strings.Repeat("k", MaxAppClientIdempotencyKey+1)) {
		t.Fatal("key bounds not enforced")
	}
	if !ValidAppTaskRole("") {
		t.Fatal("empty role is valid (optional)")
	}
	if !ValidAppTaskRole(strings.Repeat("r", MaxAppTaskRoleRunes)) {
		t.Fatal("max role rejected")
	}
	if ValidAppTaskRole(strings.Repeat("r", MaxAppTaskRoleRunes+1)) {
		t.Fatal("oversize role accepted")
	}
	if !ValidAppTaskGoal("do the thing") {
		t.Fatal("valid goal rejected")
	}
	if ValidAppTaskGoal("") || ValidAppTaskGoal(strings.Repeat("g", MaxAppTaskGoalBytes+1)) {
		t.Fatal("goal bounds not enforced")
	}
	if ValidAppTaskUUID("") || ValidAppTaskUUID("not-a-uuid") {
		t.Fatal("uuid grammar not enforced")
	}
}
