package xvfbengine

import (
	"bytes"
	"context"
	"log/slog"
	"math"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
	surfacev1 "github.com/yangtao121/workos/gen/go/workos/surface/v1"
	"github.com/yangtao121/workos/internal/runtime/nativehost/domain"
)

func TestInputFailureDoesNotLogText(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	defer slog.SetDefault(previous)
	d := &display{engine: &Engine{Xdotool: "/bin/false"}, runCtx: context.Background(), displayName: ":90"}
	d.applyInput([]byte(`{"type":"text","text":"private-fixture-marker"}`))
	if strings.Contains(logs.String(), "private-fixture-marker") {
		t.Fatal("input content leaked to logs")
	}
	if !strings.Contains(logs.String(), "injection failed") {
		t.Fatal("failure was not recorded")
	}
}

func TestCanonicalInputRejectsUnknownAndMixedPayloads(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	defer slog.SetDefault(previous)
	d := &display{engine: &Engine{Xdotool: "/bin/false"}, runCtx: context.Background(), width: 800, height: 600}
	for _, raw := range []string{
		`{"type":"key","key":"Return","unknown":"value"}`,
		`{"type":"text","text":"fixture","key":"Return"}`,
		`{"type":"pointer","action":"move","x":"NaN"}`,
		`{"type":"pointer","action":"move","text":"fixture"}`,
	} {
		d.applyInput([]byte(raw))
	}
	if logs.Len() != 0 {
		t.Fatal("invalid input reached the injection process")
	}
}

func TestReserveConcurrentRelease(t *testing.T) {
	e := &Engine{}
	release, err := e.Reserve()
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 32 {
		wg.Go(release)
	}
	wg.Wait()
	if e.count != 0 {
		t.Fatalf("reservation leaked: %d", e.count)
	}
}

func TestPointerPositionsAndBounds(t *testing.T) {
	d := &display{width: 800, height: 600}
	argv := d.pointerArgv(&surfacev1.NativeInputEvent{Action: "down", X: 1, Y: 1, Button: 3})
	if strings.Join(argv, " ") != "mousemove --sync 799 599 mousedown 3" {
		t.Fatalf("wrong pointer mapping: %v", argv)
	}
	if d.pointerArgv(&surfacev1.NativeInputEvent{Action: "move", X: 1.1}) != nil {
		t.Fatal("out of range coordinate accepted")
	}
	if d.pointerArgv(&surfacev1.NativeInputEvent{Action: "move", X: math.NaN()}) != nil {
		t.Fatal("non-finite coordinate accepted")
	}
	if validText(strings.Repeat("a", domain.MaxTextRunes+1)) || validText("\xff") {
		t.Fatal("invalid text accepted")
	}
}

// The gate executes this compiled test binary in the real X11 toolchain image.
func TestRealEngineLifecycle(t *testing.T) {
	if os.Getenv("WORKOS_NATIVE_ENGINE_TEST") != "1" {
		t.Skip("requires native-surface X11 toolchain gate")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	scratch := t.TempDir()
	e, err := New("Xvfb", "xterm", "ffmpeg", "xdotool", scratch, "")
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		value, err := e.Launch(ctx, 640, 480, "")
		if err != nil {
			t.Fatal(err)
		}
		d := value.(*display)
		d.Stop()
		d.Stop()
		select {
		case <-d.inputDone:
		case <-time.After(time.Second):
			t.Fatal("input worker leaked after Stop")
		}
		if _, err := os.Stat(d.dir); !os.IsNotExist(err) {
			t.Fatal("scratch directory leaked")
		}
	}
	// Executable present but launch failure / early exit must never serve an empty root.
	for _, client := range []string{"/bin/false", filepath.Join(scratch, "missing-client")} {
		e.Client = []string{client}
		if value, err := e.Launch(ctx, 640, 480, ""); err == nil {
			value.Stop()
			t.Fatal("failed client reported running")
		}
	}
	e.Client = []string{"xterm"}
	e.FFmpeg = "/bin/false"
	if value, err := e.Launch(ctx, 640, 480, ""); err == nil {
		value.Stop()
		t.Fatal("failed encoder reported running")
	}
	entries, err := os.ReadDir(scratch)
	if err != nil || len(entries) != 0 {
		t.Fatalf("failed startup leaked scratch: %v %v", entries, err)
	}
}

func TestPeerExpiryPreservesReplacement(t *testing.T) {
	first, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	defer second.Close()
	d := &display{peer: second, peerEpoch: 2}
	d.expirePeer(first)
	if d.peer != second || second.ConnectionState() == webrtc.PeerConnectionStateClosed {
		t.Fatal("stale expiry revoked replacement")
	}
	d.expirePeer(second)
	if d.peer != nil || second.ConnectionState() != webrtc.PeerConnectionStateClosed || d.peerEpoch != 3 {
		t.Fatal("peer expiry failed to revoke input/media")
	}
}

// TestCandidatePolicyLoopbackDefaultUnchanged pins the fail-safe default:
// every candidate outside loopback is filtered and the loopback candidate is
// force-included. The lan mode is the only way to widen the scope — and it
// removes the filter entirely instead of adding unfiltered extras.
func TestCandidatePolicyLoopbackDefaultUnchanged(t *testing.T) {
	defaults := &Engine{}
	filter, includeLoopback := defaults.candidatePolicy()
	if filter == nil || !includeLoopback {
		t.Fatal("default candidate policy must filter to loopback and include loopback candidates")
	}
	if filter(net.ParseIP("127.0.0.1")) != true || filter(net.ParseIP("192.168.1.10")) != false || filter(net.ParseIP("::1")) != true {
		t.Fatal("default filter must admit loopback only")
	}

	lan := &Engine{Candidates: CandidatesLAN}
	filter, includeLoopback = lan.candidatePolicy()
	if filter != nil {
		t.Fatal("lan mode must not install an IP filter")
	}
	if includeLoopback {
		t.Fatal("lan mode must not force loopback candidate inclusion")
	}

	loopback := &Engine{Candidates: CandidatesLoopback}
	filter, includeLoopback = loopback.candidatePolicy()
	if filter == nil || !includeLoopback || filter(net.ParseIP("10.0.0.2")) != false {
		t.Fatal("explicit loopback mode must behave exactly like the default")
	}
}

// TestNewRejectsUnknownCandidateMode: candidate scope is operator
// configuration with exactly two legal values; anything else fails startup.
func TestNewRejectsUnknownCandidateMode(t *testing.T) {
	// /bin/true stands in for the toolchain binaries: the candidate
	// validation is under test, not the X11 toolchain.
	if _, err := New("/bin/true", "/bin/true", "/bin/true", "/bin/true", t.TempDir(), "wider-internet"); err == nil {
		t.Fatal("unknown candidate mode must be rejected")
	}
	if _, err := New("/bin/true", "/bin/true", "/bin/true", "/bin/true", t.TempDir(), CandidatesLAN); err != nil {
		t.Fatalf("lan mode must be accepted: %v", err)
	}
	engine, err := New("/bin/true", "/bin/true", "/bin/true", "/bin/true", t.TempDir(), "")
	if err != nil || engine.Candidates != CandidatesLoopback {
		t.Fatal("empty candidate mode must default to loopback")
	}
	scope := ""
	for _, limit := range engine.Facts().EnforcedLimits {
		if limit == "lan-host-candidates" || limit == "loopback-host-candidates-only" {
			scope = limit
		}
	}
	if scope != "loopback-host-candidates-only" {
		t.Fatalf("facts must report the honest candidate scope, got %q", scope)
	}
}

// TestInputGateBlocksQueuedEvents pins the apply-time control check: a gate
// that reports no control refuses queued events, a removed gate reopens the
// plain owner-scoped path (no continuity enforcement bound).
func TestInputGateBlocksQueuedEvents(t *testing.T) {
	d := &display{}
	d.GuardInput(func() bool { return false })
	d.peerMu.Lock()
	if d.inputAllowed() {
		t.Fatal("blocked gate must refuse queued input")
	}
	d.peerMu.Unlock()
	d.GuardInput(func() bool { return true })
	d.peerMu.Lock()
	if !d.inputAllowed() {
		t.Fatal("admitting gate must pass input")
	}
	d.peerMu.Unlock()
	d.GuardInput(nil)
	d.peerMu.Lock()
	if !d.inputAllowed() {
		t.Fatal("no gate means no continuity enforcement is bound")
	}
	d.peerMu.Unlock()
}
