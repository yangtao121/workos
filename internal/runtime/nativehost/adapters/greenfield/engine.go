// Package greenfield supervises a compositor-proxy and one native client
// (ADR-0038). The browser is only a temporary compositor connection. Closing
// that connection does not stop the client. SDP is rejected.
package greenfield

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/yangtao121/workos/internal/runtime/nativehost/domain"
	"github.com/yangtao121/workos/internal/runtime/nativehost/ports"
)

const clipboardMax = domain.MaxClipboardBytes

// Engine launches one proxy process group per display.
type Engine struct {
	Proxy   string
	App     string
	AppArgs []string
	Scratch string

	mu    sync.Mutex
	count int
}

func New(proxy, app, scratch string) *Engine {
	return &Engine{Proxy: proxy, App: app, Scratch: scratch}
}

func (e *Engine) Facts() ports.EngineFacts {
	return ports.EngineFacts{
		Engine:           "greenfield",
		ProcessGroupKill: true,
		ParentDeathSig:   true,
		CgroupIsolated:   false,
		EnforcedLimits:   []string{"clipboard_bytes"},
	}
}

func (e *Engine) Available(context.Context) error {
	if e.Proxy == "" || e.App == "" || e.Scratch == "" {
		return errors.New("greenfield proxy, app, or scratch is not configured")
	}
	if _, err := os.Stat(e.Proxy); err != nil {
		return fmt.Errorf("greenfield proxy: %w", err)
	}
	if _, err := os.Stat(e.App); err != nil {
		return fmt.Errorf("greenfield app: %w", err)
	}
	return nil
}

func (e *Engine) Reserve() (func(), error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.count >= 4 {
		return nil, domain.ErrSessionLimit
	}
	e.count++
	return func() {
		e.mu.Lock()
		e.count--
		e.mu.Unlock()
	}, nil
}

func (e *Engine) Launch(ctx context.Context, width, height int32, workingDirectory string) (ports.Display, error) {
	return e.LaunchLifecycle(ctx, width, height, workingDirectory, false, domain.LifecycleManualStop)
}

func (e *Engine) LaunchWorkspace(ctx context.Context, width, height int32, workingDirectory string, readOnly bool) (ports.Display, error) {
	return e.LaunchLifecycle(ctx, width, height, workingDirectory, readOnly, domain.LifecycleManualStop)
}

func (e *Engine) LaunchLifecycle(ctx context.Context, width, height int32, workingDirectory string, _ bool, _ domain.LifecycleMode) (ports.Display, error) {
	if err := e.Available(ctx); err != nil {
		return nil, err
	}
	port, err := freePort()
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(e.Scratch, "display-")
	if err != nil {
		return nil, err
	}
	apps := map[string]map[string]any{
		"/code": {
			"name":       "Code",
			"executable": e.App,
			"args":       e.AppArgs,
			"env":        map[string]string{"HOME": dir},
		},
	}
	if workingDirectory != "" {
		apps["/code"]["args"] = append(append([]string{}, e.AppArgs...), workingDirectory)
	}
	raw, err := json.Marshal(apps)
	if err != nil {
		return nil, err
	}
	appsPath := filepath.Join(dir, "apps.json")
	if err := os.WriteFile(appsPath, raw, 0o600); err != nil {
		return nil, err
	}
	logPath := filepath.Join(dir, "proxy.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(e.Proxy, "--bind-ip", "127.0.0.1", "--bind-port", strconv.Itoa(port), "--allow-origin", "http://localhost", "--base-url", "ws://127.0.0.1:"+strconv.Itoa(port), "--encoder", "x264", "--applications", appsPath)
	cmd.Env = append(os.Environ(), "RENDERER_ALLOW_SOFTWARE=1")
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	if err := cmd.Start(); err != nil {
		logFile.Close()
		return nil, err
	}
	d := &display{
		cmd: cmd, port: port, width: width, height: height, dir: dir,
		clipboardMax: clipboardMax, compositorSession: "workos",
	}
	d.localBase = fmt.Sprintf("127.0.0.1:%d", port)
	if err := waitListen(ctx, logPath); err != nil {
		d.Stop()
		return nil, err
	}
	pid, key, err := launchApp(ctx, port)
	if err != nil {
		d.Stop()
		return nil, err
	}
	d.appPID = pid
	d.key = key
	return d, nil
}

func freePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	return port, ln.Close()
}

func waitListen(ctx context.Context, logPath string) error {
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		raw, err := os.ReadFile(logPath)
		if err == nil && (strings.Contains(string(raw), "Listening on") || strings.Contains(string(raw), "listening")) {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return errors.New("greenfield proxy did not listen")
}

func launchApp(ctx context.Context, port int) (int, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:"+strconv.Itoa(port)+"/code", nil)
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("x-compositor-session-id", "workos")
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return 0, "", fmt.Errorf("greenfield launch status %d", resp.StatusCode)
	}
	var body struct {
		PID string `json:"pid"`
		Key string `json:"key"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return 0, "", err
	}
	pid, err := strconv.Atoi(body.PID)
	if err != nil || pid <= 0 {
		return 0, "", errors.New("greenfield launch returned no pid")
	}
	return pid, body.Key, nil
}

type display struct {
	mu                 sync.Mutex
	cmd                *exec.Cmd
	port               int
	width, height      int32
	dprMillis          int32
	dir                string
	appPID             int
	key                string
	sessionID          string
	localBase          string
	compositorSession  string
	clipboard          string
	clipboardMax       int
	clipboardConnected bool
	stopped            bool
	inputGate          func() bool
}

func (d *display) Connect(context.Context, string) (string, error) {
	return "", domain.ErrWrongEngine
}

func (d *display) GuardInput(gate func() bool) { d.mu.Lock(); d.inputGate = gate; d.mu.Unlock() }

func (d *display) Detach() {
	d.mu.Lock()
	d.clipboardConnected = false
	d.clipboard = ""
	d.mu.Unlock()
}

func (d *display) Exited() bool {
	if d.stopped || d.appPID <= 0 {
		return true
	}
	return syscall.Kill(d.appPID, 0) != nil
}

func (d *display) Stop() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stopped {
		return
	}
	d.stopped = true
	d.clipboard = ""
	d.clipboardConnected = false
	if d.cmd != nil && d.cmd.Process != nil {
		_ = syscall.Kill(-d.cmd.Process.Pid, syscall.SIGKILL)
		_ = syscall.Kill(d.appPID, syscall.SIGKILL)
		_, _ = d.cmd.Process.Wait()
	}
	if d.sessionID != "" {
		unregisterSession(d.sessionID)
	}
	if d.dir != "" {
		_ = os.RemoveAll(d.dir)
	}
}

// BindSession publishes this display on the runtime-local proxy path.
func (d *display) BindSession(id string) {
	d.mu.Lock()
	d.sessionID = id
	d.mu.Unlock()
	registerSession(id, d)
}

// Endpoint is the runtime-local websocket the gateway must proxy.
func (d *display) Endpoint(dprMillis int32) (string, string, int32, int32, int32, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stopped || d.Exited() {
		return "", "", 0, 0, 0, domain.ErrEngineUnavailable
	}
	if dprMillis != 0 {
		d.dprMillis = dprMillis
	}
	if d.sessionID == "" {
		return "", "", 0, 0, 0, domain.ErrEngineUnavailable
	}
	path := "/native/greenfield/" + d.sessionID + "/code"
	return path, d.compositorSession, d.width, d.height, d.dprMillis, nil
}

func (d *display) WriteClipboard(text string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stopped || !d.clipboardConnected {
		return domain.ErrClipboardDisconnected
	}
	if len(text) > d.clipboardMax {
		return domain.ErrClipboardTooLarge
	}
	d.clipboard = text
	return nil
}

func (d *display) ReadClipboard() (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stopped || !d.clipboardConnected {
		return "", domain.ErrClipboardDisconnected
	}
	return d.clipboard, nil
}

func (d *display) AttachClipboard() {
	d.mu.Lock()
	d.clipboardConnected = true
	d.mu.Unlock()
}
