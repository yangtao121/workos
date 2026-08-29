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
