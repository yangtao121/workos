package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yangtao121/workos/internal/core/desktop/domain"
	"github.com/yangtao121/workos/internal/core/desktop/ports"
	"github.com/yangtao121/workos/internal/platform/dbtransient"
)

type Store struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

type transaction struct {
	tx    pgx.Tx
	owner string
	state domain.State
}

func failure(err error) error {
	if err == nil {
		return nil
	}
	if dbtransient.IsTransient(err) || errors.Is(err, context.DeadlineExceeded) {
		return domain.ErrUnavailable
	}
	return err
}
func decode(b []byte, revision int64) (domain.State, error) {
	var s domain.State
	if len(b) > 65536 || json.Unmarshal(b, &s) != nil || s.Revision != revision || s.Validate() != nil {
		return s, domain.ErrCorrupt
	}
	return s, nil
}
func (s *Store) Locked(ctx context.Context, owner string, run func(ports.Transaction) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return failure(err)
	}
	defer tx.Rollback(context.Background()) //nolint:errcheck
	_, err = tx.Exec(ctx, `INSERT INTO workos_core.desktop_states(owner_user_id,state) VALUES($1,'{"windows":[],"revision":0}') ON CONFLICT DO NOTHING`, owner)
	if err != nil {
		return failure(err)
	}
	var bytes []byte
	var revision int64
	err = tx.QueryRow(ctx, `SELECT state,revision FROM workos_core.desktop_states WHERE owner_user_id=$1 FOR UPDATE`, owner).Scan(&bytes, &revision)
	if err != nil {
		return failure(err)
	}
	state, err := decode(bytes, revision)
	if err != nil {
		return err
	}
	t := &transaction{tx: tx, owner: owner, state: state}
	if err := run(t); err != nil {
		return failure(err)
	}
	return failure(tx.Commit(ctx))
}
func (t *transaction) State() domain.State { return t.state.Clone() }
func (t *transaction) Request(ctx context.Context, key string) (string, bool, error) {
	var digest string
	err := t.tx.QueryRow(ctx, `SELECT digest FROM workos_core.desktop_operations WHERE owner_user_id=$1 AND idempotency_key=$2`, t.owner, key).Scan(&digest)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	return digest, err == nil, failure(err)
}
func (t *transaction) Remember(ctx context.Context, key, digest string) error {
	_, err := t.tx.Exec(ctx, `INSERT INTO workos_core.desktop_operations(owner_user_id,idempotency_key,digest) VALUES($1,$2,$3)`, t.owner, key, digest)
	return failure(err)
}
func (t *transaction) Save(ctx context.Context, state domain.State) error {
	if state.Validate() != nil || state.Revision != t.state.Revision+1 {
		return domain.ErrCorrupt
	}
	bytes, err := json.Marshal(state)
	if err != nil {
		return err
	}
	_, err = t.tx.Exec(ctx, `UPDATE workos_core.desktop_states SET state=$2,revision=$3,updated_at=now() WHERE owner_user_id=$1`, t.owner, bytes, state.Revision)
	if err != nil {
		return failure(err)
	}
	_, err = t.tx.Exec(ctx, `INSERT INTO workos_core.desktop_events(owner_user_id,revision,state) VALUES($1,$2,$3)`, t.owner, state.Revision, bytes)
	if err != nil {
		return failure(err)
	}
	_, err = t.tx.Exec(ctx, `DELETE FROM workos_core.desktop_events WHERE owner_user_id=$1 AND revision <= $2`, t.owner, state.Revision-domain.EventRetention)
	if err == nil {
		t.state = state.Clone()
	}
	return failure(err)
}
func (t *transaction) Events(ctx context.Context, after int64) ([]domain.State, error) {
	rows, err := t.tx.Query(ctx, `SELECT state,revision FROM workos_core.desktop_events WHERE owner_user_id=$1 AND revision>$2 ORDER BY revision LIMIT 256`, t.owner, after)
	if err != nil {
		return nil, failure(err)
	}
	defer rows.Close()
	result := []domain.State{}
	for rows.Next() {
		var b []byte
		var rev int64
		if err := rows.Scan(&b, &rev); err != nil {
			return nil, failure(err)
		}
		s, err := decode(b, rev)
		if err != nil {
			return nil, err
		}
		result = append(result, s)
	}
	return result, failure(rows.Err())
}
