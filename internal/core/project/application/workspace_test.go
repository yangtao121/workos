package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/yangtao121/workos/internal/core/project/domain"
	"github.com/yangtao121/workos/internal/core/project/ports"
)

type fakeDirectory struct {
	sources []ports.WorkspaceSource
	failing bool
}

func (f *fakeDirectory) Sources(_ context.Context, ownerUserID string) ([]ports.WorkspaceSource, error) {
	if f.failing {
		return nil, errors.New("directory unreachable")
	}
	return f.sources, nil
}

type WorkspaceSource = ports.WorkspaceSource

type fakeWorkspaceRepository struct {
	bindings map[string]domain.WorkspaceBinding
	byKey    map[string]string
}

func newFakeWorkspaceRepository() *fakeWorkspaceRepository {
	return &fakeWorkspaceRepository{bindings: map[string]domain.WorkspaceBinding{}, byKey: map[string]string{}}
}

func (f *fakeWorkspaceRepository) InsertWorkspaceBinding(_ context.Context, binding domain.WorkspaceBinding, digest string) (string, bool, error) {
	if existingID, ok := f.byKey[binding.OwnerUserID+"\x00"+binding.IdempotencyKey]; ok {
		existing := f.bindings[existingID]
		existingDigest := workspaceRequestDigest(existing.WorkspaceSourceID, existing.DisplayName, existing.ReadOnly)
		return existingDigest, false, nil
	}
	binding.Revision = 1
	f.bindings[binding.ID] = binding
	f.byKey[binding.OwnerUserID+"\x00"+binding.IdempotencyKey] = binding.ID
	return digest, true, nil
}

func (f *fakeWorkspaceRepository) GetWorkspaceBinding(_ context.Context, ownerUserID, bindingID string) (domain.WorkspaceBinding, error) {
	binding, ok := f.bindings[bindingID]
	if !ok || binding.OwnerUserID != ownerUserID {
		return domain.WorkspaceBinding{}, domain.ErrNotFound
	}
	return binding, nil
}

func (f *fakeWorkspaceRepository) GetWorkspaceBindingByIdempotency(_ context.Context, ownerUserID, key string) (domain.WorkspaceBinding, error) {
	id, ok := f.byKey[ownerUserID+"\x00"+key]
	if !ok {
		return domain.WorkspaceBinding{}, domain.ErrNotFound
	}
	return f.bindings[id], nil
}

func (f *fakeWorkspaceRepository) GetActiveWorkspaceBindingForProject(_ context.Context, ownerUserID, projectID string) (domain.WorkspaceBinding, error) {
	for _, binding := range f.bindings {
		if binding.OwnerUserID == ownerUserID && binding.ProjectID == projectID && binding.State == domain.WorkspaceBindingActive {
			return binding, nil
		}
	}
	return domain.WorkspaceBinding{}, domain.ErrNotFound
}

func (f *fakeWorkspaceRepository) ListWorkspaceBindings(_ context.Context, ownerUserID, projectID string, includeArchived bool) ([]domain.WorkspaceBinding, error) {
	var out []domain.WorkspaceBinding
	for _, binding := range f.bindings {
		if binding.OwnerUserID != ownerUserID || binding.ProjectID != projectID {
			continue
		}
		if !includeArchived && binding.State == domain.WorkspaceBindingArchived {
			continue
		}
		out = append(out, binding)
	}
	return out, nil
}

func (f *fakeWorkspaceRepository) UpdateWorkspaceAccess(_ context.Context, ownerUserID, bindingID string, readOnly bool, expectedRevision int64, _ time.Time) (domain.WorkspaceBinding, error) {
	binding, ok := f.bindings[bindingID]
	if !ok || binding.OwnerUserID != ownerUserID || binding.State != domain.WorkspaceBindingActive || binding.Revision != expectedRevision {
		return domain.WorkspaceBinding{}, domain.ErrNotFound
	}
	binding.ReadOnly = readOnly
	binding.Revision++
	f.bindings[bindingID] = binding
	return binding, nil
}

func (f *fakeWorkspaceRepository) ArchiveWorkspaceBinding(_ context.Context, ownerUserID, bindingID string, expectedRevision int64, now time.Time) (domain.WorkspaceBinding, error) {
	binding, ok := f.bindings[bindingID]
	if !ok || binding.OwnerUserID != ownerUserID || binding.State != domain.WorkspaceBindingActive || binding.Revision != expectedRevision {
		return domain.WorkspaceBinding{}, domain.ErrNotFound
	}
	binding.State = domain.WorkspaceBindingArchived
	binding.Revision++
	binding.ArchivedAt = &now
	f.bindings[bindingID] = binding
	return binding, nil
}

type fixedGenerator struct{ counter int }

func (g *fixedGenerator) New() string {
	g.counter++
	return "0199aaaa-aaaa-7aaa-8aaa-aaaaaaaaaa0" + string(rune('0'+g.counter%10))
}

func newWorkspaceService(directory ports.SourceDirectory) (*WorkspaceService, *fakeWorkspaceRepository) {
	repository := newFakeWorkspaceRepository()
	return NewWorkspaceService(repository, directory, &fixedGenerator{}), repository
}

func TestBindRejectsUnregisteredSource(t *testing.T) {
	service, _ := newWorkspaceService(&fakeDirectory{sources: []ports.WorkspaceSource{
		{ID: "ws_registered0001", DisplayName: "project", ReadOnly: false},
	}})
	if _, err := service.Bind(context.Background(), "0199aaaa-aaaa-7aaa-8aaa-aaaaaaaaaaaa", "0199bbbb-bbbb-7bbb-9bbb-bbbbbbbbbbbb", "ws_evil_client_id", "name", "key-1"); !errors.Is(err, domain.ErrWorkspaceSourceUnknown) {
		t.Fatalf("unregistered source accepted: %v", err)
	}
}

func TestBindPersistsReadOnlyFactAndReplaysIdempotently(t *testing.T) {
	service, repository := newWorkspaceService(&fakeDirectory{sources: []ports.WorkspaceSource{
		{ID: "ws_registered0001", DisplayName: "project", ReadOnly: true},
	}})
	owner := "0199aaaa-aaaa-7aaa-8aaa-aaaaaaaaaaaa"
	project := "0199bbbb-bbbb-7bbb-9bbb-bbbbbbbbbbbb"
	first, err := service.Bind(context.Background(), owner, project, "ws_registered0001", "dev tree", "key-1")
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	if !first.ReadOnly || first.Revision != 1 || first.State != domain.WorkspaceBindingActive {
		t.Fatalf("binding facts: %+v", first)
	}
	replay, err := service.Bind(context.Background(), owner, project, "ws_registered0001", "dev tree", "key-1")
	if err != nil || replay.ID != first.ID {
		t.Fatalf("replay: %v %+v", err, replay)
	}
	// Same key with different facts conflicts.
	if _, err := service.Bind(context.Background(), owner, project, "ws_registered0001", "renamed tree", "key-1"); !errors.Is(err, domain.ErrWorkspaceConflict) {
		t.Fatalf("conflict accepted: %v", err)
	}
	if len(repository.bindings) != 1 {
		t.Fatalf("replay created rows: %d", len(repository.bindings))
	}
}

func TestUpdateAccessAndArchiveEnforceRevision(t *testing.T) {
	service, _ := newWorkspaceService(&fakeDirectory{sources: []ports.WorkspaceSource{
		{ID: "ws_registered0001", DisplayName: "project"},
	}})
	owner := "0199aaaa-aaaa-7aaa-8aaa-aaaaaaaaaaaa"
	project := "0199bbbb-bbbb-7bbb-9bbb-bbbbbbbbbbbb"
	binding, err := service.Bind(context.Background(), owner, project, "ws_registered0001", "dev tree", "key-1")
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	if _, err := service.UpdateAccess(context.Background(), owner, binding.ID, true, 999); !errors.Is(err, domain.ErrWorkspaceRevision) {
		t.Fatalf("stale revision accepted: %v", err)
	}
	updated, err := service.UpdateAccess(context.Background(), owner, binding.ID, true, binding.Revision)
	if err != nil || !updated.ReadOnly || updated.Revision != 2 {
		t.Fatalf("update: %v %+v", err, updated)
	}
	archived, err := service.Archive(context.Background(), owner, binding.ID, updated.Revision)
	if err != nil || archived.State != domain.WorkspaceBindingArchived || archived.ArchivedAt == nil {
		t.Fatalf("archive: %v %+v", err, archived)
	}
	if _, err := service.ActiveForProject(context.Background(), owner, project); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("archived binding still active: %v", err)
	}
}

func TestBindRejectsMalformedInput(t *testing.T) {
	service, _ := newWorkspaceService(&fakeDirectory{sources: []ports.WorkspaceSource{
		{ID: "ws_registered0001"},
	}})
	owner := "0199aaaa-aaaa-7aaa-8aaa-aaaaaaaaaaaa"
	project := "0199bbbb-bbbb-7bbb-9bbb-bbbbbbbbbbbb"
	if _, err := service.Bind(context.Background(), owner, project, "../etc", "name", "key"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("path-like source id accepted: %v", err)
	}
	if _, err := service.Bind(context.Background(), owner, project, "ws_registered0001", "", "key"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("empty display name accepted: %v", err)
	}
}
