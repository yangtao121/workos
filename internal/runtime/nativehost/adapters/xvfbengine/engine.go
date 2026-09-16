// Package xvfbengine supervises real virtual-display children (ADR-0029):
// per session one Xvfb display, one configured native X client, and one
// ffmpeg x11grab/VP8 capture whose IVF frames feed the WebRTC video track.
// Input events arrive on the workos.input data channel and map to xdotool
// XTEST injection. The default topology is loopback-only host candidates; the
// operator-configured "lan" candidate mode enumerates real host LAN
// interfaces instead (ADR-0031 §5) — never STUN/TURN.
package xvfbengine

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/yangtao121/workos/internal/platform/containerprocess"
	"github.com/yangtao121/workos/internal/platform/ids"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"

	"github.com/yangtao121/workos/internal/runtime/nativehost/domain"
	"github.com/yangtao121/workos/internal/runtime/nativehost/ports"
)

const (
	maxDisplays        = 4
	displayBase        = 60
	displaySpan        = 200
	socketWait         = 5 * time.Second
	inputQueueDepth    = 256
	xdotoolTimeout     = 2 * time.Second
	peerGatherTimeout  = 8 * time.Second
	maxIVFFramePayload = 2 * 1024 * 1024
)

// Candidate modes (ADR-0031 §5). Loopback is the fail-safe default; "lan"
// enumerates the host's real LAN interfaces for same-LAN devices. The mode is
// operator configuration, never a client request.
const (
	CandidatesLoopback = "loopback"
	CandidatesLAN      = "lan"
)

// Engine facts: process-level supervision only; no cgroup or namespace
// isolation claim, no display auth beyond the single-uid Unix socket, and no
// TURN relay. The enforced candidate scope follows the operator mode.
type Engine struct {
	containers       *containerprocess.Client
	x11HostDirectory string
	Xvfb             string
	Client           []string
	FFmpeg           string
	Xdotool          string
	Scratch          string
	// Candidates is "loopback" (default) or "lan" — the ICE candidate
	// policy of every peer this engine builds.
	Candidates     string
	LANNetworks    []*net.IPNet
	UDPMin, UDPMax uint16

	launchMu sync.Mutex
	mu       sync.Mutex
	count    int
}

// candidatePolicy returns the ICE IP filter and whether loopback candidates
// are force-included. Loopback mode admits loopback IPs only and includes
// the loopback candidate explicitly; lan mode drops the filter so the host's
// real interfaces are enumerable and does not force loopback inclusion.
func (e *Engine) candidatePolicy() (filter func(ip net.IP) bool, includeLoopback bool) {
	if e.Candidates == CandidatesLAN {
		return func(ip net.IP) bool {
			if ip.IsLoopback() || !ip.IsPrivate() {
				return false
			}
			for _, network := range e.LANNetworks {
				if network.Contains(ip) {
					return true
				}
			}
			return false
		}, false
	}
	return func(ip net.IP) bool { return ip.IsLoopback() }, true
}

// resolveExecutable accepts an absolute path or a PATH-relative binary name
// and returns the resolved executable path, or "" when it cannot run.
func resolveExecutable(name string) string {
	if name == "" {
		return ""
	}
	if path, err := exec.LookPath(name); err == nil {
		return path
	}
	return ""
}

func New(xvfb, client, ffmpeg, xdotool, scratch, candidates string) (*Engine, error) {
	argv := strings.Fields(client)
	if xvfb == "" || ffmpeg == "" || xdotool == "" || len(argv) == 0 || scratch == "" {
		return nil, errors.New("native engine requires xvfb, client argv, ffmpeg, xdotool and scratch")
	}
	switch candidates {
	case "":
		candidates = CandidatesLoopback
	case CandidatesLoopback, CandidatesLAN:
	default:
		return nil, fmt.Errorf("native candidate mode must be %q or %q", CandidatesLoopback, CandidatesLAN)
	}
	resolvedXvfb := resolveExecutable(xvfb)
	resolvedFFmpeg := resolveExecutable(ffmpeg)
	resolvedXdotool := resolveExecutable(xdotool)
	resolvedClient := resolveExecutable(argv[0])
	if resolvedXvfb == "" || resolvedFFmpeg == "" || resolvedXdotool == "" || resolvedClient == "" {
		return nil, fmt.Errorf("native engine toolchain is not executable (xvfb=%q client=%q ffmpeg=%q xdotool=%q)", xvfb, argv[0], ffmpeg, xdotool)
	}
	argv[0] = resolvedClient
	return &Engine{Xvfb: resolvedXvfb, Client: argv, FFmpeg: resolvedFFmpeg, Xdotool: resolvedXdotool, Scratch: scratch, Candidates: candidates}, nil
}

// WithLAN requires explicit private CIDRs and a bounded UDP range. Empty or
// public ranges fail closed; neither browser requests nor discovered public
// interfaces can widen this operator boundary.
func (e *Engine) WithLAN(cidrs, portsRange string) error {
	if e.Candidates != CandidatesLAN {
		return nil
	}
	parts := strings.Split(portsRange, "-")
	if len(parts) != 2 {
		return errors.New("native LAN requires UDP min-max")
	}
	min, err := strconv.ParseUint(parts[0], 10, 16)
	if err != nil {
		return err
	}
	max, err := strconv.ParseUint(parts[1], 10, 16)
	if err != nil || min < 1024 || max < min || max-min > 1023 {
		return errors.New("invalid native UDP range")
	}
	var networks []*net.IPNet
	for _, entry := range strings.Split(cidrs, ",") {
		ip, network, err := net.ParseCIDR(strings.TrimSpace(entry))
		if err != nil || !ip.IsPrivate() {
			return errors.New("native LAN requires private CIDRs")
		}
		last := append(net.IP(nil), network.IP...)
		for i := range last {
			last[i] |= ^network.Mask[i]
		}
		if !network.IP.IsPrivate() || !last.IsPrivate() {
			return errors.New("native LAN CIDR extends outside private space")
		}
		networks = append(networks, network)
	}
	if len(networks) == 0 {
		return errors.New("native LAN allowlist empty")
	}
	e.LANNetworks, e.UDPMin, e.UDPMax = networks, uint16(min), uint16(max)
	return nil
}

func (e *Engine) Facts() ports.EngineFacts {
	candidateScope := "loopback-host-candidates-only"
	if e.Candidates == CandidatesLAN {
		candidateScope = "lan-host-candidates"
	}
	return ports.EngineFacts{
		Engine:           "xvfb-x11grab-vp8-webrtc",
		ProcessGroupKill: true,
		ParentDeathSig:   true,
		EnforcedLimits:   []string{"process-group-kill", "parent-death-signal", "input-rate", "session-ttl", candidateScope, "peer-authorization-ttl-30s"},
	}
}

func (e *Engine) WithContainers(socket, image, x11HostDirectory string) *Engine {
	e.containers = containerprocess.New(socket, image)
	e.x11HostDirectory = x11HostDirectory
	return e
}
func (e *Engine) Available(ctx context.Context) error {
	if e.containers != nil {
		if !filepath.IsAbs(e.x11HostDirectory) {
			return domain.ErrEngineUnavailable
		}
		if err := e.containers.Available(ctx); err != nil {
			return domain.ErrEngineUnavailable
		}
	}
	for _, name := range []string{e.Xvfb, e.FFmpeg, e.Xdotool, e.Client[0]} {
		if resolveExecutable(name) == "" {
			return domain.ErrEngineUnavailable
		}
	}
	if err := os.MkdirAll(e.Scratch, 0o700); err != nil {
		return domain.ErrEngineUnavailable
	}
	return nil
}

func (e *Engine) Reserve() (func(), error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.count >= maxDisplays {
		return nil, domain.ErrSessionLimit
	}
	e.count++
	var once sync.Once
	return func() { once.Do(func() { e.mu.Lock(); defer e.mu.Unlock(); e.count-- }) }, nil
}

type queuedInput struct {
	raw   json.RawMessage
	epoch uint64
}

type sampleSink func(frame []byte, duration time.Duration)

// display couples the supervised children with at most one live WebRTC peer.
type display struct {
	container   *containerprocess.Process
	engine      *Engine
	dir         string
	displayName string
	width       int32
	height      int32

	xvfbCmd    *exec.Cmd
	clientCm   *exec.Cmd
	ffmpegCmd  *exec.Cmd
	cancel     context.CancelFunc
	exited     chan struct{}
	stopOnce   sync.Once
	runCtx     context.Context
	children   sync.WaitGroup
	inputDone  chan struct{}
	frameReady chan struct{}
	readyOnce  sync.Once

	sinkMu sync.Mutex
	sink   sampleSink

	peerMu    sync.Mutex
	peer      *webrtc.PeerConnection
	peerEpoch uint64
	// inputGate is the per-event control gate (ADR-0031 §4), guarded by
	// peerMu like the peer it belongs to. nil admits events (no continuity
	// enforcement bound).
	inputGate func() bool

	inputMu      sync.Mutex
	inputTokens  float64
	inputStamp   time.Time
	inputQueue   chan queuedInput
	inputDropped int64
}

func (e *Engine) Launch(ctx context.Context, width, height int32, workingDirectory string) (ports.Display, error) {
	return e.LaunchWorkspace(ctx, width, height, workingDirectory, false)
}
func (e *Engine) LaunchWorkspace(ctx context.Context, width, height int32, workingDirectory string, readOnly bool) (ports.Display, error) {
	if (readOnly || workingDirectory != "") && e.containers == nil {
		return nil, domain.ErrEngineUnavailable
	}
	if !domain.ValidSize(width, height) {
		return nil, domain.ErrInvalid
	}
	if workingDirectory != "" {
		info, err := os.Stat(workingDirectory)
		if err != nil || !info.IsDir() {
			return nil, domain.ErrInvalid
		}
	}
	e.launchMu.Lock()
	defer e.launchMu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(e.Scratch, "native-")
	if err != nil {
		return nil, err
	}
	runCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), domain.SessionTTL)
	d := &display{engine: e, dir: dir, width: width, height: height,
		runCtx: runCtx, cancel: cancel, exited: make(chan struct{}), inputDone: make(chan struct{}), frameReady: make(chan struct{}),
		inputQueue:  make(chan queuedInput, inputQueueDepth),
		inputTokens: float64(domain.InputBurst), inputStamp: time.Now()}
	launched := false
	defer func() {
		if !launched {
			d.Stop()
		}
	}()
	number, err := freeDisplayNumber()
	if err != nil {
		return nil, err
	}
	name := ":" + strconv.Itoa(number)
	d.displayName = name
	xvfb := exec.Command(e.Xvfb, name, "-screen", "0", fmt.Sprintf("%dx%dx24", width, height), "-nolisten", "tcp", "-ac")
	// No runtime secrets in any of the display children's environments.
	env := []string{"DISPLAY=" + name, "HOME=" + dir, "PATH=/usr/local/bin:/usr/bin:/bin", "LANG=C.UTF-8", "XDG_RUNTIME_DIR=" + dir}
	xvfb.Env = env
	xvfb.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	d.xvfbCmd = xvfb
	if err := xvfb.Start(); err != nil {
		return nil, fmt.Errorf("start xvfb: %w", err)
	}
	xvfbDone := make(chan struct{})
	d.children.Add(1)
	go func() { defer d.children.Done(); _ = xvfb.Wait(); close(xvfbDone) }()
	socket := filepath.Join("/tmp", ".X11-unix", "X"+strconv.Itoa(number))
	if err := waitForSocket(ctx, socket, xvfbDone); err != nil {
		return nil, err
	}

	clientDone := make(chan struct{})
	if e.containers != nil {
		if !filepath.IsAbs(e.x11HostDirectory) {
			return nil, domain.ErrEngineUnavailable
		}
		container, err := e.containers.Start(ctx, containerprocess.Spec{ID: (ids.UUIDv7{}).New(), Workspace: workingDirectory, ReadOnly: readOnly, Argv: e.Client, Environment: []string{"DISPLAY=" + name}, ExtraMounts: []string{filepath.Join(e.x11HostDirectory, filepath.Base(socket)) + ":" + socket + ":ro"}, Lifetime: domain.SessionTTL})
		if err != nil {
			return nil, domain.ErrEngineUnavailable
		}
		d.container = container
		d.children.Add(1)
		go func() { defer d.children.Done(); <-container.Done(); close(clientDone) }()
	} else {
		client := exec.Command(e.Client[0], e.Client[1:]...)
		client.Dir = workingDirectory
		client.Env = env
		client.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pgid: xvfb.Process.Pid, Pdeathsig: syscall.SIGKILL}
		d.clientCm = client
		if err := client.Start(); err != nil {
			return nil, fmt.Errorf("start native client: %w", err)
		}
		d.children.Add(1)
		go func() { defer d.children.Done(); _ = client.Wait(); close(clientDone) }()
	}
	if err := waitForClientWindow(ctx, env, e.Xdotool, clientDone); err != nil {
		return nil, err
	}
	ffmpeg := exec.CommandContext(runCtx, e.FFmpeg, "-nostdin", "-loglevel", "error",
		"-f", "x11grab", "-draw_mouse", "1", "-video_size", fmt.Sprintf("%dx%d", width, height),
		"-framerate", strconv.Itoa(domain.FrameRate), "-i", name,
		"-c:v", "libvpx", "-deadline", "realtime", "-cpu-used", "8", "-b:v", "800k", "-g", "20",
		"-pix_fmt", "yuv420p", "-vf", "scale=trunc(iw/2)*2:trunc(ih/2)*2", "-f", "ivf", "pipe:1")
	ffmpeg.Env = env
	ffmpeg.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pgid: xvfb.Process.Pid, Pdeathsig: syscall.SIGKILL}
	ffmpeg.WaitDelay = time.Second
	d.ffmpegCmd = ffmpeg
	stdout, err := ffmpeg.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := ffmpeg.Start(); err != nil {
		_ = stdout.Close()
		return nil, fmt.Errorf("start ffmpeg: %w", err)
	}
	ffmpegDone := make(chan struct{})
	d.children.Add(1)
	go func() {
		defer d.children.Done()
		// Drain the pipe before Wait: Wait closes StdoutPipe and must not race the reader.
		d.readIVF(bufio.NewReaderSize(stdout, 64*1024))
		_ = ffmpeg.Process.Kill()
		_ = ffmpeg.Wait()
		close(ffmpegDone)
	}()
	d.children.Add(1)
	go func() {
		defer d.children.Done()
		defer close(d.inputDone)
		for {
			select {
			case <-runCtx.Done():
				return
			case event := <-d.inputQueue:
				if runCtx.Err() != nil {
					return
				}
				d.peerMu.Lock()
				// The control gate is re-consulted at APPLY time: queued
				// events of a superseded controller die here too, not only
				// at enqueue (ADR-0031 §4).
				if event.epoch == d.peerEpoch && !d.Exited() && d.inputAllowed() {
					d.applyInput(event.raw)
				}
				d.peerMu.Unlock()
			}
		}
	}()
	// Any child exit terminates the entire session; an empty X root is not a running app.
	go func() {
		select {
		case <-xvfbDone:
		case <-clientDone:
		case <-ffmpegDone:
		case <-runCtx.Done():
		}
		d.Stop()
	}()
	select {
	case <-d.frameReady:
		if d.Exited() {
			return nil, domain.ErrEngineUnavailable
		}
		launched = true
		return d, nil
	case <-ctx.Done():
		d.Stop()
		return nil, ctx.Err()
	case <-d.exited:
		return nil, domain.ErrEngineUnavailable
	case <-time.After(socketWait):
		d.Stop()
		return nil, domain.ErrEngineUnavailable
	}
}

func (e *Engine) EngineClient() string { return e.Client[0] }

func freeDisplayNumber() (int, error) {
	for attempt := 0; attempt < displaySpan; attempt++ {
		number := displayBase + int(time.Now().UnixNano()+int64(attempt))%displaySpan
		if number < displayBase {
			number += displayBase
		}
		if _, err := os.Stat(filepath.Join("/tmp", ".X11-unix", "X"+strconv.Itoa(number))); os.IsNotExist(err) {
			return number, nil
		}
	}
	return 0, errors.New("no free display number")
}

func waitForSocket(ctx context.Context, socket string, exited <-chan struct{}) error {
	timer := time.NewTimer(socketWait)
	defer timer.Stop()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-exited:
			return errors.New("xvfb exited during startup")
		case <-timer.C:
			return errors.New("xvfb socket did not appear")
		case <-ticker.C:
			if _, err := os.Stat(socket); err == nil {
				return nil
			}
		}
	}
}

func waitForClientWindow(ctx context.Context, env []string, xdotool string, exited <-chan struct{}) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			return errors.New("native client window did not appear")
		case <-exited:
			return errors.New("native client exited during startup")
		default:
		}
		probeCtx, probeCancel := context.WithTimeout(ctx, time.Second)
		probe := exec.CommandContext(probeCtx, xdotool, "search", "--onlyvisible", "--name", ".")
		probe.Env = env
		output, err := probe.Output()
		probeCancel()
		if err == nil {
			windowID := strings.TrimSpace(strings.SplitN(string(output), "\n", 2)[0])
			if windowID != "" {
				focus := exec.CommandContext(ctx, xdotool, "windowfocus", "--sync", windowID)
				focus.Env = env
				if err := focus.Run(); err != nil {
					return errors.New("native client focus failed")
				}
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func (d *display) Exited() bool {
	select {
	case <-d.exited:
		return true
	default:
		return false
	}
}

func (d *display) Stop() {
	d.stopOnce.Do(func() {
		close(d.exited)
		d.cancel()
		if d.container != nil {
			d.container.Stop()
		}
		if d.xvfbCmd != nil && d.xvfbCmd.Process != nil {
			_ = syscall.Kill(-d.xvfbCmd.Process.Pid, syscall.SIGKILL)
		}
		d.peerMu.Lock()
		peer := d.peer
		d.peer = nil
		d.sinkMu.Lock()
		d.sink = nil
		d.sinkMu.Unlock()
		d.peerMu.Unlock()
		if peer != nil {
			_ = peer.Close()
		}
		d.children.Wait()
		_ = os.RemoveAll(d.dir)
	})
}

// readIVF parses the ffmpeg IVF stream and pushes each VP8 frame into the
// current sink. Before the first Connect frames go to a no-op sink, so the
// capture pipeline is warm when signaling completes.
func (d *display) readIVF(reader *bufio.Reader) {
	header := make([]byte, 32)
	if _, err := ioReadFull(reader, header); err != nil {
		return
	}
	if string(header[0:4]) != "DKIF" || string(header[8:12]) != "VP80" {
		return
	}
	// IVF frame timestamps count in scale/rate seconds per tick (ffmpeg
	// writes rate=framerate, scale=1); deriving the sample duration from the
	// header keeps pion's pacing at the encoder's real framerate.
	rate := binary.LittleEndian.Uint32(header[16:20])
	scale := binary.LittleEndian.Uint32(header[20:24])
	if rate == 0 {
		rate = uint32(domain.FrameRate)
		scale = 1
	}
	tickNanos := float64(scale) / float64(rate) * float64(time.Second)
	frameHeader := make([]byte, 12)
	var lastTS uint64
	var frames, bytes, largest int
	for {
		if _, err := ioReadFull(reader, frameHeader); err != nil {
			return
		}
		size := binary.LittleEndian.Uint32(frameHeader[0:4])
		ts := binary.LittleEndian.Uint64(frameHeader[4:12])
		if size == 0 || size > maxIVFFramePayload {
			return
		}
		payload := make([]byte, size)
		if _, err := ioReadFull(reader, payload); err != nil {
			return
		}
		d.readyOnce.Do(func() { close(d.frameReady) })
		duration := time.Duration(tickNanos)
		if ts > lastTS {
			duration = time.Duration(float64(ts-lastTS) * tickNanos)
		}
		lastTS = ts
		frames++
		bytes += int(size)
		if int(size) > largest {
			largest = int(size)
		}
		if frames%50 == 0 {
			slog.Info("native capture stats", "display", d.displayName, "frames", frames, "bytes", bytes, "largest", largest)
		}
		d.sinkMu.Lock()
		sink := d.sink
		d.sinkMu.Unlock()
		if sink != nil {
			sink(payload, duration)
		}
	}
}

func ioReadFull(reader *bufio.Reader, buffer []byte) (int, error) {
	total := 0
	for total < len(buffer) {
		n, err := reader.Read(buffer[total:])
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// GuardInput installs the per-event control gate (ADR-0031 §4): input events
// of the CURRENT peer — including events already queued but not yet injected
// — are admitted only while the callback still reports control. The gate is
// read under the peer lock at apply time, so a takeover or lease expiry
// blocks the superseded device's queued input too. nil removes the gate.
func (d *display) GuardInput(gate func() bool) {
	d.peerMu.Lock()
	d.inputGate = gate
	d.peerMu.Unlock()
}

// Connect exchanges one complete offer for an answer and swaps the live peer.
func (d *display) Connect(ctx context.Context, offerSDP string) (string, error) {
	leaseDeadline := time.Now().Add(30 * time.Second)
	if deadline, ok := ctx.Deadline(); ok && deadline.Before(leaseDeadline) {
		leaseDeadline = deadline
	}
	if d.Exited() {
		return "", domain.ErrEngineUnavailable
	}
	settings := webrtc.SettingEngine{}
	if d.engine.Candidates == CandidatesLAN {
		if len(d.engine.LANNetworks) == 0 || d.engine.UDPMin == 0 {
			return "", domain.ErrEngineUnavailable
		}
		if err := settings.SetEphemeralUDPPortRange(d.engine.UDPMin, d.engine.UDPMax); err != nil {
			return "", err
		}
	}
	candidateFilter, includeLoopback := d.engine.candidatePolicy()
	if includeLoopback {
		settings.SetIncludeLoopbackCandidate(true)
	}
	if candidateFilter != nil {
		settings.SetIPFilter(candidateFilter)
	}
	pc, err := webrtc.NewAPI(webrtc.WithSettingEngine(settings)).NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		return "", err
	}
	track, err := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8}, "workos-video", "workos-stream")
	if err != nil {
		_ = pc.Close()
		return "", err
	}
	sender, err := pc.AddTrack(track)
	if err != nil {
		_ = pc.Close()
		return "", err
	}
	go func() {
		buffer := make([]byte, 1500)
		for {
			if _, _, err := sender.Read(buffer); err != nil {
				return
			}
		}
	}()
	channel, err := pc.CreateDataChannel("workos.input", nil)
	if err != nil {
		_ = pc.Close()
		return "", err
	}
	channel.OnMessage(func(message webrtc.DataChannelMessage) {
		d.peerMu.Lock()
		defer d.peerMu.Unlock()
		if d.peer == pc && !d.Exited() {
			d.enqueueInput(message.Data)
		}
	})
	gather := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: offerSDP}); err != nil {
		_ = pc.Close()
		return "", err
	}
	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		_ = pc.Close()
		return "", err
	}
	if err := pc.SetLocalDescription(answer); err != nil {
		_ = pc.Close()
		return "", err
	}
	select {
	case <-gather:
	case <-time.After(peerGatherTimeout):
		_ = pc.Close()
		return "", errors.New("ice gathering did not complete")
	case <-ctx.Done():
		_ = pc.Close()
		return "", ctx.Err()
	}
	d.peerMu.Lock()
	if d.Exited() || ctx.Err() != nil {
		d.peerMu.Unlock()
		_ = pc.Close()
		return "", domain.ErrEngineUnavailable
	}
	// Discard queued events from the superseded peer.
	d.peerEpoch++
	previous := d.peer
	d.peer = pc
	d.sinkMu.Lock()
	d.sink = func(frame []byte, duration time.Duration) {
		if err := track.WriteSample(media.Sample{Data: frame, Duration: duration}); err != nil {
			slog.Debug("native video sample dropped", "error", err)
		}
	}
	d.sinkMu.Unlock()
	d.peerMu.Unlock()
	if previous != nil {
		_ = previous.Close()
	}
	// Media bypasses Gateway after signaling. Expire the peer after 30s so
	// continuing input/video requires a newly authenticated Gateway request.
	time.AfterFunc(time.Until(leaseDeadline), func() { d.expirePeer(pc) })
	return pc.LocalDescription().SDP, nil
}

var _ ports.Display = (*display)(nil)
var _ ports.Engine = (*Engine)(nil)

// inputAllowed consults the CURRENT control gate; callers hold peerMu, the
// same lock GuardInput registers the gate under, so every event observes the
// freshest control verdict. No gate means no continuity enforcement is bound.
func (d *display) inputAllowed() bool {
	return d.inputGate == nil || d.inputGate()
}

// Detach releases the current peer only (ADR-0031): the display children
// keep running and a later Connect rebuilds media and input.
func (d *display) Detach() {
	d.peerMu.Lock()
	pc := d.peer
	d.peerMu.Unlock()
	if pc != nil {
		d.expirePeer(pc)
	}
}

// An old timer cannot close a newer authorized peer.
func (d *display) expirePeer(pc *webrtc.PeerConnection) {
	d.peerMu.Lock()
	if d.peer == pc {
		d.peer = nil
		d.peerEpoch++
		d.sinkMu.Lock()
		d.sink = nil
		d.sinkMu.Unlock()
	}
	d.peerMu.Unlock()
	_ = pc.Close()
}
