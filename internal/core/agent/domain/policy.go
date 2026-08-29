package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
)

// Policy decision errors. Transports map every one of them to a fixed,
// sanitized Connect code; none of them ever carry policy numbers, quota
// counters, goals, or raw causes into a public message.
var (
	// ErrPolicyStale marks a SetAppPolicy whose expected policy revision does
	// not match the stored fact (including "expected none but one exists").
	// Transport maps it to a sanitized Aborted.
	ErrPolicyStale = errors.New("app agent policy revision is stale")
	// ErrPolicyBlocksRuns marks a fresh App run rejected by an explicit block
	// execution mode. Transport maps it to a sanitized PermissionDenied.
	ErrPolicyBlocksRuns = errors.New("app agent policy blocks new runs")
	// ErrQuotaExhausted marks a fresh run or approval that cannot reserve the
	// daily allowance without exceeding the policy's UTC daily limits. The
	// App run key is not consumed. Transport maps it to ResourceExhausted.
	ErrQuotaExhausted = errors.New("app agent daily quota is exhausted")
	// ErrQuotaBreached marks a bucket whose observed usage exceeded a task
	// reservation: the deterministic circuit break keeps failing fresh runs
	// closed for the rest of the UTC day. Transport maps it to
	// ResourceExhausted.
	ErrQuotaBreached = errors.New("app agent daily quota is circuit-broken by a usage breach")
	// ErrApprovalAlreadyDecided marks a decide call whose approval already
	// reached a terminal decision under a different idempotency key.
	ErrApprovalAlreadyDecided = errors.New("app agent approval is already decided")
	// ErrApprovalNotPending marks a decide call against an approval that can
	// no longer be decided (expired by a policy change, or its task is no
	// longer waiting). Transport maps it to FailedPrecondition.
	ErrApprovalNotPending = errors.New("app agent approval is no longer pending")
	// ErrProviderCapabilityMissing marks a fresh App run whose snapshotted
	// provider does not explicitly support the required hard budget, runtime
	// deadline, and usage-reporting contract. Transport maps it to
	// FailedPrecondition.
	ErrProviderCapabilityMissing = errors.New("provider does not support the required budget contract")
)

// PolicyMode is the execution mode a policy applies to fresh App tasks.
type PolicyMode string

const (
	PolicyModeAllow           PolicyMode = "allow"
	PolicyModeRequireApproval PolicyMode = "require_approval"
	PolicyModeBlock           PolicyMode = "block"
)

// PolicySource distinguishes the pinned code-defined default from an explicit
// owner-set policy row.
type PolicySource string

const (
	PolicySourceSystemDefault PolicySource = "system_default"
	PolicySourceExplicit      PolicySource = "explicit"
)

// Finite, fixed policy bounds (ADR-0005). Zero is never unlimited: every
// limit must be positive and inside its bound, and the daily token allowance
// must be able to hold at least one task reservation.
const (
	MinPolicyOutputTokensPerTask     = 1
	MaxPolicyOutputTokensPerTask     = 1_000_000
	MinPolicyRuntimeSecondsPerTask   = 1
	MaxPolicyRuntimeSecondsPerTask   = 86_400
	MinPolicyTasksPerUTCDay          = 1
	MaxPolicyTasksPerUTCDay          = 10_000
	MinPolicyReservedTokensPerUTCDay = 1
	MaxPolicyReservedTokensPerUTCDay = 10_000_000
)

// SystemDefaultPolicyVersion pins the version of the code-defined default
// policy. Changing the default numbers requires bumping this version, a new
// ADR decision, and test updates — historical task snapshots never re-interpret.
const SystemDefaultPolicyVersion int64 = 1

// SystemDefaultPolicy is the versioned, finite default applied to active App
// installations without an explicit policy row (ADR-0005 §2). It keeps
// existing grant-authorized App runs working while never being unlimited:
// values align with the fact boundaries of the current adapters (DeepSeek
// accepts caps up to 384000 tokens / 600s; Fake emits a fixed handful of
// tokens) but stay far below the provider maxima.
func SystemDefaultPolicy() Policy {
	return Policy{
		Source:   PolicySourceSystemDefault,
		Revision: SystemDefaultPolicyVersion,
		Spec: PolicySpec{
			Mode:                             PolicyModeAllow,
			MaxOutputTokensPerTask:           4096,
			MaxRuntimeSecondsPerTask:         120,
			MaxTasksPerUTCDay:                50,
			MaxReservedOutputTokensPerUTCDay: 204_800,
		},
	}
}

// PolicySpec is the full-replacement policy payload. Field order in Digest is
// declaration order and must stay alphabetical for deterministic encoding.
type PolicySpec struct {
	Mode                             PolicyMode
	MaxOutputTokensPerTask           int64
	MaxRuntimeSecondsPerTask         int64
	MaxTasksPerUTCDay                int64
	MaxReservedOutputTokensPerUTCDay int64
}

// Validate enforces the bounded positive-number grammar: known mode, every
// limit inside its fixed bound, and a daily token allowance that can hold at
// least one per-task reservation.
func (s PolicySpec) Validate() error {
	switch s.Mode {
	case PolicyModeAllow, PolicyModeRequireApproval, PolicyModeBlock:
	default:
		return ErrInvalid
	}
	if s.MaxOutputTokensPerTask < MinPolicyOutputTokensPerTask || s.MaxOutputTokensPerTask > MaxPolicyOutputTokensPerTask {
		return ErrInvalid
	}
	if s.MaxRuntimeSecondsPerTask < MinPolicyRuntimeSecondsPerTask || s.MaxRuntimeSecondsPerTask > MaxPolicyRuntimeSecondsPerTask {
		return ErrInvalid
	}
	if s.MaxTasksPerUTCDay < MinPolicyTasksPerUTCDay || s.MaxTasksPerUTCDay > MaxPolicyTasksPerUTCDay {
		return ErrInvalid
	}
	if s.MaxReservedOutputTokensPerUTCDay < MinPolicyReservedTokensPerUTCDay || s.MaxReservedOutputTokensPerUTCDay > MaxPolicyReservedTokensPerUTCDay {
		return ErrInvalid
	}
	if s.MaxReservedOutputTokensPerUTCDay < s.MaxOutputTokensPerTask {
		return ErrInvalid
	}
	return nil
}

// Digest is the canonical policy-spec identity (sha256, versioned). It covers
// only the spec fields — owner and app instance are the storage namespace,
// never request content.
func (s PolicySpec) Digest() string {
	canonical := struct {
		ExecutionMode                    string `json:"executionMode"`
		MaxOutputTokensPerTask           int64  `json:"maxOutputTokensPerTask"`
		MaxReservedOutputTokensPerUTCDay int64  `json:"maxReservedOutputTokensPerUTCDay"`
		MaxRuntimeSecondsPerTask         int64  `json:"maxRuntimeSecondsPerTask"`
		MaxTasksPerUTCDay                int64  `json:"maxTasksPerUTCDay"`
		Version                          string `json:"version"`
	}{
		ExecutionMode:                    string(s.Mode),
		MaxOutputTokensPerTask:           s.MaxOutputTokensPerTask,
		MaxReservedOutputTokensPerUTCDay: s.MaxReservedOutputTokensPerUTCDay,
		MaxRuntimeSecondsPerTask:         s.MaxRuntimeSecondsPerTask,
		MaxTasksPerUTCDay:                s.MaxTasksPerUTCDay,
		Version:                          "workos.agent-app-policy.v1",
	}
	body, _ := json.Marshal(canonical)
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Policy is the effective policy fact for one App installation: either the
// explicit stored row or the system default. A policy never widens the
// installation grant; it only governs fresh runs.
type Policy struct {
	OwnerUserID   string
	AppInstanceID string
	ProjectID     string
	// AppID is the installation's pinned app identity, resolved from trusted
	// installation facts whenever the effective policy is read.
	AppID    string
	Spec     PolicySpec
	Source   PolicySource
	Revision int64
}

// SetPolicyRequestDigest is the canonical SetAppPolicy request identity
// (sha256, versioned). Same key + same digest replays the first response;
// same key + any other digest is a stable conflict.
func SetPolicyRequestDigest(projectID, appInstanceID string, expectedRevision int64, spec PolicySpec) string {
	canonical := struct {
		ExpectedPolicyRevision int64      `json:"expectedPolicyRevision"`
		InstallationID         string     `json:"installationId"`
		ProjectID              string     `json:"projectId"`
		Spec                   PolicySpec `json:"spec"`
		Version                string     `json:"version"`
	}{
		ExpectedPolicyRevision: expectedRevision,
		InstallationID:         appInstanceID,
		ProjectID:              projectID,
		Spec:                   spec,
		Version:                "workos.agent-app-policy-set.v1",
	}
	body, _ := json.Marshal(canonical)
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// PolicyResultSnapshotVersion pins the first-response snapshot format of
// SetAppPolicy.
const PolicyResultSnapshotVersion = "1"

// EncodePolicyResult freezes the first successful SetAppPolicy response as a
// versioned snapshot so replays return the exact first facts even after the
// policy row later changes.
func EncodePolicyResult(policy Policy) ([]byte, error) {
	payload, err := json.Marshal(map[string]any{
		"result_version":                   PolicyResultSnapshotVersion,
		"projectId":                        policy.ProjectID,
		"executionMode":                    string(policy.Spec.Mode),
		"maxOutputTokensPerTask":           policy.Spec.MaxOutputTokensPerTask,
		"maxRuntimeSecondsPerTask":         policy.Spec.MaxRuntimeSecondsPerTask,
		"maxTasksPerUTCDay":                policy.Spec.MaxTasksPerUTCDay,
		"maxReservedOutputTokensPerUTCDay": policy.Spec.MaxReservedOutputTokensPerUTCDay,
		"policyRevision":                   policy.Revision,
	})
	if err != nil {
		return nil, err
	}
	return payload, nil
}

// DecodePolicyResult rehydrates a stored first-response snapshot. Corrupt
// snapshots are storage corruption: they surface as invalid-domain errors,
// never as a guessed policy.
func DecodePolicyResult(payload []byte) (Policy, error) {
	var snapshot struct {
		ResultVersion                    string `json:"result_version"`
		ProjectID                        string `json:"projectId"`
		ExecutionMode                    string `json:"executionMode"`
		MaxOutputTokensPerTask           int64  `json:"maxOutputTokensPerTask"`
		MaxRuntimeSecondsPerTask         int64  `json:"maxRuntimeSecondsPerTask"`
		MaxTasksPerUTCDay                int64  `json:"maxTasksPerUTCDay"`
		MaxReservedOutputTokensPerUTCDay int64  `json:"maxReservedOutputTokensPerUTCDay"`
		PolicyRevision                   int64  `json:"policyRevision"`
	}
	if err := json.Unmarshal(payload, &snapshot); err != nil {
		return Policy{}, ErrInvalid
	}
	if snapshot.ResultVersion != PolicyResultSnapshotVersion || snapshot.PolicyRevision <= 0 || !ValidAppTaskUUID(snapshot.ProjectID) {
		return Policy{}, ErrInvalid
	}
	spec := PolicySpec{
		Mode:                             PolicyMode(snapshot.ExecutionMode),
		MaxOutputTokensPerTask:           snapshot.MaxOutputTokensPerTask,
		MaxRuntimeSecondsPerTask:         snapshot.MaxRuntimeSecondsPerTask,
		MaxTasksPerUTCDay:                snapshot.MaxTasksPerUTCDay,
		MaxReservedOutputTokensPerUTCDay: snapshot.MaxReservedOutputTokensPerUTCDay,
	}
	if err := spec.Validate(); err != nil {
		return Policy{}, err
	}
	return Policy{
		ProjectID: snapshot.ProjectID,
		Spec:      spec,
		Source:    PolicySourceExplicit,
		Revision:  snapshot.PolicyRevision,
	}, nil
}

// ApprovalDecision is the only decision vocabulary for a pre-run approval.
type ApprovalDecision string

const (
	ApprovalDecisionApprove ApprovalDecision = "approve"
	ApprovalDecisionReject  ApprovalDecision = "reject"
)

// DecideApprovalRequestDigest is the canonical decide-request identity so the
// same key replaying the same decision replays the first response, while the
// same key with the opposite decision is a stable conflict.
func DecideApprovalRequestDigest(approvalID string, decision ApprovalDecision) string {
	canonical := struct {
		ApprovalID string `json:"approvalId"`
		Decision   string `json:"decision"`
		Version    string `json:"version"`
	}{ApprovalID: approvalID, Decision: string(decision), Version: "workos.agent-approval-decide.v1"}
	body, _ := json.Marshal(canonical)
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:])
}
