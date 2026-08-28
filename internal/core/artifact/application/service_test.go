package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/yangtao121/workos/internal/core/artifact/domain"
	"github.com/yangtao121/workos/internal/core/artifact/ports"
	"github.com/yangtao121/workos/internal/platform/ids"
)

type fakeRepository struct {
	mu        sync.Mutex
	artifacts map[string]domain.Artifact
	files     map[string]map[string]domain.BundleFile
	requests  map[string]ports.CreateCommand
	failWith  error
}

func newFakeRepository() *fakeRepository {
	return &fakeRepository{
		artifacts: map[string]domain.Artifact{}, files: map[string]map[string]domain.BundleFile{},
		requests: map[string]ports.CreateCommand{},
	}
}

func ownerKey(owner, id string) string { return owner + "/" + id }

func (r *fakeRepository) Create(_ context.Context, command ports.CreateCommand) (domain.Artifact, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failWith != nil {
		return domain.Artifact{}, r.failWith
	}
	key := ownerKey(command.Artifact.OwnerUserID, command.IdempotencyKey)
	if stored, ok := r.requests[key]; ok {
		if stored.RequestDigest != command.RequestDigest {
			return domain.Artifact{}, domain.ErrIdempotencyConflict
		}
		return r.artifacts[ownerKey(stored.Artifact.OwnerUserID, stored.Artifact.ID)], nil
	}
	storageKey := ownerKey(command.Artifact.OwnerUserID, command.Artifact.ID)
	r.artifacts[storageKey] = command.Artifact
	filesByPath := map[string]domain.BundleFile{}
	for _, file := range command.Bundle.Files {
		filesByPath[file.Path] = file
	}
	r.files[storageKey] = filesByPath
	r.requests[key] = command
	return command.Artifact, nil
}

func (r *fakeRepository) Get(_ context.Context, ownerUserID, artifactID string) (domain.Artifact, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failWith != nil {
		return domain.Artifact{}, r.failWith
	}
	artifact, ok := r.artifacts[ownerKey(ownerUserID, artifactID)]
	if !ok || artifact.OwnerUserID != ownerUserID {
		return domain.Artifact{}, domain.ErrNotFound
	}
	return artifact, nil
}

func (r *fakeRepository) ListIDsPage(_ context.Context, ownerUserID, cursor string, limit int) ([]string, string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	all := make([]string, 0, len(r.artifacts))
	for key, artifact := range r.artifacts {
		if artifact.OwnerUserID == ownerUserID {
			all = append(all, strings.TrimPrefix(key, ownerUserID+"/"))
		}
	}
	sortStrings(all)
	result := make([]string, 0, limit)
	for _, id := range all {
		if id > cursor {
			result = append(result, id)
		}
		if len(result) > limit {
			break
		}
	}
	if len(result) <= limit {
		return result, "", nil
	}
	page := result[:limit]
	return page, page[len(page)-1], nil
}

func (r *fakeRepository) VisitSummaries(_ context.Context, ownerUserID string, artifacts []string, visit func(domain.Artifact) error) error {
	for _, id := range artifacts {
		if artifact, ok := r.artifacts[ownerKey(ownerUserID, id)]; ok {
			if err := visit(artifact); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *fakeRepository) ReadAsset(_ context.Context, ownerUserID, artifactID, path string) (domain.BundleFile, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failWith != nil {
		return domain.BundleFile{}, r.failWith
	}
	filesByPath, ok := r.files[ownerKey(ownerUserID, artifactID)]
	if !ok {
		return domain.BundleFile{}, domain.ErrNotFound
	}
	file, ok := filesByPath[path]
	if !ok {
		return domain.BundleFile{}, domain.ErrNotFound
	}
	return file, nil
}

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}

type staticGenerator struct{ counter int }

func (g *staticGenerator) New() string {
	g.counter++
	return fmt.Sprintf("0198d7ea-2110-7c42-b659-%010dab", g.counter)
}

func newTestService(repository ports.Repository) *Service {
	service, err := New(repository, &staticGenerator{})
	if err != nil {
		panic(err)
	}
	return service
}

const (
	testOwner = "0198d7ea-2110-7c42-b659-c5e4d73bc337"
	otherUUID = "0198d7ea-2110-7c42-b659-c5e4d73bc338"
)

func bundleInput() []domain.BundleFileInput {
	return []domain.BundleFileInput{
		{Path: "index.html", Content: []byte("<!doctype html>")},
		{Path: "app.js", Content: []byte("console.log(1)")},
	}
}

func TestCreateWebBundlePersistsImmutableMetadata(t *testing.T) {
	t.Parallel()
	repository := newFakeRepository()
	service := newTestService(repository)
	artifact, err := service.CreateWebBundle(context.Background(), testOwner, "key-1", "Notes", "index.html", bundleInput())
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}
	if artifact.Type != domain.TypeWebBundle || artifact.MediaType != domain.MediaTypeBundle {
		t.Fatalf("unexpected type constants: %q %q", artifact.Type, artifact.MediaType)
	}
	if artifact.ID == "" || artifact.ContentRef == "" || artifact.ContentRef == artifact.ID {
		t.Fatal("server-owned identity or opaque content ref missing")
	}
	if artifact.FileCount != 2 || artifact.TotalSizeBytes != int64(len("<!doctype html>")+len("console.log(1)")) {
		t.Fatalf("wrong file accounting: %d files %d bytes", artifact.FileCount, artifact.TotalSizeBytes)
	}
	if !domain.ValidArtifactDigest(artifact.Digest) || !strings.HasPrefix(artifact.ContentRef, "wbbnd:") {
		t.Fatalf("malformed digest or content ref: %q %q", artifact.Digest, artifact.ContentRef)
	}
	if artifact.CreatedAt.IsZero() {
		t.Fatal("created time missing")
	}
}

func TestCreateWebBundleIdempotency(t *testing.T) {
	t.Parallel()
	repository := newFakeRepository()
	service := newTestService(repository)
	ctx := context.Background()
	first, err := service.CreateWebBundle(ctx, testOwner, "key-1", "Notes", "index.html", bundleInput())
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := service.CreateWebBundle(ctx, testOwner, "key-1", "Notes", "index.html", bundleInput())
	if err != nil {
		t.Fatalf("same-key replay failed: %v", err)
	}
	if replayed.ID != first.ID || replayed.Digest != first.Digest {
		t.Fatal("replay did not return the first artifact")
	}
	// Order-independent input with the same logical content still replays.
	reordered, err := service.CreateWebBundle(ctx, testOwner, "key-1", "Notes", "index.html", []domain.BundleFileInput{
		{Path: "app.js", Content: []byte("console.log(1)")},
		{Path: "index.html", Content: []byte("<!doctype html>")},
	})
	if err != nil || reordered.ID != first.ID {
		t.Fatalf("order-independent replay failed: %v %s", err, reordered.ID)
	}
	twoEntrypoints := append(bundleInput(), domain.BundleFileInput{Path: "alt.html", Content: []byte("<p>alt</p>")})
	for name, conflict := range map[string]struct {
		title      string
		entrypoint string
		files      []domain.BundleFileInput
	}{
		"title":      {"Other", "index.html", bundleInput()},
		"entrypoint": {"Notes", "alt.html", twoEntrypoints},
		"content":    {"Notes", "index.html", []domain.BundleFileInput{{Path: "index.html", Content: []byte("x")}, {Path: "app.js", Content: []byte("y")}}},
	} {
		if _, err := service.CreateWebBundle(ctx, testOwner, "key-1", conflict.title, conflict.entrypoint, conflict.files); !errors.Is(err, domain.ErrIdempotencyConflict) {
			t.Errorf("%s conflict did not abort: %v", name, err)
		}
	}
}

func TestCreateWebBundleValidationFailuresDoNotConsumeKey(t *testing.T) {
	t.Parallel()
	repository := newFakeRepository()
	service := newTestService(repository)
	ctx := context.Background()
	if _, err := service.CreateWebBundle(ctx, testOwner, "key-1", "Notes", "missing.html", bundleInput()); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("invalid entrypoint verdict: %v", err)
	}
	if _, ok := repository.requests[ownerKey(testOwner, "key-1")]; ok {
		t.Fatal("failed validation consumed the idempotency key")
	}
	repository.failWith = errors.New("connection refused")
	if _, err := service.CreateWebBundle(ctx, testOwner, "key-1", "Notes", "index.html", bundleInput()); err == nil {
		t.Fatal("repository failure swallowed")
	}
	repository.failWith = nil
	if _, err := service.CreateWebBundle(ctx, testOwner, "key-1", "Notes", "index.html", bundleInput()); err != nil {
		t.Fatalf("key was consumed by failures: %v", err)
	}
}

func TestGetAndListAreOwnerScoped(t *testing.T) {
	t.Parallel()
	repository := newFakeRepository()
	service := newTestService(repository)
	ctx := context.Background()
	created, err := service.CreateWebBundle(ctx, testOwner, "key-1", "Notes", "index.html", bundleInput())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Get(ctx, otherUUID, created.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("foreign read did not fail closed: %v", err)
	}
	got, err := service.Get(ctx, testOwner, created.ID)
	if err != nil || got.ID != created.ID {
		t.Fatalf("owner read failed: %v", err)
	}
	page, err := service.List(ctx, otherUUID, "", "", 0)
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("foreign list leaked rows: %v %d", err, len(page.Items))
	}
	page, err = service.List(ctx, testOwner, "", "", 0)
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != created.ID {
		t.Fatalf("owner list missing artifact: %v %d", err, len(page.Items))
	}
	if _, err := service.List(ctx, testOwner, "0198d7ea-2110-7c42-b659-c5e4d73bc337", "", 0); !errors.Is(err, domain.ErrUnsupported) {
		t.Fatalf("project-scoped listing is not implemented: %v", err)
	}
	for _, pageSize := range []int{-1, 0, 101} {
		if _, err := service.List(ctx, testOwner, "", "", pageSize); pageSize < 0 && !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("negative page size accepted: %v", err)
		}
	}
}

func TestVerifyWebBundleAndReadVerifiedAsset(t *testing.T) {
	t.Parallel()
	repository := newFakeRepository()
	service := newTestService(repository)
	ctx := context.Background()
	created, err := service.CreateWebBundle(ctx, testOwner, "key-1", "Notes", "index.html", bundleInput())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.VerifyWebBundle(ctx, otherUUID, created.ID, created.Digest); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("foreign verify leaked: %v", err)
	}
	if _, err := service.VerifyWebBundle(ctx, testOwner, created.ID, "sha256:"+strings.Repeat("b", 64)); !errors.Is(err, domain.ErrDigestMismatch) {
		t.Fatalf("digest mismatch verdict: %v", err)
	}
	summary, err := service.VerifyWebBundle(ctx, testOwner, created.ID, created.Digest)
	if err != nil || summary.Entrypoint != "index.html" {
		t.Fatalf("verify failed: %v %+v", err, summary)
	}

	entry, err := service.ReadVerifiedWebBundleAsset(ctx, testOwner, created.ID, created.Digest, "")
	if err != nil || entry.Path != "index.html" || string(entry.Content) != "<!doctype html>" {
		t.Fatalf("entrypoint read failed: %v %+v", err, entry)
	}
	if entry.FileDigest == "" || entry.MediaType != "text/html; charset=utf-8" {
		t.Fatalf("asset metadata missing: %+v", entry)
	}
	script, err := service.ReadVerifiedWebBundleAsset(ctx, testOwner, created.ID, created.Digest, "app.js")
	if err != nil || string(script.Content) != "console.log(1)" {
		t.Fatalf("asset read failed: %v", err)
	}
	for name, query := range map[string]struct {
		owner  string
		digest string
		path   string
	}{
		"foreign owner":   {otherUUID, created.Digest, "app.js"},
		"digest drift":    {testOwner, "sha256:" + strings.Repeat("b", 64), "app.js"},
		"missing file":    {testOwner, created.Digest, "nope.js"},
		"traversal shape": {testOwner, created.Digest, "../index.html"},
		"unknown type":    {testOwner, created.Digest, "a.exe"},
	} {
		_, err := service.ReadVerifiedWebBundleAsset(ctx, query.owner, created.ID, query.digest, query.path)
		if err == nil {
			t.Errorf("%s: read unexpectedly succeeded", name)
		}
	}
}

func TestRepositoryFailureIsSanitized(t *testing.T) {
	t.Parallel()
	repository := newFakeRepository()
	service := newTestService(repository)
	repository.failWith = errors.New("sql: connection reset")
	if _, err := service.CreateWebBundle(context.Background(), testOwner, "k", "T", "index.html", bundleInput()); err == nil || errors.Is(err, domain.ErrInvalid) || errors.Is(err, domain.ErrNotFound) {
		t.Fatal("infrastructure error surfaced as a domain verdict")
	}
}

func TestNewRejectsMissingDependencies(t *testing.T) {
	t.Parallel()
	if _, err := New(nil, &staticGenerator{}); err == nil {
		t.Fatal("missing repository accepted")
	}
	if _, err := New(newFakeRepository(), nil); err == nil {
		t.Fatal("missing generator accepted")
	}
}

var _ ports.Repository = (*fakeRepository)(nil)
var _ ids.Generator = (*staticGenerator)(nil)
