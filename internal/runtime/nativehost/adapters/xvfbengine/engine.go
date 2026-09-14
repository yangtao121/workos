// Package xvfbengine supervises real virtual-display children (ADR-0029):
// per session one Xvfb display, one configured native X client, and one
// ffmpeg x11grab/VP8 capture whose IVF frames feed the WebRTC video track.
// Input events arrive on the workos.input data channel and map to xdotool
// XTEST injection. Loopback topology only: host candidates, no STUN/TURN.
package xvfbengine

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
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

// Engine facts: process-level supervision only; no cgroup or namespace
// isolation claim, no display auth beyond the single-uid Unix socket, and no
// TURN relay (loopback host candidates only).
type Engine struct {
	Xvfb    string
	Client  []string
	FFmpeg  string
	Xdotool string
	Scratch string

	mu    sync.Mutex
	count int
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

func New(xvfb, client, ffmpeg, xdotool, scratch string) (*Engine, error) {
	argv := strings.Fields(client)
	if xvfb == "" || ffmpeg == "" || xdotool == "" || len(argv) == 0 || scratch == "" {
		return nil, errors.New("native engine requires xvfb, client argv, ffmpeg, xdotool and scratch")
	}
	resolvedXvfb := resolveExecutable(xvfb)
	resolvedFFmpeg := resolveExecutable(ffmpeg)
	resolvedXdotool := resolveExecutable(xdotool)
	resolvedClient := resolveExecutable(argv[0])
	if resolvedXvfb == "" || resolvedFFmpeg == "" || resolvedXdotool == "" || resolvedClient == "" {
		return nil, fmt.Errorf("native engine toolchain is not executable (xvfb=%q client=%q ffmpeg=%q xdotool=%q)", xvfb, argv[0], ffmpeg, xdotool)
	}
	return &Engine{Xvfb: resolvedXvfb, Client: argv, FFmpeg: resolvedFFmpeg, Xdotool: resolvedXdotool, Scratch: scratch}, nil
}

func (e *Engine) Facts() ports.EngineFacts {
	return ports.EngineFacts{
		Engine:           "xvfb-x11grab-vp8-webrtc",
		ProcessGroupKill: true,
		ParentDeathSig:   true,
		EnforcedLimits:   []string{"process-group-kill", "parent-death-signal", "input-rate", "session-ttl", "loopback-host-candidates-only"},
	}
}

func (e *Engine) Available(ctx context.Context) error {
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

type sampleSink func(frame []byte, duration time.Duration)

// display couples the supervised children with at most one live WebRTC peer.
type display struct {
	engine      *Engine
	dir         string
	displayName string
	width       int32
	height      int32

	xvfbCmd   *exec.Cmd
	clientCm  *exec.Cmd
	ffmpegCmd *exec.Cmd
	cancel    context.CancelFunc
	exited    chan struct{}
	closeOnce sync.Once

	sinkMu sync.Mutex
	sink   sampleSink

	peerMu sync.Mutex
	peer   *webrtc.PeerConnection

	inputMu      sync.Mutex
	inputTokens  float64
	inputStamp   time.Time
	inputQueue   chan json.RawMessage
	inputDropped int64
}

func (e *Engine) Launch(ctx context.Context, width, height int32) (ports.Display, error) {
	if !domain.ValidSize(width, height) {
		return nil, domain.ErrInvalid
	}
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	dir, err := os.MkdirTemp(e.Scratch, "native-")
	if err != nil {
		cancel()
		return nil, err
	}
	_ = os.Chmod(dir, 0o700)
	number, err := freeDisplayNumber()
	if err != nil {
		cancel()
		_ = os.RemoveAll(dir)
		return nil, err
	}
	name := ":" + strconv.Itoa(number)
	xvfb := exec.Command(e.Xvfb, name, "-screen", "0", fmt.Sprintf("%dx%dx24", width, height), "-nolisten", "tcp", "-ac")
	xvfb.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	if err := xvfb.Start(); err != nil {
		cancel()
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("start xvfb: %w", err)
	}
	socket := filepath.Join("/tmp", ".X11-unix", "X"+strconv.Itoa(number))
	if err := waitForSocket(socket, xvfb); err != nil {
		_ = xvfb.Process.Kill()
		cancel()
		_ = os.RemoveAll(dir)
		return nil, err
	}
	env := []string{fmt.Sprintf("DISPLAY=%s", name), "HOME=" + dir, "PATH=/usr/local/bin:/usr/bin:/bin", "LANG=C.UTF-8", "XDG_RUNTIME_DIR=" + dir}
	client := exec.Command(e.Client[0], e.Client[1:]...)
	client.Env = env
	client.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pgid: xvfb.Process.Pid, Pdeathsig: syscall.SIGKILL}
	clientErr := client.Start()
	if clientErr == nil {
		go func() {
			err := client.Wait()
			slog.Warn("native client exited", "display", name, "error", err)
		}()
	} else {
		slog.Warn("native client start failed; display serves the root window", "error", clientErr)
	}
	// Opening the display before it serves clients makes x11grab hang, and
	// capturing before the client maps its window streams an empty root.
	// Wait for the first visible window (bounded); the capture then starts
	// against an established display with real content.
	waitForClientWindow(env, e.Xdotool)

	ffmpeg := exec.CommandContext(runCtx, e.FFmpeg, "-nostdin", "-loglevel", "error",
		"-f", "x11grab", "-draw_mouse", "1", "-video_size", fmt.Sprintf("%dx%d", width, height),
		"-framerate", strconv.Itoa(domain.FrameRate), "-i", name,
		// Debian ffmpeg 5.x registers the VP8 encoder as plain "libvpx".
		// -g 20 keeps a keyframe every two seconds so consumers (and the
		// gate) can observe display content changes deterministically.
		"-c:v", "libvpx", "-deadline", "realtime", "-cpu-used", "8", "-b:v", "800k",
		"-g", "20",
		"-pix_fmt", "yuv420p", "-vf", "scale=trunc(iw/2)*2:trunc(ih/2)*2",
		"-f", "ivf", "pipe:1")
	ffmpeg.Env = env
	ffmpeg.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pgid: xvfb.Process.Pid, Pdeathsig: syscall.SIGKILL}
	ffmpeg.WaitDelay = time.Second
	stdout, err := ffmpeg.StdoutPipe()
	if err != nil {
		_ = xvfb.Process.Kill()
		cancel()
		_ = os.RemoveAll(dir)
		return nil, err
	}
	if err := ffmpeg.Start(); err != nil {
		_ = xvfb.Process.Kill()
		cancel()
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("start ffmpeg: %w", err)
	}
	d := &display{
		engine: e, dir: dir, displayName: name, width: width, height: height,
		xvfbCmd: xvfb, clientCm: client, ffmpegCmd: ffmpeg, cancel: cancel,
		exited:      make(chan struct{}),
		inputQueue:  make(chan json.RawMessage, inputQueueDepth),
		inputTokens: float64(domain.InputBurst),
		inputStamp:  time.Now(),
	}
	// The single input worker owns xdotool ordering; it runs on its own
	// goroutine so the data-channel callback never blocks on injection.
	go func() {
		for event := range d.inputQueue {
			d.applyInput(event)
		}
	}()
	go func() {
		_ = xvfb.Wait()
		d.markExited()
	}()
	go func() {
		_ = ffmpeg.Wait()
		d.markExited()
	}()
	go d.readIVF(bufio.NewReaderSize(stdout, 64*1024))
	return d, nil
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

func waitForSocket(socket string, xvfb *exec.Cmd) error {
	deadline := time.Now().Add(socketWait)
	for time.Now().Before(deadline) {
		select {
		case <-time.After(50 * time.Millisecond):
		}
		if _, err := os.Stat(socket); err == nil {
			return nil
		}
		if xvfb.ProcessState != nil {
			return fmt.Errorf("xvfb exited during startup")
		}
	}
	return errors.New("xvfb socket did not appear")
}

// waitForClientWindow polls xdotool until the client maps a visible window
// (bounded), then focuses it: with no window manager the new window never
// receives keyboard focus on its own, and XTEST keystrokes would go nowhere.
// A headless or slow client falls through to the root-window capture
// honestly, with the outcome logged.
func waitForClientWindow(env []string, xdotool string) {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		probe := exec.CommandContext(ctx, xdotool, "search", "--onlyvisible", "--name", ".")
		probe.Env = env
		output, err := probe.Output()
		cancel()
		if err == nil {
			windowID := strings.TrimSpace(strings.SplitN(string(output), "\n", 2)[0])
			if windowID != "" {
				focusCtx, focusCancel := context.WithTimeout(context.Background(), time.Second)
				focus := exec.CommandContext(focusCtx, xdotool, "windowfocus", "--sync", windowID)
				focus.Env = env
				focusErr := focus.Run()
				focusCancel()
				slog.Info("native client window detected before capture", "display", env[0], "window", windowID, "focusError", focusErr)
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	slog.Warn("native client window never appeared; capturing the root window", "display", env[0])
}

func (d *display) markExited() {
	d.closeOnce.Do(func() { close(d.exited) })
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
	if d.xvfbCmd.Process != nil {
		_ = syscall.Kill(-d.xvfbCmd.Process.Pid, syscall.SIGKILL)
	}
	deadline := time.Now().Add(3 * time.Second)
	for !d.Exited() && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	d.peerMu.Lock()
	peer := d.peer
	d.peer = nil
	d.peerMu.Unlock()
	if peer != nil {
		_ = peer.Close()
	}
	d.cancel()
	_ = os.RemoveAll(d.dir)
}

// readIVF parses the ffmpeg IVF stream and pushes each VP8 frame into the
// current sink. Before the first Connect frames go to a no-op sink, so the
// capture pipeline is warm when signaling completes.
func (d *display) readIVF(reader *bufio.Reader) {
	header := make([]byte, 32)
	if _, err := ioReadFull(reader, header); err != nil {
		d.markExited()
		return
	}
	if string(header[0:4]) != "DKIF" || string(header[8:12]) != "VP80" {
		d.markExited()
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
			d.markExited()
			return
		}
		size := binary.LittleEndian.Uint32(frameHeader[0:4])
		ts := binary.LittleEndian.Uint64(frameHeader[4:12])
		if size == 0 || size > maxIVFFramePayload {
			d.markExited()
			return
		}
		payload := make([]byte, size)
		if _, err := ioReadFull(reader, payload); err != nil {
			d.markExited()
			return
		}
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

// Connect exchanges one complete offer for an answer and swaps the live peer.
func (d *display) Connect(ctx context.Context, offerSDP string) (string, error) {
	if d.Exited() {
		return "", domain.ErrEngineUnavailable
	}
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		return "", err
	}
	track, err := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8}, "workos-video", "workos-stream")
	if err != nil {
		_ = pc.Close()
		return "", err
	}
	if _, err := pc.AddTrack(track); err != nil {
		_ = pc.Close()
		return "", err
	}
	channel, err := pc.CreateDataChannel("workos.input", nil)
	if err != nil {
		_ = pc.Close()
		return "", err
	}
	channel.OnMessage(func(message webrtc.DataChannelMessage) {
		slog.Info("native input received", "display", d.displayName, "bytes", len(message.Data))
		d.enqueueInput(message.Data)
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
	previous := d.peer
	d.peer = pc
	d.peerMu.Unlock()
	if previous != nil {
		_ = previous.Close()
	}
	d.sinkMu.Lock()
	d.sink = func(frame []byte, duration time.Duration) {
		if err := track.WriteSample(media.Sample{Data: frame, Duration: duration}); err != nil {
			slog.Debug("native video sample dropped", "error", err)
		}
	}
	d.sinkMu.Unlock()
	return pc.LocalDescription().SDP, nil
}

var _ ports.Display = (*display)(nil)
var _ ports.Engine = (*Engine)(nil)
