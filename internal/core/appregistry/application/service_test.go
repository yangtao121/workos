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

// fakeRepository mirrors the PostgreSQL constraints so application semantics
// can be tested without a database.
type fakeRepository struct {
	mu       sync.Mutex
	versions []domain.AppVersion
	failWith error
}

func (r *fakeRepository) Register(_ context.Context, record domain.AppVersion) (domain.AppVersion, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failWith != nil {
		return domain.AppVersion{}, r.failWith
	}
	for _, stored := range r.versions {
		if stored.OwnerUserID == record.OwnerUserID && stored.IdempotencyKey == record.IdempotencyKey {
			if stored.RequestDigest != record.RequestDigest {
				return domain.AppVersion{}, domain.ErrIdempotencyConflict
			}
			return stored, nil
		}
	}
	for _, stored := range r.versions {
		if stored.OwnerUserID == record.OwnerUserID && stored.AppID == record.AppID && stored.Version == record.Version {
			if stored.ManifestDigest == record.ManifestDigest {
				return stored, nil
			}
			return domain.AppVersion{}, domain.ErrVersionExists
		}
	}
	r.versions = append(r.versions, record)
	return record, nil
}

func (r *fakeRepository) GetVersion(_ context.Context, ownerUserID, appID, version string) (domain.AppVersion, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, stored := range r.versions {
		if stored.OwnerUserID == ownerUserID && stored.AppID == appID && stored.Version == version {
			return stored, nil
		}
	}
	return domain.AppVersion{}, domain.ErrNotFound
}

func (r *fakeRepository) GetAppVersions(_ context.Context, ownerUserID, appID string) ([]domain.AppVersion, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var result []domain.AppVersion
	for _, stored := range r.versions {
		if stored.OwnerUserID == ownerUserID && stored.AppID == appID {
			result = append(result, stored)
		}
	}
	return result, nil
}

func (r *fakeRepository) ListAppIDs(_ context.Context, ownerUserID, cursor string, limit int) ([]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	seen := map[string]bool{}
	var ids []string
	for _, stored := range r.versions {
		if stored.OwnerUserID == ownerUserID && stored.AppID > cursor && !seen[stored.AppID] {
			seen[stored.AppID] = true
			ids = append(ids, stored.AppID)
		}
	}
	sort.Strings(ids)
	if len(ids) > limit {
		ids = ids[:limit]
	}
	return ids, nil
}

func (r *fakeRepository) GetVersionsForApps(_ context.Context, ownerUserID string, appIDs []string) ([]domain.AppVersion, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var result []domain.AppVersion
	for _, stored := range r.versions {
		if stored.OwnerUserID != ownerUserID {
			continue
		}
		for _, appID := range appIDs {
			if stored.AppID == appID {
				result = append(result, stored)
				break
			}
		}
	}
	return result, nil
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
	service, err := New(repository, fakeValidator{}, projects, generator)
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
	if first.AppID != "notes" || first.Version != "1.0.0" || first.RequestDigest != first.ManifestDigest {
		t.Fatalf("unexpected record: %#v", first)
	}

	replay, err := service.Register(context.Background(), "owner-1", "key-1", manifestFor("notes", "Notes", "1.0.0"))
	if err != nil || replay.ID != first.ID {
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

	sameDigestNewKey, err := service.Register(context.Background(), "owner-1", "key-3", manifestFor("notes", "Notes", "1.0.0"))
	if err != nil || sameDigestNewKey.ID != first.ID {
		t.Fatalf("same version and digest under a new key must replay the original: %#v err=%v", sameDigestNewKey, err)
	}

	// A different owner may register the same app id and version.
	foreign, err := service.Register(context.Background(), "owner-2", "key-1", manifestFor("notes", "Notes", "1.0.0"))
	if err != nil || foreign.OwnerUserID != "owner-2" || foreign.ID == first.ID {
		t.Fatalf("owner isolation failed: %#v err=%v", foreign, err)
	}
	if len(repository.versions) != 2 {
		t.Fatalf("expected exactly two stored versions, got %d", len(repository.versions))
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

func TestListCurrentVersionsPerApp(t *testing.T) {
	t.Parallel()
	service, _, _ := newTestService(nil)
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
	for _, record := range listed {
		order = append(order, record.AppID)
	}
	if strings.Join(order, ",") != "alpha-notes,mid-chart,zeta-board" {
		t.Fatalf("list must return one current version per app sorted by app id: %v", order)
	}
	if listed[0].Version != "2.0.0" || listed[2].Version != "1.1.0" {
		t.Fatalf("list must project the current version: %#v", listed)
	}

	paged, err := service.List(context.Background(), "owner-1", "", "", 2)
	if err != nil || len(paged) != 2 || paged[1].AppID != "mid-chart" {
		t.Fatalf("paging must be stable: %#v err=%v", paged, err)
	}
	resumed, err := service.List(context.Background(), "owner-1", "", "mid-chart", 2)
	if err != nil || len(resumed) != 1 || resumed[0].AppID != "zeta-board" {
		t.Fatalf("cursor must resume deterministically: %#v err=%v", resumed, err)
	}

	foreign, err := service.List(context.Background(), "owner-2", "", "", 10)
	if err != nil || len(foreign) != 0 {
		t.Fatalf("owner isolation failed: %#v err=%v", foreign, err)
	}
}

func TestListProjectContextFailsClosed(t *testing.T) {
	t.Parallel()
	directory := fakeProjectDirectory{owner: "owner-1", archived: map[string]bool{"active": false, "gone": true}}
	service, _, _ := newTestService(directory)
	if _, err := service.Register(context.Background(), "owner-1", "key-1", manifestFor("notes", "Notes", "1.0.0")); err != nil {
		t.Fatalf("register: %v", err)
	}
	listed, err := service.List(context.Background(), "owner-1", "active", "", 10)
	if err != nil || len(listed) != 1 {
		t.Fatalf("active project context must list the registry catalog: %#v err=%v", listed, err)
	}
	for _, projectID := range []string{"gone", "missing"} {
		if _, err := service.List(context.Background(), "owner-1", projectID, "", 10); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("project %s must map to NotFound: %v", projectID, err)
		}
	}
	if _, err := service.List(context.Background(), "owner-2", "active", "", 10); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("foreign owner must not use the project context: %v", err)
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
}

// Compile-time proof that the fake satisfies the port contract.
var _ ports.Repository = (*fakeRepository)(nil)
