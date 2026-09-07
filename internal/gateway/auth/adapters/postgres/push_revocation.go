package postgres

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/yangtao121/workos/internal/gateway/auth/adapters/postgres/gatewayauthdb"
	"github.com/yangtao121/workos/internal/gateway/auth/domain"
)

func (s *Store) ClaimPushRevocations(ctx context.Context, now time.Time) ([]domain.PushRevocation, error) {
	token, err := uuid.NewV7()
	if err != nil {
		return nil, err
	}
	rows, err := s.queries().ClaimPushRevocations(ctx, gatewayauthdb.ClaimPushRevocationsParams{Now: now, ClaimToken: token.String()})
	if err != nil {
		return nil, s.wrap(err)
	}
	result := make([]domain.PushRevocation, 0, len(rows))
	for _, row := range rows {
		result = append(result, domain.PushRevocation{OwnerID: row.OwnerUserID, DeviceID: row.DeviceID, RevokedAt: row.RevokedAt, ClaimToken: token.String()})
	}
	return result, nil
}

func (s *Store) CompletePushRevocation(ctx context.Context, claim domain.PushRevocation, now time.Time) error {
	return s.wrap(s.queries().CompletePushRevocation(ctx, gatewayauthdb.CompletePushRevocationParams{
		DeviceID: claim.DeviceID, OwnerUserID: claim.OwnerID, ClaimToken: claim.ClaimToken, Now: now,
	}))
}
