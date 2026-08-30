package ports

import (
	"context"
	"time"

	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
	harnessv1 "github.com/yangtao121/workos/gen/go/workos/harness/v1"
)

type Emit func(*agentv1.AgentEvent) error

// ArtifactOutput is the neutral, canonical artifact emission contract: the
// only facts a provider may choose are the stable output key, the title, the
// canonical type, and the bounded content. Owner, project, task, artifact
// identity, digest, and time are always server-derived from the active lease.
type ArtifactOutput struct {
	Key     string
	Title   string
	Type    string
	Content []byte
}

// ArtifactSink materializes one provider artifact output through the
// harness worker's private lease-bound Core RPC. A returned error aborts the
// run: the provider must never treat artifact materialization as optional
// and must not emit its terminal event after a failed output.
type ArtifactSink func(ArtifactOutput) error

// Canonical credential purposes the lease contract understands.
const PurposeProviderAPIKeyV1 = "provider-api-key.v1"

// CredentialLease is the neutral short-lived task-bound credential grant a
// worker derives from the Core Credential Vault (ADR-0009). Secret material
// is supplied exactly once per lease, held only in memory, and never
// logged, cached across tasks, or handed to any process other than the
// allowlisted provider child of this one task.
type CredentialLease struct {
	ID                 string
	TaskLeaseID        string
	ConsumerID         string
	Purpose            string
	CredentialRevision int64
	ExpiresAt          time.Time
	Secret             []byte
}

// ValidFor reports whether this lease is a live grant for the given consumer
// and purpose at the given instant.
func (l *CredentialLease) ValidFor(consumerID, purpose string, now time.Time) bool {
	if l == nil || l.ID == "" || len(l.Secret) == 0 {
		return false
	}
	if l.ConsumerID != consumerID || l.Purpose != purpose {
		return false
	}
	return now.Before(l.ExpiresAt)
}

// ContextDocument is one canonical bounded context document the worker
// resolved from Core under the active task lease (ADR-0010). It exists for
// this execution only: no cross-task cache, no local files, no provider
// callback path.
type ContextDocument struct {
	RefType      string
	ArtifactType string
	ArtifactID   string
	Digest       string
	Title        string
	MediaType    string
	Content      []byte
}

// Execution is the neutral provider execution input: one struct, so the
// provider contract grows by fields rather than unstructured positional
// parameters. TaskID and Input are derived facts from the claimed task;
// Credential is nil for providers (and tasks) that need no credential, and
// Context is empty for tasks without pinned context.
// ArtifactBatchSink atomically materializes a group of provider outputs
// whose publication must stand or fall together (ADR-0011). All-or-nothing:
// a partial batch is never observable.
type ArtifactBatchSink func([]ArtifactOutput) error

type Execution struct {
	TaskID         string
	Input          *agentv1.AgentTaskInput
	Emit           Emit
	Artifacts      ArtifactSink
	ArtifactsBatch ArtifactBatchSink
	Credential     *CredentialLease
	Context        []ContextDocument
}

type Provider interface {
	Describe() *harnessv1.HarnessProviderInfo
	Run(context.Context, Execution) error
}
