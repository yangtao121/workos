package application

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/yangtao121/workos/internal/core/project/domain"
	"github.com/yangtao121/workos/internal/core/project/ports"
)

const createTestOwner = "owner-1"

var errRepositoryFailed = errors.New("repository failed")

// stubRepository records the commands the application issues so tests can
// assert validation happens before any repository call and that page
// results are the application's verdict.
type stubRepository struct {
	lookups   int
	creates   int
	getCalls  int
	listCalls int
	listLimit int

	lookupStored ports.StoredCreateRequest
	lookupFound  bool
	lookupErr    error

	createErr  error
	createRecv ports.CreateCommand
	getProject domain.Project
	getErr     error
	updateErr  error
	listItems  []domain.Project
	listErr    error
}

func (r *stubRepository) LookupCreateRequest(context.Context, string, string) (ports.StoredCreateRequest, bool, error) {
	r.lookups++
	return r.lookupStored, r.lookupFound, r.lookupErr
}

func (r *stubRepository) CreateProject(_ context.Context, command ports.CreateCommand) (domain.Project, error) {
	r.creates++
	r.createRecv = command
	if r.createErr != nil {
		return domain.Project{}, r.createErr
	}
	return command.Project, nil
}

func (r *stubRepository) GetProject(context.Context, string, string) (domain.Project, error) {
	r.getCalls++
	if r.getErr != nil {
		return domain.Project{}, r.getErr
	}
	if r.getProject.ID == "" {
		return domain.Project{}, domain.ErrNotFound
	}
	return r.getProject, nil
}

func (r *stubRepository) ListProjects(_ context.Context, _, _ string, limit int, _ bool) ([]domain.Project, error) {
	r.listCalls++
	r.listLimit = limit
	if r.listErr != nil {
		return nil, r.listErr
	}
	return r.listItems, nil
}

func (r *stubRepository) UpdateProject(_ context.Context, project domain.Project, _ int64) (domain.Project, error) {
	if r.updateErr != nil {
		return domain.Project{}, r.updateErr
	}
	return project, nil
}

func (r *stubRepository) ArchiveProject(context.Context, string, string, int64) (domain.Project, error) {
	return domain.Project{}, nil
}

type staticGenerator struct{}

func (staticGenerator) New() string { return "01999999-9999-7999-8999-999999999999" }

func newService(repository ports.Repository) *Service {
	return New(repository, staticGenerator{})
}

func legalCreateInput() CreateInput {
	return CreateInput{
		OwnerUserID: createTestOwner, IdempotencyKey: "create-key", Name: "  Mission Control  ", Icon: "◈",
		WorkspaceRefs:  []domain.WorkspaceRef{{ID: "r1", Kind: "WORKSPACE_KIND_LOCAL_GIT", URI: "file:///repos/a"}},
		HarnessBinding: &domain.HarnessBinding{ProviderID: "fake", InstancePolicy: "lazy", ResourcePolicyID: "project-no-tools"},
	}
}

func TestCreateValidatesBeforeAnyRepositoryCall(t *testing.T) {
	t.Parallel()
	for name, mutate := range map[string]func(*CreateInput){
		"missing owner":     func(i *CreateInput) { i.OwnerUserID = "" },
		"empty key":         func(i *CreateInput) { i.IdempotencyKey = "" },
		"key control char":  func(i *CreateInput) { i.IdempotencyKey = "key\n" },
		"key over limit":    func(i *CreateInput) { i.IdempotencyKey = string(make([]rune, 129)) },
		"blank name":        func(i *CreateInput) { i.Name = "   " },
		"icon over limit":   func(i *CreateInput) { i.Icon = string(make([]rune, 129)) },
		"icon control char": func(i *CreateInput) { i.Icon = "i\n" },
		"ref bad kind": func(i *CreateInput) {
			i.WorkspaceRefs[0].Kind = "WORKSPACE_KIND_UNSPECIFIED"
		},
		"ref duplicate id": func(i *CreateInput) {
			i.WorkspaceRefs = append(i.WorkspaceRefs, i.WorkspaceRefs[0])
		},
		"ref empty uri": func(i *CreateInput) { i.WorkspaceRefs[0].URI = "" },
		"binding unknown policy": func(i *CreateInput) {
			i.HarnessBinding.InstancePolicy = "magic"
		},
	} {
		repository := &stubRepository{}
		input := legalCreateInput()
		mutate(&input)
		if _, err := newService(repository).Create(context.Background(), input); !errors.Is(err, domain.ErrInvalid) {
			t.Errorf("%s: expected ErrInvalid, got %v", name, err)
		}
		if repository.lookups != 0 || repository.creates != 0 {
			t.Errorf("%s: validation must not touch the repository (lookups=%d creates=%d)", name, repository.lookups, repository.creates)
		}
	}
}

func TestCreateIssuesValidatedCommand(t *testing.T) {
	t.Parallel()
	repository := &stubRepository{}
	input := legalCreateInput()
	project, err := newService(repository).Create(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if project.Revision != 1 || project.ID == "" || project.KnowledgeCollectionID == "" || project.ArtifactCollectionID == "" {
		t.Fatalf("first response must start the aggregate: %#v", project)
	}
	if project.Name != "Mission Control" {
		t.Fatalf("name must be normalized before digest and storage: %q", project.Name)
	}
	if !repository.createRecv.Now.IsZero() && repository.createRecv.Now.Location() != time.UTC {
		t.Fatal("command clock must be UTC")
	}
	if repository.createRecv.RequestDigest != domain.CreateRequestDigest("Mission Control", input.Icon, input.WorkspaceRefs, input.HarnessBinding) {
		t.Fatal("command digest must cover the normalized request")
	}
	if repository.createRecv.IdempotencyKey != input.IdempotencyKey {
		t.Fatal("key must be carried for owner-scoped adjudication")
	}
}

func TestCreateReplaysStoredSnapshotOnSameDigest(t *testing.T) {
	t.Parallel()
	snapshot := domain.Project{ID: "01999999-9999-7999-8999-999999999991", OwnerUserID: createTestOwner, Name: "First", Revision: 1}
	input := legalCreateInput()
	digest := domain.CreateRequestDigest("Mission Control", input.Icon, input.WorkspaceRefs, input.HarnessBinding)
	repository := &stubRepository{lookupFound: true, lookupStored: ports.StoredCreateRequest{RequestDigest: digest, Result: snapshot}}
	replayed, err := newService(repository).Create(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.ID != snapshot.ID || replayed.Revision != 1 || replayed.Name != "First" {
		t.Fatalf("replay must return the first response snapshot: %#v", replayed)
	}
	if repository.creates != 0 {
		t.Fatal("replay must not attempt a second create")
	}

	// Any canonical difference is a stable conflict, including a changed
	// name after trimming.
	conflictInput := legalCreateInput()
	conflictInput.Name = "Different Name"
	_, err = newService(repository).Create(context.Background(), conflictInput)
	if !errors.Is(err, domain.ErrIdempotencyConflict) {
		t.Fatalf("different request on same key must conflict, got %v", err)
	}
	if repository.creates != 0 {
		t.Fatal("conflict must not attempt a create")
	}
}

func TestListProjectsNormalizesPageSizeAndToken(t *testing.T) {
	t.Parallel()
	items := func(ids ...string) []domain.Project {
		projects := make([]domain.Project, 0, len(ids))
		for _, id := range ids {
			projects = append(projects, domain.Project{ID: id})
		}
		return projects
	}
	valid := "01999999-9999-7999-8999-999999999991"

	t.Run("negative page size is invalid", func(t *testing.T) {
		repository := &stubRepository{}
		if _, err := newService(repository).ListProjects(context.Background(), createTestOwner, "", -1, false); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("expected ErrInvalid, got %v", err)
		}
		if repository.listCalls != 0 {
			t.Fatal("invalid page size must not reach the repository")
		}
	})
	t.Run("zero means default with one-row probe", func(t *testing.T) {
		repository := &stubRepository{listItems: items("a")}
		page, err := newService(repository).ListProjects(context.Background(), createTestOwner, "", 0, false)
		if err != nil {
			t.Fatal(err)
		}
		if repository.listLimit != 51 {
			t.Fatalf("default page must probe 51 rows, got %d", repository.listLimit)
		}
		if len(page.Items) != 1 || page.NextToken != "" {
			t.Fatalf("partial page must not mint a token: %#v", page)
		}
	})
	t.Run("over one hundred clamps", func(t *testing.T) {
		repository := &stubRepository{}
		if _, err := newService(repository).ListProjects(context.Background(), createTestOwner, "", 500, false); err != nil {
			t.Fatal(err)
		}
		if repository.listLimit != 101 {
			t.Fatalf("clamped page must probe 101 rows, got %d", repository.listLimit)
		}
	})
	t.Run("exact full page issues token of last id", func(t *testing.T) {
		// The third row is the probe row proving a next page exists; the
		// application trims the page and mints the token from its own trim.
		repository := &stubRepository{listItems: items("a", "b", "c")}
		page, err := newService(repository).ListProjects(context.Background(), createTestOwner, "", 2, false)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) != 2 {
			t.Fatalf("must return only the effective page size: %d", len(page.Items))
		}
		if page.NextToken != "b" {
			t.Fatalf("next token must be the last returned id: %q", page.NextToken)
		}
	})
	t.Run("malformed cursor is invalid", func(t *testing.T) {
		for name, cursor := range map[string]string{
			"garbage":   "not-a-cursor",
			"uppercase": "01999999-9999-7999-8999-99999999999A",
			"uuid v4":   "01999999-9999-4999-8999-999999999991",
		} {
			repository := &stubRepository{}
			if _, err := newService(repository).ListProjects(context.Background(), createTestOwner, cursor, 10, false); !errors.Is(err, domain.ErrInvalid) {
				t.Errorf("%s cursor: expected ErrInvalid, got %v", name, err)
			}
			if repository.listCalls != 0 {
				t.Errorf("%s cursor: must not reach the repository", name)
			}
		}
	})
	t.Run("valid cursor reaches the repository", func(t *testing.T) {
		repository := &stubRepository{}
		if _, err := newService(repository).ListProjects(context.Background(), createTestOwner, valid, 10, true); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("repository failures pass through", func(t *testing.T) {
		repository := &stubRepository{listErr: fmt.Errorf("wrapped: %w", ports.ErrStoreUnavailable)}
		if _, err := newService(repository).ListProjects(context.Background(), createTestOwner, "", 10, false); !errors.Is(err, ports.ErrStoreUnavailable) {
			t.Fatalf("sentinels must pass through for transport mapping, got %v", err)
		}
	})
}

func TestUpdateRejectsAmbiguousInputBeforeReading(t *testing.T) {
	t.Parallel()
	refs := []domain.WorkspaceRef{{ID: "r1", Kind: "WORKSPACE_KIND_LOCAL_GIT", URI: "file:///repos/a"}}
	binding := &domain.HarnessBinding{ProviderID: "fake", InstancePolicy: "lazy", ResourcePolicyID: "p"}
	validProject := domain.Project{ID: "01999999-9999-7999-8999-999999999991", OwnerUserID: createTestOwner, Revision: 3}

	cases := []struct {
		name   string
		mutate func(*UpdateInput)
	}{
		{"malformed project id", func(i *UpdateInput) { i.ProjectID = "p-1" }},
		{"zero revision", func(i *UpdateInput) { i.ExpectedRevision = 0 }},
		{"clear and provide binding conflict", func(i *UpdateInput) { i.ClearHarnessBinding, i.HarnessBinding = true, binding }},
		{"refs without replace flag", func(i *UpdateInput) { i.WorkspaceRefs, i.ReplaceWorkspaceRefs = refs, false }},
		{"replacement refs invalid kind", func(i *UpdateInput) {
			i.ReplaceWorkspaceRefs = true
			i.WorkspaceRefs = []domain.WorkspaceRef{{ID: "r1", Kind: "WORKSPACE_KIND_UNSPECIFIED", URI: "file:///x"}}
		}},
		{"icon invalid", func(i *UpdateInput) { icon := "bad\nicon"; i.Icon = &icon }},
		{"name blank", func(i *UpdateInput) { blank := "  "; i.Name = &blank }},
	}
	for _, testCase := range cases {
		repository := &stubRepository{getProject: validProject}
		input := UpdateInput{OwnerUserID: createTestOwner, ProjectID: validProject.ID, ExpectedRevision: 3}
		testCase.mutate(&input)
		if _, err := newService(repository).Update(context.Background(), input); !errors.Is(err, domain.ErrInvalid) {
			t.Errorf("%s: expected ErrInvalid, got %v", testCase.name, err)
		}
		if repository.getCalls != 0 {
			t.Errorf("%s: ambiguous input must be rejected before the existence read", testCase.name)
		}
	}

	t.Run("repository conflict verdict passes through", func(t *testing.T) {
		// A guarded-update miss (lost revision race) is the repository's
		// verdict; the application forwards it untouched.
		repository := &stubRepository{getProject: validProject, updateErr: domain.ErrConflict}
		name := "ok"
		_, err := newService(repository).Update(context.Background(), UpdateInput{
			OwnerUserID: createTestOwner, ProjectID: validProject.ID, ExpectedRevision: 2, Name: &name,
		})
		if !errors.Is(err, domain.ErrConflict) {
			t.Fatalf("expected ErrConflict, got %v", err)
		}
	})
	t.Run("archived project conflicts", func(t *testing.T) {
		archived := validProject
		archived.ArchivedAt = &time.Time{}
		repository := &stubRepository{getProject: archived}
		_, err := newService(repository).Update(context.Background(), UpdateInput{
			OwnerUserID: createTestOwner, ProjectID: archived.ID, ExpectedRevision: 3,
		})
		if !errors.Is(err, domain.ErrConflict) {
			t.Fatalf("expected ErrConflict for archived project, got %v", err)
		}
	})
	t.Run("replacement refs applied", func(t *testing.T) {
		repository := &stubRepository{getProject: validProject}
		updated, err := newService(repository).Update(context.Background(), UpdateInput{
			OwnerUserID: createTestOwner, ProjectID: validProject.ID, ExpectedRevision: 3,
			WorkspaceRefs: refs, ReplaceWorkspaceRefs: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(updated.WorkspaceRefs) != 1 || updated.Revision != 4 {
			t.Fatalf("unexpected update result: %#v", updated)
		}
	})
}

func TestArchiveDistinguishesMissingFromStale(t *testing.T) {
	t.Parallel()
	valid := "01999999-9999-7999-8999-999999999991"
	t.Run("malformed input", func(t *testing.T) {
		repository := &stubRepository{}
		if _, err := newService(repository).Archive(context.Background(), createTestOwner, "p-1", 1); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("malformed id must be invalid, got %v", err)
		}
		if _, err := newService(repository).Archive(context.Background(), createTestOwner, valid, 0); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("non-positive revision must be invalid, got %v", err)
		}
		if repository.getCalls != 0 {
			t.Fatal("malformed archive must not read")
		}
	})
	t.Run("missing project is not found", func(t *testing.T) {
		repository := &stubRepository{}
		if _, err := newService(repository).Archive(context.Background(), createTestOwner, valid, 4); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("missing project must be NotFound, got %v", err)
		}
	})
	t.Run("archived project conflicts", func(t *testing.T) {
		archived := domain.Project{ID: valid, OwnerUserID: createTestOwner, Revision: 5, ArchivedAt: &time.Time{}}
		repository := &stubRepository{getProject: archived}
		if _, err := newService(repository).Archive(context.Background(), createTestOwner, valid, 5); !errors.Is(err, domain.ErrConflict) {
			t.Fatalf("re-archiving must conflict, got %v", err)
		}
	})
	t.Run("existing project archives through repository", func(t *testing.T) {
		repository := &stubRepository{getProject: domain.Project{ID: valid, OwnerUserID: createTestOwner, Revision: 5}}
		if _, err := newService(repository).Archive(context.Background(), createTestOwner, valid, 5); err != nil {
			t.Fatal(err)
		}
	})
}

func (r *stubRepository) ReconcileArchivedProjectsPage(context.Context, string, int) ([]ports.ArchivedProjectRef, string, error) {
	return nil, "", errors.New("not used in this test")
}
