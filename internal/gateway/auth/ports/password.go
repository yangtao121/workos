package ports

import (
	"context"
	"time"

	"github.com/yangtao121/workos/internal/gateway/auth/domain"
)

// PasswordRepository owns the separate password-mode facts in Gateway SQL.
type PasswordRepository interface {
	Credential(ctx context.Context, ownerID string) (username, hash string, err error)
	SetPassword(ctx context.Context, ownerID, username, hash string, now time.Time) error
	CreatePasswordSession(ctx context.Context, ownerID, username, expectedHash string, device domain.Device, session domain.DeviceSession) error
	ResolvePasswordSession(ctx context.Context, tokenHash string) (domain.DeviceSession, domain.Device, error)
	GetPasswordDevice(ctx context.Context, ownerID, deviceID string) (domain.Device, error)
	ListPasswordDevices(ctx context.Context, ownerID, cursor string, limit int) ([]domain.Device, error)
	RevokePasswordDevice(ctx context.Context, op RevokeDeviceOp) (domain.Device, bool, error)
	LogoutPasswordSession(ctx context.Context, ownerID, sessionID string, now time.Time) error
}
