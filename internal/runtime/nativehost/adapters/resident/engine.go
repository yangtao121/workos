// Package resident owns one isolated Docker child per Greenfield native
// workload. Trusted Go brokers media and input over a private Unix socket;
// Code, proxy, Chromium and Node never share Runtime's process/network space.
package resident

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yangtao121/workos/internal/platform/ids"
	"github.com/yangtao121/workos/internal/runtime/nativehost/domain"
	"github.com/yangtao121/workos/internal/runtime/nativehost/ports"
)

type Config struct {
	DockerSocket string
	Image        string
	IPCRoot      string
	RenderDevice string
	RenderGID    string
	GPUDriver    string
}

type Engine struct {
	config       Config
	docker       *dockerAPI
	mu           sync.Mutex
	count        int
	probeMu      sync.Mutex
	probeImageID string
	probeAt      time.Time
}

func New(config Config) *Engine {
	return &Engine{config: config, docker: newDockerAPI(config.DockerSocket)}
}

func (e *Engine) Facts() ports.EngineFacts {
	return ports.EngineFacts{Engine: "greenfield", CgroupIsolated: true, EnforcedLimits: []string{"container_memory", "container_pids", "container_cpu", "ipc_record_bytes"}}
}

func (e *Engine) Available(ctx context.Context) error {
	if err := e.baseAvailable(); err != nil {
		return err
	}
	imageID, err := e.docker.imageID(ctx, e.config.Image)
	if err != nil || !e.docker.hasRuntime(ctx, e.config.GPUDriver) {
		return domain.ErrEngineUnavailable
	}
	e.probeMu.Lock()
	defer e.probeMu.Unlock()
	if e.probeImageID == imageID && time.Since(e.probeAt) < 15*time.Second {
		return nil
	}
	probeCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if err := e.probeGPU(probeCtx); err != nil {
		return domain.ErrEngineUnavailable
	}
	e.probeImageID, e.probeAt = imageID, time.Now()
	return nil
}

// Probe the exact Docker GPU grants in a disposable, no-network child. The
// trusted Runtime container itself need not (and does not) mount /dev/dri.
func (e *Engine) probeGPU(ctx context.Context) error {
	dir, err := os.MkdirTemp(e.config.IPCRoot, "gpu-probe-")
	if err != nil {
		return domain.ErrEngineUnavailable
	}
	defer os.RemoveAll(dir)
	if err := os.Chmod(dir, 0700); err != nil {
		return domain.ErrEngineUnavailable
	}
	newID := ids.UUIDv7{}.New
	session := domain.Session{SessionID: newID(), OwnerUserID: newID(), ProjectID: newID(), Generation: 1,
		State: domain.StateQueued, Width: 320, Height: 240}
	spec := ports.ResidentLaunch{Session: session}
	config := e.childConfig(spec, dir)
	config["Entrypoint"] = []string{"/bin/sh", "-c"}
	config["Cmd"] = []string{"test -c /dev/dri/renderD128 && test -r /dev/dri/renderD128 && test -c /dev/nvidia0 && test -r /dev/nvidia0"}
	config["Labels"] = map[string]string{"workos.purpose": "greenfield-gpu-probe"}
	id, err := e.docker.create(ctx, "workos-gf-gpu-probe-"+strings.ReplaceAll(session.SessionID, "-", ""), config)
	if err != nil {
		return domain.ErrEngineUnavailable
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = e.docker.remove(cleanup, id)
	}()
	if err := e.docker.start(ctx, id); err != nil {
		return domain.ErrEngineUnavailable
	}
	return e.docker.waitStopped(ctx, id)
}

func (e *Engine) baseAvailable() error {
	c := e.config
	if c.DockerSocket == "" || c.Image == "" || c.IPCRoot == "" || c.RenderDevice != "/dev/dri/renderD128" || c.GPUDriver != "nvidia" {
		return domain.ErrEngineUnavailable
	}
	if gid, err := strconv.Atoi(c.RenderGID); err != nil || gid < 1 {
		return domain.ErrEngineUnavailable
	}
	if !filepath.IsAbs(c.IPCRoot) || filepath.Clean(c.IPCRoot) != c.IPCRoot || c.IPCRoot == "/" {
		return domain.ErrEngineUnavailable
	}
	if _, err := os.Stat(c.DockerSocket); err != nil {
		return domain.ErrEngineUnavailable
	}
	info, err := os.Lstat(c.IPCRoot)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return domain.ErrEngineUnavailable
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
	return func() { e.mu.Lock(); e.count--; e.mu.Unlock() }, nil
}

func (e *Engine) Launch(ctx context.Context, _, _ int32, _ string) (ports.Display, error) {
	return nil, domain.ErrEngineUnavailable
}

func validLaunch(spec ports.ResidentLaunch) bool {
	s := spec.Session
	return domain.ValidUUIDv7(s.SessionID) && domain.ValidUUIDv7(s.OwnerUserID) && domain.ValidUUIDv7(s.ProjectID) &&
		s.Generation >= 1 && domain.ValidSize(s.Width, s.Height) && !s.State.Terminal()
}

func validateWorkspace(root string) error {
	if root == "" {
		return nil
	}
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || root == "/" {
		return domain.ErrEngineUnavailable
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil || resolved != root {
		return domain.ErrEngineUnavailable
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return domain.ErrEngineUnavailable
	}
	return nil
}

func (e *Engine) prepareIPC(spec ports.ResidentLaunch) (string, *broker, error) {
	dir := cleanIPCDir(e.config.IPCRoot, spec.Session)
	if err := os.Mkdir(dir, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", nil, domain.ErrEngineUnavailable
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return "", nil, domain.ErrEngineUnavailable
	}
	b, err := newBroker(spec.Session.SessionID, spec.Session.Generation, dir)
	if err != nil {
		return "", nil, err
	}
	return dir, b, nil
}

func (e *Engine) LaunchResident(ctx context.Context, spec ports.ResidentLaunch) (ports.Display, error) {
	if !validLaunch(spec) || validateWorkspace(spec.Workspace.Directory) != nil {
		return nil, domain.ErrEngineUnavailable
	}
	if err := e.Available(ctx); err != nil {
		return nil, err
	}
	imageID, err := e.docker.imageID(ctx, e.config.Image)
	if err != nil {
		return nil, err
	}
	// A crashed create may have left an exact child without a DB binding.
	if adopted, err := e.AdoptResident(ctx, spec); err == nil {
		return adopted, nil
	} else if !errors.Is(err, domain.ErrResidentChildNotFound) {
		return nil, err
	}
	dir, b, err := e.prepareIPC(spec)
	if err != nil {
		return nil, err
	}
	defer func() {
		if b != nil {
			b.close()
		}
	}()
	name := childName(spec.Session)
	id, err := e.docker.create(ctx, name, e.childConfig(spec, dir))
	if errors.Is(err, apiStatusError(http.StatusConflict)) {
		b.close()
		return e.AdoptResident(ctx, spec)
	}
	if err != nil {
		return nil, domain.ErrEngineUnavailable
	}
	clean := func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = e.docker.remove(cleanup, id)
	}
	if err := e.docker.start(ctx, id); err != nil {
		clean()
		return nil, err
	}
	inspected, err := e.docker.inspect(ctx, id)
	if err != nil || e.verifyChild(spec, dir, imageID, inspected) != nil {
		clean()
		return nil, domain.ErrEngineUnavailable
	}
	readyCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := b.waitReady(readyCtx); err != nil {
		clean()
		return nil, domain.ErrEngineUnavailable
	}
	result := &display{engine: e, spec: spec, containerID: id, imageID: imageID, ipcDir: dir, broker: b}
	b = nil
	result.monitor()
	return result, nil
}

func (e *Engine) AdoptResident(ctx context.Context, spec ports.ResidentLaunch) (ports.Display, error) {
	if !validLaunch(spec) || validateWorkspace(spec.Workspace.Directory) != nil {
		return nil, domain.ErrEngineUnavailable
	}
	if err := e.baseAvailable(); err != nil {
		return nil, err
	}
	// A previous Runtime may have committed a new generation while retaining an
	// exact older child. Remove it only after full identity/profile verification.
	if oldGen := spec.Session.ChildGeneration; oldGen > 0 && oldGen < spec.Session.Generation {
		if err := e.ReapResident(ctx, spec); err != nil {
			return nil, err
		}
	}
	imageID := spec.Session.ChildImageID
	if spec.Session.ChildGeneration != spec.Session.Generation || imageID == "" {
		var err error
		imageID, err = e.docker.imageID(ctx, e.config.Image)
		if err != nil {
			return nil, err
		}
	}
	name := childName(spec.Session)
	inspected, err := e.docker.inspect(ctx, name)
	if err != nil {
		return nil, err
	}
	if spec.Session.ChildGeneration == spec.Session.Generation {
		if spec.Session.ChildContainerID != inspected.ID || spec.Session.ChildImageID != inspected.Image {
			return nil, domain.ErrEngineUnavailable
		}
	}
	dir := cleanIPCDir(e.config.IPCRoot, spec.Session)
	if err := e.verifyChild(spec, dir, imageID, inspected); err != nil {
		return nil, err
	}
	_, b, err := e.prepareIPC(spec)
	if err != nil {
		return nil, err
	}
	readyCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := b.waitReady(readyCtx); err != nil {
		b.close()
		return nil, domain.ErrEngineUnavailable
	}
	result := &display{engine: e, spec: spec, containerID: inspected.ID, imageID: imageID, ipcDir: dir, broker: b}
	result.monitor()
	return result, nil
}

// ReapResident handles lifecycle transitions when the previous Runtime broker
// is gone. A persisted container ID is always checked against the exact
// generation and security profile before Docker remove. An unbound child from
// a crash between create and BindChild is found only by its deterministic name
// and must match the current pinned image and profile.
func (e *Engine) ReapResident(ctx context.Context, spec ports.ResidentLaunch) error {
	s := spec.Session
	if !domain.ValidUUIDv7(s.SessionID) || !domain.ValidUUIDv7(s.OwnerUserID) || !domain.ValidUUIDv7(s.ProjectID) ||
		s.Generation < 1 || !domain.ValidSize(s.Width, s.Height) || validateWorkspace(spec.Workspace.Directory) != nil {
		return domain.ErrEngineUnavailable
	}
	if err := e.baseAvailable(); err != nil {
		return err
	}
	identity := spec.Session
	if identity.ChildGeneration > 0 {
		if identity.ChildGeneration > identity.Generation || identity.ChildContainerID == "" || identity.ChildImageID == "" {
			return domain.ErrEngineUnavailable
		}
		identity.Generation = identity.ChildGeneration
	}
	query := identity.ChildContainerID
	if query == "" {
		query = childName(identity)
	}
	child, err := e.docker.inspect(ctx, query)
	if errors.Is(err, domain.ErrResidentChildNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	imageID := identity.ChildImageID
	if imageID == "" {
		imageID, err = e.docker.imageID(ctx, e.config.Image)
		if err != nil {
			return domain.ErrEngineUnavailable
		}
	}
	if identity.ChildContainerID != "" && child.ID != identity.ChildContainerID {
		return domain.ErrEngineUnavailable
	}
	checked := spec
	checked.Session = identity
	// The child identity is durable but the original workspace grant is not.
	// For deletion only, derive the mount descriptor from this exact pinned
	// container's inspect result, then verify the complete allowed profile.
	// Adoption still uses a fresh Core grant and does not take this path.
	if checked.Workspace.Directory == "" {
		for _, mount := range child.Mounts {
			if mount.Destination != "/workspace" {
				continue
			}
			if mount.Type != "bind" || !filepath.IsAbs(mount.Source) || filepath.Clean(mount.Source) != mount.Source || mount.Source == "/" ||
				checked.Workspace.Directory != "" {
				return domain.ErrEngineUnavailable
			}
			checked.Workspace.Directory = mount.Source
			checked.Workspace.ReadOnly = !mount.RW
		}
	}
	if err := e.verifyChildProfile(checked, cleanIPCDir(e.config.IPCRoot, identity), imageID, child, false); err != nil {
		return err
	}
	return e.docker.remove(ctx, child.ID)
}

func sameChildIdentity(session domain.Session, child childInspect) bool {
	labels := childLabels(session)
	if child.ID == "" || strings.TrimPrefix(child.Name, "/") != childName(session) {
		return false
	}
	for key, expected := range labels {
		if child.Config.Labels[key] != expected {
			return false
		}
	}
	return true
}

type display struct {
	engine      *Engine
	spec        ports.ResidentLaunch
	containerID string
	imageID     string
	ipcDir      string
	broker      *broker
	mu          sync.Mutex
	stopped     bool
	failed      bool
}

func (d *display) ChildIdentity() ports.ChildIdentity {
	return ports.ChildIdentity{ContainerID: d.containerID, ImageID: d.imageID, Generation: d.spec.Session.Generation}
}
func (d *display) Connect(context.Context, string) (string, error) { return "", domain.ErrWrongEngine }
func (d *display) GuardInput(func() bool)                          {}
func (d *display) Detach()                                         {}

func (d *display) Exited() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.stopped || d.failed || d.broker.failed()
}

func (d *display) AroundInput(ctx context.Context, action func() error) error {
	return d.broker.aroundInput(ctx, action)
}

func (d *display) AroundControl(ctx context.Context, action func() error) error {
	return d.broker.aroundControl(ctx, action)
}

func (d *display) monitor() {
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for range ticker.C {
			d.mu.Lock()
			stopped := d.stopped
			d.mu.Unlock()
			if stopped {
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			child, err := d.engine.docker.inspect(ctx, d.containerID)
			cancel()
			if errors.Is(err, domain.ErrResidentChildNotFound) || (err == nil && (!child.State.Running || child.State.OOMKilled || !sameChildIdentity(d.spec.Session, child))) {
				d.mu.Lock()
				d.failed = true
				d.mu.Unlock()
				d.broker.fail(domain.ErrEngineUnavailable)
				return
			}
		}
	}()
}

func (d *display) PreserveForAdoption() {
	d.mu.Lock()
	if d.stopped {
		d.mu.Unlock()
		return
	}
	d.stopped = true
	d.mu.Unlock()
	d.broker.close()
}

func (d *display) Stop() {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_ = d.StopExact(ctx)
}

func (d *display) StopExact(ctx context.Context) error {
	d.mu.Lock()
	if d.stopped {
		d.mu.Unlock()
		return nil
	}
	d.mu.Unlock()
	child, err := d.engine.docker.inspect(ctx, d.containerID)
	if err == nil {
		if !sameChildIdentity(d.spec.Session, child) {
			return domain.ErrEngineUnavailable
		}
		if err := d.engine.docker.remove(ctx, d.containerID); err != nil {
			return err
		}
	} else if !errors.Is(err, domain.ErrResidentChildNotFound) {
		return err
	}
	d.mu.Lock()
	d.stopped = true
	d.mu.Unlock()
	d.broker.close()
	if err := os.RemoveAll(d.ipcDir); err != nil {
		return fmt.Errorf("remove resident IPC: %w", err)
	}
	return nil
}

var _ ports.ResidentEngine = (*Engine)(nil)
var _ ports.ResidentDisplay = (*display)(nil)
var _ ports.InputBarrier = (*display)(nil)
