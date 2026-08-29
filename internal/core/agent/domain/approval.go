package domain

import (
	"time"
)

// ApprovalState is the lifecycle of one pre-run approval. Pending approvals
// can only be resolved by an owner decision or by a real policy change; both
// terminal paths are durable and audit-visible.
type ApprovalState string

const (
	ApprovalPending  ApprovalState = "pending"
	ApprovalApproved ApprovalState = "approved"
	ApprovalRejected ApprovalState = "rejected"
	ApprovalExpired  ApprovalState = "expired"
)

func (s ApprovalState) Terminal() bool {
	return s == ApprovalApproved || s == ApprovalRejected || s == ApprovalExpired
}

// MaxApprovalGoalExcerptRunes bounds the untrusted task summary persisted
// with an approval and returned to the owner UI. Callers must render it as
// text only.
const MaxApprovalGoalExcerptRunes = 512

// Approval is one pre-run approval fact. It carries the full policy spec
// snapshot the waiting task was adjudicated under, so a later decision
// reserves and enqueues against exactly the approved numbers — never against
// whatever policy happens to be current at decide time.
type Approval struct {
	ID            string
	OwnerUserID   string
	AppInstanceID string
	ProjectID     string
	TaskID        string
	AppID         string
	GoalExcerpt   string
	ProviderID    string
	// Source and Spec snapshot the effective policy identity the waiting
	// task was adjudicated under.
	Source   PolicySource
	Spec     PolicySpec
	Revision int64
	State    ApprovalState
	// DecidedIdempotencyKey and DecisionDigest replay the owner decision
	// exactly like every other durable idempotency fact; they are empty
	// while pending or expired.
	DecidedIdempotencyKey string
	DecisionDigest        string
	CreatedAt             time.Time
	UpdatedAt             time.Time
	DecidedAt             time.Time
}

// ApprovalGoalExcerpt derives the bounded untrusted summary from a validated
// goal: a pure rune-boundary prefix, no ellipsis, no rewriting.
func ApprovalGoalExcerpt(goal string) string {
	runes := []rune(goal)
	if len(runes) <= MaxApprovalGoalExcerptRunes {
		return goal
	}
	return string(runes[:MaxApprovalGoalExcerptRunes])
}

// UsageReport is one validated provider usage observation carried by a
// usage_recorded event. Values are non-negative and bounded; cost is an
// optional verified observation, never an enforcement input.
type UsageReport struct {
	InputTokens  int64
	OutputTokens int64
	CostDecimal  string
	Model        string
}

// Bounded usage-event grammar.
const (
	MaxUsageTokensPerReport  = 10_000_000
	MaxUsageModelRunes       = 128
	MaxUsageCostDecimalRunes = 32
)

// Validate enforces the usage report grammar at the boundary.
func (u UsageReport) Validate() error {
	if u.InputTokens < 0 || u.OutputTokens < 0 {
		return ErrInvalid
	}
	if u.InputTokens > MaxUsageTokensPerReport || u.OutputTokens > MaxUsageTokensPerReport {
		return ErrInvalid
	}
	if !validBoundedText(u.Model, MaxUsageModelRunes, false) {
		return ErrInvalid
	}
	if u.CostDecimal == "" {
		return nil
	}
	if len(u.CostDecimal) > MaxUsageCostDecimalRunes {
		return ErrInvalid
	}
	// Plain non-negative decimal with an optional fractional part; no sign,
	// no exponent. Cost is observation only, but it still must be a real
	// number, not free text.
	digits := 0
	seenDot := false
	for index := 0; index < len(u.CostDecimal); index++ {
		c := u.CostDecimal[index]
		switch {
		case c >= '0' && c <= '9':
			digits++
		case c == '.' && !seenDot && digits > 0:
			seenDot = true
		default:
			return ErrInvalid
		}
	}
	if digits == 0 {
		return ErrInvalid
	}
	return nil
}
