package ports

import (
	"context"
	"time"

	"github.com/yangtao121/workos/internal/gateway/auth/domain"
)

type PushRevocationStore interface {
	ClaimPushRevocations(context.Context, time.Time) ([]domain.PushRevocation, error)
	CompletePushRevocation(context.Context, domain.PushRevocation, time.Time) error
}

type PushRevocationSink interface {
	RevokeDevicePush(context.Context, domain.PushRevocation) error
}
