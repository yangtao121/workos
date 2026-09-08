//go:build integration

package integration_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	projectpostgres "github.com/yangtao121/workos/internal/core/project/adapters/postgres"
	projectapp "github.com/yangtao121/workos/internal/core/project/application"
	projectdomain "github.com/yangtao121/workos/internal/core/project/domain"
	"github.com/yangtao121/workos/internal/platform/migrations"
)

func TestRepairTargetReadsOwnedActiveInstallationSnapshot(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	dsn := scratchDatabase(t)
	if err := migrations.Run(ctx, dsn); err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	id := func() string { return uuid.Must(uuid.NewV7()).String() }
	owner, project, installation := id(), id(), id()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO workos_core.users(id,kind,display_name,created_at) VALUES($1,'owner','Repair target fixture',now())`, owner)
	exec(`INSERT INTO workos_core.projects(id,owner_user_id,idempotency_key,name,knowledge_collection_id,artifact_collection_id,revision,created_at,updated_at) VALUES($1,$2,'repair-target','Repair target',$3,$4,2,now(),now())`, project, owner, id(), id())
	digest := "sha256:" + strings.Repeat("a", 64)
	exec(`INSERT INTO workos_core.project_app_installations(id,owner_user_id,project_id,app_id,version,manifest_digest,installed_at) VALUES($1,$2,$3,'repair-app','1.0.0',$4,now())`, installation, owner, project, digest)
	service, err := projectapp.NewRepairTargets(projectpostgres.New(pool))
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.Get(ctx, owner, project, installation)
	if err != nil || first.Installation.Version != "1.0.0" || first.Installation.ManifestDigest != digest || first.ProjectRevision != 2 {
		t.Fatalf("initial target: %+v %v", first, err)
	}
	// The revision and version change in one transaction. Readers must never
	// combine facts from either side of that commit.
	done := make(chan error, 1)
	go func() {
		for i := 0; i < 40; i++ {
			tx, err := pool.Begin(ctx)
			if err != nil {
				done <- err
				return
			}
			_, err = tx.Exec(ctx, `UPDATE workos_core.projects SET revision=$2 WHERE id=$1`, project, 3+i)
			if err == nil {
				_, err = tx.Exec(ctx, `UPDATE workos_core.project_app_installations SET version=$2 WHERE id=$1`, installation, []string{"2.0.0", "1.0.0"}[i%2])
			}
			if err == nil {
				err = tx.Commit(ctx)
			} else {
				_ = tx.Rollback(ctx)
			}
			if err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	for range 80 {
		target, err := service.Get(ctx, owner, project, installation)
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"1.0.0", "2.0.0"}[target.ProjectRevision%2]
		if target.Installation.Version != want {
			t.Fatalf("torn project/installation snapshot: %+v", target)
		}
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	for _, args := range [][3]string{{id(), project, installation}, {owner, id(), installation}, {owner, project, id()}} {
		if _, err := service.Get(ctx, args[0], args[1], args[2]); !errors.Is(err, projectdomain.ErrNotFound) {
			t.Fatalf("foreign target read: %v", err)
		}
	}
	exec(`UPDATE workos_core.projects SET archived_at=now() WHERE id=$1`, project)
	if _, err := service.Get(ctx, owner, project, installation); !errors.Is(err, projectdomain.ErrNotFound) {
		t.Fatalf("archived target read: %v", err)
	}
	exec(`UPDATE workos_core.projects SET archived_at=NULL WHERE id=$1`, project)
	exec(`UPDATE workos_core.project_app_installations SET uninstalled_at=now() WHERE id=$1`, installation)
	if _, err := service.Get(ctx, owner, project, installation); !errors.Is(err, projectdomain.ErrNotFound) {
		t.Fatalf("uninstalled target read: %v", err)
	}
}
