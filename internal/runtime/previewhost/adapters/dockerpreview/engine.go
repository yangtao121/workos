// Package dockerpreview starts the development server without networking.
// A per-preview Unix socket forwards HTTP to its container-local TCP port.
package dockerpreview

import (
	"bytes"
	"context"
	"errors"
	"github.com/yangtao121/workos/internal/platform/containerprocess"
	"github.com/yangtao121/workos/internal/runtime/previewhost/domain"
	"github.com/yangtao121/workos/internal/runtime/previewhost/ports"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

// The private root must be mounted at the same absolute path in Runtime and
// the Docker daemon host. The project sees only its own bridge directory.
type Engine struct {
	client *containerprocess.Client
	root   string
}

func New(socket, image, root string) *Engine {
	return &Engine{containerprocess.New(socket, image), root}
}

// The bridge handles raw TCP so HTTP framing stays with net/http on each end.
const bridge = `const net=require('net');const fs=require('fs');const path='/bridge/http.sock';try{fs.unlinkSync(path)}catch{};net.createServer(c=>{const s=net.connect({host:'127.0.0.1',port:Number(process.env.PORT)});c.on('error',()=>s.destroy());s.on('error',()=>c.destroy());c.pipe(s);s.pipe(c);c.on('close',()=>s.destroy());s.on('close',()=>c.destroy())}).listen(path);`

func (e *Engine) Launch(ctx context.Context, r ports.PreviewRecord, g ports.WorkspaceGrant) (ports.Process, error) {
	if !filepath.IsAbs(e.root) {
		return nil, domain.ErrUnavailable
	}
	if err := os.MkdirAll(e.root, 0700); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(e.root, "preview-")
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			os.RemoveAll(dir)
		}
	}()
	// Command and bridge source travel as separate argv values, never interpolated.
	argv := []string{"/bin/bash", "--noprofile", "--norc", "-c", `node -e "$1" & bridge=$!; /bin/bash --noprofile --norc -c "$2"; result=$?; kill "$bridge" 2>/dev/null; exit "$result"`, "workos-preview", bridge, r.Command}
	process, err := e.client.Start(ctx, containerprocess.Spec{ID: r.PreviewID, Workspace: g.Directory, ReadOnly: g.ReadOnly, Argv: argv, Environment: []string{"PORT=" + strconv.Itoa(int(r.Port)), "WORKOS_PREVIEW_BASE=/previews/" + r.PreviewID + "/" + r.AccessToken + "/"}, ExtraMounts: []string{dir + ":/bridge:rw"}, Lifetime: domain.PreviewTTL})
	if err != nil {
		return nil, err
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		// Linux sockaddr_un is limited to 108 bytes. Worktrees can make the
		// host bridge path longer even though the container's /bridge path is
		// short. Resolve through an open directory fd without a global symlink
		// or a shared short-path directory; keep it open until connect returns.
		directory, err := os.Open(dir)
		if err != nil {
			return nil, err
		}
		defer directory.Close()
		socket := filepath.Join("/proc/self/fd", strconv.FormatUint(uint64(directory.Fd()), 10), "http.sock")
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}, MaxIdleConns: 4, MaxConnsPerHost: 8, ResponseHeaderTimeout: 10 * time.Second}
	p := &server{process: process, transport: transport, client: &http.Client{Transport: transport, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, directory: dir}
	ready, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if p.Exited() {
			p.Stop()
			return nil, domain.ErrUnavailable
		}
		if response, err := p.Request(ready, ports.Request{Method: "GET", Path: "/", Headers: map[string]string{}}); err == nil && response.Status > 0 {
			ok = true
			return p, nil
		}
		select {
		case <-ready.Done():
			p.Stop()
			return nil, domain.ErrUnavailable
		case <-ticker.C:
		}
	}
}

type server struct {
	process   *containerprocess.Process
	transport *http.Transport
	client    *http.Client
	directory string
	once      sync.Once
}

func (p *server) Exited() bool {
	select {
	case <-p.process.Done():
		return true
	default:
		return false
	}
}
func (p *server) Stop() {
	p.once.Do(func() { p.process.Stop(); p.transport.CloseIdleConnections(); os.RemoveAll(p.directory) })
}
func (p *server) Request(ctx context.Context, r ports.Request) (ports.Response, error) {
	target := "http://preview" + r.Path
	if r.Query != "" {
		target += "?" + r.Query
	}
	request, err := http.NewRequestWithContext(ctx, r.Method, target, bytes.NewReader(r.Body))
	if err != nil {
		return ports.Response{}, domain.ErrInvalid
	}
	for key, value := range r.Headers {
		request.Header.Set(key, value)
	}
	response, err := p.client.Do(request)
	if err != nil {
		return ports.Response{}, domain.ErrUnavailable
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, domain.MaxPreviewFileBytes+1))
	if err != nil || len(body) > domain.MaxPreviewFileBytes {
		return ports.Response{}, domain.ErrFileLimit
	}
	if response.StatusCode < 200 || response.StatusCode > 599 {
		return ports.Response{}, errors.New("invalid preview response")
	}
	headers := map[string]string{}
	for _, name := range []string{"Content-Type", "ETag", "Last-Modified", "Location"} {
		if value := response.Header.Get(name); value != "" {
			headers[name] = value
		}
	}
	return ports.Response{Status: response.StatusCode, Headers: headers, Body: body}, nil
}
