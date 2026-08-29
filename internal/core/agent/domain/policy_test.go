package domain

import (
	"strings"
	"testing"
)

func validPolicySpec() PolicySpec {
	return PolicySpec{
		Mode: PolicyModeAllow, MaxOutputTokensPerTask: 512, MaxRuntimeSecondsPerTask: 60,
		MaxTasksPerUTCDay: 10, MaxReservedOutputTokensPerUTCDay: 5120,
	}
}

func TestPolicySpecValidateBounds(t *testing.T) {
	t.Parallel()
	if err := validPolicySpec().Validate(); err != nil {
		t.Fatalf("valid spec rejected: %v", err)
	}
	cases := map[string]func(*PolicySpec){
		"zero tokens":            func(s *PolicySpec) { s.MaxOutputTokensPerTask = 0 },
		"negative tokens":        func(s *PolicySpec) { s.MaxOutputTokensPerTask = -1 },
		"tokens over bound":      func(s *PolicySpec) { s.MaxOutputTokensPerTask = MaxPolicyOutputTokensPerTask + 1 },
		"zero runtime":           func(s *PolicySpec) { s.MaxRuntimeSecondsPerTask = 0 },
		"runtime over bound":     func(s *PolicySpec) { s.MaxRuntimeSecondsPerTask = MaxPolicyRuntimeSecondsPerTask + 1 },
		"zero daily tasks":       func(s *PolicySpec) { s.MaxTasksPerUTCDay = 0 },
		"daily tasks over bound": func(s *PolicySpec) { s.MaxTasksPerUTCDay = MaxPolicyTasksPerUTCDay + 1 },
		"zero daily tokens":      func(s *PolicySpec) { s.MaxReservedOutputTokensPerUTCDay = 0 },
		"daily tokens over bound": func(s *PolicySpec) {
			s.MaxReservedOutputTokensPerUTCDay = MaxPolicyReservedTokensPerUTCDay + 1
		},
		"daily below per task": func(s *PolicySpec) { s.MaxReservedOutputTokensPerUTCDay = s.MaxOutputTokensPerTask - 1 },
		"unknown mode":         func(s *PolicySpec) { s.Mode = PolicyMode("steal") },
		"empty mode":           func(s *PolicySpec) { s.Mode = "" },
	}
	for name, mutate := range cases {
		spec := validPolicySpec()
		mutate(&spec)
		if err := spec.Validate(); err == nil {
			t.Fatalf("%s: expected rejection", name)
		}
	}
	for _, mode := range []PolicyMode{PolicyModeAllow, PolicyModeRequireApproval, PolicyModeBlock} {
		spec := validPolicySpec()
		spec.Mode = mode
		if err := spec.Validate(); err != nil {
			t.Fatalf("mode %q rejected: %v", mode, err)
		}
	}
}

func TestPolicySpecDigestDeterministicAndSensitive(t *testing.T) {
	t.Parallel()
	spec := validPolicySpec()
	if spec.Digest() != spec.Digest() {
		t.Fatal("digest is not deterministic")
	}
	if len(spec.Digest()) != len("sha256:")+64 || !strings.HasPrefix(spec.Digest(), "sha256:") {
		t.Fatalf("digest shape invalid: %q", spec.Digest())
	}
	other := validPolicySpec()
	other.Mode = PolicyModeRequireApproval
	if spec.Digest() == other.Digest() {
		t.Fatal("different specs share one digest")
	}
	other = validPolicySpec()
	other.MaxTasksPerUTCDay++
	if spec.Digest() == other.Digest() {
		t.Fatal("limit change must change the digest")
	}
}

func TestSystemDefaultPolicyIsFiniteAndVersioned(t *testing.T) {
	t.Parallel()
	policy := SystemDefaultPolicy()
	if policy.Source != PolicySourceSystemDefault || policy.Revision != SystemDefaultPolicyVersion {
		t.Fatalf("default identity invalid: %#v", policy)
	}
	if policy.Spec.Mode != PolicyModeAllow {
		t.Fatalf("default must keep granted app runs working: %q", policy.Spec.Mode)
	}
	if err := policy.Spec.Validate(); err != nil {
		t.Fatalf("default spec must be valid and finite: %v", err)
	}
}

func TestPolicyResultSnapshotRoundTrip(t *testing.T) {
	t.Parallel()
	policy := Policy{ProjectID: "018f0000-0000-7000-8000-0000000000ab", Spec: validPolicySpec(), Source: PolicySourceExplicit, Revision: 3}
	payload, err := EncodePolicyResult(policy)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodePolicyResult(payload)
	if err != nil {
		t.Fatalf("round trip failed: %v", err)
	}
	if decoded.ProjectID != policy.ProjectID || decoded.Revision != 3 || decoded.Spec != policy.Spec {
		t.Fatalf("snapshot drifted: %#v", decoded)
	}
	if _, err := DecodePolicyResult([]byte(`{"resultVersion":"1"}`)); err == nil {
		t.Fatal("corrupt snapshot must be rejected")
	}
}

func TestApprovalGoalExcerptBounded(t *testing.T) {
	t.Parallel()
	if got := ApprovalGoalExcerpt("short goal"); got != "short goal" {
		t.Fatalf("excerpt mutated a short goal: %q", got)
	}
	long := strings.Repeat("字", MaxApprovalGoalExcerptRunes+10)
	got := ApprovalGoalExcerpt(long)
	if runeCount := len([]rune(got)); runeCount != MaxApprovalGoalExcerptRunes {
		t.Fatalf("excerpt exceeded bound: %d", runeCount)
	}
}

func TestDecideApprovalRequestDigestSensitivity(t *testing.T) {
	t.Parallel()
	id := "018f0000-0000-7000-8000-0000000000cd"
	approve := DecideApprovalRequestDigest(id, ApprovalDecisionApprove)
	if approve != DecideApprovalRequestDigest(id, ApprovalDecisionApprove) {
		t.Fatal("decision digest not deterministic")
	}
	if approve == DecideApprovalRequestDigest(id, ApprovalDecisionReject) {
		t.Fatal("opposite decisions share one digest")
	}
}

func TestUsageReportValidation(t *testing.T) {
	t.Parallel()
	valid := UsageReport{InputTokens: 10, OutputTokens: 4, Model: "fake/deterministic"}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid report rejected: %v", err)
	}
	cases := map[string]UsageReport{
		"negative input":      {InputTokens: -1, OutputTokens: 1},
		"negative output":     {InputTokens: 1, OutputTokens: -1},
		"tokens over bound":   {InputTokens: MaxUsageTokensPerReport + 1},
		"model with control":  {Model: "bad\x07model"},
		"cost not a decimal":  {InputTokens: 1, CostDecimal: "1.2.3"},
		"cost with exponent":  {InputTokens: 1, CostDecimal: "1e9"},
		"cost sign rejected":  {InputTokens: 1, CostDecimal: "-1.0"},
		"cost dot only":       {InputTokens: 1, CostDecimal: "."},
		"cost over char cost": {InputTokens: 1, CostDecimal: strings.Repeat("1", MaxUsageCostDecimalRunes+1)},
	}
	for name, report := range cases {
		if err := report.Validate(); err == nil {
			t.Fatalf("%s: expected rejection", name)
		}
	}
	if err := (UsageReport{InputTokens: 1, CostDecimal: "12.5"}).Validate(); err != nil {
		t.Fatalf("valid cost rejected: %v", err)
	}
}
