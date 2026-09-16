package dockerpty

import (
	"context"
	"github.com/yangtao121/workos/internal/platform/containerprocess"
	"github.com/yangtao121/workos/internal/platform/ids"
	"github.com/yangtao121/workos/internal/runtime/ptyhost/domain"
	"github.com/yangtao121/workos/internal/runtime/ptyhost/ports"
	"sync"
)

type Engine struct {
	client *containerprocess.Client
	slots  chan struct{}
}

func New(socket, image string) *Engine {
	return &Engine{containerprocess.New(socket, image), make(chan struct{}, 4)}
}
func (e *Engine) Facts() ports.EngineFacts {
	return ports.EngineFacts{Engine: "docker-workspace-pty", ProcessGroupKill: true, EnforcedLimits: []string{"container-filesystem", "no-network", "readonly-root", "pids", "memory", "cpu", "output-ring", "session-ttl"}}
}
func (e *Engine) Available(ctx context.Context) error { return e.client.Available(ctx) }
func (e *Engine) Reserve() (func(), error) {
	select {
	case e.slots <- struct{}{}:
		var once sync.Once
		return func() { once.Do(func() { <-e.slots }) }, nil
	default:
		return nil, domain.ErrSessionLimit
	}
}
func (e *Engine) Launch(ctx context.Context, columns, rows int32, root string) (ports.Terminal, error) {
	return e.LaunchWorkspace(ctx, columns, rows, root, false)
}
func (e *Engine) LaunchWorkspace(ctx context.Context, columns, rows int32, root string, readOnly bool) (ports.Terminal, error) {
	if !domain.ValidSize(columns, rows) {
		return nil, domain.ErrInvalid
	}
	process, err := e.client.Start(ctx, containerprocess.Spec{ID: (ids.UUIDv7{}).New(), Workspace: root, ReadOnly: readOnly, Tty: true, Argv: []string{"/bin/bash", "--noprofile", "--norc"}, Lifetime: domain.SessionTTL})
	if err != nil {
		return nil, domain.ErrEngineUnavailable
	}
	if err := process.Resize(ctx, columns, rows); err != nil {
		process.Stop()
		return nil, domain.ErrEngineUnavailable
	}
	terminal := &terminal{process: process}
	go terminal.drain()
	return terminal, nil
}

type terminal struct {
	process *containerprocess.Process
	mu      sync.Mutex
	ring    []byte
	cursor  int64
}

func (t *terminal) drain() {
	buffer := make([]byte, 8192)
	for {
		n, err := t.process.Read(buffer)
		if n > 0 {
			t.mu.Lock()
			t.cursor += int64(n)
			t.ring = append(t.ring, buffer[:n]...)
			if len(t.ring) > 256*1024 {
				t.ring = t.ring[len(t.ring)-256*1024:]
			}
			t.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}
func (t *terminal) Write(ctx context.Context, input []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err := t.process.Write(input)
	return err
}
func (t *terminal) Read(ctx context.Context, after int64, maxBytes int32) (int64, []byte, error) {
	if err := ctx.Err(); err != nil {
		return 0, nil, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	start := t.cursor - int64(len(t.ring))
	if after < start {
		after = start
	}
	if after > t.cursor {
		return 0, nil, domain.ErrInvalid
	}
	if maxBytes <= 0 || maxBytes > domain.MaxReadBytes {
		maxBytes = domain.MaxReadBytes
	}
	offset := int(after - start)
	length := min(len(t.ring)-offset, int(maxBytes))
	return after + int64(length), append([]byte{}, t.ring[offset:offset+length]...), nil
}
func (t *terminal) Resize(ctx context.Context, columns, rows int32) error {
	return t.process.Resize(ctx, columns, rows)
}
func (t *terminal) Exited() bool {
	select {
	case <-t.process.Done():
		return true
	default:
		return false
	}
}
func (t *terminal) Stop() { t.process.Stop() }
