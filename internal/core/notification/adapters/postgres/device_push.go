package postgres

import (
	"context"
	"time"

	"github.com/yangtao121/workos/internal/core/notification/adapters/postgres/notificationdb"
	"github.com/yangtao121/workos/internal/core/notification/domain"
)

func (r *Repository) writePushDevice(ctx context.Context, ownerID, deviceID string, write func(*notificationdb.Queries) error) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return storeError("begin push device write", err)
	}
	defer tx.Rollback(ctx)
	q := r.queries.WithTx(tx)
	if err := q.LockPushDevice(ctx, ownerID+":"+deviceID); err != nil {
		return storeError("lock push device", err)
	}
	if err := write(q); err != nil {
		return storeError("write push device", err)
	}
	return storeError("commit push device", tx.Commit(ctx))
}

func (r *Repository) RevokePushDevice(ctx context.Context, ownerID, deviceID string, revokedAt time.Time) error {
	return r.writePushDevice(ctx, ownerID, deviceID, func(q *notificationdb.Queries) error {
		stored, err := q.RememberPushDeviceRevocation(ctx, notificationdb.RememberPushDeviceRevocationParams{OwnerUserID: ownerID, DeviceID: deviceID, RevokedAt: revokedAt})
		if err != nil {
			return err
		}
		if !stored.Equal(revokedAt.Truncate(time.Microsecond)) {
			return domain.ErrPushConflict
		}
		return q.RevokeDevicePushSubscriptions(ctx, notificationdb.RevokeDevicePushSubscriptionsParams{OwnerUserID: ownerID, DeviceID: deviceID, Now: time.Now().UTC()})
	})
}
