package application

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/yangtao121/workos/internal/core/appregistry/domain"
	"github.com/yangtao121/workos/internal/core/appregistry/ports"
)

const testManifestYAML = "apiVersion: workos.app/v1\nid: %s\nname: %s\nversion: %s\nscope: user\n" +
	"runtime:\n  type: container\nsurfaces:\n  - id: main\n    renderer: web-bundle\n" +
	"permissions: [artifact.read]\nresources: {}\nhealth: {}\nmaintainer: {}\n"

type fakeValidator struct{}

func (fakeValidator) Validate(yamlBytes []byte) (domain.Manifest, []string) {

	id, name, version := "", "", ""
	for _, line := range strings.Split(string(yamlBytes), "\n") {
		for prefix, target := range map[string]*string{"id: ": &id, "name: ": &name, "version: ": &version} {
			if strings.HasPrefix(line, prefix) {
				*target = strings.TrimSpace(strings.TrimPrefix(line, prefix))
			}
		}
	}
	if id == "" || name == "" || version == "" {
		return domain.Manifest{}, []string{"manifest is missing required identity fields"}
	}
	canonical := []byte(fmt.Sprintf(`{"id":%q,"name":%q,"version":%q}`, id, name, version))
	return domain.Manifest{
		ID: id, Name: name, Version: version, Scope: domain.ScopeUser,
		Permissions: []string{"artifact.read"}, CanonicalJSON: canonical, Digest: domain.ManifestDigest(canonical),
	}, nil
}

// registrationRequest mirrors one app_registration_requests row.
type registrationRequest struct {
	OwnerUserID    string
	IdempotencyKey string
	RequestDigest  string
	VersionID      string
}

// fakeRepository mirrors the PostgreSQL semantics: consumed idempotency keys
// rule first, the immutable version unique constraint arbitrates inserts, and
// every successful registration consumes its key exactly once.
type fakeRepository struct {
	mu        sync.Mutex
	versions  []domain.AppVersion
	requests  []registrationRequest
	failWith  error
	visitErr  error
	listCalls []int
}

func (r *fakeRepository) Register(_ context.Context, record domain.AppVersion) (domain.AppVersionSummary, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failWith != nil {
		return domain.AppVersionSummary{}, r.failWith
	}
	// A consumed key dominates every other verdict.
	for _, request := range r.requests {
		if request.OwnerUserID == record.OwnerUserID && request.IdempotencyKey == record.IdempotencyKey {
			if request.RequestDigest != record.RequestDigest {
				return domain.AppVersionSummary{}, domain.ErrIdempotencyConflict
			}
			for _, stored := range r.versions {
				if stored.ID == request.VersionID {
					return domain.SummaryOf(stored), nil
				}
			}
			return domain.AppVersionSummary{}, errors.New("registration request references a missing version")
		}
	}
	// The immutable version constraint arbitrates the insert.
	for _, stored := range r.versions {
		if stored.OwnerUserID == record.OwnerUserID && stored.AppID == record.AppID && stored.Version == record.Version {
			if stored.ManifestDigest != record.ManifestDigest {
				return domain.AppVersionSummary{}, domain.ErrVersionExists
			}
			r.requests = append(r.requests, registrationRequest{
				OwnerUserID: record.OwnerUserID, IdempotencyKey: record.IdempotencyKey,
				RequestDigest: record.RequestDigest, VersionID: stored.ID,
			})
			return domain.SummaryOf(stored), nil
		}
	}
	// Fresh version: the mapping is consumed atomically with the insert.
	r.versions = append(r.versions, record)
	r.requests = append(r.requests, registrationRequest{
		OwnerUserID: record.OwnerUserID, IdempotencyKey: record.IdempotencyKey,
		RequestDigest: record.RequestDigest, VersionID: record.ID,
	})
	return domain.SummaryOf(record), nil
}

func (r *fakeRepository) GetVersion(_ context.Context, ownerUserID, appID, version string) (domain.AppVersionSummary, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, stored := range r.versions {
		if stored.OwnerUserID == ownerUserID && stored.AppID == appID && stored.Version == version {
			return domain.SummaryOf(stored), nil
		}
	}
	return domain.AppVersionSummary{}, domain.ErrNotFound
}

func (r *fakeRepository) GetVersionManifest(_ context.Context, ownerUserID, appID, version string) (string, []byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failWith != nil {
		return "", nil, r.failWith
	}
	for _, stored := range r.versions {
		if stored.OwnerUserID == ownerUserID && stored.AppID == appID && stored.Version == version {
			return stored.ManifestDigest, stored.CanonicalManifest, nil
		}
	}
	return "", nil, domain.ErrNotFound
}

func (r *fakeRepository) ListAppIDPage(_ context.Context, ownerUserID, cursor string, limit int) ([]string, string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.listCalls = append(r.listCalls, limit)
	seen := map[string]bool{}
	var ids []string
	for _, stored := range r.versions {
		if stored.OwnerUserID == ownerUserID && stored.AppID > cursor && !seen[stored.AppID] {
			seen[stored.AppID] = true
			ids = append(ids, stored.AppID)
		}
	}
	sort.Strings(ids)
	if len(ids) <= limit {
		return ids, "", nil
	}
	page := ids[:limit]
	return page, page[len(page)-1], nil
}

func (r *fakeRepository) VisitVersionSummaries(_ context.Context, ownerUserID string, appIDs []string, visit func(domain.AppVersionSummary) error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.visitErr != nil {
		return r.visitErr
	}
	requested := make(map[string]bool, len(appIDs))
	for _, appID := range appIDs {
		requested[appID] = true
	}
	ordered := make([]domain.AppVersion, 0, len(r.versions))
	for _, stored := range r.versions {
		if stored.OwnerUserID == ownerUserID && requested[stored.AppID] {
			ordered = append(ordered, stored)
		}
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].AppID < ordered[j].AppID })
	for _, stored := range ordered {
		if err := visit(domain.SummaryOf(stored)); err != nil {
			return err
		}
	}
	return nil
}

type fakeProjectDirectory struct {
	archived map[string]bool
	owner    string
}

func (d fakeProjectDirectory) Get(_ context.Context, ownerUserID, projectID string) (ProjectSummary, error) {
	if ownerUserID == "" || projectID == "" || ownerUserID != d.owner {
		return ProjectSummary{}, ErrProjectDenied
	}
	if archived, ok := d.archived[projectID]; ok {
		if archived {
			return ProjectSummary{}, ErrProjectDenied
		}
		return ProjectSummary{}, nil
	}
	return ProjectSummary{}, ErrProjectDenied
}

type countingGenerator struct{ counter int }

func (g *countingGenerator) New() string {
	g.counter++
	return fmt.Sprintf("00000000-0000-7000-8000-%012d", g.counter)
}

func newTestService(projects ProjectDirectory) (*Service, *fakeRepository, *countingGenerator) {
	repository := &fakeRepository{}
	generator := &countingGenerator{}
	service, err := New(repository, fakeValidator{}, projects, nil, generator)
	if err != nil {
		panic(err)
	}
	return service, repository, generator
}

func manifestFor(id, name, version string) []byte {
	return []byte(fmt.Sprintf(testManifestYAML, id, name, version))
}

func TestRegisterAndIdempotency(t *testing.T) {
	t.Parallel()
	service, repository, _ := newTestService(nil)

	first, err := service.Register(context.Background(), "owner-1", "key-1", manifestFor("notes", "Notes", "1.0.0"))
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if first.AppID != "notes" || first.Version != "1.0.0" {
		t.Fatalf("unexpected summary: %#v", first)
	}

	replay, err := service.Register(context.Background(), "owner-1", "key-1", manifestFor("notes", "Notes", "1.0.0"))
	if err != nil || replay.ManifestDigest != first.ManifestDigest {
		t.Fatalf("idempotent replay must return the stored record: %#v err=%v", replay, err)
	}

	_, err = service.Register(context.Background(), "owner-1", "key-1", manifestFor("notes", "Different", "1.0.0"))
	if !errors.Is(err, domain.ErrIdempotencyConflict) {
		t.Fatalf("same key with a different request must conflict deterministically: %v", err)
	}

	_, err = service.Register(context.Background(), "owner-1", "key-2", manifestFor("notes", "Different", "1.0.0"))
	if !errors.Is(err, domain.ErrVersionExists) {
		t.Fatalf("same version with a different manifest must fail closed: %v", err)
	}

	// The review's core scenario: a second key over the same immutable fact
	// must succeed AND persist, so reusing that key for another request is a
	// conflict instead of a fresh registration.
	sameDigestNewKey, err := service.Register(context.Background(), "owner-1", "key-3", manifestFor("notes", "Notes", "1.0.0"))
	if err != nil || sameDigestNewKey.ManifestDigest != first.ManifestDigest {
		t.Fatalf("same version and digest under a new key must replay the original: %#v err=%v", sameDigestNewKey, err)
	}
	if _, err := service.Register(context.Background(), "owner-1", "key-3", manifestFor("notes", "Notes", "1.1.0")); !errors.Is(err, domain.ErrIdempotencyConflict) {
		t.Fatalf("persisted key must conflict on a different request: %v", err)
	}

	// A different owner may register the same app id and version independently.
	foreign, err := service.Register(context.Background(), "owner-2", "key-1", manifestFor("notes", "Notes", "1.0.0"))
	if err != nil || foreign.AppID != "notes" {
		t.Fatalf("owner isolation failed: %#v err=%v", foreign, err)
	}
	if len(repository.versions) != 2 {
		t.Fatalf("expected exactly two stored versions, got %d", len(repository.versions))
	}
	// owner-1 consumed key-1 and key-3; owner-2 consumed its own key-1.
	if len(repository.requests) != 3 {
		t.Fatalf("expected three persisted registration requests, got %d", len(repository.requests))
	}
}

func TestRegisterRejectsInvalidInput(t *testing.T) {
	t.Parallel()
	service, _, _ := newTestService(nil)
	cases := []struct {
		name  string
		owner string
		key   string
		yaml  []byte
	}{
		{name: "no owner", owner: "", key: "k", yaml: manifestFor("notes", "Notes", "1.0.0")},
		{name: "no idempotency key", owner: "owner-1", key: "", yaml: manifestFor("notes", "Notes", "1.0.0")},
		{name: "oversize key", owner: "owner-1", key: strings.Repeat("k", 129), yaml: manifestFor("notes", "Notes", "1.0.0")},
		{name: "control character key", owner: "owner-1", key: "bad\x01key", yaml: manifestFor("notes", "Notes", "1.0.0")},
		{name: "invalid utf8 key", owner: "owner-1", key: "bad\xffkey", yaml: manifestFor("notes", "Notes", "1.0.0")},
		{name: "oversize manifest", owner: "owner-1", key: "k", yaml: make([]byte, domain.MaxManifestBytes+1)},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := service.Register(context.Background(), testCase.owner, testCase.key, testCase.yaml); !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("expected ErrInvalid, got %v", err)
			}
		})
	}
}

func TestGetReturnsCurrentAndExplicitVersions(t *testing.T) {
	t.Parallel()
	service, _, _ := newTestService(nil)
	for _, version := range []string{"1.9.0", "1.10.0", "1.10.0-rc.2"} {
		if _, err := service.Register(context.Background(), "owner-1", "key-"+version, manifestFor("notes", "Notes", version)); err != nil {
			t.Fatalf("register %s: %v", version, err)
		}
	}
	current, err := service.Get(context.Background(), "owner-1", "notes", "")
	if err != nil || current.Version != "1.10.0" {
		t.Fatalf("current version must follow SemVer precedence: %#v err=%v", current, err)
	}
	explicit, err := service.Get(context.Background(), "owner-1", "notes", "1.9.0")
	if err != nil || explicit.Version != "1.9.0" {
		t.Fatalf("explicit version lookup failed: %#v err=%v", explicit, err)
	}
	if _, err := service.Get(context.Background(), "owner-2", "notes", ""); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("foreign owner must see nothing: %v", err)
	}
	if _, err := service.Get(context.Background(), "owner-1", "missing", ""); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unknown app must be NotFound: %v", err)
	}
	if _, err := service.Get(context.Background(), "owner-1", "notes", "not-semver"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("malformed version must be InvalidArgument-grade: %v", err)
	}
}

func TestGetRejectsMalformedAppID(t *testing.T) {
	t.Parallel()
	service, _, _ := newTestService(nil)
	for _, appID := range []string{"Bad_ID", "x", "", "-leading", "UPPER", strings.Repeat("a", 64)} {
		if _, err := service.Get(context.Background(), "owner-1", appID, ""); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("malformed app id %q must be ErrInvalid, got %v", appID, err)
		}
	}
}

func TestListCurrentVersionsPerApp(t *testing.T) {
	t.Parallel()
	service, repository, _ := newTestService(nil)
	registrations := []struct{ appID, version string }{
		{"zeta-board", "1.0.0"}, {"zeta-board", "1.1.0"},
		{"alpha-notes", "2.0.0"}, {"alpha-notes", "2.0.0-rc.1"},
		{"mid-chart", "0.1.0"},
	}
	for index, registration := range registrations {
		if _, err := service.Register(context.Background(), "owner-1", fmt.Sprintf("key-%d", index), manifestFor(registration.appID, "App", registration.version)); err != nil {
			t.Fatalf("register %s@%s: %v", registration.appID, registration.version, err)
		}
	}
	listed, err := service.List(context.Background(), "owner-1", "", "", 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var order []string
	for _, record := range listed.Items {
		order = append(order, record.AppID)
	}
	if strings.Join(order, ",") != "alpha-notes,mid-chart,zeta-board" {
		t.Fatalf("list must return one current version per app sorted by app id: %v", order)
	}
	if listed.Items[0].Version != "2.0.0" || listed.Items[2].Version != "1.1.0" {
		t.Fatalf("list must project the current version: %#v", listed.Items)
	}

	paged, err := service.List(context.Background(), "owner-1", "", "", 2)
	if err != nil || len(paged.Items) != 2 || paged.Items[1].AppID != "mid-chart" || paged.NextToken != "mid-chart" {
		t.Fatalf("paging must be stable: %#v err=%v", paged, err)
	}
	resumed, err := service.List(context.Background(), "owner-1", "", "mid-chart", 2)
	if err != nil || len(resumed.Items) != 1 || resumed.Items[0].AppID != "zeta-board" || resumed.NextToken != "" {
		t.Fatalf("cursor must resume deterministically to a final page: %#v err=%v", resumed, err)
	}

	// An exactly-full page must not fabricate a cursor: repository probes
	// limit+1 and only a real extra app produces a token.
	if len(repository.listCalls) == 0 {
		t.Fatal("expected repository paging calls")
	}

	foreign, err := service.List(context.Background(), "owner-2", "", "", 10)
	if err != nil || len(foreign.Items) != 0 || foreign.NextToken != "" {
		t.Fatalf("owner isolation failed: %#v err=%v", foreign, err)
	}
}

func TestListPageSizeNormalizationAndBoundaries(t *testing.T) {
	t.Parallel()
	service, repository, _ := newTestService(nil)
	for index := 0; index < 8; index++ {
		appID := fmt.Sprintf("app-%02d", index)
		if _, err := service.Register(context.Background(), "owner-1", fmt.Sprintf("key-%02d", index), manifestFor(appID, "App", "1.0.0")); err != nil {
			t.Fatalf("register %s: %v", appID, err)
		}
	}

	// Zero page size means the default; negative page size is an explicit
	// request error, never a silent default.
	defaultPage, err := service.List(context.Background(), "owner-1", "", "", 0)
	if err != nil || len(defaultPage.Items) != 8 || defaultPage.NextToken != "" {
		t.Fatalf("default page must hold every app without a token: %#v err=%v", defaultPage, err)
	}
	if _, err := service.List(context.Background(), "owner-1", "", "", -1); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("negative page size must be ErrInvalid: %v", err)
	}

	// Values above the maximum clamp at the application boundary exactly once.
	repository.listCalls = nil
	if _, err := service.List(context.Background(), "owner-1", "", "", 500); err != nil {
		t.Fatalf("oversize page request: %v", err)
	}
	if len(repository.listCalls) != 1 || repository.listCalls[0] != maxPageSize {
		t.Fatalf("page size must clamp to %d before the repository: %v", maxPageSize, repository.listCalls)
	}

	// A cursor is a last app ID and must obey the app-ID grammar.
	for _, cursor := range []string{"not a cursor", strings.Repeat("x", 200), "UPPER-CASE"} {
		if _, err := service.List(context.Background(), "owner-1", "", cursor, 10); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("malformed cursor %q must be ErrInvalid: %v", cursor, err)
		}
	}
}

func TestListExactlyFullPageHasNoFakeToken(t *testing.T) {
	t.Parallel()
	service, _, _ := newTestService(nil)
	for index := 0; index < 3; index++ {
		appID := fmt.Sprintf("exact-%02d", index)
		if _, err := service.Register(context.Background(), "owner-1", fmt.Sprintf("key-%02d", index), manifestFor(appID, "App", "1.0.0")); err != nil {
			t.Fatalf("register %s: %v", appID, err)
		}
	}
	page, err := service.List(context.Background(), "owner-1", "", "", 3)
	if err != nil || len(page.Items) != 3 || page.NextToken != "" {
		t.Fatalf("exactly-full final page must have no next token: %#v err=%v", page, err)
	}
	// Following an anyway-empty page after the last app must be empty and
	// terminal rather than looping.
	next, err := service.List(context.Background(), "owner-1", "", "exact-02", 3)
	if err != nil || len(next.Items) != 0 || next.NextToken != "" {
		t.Fatalf("page after the last app must be empty: %#v err=%v", next, err)
	}
}

func TestListProjectContextFailsClosed(t *testing.T) {
	t.Parallel()
	const (
		activeProject   = "00000000-0000-7000-8000-000000000001"
		archivedProject = "00000000-0000-7000-8000-000000000002"
	)
	directory := fakeProjectDirectory{owner: "owner-1", archived: map[string]bool{activeProject: false, archivedProject: true}}
	service, _, _ := newTestService(directory)
	if _, err := service.Register(context.Background(), "owner-1", "key-1", manifestFor("notes", "Notes", "1.0.0")); err != nil {
		t.Fatalf("register: %v", err)
	}
	listed, err := service.List(context.Background(), "owner-1", activeProject, "", 10)
	if err != nil || len(listed.Items) != 1 {
		t.Fatalf("active project context must list the registry catalog: %#v err=%v", listed, err)
	}
	for _, projectID := range []string{archivedProject, "00000000-0000-7000-8000-000000000003"} {
		if _, err := service.List(context.Background(), "owner-1", projectID, "", 10); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("project %s must map to NotFound: %v", projectID, err)
		}
	}
	if _, err := service.List(context.Background(), "owner-2", activeProject, "", 10); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("foreign owner must not use the project context: %v", err)
	}
	// Malformed project identifiers are request errors, not database errors.
	for _, projectID := range []string{"not-a-uuid", "00000000-0000-7000-8000"} {
		if _, err := service.List(context.Background(), "owner-1", projectID, "", 10); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("malformed project id %q must be ErrInvalid: %v", projectID, err)
		}
	}
}

func TestRepositoryFailureIsNotADomainError(t *testing.T) {
	t.Parallel()
	service, repository, _ := newTestService(nil)
	// Internal repository failures (SQL, constraints, connections) must stay
	// distinguishable from every domain outcome; transport maps only this
	// class to a sanitized Internal error.
	repository.failWith = errors.New("connection refused")
	_, err := service.Register(context.Background(), "owner-1", "key-1", manifestFor("notes", "Notes", "1.0.0"))
	if err == nil {
		t.Fatal("repository failure must surface as an error")
	}
	for _, sentinel := range []error{domain.ErrInvalid, domain.ErrNotFound, domain.ErrVersionExists, domain.ErrIdempotencyConflict} {
		if errors.Is(err, sentinel) {
			t.Fatalf("internal failure must not masquerade as %v", sentinel)
		}
	}

	repository.failWith = nil
	repository.visitErr = errors.New("result stream reset")
	if _, err := service.Get(context.Background(), "owner-1", "notes", ""); err == nil || errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("stream failure must not masquerade as a domain outcome: %v", err)
	}
}

// Compile-time proof that the fake satisfies the port contract.
var _ ports.Repository = (*fakeRepository)(nil)

// bundleArtifactDirectory is the neutral ArtifactDirectory fake: it denies
// anything except the exact (owner, artifact, digest) triple it knows.
type bundleArtifactDirectory struct {
	artifactID     string
	artifactDigest string
	ownerUserID    string
	failWith       error
}

func (d *bundleArtifactDirectory) VerifyWebBundle(_ context.Context, ownerUserID, artifactID, digest string) error {
	if d.failWith != nil {
		return d.failWith
	}
	if ownerUserID == d.ownerUserID && artifactID == d.artifactID && digest == d.artifactDigest {
		return nil
	}
	return ErrArtifactDenied
}

const webBundleManifestYAML = "apiVersion: workos.app/v1\nid: bundle-app\nname: Bundle App\nversion: 1.0.0\nscope: user\n" +
	"runtime:\n  type: web-bundle\n  artifactId: 0198d7ea-2110-7c42-b659-c5e4d73bc343\n" +
	"  artifactDigest: sha256:%s\nsurfaces:\n  - id: main\n    renderer: web-bundle\n" +
	"permissions: [artifact.read]\nresources: {}\nhealth: {}\nmaintainer: {}\n"

type bundleValidator struct{}

func (bundleValidator) Validate(yamlBytes []byte) (domain.Manifest, []string) {
	if !strings.Contains(string(yamlBytes), "type: web-bundle") {
		return domain.Manifest{}, []string{"not a web bundle manifest"}
	}
	digest := ""
	for _, line := range strings.Split(string(yamlBytes), "\n") {
		if strings.HasPrefix(line, "  artifactDigest: ") {
			digest = strings.TrimSpace(strings.TrimPrefix(line, "  artifactDigest: "))
		}
	}
	canonical := []byte(`{"id":"bundle-app","runtime":{"artifactDigest":"` + digest + `","artifactId":"0198d7ea-2110-7c42-b659-c5e4d73bc343","type":"web-bundle"},"version":"1.0.0"}`)
	ref, ok := domain.ParseWebBundleRef(canonical)
	if !ok {
		return domain.Manifest{}, []string{"web bundle reference is malformed"}
	}
	return domain.Manifest{
		ID: "bundle-app", Version: "1.0.0", Scope: domain.ScopeUser,
		RuntimeType: domain.RuntimeTypeWebBundle, WebBundle: &ref,
		CanonicalJSON: canonical, Digest: domain.ManifestDigest(canonical),
	}, nil
}

func newBundleService(repository ports.Repository, directory ArtifactDirectory) *Service {
	service, err := New(repository, bundleValidator{}, nil, directory, &countingGenerator{})
	if err != nil {
		panic(err)
	}
	return service
}

func TestRegisterVerifiesWebBundleReference(t *testing.T) {
	t.Parallel()
	owner := "owner-1"
	digest := strings.Repeat("a", 64)
	repository := &fakeRepository{}
	directory := &bundleArtifactDirectory{artifactID: "0198d7ea-2110-7c42-b659-c5e4d73bc343", artifactDigest: "sha256:" + digest, ownerUserID: owner}
	service := newBundleService(repository, directory)
	ctx := context.Background()

	if _, err := service.Register(ctx, owner, "bundle-1", []byte(fmt.Sprintf(webBundleManifestYAML, digest))); err != nil {
		t.Fatalf("verified bundle manifest rejected: %v", err)
	}
	// The registry stored the launchable version.
	if resolution, err := service.ResolveWebBundle(ctx, owner, "bundle-app", "1.0.0"); err != nil || resolution.Ref.ArtifactID != directory.artifactID {
		t.Fatalf("resolve after register failed: %v %+v", err, resolution)
	}

	// Foreign artifact: sanitized NotFound, nothing persisted.
	foreign := &bundleArtifactDirectory{ownerUserID: "owner-2", artifactID: "0198d7ea-2110-7c42-b659-c5e4d73bc343", artifactDigest: "sha256:" + digest}
	foreignService := newBundleService(&fakeRepository{}, foreign)
	if _, err := foreignService.Register(ctx, owner, "bundle-2", []byte(fmt.Sprintf(webBundleManifestYAML, digest))); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("foreign artifact verdict: %v", err)
	}
	// Digest mismatch: ErrArtifactDenied maps to NotFound by the port contract.
	mismatch := &bundleArtifactDirectory{ownerUserID: owner, artifactID: "0198d7ea-2110-7c42-b659-c5e4d73bc343", artifactDigest: "sha256:" + strings.Repeat("b", 64)}
	mismatchService := newBundleService(&fakeRepository{}, mismatch)
	if _, err := mismatchService.Register(ctx, owner, "bundle-3", []byte(fmt.Sprintf(webBundleManifestYAML, digest))); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("mismatched digest verdict: %v", err)
	}
	// Infrastructure failure is not a domain verdict.
	failing := &bundleArtifactDirectory{failWith: errors.New("artifact store unreachable")}
	failingService := newBundleService(&fakeRepository{}, failing)
	if _, err := failingService.Register(ctx, owner, "bundle-4", []byte(fmt.Sprintf(webBundleManifestYAML, digest))); err == nil || errors.Is(err, domain.ErrNotFound) || errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("infrastructure error surfaced as domain verdict: %v", err)
	}
}

func TestResolveWebBundleVerdicts(t *testing.T) {
	t.Parallel()
	owner := "owner-1"
	repository := &fakeRepository{}
	service := newBundleService(repository, &bundleArtifactDirectory{})
	ctx := context.Background()
	if _, err := service.ResolveWebBundle(ctx, owner, "unknown-app", "1.0.0"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unknown app verdict: %v", err)
	}
	if _, err := service.ResolveWebBundle(ctx, owner, "bundle-app", ""); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("empty version verdict: %v", err)
	}
	// A legacy (non-bundle) version resolves as unsupported.
	legacy, err := New(repository, fakeValidator{}, nil, nil, &countingGenerator{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Register(ctx, owner, "legacy-key-1", []byte(fmt.Sprintf(testManifestYAML, "legacy-app", "Legacy", "1.0.0"))); err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.ResolveWebBundle(ctx, owner, "legacy-app", "1.0.0"); !errors.Is(err, ErrUnsupportedRuntime) {
		t.Fatalf("legacy version verdict: %v", err)
	}
}
