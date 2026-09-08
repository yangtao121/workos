// Package chromiumengine launches and drives real headless Chromium workers
// (ADR-0027). One child process per session: fixed flags, private profile
// directory, process group with parent-death signal, bounded restarts and
// a wall-clock watchdog. Page control and screencast frames arrive over the
// Chrome DevTools Protocol; no user-provided flag is ever passed through.
package chromiumengine

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/yangtao121/workos/internal/runtime/browserpool/ports"
)

const (
	devtoolsPrefix    = "DevTools listening on "
	startupTimeout    = 20 * time.Second
	maxSessionWorkers = 4
)

// Engine facts are reported honestly: process-level bounds only, no kernel
// memory or cgroup isolation claim on hosts without delegation.
type Engine struct {
	binary      string
	ScratchRoot string

	mu       sync.Mutex
	sessions int
}

// Facts reports the enforced bounds honestly: process-level only; no
// cgroup isolation claim without a delegated hierarchy.
func (e *Engine) Facts() ports.EngineFacts {
	return ports.EngineFacts{
		Engine:           "chromium",
		ProcessGroupKill: true,
		ParentDeathSig:   true,
		CgroupIsolated:   false,
		EnforcedLimits:   []string{"process-group-kill", "parent-death-signal", "concurrent-sessions", "session-ttl"},
	}
}

// LaunchWorker implements the pool's Launcher port.
func (e *Engine) LaunchWorker(ctx context.Context) (ports.Worker, string, error) {
	worker, err := e.launchProcess(ctx)
	if err != nil {
		return nil, "", err
	}
	return &cdpWorker{worker: worker, conn: newCDP(worker.DevtoolsURL)}, worker.DevtoolsURL, nil
}

// cdpWorker couples the child process with its CDP page connection.
type cdpWorker struct {
	worker *Worker
	conn   *cdpConn
}

// Diagnostics returns the child's recent stderr tail (bounded, sanitized to
// Chromium's own messages).
func (w *cdpWorker) Diagnostics() string { return w.worker.tail.String() }

func (w *cdpWorker) Navigate(ctx context.Context, url string) error {
	if err := w.conn.openPage(ctx); err != nil {
		return err
	}
	return w.conn.Navigate(ctx, url)
}

func (w *cdpWorker) Screenshot(ctx context.Context) ([]byte, error) {
	return w.conn.Screenshot(ctx)
}

func (w *cdpWorker) Exited() bool {
	// The process reap can lag behind reality: chromium's stderr pipe stays
	// open in surviving grandchildren, so cmd.Wait blocks. The page
	// websocket dying is the authoritative worker-death signal.
	return w.worker.Exited() || w.conn.Dead()
}

func (w *cdpWorker) Stop() {
	w.conn.Close()
	w.worker.Stop()
}

func New(binary, scratchRoot string) (*Engine, error) {
	if binary == "" || !filepath.IsAbs(binary) {
		return nil, errors.New("chromium engine requires an absolute binary path")
	}
	if scratchRoot == "" || !filepath.IsAbs(scratchRoot) {
		return nil, errors.New("chromium engine requires an absolute scratch root")
	}
	return &Engine{binary: binary, ScratchRoot: scratchRoot}, nil
}

// Available verifies the engine can launch: the binary exists and is
// executable and the scratch root is usable. It never fabricates capability.
func (e *Engine) Available(ctx context.Context) error {
	info, err := os.Stat(e.binary)
	if err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
		return errors.New("browser engine is unavailable")
	}
	if info, err := os.Stat(e.ScratchRoot); err == nil {
		if !info.IsDir() {
			return errors.New("browser engine is unavailable")
		}
		return nil
	}
	return os.MkdirAll(e.ScratchRoot, 0o700)
}

// Reserve acquires one of the bounded concurrent worker slots.
func (e *Engine) Reserve() (func(), error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.sessions >= maxSessionWorkers {
		return nil, errors.New("browser session limit reached")
	}
	e.sessions++
	var released bool
	return func() {
		if released {
			return
		}
		released = true
		e.mu.Lock()
		e.sessions--
		e.mu.Unlock()
	}, nil
}

// boundedTail keeps the last few stderr lines for failure diagnosis.
type boundedTail struct {
	mu    sync.Mutex
	lines []string
}

func (t *boundedTail) append(line string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lines = append(t.lines, line)
	if len(t.lines) > 8 {
		t.lines = t.lines[len(t.lines)-8:]
	}
}

func (t *boundedTail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.Join(t.lines, " | ")
}

// Worker is one launched Chromium child with its CDP endpoint.
type Worker struct {
	DevtoolsURL string
	ProfileDir  string
	cmd         *exec.Cmd
	cancel      context.CancelFunc
	done        chan struct{}
	tail        *boundedTail
}

// Exited reports whether the child process has been reaped. A monitor
// goroutine owns the single Wait call; the done channel makes exit visible
// without racing the reaper.
func (w *Worker) Exited() bool {
	if w == nil || w.done == nil {
		return true
	}
	select {
	case <-w.done:
		return true
	default:
		return false
	}
}

// Launch starts one headless Chromium with fixed flags and waits for the
// DevTools websocket endpoint on stderr. The profile directory is private
// and removed on Stop.
func (e *Engine) launchProcess(ctx context.Context) (*Worker, error) {
	if err := e.Available(ctx); err != nil {
		return nil, err
	}
	profile, err := os.MkdirTemp(e.ScratchRoot, "browser-")
	if err != nil {
		return nil, fmt.Errorf("browser profile: %w", err)
	}
	// The worker outlives the admitting request: its lifetime belongs to the
	// session (Stop/Sweep), never to the RPC that launched it.
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	cmd := exec.CommandContext(runCtx, e.binary,
		"--headless=new",
		"--no-sandbox",
		"--disable-gpu",
		"--disable-dev-shm-usage",
		"--no-first-run",
		"--disable-extensions",
		"--disable-background-networking",
		"--remote-debugging-port=0",
		"--user-data-dir="+profile,
		"--window-size=1280,800",
		"about:blank",
	)
	cmd.Env = []string{"HOME=" + profile, "PATH=/usr/local/bin:/usr/bin:/bin", "LANG=C.UTF-8", "TZ=UTC"}
	tail := &boundedTail{}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	cmd.Cancel = func() error { return killGroup(cmd) }
	cmd.WaitDelay = time.Second
	cmd.Stdin = nil
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		_ = os.RemoveAll(profile)
		return nil, fmt.Errorf("browser stderr: %w", err)
	}
	if err := cmd.Start(); err != nil {
		cancel()
		_ = os.RemoveAll(profile)
		return nil, fmt.Errorf("start browser: %w", err)
	}
	devtools := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			line := scanner.Text()
			tail.append(line)
			if strings.HasPrefix(line, devtoolsPrefix) {
				select {
				case devtools <- strings.TrimSpace(strings.TrimPrefix(line, devtoolsPrefix)):
				default:
				}
			}
		}
	}()
	select {
	case url := <-devtools:
		done := make(chan struct{})
		go func() {
			_ = cmd.Wait()
			close(done)
		}()
		return &Worker{DevtoolsURL: url, ProfileDir: profile, cmd: cmd, cancel: cancel, done: done, tail: tail}, nil
	case <-time.After(startupTimeout):
		_ = killGroup(cmd)
		_ = cmd.Wait()
		cancel()
		_ = os.RemoveAll(profile)
		return nil, errors.New("browser engine did not expose its devtools endpoint")
	case <-ctx.Done():
		_ = killGroup(cmd)
		_ = cmd.Wait()
		cancel()
		_ = os.RemoveAll(profile)
		return nil, ctx.Err()
	}
}

// Stop kills the process group, waits for the monitor's reap, and removes
// the private profile directory.
func (w *Worker) Stop() {
	if w == nil {
		return
	}
	_ = killGroup(w.cmd)
	if w.done != nil {
		select {
		case <-w.done:
		case <-time.After(5 * time.Second):
		}
	}
	w.cancel()
	_ = os.RemoveAll(w.ProfileDir)
}

func killGroup(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return os.ErrProcessDone
	}
	err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return os.ErrProcessDone
	}
	return err
}
