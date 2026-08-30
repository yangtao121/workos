package domain

import "errors"

var ErrUnavailable = errors.New("harness provider catalog is unavailable")

type Health string

const (
	HealthUnknown     Health = "unknown"
	HealthStarting    Health = "starting"
	HealthHealthy     Health = "healthy"
	HealthDegraded    Health = "degraded"
	HealthUnavailable Health = "unavailable"
)

type Capabilities struct {
	Streaming           bool
	PersistentSessions  bool
	Resume              bool
	SteerDuringRun      bool
	Approvals           bool
	ToolRegistration    bool
	MCP                 bool
	Subagents           bool
	WorkspaceMount      bool
	StructuredArtifacts bool
	UsageReporting      bool
	// HardTokenBudget and HardRuntimeDeadline are only true when the adapter
	// demonstrably enforces the corresponding AgentBudget field (ADR-0005);
	// adapters that cannot enforce them must report false.
	HardTokenBudget     bool
	HardRuntimeDeadline bool
	// MaxOutputTokens and MaxRuntimeSeconds are the enforced per-run budget
	// maxima the adapter accepts; zero means the matching hard capability is
	// unsupported. Core refuses fresh App runs whose policy budget exceeds
	// them before any queue or reservation.
	MaxOutputTokens   int64
	MaxRuntimeSeconds int64
	// StructuredArtifacts may only be true when SupportedArtifactTypes is
	// non-empty and names the exact canonical artifact types the adapter
	// demonstrably produces (ADR-0008). Any bool/list drift is capability
	// corruption, never "all types": the catalog surfaces it as unavailable.
	SupportedArtifactTypes []string
	// SupportedContextRefTypes names the exact canonical context ref types
	// the adapter demonstrably consumes as resolved bounded context
	// (ADR-0010). Values outside the canonical vocabulary, duplicates, or a
	// non-empty list on an adapter without the matching runtime behavior are
	// capability corruption: the catalog surfaces them as unavailable.
	SupportedContextRefTypes []string
	// RequiresTaskCredentialLease is true when the adapter can only run with
	// a task-bound credential lease from the Core Credential Vault
	// (ADR-0009). The owner-aware catalog projection reports such providers
	// unavailable to owners without a matching active credential, with a
	// fixed reason that never discloses credential existence or state.
	RequiresTaskCredentialLease bool
}

type Provider struct {
	ID                string
	DisplayName       string
	AdapterVersion    string
	Capabilities      Capabilities
	Health            Health
	UnavailableReason string
}

type Catalog struct {
	Providers         []Provider
	DefaultProviderID string
}
