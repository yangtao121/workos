package application

import (
	"context"
	"testing"
	"time"

	"github.com/yangtao121/workos/internal/platform/config"
)

func newMounts() []config.WorkspaceMount {
	return []config.WorkspaceMount{
		{OwnerUserID: "0199aaaa-aaaa-7aaa-8aaa-aaaaaaaaaaaa", ProjectID: "0199bbbb-bbbb-7bbb-9bbb-bbbbbbbbbbbb", RootPath: "/tmp/alice-project"},
		{OwnerUserID: "0199cccc-cccc-7ccc-8ccc-cccccccccccc", ProjectID: "0199bbbb-bbbb-7bbb-9bbb-bbbbbbbbbbbb", RootPath: "/tmp/bob-project", ReadOnly: true},
	}
}

func TestSourcesAreOwnerScoped(t *testing.T) {
	service, err := New(time.Now().UTC(), newMounts())
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	alice := service.Sources("0199aaaa-aaaa-7aaa-8aaa-aaaaaaaaaaaa")
	if len(alice) != 1 || alice[0].DisplayName != "alice-project" || alice[0].ReadOnly {
		t.Fatalf("alice sources: %+v", alice)
	}
	bob := service.Sources("0199cccc-cccc-7ccc-8ccc-cccccccccccc")
	if len(bob) != 1 || !bob[0].ReadOnly {
		t.Fatalf("bob sources: %+v", bob)
	}
	if len(service.Sources("0199dddd-dddd-7ddd-8ddd-dddddddddddd")) != 0 {
		t.Fatal("foreign owner saw sources")
	}
	if alice[0].ID == bob[0].ID {
		t.Fatal("distinct scopes produced identical source ids")
	}
}

func TestResolveAndPrepareScopeBound(t *testing.T) {
	now := time.Now().UTC()
	service, err := New(now, newMounts())
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	alice := "0199aaaa-aaaa-7aaa-8aaa-aaaaaaaaaaaa"
	project := "0199bbbb-bbbb-7bbb-9bbb-bbbbbbbbbbbb"
	source, ok := service.Resolve(alice, project)
	if !ok || source.Path != "/tmp/alice-project" {
		t.Fatalf("resolve: %+v ok=%v", source, ok)
	}
	// A foreign owner never resolves another owner's directory.
	if _, ok := service.Resolve("0199cccc-cccc-7ccc-8ccc-cccccccccccc", project); !ok {
		// bob has his own binding for the same project id; that is his
		// directory, not alice's.
		bobSource, bobOK := service.Resolve("0199cccc-cccc-7ccc-8ccc-cccccccccccc", project)
		if !bobOK || bobSource.Path != "/tmp/bob-project" {
			t.Fatalf("bob resolve: %+v ok=%v", bobSource, bobOK)
		}
	}
	environment, err := service.Prepare(context.Background(), alice, project, "terminal", now)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if environment.SourceID != source.ID || environment.ReadOnly {
		t.Fatalf("environment: %+v", environment)
	}
	if !environment.ExpiresAt.After(environment.PreparedAt) {
		t.Fatal("environment has no validity window")
	}
	if _, err := service.Prepare(context.Background(), alice, "0199eeee-eeee-7eee-8eee-eeeeeeeeeeee", "terminal", now); err == nil {
		t.Fatal("prepared an unbound project")
	}
	if _, err := service.Prepare(context.Background(), alice, project, "webhook", now); err == nil {
		t.Fatal("prepared an unknown consumer")
	}
}

func TestNewRejectsOverlappingAndRelativeRoots(t *testing.T) {
	now := time.Now().UTC()
	if _, err := New(now, []config.WorkspaceMount{{OwnerUserID: "a", ProjectID: "b", RootPath: "relative/path"}}); err == nil {
		t.Fatal("relative root accepted")
	}
	if _, err := New(now, []config.WorkspaceMount{
		{OwnerUserID: "a", ProjectID: "b", RootPath: "/tmp/x"},
		{OwnerUserID: "a", ProjectID: "c", RootPath: "/tmp/x/sub"},
	}); err == nil {
		t.Fatal("nested roots accepted")
	}
}
