package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/jackc/pgx/v5"
	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
	artifactv1 "github.com/yangtao121/workos/gen/go/workos/artifact/v1"
	agentdomain "github.com/yangtao121/workos/internal/core/agent/domain"
	agentports "github.com/yangtao121/workos/internal/core/agent/ports"
	artifactpostgres "github.com/yangtao121/workos/internal/core/artifact/adapters/postgres"
	artifactapp "github.com/yangtao121/workos/internal/core/artifact/application"
	artifactdomain "github.com/yangtao121/workos/internal/core/artifact/domain"
	artifactports "github.com/yangtao121/workos/internal/core/artifact/ports"

	"github.com/yangtao121/workos/internal/platform/dbtx"
	"github.com/yangtao121/workos/internal/platform/ids"
)

const (
	leaseID  = "0198d7ea-0000-7000-8000-000000000001"
	taskID   = "0198d7ea-0000-7000-8000-000000000002"
	ownerID  = "0198d7ea-0000-7000-8000-000000000003"
	project1 = "0198d7ea-0000-7000-8000-000000000004"
	project2 = "0198d7ea-0000-7000-8000-000000000005"
)

// fakeStreams stands in for the Agent module's transaction-scoped port.
type fakeStreams struct {
	leaseLost    bool
	terminal     bool
	events       []agentdomain.Event
	lastSequence int64
	projectID    string
	globalScope  bool
	input        string
}

func (f *fakeStreams) LockTaskArtifactStream(_ context.Context, _ dbtx.Tx, _, _ string, _ time.Time) (agentports.TaskStreamFacts, error) {
	if f.leaseLost {
		return agentports.TaskStreamFacts{}, agentdomain.ErrLeaseLost
	}
	if f.terminal {
		return agentports.TaskStreamFacts{}, agentdomain.ErrTerminal
	}
	project := f.projectID
	if project == "" && !f.globalScope {
		project = project1
	}
	input := f.input
	if input == "" {
		input = `{"outputArtifactTypes":["document.markdown.v1","code.unified-diff.v1"],"goal":"synthetic"}`
	}
	return agentports.TaskStreamFacts{
		TaskID: taskID, OwnerUserID: ownerID, ProjectID: project, ProviderID: "fake",
		State:             agentdomain.StateRunning,
		Input:             []byte(input),
		LastEventSequence: f.lastSequence,
	}, nil
}

func (f *fakeStreams) AppendPublicationEvent(_ context.Context, _ dbtx.Tx, stream agentports.TaskStreamFacts, event agentdomain.Event) error {
	if event.Sequence != stream.LastEventSequence+1 {
		return errors.New("sequence mismatch")
	}
	// Mirror the real store: the stream metadata is injected into the stored
	// payload before it lands in the event table.
	var document map[string]any
	if err := json.Unmarshal(event.Payload, &document); err != nil {
		return err
	}
	document["id"] = event.ID
	document["taskId"] = event.TaskID
	document["sequence"] = event.Sequence
	document["occurredAt"] = event.OccurredAt.Format(time.RFC3339Nano)
	payload, err := json.Marshal(document)
	if err != nil {
		return err
	}
	event.Payload = payload
	f.events = append(f.events, event)
	f.lastSequence = event.Sequence
	return nil
}

// fakeReviewOutputs stands in for the Artifact module's transaction-scoped
// adjudication port, replaying the real ON CONFLICT semantics in memory.
type fakeReviewOutputs struct {
	outputs   map[string]artifactports.TaskOutputRecord
	facts     map[string]artifactdomain.ReviewArtifact
	contents  map[string][]byte
	insertErr error
}

func newFakeReviewOutputs() *fakeReviewOutputs {
	return &fakeReviewOutputs{
		outputs:  map[string]artifactports.TaskOutputRecord{},
		facts:    map[string]artifactdomain.ReviewArtifact{},
		contents: map[string][]byte{},
	}
}

func outputIdentity(taskID, outputKey string) string { return taskID + "/" + outputKey }

func (f *fakeReviewOutputs) FindTaskOutput(_ context.Context, _ dbtx.Tx, taskID, outputKey string) (artifactports.TaskOutputRecord, bool, error) {
	record, found := f.outputs[outputIdentity(taskID, outputKey)]
	return record, found, nil
}

func (f *fakeReviewOutputs) InsertTaskOutput(_ context.Context, _ dbtx.Tx, command artifactports.ReviewOutputCommand) (int64, error) {
	if f.insertErr != nil {
		return 0, f.insertErr
	}
	// Physical arbiter: (task, output key) primary key and (task, type) unique
	// index, mirroring the migration's constraints.
	for _, record := range f.outputs {
		if record.ArtifactType == command.Artifact.Type {
			return 0, nil
		}
	}
	if _, consumed := f.outputs[outputIdentity(command.Artifact.SourceTask, command.Artifact.OutputKey)]; consumed {
		return 0, nil
	}
	if !artifactdomain.ValidStoredReviewFact(command.Artifact) {
		return 0, errors.New("insert refused an invalid stored fact")
	}
	f.outputs[outputIdentity(command.Artifact.SourceTask, command.Artifact.OutputKey)] = artifactports.TaskOutputRecord{
		RequestDigest: command.RequestDigest, ArtifactID: command.Artifact.ID,
		ArtifactType: command.Artifact.Type, Publication: command.Publication,
	}
	f.facts[command.Artifact.ID] = command.Artifact
	f.contents[command.Artifact.ID] = command.Content
	return 1, nil
}

func (f *fakeReviewOutputs) ReviewArtifactByID(_ context.Context, _ dbtx.Tx, artifactID string) (artifactdomain.ReviewArtifact, error) {
	fact, found := f.facts[artifactID]
	if !found {
		return artifactdomain.ReviewArtifact{}, artifactdomain.ErrNotFound
	}
	return fact, nil
}

// fakeTxSource hands out one throwaway transaction handle per test; the fake
// stores ignore it, mirroring the in-memory semantics. Only Commit and
// Rollback are ever invoked on it by the coordinator.
type fakeTxSource struct{}

func (fakeTxSource) Begin(context.Context) (dbtx.Tx, error) { return noopTx{}, nil }

type noopTx struct{ pgx.Tx }

func (noopTx) Commit(context.Context) error   { return nil }
func (noopTx) Rollback(context.Context) error { return nil }

func newMaterializer(t *testing.T) (*TaskArtifactMaterializer, *fakeStreams, *fakeReviewOutputs) {
	t.Helper()
	streams := &fakeStreams{lastSequence: 2}
	outputs := newFakeReviewOutputs()
	preparer, err := artifactapp.New(artifactpostgres.New(nil), ids.UUIDv7{})
	if err != nil {
		t.Fatal(err)
	}
	materializer, err := NewTaskArtifactMaterializer(fakeTxSource{}, streams, outputs, preparer, ids.UUIDv7{})
	if err != nil {
		t.Fatal(err)
	}
	return materializer, streams, outputs
}

func materializeMarkdown(m *TaskArtifactMaterializer, content []byte) (*artifactv1.Artifact, *agentv1.AgentEvent, error) {
	return m.MaterializeTaskArtifact(context.Background(), leaseID, "worker-1", "document", "Title", "document.markdown.v1", content)
}

func TestMaterializerPersistsArtifactAndMintsExactlyOneEvent(t *testing.T) {
	m, streams, outputs := newMaterializer(t)
	artifact, event, err := materializeMarkdown(m, []byte("# Hello\n"))
	if err != nil {
		t.Fatal(err)
	}
	if artifact.GetId() == "" || artifact.GetProjectId() != project1 || artifact.GetSourceTaskId() != taskID ||
		artifact.GetType() != "document.markdown.v1" || artifact.GetTotalSizeBytes() != 8 {
		t.Fatalf("unexpected artifact projection: %#v", artifact)
	}
	if event.GetArtifactCreated().GetArtifactId() != artifact.GetId() || event.GetSequence() != 3 || event.GetTaskId() != taskID {
		t.Fatalf("unexpected Core-minted event: %#v", event)
	}
	if len(streams.events) != 1 || streams.lastSequence != 3 {
		t.Fatalf("exactly one publication expected: %d", len(streams.events))
	}
	if len(outputs.facts) != 1 {
		t.Fatalf("exactly one artifact expected: %d", len(outputs.facts))
	}
}

func TestMaterializerReplaysAfterResponseLoss(t *testing.T) {
	m, streams, _ := newMaterializer(t)
	first, firstEvent, err := materializeMarkdown(m, []byte("# Hello\n"))
	if err != nil {
		t.Fatal(err)
	}
	streams.lastSequence = 99 // a replay must not mint another sequence step
	second, secondEvent, err := materializeMarkdown(m, []byte("# Hello\n"))
	if err != nil {
		t.Fatal(err)
	}
	if first.GetId() != second.GetId() || firstEvent.GetSequence() != secondEvent.GetSequence() {
		t.Fatalf("replay diverged: %s/%d vs %s/%d", first.GetId(), firstEvent.GetSequence(), second.GetId(), secondEvent.GetSequence())
	}
	if secondEvent.GetSequence() != 3 {
		t.Fatalf("replay must return the first published event, got sequence %d", secondEvent.GetSequence())
	}
}

func TestMaterializerConflictFailsClosed(t *testing.T) {
	m, streams, outputs := newMaterializer(t)
	if _, _, err := materializeMarkdown(m, []byte("# Hello\n")); err != nil {
		t.Fatal(err)
	}
	publications := len(streams.events)
	if _, _, err := materializeMarkdown(m, []byte("# Different\n")); !errors.Is(err, artifactdomain.ErrOutputConflict) {
		t.Fatalf("different content on the same key must conflict, got %v", err)
	}
	if len(streams.events) != publications || len(outputs.facts) != 1 {
		t.Fatalf("conflict must not write facts or events: %d/%d", len(streams.events), len(outputs.facts))
	}
	if _, _, err := m.MaterializeTaskArtifact(context.Background(), leaseID, "worker-1", "other",
		"Title", "document.markdown.v1", []byte("# Also different\n")); !errors.Is(err, artifactdomain.ErrOutputConflict) {
		t.Fatalf("second artifact of one type must conflict, got %v", err)
	}
}

func TestMaterializerRejectsUnrequestedTypesAndGlobalScope(t *testing.T) {
	m, streams, _ := newMaterializer(t)
	streams.input = `{"outputArtifactTypes":["document.markdown.v1"],"goal":"synthetic"}`
	if _, _, err := m.MaterializeTaskArtifact(context.Background(), leaseID, "worker-1", "sneaky",
		"Title", "code.unified-diff.v1", []byte("diff\n")); err == nil {
		t.Fatal("unrequested type accepted")
	}
	// The global-scope case is enforced through the stream's project fact:
	// the coordinator must refuse an empty project even for a requested type.
	global, streams, outputs := newMaterializer(t)
	streams.globalScope = true
	if _, _, err := materializeMarkdown(global, []byte("# Hello\n")); !errors.Is(err, artifactdomain.ErrInvalid) {
		t.Fatalf("global task must not materialize project review outputs, got %v", err)
	}
	if len(outputs.facts) != 0 {
		t.Fatal("global-scope refusal must not persist facts")
	}
}

func TestMaterializerRejectsLostLeaseAndTerminalTasks(t *testing.T) {
	m, streams, _ := newMaterializer(t)
	streams.leaseLost = true
	if _, _, err := materializeMarkdown(m, []byte("# Hello\n")); !errors.Is(err, agentdomain.ErrLeaseLost) {
		t.Fatalf("lost lease must fail closed, got %v", err)
	}
	streams.leaseLost = false
	streams.terminal = true
	if _, _, err := materializeMarkdown(m, []byte("# Hello\n")); !errors.Is(err, agentdomain.ErrTerminal) {
		t.Fatalf("terminal task must fail closed, got %v", err)
	}
}

func TestMaterializerValidationFailuresConsumeNothing(t *testing.T) {
	m, _, outputs := newMaterializer(t)
	if _, _, err := materializeMarkdown(m, []byte("bad\x00content")); err == nil {
		t.Fatal("NUL content accepted")
	}
	if _, _, err := materializeMarkdown(m, nil); err == nil {
		t.Fatal("empty content accepted")
	}
	if _, _, err := m.MaterializeTaskArtifact(context.Background(), leaseID, "worker-1", "BAD KEY",
		"Title", "document.markdown.v1", []byte("x")); err == nil {
		t.Fatal("invalid output key accepted")
	}
	if len(outputs.facts) != 0 {
		t.Fatalf("failed validation must not persist facts: %d", len(outputs.facts))
	}
}

func TestMaterializerEventPayloadIsCanonical(t *testing.T) {
	m, streams, _ := newMaterializer(t)
	artifact, event, err := materializeMarkdown(m, []byte("# Hello\n"))
	if err != nil {
		t.Fatal(err)
	}
	// The stored payload must decode into the same Core-minted reference the
	// RPC returned — the WatchTaskEvents and append surfaces share it.
	var decoded agentv1.AgentEvent
	if err := protojson.Unmarshal(streams.events[0].Payload, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.GetArtifactCreated().GetArtifactId() != artifact.GetId() ||
		decoded.GetId() != event.GetId() || decoded.GetSequence() != event.GetSequence() {
		t.Fatalf("stored payload diverges from the RPC event: id=%q/%q seq=%d/%d artifact=%q/%q",
			decoded.GetId(), event.GetId(), decoded.GetSequence(), event.GetSequence(),
			decoded.GetArtifactCreated().GetArtifactId(), event.GetArtifactCreated().GetArtifactId())
	}
	if len(streams.events[0].Payload) > 512 {
		t.Fatalf("publication payload carries more than the reference: %s", streams.events[0].Payload)
	}
}
