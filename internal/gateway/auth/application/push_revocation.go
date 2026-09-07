package application

import (
	"context"
	"log/slog"
	"time"

	"github.com/yangtao121/workos/internal/gateway/auth/ports"
)

// PushRevocationConsumer drains only durable Gateway-owned obligations.
type PushRevocationConsumer struct {
	Store ports.PushRevocationStore
	Sink  ports.PushRevocationSink
}

func (c *PushRevocationConsumer) Pass(ctx context.Context) error {
	claims, err := c.Store.ClaimPushRevocations(ctx, time.Now().UTC())
	if err != nil {
		return err
	}
	// Finish the batch within its lease. Unvisited claims recover at expiry.
	batchCtx, stop := context.WithTimeout(ctx, 25*time.Second)
	defer stop()
	var failed error
	for _, claim := range claims {
		if err := batchCtx.Err(); err != nil {
			return err
		}
		sendCtx, cancel := context.WithTimeout(batchCtx, 3*time.Second)
		err := c.Sink.RevokeDevicePush(sendCtx, claim)
		cancel()
		if err != nil {
			failed = err
			continue
		}
		if err := c.Store.CompletePushRevocation(batchCtx, claim, time.Now().UTC()); err != nil {
			return err
		}
	}
	return failed
}

func (c *PushRevocationConsumer) Run(ctx context.Context, logger *slog.Logger) {
	for {
		if err := c.Pass(ctx); err != nil && ctx.Err() == nil {
			logger.Warn("device push revocations awaiting synchronization")
		}
		timer := time.NewTimer(5 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
