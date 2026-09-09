// Package shellexec supervises real login-shell children on a pty
// (ADR-0028): one process group per session, bounded environment, parent
// death signal, bounded output ring.
package shellexec

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"

	"github.com/yangtao121/workos/internal/runtime/ptyhost/domain"
	"github.com/yangtao121/workos/internal/runtime/ptyhost/ports"
)

const (
	outputRingBytes   = 256 * 1024
	drainInterval     = 50 * time.Millisecond
	maxSessionWorkers = 4
)

// Engine facts: process-level bounds only; no cgroup or namespace claim.
type Engine struct {
	Shell string
	mu    sync.Mutex
	count int
}

func New(shell string) (*Engine, error) {
	if shell == "" {
		shell = "/bin/sh"
	}
	info, err := os.Stat(shell)
	if err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
		return nil, fmt.Errorf("pty shell %q is not executable", shell)
	}
	return &Engine{Shell: shell}, nil
}

func (e *Engine) Facts() ports.EngineFacts {
	return ports.EngineFacts{
		Engine:           "login-shell",
		ProcessGroupKill: true,
		ParentDeathSig:   true,
		EnforcedLimits:   []string{"process-group-kill", "parent-death-signal", "output-ring", "session-ttl"},
	}
}

func (e *Engine) Available(ctx context.Context) error {
	info, err := os.Stat(e.Shell)
	if err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
		return domain.ErrEngineUnavailable
	}
	return nil
}

func (e *Engine) Reserve() (func(), error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.count >= maxSessionWorkers {
		return nil, domain.ErrSessionLimit
	}
	e.count++
	var released bool
	return func() {
		if released {
			return
		}
		released = true
		e.mu.Lock()
		e.count--
		e.mu.Unlock()
	}, nil
}

// terminal couples the pty master with a bounded output ring.
type terminal struct {
	cmd     *exec.Cmd
	ptmx    *os.File
	cancel  context.CancelFunc
	done    chan struct{}
	mu      sync.Mutex
	ring    []byte
	cursor  int64
	dropped int64
	logger  *slog.Logger
}

func (e *Engine) Launch(ctx context.Context, columns, rows int32) (ports.Terminal, error) {
	if !domain.ValidSize(columns, rows) {
		return nil, domain.ErrInvalid
	}
	// The child outlives the admitting request; the done-channel monitor
	// below releases the run context when the shell is reaped, so no path
	// leaks it. Stop/Sweep still own the process lifetime.
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	cmd := exec.CommandContext(runCtx, e.Shell)
	cmd.Env = []string{"HOME=/tmp", "PATH=/usr/local/bin:/usr/bin:/bin", "LANG=C.UTF-8", "TERM=xterm-256color", "PS1=$ "}
	// The pty library's start path adds Setsid+Setctty; Setpgid here would
	// make the child its own group leader first and setsid would EPERM.
	// After setsid the child still leads its own process group, so group
	// kills below remain effective.
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		return os.ErrProcessDone
	}
	cmd.WaitDelay = time.Second
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(columns), Rows: uint16(rows)})
	if err != nil {
		cancel()
		return nil, fmt.Errorf("start pty: %w", err)
	}
	t := &terminal{cmd: cmd, ptmx: ptmx, cancel: cancel, done: make(chan struct{}), logger: slog.Default()}
	go func() {
		_ = cmd.Wait()
		cancel()
		close(t.done)
	}()
	go t.drain()
	return t, nil
}

// drain continuously reads the pty into the bounded ring until EOF.
func (t *terminal) drain() {
	buffer := make([]byte, 8192)
	for {
		n, err := t.ptmx.Read(buffer)
		if n > 0 {
			t.mu.Lock()
			t.cursor += int64(n)
			t.ring = append(t.ring, buffer[:n]...)
			if excess := len(t.ring) - outputRingBytes; excess > 0 {
				t.ring = t.ring[excess:]
				t.dropped += int64(excess)
			}
			t.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

func (t *terminal) Write(ctx context.Context, input []byte) error {
	if len(input) == 0 {
		return nil
	}
	_, err := t.ptmx.Write(input)
	if err != nil {
		return domain.ErrEngineUnavailable
	}
	return nil
}

// Read returns output strictly after the cursor. Unknown (too old) cursors
// return the current ring start; callers observe the skip via the cursor.
func (t *terminal) Read(ctx context.Context, after int64, maxBytes int32) (int64, []byte, error) {
	if maxBytes <= 0 || maxBytes > domain.MaxReadBytes {
		maxBytes = domain.MaxReadBytes
	}
	t.mu.Lock()
	ringStart := t.cursor - int64(len(t.ring))
	if after < ringStart {
		after = ringStart
	}
	offset := int(after - ringStart)
	if offset > len(t.ring) {
		offset = len(t.ring)
	}
	chunk := t.ring[offset:]
	if int32(len(chunk)) > maxBytes {
		chunk = chunk[:maxBytes]
	}
	out := make([]byte, len(chunk))
	copy(out, chunk)
	cursor := after + int64(len(out))
	t.mu.Unlock()
	return cursor, out, nil
}

func (t *terminal) Resize(ctx context.Context, columns, rows int32) error {
	if !domain.ValidSize(columns, rows) {
		return domain.ErrInvalid
	}
	if err := pty.Setsize(t.ptmx, &pty.Winsize{Cols: uint16(columns), Rows: uint16(rows)}); err != nil {
		return domain.ErrEngineUnavailable
	}
	return syscall.Kill(t.cmd.Process.Pid, syscall.SIGWINCH)
}

func (t *terminal) Exited() bool {
	select {
	case <-t.done:
		return true
	default:
		return false
	}
}

func (t *terminal) Stop() {
	if t.cmd.Process != nil {
		_ = syscall.Kill(-t.cmd.Process.Pid, syscall.SIGKILL)
	}
	select {
	case <-t.done:
	case <-time.After(3 * time.Second):
	}
	_ = t.ptmx.Close()
	t.cancel()
}
