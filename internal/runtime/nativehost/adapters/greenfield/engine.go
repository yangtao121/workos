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
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/yangtao121/workos/internal/runtime/nativehost/domain"
	"github.com/yangtao121/workos/internal/runtime/nativehost/ports"
)

// Engine launches one proxy process group per display.
type Engine struct {
	Proxy   string
	App     string
	AppArgs []string
	Scratch string
	// RenderDevice is the DRM render node used by Greenfield's EGL path.
	RenderDevice string
	PublicOrigin string

	mu    sync.Mutex
	count int
	// testEnv is set only by this package's process fixture tests.
	testEnv []string
}

func New(proxy, app, scratch string) *Engine {
	return &Engine{
		Proxy: proxy, App: app, Scratch: scratch, RenderDevice: renderDevice(),
		// Official Electron cannot open an X11 window in this container without these flags.
		AppArgs: []string{"--ozone-platform=x11", "--disable-gpu", "--no-sandbox"},
	}
}

func renderDevice() string {
	if path := strings.TrimSpace(os.Getenv("WORKOS_RUNTIME_NATIVE_RENDER_DEVICE")); path != "" {
		return path
	}
	return "/dev/dri/renderD128"
}

func (e *Engine) WithPublicOrigin(origin string) *Engine {
	e.PublicOrigin = origin
	return e
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
	device, err := os.Stat(e.RenderDevice)
	if err != nil {
		return fmt.Errorf("greenfield render device: %w", err)
	}
	if device.Mode()&os.ModeCharDevice == 0 {
		return errors.New("greenfield render device is not a character device")
	}
	fd, err := os.OpenFile(e.RenderDevice, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("greenfield render device access: %w", err)
	}
	_ = fd.Close()
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
	return e.LaunchSessionLifecycle(ctx, "", width, height, workingDirectory, false, domain.LifecycleManualStop)
}

func (e *Engine) LaunchWorkspace(ctx context.Context, width, height int32, workingDirectory string, readOnly bool) (ports.Display, error) {
	return e.LaunchSessionLifecycle(ctx, "", width, height, workingDirectory, readOnly, domain.LifecycleManualStop)
}

func (e *Engine) LaunchLifecycle(ctx context.Context, width, height int32, workingDirectory string, _ bool, _ domain.LifecycleMode) (ports.Display, error) {
	return e.LaunchSessionLifecycle(ctx, "", width, height, workingDirectory, false, domain.LifecycleManualStop)
}

// LaunchSessionLifecycle gives the proxy its final public base URL before any
// Wayland client starts. Greenfield embeds that URL in WebSocket frames and
// file-descriptor messages, so rewriting HTTP response bodies cannot fix it.
func (e *Engine) LaunchSessionLifecycle(ctx context.Context, sessionID string, width, height int32, workingDirectory string, _ bool, _ domain.LifecycleMode) (ports.Display, error) {
	if err := e.Available(ctx); err != nil {
		return nil, err
	}
	if sessionID != "" {
		if !domain.ValidUUIDv7(sessionID) {
			return nil, domain.ErrInvalid
		}
		if _, err := e.publicBaseURL(sessionID); err != nil {
			return nil, err
		}
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
	proxyName, proxyArgs := e.proxyCommand(port, appsPath, sessionID)
	cmd := exec.Command(proxyName, proxyArgs...)
	cmd.Env = e.processEnv(dir)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	if err := cmd.Start(); err != nil {
		logFile.Close()
		return nil, err
	}
	compositorSession := sessionID
	if compositorSession == "" {
		compositorSession = "workos"
	}
	baseURL := "ws://127.0.0.1:" + strconv.Itoa(port)
	if sessionID != "" {
		baseURL, _ = e.publicBaseURL(sessionID)
	}
	d := &display{
		cmd: cmd, port: port, width: width, height: height, dir: dir,
		compositorSession: compositorSession, publicBaseURL: baseURL,
	}
	d.localBase = fmt.Sprintf("127.0.0.1:%d", port)
	if err := waitListen(ctx, logPath); err != nil {
		d.Stop()
		return nil, err
	}
	pid, key, err := launchApp(ctx, port, compositorSession)
	if err != nil {
		d.Stop()
		return nil, err
	}
	if err := checkRenderInitialization(logPath); err != nil {
		d.Stop()
		return nil, err
	}
	d.appPID = pid
	d.key = key
	return d, nil
}

// The proxy launches Code as its child. Never pass Runtime's DB URL, bridge
// credentials, or service tokens to either process through inherited env.
// This is defense in depth; a production deployment also needs a separate
// container boundary because Code shares this experimental image with Runtime.
func (e *Engine) processEnv(dir string) []string {
	env := []string{
		"PATH=/usr/local/bin:/usr/bin:/bin",
		"HOME=" + dir,
		"XDG_RUNTIME_DIR=" + dir,
		"XDG_CACHE_HOME=" + filepath.Join(dir, "cache"),
		"XDG_CONFIG_HOME=" + filepath.Join(dir, "config"),
		"LANG=C.UTF-8",
		"ELECTRON_OZONE_PLATFORM_HINT=x11",
	}
	// These variables select graphics libraries injected by the container
	// runtime. Their names are fixed; WORKOS_* and generic service env are not.
	for _, name := range []string{
		"LD_LIBRARY_PATH", "__EGL_VENDOR_LIBRARY_FILENAMES", "__GLX_VENDOR_LIBRARY_NAME",
		"GBM_BACKEND", "LIBVA_DRIVER_NAME", "VK_ICD_FILENAMES", "NVIDIA_VISIBLE_DEVICES",
		"NVIDIA_DRIVER_CAPABILITIES", "FONTCONFIG_FILE", "FONTCONFIG_PATH",
	} {
		if value, ok := os.LookupEnv(name); ok {
			env = append(env, name+"="+value)
		}
	}
	return append(env, e.testEnv...)
}

func (e *Engine) proxyCommand(port int, appsPath, sessionID string) (string, []string) {
	baseURL := "ws://127.0.0.1:" + strconv.Itoa(port)
	if sessionID != "" {
		baseURL, _ = e.publicBaseURL(sessionID)
	}
	args := []string{
		"--bind-ip", "127.0.0.1",
		"--bind-port", strconv.Itoa(port),
		"--allow-origin", "http://localhost",
		"--base-url", baseURL,
		"--encoder", "x264",
		"--render-device", e.RenderDevice,
		"--applications", appsPath,
	}
	if strings.HasSuffix(e.Proxy, ".js") {
		return "node", append([]string{e.Proxy}, args...)
	}
	return e.Proxy, args
}

func (e *Engine) publicBaseURL(sessionID string) (string, error) {
	origin, err := url.Parse(e.PublicOrigin)
	if err != nil || origin == nil || (origin.Scheme != "http" && origin.Scheme != "https") || origin.Host == "" || origin.User != nil || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" {
		return "", errors.New("greenfield public origin must be a canonical http(s) origin")
	}
	if origin.Scheme == "https" {
		origin.Scheme = "wss"
	} else {
		origin.Scheme = "ws"
	}
	origin.Path = proxyPrefix + sessionID
	return origin.String(), nil
}

func checkRenderInitialization(logPath string) error {
	raw, err := os.ReadFile(logPath)
	if err != nil {
		return err
	}
	if strings.Contains(string(raw), "Failed to initialize EGL") || strings.Contains(string(raw), "Can't initialize EGL") {
		return errors.New("greenfield EGL initialization failed; check render device and GPU driver access")
	}
	return nil
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

func launchApp(ctx context.Context, port int, compositorSession string) (int, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:"+strconv.Itoa(port)+"/code", nil)
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("x-compositor-session-id", compositorSession)
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
	ownerUserID        string
	controllerDeviceID string
	localBase          string
	compositorSession  string
	publicBaseURL      string
	stopped            bool
	inputGate          func() bool
}

func (d *display) Connect(context.Context, string) (string, error) {
	return "", domain.ErrWrongEngine
}

func (d *display) GuardInput(gate func() bool) { d.mu.Lock(); d.inputGate = gate; d.mu.Unlock() }

// BindController replaces the device and lease gate atomically. The proxy
// consults this gate for each browser frame, including on already open sockets.
func (d *display) BindController(deviceID string, authorized func() bool) {
	d.mu.Lock()
	d.controllerDeviceID = deviceID
	d.inputGate = authorized
	d.mu.Unlock()
}

func (d *display) canProxy(deviceID string) bool {
	d.mu.Lock()
	allowed := !d.stopped && deviceID != "" && deviceID == d.controllerDeviceID
	gate := d.inputGate
	d.mu.Unlock()
	return allowed && gate != nil && gate()
}

func (d *display) Detach() {
	// A browser connection is transient; only the proxy's WebSocket lifecycle
	// changes. Runtime Stop owns the Code process lifetime.
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
func (d *display) BindSession(id, ownerUserID string) {
	d.mu.Lock()
	d.sessionID = id
	d.ownerUserID = ownerUserID
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
