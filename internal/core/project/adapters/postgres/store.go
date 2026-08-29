package postgres

import (
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/yangtao121/workos/internal/core/project/ports"
	"github.com/yangtao121/workos/internal/platform/dbtransient"
)

// storeError is the single port-boundary failure classifier shared by every
// Project repository path — base CRUD, installation commands, and the
// create-request authority — so the two command families can never drift
// into different availability semantics. Transient dependency failures
// (unreachable server, broken connection, resource exhaustion) carry the
// ErrStoreUnavailable sentinel so transports can answer a sanitized
// Unavailable; every other failure stays an opaque internal error —
// classification never reads SQLSTATE message text or constraint names.
func storeError(operation string, err error) error {
	if err == nil {
		return nil
	}
	if dbtransient.IsTransient(err) {
		return fmt.Errorf("%s: %w: %w", operation, ports.ErrStoreUnavailable, err)
	}
	return fmt.Errorf("%s: %w", operation, err)
}

func timestamp(value time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: value, Valid: true}
}

func timePtr(value pgtype.Timestamptz) *time.Time {
	if !value.Valid {
		return nil
	}
	timestamp := value.Time
	return &timestamp
}
