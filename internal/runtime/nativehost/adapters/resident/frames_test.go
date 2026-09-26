package resident

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"
	"time"

	surfacev1 "github.com/yangtao121/workos/gen/go/workos/surface/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	frameTestSession = "01999999-9999-7999-8999-000000000101"
	frameTestWindow  = "01999999-9999-7999-8999-000000000102"
)

func frameTestBroker(t *testing.T) *broker {
	t.Helper()
	b := &broker{sessionID: frameTestSession, generation: 2, ready: make(chan struct{}), done: make(chan struct{}),
		windowSubs: map[uint64]chan *surfacev1.GreenfieldWindowSnapshot{}, frameSubs: map[string]map[uint64]*frameSubscriber{},
		windows: map[string]*surfacev1.GreenfieldWindow{}, assemblies: map[string]*frameAssembly{}, lastSequence: map[string]uint64{}}
	rect := &surfacev1.GreenfieldWindowRect{Width: 1024, Height: 512}
	if err := b.acceptSnapshot(&surfacev1.GreenfieldWindowSnapshot{SessionId: frameTestSession, WorkloadGeneration: 2, Revision: 1,
		State:   surfacev1.GreenfieldDisplayState_GREENFIELD_DISPLAY_STATE_RUNNING,
		Windows: []*surfacev1.GreenfieldWindow{{Id: frameTestWindow, Revision: 1, ContentRect: rect, VisualRect: rect, DevicePixelRatioMillis: 1000}}}); err != nil {
		t.Fatal(err)
	}
	return b
}

func frameTestPNG(t *testing.T) []byte {
	t.Helper()
	pixels := image.NewRGBA(image.Rect(0, 0, 512, 512))
	pixels.Set(0, 0, color.RGBA{R: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, pixels); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func frameTestTile(t *testing.T, seq uint64, index, count, x uint32, full bool) *surfacev1.GreenfieldWindowFrameTile {
	t.Helper()
	return &surfacev1.GreenfieldWindowFrameTile{WindowId: frameTestWindow, WorkloadGeneration: 2, WindowRevision: 1,
		FrameSequence: seq, TileIndex: index, TileCount: count, X: x, Y: 0, Width: 512, Height: 512,
		FrameWidth: 1024, FrameHeight: 512, Png: frameTestPNG(t), FullRefresh: full,
		RenderedAt: timestamppb.New(time.Unix(1_000+int64(seq), 0).UTC())}
}

func TestBrokerRequiresFullFrameForNewAndSlowViewer(t *testing.T) {
	b := frameTestBroker(t)
	first, closeFirst, err := b.subscribeFrames(frameTestWindow)
	if err != nil {
		t.Fatal(err)
	}
	defer closeFirst()
	if err := b.acceptTile(frameTestTile(t, 1, 0, 1, 0, false)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-first:
		t.Fatal("new viewer received a sparse frame before a full refresh")
	default:
	}
	if err := b.acceptTile(frameTestTile(t, 2, 0, 2, 0, true)); err != nil {
		t.Fatal(err)
	}
	if err := b.acceptTile(frameTestTile(t, 2, 1, 2, 512, true)); err != nil {
		t.Fatal(err)
	}
	if frame := <-first; len(frame) != 2 || !frame[0].GetFullRefresh() {
		t.Fatalf("initial full frame invalid: %+v", frame)
	}
	if err := b.acceptTile(frameTestTile(t, 3, 0, 1, 0, false)); err != nil {
		t.Fatal(err)
	}
	if frame := <-first; len(frame) != 1 || frame[0].GetFullRefresh() {
		t.Fatalf("changed-tile frame invalid: %+v", frame)
	}
	second, closeSecond, err := b.subscribeFrames(frameTestWindow)
	if err != nil {
		t.Fatal(err)
	}
	defer closeSecond()
	if err := b.acceptTile(frameTestTile(t, 4, 0, 1, 512, false)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-second:
		t.Fatal("late viewer received a delta before full refresh")
	default:
	}
	if err := b.acceptTile(frameTestTile(t, 5, 0, 2, 0, true)); err != nil {
		t.Fatal(err)
	}
	if err := b.acceptTile(frameTestTile(t, 5, 1, 2, 512, true)); err != nil {
		t.Fatal(err)
	}
	if frame := <-second; len(frame) != 2 || !frame[0].GetFullRefresh() {
		t.Fatalf("late viewer full frame invalid: %+v", frame)
	}
}

func TestBrokerRejectsOverlappingFullFrame(t *testing.T) {
	b := frameTestBroker(t)
	if err := b.acceptTile(frameTestTile(t, 1, 0, 2, 0, true)); err != nil {
		t.Fatal(err)
	}
	if err := b.acceptTile(frameTestTile(t, 1, 1, 2, 0, true)); err == nil {
		t.Fatal("overlapping full frame passed validation")
	}
}
