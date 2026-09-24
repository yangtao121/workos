package application

import (
	"context"

	"github.com/yangtao121/workos/internal/runtime/nativehost/domain"
	"github.com/yangtao121/workos/internal/runtime/nativehost/ports"
)

type greenfieldDisplay interface {
	Endpoint(dprMillis int32) (string, string, int32, int32, int32, error)
	AttachClipboard()
	WriteClipboard(string) error
	ReadClipboard() (string, error)
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
	display.AttachClipboard()
	return display.Endpoint(dprMillis)
}

func (s *Service) TransferClipboard(ctx context.Context, ownerUserID, deviceID, sessionID, direction string, text []byte, epoch int64) ([]byte, error) {
	display, _, err := s.greenfieldDisplay(ctx, ownerUserID, deviceID, sessionID, epoch)
	if err != nil {
		return nil, err
	}
	switch direction {
	case "host_to_app":
		if err := display.WriteClipboard(string(text)); err != nil {
			return nil, err
		}
		return text, nil
	case "app_to_host":
		got, err := display.ReadClipboard()
		if err != nil {
			return nil, err
		}
		return []byte(got), nil
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
	if gate, ok := s.control.(ports.EpochControlAuthorizer); ok {
		if err := gate.AuthorizeInputGeneration(ctx, ownerUserID, sessionID, deviceID, epoch); err != nil {
			return nil, domain.Session{}, err
		}
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
