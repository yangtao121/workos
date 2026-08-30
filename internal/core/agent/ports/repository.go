package ports

import (
	"context"
	"errors"
	"time"

	"github.com/yangtao121/workos/internal/core/agent/domain"
)

// ErrStoreUnavailable marks a temporarily unreachable Agent store. The
// postgres adapter wraps transient driver failures (connection, resource,
// operator intervention) with it at the port boundary using the shared
// dbtransient classification; transports map it to a sanitized retryable
// Unavailable. Invariant/constraint failures stay opaque internal errors.
var ErrStoreUnavailable = errors.New("agent store is temporarily unavailable")

// AppTaskProvenance is the durable App fact bound to one bridge-created task:
// which app installation of which owner used which client key, and the
// canonical request digest that adjudicates replays.
type AppTaskProvenance struct {
	// TaskIdempotencyKey is the opaque unique value stored in the task row's
	// own key column; App adjudication lives in the mapping below.
	TaskIdempotencyKey   string
	AppInstanceID        string
	ClientIdempotencyKey string
	RequestDigest        string
}

// AppTaskRequestRecord is one persisted App task mapping read.
type AppTaskRequestRecord struct {
	RequestDigest string
	TaskID        string
	ProjectID     string
}

// PolicySnapshot is the effective policy identity persisted on every App
// task at creation time, so history is never re-interpreted under a later
// policy.
type PolicySnapshot struct {
	Source     domain.PolicySource
	Revision   int64
	SpecDigest string
	Spec       domain.PolicySpec
}

// DailyAllowance is the per-UTC-day reservation ceiling enforced by the
// guarded bucket update.
type DailyAllowance struct {
	MaxTasks                int64
	MaxReservedOutputTokens int64
}

// InstallationFacts is the neutral projection of installation liveness the
// policy and approval services revalidate before any mutation. It is
// deliberately not the Project module's type: the Agent module never imports
// Project adapters or internals.
type InstallationFacts struct {
	AppID              string
	GrantedPermissions []string
	GrantRevision      int64
}

// InstallationSource revalidates owner/project/installation liveness through
// a neutral port. Unknown, foreign, uninstalled, or archived-project facts
// surface as domain.ErrNotFound.
type InstallationSource interface {
	ResolveActiveInstallation(ctx context.Context, ownerUserID, projectID, installationID string) (InstallationFacts, error)
}

// ProviderCapabilities is the budget-contract, artifact-capability, and
// credential subset of the harness catalog a fresh run is verified against
// before enqueueing. The maxima are the enforced per-run budget bounds the
// provider declared; zero means the corresponding hard capability is
// unsupported.
type ProviderCapabilities struct {
	HardTokenBudget     bool
	HardRuntimeDeadline bool
	UsageReporting      bool
	MaxOutputTokens     int64
	MaxRuntimeSeconds   int64
	// StructuredArtifacts is only true when SupportedArtifactTypes is
	// non-empty and exact (ADR-0008). Core refuses requested artifact types
	// outside the resolved provider's exact list before queueing.
	StructuredArtifacts    bool
	SupportedArtifactTypes []string
	// RequiresTaskCredentialLease is true when the provider can only run
	// with a task-bound credential lease from the Core Credential Vault
	// (ADR-0009). Fresh tasks for such providers are admitted only when the
	// owner has an active credential; the exact snapshot is resolved and
	// persisted before any queue, outbox, reservation, or waiting approval.
	RequiresTaskCredentialLease bool
}

// CredentialSnapshotRef is the opaque credential identity resolved for a
// fresh task: the vault's credential ID and its exact revision.
type CredentialSnapshotRef struct {
	CredentialID string
	Revision     int64
}

// CredentialSnapshots resolves the owner's active credential for one
// consumer. Unknown/revoked facts surface as domain.ErrNotFound.
type CredentialSnapshots interface {
	ActiveSnapshot(ctx context.Context, ownerUserID, consumerID string) (CredentialSnapshotRef, error)
}

// CredentialSnapshotVerifier re-proves that one task's durable snapshot
// still points at the active credential revision. A revoke or rotate fails
// closed as domain.ErrLeaseLost; unknown credentials are indistinguishable.
type CredentialSnapshotVerifier interface {
	VerifySnapshot(ctx context.Context, ownerUserID, consumerID, credentialID string, revision int64) error
}

// Complete reports whether the provider explicitly supports the full budget
// contract App runs are adjudicated against.
func (c ProviderCapabilities) Complete() bool {
	return c.HardTokenBudget && c.HardRuntimeDeadline && c.UsageReporting
}

// Supports reports whether the provider enforces the full budget contract and
// accepts the given per-task limits. A policy budget beyond the provider's
// enforced maxima would only fail inside the adapter — after the quota
// reservation and the queue slot — so it must fail closed here instead.
func (c ProviderCapabilities) Supports(maxOutputTokens, maxRuntimeSeconds int64) bool {
	return c.Complete() &&
		c.MaxOutputTokens > 0 && c.MaxOutputTokens >= maxOutputTokens &&
		c.MaxRuntimeSeconds > 0 && c.MaxRuntimeSeconds >= maxRuntimeSeconds
}

// SupportsArtifactTypes reports whether the provider demonstrably produces
// every requested canonical artifact type. Any request requires the bool
// capability, a non-empty exact list, and membership for each requested
// type; a bool/list drift therefore fails closed instead of silently
// widening the provider's contract (ADR-0008).
func (c ProviderCapabilities) SupportsArtifactTypes(requested []string) bool {
	if len(requested) == 0 {
		return true
	}
	if !c.StructuredArtifacts || len(c.SupportedArtifactTypes) == 0 {
		return false
	}
	supported := make(map[string]struct{}, len(c.SupportedArtifactTypes))
	for _, artifactType := range c.SupportedArtifactTypes {
		supported[artifactType] = struct{}{}
	}
	for _, artifactType := range requested {
		if _, ok := supported[artifactType]; !ok {
			return false
		}
	}
	return true
}

// ProviderCatalog resolves provider budget capabilities. Unknown providers
// surface as domain.ErrNotFound; catalog unavailability carries
// ErrStoreUnavailable semantics from the underlying source.
type ProviderCatalog interface {
	Capabilities(ctx context.Context, providerID string) (ProviderCapabilities, error)
}

// SetPolicyCommand is one full-replacement policy mutation adjudicated inside
// a single Agent transaction.
type SetPolicyCommand struct {
	OwnerUserID            string
	AppInstanceID          string
	ProjectID              string
	Spec                   domain.PolicySpec
	SpecDigest             string
	ExpectedPolicyRevision int64
	IdempotencyKey         string
	RequestDigest          string
	Now                    time.Time
}

// SetPolicyResult reports the mutation verdict: Replay means the key was
// already consumed by the same canonical request and the first response
// snapshot is authoritative; Changed means the policy row actually moved
// (revision +1, pending approvals expired); Noop means the identical spec was
// re-set and the key was consumed without a revision bump.
type SetPolicyResult struct {
	Replay  bool
	Changed bool
}

// PolicyRequestRecord is one consumed SetAppPolicy key with its versioned
// first-response snapshot.
type PolicyRequestRecord struct {
	RequestDigest string
	Result        []byte
}

// DecideApprovalCommand is one owner decision adjudicated inside a single
// Agent transaction.
type DecideApprovalCommand struct {
	OwnerUserID    string
	ApprovalID     string
	Decision       domain.ApprovalDecision
	IdempotencyKey string
	DecisionDigest string
	Now            time.Time
}

type Repository interface {
	Create(context.Context, domain.Task, string) (domain.Task, error)
	// CreateForApp inserts the task, the App provenance mapping, the guarded
	// daily quota reservation, and the task outbox row in one transaction
	// (policy mode allow). A concurrent same-key mapping winner replays or
	// conflicts exactly like the user path; the losing transaction rolls back
	// with no orphan task, mapping, reservation, or outbox row. When the
	// guarded reservation cannot fit the daily allowance the whole transaction
	// fails with domain.ErrQuotaExhausted and the key is not consumed.
	CreateForApp(context.Context, domain.Task, AppTaskProvenance, PolicySnapshot, DailyAllowance) (domain.Task, error)
	// CreateForAppApproval inserts the waiting task, the App provenance
	// mapping, the pending approval (full policy-spec snapshot), and the
	// approval_required event in one transaction. It deliberately creates no
	// claimable outbox row and reserves no quota: a waiting task is not an
	// enqueued task.
	CreateForAppApproval(context.Context, domain.Task, domain.Approval, AppTaskProvenance) (domain.Task, domain.Approval, error)
	Get(context.Context, string, string) (domain.Task, error)
	GetByIdempotency(context.Context, string, string) (domain.Task, error)
	// GetAppTaskRequest reads one consumed (owner, app instance, client key)
	// mapping for replay adjudication.
	GetAppTaskRequest(context.Context, string, string, string) (AppTaskRequestRecord, bool, error)
	// GetAppTaskByTask reads the mapping that proves one task was created by
	// one app installation of one owner (watch provenance check).
	GetAppTaskByTask(context.Context, string, string, string) (AppTaskRequestRecord, bool, error)
	List(context.Context, string, string, string, int) ([]domain.Task, error)
	Cancel(context.Context, string, string, string, time.Time) (domain.Task, *domain.Event, error)
	ListEvents(context.Context, string, string, int64, int) ([]domain.Event, error)
	Claim(context.Context, string, time.Duration, string, time.Time) (*domain.Lease, error)
	Renew(context.Context, string, string, time.Duration, time.Time) (time.Time, bool, error)
	// AppendEvent persists one worker event and advances the task state in the
	// lease's transaction. A non-nil usage report projects the observation
	// into the Agent-owned per-task and per-bucket usage facts in the same
	// transaction, tripping the deterministic circuit break when the reported
	// output exceeds the task's reserved budget.
	AppendEvent(context.Context, string, string, domain.Event, domain.State, string, string, *domain.UsageReport, time.Time) (domain.Event, error)
	FinishLease(context.Context, string, string, time.Time) error

	// GetPolicy reads the explicit policy row for one installation, or
	// found=false when only the system default applies.
	GetPolicy(ctx context.Context, ownerUserID, appInstanceID string) (domain.Policy, bool, error)
	// SetPolicy adjudicates one full-replacement policy mutation: idempotency
	// replay/conflict first, optimistic expected-revision check, then
	// policy upsert plus atomic invalidation of all pending approvals of the
	// same installation (their waiting tasks terminate) in the same
	// transaction.
	SetPolicy(ctx context.Context, command SetPolicyCommand) (domain.Policy, SetPolicyResult, error)
	// GetPolicyRequest reads one consumed SetAppPolicy key for replay.
	GetPolicyRequest(ctx context.Context, ownerUserID, idempotencyKey string) (PolicyRequestRecord, bool, error)

	// GetApproval reads one approval; unknown or foreign IDs are
	// domain.ErrNotFound without existence disclosure.
	GetApproval(ctx context.Context, ownerUserID, approvalID string) (domain.Approval, error)
	// ListApprovals pages the owner's approvals in deterministic id order with
	// optional project/state filters (empty filter = match all).
	ListApprovals(ctx context.Context, ownerUserID, projectID string, state domain.ApprovalState, cursor string, limit int) ([]domain.Approval, error)
	// DecideApproval adjudicates one owner decision inside a single Agent
	// transaction: decision idempotency replay/conflict, guarded daily
	// reservation (approve), waiting→queued transition + approval_decided
	// event + claimable outbox row (approve), terminal rejection with no
	// outbox and no reservation (reject). Any failure leaves the approval
	// exactly as it was.
	DecideApproval(ctx context.Context, command DecideApprovalCommand) (domain.Approval, error)

	// GetAppDailyUsage reads the reservation bucket and usage projection for
	// one (installation, UTC date); missing rows read as zeroes. The date is
	// YYYY-MM-DD UTC.
	GetAppDailyUsage(ctx context.Context, ownerUserID, appInstanceID string, date string) (DailyUsage, error)
}

// DailyUsage is the storage projection of one quota bucket: reserved
// allowance and observed usage stay separate facts, and cost is only
// available when a verified observation exists.
type DailyUsage struct {
	UTCDate              string
	TasksReserved        int64
	OutputTokensReserved int64
	ReservationRevision  int64
	TasksRecorded        int64
	InputTokensRecorded  int64
	OutputTokensRecorded int64
	CostDecimalRecorded  string
	CostAvailable        bool
	QuotaBreached        bool
}
