//go:build integration && nativegate

package integration_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"hash"
	"net/http"
	"os"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/pion/webrtc/v4"
	projectv1 "github.com/yangtao121/workos/gen/go/workos/project/v1"
	"github.com/yangtao121/workos/gen/go/workos/project/v1/projectv1connect"
	surfacev1 "github.com/yangtao121/workos/gen/go/workos/surface/v1"
	"github.com/yangtao121/workos/gen/go/workos/surface/v1/surfacev1connect"
	"github.com/yangtao121/workos/internal/platform/identity"
)

func nativeGateEnv(t *testing.T, name string) string {
	t.Helper()
	value := os.Getenv("WORKOS_NATIVE_GATE_" + name)
	if value == "" {
		t.Fatalf("run through tools/native-surface/gate.sh (missing %s)", name)
	}
	return value
}

// frameStats accumulates complete VP8 frames (RTP marker boundaries) so the
// test can compare display content across time windows.
type frameStats struct {
	mu     sync.Mutex
	hashes []string
	sizes  []int
}

func (f *frameStats) add(digest hash.Hash, size int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hashes = append(f.hashes, fmt.Sprintf("%x", digest.Sum(nil)))
	f.sizes = append(f.sizes, size)
}

func (f *frameStats) snapshot() (hashes []string, sizes []int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.hashes...), append([]int{}, f.sizes...)
}

// maxSize returns the largest complete-frame payload in the slice: with the
// encoder's periodic keyframes this is the honest per-window content signal.
func maxSize(sizes []int) int {
	largest := 0
	for _, size := range sizes {
		if size > largest {
			largest = size
		}
	}
	return largest
}

// TestNativeSessions proves the virtual-display native runner (ADR-0029) on
// a real stack: a Go WebRTC peer receives genuine VP8 frames of the Xvfb
// display, Data Channel input drives the native xterm (exit closes the
// window and collapses the frame payloads), and the durable RPC matrix
// (idempotency, drift, sizes, cap, isolation, close) holds.
func TestNativeSessions(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()
	client := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 60 * time.Second}
	t.Cleanup(client.CloseIdleConnections)
	gatewayURL := nativeGateEnv(t, "GATEWAY_URL")
	natives := surfacev1connect.NewNativeSessionServiceClient(client, gatewayURL)
	directNatives := surfacev1connect.NewNativeSessionServiceClient(client, nativeGateEnv(t, "RUNTIME_URL"))
	projects := projectv1connect.NewProjectServiceClient(client, gatewayURL)
	owner := "01999999-9999-7999-8999-000000000b01"
	device := "01999999-9999-7999-8999-000000000b02"

	created, err := projects.CreateProject(ctx, connect.NewRequest(&projectv1.CreateProjectRequest{
		IdempotencyKey: fmt.Sprintf("native-%d", time.Now().UnixNano()), Name: "Native Fixture",
	}))
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	projectID := created.Msg.GetProject().GetId()

	key := fmt.Sprintf("native-%d", time.Now().UnixNano())
	session, err := natives.CreateNativeSession(ctx, nativeRequest(&surfacev1.CreateNativeSessionRequest{
		IdempotencyKey: key, ProjectId: projectID, Width: 800, Height: 600,
	}, owner, device))
	if err != nil {
		t.Fatalf("create native session: %v", err)
	}
	sessionID := session.Msg.GetSession().GetId()
	if sessionID == "" || session.Msg.GetSession().GetState() != "running" {
		t.Fatalf("unexpected session: %+v", session.Msg.GetSession())
	}
	defer func() {
		_, _ = natives.CloseNativeSession(context.Background(), connect.NewRequest(&surfacev1.CloseNativeSessionRequest{SessionId: sessionID}))
	}()

	// Idempotent replay returns the same session; a drifted size aborts.
	replay, err := natives.CreateNativeSession(ctx, nativeRequest(&surfacev1.CreateNativeSessionRequest{
		IdempotencyKey: key, ProjectId: projectID, Width: 800, Height: 600,
	}, owner, device))
	if err != nil || replay.Msg.GetSession().GetId() != sessionID {
		t.Fatalf("replay drifted: %v %+v", err, replay.Msg.GetSession())
	}
	if _, err := natives.CreateNativeSession(ctx, nativeRequest(&surfacev1.CreateNativeSessionRequest{
		IdempotencyKey: key, ProjectId: projectID, Width: 1024, Height: 768,
	}, owner, device)); connect.CodeOf(err) != connect.CodeAborted {
		t.Fatalf("drifted replay must abort: %v", err)
	}
	// Invalid sizes fail closed.
	if _, err := natives.CreateNativeSession(ctx, nativeRequest(&surfacev1.CreateNativeSessionRequest{
		IdempotencyKey: key + "-bad", ProjectId: projectID, Width: 32, Height: 24,
	}, owner, device)); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("bad size must be invalid: %v", err)
	}

	// The Go peer plays the desktop role: receive-only video, the runtime
	// creates the workos.input data channel.
	peer, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatalf("peer: %v", err)
	}
	defer func() { _ = peer.Close() }()
	if _, err := peer.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo, webrtc.RTPTransceiverInit{
		Direction: webrtc.RTPTransceiverDirectionRecvonly,
	}); err != nil {
		t.Fatalf("transceiver: %v", err)
	}
	stats := &frameStats{}
	inputReady := make(chan *webrtc.DataChannel, 1)
	peer.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		go readFrames(track, stats)
	})
	peer.OnDataChannel(func(channel *webrtc.DataChannel) {
		if channel.Label() == "workos.input" {
			select {
			case inputReady <- channel:
			default:
			}
		}
	})
	// Unlike browsers, pion only advertises the SCTP application section when
	// the offerer owns a data channel; the runtime creates workos.input on
	// the answer side, so a placeholder channel must open the m-line for it.
	if _, err := peer.CreateDataChannel("offer-sctp", nil); err != nil {
		t.Fatalf("placeholder data channel: %v", err)
	}
	offer, err := peer.CreateOffer(nil)
	if err != nil {
		t.Fatalf("offer: %v", err)
	}
	gathered := webrtc.GatheringCompletePromise(peer)
	if err := peer.SetLocalDescription(offer); err != nil {
		t.Fatalf("set local: %v", err)
	}
	select {
	case <-gathered:
	case <-time.After(10 * time.Second):
		t.Fatal("local ICE gathering did not complete")
	}
	connected, err := natives.ConnectNativeSession(ctx, nativeRequest(&surfacev1.ConnectNativeSessionRequest{
		SessionId: sessionID, OfferSdp: peer.LocalDescription().SDP,
	}, owner, device))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if connected.Msg.GetAnswerSdp() == "" {
		t.Fatal("empty answer sdp")
	}
	if err := peer.SetRemoteDescription(webrtc.SessionDescription{
		Type: webrtc.SDPTypeAnswer, SDP: connected.Msg.GetAnswerSdp(),
	}); err != nil {
		t.Fatalf("set remote: %v", err)
	}

	// Real video: complete VP8 frames keep arriving from the display.
	waitForFrames := func(count int, timeout time.Duration) []int {
		deadline := time.Now().Add(timeout)
		for time.Now().Before(deadline) {
			_, sizes := stats.snapshot()
			if len(sizes) >= count {
				return sizes
			}
			time.Sleep(200 * time.Millisecond)
		}
		_, sizes := stats.snapshot()
		return sizes
	}
	// VP8 codes this fixture (a flat white xterm window on black) extremely
	// efficiently: the static screen's keyframes weigh ~1 KiB. Absolute size
	// thresholds are therefore meaningless; the honest content signal is the
	// SIZE DELTA between display states driven through the data channel.
	before := waitForFrames(25, 30*time.Second)
	if len(before) < 25 {
		t.Fatalf("expected real video frames, got %d", len(before))
	}
	beforeMax := maxSize(before)

	// Input round trip, phase 1: run `yes` in the xterm's shell over the
	// data channel. The screen fills with dense text (high entropy), so the
	// encoded frames grow substantially — proof the injected keystrokes
	// reached the real display and changed what the camera sees.
	var channel *webrtc.DataChannel
	select {
	case channel = <-inputReady:
	case <-time.After(15 * time.Second):
		t.Fatal("workos.input data channel never opened")
	}
	openDeadline := time.Now().Add(15 * time.Second)
	for channel.ReadyState() != webrtc.DataChannelStateOpen && time.Now().Before(openDeadline) {
		time.Sleep(200 * time.Millisecond)
	}
	if channel.ReadyState() != webrtc.DataChannelStateOpen {
		t.Fatalf("data channel state %v", channel.ReadyState())
	}
	floodStart := len(before)
	if err := channel.SendText(`{"type":"text","text":"yes workos-native-proof-0123456789abcdefghij"}`); err != nil {
		t.Fatalf("send text: %v", err)
	}
	if err := channel.SendText(`{"type":"key","key":"Return"}`); err != nil {
		t.Fatalf("send key: %v", err)
	}
	// Phase 2: stop the flood and exit; the window closes and the display
	// collapses back toward the empty root (sizes return to the before level).
	waitFor := func(from int, match func(tail []int) bool, timeout time.Duration) bool {
		deadline := time.Now().Add(timeout)
		for time.Now().Before(deadline) {
			_, sizes := stats.snapshot()
			if len(sizes) > from+25 && match(sizes[len(sizes)-25:]) {
				return true
			}
			time.Sleep(300 * time.Millisecond)
		}
		return false
	}
	flooded := waitFor(floodStart, func(tail []int) bool {
		return maxSize(tail) > 2*beforeMax
	}, 30*time.Second)
	if !flooded {
		_, sizes := stats.snapshot()
		t.Fatalf("typing did not fill the display: static max %d, after max %d (sizes %v)",
			beforeMax, maxSize(sizes[floodStart:]), sizes[floodStart:floodStart+20])
	}
	floodedMax := func() int {
		_, sizes := stats.snapshot()
		return maxSize(sizes[floodStart:])
	}()
	if err := channel.SendText(`{"type":"key","key":"ctrl+c"}`); err != nil {
		t.Fatalf("send ctrl+c: %v", err)
	}
	if err := channel.SendText(`{"type":"text","text":"exit"}`); err != nil {
		t.Fatalf("send exit: %v", err)
	}
	if err := channel.SendText(`{"type":"key","key":"Return"}`); err != nil {
		t.Fatalf("send key: %v", err)
	}
	_, sizesAtExit := stats.snapshot()
	exitStart := len(sizesAtExit)
	collapsed := waitFor(exitStart, func(tail []int) bool {
		return maxSize(tail) < floodedMax/2
	}, 30*time.Second)
	if !collapsed {
		_, sizes := stats.snapshot()
		t.Fatalf("exit did not collapse the display: flooded max %d, tail max %d",
			floodedMax, maxSize(sizes[len(sizes)-25:]))
	}

	// Foreign owners never see or drive the session (identity headers are
	// trusted only on the runtime listener, so isolation is checked there).
	if _, err := directNatives.GetNativeSession(ctx, nativeRequest(&surfacev1.GetNativeSessionRequest{
		SessionId: sessionID,
	}, "01999999-9999-7999-8999-000000000c99", device)); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("foreign get must 404: %v", err)
	}

	// The per-owner cap holds: one live session exists, exactly one more is
	// admitted, the third is rejected (ADR-0029: 2/owner).
	admitted := 0
	for index := 0; index < 2; index++ {
		extra, err := natives.CreateNativeSession(ctx, nativeRequest(&surfacev1.CreateNativeSessionRequest{
			IdempotencyKey: fmt.Sprintf("%s-cap-%d", key, index), ProjectId: projectID, Width: 640, Height: 480,
		}, owner, device))
		if err == nil {
			admitted++
			defer func(id string) {
				_, _ = natives.CloseNativeSession(context.Background(), connect.NewRequest(&surfacev1.CloseNativeSessionRequest{SessionId: id}))
			}(extra.Msg.GetSession().GetId())
		}
	}
	if admitted > 1 {
		t.Fatalf("session cap did not hold: %d admitted", admitted)
	}

	// Close is terminal: signaling after close fails.
	if _, err := natives.CloseNativeSession(ctx, nativeRequest(&surfacev1.CloseNativeSessionRequest{
		SessionId: sessionID,
	}, owner, device)); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := natives.ConnectNativeSession(ctx, nativeRequest(&surfacev1.ConnectNativeSessionRequest{
		SessionId: sessionID, OfferSdp: "v=0\r\n",
	}, owner, device)); err == nil {
		t.Fatal("connect after close must fail")
	}
}

// readFrames assembles complete frames on RTP marker boundaries and records
// payload hashes and sizes.
func readFrames(track *webrtc.TrackRemote, stats *frameStats) {
	var digest hash.Hash = sha256.New()
	var size int
	for {
		packet, _, err := track.ReadRTP()
		if err != nil {
			return
		}
		digest.Write(packet.Payload)
		size += len(packet.Payload)
		if packet.Marker {
			stats.add(digest, size)
			digest.Reset()
			size = 0
		}
	}
}

// nativeRequest carries the trusted identity headers the dev-bypass gateway
// normally injects.
func nativeRequest[Req any](body *Req, owner, device string) *connect.Request[Req] {
	request := connect.NewRequest(body)
	request.Header().Set(identity.UserHeader, owner)
	request.Header().Set(identity.DeviceHeader, device)
	return request
}
