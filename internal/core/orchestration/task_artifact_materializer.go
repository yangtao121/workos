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
	"math"
	"strings"
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
	indexfeeddomain "github.com/yangtao121/workos/internal/core/indexfeed/domain"
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
	PublicationEventMatches(ctx context.Context, tx dbtx.Tx, expected agentdomain.Event) (bool, error)
}

// ReviewOutputStore is the Artifact module's transaction-scoped adjudication
// port.
type ReviewOutputStore interface {
	FindTaskOutput(ctx context.Context, tx dbtx.Tx, taskID, outputKey string) (artifactports.TaskOutputRecord, bool, error)
	InsertTaskOutput(ctx context.Context, tx dbtx.Tx, command artifactports.ReviewOutputCommand) (int64, error)
	ReviewArtifactByID(ctx context.Context, tx dbtx.Tx, artifactID string) (artifactdomain.ReviewArtifact, error)
}

// IndexPublicationSink is the index feed's transaction-scoped append port
// (ADR-0013). The publication joins this same transaction: it commits
// exactly when the artifact commits, and any failure rolls the whole
// materialization back.
type IndexPublicationSink interface {
	AppendReviewArtifactUpsert(ctx context.Context, tx dbtx.Tx, publication indexfeeddomain.Publication) error
}

// TaskArtifactMaterializer coordinates one provider artifact output into one
// immutable artifact fact plus exactly one Core-minted timeline event plus
// exactly one durable index publication.
type TaskArtifactMaterializer struct {
	pool      TaskTxSource
	streams   TaskStreamStore
	artifacts ReviewOutputStore
	preparer  *artifactapp.Service
	feedSink  IndexPublicationSink
	ids       ids.Generator
	now       func() time.Time
}

func NewTaskArtifactMaterializer(
	pool TaskTxSource, streams TaskStreamStore, artifacts ReviewOutputStore,
	preparer *artifactapp.Service, feedSink IndexPublicationSink, generator ids.Generator,
) (*TaskArtifactMaterializer, error) {
	if pool == nil || streams == nil || artifacts == nil || preparer == nil || feedSink == nil || generator == nil {
		return nil, errors.New("task artifact materializer requires pool, stream store, review output store, artifact preparer, index publication sink, and id generator")
	}
	return &TaskArtifactMaterializer{
		pool: pool, streams: streams, artifacts: artifacts, preparer: preparer,
		feedSink: feedSink,
		ids:      generator, now: func() time.Time { return time.Now().UTC() },
	}, nil
}

// MaterializeTaskArtifact validates one provider output against the task the
// active lease holds, then atomically adjudicates, persists, and publishes.
// Replay (identical canonical request after response loss) returns the first
// artifact and its first published event; a different canonical request for
// an already-consumed (task, output key) — or a second artifact of an
// already-materialized type — fails closed with a stable conflict.
func (m *TaskArtifactMaterializer) MaterializeTaskArtifact(ctx context.Context, leaseID, workerID, outputKey, rawTitle, artifactType string, content []byte) (*artifactv1.Artifact, *agentv1.AgentEvent, error) {
	now := artifactdomain.CanonicalUTCTime(m.now())
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
	if !validTaskArtifactStream(stream) {
		return nil, nil, artifactdomain.ErrCorrupt
	}

	// Steps 2–6 run through the shared per-item path; the sequence base is
	// the stream's current last event. Project review artifacts exist only
	// for project-scoped tasks.
	if stream.ProjectID == "" {
		return nil, nil, artifactdomain.ErrInvalid
	}
	requested, err := requestedArtifactTypes(stream.Input, stream.ProjectID)
	if err != nil {
		return nil, nil, err
	}
	nextSeq := stream.LastEventSequence
	artifact, event, err := m.materializeItem(ctx, tx, stream, requested, &nextSeq, outputKey, rawTitle, artifactType, content, now)
	if err != nil {
		return nil, nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, nil, fmt.Errorf("commit artifact materialization: %w", err)
	}
	return artifact, event, nil
}

// MaterializeTaskArtifactBatch atomically materializes up to two provider
// outputs whose publication must stand or fall together (ADR-0011). One
// transaction locks the stream, then walks the outputs in request order:
// each is replayed exactly or prepared/inserted/published with consecutive
// Core-minted event sequences. Any conflict, corruption, or validation
// failure aborts the whole transaction — the task stream gains nothing.
func (m *TaskArtifactMaterializer) MaterializeTaskArtifactBatch(ctx context.Context, leaseID, workerID string, outputs []BatchOutput) ([]*artifactv1.Artifact, []*agentv1.AgentEvent, error) {
	if len(outputs) == 0 || len(outputs) > 2 {
		return nil, nil, artifactdomain.ErrInvalid
	}
	seenKeys := make(map[string]struct{}, len(outputs))
	seenTypes := make(map[string]struct{}, len(outputs))
	for _, output := range outputs {
		if _, dupKey := seenKeys[output.Key]; dupKey {
			return nil, nil, artifactdomain.ErrInvalid
		}
		if _, dupType := seenTypes[output.Type]; dupType {
			return nil, nil, artifactdomain.ErrInvalid
		}
		seenKeys[output.Key] = struct{}{}
		seenTypes[output.Type] = struct{}{}
	}
	now := artifactdomain.CanonicalUTCTime(m.now())
	tx, err := m.pool.Begin(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("begin artifact batch materialization: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	stream, err := m.streams.LockTaskArtifactStream(ctx, tx, leaseID, workerID, now)
	if err != nil {
		return nil, nil, err
	}
	if !validTaskArtifactStream(stream) {
		return nil, nil, artifactdomain.ErrCorrupt
	}
	if stream.ProjectID == "" {
		return nil, nil, artifactdomain.ErrInvalid
	}
	requested, err := requestedArtifactTypes(stream.Input, stream.ProjectID)
	if err != nil {
		return nil, nil, err
	}
	nextSeq := stream.LastEventSequence
	artifacts := make([]*artifactv1.Artifact, 0, len(outputs))
	events := make([]*agentv1.AgentEvent, 0, len(outputs))
	for _, output := range outputs {
		artifact, event, err := m.materializeItem(ctx, tx, stream, requested, &nextSeq,
			output.Key, output.Title, output.Type, output.Content, now)
		if err != nil {
			// The shared transaction rolls back: zero new writes for the batch.
			return nil, nil, err
		}
		artifacts = append(artifacts, artifact)
		events = append(events, event)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, nil, fmt.Errorf("commit artifact batch materialization: %w", err)
	}
	return artifacts, events, nil
}

// BatchOutput is one element of an atomic batch: key, title, typed content.
type BatchOutput struct {
	Key     string
	Title   string
	Type    string
	Content []byte
}

// materializeItem materializes exactly one output inside the caller's
// transaction, minting its publication event at nextSeq+1 and advancing the
// sequence cursor. Replay verifies the stored canonical digest exactly.
func (m *TaskArtifactMaterializer) materializeItem(
	ctx context.Context, tx dbtx.Tx, stream agentports.TaskStreamFacts,
	requested map[string]bool, nextSeq *int64,
	outputKey, rawTitle, artifactType string, content []byte,
	now time.Time,
) (*artifactv1.Artifact, *agentv1.AgentEvent, error) {
	// Scope and request verification: project review artifacts exist only
	// for project-scoped tasks, and only for types the task actually
	// requested.
	if !requested[artifactType] {
		return nil, nil, artifactdomain.ErrInvalid
	}

	// Replay/conflict adjudication on the durable mapping. The replayed
	// raw request must reproduce the stored canonical digest exactly; any
	// other input — including input too malformed to digest — is the stable
	// conflict verdict and consumes nothing.
	if existing, found, findErr := m.artifacts.FindTaskOutput(ctx, tx, stream.TaskID, outputKey); findErr != nil {
		return nil, nil, findErr
	} else if found {
		if !validTaskOutputRecord(existing, stream, outputKey) {
			return nil, nil, artifactdomain.ErrCorrupt
		}
		digest, digestable := artifactapp.ReviewOutputRequestDigestFor(
			stream.ProjectID, stream.TaskID, outputKey, rawTitle, artifactType, content,
		)
		if !digestable || digest != existing.RequestDigest {
			return nil, nil, artifactdomain.ErrOutputConflict
		}
		return m.replay(ctx, tx, stream, existing)
	}

	// Canonical preparation: normalize, bound, digest, and mint the artifact
	// identity. Validation failures consume nothing.
	command, err := m.preparer.PrepareReviewOutput(
		stream.OwnerUserID, stream.ProjectID, stream.TaskID, outputKey, rawTitle, artifactType, content,
	)
	if err != nil {
		return nil, nil, err
	}
	publication := artifactdomain.PublicationRecord{
		EventID: m.ids.New(), EventSeq: *nextSeq + 1, OccurredAt: now,
	}
	if !artifactdomain.ValidStoredPublicationRecord(publication) {
		return nil, nil, artifactdomain.ErrCorrupt
	}
	command.Publication = publication

	// Atomic insert: artifact row + adjudication mapping. Zero rows means
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
		if found && !validTaskOutputRecord(existing, stream, outputKey) {
			return nil, nil, artifactdomain.ErrCorrupt
		}
		if found && existing.RequestDigest == command.RequestDigest {
			return m.replay(ctx, tx, stream, existing)
		}
		return nil, nil, artifactdomain.ErrOutputConflict
	}
	if rows != 1 {
		return nil, nil, artifactdomain.ErrCorrupt
	}

	// Core-minted publication: exactly one artifact_created event per
	// artifact, in the same transaction as the artifact row.
	event, err := agentapp.NewArtifactPublicationEvent(
		publication.EventID, stream.TaskID, publication.EventSeq, publication.OccurredAt,
		command.Artifact.ID, command.Artifact.Type,
	)
	if err != nil {
		return nil, nil, err
	}
	// The publication validator checks the event against the caller's view
	// of the stream: sync the local copy to this item's base so a batch's
	// second item validates against the first item's sequence.
	stream.LastEventSequence = *nextSeq
	if err := m.streams.AppendPublicationEvent(ctx, tx, stream, event); err != nil {
		return nil, nil, err
	}
	*nextSeq = publication.EventSeq
	// Durable index publication (ADR-0013): same transaction as the artifact
	// row and the timeline event. One immutable artifact maps to exactly one
	// upsert publication; the unique arbitration makes any duplicate a
	// corruption verdict rather than a business event. Replays return before
	// this point and never publish twice.
	if err := m.feedSink.AppendReviewArtifactUpsert(ctx, tx, indexfeeddomain.Publication{
		ID:           m.ids.New(),
		Operation:    indexfeeddomain.OperationReviewArtifactUpsert,
		OwnerUserID:  stream.OwnerUserID,
		ProjectID:    stream.ProjectID,
		SourceType:   indexfeeddomain.SourceType,
		SourceID:     command.Artifact.ID,
		ArtifactType: command.Artifact.Type,
		Digest:       command.Artifact.Digest,
		OccurredAt:   now,
	}); err != nil {
		return nil, nil, err
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
		fact.SourceTask != stream.TaskID || fact.OutputKey != existing.OutputKey ||
		fact.Type != existing.ArtifactType || !fact.CreatedAt.Equal(existing.CreatedAt) {
		return nil, nil, artifactdomain.ErrCorrupt
	}
	expectedEvent, err := agentapp.NewArtifactPublicationEvent(
		existing.Publication.EventID, stream.TaskID, existing.Publication.EventSeq,
		existing.Publication.OccurredAt, fact.ID, fact.Type,
	)
	if err != nil {
		return nil, nil, artifactdomain.ErrCorrupt
	}
	matches, err := m.streams.PublicationEventMatches(ctx, tx, expectedEvent)
	if err != nil {
		return nil, nil, err
	}
	if !matches {
		return nil, nil, artifactdomain.ErrCorrupt
	}
	return reviewArtifactProto(fact), publicationProto(stream, existing.Publication, fact), nil
}

func validTaskArtifactStream(stream agentports.TaskStreamFacts) bool {
	if !artifactdomain.ValidArtifactUUID(stream.TaskID) ||
		!artifactdomain.ValidArtifactUUID(stream.OwnerUserID) ||
		stream.ProviderID == "" || stream.ProviderID != strings.TrimSpace(stream.ProviderID) ||
		stream.LastEventSequence < 0 || stream.LastEventSequence == math.MaxInt64 {
		return false
	}
	if stream.ProjectID != "" && !artifactdomain.ValidArtifactUUID(stream.ProjectID) {
		return false
	}
	return stream.State == agentdomain.StateRunning || stream.State == agentdomain.StateWaiting
}

func validTaskOutputRecord(record artifactports.TaskOutputRecord, stream agentports.TaskStreamFacts, outputKey string) bool {
	if !artifactdomain.ValidArtifactDigest(record.RequestDigest) ||
		!artifactdomain.ValidArtifactUUID(record.OwnerUserID) ||
		!artifactdomain.ValidArtifactUUID(record.ProjectID) ||
		!artifactdomain.ValidArtifactUUID(record.TaskID) ||
		!artifactdomain.ValidReviewOutputKey(record.OutputKey) ||
		!artifactdomain.ValidArtifactUUID(record.ArtifactID) ||
		!artifactdomain.IsReviewType(record.ArtifactType) ||
		!artifactdomain.ValidStoredPublicationRecord(record.Publication) ||
		!artifactdomain.ValidStoredUTCTime(record.CreatedAt) {
		return false
	}
	return record.OwnerUserID == stream.OwnerUserID && record.ProjectID == stream.ProjectID &&
		record.TaskID == stream.TaskID && record.OutputKey == outputKey &&
		record.Publication.EventSeq <= stream.LastEventSequence
}

// requestedArtifactTypes decodes and revalidates the task's immutable input
// snapshot. Drift between the stored target scope and task row, an unknown or
// duplicate type, or an over-wide list is stored corruption rather than a
// provider InvalidArgument verdict.
func requestedArtifactTypes(input []byte, projectID string) (map[string]bool, error) {
	var parsed agentv1.AgentTaskInput
	if err := protojson.Unmarshal(input, &parsed); err != nil {
		return nil, artifactdomain.ErrCorrupt
	}
	scope := parsed.GetTargetScope()
	if scope == nil {
		return nil, artifactdomain.ErrCorrupt
	}
	switch target := scope.Scope.(type) {
	case *agentv1.TargetScope_ProjectId:
		if projectID == "" || target.ProjectId != projectID {
			return nil, artifactdomain.ErrCorrupt
		}
	case *agentv1.TargetScope_Global:
		if !target.Global || projectID != "" {
			return nil, artifactdomain.ErrCorrupt
		}
	default:
		return nil, artifactdomain.ErrCorrupt
	}
	types := make(map[string]bool, len(parsed.GetOutputArtifactTypes()))
	if len(parsed.GetOutputArtifactTypes()) > 2 {
		return nil, artifactdomain.ErrCorrupt
	}
	for _, requested := range parsed.GetOutputArtifactTypes() {
		if !artifactdomain.IsReviewType(requested) || types[requested] {
			return nil, artifactdomain.ErrCorrupt
		}
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
