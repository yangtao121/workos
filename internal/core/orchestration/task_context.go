// Review artifact context composition (ADR-0010): the submission-time
// verifier proves every context ref pins an existing immutable review
// artifact of this owner and project at the exact digest; the lease-bound
// resolver materializes the pinned documents for exactly one provider start
// inside the claimed task lease's transaction.
package orchestration

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/encoding/protojson"

	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
	agentdomain "github.com/yangtao121/workos/internal/core/agent/domain"
	agentports "github.com/yangtao121/workos/internal/core/agent/ports"
	artifactdomain "github.com/yangtao121/workos/internal/core/artifact/domain"
	dbtransient "github.com/yangtao121/workos/internal/platform/dbtransient"
	"github.com/yangtao121/workos/internal/platform/dbtx"
	"github.com/yangtao121/workos/internal/platform/ids"
)

// MaxResolvedContextAggregateBytes bounds the wire aggregate of one task's
// resolved context documents before any encode: four refs of 512 KiB would
// exceed it, so the aggregate — not only the per-artifact bound — is the
// hard pre-decode limit (ADR-0010).
const MaxResolvedContextAggregateBytes = 1 << 20

// artifactContextVerifier adapts the Artifact application's typed read to
// the router's submission-time port. The Agent module never queries Artifact
// SQL; this wrapper reads through the Artifact application only.
type artifactContextVerifier struct {
	getReview func(ctx context.Context, ownerUserID, artifactID string) (artifactdomain.ReviewArtifact, artifactdomain.NormalizedReviewContent, error)
}

// NewArtifactContextVerifier wraps the artifact application service. The
// verification is read-only: unknown, foreign, wrong-project, non-review,
// and digest-mismatched refs all fail closed without leaking which fact
// mismatched.
func NewArtifactContextVerifier(service interface {
	GetReview(ctx context.Context, ownerUserID, artifactID string) (artifactdomain.ReviewArtifact, artifactdomain.NormalizedReviewContent, error)
}) (ArtifactContextVerifier, error) {
	if service == nil {
		return nil, errors.New("artifact context verifier requires the artifact service")
	}
	return artifactContextVerifier{getReview: service.GetReview}, nil
}

func (v artifactContextVerifier) VerifyTaskContext(ctx context.Context, ownerUserID, projectID string, refs []agentports.ContextRef) error {
	if ownerUserID == "" || projectID == "" {
		return agentdomain.ErrInvalid
	}
	if err := agentdomain.ValidateContextRefs(toAgentDomainRefs(refs)); err != nil {
		return err
	}
	for _, ref := range refs {
		fact, content, err := v.getReview(ctx, ownerUserID, ref.ID)
		if err != nil {
			if errors.Is(err, artifactdomain.ErrNotFound) || errors.Is(err, artifactdomain.ErrUnsupported) {
				// Unknown, foreign, or not-reviewable artifacts are
				// indistinguishable NotFound: no existence oracle.
				return agentdomain.ErrNotFound
			}
			return err
		}
		if fact.ProjectID != projectID || fact.Digest != ref.Revision || content.Digest != ref.Revision {
			return agentdomain.ErrNotFound
		}
	}
	return nil
}

func toAgentDomainRefs(refs []agentports.ContextRef) []agentdomain.ContextRef {
	out := make([]agentdomain.ContextRef, 0, len(refs))
	for _, ref := range refs {
		out = append(out, agentdomain.ContextRef{Type: ref.Type, ID: ref.ID, Revision: ref.Revision})
	}
	return out
}

// TaskContextArtifactReader is the Artifact module's transaction-scoped read
// port the resolver coordinates with.
type TaskContextArtifactReader interface {
	ReviewArtifactContentByID(ctx context.Context, tx dbtx.Tx, artifactID string) (artifactdomain.ReviewArtifact, artifactdomain.NormalizedReviewContent, error)
}

// TaskContextResolver implements the private lease-bound context
// materialization. Every fact is derived from the claimed task lease inside
// one transaction: the Agent authority locks the stream and returns the task
// input, the Artifact store reads each pinned artifact, and the resolver
// revalidates owner, project, subtype, and exact digest before any byte
// leaves Core.
type TaskContextResolver struct {
	pool      *pgxpool.Pool
	tasks     agentports.TaskStreamStore
	artifacts TaskContextArtifactReader
	ids       ids.Generator
}

func NewTaskContextResolver(pool *pgxpool.Pool, tasks agentports.TaskStreamStore, artifacts TaskContextArtifactReader, generator ids.Generator) (*TaskContextResolver, error) {
	if pool == nil || tasks == nil || artifacts == nil || generator == nil {
		return nil, errors.New("task context resolver requires pool, task authority, artifact reader, and ids")
	}
	return &TaskContextResolver{pool: pool, tasks: tasks, artifacts: artifacts, ids: generator}, nil
}

// ResolvedDocument is one canonical context document in request order.
type ResolvedDocument struct {
	RefType      string
	ArtifactType string
	ArtifactID   string
	Digest       string
	Title        string
	MediaType    string
	Content      []byte
}

func (r *TaskContextResolver) Resolve(ctx context.Context, taskLeaseID, workerID string) ([]ResolvedDocument, error) {
	now := time.Now().UTC()
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, storeFailureContext("begin task context resolve", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck -- explicit commit or classified failure
	stream, err := r.tasks.LockTaskArtifactStream(ctx, tx, taskLeaseID, workerID, now)
	if err != nil {
		if errors.Is(err, agentdomain.ErrLeaseLost) || errors.Is(err, agentdomain.ErrTerminal) {
			return nil, err
		}
		return nil, storeFailureContext("lock task context stream", err)
	}
	var input agentv1.AgentTaskInput
	if err := protojson.Unmarshal(stream.Input, &input); err != nil {
		return nil, agentdomain.ErrInvalid
	}
	wireRefs := input.GetContextRefs()
	if len(wireRefs) == 0 {
		if err := tx.Commit(ctx); err != nil {
			return nil, storeFailureContext("commit empty task context", err)
		}
		return []ResolvedDocument{}, nil
	}
	refs := make([]agentports.ContextRef, 0, len(wireRefs))
	for _, ref := range wireRefs {
		refs = append(refs, agentports.ContextRef{Type: ref.GetType(), ID: ref.GetId(), Revision: ref.GetRevision()})
	}
	if err := agentdomain.ValidateContextRefs(toAgentDomainRefs(refs)); err != nil {
		return nil, err
	}
	if stream.ProjectID == "" {
		return nil, agentdomain.ErrInvalid
	}
	aggregate := 0
	documents := make([]ResolvedDocument, 0, len(refs))
	for _, ref := range refs {
		fact, content, err := r.artifacts.ReviewArtifactContentByID(ctx, tx, ref.ID)
		if err != nil {
			if errors.Is(err, artifactdomain.ErrNotFound) {
				// Immutable artifacts cannot vanish; a ref that misses its
				// artifact is stored drift surfaced as lease-lost semantics.
				return nil, agentdomain.ErrLeaseLost
			}
			return nil, err
		}
		if fact.OwnerUserID != stream.OwnerUserID || fact.ProjectID != stream.ProjectID ||
			fact.Digest != ref.Revision || content.Digest != ref.Revision {
			return nil, agentdomain.ErrLeaseLost
		}
		aggregate += len(content.Content)
		if aggregate > MaxResolvedContextAggregateBytes {
			return nil, agentdomain.ErrInvalid
		}
		documents = append(documents, ResolvedDocument{
			RefType:      agentdomain.ContextRefTypeArtifactReviewV1,
			ArtifactType: fact.Type,
			ArtifactID:   fact.ID,
			Digest:       fact.Digest,
			Title:        fact.Title,
			MediaType:    fact.MediaType,
			Content:      content.Content,
		})
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, storeFailureContext("commit task context", err)
	}
	return documents, nil
}

func storeFailureContext(stage string, err error) error {
	if dbtransient.IsTransient(err) {
		return fmt.Errorf("%s: %w: %w", stage, agentports.ErrStoreUnavailable, err)
	}
	return fmt.Errorf("%s: %w", stage, err)
}
