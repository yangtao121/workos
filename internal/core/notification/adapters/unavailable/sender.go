// Package unavailable holds the honest push senders for platforms that
// require external provider credentials (APNs/FCM today). They always report
// ErrPushUnavailable: subscribing stays possible, dispatch fails loudly with
// the sanitized unavailable verdict, and nothing pretends to deliver.
package unavailable

import (
	"context"

	"github.com/yangtao121/workos/internal/core/notification/domain"
)

// Sender is the always-unavailable push sender.
type Sender struct{}

// New builds the unavailable sender.
func New() *Sender { return &Sender{} }

// Deliver reports the sanitized unavailable verdict for every dispatch.
func (s *Sender) Deliver(ctx context.Context, subscription domain.PushSubscription, payload domain.PushPayload) error {
	return domain.ErrPushUnavailable
}
