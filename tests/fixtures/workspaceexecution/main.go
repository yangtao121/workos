// Deterministic loopback-only Runtime fixture for native Harness probes.
// It uses the production execution service, durable journal and Docker engine.
package main

import (
	"context"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yangtao121/workos/internal/platform/config"
	"github.com/yangtao121/workos/internal/platform/migrations"
	"github.com/yangtao121/workos/internal/runtime/workspacehost/adapters/dockerexec"
	"github.com/yangtao121/workos/internal/runtime/workspacehost/adapters/localfs"
	workspacepostgres "github.com/yangtao121/workos/internal/runtime/workspacehost/adapters/postgres"
	"github.com/yangtao121/workos/internal/runtime/workspacehost/application"
	"github.com/yangtao121/workos/internal/runtime/workspacehost/transport"
	"log"
	"net/http"
	"os"
	"time"
)

func main() {
	ctx := context.Background()
	dsn := os.Getenv("WORKOS_TEST_DATABASE_URL")
	root := os.Getenv("WORKOS_WORKSPACE_TEST_ROOT")
	if dsn == "" || root == "" {
		log.Fatal("test database and workspace required")
	}
	if err := migrations.Run(ctx, dsn); err != nil {
		log.Fatal("fixture migrations failed")
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()
	sources, err := application.New(time.Now().UTC(), []config.WorkspaceMount{{OwnerUserID: "0198d7ea-2110-7c42-b659-c5e4d73bc111", ProjectID: "0198d7ea-2110-7c42-b659-c5e4d73bc112", RootPath: root}})
	if err != nil {
		log.Fatal(err)
	}
	source, _ := sources.Resolve("0198d7ea-2110-7c42-b659-c5e4d73bc111", "0198d7ea-2110-7c42-b659-c5e4d73bc112")
	service := application.NewExecution(sources, &localfs.Files{}, dockerexec.New("/var/run/docker.sock", "workos-workspace-runtime:dev"), workspacepostgres.New(pool))
	route, handler := transport.NewExecutionHandler(service)
	mux := http.NewServeMux()
	mux.Handle(route, handler)
	mux.HandleFunc("/source", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(source.ID)) })
	log.Print("workspace fixture listening on 127.0.0.1:18988")
	log.Fatal(http.ListenAndServe("127.0.0.1:18988", mux))
}
