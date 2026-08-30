// The lease-bound artifact materialization coordinator (ADR-0008). It is the
// neutral composition point between the Agent and Artifact modules: one
// shared transaction locks the task stream through the Agent module's
// transaction-scoped port, adjudicates the (task, output key) identity and
// persists the immutable review artifact through the Artifact module's
// transaction-scoped port, and appends exactly one Core-minted
// artifact_created timeline event. Neither module writes the other's tables;
// neither ever sees the other's SQL.
//
// Provider inputs (output key, title, typed content) are the only untrusted
// facts. Owner, project, task, artifact identity, digest, time, event
// sequence, and publication are derived from the active lease and Core-minted
// server-side.
package orchestration

import (
	"context"
	"errors"
	"fmt"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
	artifactv1 "github.com/yangtao121/workos/gen/go/workos/artifact/v1"
	agentapp "github.com/yangtao121/workos/internal/core/agent/application"
	agentdomain "github.com/yangtao121/workos/internal/core/agent/domain"
	agentports "github.com/yangtao121/workos/internal/core/agent/ports"
	artifactapp "github.com/yangtao121/workos/internal/core/artifact/application"
	artifactdomain "github.com/yangtao121/workos/internal/core/artifact/domain"
	artifactports "github.com/yangtao121/workos/internal/core/artifact/ports"
	"github.com/yangtao121/workos/internal/platform/dbtx"
	"github.com/yangtao121/workos/internal/platform/ids"
)

// TaskTxSource opens the shared transaction. *pgxpool.Pool satisfies it.
type TaskTxSource interface {
	Begin(ctx context.Context) (dbtx.Tx, error)
}

// TaskStreamStore is the Agent module's transaction-scoped coordination port.
type TaskStreamStore interface {
	LockTaskArtifactStream(ctx context.Context, tx dbtx.Tx, leaseID, workerID string, now time.Time) (agentports.TaskStreamFacts, error)
	AppendPublicationEvent(ctx context.Context, tx dbtx.Tx, stream agentports.TaskStreamFacts, event agentdomain.Event) error
}

// ReviewOutputStore is the Artifact module's transaction-scoped adjudication
// port.
type ReviewOutputStore interface {
	FindTaskOutput(ctx context.Context, tx dbtx.Tx, taskID, outputKey string) (artifactports.TaskOutputRecord, bool, error)
	InsertTaskOutput(ctx context.Context, tx dbtx.Tx, command artifactports.ReviewOutputCommand) (int64, error)
	ReviewArtifactByID(ctx context.Context, tx dbtx.Tx, artifactID string) (artifactdomain.ReviewArtifact, error)
}

// TaskArtifactMaterializer coordinates one provider artifact output into one
// immutable artifact fact plus exactly one Core-minted timeline event.
type TaskArtifactMaterializer struct {
	pool      TaskTxSource
	streams   TaskStreamStore
	artifacts ReviewOutputStore
	preparer  *artifactapp.Service
	ids       ids.Generator
	now       func() time.Time
}

func NewTaskArtifactMaterializer(
	pool TaskTxSource, streams TaskStreamStore, artifacts ReviewOutputStore,
	preparer *artifactapp.Service, generator ids.Generator,
) (*TaskArtifactMaterializer, error) {
	if pool == nil || streams == nil || artifacts == nil || preparer == nil || generator == nil {
		return nil, errors.New("task artifact materializer requires pool, stream store, review output store, artifact preparer, and id generator")
	}
	return &TaskArtifactMaterializer{
		pool: pool, streams: streams, artifacts: artifacts, preparer: preparer,
		ids: generator, now: func() time.Time { return time.Now().UTC() },
	}, nil
}

// MaterializeTaskArtifact validates one provider output against the task the
// active lease holds, then atomically adjudicates, persists, and publishes.
// Replay (identical canonical request after response loss) returns the first
// artifact and its first published event; a different canonical request for
// an already-consumed (task, output key) — or a second artifact of an
// already-materialized type — fails closed with a stable conflict.
func (m *TaskArtifactMaterializer) MaterializeTaskArtifact(ctx context.Context, leaseID, workerID, outputKey, rawTitle, artifactType string, content []byte) (*artifactv1.Artifact, *agentv1.AgentEvent, error) {
	now := m.now()
	tx, err := m.pool.Begin(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("begin artifact materialization: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// 1. Lease-bound provenance: the active lease is the only source of
	// owner/project/task identity. Terminal tasks and lost leases fail
	// closed without touching storage.
	stream, err := m.streams.LockTaskArtifactStream(ctx, tx, leaseID, workerID, now)
	if err != nil {
		return nil, nil, err
	}

	// 2. Scope and request verification: project review artifacts exist only
	// for project-scoped tasks, and only for types the task actually
	// requested. All failures here are client-input verdicts that consume
	// nothing.
	if stream.ProjectID == "" {
		return nil, nil, artifactdomain.ErrInvalid
	}
	requested, err := requestedArtifactTypes(stream.Input)
	if err != nil {
		return nil, nil, err
	}
	if !requested[artifactType] {
		return nil, nil, artifactdomain.ErrInvalid
	}

	// 3. Replay/conflict adjudication on the durable mapping. The replayed
	// raw request must reproduce the stored canonical digest exactly; any
	// other input — including input too malformed to digest — is the stable
	// conflict verdict and consumes nothing.
	if existing, found, findErr := m.artifacts.FindTaskOutput(ctx, tx, stream.TaskID, outputKey); findErr != nil {
		return nil, nil, findErr
	} else if found {
		digest, digestable := artifactapp.ReviewOutputRequestDigestFor(
			stream.ProjectID, stream.TaskID, outputKey, rawTitle, artifactType, content,
		)
		if !digestable || digest != existing.RequestDigest {
			return nil, nil, artifactdomain.ErrOutputConflict
		}
		return m.replay(ctx, tx, stream, existing)
	}

	// 4. Canonical preparation: normalize, bound, digest, and mint the
	// artifact identity. Validation failures consume nothing.
	command, err := m.preparer.PrepareReviewOutput(
		stream.OwnerUserID, stream.ProjectID, stream.TaskID, outputKey, rawTitle, artifactType, content,
	)
	if err != nil {
		return nil, nil, err
	}
	publication := artifactdomain.PublicationRecord{
		EventID: m.ids.New(), EventSeq: stream.LastEventSequence + 1, OccurredAt: now,
	}
	command.Publication = publication

	// 5. Atomic insert: artifact row + adjudication mapping. Zero rows means
	// a concurrent winner consumed the (task, output key) identity or the
	// (task, type) slot under the same serialized stream; re-classify.
	rows, err := m.artifacts.InsertTaskOutput(ctx, tx, command)
	if err != nil {
		return nil, nil, err
	}
	if rows == 0 {
		existing, found, findErr := m.artifacts.FindTaskOutput(ctx, tx, stream.TaskID, outputKey)
		if findErr != nil {
			return nil, nil, findErr
		}
		if found && existing.RequestDigest == command.RequestDigest {
			return m.replay(ctx, tx, stream, existing)
		}
		return nil, nil, artifactdomain.ErrOutputConflict
	}

	// 6. Core-minted publication: exactly one artifact_created event per
	// artifact, in the same transaction as the artifact row.
	event, err := agentapp.NewArtifactPublicationEvent(
		publication.EventID, stream.TaskID, publication.EventSeq, publication.OccurredAt,
		command.Artifact.ID, command.Artifact.Type,
	)
	if err != nil {
		return nil, nil, err
	}
	if err := m.streams.AppendPublicationEvent(ctx, tx, stream, event); err != nil {
		return nil, nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, nil, fmt.Errorf("commit artifact materialization: %w", err)
	}
	return reviewArtifactProto(command.Artifact), publicationProto(stream, publication, command.Artifact), nil
}

// replay returns the first artifact and its first published event. The
// stored publication reference plus the revalidated artifact row reconstruct
// the exact Core-minted event; no second event is ever appended.
func (m *TaskArtifactMaterializer) replay(ctx context.Context, tx dbtx.Tx, stream agentports.TaskStreamFacts, existing artifactports.TaskOutputRecord) (*artifactv1.Artifact, *agentv1.AgentEvent, error) {
	fact, err := m.artifacts.ReviewArtifactByID(ctx, tx, existing.ArtifactID)
	if err != nil {
		return nil, nil, err
	}
	// The mapping must agree with the lease-derived provenance; anything
	// else is stored corruption, not a client error.
	if fact.OwnerUserID != stream.OwnerUserID || fact.ProjectID != stream.ProjectID ||
		fact.SourceTask != stream.TaskID || fact.Type != existing.ArtifactType {
		return nil, nil, artifactdomain.ErrCorrupt
	}
	return reviewArtifactProto(fact), publicationProto(stream, existing.Publication, fact), nil
}

// requestedArtifactTypes decodes the task's canonical input into the set of
// requested output artifact types.
func requestedArtifactTypes(input []byte) (map[string]bool, error) {
	var parsed agentv1.AgentTaskInput
	if err := protojson.Unmarshal(input, &parsed); err != nil {
		return nil, artifactdomain.ErrInvalid
	}
	types := make(map[string]bool, len(parsed.GetOutputArtifactTypes()))
	for _, requested := range parsed.GetOutputArtifactTypes() {
		types[requested] = true
	}
	return types, nil
}

func reviewArtifactProto(fact artifactdomain.ReviewArtifact) *artifactv1.Artifact {
	return &artifactv1.Artifact{
		Id: fact.ID, ProjectId: fact.ProjectID, Type: fact.Type, Title: fact.Title,
		MediaType: fact.MediaType, Digest: fact.Digest,
		TotalSizeBytes: int64(fact.ByteCount), FileCount: 1,
		CreatedAt: timestamppb.New(fact.CreatedAt), SourceTaskId: fact.SourceTask,
	}
}

func publicationProto(stream agentports.TaskStreamFacts, publication artifactdomain.PublicationRecord, fact artifactdomain.ReviewArtifact) *agentv1.AgentEvent {
	return &agentv1.AgentEvent{
		Id: publication.EventID, TaskId: stream.TaskID, Sequence: publication.EventSeq,
		OccurredAt: timestamppb.New(publication.OccurredAt),
		Event: &agentv1.AgentEvent_ArtifactCreated{ArtifactCreated: &agentv1.ArtifactCreated{
			ArtifactId: fact.ID, ArtifactType: fact.Type,
		}},
	}
}
