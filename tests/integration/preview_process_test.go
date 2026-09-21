//go:build integration

package integration_test

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yangtao121/workos/internal/platform/ids"
	"github.com/yangtao121/workos/internal/platform/migrations"
	"github.com/yangtao121/workos/internal/runtime/previewhost/adapters/dockerpreview"
	previewpostgres "github.com/yangtao121/workos/internal/runtime/previewhost/adapters/postgres"
	previewapp "github.com/yangtao121/workos/internal/runtime/previewhost/application"
	"github.com/yangtao121/workos/internal/runtime/previewhost/ports"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type previewGrant struct {
	root, owner, project string
	revoked              atomic.Bool
}

func (g *previewGrant) AuthorizeWorkspace(_ context.Context, owner, project string) (ports.WorkspaceGrant, error) {
	if owner != g.owner || project != g.project || g.revoked.Load() {
		return ports.WorkspaceGrant{}, errors.New("denied")
	}
	return ports.WorkspaceGrant{Directory: g.root, BindingID: "fixture", SourceID: "source", Revision: 1, Validate: func(context.Context) error {
		if g.revoked.Load() {
			return errors.New("revoked")
		}
		return nil
	}}, nil
}
func TestPreviewRealProcessContinuityAndRevocation(t *testing.T) {
	base := os.Getenv("WORKOS_WORKSPACE_TEST_ROOT")
	if base == "" {
		t.Skip("requires Docker and host-visible workspace")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	root, err := os.MkdirTemp(base, "preview-project-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	// This small server persists state into the exact project tree. The test
	// reads that file externally and proves the process survives new requests.
	source := `const http=require('http'),fs=require('fs');let count=0;http.createServer((req,res)=>{if(req.url==='/state'&&req.method==='POST'){count++;fs.writeFileSync('preview-state.txt',String(count));}res.setHeader('Content-Type','text/plain');res.end(JSON.stringify({pid:process.pid,count,secret:!!process.env.DEEPSEEK_API_KEY,socket:fs.existsSync('/var/run/docker.sock')}));}).listen(Number(process.env.PORT),'127.0.0.1');`
	if err := os.WriteFile(filepath.Join(root, "server.cjs"), []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	// The daemon accepts long mount paths, while Unix connect must use the
	// directory-fd path to avoid sockaddr_un's 108-byte limit in worktrees.
	bridgeRoot, err := os.MkdirTemp(base, "bridges-"+strings.Repeat("long", 25))
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(bridgeRoot)
	dsn := scratchDatabase(t)
	if err := migrations.Run(ctx, dsn); err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	gen := ids.UUIDv7{}
	grant := &previewGrant{root: root, owner: gen.New(), project: gen.New()}
	service, err := previewapp.New(previewpostgres.New(pool), grant, dockerpreview.New("/var/run/docker.sock", "workos-workspace-runtime:dev", bridgeRoot), gen)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	r, err := service.Start(ctx, grant.owner, grant.project, "create", "node server.cjs", 3000)
	if err != nil {
		t.Fatal(err)
	}
	if r.State != "running" || r.Generation != 1 {
		t.Fatalf("start facts: %+v", r)
	}
	request := ports.Request{Method: "POST", Path: "/state"}
	response, err := service.Request(ctx, r.PreviewID, r.AccessToken, request)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(response.Body), `"count":1`) || !strings.Contains(string(response.Body), `"secret":false,"socket":false`) {
		t.Fatalf("unexpected server facts: %s", response.Body)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "preview-state.txt")); string(data) != "1" {
		t.Fatal("server did not use shared workspace")
	}
	if _, err := service.Request(ctx, r.PreviewID, strings.Repeat("0", 64), request); err == nil {
		t.Fatal("invalid capability admitted")
	}
	if _, err := service.Get(ctx, gen.New(), r.PreviewID); err == nil {
		t.Fatal("foreign owner obtained preview capability")
	}
	again, err := service.Start(ctx, grant.owner, grant.project, "create", "node server.cjs", 3000)
	if err != nil || again.PreviewID != r.PreviewID {
		t.Fatal("start replay did not retain identity")
	}
	if _, err := service.Start(ctx, grant.owner, grant.project, "create", "node other.cjs", 3000); err == nil {
		t.Fatal("changed intent reused key")
	}
	// Simulate a new device obtaining the same preview after the first closed
	// its view: no process lifecycle command is sent on view detach.
	listed, err := service.List(ctx, grant.owner, grant.project)
	if err != nil || len(listed) != 1 || listed[0].Generation != 1 {
		t.Fatal("preview discovery failed")
	}
	response, err = service.Request(ctx, listed[0].PreviewID, listed[0].AccessToken, request)
	if err != nil || !strings.Contains(string(response.Body), `"count":2`) {
		t.Fatal("second device lost live process state")
	}
	if err := service.Stop(ctx, grant.owner, r.PreviewID, "stop"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Request(ctx, r.PreviewID, r.AccessToken, request); err == nil {
		t.Fatal("stopped process still served")
	}
	restarted, err := service.Restart(ctx, grant.owner, r.PreviewID, "restart")
	if err != nil || restarted.Generation != 2 {
		t.Fatalf("restart: %+v %v", restarted, err)
	}
	if err := service.Stop(ctx, grant.owner, r.PreviewID, "stop"); err != nil {
		t.Fatal(err)
	}
	response, err = service.Request(ctx, r.PreviewID, r.AccessToken, ports.Request{Method: "GET", Path: "/"})
	if err != nil || !strings.Contains(string(response.Body), `"count":0`) {
		t.Fatal("delayed stop affected replacement")
	}
	replay, err := service.Restart(ctx, grant.owner, r.PreviewID, "restart")
	if err != nil || replay.Generation != 2 {
		t.Fatal("restart replay launched again")
	}
	grant.revoked.Store(true)
	if err := service.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	state, err := service.Get(ctx, grant.owner, r.PreviewID)
	if err != nil || state.State != "failed" {
		t.Fatal("revocation did not terminate preview")
	}
	if _, err := service.Request(ctx, r.PreviewID, r.AccessToken, request); err == nil {
		t.Fatal("revoked preview served")
	}
}
