package ports

import (
	"context"

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

type Provider interface {
	Describe() *harnessv1.HarnessProviderInfo
	Run(context.Context, string, *agentv1.AgentTaskInput, Emit, ArtifactSink) error
}
