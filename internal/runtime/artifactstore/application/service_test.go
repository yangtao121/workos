package application

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/yangtao121/workos/internal/runtime/artifactstore/adapters/files"
	"github.com/yangtao121/workos/internal/runtime/artifactstore/domain"
)

// memoryMeta is an in-flight MetadataStore fake honoring the same unique
// arbiters as PostgreSQL for service-level tests.
type memoryMeta struct {
	mu        sync.Mutex
	byID      map[string]domain.Artifact
	byDigest  map[string]domain.Artifact
	byTask    map[string]domain.Artifact
	byKey     map[string]domain.Artifact
	usageFail error
}

func newMemoryMeta() *memoryMeta {
	return &memoryMeta{
		byID: map[string]domain.Artifact{}, byDigest: map[string]domain.Artifact{},
		byTask: map[string]domain.Artifact{}, byKey: map[string]domain.Artifact{},
	}
}

func (m *memoryMeta) InsertReady(_ context.Context, artifact domain.Artifact) (domain.Artifact, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := artifact.OwnerUserID + "|" + artifact.Digest
	if existing, ok := m.byDigest[key]; ok {
		return existing, false, nil
	}
	m.byID[artifact.ID] = artifact
	m.byDigest[key] = artifact
	if artifact.TaskID != "" {
		m.byTask[artifact.TaskID] = artifact
	}
	if artifact.Origin == domain.OriginOperatorImport {
		m.byKey[artifact.OwnerUserID+"|"+artifact.IdempotencyKey] = artifact
	}
	return artifact, true, nil
}

func (m *memoryMeta) GetByID(_ context.Context, id string) (domain.Artifact, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if a, ok := m.byID[id]; ok {
		return a, nil
	}
	return domain.Artifact{}, domain.ErrNotFound
}

func (m *memoryMeta) GetByOwnerDigest(_ context.Context, owner, digest string) (domain.Artifact, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if a, ok := m.byDigest[owner+"|"+digest]; ok {
		return a, nil
	}
	return domain.Artifact{}, domain.ErrNotFound
}

func (m *memoryMeta) GetByTask(_ context.Context, taskID string) (domain.Artifact, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if a, ok := m.byTask[taskID]; ok {
		return a, nil
	}
	return domain.Artifact{}, domain.ErrNotFound
}

func (m *memoryMeta) GetByImportKey(_ context.Context, owner, key string) (domain.Artifact, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if a, ok := m.byKey[owner+"|"+key]; ok {
		return a, nil
	}
	return domain.Artifact{}, domain.ErrNotFound
}

func (m *memoryMeta) MarkState(_ context.Context, id, state string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.byID[id]
	if !ok {
		return domain.ErrNotFound
	}
	a.State = state
	m.byID[id] = a
	m.byDigest[a.OwnerUserID+"|"+a.Digest] = a
	if a.TaskID != "" {
		m.byTask[a.TaskID] = a
	}
	if a.Origin == domain.OriginOperatorImport {
		m.byKey[a.OwnerUserID+"|"+a.IdempotencyKey] = a
	}
	return nil
}

func (m *memoryMeta) OwnerUsageBytes(_ context.Context, owner string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.usageFail != nil {
		return 0, m.usageFail
	}
	var total int64
	for _, a := range m.byDigest {
		if a.OwnerUserID == owner && (a.State == domain.StatePreparing || a.State == domain.StateReady) {
			total += a.SizeBytes
		}
	}
	return total, nil
}

func (m *memoryMeta) ListByState(_ context.Context, state string) ([]domain.Artifact, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.Artifact
	for _, a := range m.byID {
		if a.State == state {
			out = append(out, a)
		}
	}
	return out, nil
}

const (
	ownerA  = "0198c0de-0000-7000-8000-0000000000aa"
	taskOne = "0198c0de-0000-7000-8000-0000000000bb"
)

func newTestService(t *testing.T) (*Service, *memoryMeta) {
	t.Helper()
	store, err := files.New(filepath.Join(t.TempDir(), "repo"))
	if err != nil {
		t.Fatal(err)
	}
	meta := newMemoryMeta()
	return New(meta, store), meta
}

func bundleBytes(t *testing.T, name, content string) []byte {
	t.Helper()
	root := t.TempDir()
	full := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if _, err := domain.EncodeDirectory(root, &buf); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestImportAndReplay(t *testing.T) {
	service, _ := newTestService(t)
	raw := bundleBytes(t, "dist/server", "A")

	first, err := service.Import(context.Background(), ImportRequest{
		OwnerUserID: ownerA, AppID: "demo-app", IdempotencyKey: "import-1", Reader: bytes.NewReader(raw),
	})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if !first.Created || first.Artifact.Origin != domain.OriginOperatorImport || first.Artifact.State != domain.StateReady {
		t.Fatalf("unexpected first import: %+v", first.Artifact)
	}

	replay, err := service.Import(context.Background(), ImportRequest{
		OwnerUserID: ownerA, AppID: "demo-app", IdempotencyKey: "import-1", Reader: bytes.NewReader(raw),
	})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if replay.Created || replay.Artifact.ID != first.Artifact.ID {
		t.Fatalf("same key + same content must replay the same artifact: %+v", replay.Artifact)
	}
}

func TestImportKeyConflictOnDifferentContent(t *testing.T) {
	service, _ := newTestService(t)
	if _, err := service.Import(context.Background(), ImportRequest{OwnerUserID: ownerA, AppID: "demo-app", IdempotencyKey: "k", Reader: bytes.NewReader(bundleBytes(t, "dist/s", "A"))}); err != nil {
		t.Fatal(err)
	}
	_, err := service.Import(context.Background(), ImportRequest{OwnerUserID: ownerA, AppID: "demo-app", IdempotencyKey: "k", Reader: bytes.NewReader(bundleBytes(t, "dist/s", "B"))})
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("same key different content must conflict, got %v", err)
	}
}

func TestImportExpectedDigestMismatch(t *testing.T) {
	service, _ := newTestService(t)
	claimed := "sha256:" + strings.Repeat("c", 64)
	_, err := service.Import(context.Background(), ImportRequest{
		OwnerUserID: ownerA, AppID: "demo-app", IdempotencyKey: "k2", ExpectedDigest: claimed,
		Reader: bytes.NewReader(bundleBytes(t, "dist/s", "A")),
	})
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("claimed digest mismatch must conflict, got %v", err)
	}
}

func TestImportRejectsMalformedStream(t *testing.T) {
	service, _ := newTestService(t)
	_, err := service.Import(context.Background(), ImportRequest{
		OwnerUserID: ownerA, AppID: "demo-app", IdempotencyKey: "k3",
		Reader: bytes.NewReader([]byte("definitely not a tar archive, too short")),
	})
	if err == nil {
		t.Fatal("garbage stream must be rejected")
	}
	facts, factsErr := service.Facts(context.Background(), FactsQuery{ArtifactID: "0198c0de-0000-7000-8000-0000000000ff"})
	if factsErr == nil || facts.State != "" {
		t.Fatalf("no artifact should exist after a rejected import: %+v %v", facts, factsErr)
	}
}

func TestCommitBuildAndFacts(t *testing.T) {
	service, meta := newTestService(t)
	outDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(outDir, "dist"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outDir, "dist", "server"), []byte("B"), 0o755); err != nil {
		t.Fatal(err)
	}
	result, err := service.CommitBuild(context.Background(), BuildCommit{
		OwnerUserID: ownerA, TaskID: taskOne, JobID: "0198c0de-0000-7000-8000-0000000000cc",
		SourceDigest: "sha256:" + strings.Repeat("1", 64), ManifestDigest: "sha256:" + strings.Repeat("2", 64),
		BaseImage: "golang@sha256:" + strings.Repeat("3", 64), BuildCommand: []string{"go", "build"},
		TestCommand: []string{"go", "test"}, OutputDirectory: "dist", IdempotencyKey: "build-1", OutputDir: outDir,
	})
	if err != nil {
		t.Fatalf("commit build: %v", err)
	}
	if !result.Created || result.Artifact.Origin != domain.OriginBuildJob {
		t.Fatalf("unexpected result: %+v", result.Artifact)
	}
	facts, err := service.Facts(context.Background(), FactsQuery{TaskID: taskOne})
	if err != nil || facts.ID != result.Artifact.ID || facts.ManifestDigest != "sha256:"+strings.Repeat("2", 64) {
		t.Fatalf("facts mismatch: %+v %v", facts, err)
	}
	// Replay: same input converges to the same artifact.
	again, err := service.CommitBuild(context.Background(), BuildCommit{
		OwnerUserID: ownerA, TaskID: taskOne, JobID: "0198c0de-0000-7000-8000-0000000000cc",
		SourceDigest: "sha256:" + strings.Repeat("1", 64), ManifestDigest: "sha256:" + strings.Repeat("2", 64),
		BaseImage: "golang@sha256:" + strings.Repeat("3", 64), BuildCommand: []string{"go", "build"},
		TestCommand: []string{"go", "test"}, OutputDirectory: "dist", IdempotencyKey: "build-1", OutputDir: outDir,
	})
	if err != nil || again.Created || again.Artifact.ID != result.Artifact.ID {
		t.Fatalf("replay must converge: %+v %v", again.Artifact, err)
	}
	if len(meta.byID) != 1 {
		t.Fatalf("replay created a second row: %d", len(meta.byID))
	}
}

func TestOpenForLaunchVerifiesDigest(t *testing.T) {
	service, _ := newTestService(t)
	raw := bundleBytes(t, "dist/server", "A")
	result, err := service.Import(context.Background(), ImportRequest{
		OwnerUserID: ownerA, AppID: "demo-app", IdempotencyKey: "launch", Reader: bytes.NewReader(raw),
	})
	if err != nil {
		t.Fatal(err)
	}
	path, err := service.OpenForLaunch(context.Background(), ownerA, result.Artifact.Digest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	// Corrupt the stored bytes; the next launch open must fail closed.
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := service.OpenForLaunch(context.Background(), ownerA, result.Artifact.Digest); err == nil {
		t.Fatal("launch open must re-hash and fail on corrupted bytes")
	}
}

func TestQuotaFailClosed(t *testing.T) {
	service, meta := newTestService(t)
	// Simulate an owner already at quota.
	meta.mu.Lock()
	meta.byDigest[ownerA+"|sha256:"+strings.Repeat("9", 64)] = domain.Artifact{
		ID: "0198c0de-0000-7000-8000-0000000000dd", OwnerUserID: ownerA,
		Digest: "sha256:" + strings.Repeat("9", 64), State: domain.StateReady,
		SizeBytes: domain.OwnerQuotaBytes, Origin: domain.OriginOperatorImport,
	}
	meta.byID["0198c0de-0000-7000-8000-0000000000dd"] = domain.Artifact{
		ID: "0198c0de-0000-7000-8000-0000000000dd", OwnerUserID: ownerA,
		Digest: "sha256:" + strings.Repeat("9", 64), State: domain.StateReady,
		SizeBytes: domain.OwnerQuotaBytes, Origin: domain.OriginOperatorImport,
	}
	meta.mu.Unlock()
	_, err := service.Import(context.Background(), ImportRequest{
		OwnerUserID: ownerA, AppID: "demo-app", IdempotencyKey: "quota", Reader: bytes.NewReader(bundleBytes(t, "s", "x")),
	})
	if !errors.Is(err, domain.ErrQuotaExceeded) {
		t.Fatalf("quota must fail closed, got %v", err)
	}
}

func TestReconcileConvergesCrashWindows(t *testing.T) {
	service, meta := newTestService(t)
	ctx := context.Background()

	// Window 1: bytes were promoted durably but the metadata row never left
	// preparing. Import the same bytes (idempotent), then flip the row back
	// to preparing to model the crash, and reconcile: it must become ready.
	raw := bundleBytes(t, "dist/a", "x")
	digest := digestOfBytes(t, raw)
	if _, err := service.Import(ctx, ImportRequest{OwnerUserID: ownerA, AppID: "demo-app", IdempotencyKey: "win1", Reader: bytes.NewReader(raw)}); err != nil {
		t.Fatalf("import for window 1: %v", err)
	}
	imported, err := meta.GetByImportKey(ctx, ownerA, "win1")
	if err != nil {
		t.Fatal(err)
	}
	meta.mu.Lock()
	imported.State = domain.StatePreparing
	meta.byID[imported.ID] = imported
	meta.byDigest[ownerA+"|"+digest] = imported
	meta.mu.Unlock()

	// Window 2: a ready row whose bytes vanished.
	ghostDigest := "sha256:" + strings.Repeat("7", 64)
	ghost := domain.Artifact{
		ID: "0198c0de-0000-7000-8000-0000000000e2", OwnerUserID: ownerA, Digest: ghostDigest,
		State: domain.StateReady, Origin: domain.OriginBuildJob, SizeBytes: 10, FileCount: 1,
	}
	meta.mu.Lock()
	meta.byID[ghost.ID] = ghost
	meta.byDigest[ownerA+"|"+ghostDigest] = ghost
	meta.mu.Unlock()

	if err := service.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	converged, err := meta.GetByID(ctx, imported.ID)
	if err != nil || converged.State != domain.StateReady {
		t.Fatalf("preparing with verified bytes must become ready: %+v %v", converged, err)
	}
	after, err := meta.GetByID(ctx, ghost.ID)
	if err != nil || after.State != domain.StateUnavailable {
		t.Fatalf("ready without bytes must degrade to unavailable: %+v %v", after, err)
	}
}

func digestOfBytes(t *testing.T, raw []byte) string {
	t.Helper()
	stats, err := domain.Verify(bytes.NewReader(raw), "")
	if err != nil {
		t.Fatal(err)
	}
	return stats.Digest
}
