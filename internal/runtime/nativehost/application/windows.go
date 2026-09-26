package application

import (
	"context"
	"math"
	"unicode/utf8"

	surfacev1 "github.com/yangtao121/workos/gen/go/workos/surface/v1"
	"github.com/yangtao121/workos/internal/runtime/nativehost/domain"
	"github.com/yangtao121/workos/internal/runtime/nativehost/ports"
	"google.golang.org/protobuf/proto"
)

const (
	maxWindowInputEvents    = 64
	maxWindowInputBytes     = 1_500_000
	maxWindowTextBytes      = 16 << 10
	maxWindowClipboardBytes = 1 << 20
)

// WindowView is a live, generation-pinned subscription. The transport calls
// Revalidate before every send and at least once per second while idle.
type WindowView struct {
	Windows    <-chan *surfacev1.GreenfieldWindowSnapshot
	Frames     <-chan []*surfacev1.GreenfieldWindowFrameTile
	Close      func()
	Revalidate func(context.Context) error
}

func (s *Service) windowDisplay(ctx context.Context, owner, sessionID string, generation int64) (ports.WindowDisplay, error) {
	if !domain.ValidUUIDv7(owner) || !domain.ValidUUIDv7(sessionID) || generation < 1 {
		return nil, domain.ErrInvalid
	}
	session, err := s.store.GetSession(ctx, owner, sessionID)
	if err != nil {
		return nil, err
	}
	if session.State != domain.StateRunning || session.Generation != generation {
		return nil, domain.ErrEngineUnavailable
	}
	s.mu.Lock()
	display := s.displays[sessionID]
	s.mu.Unlock()
	window, ok := display.(ports.WindowDisplay)
	if !ok || display.Exited() {
		return nil, domain.ErrEngineUnavailable
	}
	return window, nil
}

func (s *Service) viewGate(ctx context.Context, owner, device, sessionID, attachmentID string, generation int64) (ports.WindowDisplay, error) {
	if !domain.ValidUUIDv7(device) || !domain.ValidUUIDv7(attachmentID) || s.windows == nil {
		return nil, domain.ErrControlDenied
	}
	display, err := s.windowDisplay(ctx, owner, sessionID, generation)
	if err != nil {
		return nil, err
	}
	if err := s.windows.AuthorizeWindowViewer(ctx, owner, device, sessionID, attachmentID, generation); err != nil {
		return nil, domain.ErrControlDenied
	}
	return display, nil
}

func (s *Service) OpenWindowSnapshots(ctx context.Context, owner, device, sessionID, attachmentID string, generation int64) (WindowView, error) {
	display, err := s.viewGate(ctx, owner, device, sessionID, attachmentID, generation)
	if err != nil {
		return WindowView{}, err
	}
	updates, closeFn, err := display.SubscribeWindows()
	if err != nil {
		return WindowView{}, err
	}
	return WindowView{Windows: updates, Close: closeFn,
		Revalidate: func(ctx context.Context) error {
			current, err := s.viewGate(ctx, owner, device, sessionID, attachmentID, generation)
			if err != nil {
				return err
			}
			if current != display {
				return domain.ErrEngineUnavailable
			}
			return nil
		}}, nil
}

func (s *Service) OpenWindowFrames(ctx context.Context, owner, device, sessionID, attachmentID string, generation int64, windowID string) (WindowView, error) {
	if !domain.ValidUUIDv7(windowID) {
		return WindowView{}, domain.ErrInvalid
	}
	display, err := s.viewGate(ctx, owner, device, sessionID, attachmentID, generation)
	if err != nil {
		return WindowView{}, err
	}
	frames, closeFn, err := display.SubscribeFrames(windowID)
	if err != nil {
		return WindowView{}, err
	}
	return WindowView{Frames: frames, Close: closeFn,
		Revalidate: func(ctx context.Context) error {
			current, err := s.viewGate(ctx, owner, device, sessionID, attachmentID, generation)
			if err != nil {
				return err
			}
			if current != display {
				return domain.ErrEngineUnavailable
			}
			return nil
		}}, nil
}

func validWindowEvent(event *surfacev1.GreenfieldWindowInputEvent) bool {
	if event == nil || event.GetSequence() == 0 || !domain.ValidUUIDv7(event.GetWindowId()) {
		return false
	}
	switch payload := event.Event.(type) {
	case *surfacev1.GreenfieldWindowInputEvent_Pointer:
		p := payload.Pointer
		return p != nil && p.GetAction() != surfacev1.GreenfieldPointerAction_GREENFIELD_POINTER_ACTION_UNSPECIFIED &&
			!math.IsNaN(p.GetX()) && !math.IsInf(p.GetX(), 0) && !math.IsNaN(p.GetY()) && !math.IsInf(p.GetY(), 0) &&
			!math.IsNaN(p.GetDeltaX()) && !math.IsInf(p.GetDeltaX(), 0) && !math.IsNaN(p.GetDeltaY()) && !math.IsInf(p.GetDeltaY(), 0) && p.GetButton() <= 5
	case *surfacev1.GreenfieldWindowInputEvent_Key:
		k := payload.Key
		return k != nil && k.GetAction() != surfacev1.GreenfieldKeyAction_GREENFIELD_KEY_ACTION_UNSPECIFIED && validKey(k.GetCode(), k.GetKey())
	case *surfacev1.GreenfieldWindowInputEvent_Text:
		text := payload.Text.GetText()
		return text != "" && len(text) <= maxWindowTextBytes && utf8.ValidString(text)
	case *surfacev1.GreenfieldWindowInputEvent_Resize:
		r := payload.Resize
		return r != nil && r.GetContentWidth() >= 320 && r.GetContentWidth() <= 4096 && r.GetContentHeight() >= 240 && r.GetContentHeight() <= 4096 && r.GetDevicePixelRatioMillis() >= 500 && r.GetDevicePixelRatioMillis() <= 4000
	case *surfacev1.GreenfieldWindowInputEvent_ClipboardWrite:
		text := payload.ClipboardWrite.GetTextUtf8()
		return len(text) <= maxWindowClipboardBytes && utf8.Valid(text)
	case *surfacev1.GreenfieldWindowInputEvent_Focus:
		return payload.Focus != nil
	case *surfacev1.GreenfieldWindowInputEvent_Close:
		return payload.Close != nil
	default:
		return false
	}
}

func validKey(code, key string) bool {
	if len(code) == 0 || len(code) > 64 || len(key) > 64 || !utf8.ValidString(key) {
		return false
	}
	for _, r := range code {
		if !((r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')) {
			return false
		}
	}
	for _, r := range key {
		if r < 32 || r == 127 {
			return false
		}
	}
	return true
}

func validateWindowInput(request *surfacev1.SendGreenfieldWindowInputRequest) error {
	if request == nil || proto.Size(request) > maxWindowInputBytes || len(request.GetEvents()) < 1 || len(request.GetEvents()) > maxWindowInputEvents {
		return domain.ErrInvalid
	}
	clipboardWrites := 0
	var previous uint64
	for _, event := range request.GetEvents() {
		if !validWindowEvent(event) || (previous != 0 && event.GetSequence() != previous+1) {
			return domain.ErrInvalid
		}
		if event.GetClipboardWrite() != nil {
			clipboardWrites++
		}
		previous = event.GetSequence()
	}
	if clipboardWrites > 1 {
		return domain.ErrInvalid
	}
	return nil
}

// SendWindowInput uses one barrier acquisition per event, not per batch. A
// takeover may therefore interrupt a batch between acknowledged events.
func (s *Service) SendWindowInput(ctx context.Context, owner, device string, request *surfacev1.SendGreenfieldWindowInputRequest) (*surfacev1.SendGreenfieldWindowInputResponse, error) {
	if err := validateWindowInput(request); err != nil {
		return nil, err
	}
	if !domain.ValidUUIDv7(device) || !domain.ValidUUIDv7(request.GetAttachmentId()) || s.windows == nil {
		return nil, domain.ErrControlDenied
	}
	var lastApplied uint64
	for _, event := range request.GetEvents() {
		sequence := event.GetSequence()
		var result *surfacev1.SendGreenfieldWindowInputResponse
		display, err := s.windowDisplay(ctx, owner, request.GetSessionId(), request.GetExpectedWorkloadGeneration())
		if err != nil {
			if lastApplied > 0 {
				return &surfacev1.SendGreenfieldWindowInputResponse{Verdict: surfacev1.GreenfieldInputVerdict_GREENFIELD_INPUT_VERDICT_STALE_CONTROL, LastAppliedSequence: lastApplied, RejectedSequence: sequence}, nil
			}
			return nil, err
		}
		err = display.AroundInput(ctx, func() error {
			if _, err := s.windowDisplay(ctx, owner, request.GetSessionId(), request.GetExpectedWorkloadGeneration()); err != nil {
				return err
			}
			if err := s.windows.AuthorizeWindowController(ctx, owner, device, request.GetSessionId(), request.GetAttachmentId(), request.GetExpectedWorkloadGeneration(), request.GetControlGeneration()); err != nil {
				return domain.ErrControlDenied
			}
			one := &surfacev1.SendGreenfieldWindowInputRequest{SessionId: request.GetSessionId(), AttachmentId: request.GetAttachmentId(), ExpectedWorkloadGeneration: request.GetExpectedWorkloadGeneration(), ControlGeneration: request.GetControlGeneration(), Events: []*surfacev1.GreenfieldWindowInputEvent{event}}
			var err error
			result, err = display.SendWindowEvent(ctx, one)
			return err
		})
		if err != nil {
			if lastApplied > 0 && err == domain.ErrControlDenied {
				return &surfacev1.SendGreenfieldWindowInputResponse{Verdict: surfacev1.GreenfieldInputVerdict_GREENFIELD_INPUT_VERDICT_STALE_CONTROL, LastAppliedSequence: lastApplied, RejectedSequence: sequence}, nil
			}
			return nil, err
		}
		if result == nil {
			return nil, domain.ErrEngineUnavailable
		}
		switch result.GetVerdict() {
		case surfacev1.GreenfieldInputVerdict_GREENFIELD_INPUT_VERDICT_APPLIED:
			if result.GetLastAppliedSequence() < sequence {
				return nil, domain.ErrEngineUnavailable
			}
			lastApplied = sequence
		case surfacev1.GreenfieldInputVerdict_GREENFIELD_INPUT_VERDICT_STALE_CONTROL:
			if lastApplied == 0 {
				return nil, domain.ErrControlDenied
			}
			return &surfacev1.SendGreenfieldWindowInputResponse{Verdict: result.GetVerdict(), LastAppliedSequence: lastApplied, RejectedSequence: sequence}, nil
		case surfacev1.GreenfieldInputVerdict_GREENFIELD_INPUT_VERDICT_INVALID_SEQUENCE:
			return &surfacev1.SendGreenfieldWindowInputResponse{Verdict: result.GetVerdict(), LastAppliedSequence: result.GetLastAppliedSequence(), RejectedSequence: sequence}, nil
		default:
			return nil, domain.ErrEngineUnavailable
		}
	}
	return &surfacev1.SendGreenfieldWindowInputResponse{Verdict: surfacev1.GreenfieldInputVerdict_GREENFIELD_INPUT_VERDICT_APPLIED, LastAppliedSequence: lastApplied}, nil
}

func (s *Service) ReadWindowClipboard(ctx context.Context, owner, device string, request *surfacev1.ReadGreenfieldClipboardRequest) (*surfacev1.ReadGreenfieldClipboardResponse, error) {
	if request == nil || !domain.ValidUUIDv7(device) || !domain.ValidUUIDv7(request.GetAttachmentId()) || s.windows == nil {
		return nil, domain.ErrInvalid
	}
	display, err := s.windowDisplay(ctx, owner, request.GetSessionId(), request.GetExpectedWorkloadGeneration())
	if err != nil {
		return nil, err
	}
	var response *surfacev1.ReadGreenfieldClipboardResponse
	err = display.AroundInput(ctx, func() error {
		if _, err := s.windowDisplay(ctx, owner, request.GetSessionId(), request.GetExpectedWorkloadGeneration()); err != nil {
			return err
		}
		if err := s.windows.AuthorizeWindowController(ctx, owner, device, request.GetSessionId(), request.GetAttachmentId(), request.GetExpectedWorkloadGeneration(), request.GetControlGeneration()); err != nil {
			return domain.ErrControlDenied
		}
		var err error
		response, err = display.ReadWindowClipboard(ctx, request)
		return err
	})
	if err != nil {
		return nil, err
	}
	if response == nil || len(response.GetTextUtf8()) > maxWindowClipboardBytes || !utf8.Valid(response.GetTextUtf8()) {
		return nil, domain.ErrEngineUnavailable
	}
	return response, nil
}
