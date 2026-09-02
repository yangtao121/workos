// Application service of the Core notification module: the durable
// authority over owner-scoped notification facts, the monotonic owner
// change stream, read state, and the exactly-once projection of system and
// app producers. Database constraints arbitrate every mutation; stored rows
// are revalidated on every read and every idempotent replay and any drift
// fails closed (ADR-0014).
package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/yangtao121/workos/internal/core/notification/domain"
	"github.com/yangtao121/workos/internal/core/notification/ports"
	dbtransient "github.com/yangtao121/workos/internal/platform/dbtransient"
	"github.com/yangtao121/workos/internal/platform/dbtx"
	"github.com/yangtao121/workos/internal/platform/ids"
)

var (
	// ErrInvalid marks malformed input rejected before any existence read.
	ErrInvalid = errors.New("notification request is invalid")
	// ErrConflict marks a same-key/different-request replay.
	ErrConflict = errors.New("notification request conflicts with a consumed key")
	// ErrTooMany marks bounded-set violations.
	ErrTooMany = errors.New("notification request exceeds bounded set")
)

// Bounds fixed by ADR-0014 and the migration CHECKs.
const (
	DefaultPageSize = 50
	MaxPageSize     = 100
	MaxReadBatch    = 100
	// ReadRetention bounds how long an already-read fact stays listed; the
	// bounded sweep never touches recent unread facts.
	ReadRetention = 30 * 24 * time.Hour
	// SweepBatch bounds each sweep pass.
	SweepBatch = 200
)

// TxSource opens a command transaction. *pgxpool.Pool satisfies it.
type TxSource interface {
	Begin(ctx context.Context) (dbtx.Tx, error)
}

// Service composes the store with the id generator. The app authorizer is
// the neutral installation port behind the app notification ingest; it is
// optional so the public surface can start without it, and every ingest
// call fails closed until it is wired.
type Service struct {
	store         ports.NotificationStore
	pool          TxSource
	ids           ids.Generator
	appAuthorizer AppInstallationAuthorizer
	now           func() time.Time
}

func New(store ports.NotificationStore, pool TxSource, generator ids.Generator) (*Service, error) {
	if store == nil || pool == nil || generator == nil {
		return nil, errors.New("notification service requires store, tx source, and generator")
	}
	return &Service{store: store, pool: pool, ids: generator, now: func() time.Time { return time.Now().UTC() }}, nil
}

// WithAppAuthorizer wires the neutral installation authorizer. The
// composition root must wire it before exposing the app ingest surface.
func (s *Service) WithAppAuthorizer(authorizer AppInstallationAuthorizer) error {
	if authorizer == nil {
		return errors.New("notification service requires an app authorizer")
	}
	s.appAuthorizer = authorizer
	return nil
}

// List reads one owner-scoped newest-first page plus the snapshot facts.
func (s *Service) List(ctx context.Context, ownerUserID string, filter ports.Filter, pageSize int, pageToken string) (ports.Page, string, error) {
	if !domain.ValidUUID(ownerUserID) {
		return ports.Page{}, "", ErrInvalid
	}
	if filter.ProjectID != "" && !domain.ValidUUID(filter.ProjectID) {
		return ports.Page{}, "", ErrInvalid
	}
	if filter.Kind != "" && !domain.ValidKind(filter.Kind) {
		return ports.Page{}, "", ErrInvalid
	}
	size := pageSize
	if size == 0 {
		size = DefaultPageSize
	}
	if size < 0 || size > MaxPageSize {
		return ports.Page{}, "", ErrInvalid
	}
	cursor, err := decodePageToken(pageToken, ownerUserID, filter)
	if err != nil {
		return ports.Page{}, "", err
	}
	page, err := s.store.ListPage(ctx, ownerUserID, filter, cursor, size)
	if err != nil {
		return ports.Page{}, "", err
	}
	next := ""
	if page.HasMore {
		next, err = encodePageToken(page.NextCursor, ownerUserID, filter)
		if err != nil {
			return ports.Page{}, "", err
		}
	}
	return page, next, nil
}

// Get returns one owner-scoped fact.
func (s *Service) Get(ctx context.Context, ownerUserID, notificationID string) (domain.Notification, error) {
	if !domain.ValidUUID(ownerUserID) || !domain.ValidUUID(notificationID) {
		return domain.Notification{}, ErrInvalid
	}
	return s.store.OwnerNotification(ctx, ownerUserID, notificationID)
}

// Summary returns the owner-wide badge snapshot. Incident-source freshness
// is reported by the composition layer, not by this store read.
func (s *Service) Summary(ctx context.Context, ownerUserID string) (ports.Summary, error) {
	if !domain.ValidUUID(ownerUserID) {
		return ports.Summary{}, ErrInvalid
	}
	return s.store.Summary(ctx, ownerUserID)
}

// ReadResult is the outcome of one read command (single or batch).
type ReadResult struct {
	Notifications []domain.Notification
	UnreadCount   int64
	// ChangeSequence is the highest applied/replayed READ change sequence.
	ChangeSequence int64
}

// MarkReadInput is one bounded read command.
type MarkReadInput struct {
	OwnerUserID     string
	NotificationIDs []string
	IdempotencyKey  string
}

// MarkRead applies the monotonic unread→read projection for up to
// MaxReadBatch owner-scoped facts in one all-or-nothing transaction. Same
// key/same request replays the versioned first response exactly across
// requests, processes, and restarts; same key/different request is a stable
// ErrConflict; no-op reads (already read) keep a deterministic first
// response and never append a duplicate change.
func (s *Service) MarkRead(ctx context.Context, input MarkReadInput) (ReadResult, error) {
	if !domain.ValidUUID(input.OwnerUserID) || !domain.ValidIdempotencyKey(input.IdempotencyKey) {
		return ReadResult{}, ErrInvalid
	}
	if len(input.NotificationIDs) == 0 || len(input.NotificationIDs) > MaxReadBatch {
		return ReadResult{}, ErrTooMany
	}
	seen := make(map[string]struct{}, len(input.NotificationIDs))
	ids := make([]string, 0, len(input.NotificationIDs))
	for _, id := range input.NotificationIDs {
		if !domain.ValidUUID(id) {
			return ReadResult{}, ErrInvalid
		}
		if _, dup := seen[id]; dup {
			return ReadResult{}, ErrInvalid
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	digest := readRequestDigest(ids)
	now := domain.CanonicalUTCTime(s.now())
	if record, ok, err := s.store.GetReadRequest(ctx, input.OwnerUserID, input.IdempotencyKey); err != nil {
		return ReadResult{}, err
	} else if ok {
		if record.RequestDigest != digest || record.ResultVersion != 1 {
			return ReadResult{}, ErrConflict
		}
		var first ReadResult
		if err := json.Unmarshal(record.Result, &first); err != nil {
			return ReadResult{}, fmt.Errorf("decode read first response: %w", domain.ErrCorrupt)
		}
		if err := s.validateReadSnapshot(first, input.OwnerUserID, ids); err != nil {
			return ReadResult{}, err
		}
		return first, nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ReadResult{}, storeFailure("begin notification read", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	locked, err := s.store.LockForRead(ctx, tx, input.OwnerUserID, ids)
	if err != nil {
		return ReadResult{}, err
	}
	if len(locked) != len(ids) {
		// Foreign or missing ids fail the whole command: there is never a
		// partial mutation, and unknown ids are indistinguishable from
		// someone else's.
		return ReadResult{}, domain.ErrNotFound
	}
	read, err := s.store.MarkReadTx(ctx, tx, input.OwnerUserID, locked, now)
	if err != nil {
		return ReadResult{}, err
	}
	for _, fact := range read {
		if err := domain.ValidStoredNotification(fact); err != nil {
			return ReadResult{}, err
		}
	}
	unread, err := s.store.UnreadTx(ctx, tx, input.OwnerUserID)
	if err != nil {
		return ReadResult{}, err
	}
	result := ReadResult{Notifications: read, UnreadCount: unread}
	for _, fact := range read {
		if fact.ReadChangeSequence > result.ChangeSequence {
			result.ChangeSequence = fact.ReadChangeSequence
		}
	}
	first, err := json.Marshal(result)
	if err != nil {
		return ReadResult{}, fmt.Errorf("encode read first response: %w", domain.ErrCorrupt)
	}
	if err := s.store.SaveReadRequest(ctx, tx, ports.ReadRequestRecord{
		OwnerUserID: input.OwnerUserID, IdempotencyKey: input.IdempotencyKey,
		RequestDigest: digest, ResultVersion: 1, Result: first, CreatedAt: now,
	}); err != nil {
		return ReadResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ReadResult{}, storeFailure("commit notification read", err)
	}
	return result, nil
}

// validateReadSnapshot revalidates a replayed first response the same way a
// fresh one is validated: ownership, shape, and read facts must still hold;
// composed corruption can never become a replay result.
func (s *Service) validateReadSnapshot(result ReadResult, ownerUserID string, ids []string) error {
	if len(result.Notifications) != len(ids) || result.UnreadCount < 0 || result.ChangeSequence < 0 {
		return domain.ErrCorrupt
	}
	seen := make(map[string]struct{}, len(ids))
	for _, fact := range result.Notifications {
		if fact.OwnerUserID != ownerUserID || !fact.Read() {
			return domain.ErrCorrupt
		}
		if _, dup := seen[fact.ID]; dup {
			return domain.ErrCorrupt
		}
		seen[fact.ID] = struct{}{}
		if err := domain.ValidStoredNotification(fact); err != nil {
			return err
		}
	}
	for _, id := range ids {
		if _, ok := seen[id]; !ok {
			return domain.ErrCorrupt
		}
	}
	return nil
}

func readRequestDigest(ids []string) string {
	canonical := "workos.notification-read.v1"
	for _, id := range ids {
		canonical += "|" + id
	}
	sum := sha256Sum(canonical)
	return "sha256:" + sum
}

// Watch reads one bounded batch of changes after the cursor. The transport
// polls; heartbeats and stream lifetime are transport concerns.
func (s *Service) Watch(ctx context.Context, ownerUserID string, after int64, limit int) ([]domain.Change, error) {
	if !domain.ValidUUID(ownerUserID) {
		return nil, ErrInvalid
	}
	if after < 0 || limit <= 0 || limit > MaxPageSize {
		return nil, ErrInvalid
	}
	return s.store.ChangesAfter(ctx, ownerUserID, after, limit)
}

// SweptThrough reports the owner's authoritative stream-gap watermark.
func (s *Service) SweptThrough(ctx context.Context, ownerUserID string) (int64, error) {
	if !domain.ValidUUID(ownerUserID) {
		return 0, ErrInvalid
	}
	return s.store.StreamGap(ctx, ownerUserID)
}

// Sweep retires one bounded batch of old read facts. It runs on the Core
// housekeeping loop; every failure is observable and retryable, and
// correctness never relies on it.
func (s *Service) Sweep(ctx context.Context) (int, error) {
	return s.store.SweepRead(ctx, domain.CanonicalUTCTime(s.now().Add(-ReadRetention)), SweepBatch)
}

func storeFailure(stage string, err error) error {
	if dbtransient.IsTransient(err) {
		return fmt.Errorf("%s: %w: %w", stage, ports.ErrStoreUnavailable, err)
	}
	return fmt.Errorf("%s: %w", stage, err)
}
