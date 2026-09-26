package application

import (
	"context"

	"github.com/yangtao121/workos/internal/runtime/nativehost/domain"
	"github.com/yangtao121/workos/internal/runtime/nativehost/ports"
)

type greenfieldDisplay interface {
	Endpoint(dprMillis int32) (string, string, int32, int32, int32, error)
	BindController(deviceID string, authorized func() bool)
}

// OpenGreenfield returns the runtime-local compositor path. It does not
// accept an SDP offer and does not replace the supervised client.
func (s *Service) OpenGreenfield(ctx context.Context, ownerUserID, deviceID, sessionID string, dprMillis int32, epoch int64) (string, string, int32, int32, int32, error) {
	display, _, err := s.greenfieldDisplay(ctx, ownerUserID, deviceID, sessionID, epoch)
	if err != nil {
		return "", "", 0, 0, 0, err
	}
	if dprMillis != 0 && (dprMillis < 500 || dprMillis > 4000) {
		return "", "", 0, 0, 0, domain.ErrInvalid
	}
	path, compositor, width, height, dpr, err := display.Endpoint(dprMillis)
	if err != nil {
		return "", "", 0, 0, 0, err
	}
	gate := s.control.(ports.EpochControlAuthorizer)
	display.BindController(deviceID, func() bool {
		return gate.AuthorizeInputGeneration(context.Background(), ownerUserID, sessionID, deviceID, epoch) == nil
	})
	return path, compositor, width, height, dpr, nil
}

func (s *Service) TransferClipboard(ctx context.Context, ownerUserID, deviceID, sessionID, direction string, text []byte, epoch int64) ([]byte, error) {
	_, _, err := s.greenfieldDisplay(ctx, ownerUserID, deviceID, sessionID, epoch)
	if err != nil {
		return nil, err
	}
	switch direction {
	case "host_to_app", "app_to_host":
		// Browser-side Wayland selection is separate. This legacy RPC has no
		// system-selection bridge; never return an in-memory string as success.
		return nil, domain.ErrClipboardUnavailable
	default:
		return nil, domain.ErrInvalid
	}
}

func (s *Service) greenfieldDisplay(ctx context.Context, ownerUserID, deviceID, sessionID string, epoch int64) (greenfieldDisplay, domain.Session, error) {
	if !domain.ValidUUIDv7(ownerUserID) || !domain.ValidUUIDv7(sessionID) || !domain.ValidUUIDv7(deviceID) {
		return nil, domain.Session{}, domain.ErrInvalid
	}
	session, err := s.store.GetSession(ctx, ownerUserID, sessionID)
	if err != nil {
		return nil, domain.Session{}, err
	}
	if session.State.Terminal() || session.State == domain.StateFailed {
		return nil, domain.Session{}, domain.ErrEngineUnavailable
	}
	gate, ok := s.control.(ports.EpochControlAuthorizer)
	if !ok {
		return nil, domain.Session{}, domain.ErrControlDenied
	}
	if err := gate.AuthorizeInputGeneration(ctx, ownerUserID, sessionID, deviceID, epoch); err != nil {
		return nil, domain.Session{}, err
	}
	s.mu.Lock()
	display, ok := s.displays[sessionID]
	s.mu.Unlock()
	if !ok || display.Exited() {
		return nil, domain.Session{}, domain.ErrEngineUnavailable
	}
	green, ok := display.(greenfieldDisplay)
	if !ok {
		return nil, domain.Session{}, domain.ErrWrongEngine
	}
	return green, session, nil
}
