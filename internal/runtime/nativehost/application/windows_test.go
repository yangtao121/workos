package application

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	surfacev1 "github.com/yangtao121/workos/gen/go/workos/surface/v1"
	"github.com/yangtao121/workos/internal/runtime/nativehost/domain"
)

const (
	testAttachment = "01999999-9999-7999-8999-000000000004"
	testWindow     = "01999999-9999-7999-8999-000000000005"
)

type windowGate struct {
	viewer     bool
	controller bool
	attachment string
	generation int64
}

func (g *windowGate) AuthorizeWindowViewer(_ context.Context, _, _, _, attachment string, generation int64) error {
	if !g.viewer || attachment != g.attachment || generation != g.generation {
		return domain.ErrControlDenied
	}
	return nil
}
func (g *windowGate) AuthorizeWindowController(ctx context.Context, owner, device, session, attachment string, generation, controlGeneration int64) error {
	if err := g.AuthorizeWindowViewer(ctx, owner, device, session, attachment, generation); err != nil {
		return err
	}
	if !g.controller || controlGeneration != 1 {
		return domain.ErrControlDenied
	}
	return nil
}

func TestWindowObserverAndPerEventTakeoverGate(t *testing.T) {
	ctx := context.Background()
	store := newResidentMemoryStore()
	engine := &residentTestEngine{fakeEngine: &fakeEngine{}}
	service, err := NewService(store, engine, &seqGenerator{}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	gate := &windowGate{viewer: true, attachment: testAttachment, generation: 1}
	service.WithWindowAuthorization(gate)
	session, err := service.Create(ctx, testOwner, testProject, "window-gate", 800, 600, domain.LifecycleManualStop)
	if err != nil {
		t.Fatal(err)
	}
	view, err := service.OpenWindowSnapshots(ctx, testOwner, testDevice, session.SessionID, testAttachment, 1)
	if err != nil {
		t.Fatalf("observer should watch: %v", err)
	}
	defer view.Close()
	request := &surfacev1.SendGreenfieldWindowInputRequest{
		SessionId: session.SessionID, AttachmentId: testAttachment,
		ExpectedWorkloadGeneration: 1, ControlGeneration: 1,
		Events: []*surfacev1.GreenfieldWindowInputEvent{
			{Sequence: 1, WindowId: testWindow, Event: &surfacev1.GreenfieldWindowInputEvent_Focus{Focus: &surfacev1.GreenfieldWindowFocus{}}},
			{Sequence: 2, WindowId: testWindow, Event: &surfacev1.GreenfieldWindowInputEvent_Focus{Focus: &surfacev1.GreenfieldWindowFocus{}}},
		},
	}
	if _, err := service.SendWindowInput(ctx, testOwner, testDevice, request); !errors.Is(err, domain.ErrControlDenied) {
		t.Fatalf("observer input should be denied: %v", err)
	}
	gate.controller = true
	display := engine.created[0]
	display.onEvent = func(req *surfacev1.SendGreenfieldWindowInputRequest) {
		if req.GetEvents()[0].GetSequence() == 1 {
			gate.controller = false // takeover happens between child ACKs
		}
	}
	result, err := service.SendWindowInput(ctx, testOwner, testDevice, request)
	if err != nil || result.GetVerdict() != surfacev1.GreenfieldInputVerdict_GREENFIELD_INPUT_VERDICT_STALE_CONTROL ||
		result.GetLastAppliedSequence() != 1 || result.GetRejectedSequence() != 2 || len(display.events) != 1 {
		t.Fatalf("stale control crossed event boundary: %v %+v events=%v", err, result, display.events)
	}
	gate.viewer = false
	if err := view.Revalidate(ctx); !errors.Is(err, domain.ErrControlDenied) {
		t.Fatalf("detached observer stream stayed authorized: %v", err)
	}
}
