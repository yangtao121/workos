package xvfbengine

import (
	"bytes"
	"context"
	"log/slog"
	"math"
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
	e, err := New("Xvfb", "xterm", "ffmpeg", "xdotool", scratch)
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		value, err := e.Launch(ctx, 640, 480)
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
		if value, err := e.Launch(ctx, 640, 480); err == nil {
			value.Stop()
			t.Fatal("failed client reported running")
		}
	}
	e.Client = []string{"xterm"}
	e.FFmpeg = "/bin/false"
	if value, err := e.Launch(ctx, 640, 480); err == nil {
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
