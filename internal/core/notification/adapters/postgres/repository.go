// PostgreSQL adapter of the Core notification module. Every mutation is
// arbitrated by a database constraint (source receipt uniqueness, guarded
// read projection, owner counter allocation), stored rows are revalidated
// on every read and replay, and transient outages carry the
// ErrStoreUnavailable sentinel so transports answer sanitized Unavailable
// (ADR-0014).
package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yangtao121/workos/internal/core/notification/adapters/postgres/notificationdb"
	"github.com/yangtao121/workos/internal/core/notification/domain"
	"github.com/yangtao121/workos/internal/core/notification/ports"
	"github.com/yangtao121/workos/internal/platform/dbtransient"
	"github.com/yangtao121/workos/internal/platform/dbtx"
)

// storeError wraps a storage failure at the port boundary. Transient
// dependency failures carry the ErrStoreUnavailable sentinel; classification
// never reads SQLSTATE message text or constraint names.
func storeError(operation string, err error) error {
	if err == nil {
		return nil
	}
	if dbtransient.IsTransient(err) {
		return fmt.Errorf("%s: %w: %w", operation, ports.ErrStoreUnavailable, err)
	}
	return fmt.Errorf("%s: %w", operation, err)
}

// ReceiptRetentionExtension bounds how much longer a source receipt
// outlives the swept notification it projected.
const ReceiptRetentionExtension = 150 * 24 * time.Hour

type Repository struct {
	pool    *pgxpool.Pool
	queries *notificationdb.Queries
}

func New(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool, queries: notificationdb.New(pool)}
}

func (r *Repository) SerializeRequestTx(ctx context.Context, tx dbtx.Tx, key string) error {
	if err := r.queries.WithTx(tx).SerializeNotificationRequest(ctx, key); err != nil {
		return storeError("serialize notification request", err)
	}
	return nil
}

// AppendSystemNotification implements the tx-scoped producer sink: the
// projection joins the caller's source-mutation transaction, so a
// notification failure must roll the source back.
func (r *Repository) AppendSystemNotification(ctx context.Context, tx dbtx.Tx, fact domain.SystemFact, occurredAt time.Time) (domain.Notification, error) {
	prepared, err := domain.PrepareSystemFact(fact, occurredAt)
	if err != nil {
		return domain.Notification{}, err
	}
	projected, _, err := r.AppendTx(ctx, tx, prepared)
	return projected, err
}

// AppendTx projects one prepared notification inside the caller's
// transaction: receipt check → exactly-once arbitration (physical unique
// indexes on the receipt and the notification source) → owner change
// sequence allocation → notification row + CREATED change + receipt, all in
// the caller's transaction. Replays and concurrent duplicates return the
// stored fact; a same-source/different-digest drift is corruption.
func (r *Repository) AppendTx(ctx context.Context, tx dbtx.Tx, notification domain.Notification) (domain.Notification, bool, error) {
	queries := r.queries.WithTx(tx)
	if err := r.SerializeRequestTx(ctx, tx, sourceRequestLockKey(notification.SourceProcess, notification.SourceID)); err != nil {
		return domain.Notification{}, false, err
	}
	if existing, err := r.replayTx(ctx, queries, notification); err != nil || existing != nil {
		return dereplay(existing), false, err
	}
	sequence, err := queries.AllocateNotificationChangeSequence(ctx, notificationdb.AllocateNotificationChangeSequenceParams{
		OwnerUserID: notification.OwnerUserID, UpdatedAt: notification.CreatedAt,
	})
	if err != nil {
		return domain.Notification{}, false, storeError("allocate notification change sequence", err)
	}
	notification.CreatedChangeSequence = sequence
	rows, err := queries.InsertNotification(ctx, notificationdb.InsertNotificationParams{
		ID: notification.ID, OwnerUserID: notification.OwnerUserID,
		ProjectID: uuidParam(notification.ProjectID), Kind: notification.Kind,
		Severity: notification.Severity, Origin: notification.Origin,
		Title: notification.Title, Body: notification.Body,
		TargetKind: notification.TargetKind, TargetID: notification.TargetID,
		AppID: textParam(notification.AppID), AppInstallationID: uuidParam(notification.AppInstallationID),
		SourceProcess: notification.SourceProcess, SourceID: notification.SourceID,
		SourceDigest: notification.SourceDigest, CreatedAt: notification.CreatedAt,
		CreatedChangeSequence: notification.CreatedChangeSequence,
	})
	if err != nil {
		// A concurrent winner may have committed between the receipt check
		// and this insert; re-read inside this transaction before failing.
		if existing, replayErr := r.replayTx(ctx, queries, notification); replayErr == nil && existing != nil {
			return *existing, false, nil
		}
		return domain.Notification{}, false, storeError("insert notification", err)
	}
	if rows == 0 {
		return domain.Notification{}, false, storeError("insert notification", errors.New("no rows inserted"))
	}
	if _, err := queries.InsertNotificationChange(ctx, notificationdb.InsertNotificationChangeParams{
		OwnerUserID: notification.OwnerUserID, ChangeSequence: sequence,
		NotificationID: notification.ID, ChangeType: domain.ChangeCreated,
		Revision: sequence, OccurredAt: notification.CreatedAt,
	}); err != nil {
		return domain.Notification{}, false, storeError("insert notification change", err)
	}
	if _, err := queries.InsertNotificationSourceReceipt(ctx, notificationdb.InsertNotificationSourceReceiptParams{
		SourceProcess: notification.SourceProcess, SourceID: notification.SourceID,
		SourceDigest: notification.SourceDigest, NotificationID: notification.ID,
		RecordedAt: notification.CreatedAt,
	}); err != nil {
		if existing, replayErr := r.replayTx(ctx, queries, notification); replayErr == nil && existing != nil {
			return *existing, false, nil
		}
		return domain.Notification{}, false, storeError("insert notification receipt", err)
	}
	return notification, true, nil
}

func sourceRequestLockKey(sourceProcess, sourceID string) string {
	sum := sha256.Sum256([]byte("workos.notification-source-lock.v1\x00" + sourceProcess + "\x00" + sourceID))
	return hex.EncodeToString(sum[:])
}

// replayTx classifies an existing projection for the same source fact:
// identical digest → exact replay; drifted digest → stored corruption.
func (r *Repository) replayTx(ctx context.Context, queries *notificationdb.Queries, notification domain.Notification) (*domain.Notification, error) {
	receipt, err := queries.GetNotificationSourceReceipt(ctx, notificationdb.GetNotificationSourceReceiptParams{
		SourceProcess: notification.SourceProcess, SourceID: notification.SourceID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, storeError("query notification receipt", err)
	}
	if receipt.SourceDigest != notification.SourceDigest {
		return nil, ports.ErrSourceDigestDrift
	}
	fact, err := r.storedByIDTx(ctx, queries, receipt.NotificationID)
	if err != nil {
		return nil, err
	}
	if fact.OwnerUserID != notification.OwnerUserID || fact.ProjectID != notification.ProjectID ||
		fact.Kind != notification.Kind || fact.Severity != notification.Severity ||
		fact.Origin != notification.Origin || fact.Title != notification.Title || fact.Body != notification.Body ||
		fact.TargetKind != notification.TargetKind || fact.TargetID != notification.TargetID ||
		fact.AppID != notification.AppID || fact.AppInstallationID != notification.AppInstallationID ||
		fact.SourceProcess != notification.SourceProcess || fact.SourceID != notification.SourceID ||
		fact.SourceDigest != notification.SourceDigest {
		return nil, domain.ErrCorrupt
	}
	return &fact, nil
}

func dereplay(existing *domain.Notification) domain.Notification {
	if existing == nil {
		return domain.Notification{}
	}
	return *existing
}

func (r *Repository) storedByIDTx(ctx context.Context, queries *notificationdb.Queries, id string) (domain.Notification, error) {
	row, err := queries.GetNotificationByID(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Notification{}, domain.ErrCorrupt
	}
	if err != nil {
		return domain.Notification{}, storeError("query stored notification", err)
	}
	fact := notificationFromRow(row.ID, row.OwnerUserID, row.ProjectID, row.Kind, row.Severity,
		row.Origin, row.Title, row.Body, row.TargetKind, row.TargetID, row.AppID,
		row.AppInstallationID, row.SourceProcess, row.SourceID, row.SourceDigest,
		row.CreatedAt, row.CreatedChangeSequence, row.ReadAt, row.ReadChangeSequence)
	if err := domain.ValidStoredNotification(fact); err != nil {
		return domain.Notification{}, err
	}
	return fact, nil
}

func (r *Repository) OwnerNotification(ctx context.Context, ownerUserID, notificationID string) (domain.Notification, error) {
	row, err := r.queries.GetOwnerNotification(ctx, notificationdb.GetOwnerNotificationParams{
		OwnerUserID: ownerUserID, ID: notificationID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Notification{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Notification{}, storeError("query owner notification", err)
	}
	fact := notificationFromRow(row.ID, row.OwnerUserID, row.ProjectID, row.Kind, row.Severity,
		row.Origin, row.Title, row.Body, row.TargetKind, row.TargetID, row.AppID,
		row.AppInstallationID, row.SourceProcess, row.SourceID, row.SourceDigest,
		row.CreatedAt, row.CreatedChangeSequence, row.ReadAt, row.ReadChangeSequence)
	if err := domain.ValidStoredNotification(fact); err != nil {
		return domain.Notification{}, err
	}
	return fact, nil
}

func (r *Repository) ListPage(ctx context.Context, ownerUserID string, filter ports.Filter, cursor ports.Cursor, limit int) (ports.Page, error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return ports.Page{}, storeError("begin notification list snapshot", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	queries := r.queries.WithTx(tx)
	rows, err := queries.ListNotificationsPage(ctx, notificationdb.ListNotificationsPageParams{
		OwnerUserID:   ownerUserID,
		ProjectID:     uuidParam(filter.ProjectID),
		UnreadOnly:    filter.UnreadOnly,
		Kind:          textParam(filter.Kind),
		CursorCreated: pgTimestampParam(cursor.CreatedAt),
		CursorID:      uuidParam(cursor.ID),
		RowLimit:      int32(limit + 1),
	})
	if err != nil {
		return ports.Page{}, storeError("list notifications", err)
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	facts := make([]domain.Notification, 0, len(rows))
	for _, row := range rows {
		fact := notificationFromRow(row.ID, row.OwnerUserID, row.ProjectID, row.Kind, row.Severity,
			row.Origin, row.Title, row.Body, row.TargetKind, row.TargetID, row.AppID,
			row.AppInstallationID, row.SourceProcess, row.SourceID, row.SourceDigest,
			row.CreatedAt, row.CreatedChangeSequence, row.ReadAt, row.ReadChangeSequence)
		if err := domain.ValidStoredNotification(fact); err != nil {
			return ports.Page{}, err
		}
		facts = append(facts, fact)
	}
	summary, err := r.summary(ctx, queries, ownerUserID)
	if err != nil {
		return ports.Page{}, err
	}
	page := ports.Page{Notifications: facts, UnreadCount: summary.UnreadCount, Watermark: summary.Watermark, HasMore: hasMore}
	if hasMore && len(facts) > 0 {
		last := facts[len(facts)-1]
		page.NextCursor = ports.Cursor{CreatedAt: last.CreatedAt, ID: last.ID}
	}
	if err := tx.Commit(ctx); err != nil {
		return ports.Page{}, storeError("commit notification list snapshot", err)
	}
	return page, nil
}

func (r *Repository) Summary(ctx context.Context, ownerUserID string) (ports.Summary, error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return ports.Summary{}, storeError("begin notification summary snapshot", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	summary, err := r.summary(ctx, r.queries.WithTx(tx), ownerUserID)
	if err != nil {
		return ports.Summary{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ports.Summary{}, storeError("commit notification summary snapshot", err)
	}
	return summary, nil
}

func (r *Repository) summary(ctx context.Context, queries *notificationdb.Queries, ownerUserID string) (ports.Summary, error) {
	unread, err := queries.CountOwnerUnread(ctx, ownerUserID)
	if err != nil {
		return ports.Summary{}, storeError("count unread notifications", err)
	}
	watermark, err := queries.GetOwnerChangeWatermark(ctx, ownerUserID)
	if err != nil {
		return ports.Summary{}, storeError("query notification watermark", err)
	}
	swept, err := queries.GetOwnerSweptThrough(ctx, ownerUserID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return ports.Summary{}, storeError("query notification sweep watermark", err)
	}
	return ports.Summary{UnreadCount: unread, Watermark: watermark, Swept: swept}, nil
}

func (r *Repository) LockForRead(ctx context.Context, tx dbtx.Tx, ownerUserID string, ids []string) ([]domain.Notification, error) {
	rows, err := r.queries.WithTx(tx).LockOwnerNotifications(ctx, notificationdb.LockOwnerNotificationsParams{
		OwnerUserID: ownerUserID, Ids: ids,
	})
	if err != nil {
		return nil, storeError("lock notifications for read", err)
	}
	facts := make([]domain.Notification, 0, len(rows))
	for _, row := range rows {
		fact := notificationFromRow(row.ID, row.OwnerUserID, row.ProjectID, row.Kind, row.Severity,
			row.Origin, row.Title, row.Body, row.TargetKind, row.TargetID, row.AppID,
			row.AppInstallationID, row.SourceProcess, row.SourceID, row.SourceDigest,
			row.CreatedAt, row.CreatedChangeSequence, row.ReadAt, row.ReadChangeSequence)
		if err := domain.ValidStoredNotification(fact); err != nil {
			return nil, err
		}
		facts = append(facts, fact)
	}
	return facts, nil
}

// MarkReadTx applies the monotonic read projection inside the caller's
// transaction: one freshly allocated change sequence per newly-read fact,
// already-read facts are deterministic no-ops without a duplicate change.
func (r *Repository) MarkReadTx(ctx context.Context, tx dbtx.Tx, ownerUserID string, notifications []domain.Notification, now time.Time) ([]domain.Notification, error) {
	queries := r.queries.WithTx(tx)
	updated := make([]domain.Notification, 0, len(notifications))
	for _, fact := range notifications {
		if fact.Read() {
			updated = append(updated, fact)
			continue
		}
		sequence, err := queries.AllocateNotificationChangeSequence(ctx, notificationdb.AllocateNotificationChangeSequenceParams{
			OwnerUserID: ownerUserID, UpdatedAt: now,
		})
		if err != nil {
			return nil, storeError("allocate read change sequence", err)
		}
		rows, err := queries.MarkNotificationRead(ctx, notificationdb.MarkNotificationReadParams{
			ReadAt: &now, ReadChangeSequence: sequence, ID: fact.ID, OwnerUserID: ownerUserID,
		})
		if err != nil {
			return nil, storeError("mark notification read", err)
		}
		if rows == 0 {
			// Concurrent winner inside the same lock scope is corruption of
			// the locking protocol; never a silent no-op.
			return nil, domain.ErrCorrupt
		}
		if _, err := queries.InsertNotificationChange(ctx, notificationdb.InsertNotificationChangeParams{
			OwnerUserID: ownerUserID, ChangeSequence: sequence, NotificationID: fact.ID,
			ChangeType: domain.ChangeRead, Revision: sequence, OccurredAt: now,
		}); err != nil {
			return nil, storeError("insert read change", err)
		}
		fact.ReadAt, fact.ReadChangeSequence = now, sequence
		updated = append(updated, fact)
	}
	return updated, nil
}

// UnreadTx counts unread facts inside the caller's transaction.
func (r *Repository) UnreadTx(ctx context.Context, tx dbtx.Tx, ownerUserID string) (int64, error) {
	count, err := r.queries.WithTx(tx).CountOwnerUnread(ctx, ownerUserID)
	if err != nil {
		return 0, storeError("count unread notifications", err)
	}
	return count, nil
}

func (r *Repository) GetReadRequestTx(ctx context.Context, tx dbtx.Tx, ownerUserID, idempotencyKey string) (ports.ReadRequestRecord, bool, error) {
	row, err := r.queries.WithTx(tx).GetNotificationReadRequest(ctx, notificationdb.GetNotificationReadRequestParams{
		OwnerUserID: ownerUserID, IdempotencyKey: idempotencyKey,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ports.ReadRequestRecord{}, false, nil
	}
	if err != nil {
		return ports.ReadRequestRecord{}, false, storeError("query notification read request", err)
	}
	return ports.ReadRequestRecord{
		RequestDigest: row.RequestDigest, ResultVersion: row.ResultVersion, Result: row.Result,
	}, true, nil
}

func (r *Repository) SaveReadRequest(ctx context.Context, tx dbtx.Tx, record ports.ReadRequestRecord) error {
	rows, err := r.queries.WithTx(tx).InsertNotificationReadRequest(ctx, notificationdb.InsertNotificationReadRequestParams{
		OwnerUserID: record.OwnerUserID, IdempotencyKey: record.IdempotencyKey,
		RequestDigest: record.RequestDigest, Result: record.Result, CreatedAt: record.CreatedAt,
	})
	if err != nil {
		return storeError("save notification read request", err)
	}
	if rows == 0 {
		return storeError("save notification read request", errors.New("no rows inserted"))
	}
	return nil
}

func (r *Repository) ChangesAfter(ctx context.Context, ownerUserID string, after int64, limit int) ([]domain.Change, error) {
	rows, err := r.queries.GetChangesAfter(ctx, notificationdb.GetChangesAfterParams{
		OwnerUserID: ownerUserID, AfterSequence: after, RowLimit: int32(limit),
	})
	if err != nil {
		return nil, storeError("query notification changes", err)
	}
	changes := make([]domain.Change, 0, len(rows))
	for _, row := range rows {
		fact := notificationFromRow(row.NotificationID, ownerUserID, row.ProjectID, row.Kind, row.Severity,
			row.Origin, row.Title, row.Body, row.TargetKind, row.TargetID, row.AppID,
			row.AppInstallationID, row.SourceProcess, row.SourceID, row.SourceDigest,
			row.CreatedAt, row.CreatedChangeSequence, row.ReadAt, row.ReadChangeSequence)
		if err := domain.ValidStoredNotification(fact); err != nil {
			return nil, err
		}
		// Restore the created time from the change fact row is not possible
		// here; the change's own payload carries it on the wire.
		changes = append(changes, domain.Change{
			OwnerUserID: ownerUserID, ChangeSequence: row.ChangeSequence,
			NotificationID: row.NotificationID, ChangeType: row.ChangeType,
			Revision: row.Revision, Notification: fact,
		})
	}
	return changes, nil
}

func (r *Repository) StreamGap(ctx context.Context, ownerUserID string) (int64, error) {
	swept, err := r.queries.GetOwnerSweptThrough(ctx, ownerUserID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, storeError("query notification stream gap", err)
	}
	return swept, nil
}

// SweepRead retires one bounded batch of old read facts and advances each
// affected owner's sweep watermark inside one transaction. Unread facts are
// never candidates.
func (r *Repository) SweepRead(ctx context.Context, cutoff time.Time, maxBatch int) (int, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, storeError("begin notification sweep", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	queries := r.queries.WithTx(tx)
	rows, err := queries.SelectSweepableNotifications(ctx, notificationdb.SelectSweepableNotificationsParams{
		Cutoff: &cutoff, MaxBatch: int32(maxBatch),
	})
	if err != nil {
		return 0, storeError("select sweepable notifications", err)
	}
	if len(rows) == 0 {
		return 0, nil
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	watermarks, err := queries.MaxChangeSequenceForNotifications(ctx, ids)
	if err != nil {
		return 0, storeError("query sweep watermarks", err)
	}
	if _, err := queries.DeleteNotificationChangesFor(ctx, ids); err != nil {
		return 0, storeError("delete swept notification changes", err)
	}
	if _, err := queries.DeleteNotifications(ctx, ids); err != nil {
		return 0, storeError("delete swept notifications", err)
	}
	// Receipts outlive notifications on a longer horizon so a pathological
	// late replay can never project a second notification.
	if _, err := queries.DeleteOldSourceReceipts(ctx, cutoff.Add(-ReceiptRetentionExtension)); err != nil {
		return 0, storeError("delete old source receipts", err)
	}
	for _, watermark := range watermarks {
		if _, err := queries.AdvanceOwnerSweptThrough(ctx, notificationdb.AdvanceOwnerSweptThroughParams{
			OwnerUserID: watermark.OwnerUserID, SweptThrough: watermark.MaxSeq, UpdatedAt: cutoff,
		}); err != nil {
			return 0, storeError("advance notification sweep watermark", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, storeError("commit notification sweep", err)
	}
	return len(ids), nil
}

// --- row/type helpers -------------------------------------------------

func notificationFromRow(id, ownerUserID string, projectID pgtype.UUID, kind, severity, origin,
	title, body, targetKind string, targetID string, appID pgtype.Text, appInstallationID pgtype.UUID,
	sourceProcess, sourceID, sourceDigest string,
	createdAt time.Time, createdChangeSequence int64, readAt *time.Time, readChangeSequence int64) domain.Notification {
	fact := domain.Notification{
		ID: id, OwnerUserID: ownerUserID, Kind: kind, Severity: severity, Origin: origin,
		Title: title, Body: body, TargetKind: targetKind, TargetID: targetID,
		AppID: appID.String, AppInstallationID: uuidString(appInstallationID),
		SourceProcess: sourceProcess, SourceID: sourceID,
		SourceDigest: sourceDigest, CreatedAt: createdAt, CreatedChangeSequence: createdChangeSequence,
		ReadChangeSequence: readChangeSequence,
	}
	if projectID.Valid {
		fact.ProjectID = uuidString(projectID)
	}
	if appID.Valid {
		fact.AppID = appID.String
	}
	if readAt != nil {
		fact.ReadAt = *readAt
	}
	return fact
}

func uuidParam(value string) pgtype.UUID {
	if value == "" {
		return pgtype.UUID{}
	}
	parsed, err := uuid.Parse(value)
	if err != nil {
		return pgtype.UUID{}
	}
	return pgtype.UUID{Bytes: parsed, Valid: true}
}

func uuidString(value pgtype.UUID) string {
	if !value.Valid {
		return ""
	}
	return value.String()
}

func textParam(value string) pgtype.Text {
	return pgtype.Text{String: value, Valid: value != ""}
}

func pgTimestampParam(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	return &value
}

// --- app request idempotency + quota (ADR-0014) -------------------------

func (r *Repository) LastOwnerSequenceTx(ctx context.Context, tx dbtx.Tx, ownerUserID string) (int64, error) {
	sequence, err := r.queries.WithTx(tx).GetOwnerLastSequence(ctx, ownerUserID)
	if err != nil {
		return 0, storeError("query owner sequence", err)
	}
	return sequence, nil
}

func (r *Repository) GetAppRequestTx(ctx context.Context, tx dbtx.Tx, ownerUserID, appInstanceID, idempotencyKey string) (ports.AppRequestRecord, bool, error) {
	row, err := r.queries.WithTx(tx).GetNotificationAppRequest(ctx, notificationdb.GetNotificationAppRequestParams{
		OwnerUserID: ownerUserID, AppInstallationID: appInstanceID, IdempotencyKey: idempotencyKey,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ports.AppRequestRecord{}, false, nil
	}
	if err != nil {
		return ports.AppRequestRecord{}, false, storeError("query notification app request", err)
	}
	return ports.AppRequestRecord{
		RequestDigest: row.RequestDigest, ResultVersion: row.ResultVersion, Result: row.Result,
	}, true, nil
}

func (r *Repository) SaveAppRequest(ctx context.Context, tx dbtx.Tx, record ports.AppRequestRecord) error {
	rows, err := r.queries.WithTx(tx).InsertNotificationAppRequest(ctx, notificationdb.InsertNotificationAppRequestParams{
		OwnerUserID: record.OwnerUserID, AppInstallationID: record.AppInstanceID,
		IdempotencyKey: record.IdempotencyKey, RequestDigest: record.RequestDigest,
		Result: record.Result, CreatedAt: record.CreatedAt,
	})
	if err != nil {
		return storeError("save notification app request", err)
	}
	if rows == 0 {
		return storeError("save notification app request", errors.New("no rows inserted"))
	}
	return nil
}

func (r *Repository) LockAppQuota(ctx context.Context, tx dbtx.Tx, ownerUserID, appInstanceID string) (ports.AppQuotaBucket, error) {
	row, err := r.queries.WithTx(tx).GetNotificationAppQuotaForUpdate(ctx, notificationdb.GetNotificationAppQuotaForUpdateParams{
		OwnerUserID: ownerUserID, AppInstallationID: appInstanceID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ports.AppQuotaBucket{}, ports.ErrNoAppQuotaBucket
	}
	if err != nil {
		return ports.AppQuotaBucket{}, storeError("lock app quota bucket", err)
	}
	return ports.AppQuotaBucket{
		UtcDate: row.UtcDate.Time, DailyCount: int(row.DailyCount),
		BurstWindowStart: row.BurstWindowStart, BurstCount: int(row.BurstCount),
	}, nil
}

func (r *Repository) InsertAppQuota(ctx context.Context, tx dbtx.Tx, ownerUserID, appInstanceID string, now time.Time) (ports.AppQuotaBucket, error) {
	rows, err := r.queries.WithTx(tx).InsertNotificationAppQuota(ctx, notificationdb.InsertNotificationAppQuotaParams{
		OwnerUserID: ownerUserID, AppInstallationID: appInstanceID,
		UtcDate:          pgtype.Date{Time: now.Truncate(24 * time.Hour), Valid: true},
		BurstWindowStart: now, UpdatedAt: now,
	})
	if err != nil {
		return ports.AppQuotaBucket{}, storeError("insert app quota bucket", err)
	}
	if rows == 0 {
		// A concurrent first request created the bucket. ON CONFLICT DO
		// NOTHING waited for it; lock and use that committed authority.
		return r.LockAppQuota(ctx, tx, ownerUserID, appInstanceID)
	}
	return ports.AppQuotaBucket{UtcDate: now.Truncate(24 * time.Hour), BurstWindowStart: now}, nil
}

func (r *Repository) UpdateAppQuota(ctx context.Context, tx dbtx.Tx, ownerUserID, appInstanceID string, update ports.AppQuotaUpdate) error {
	rows, err := r.queries.WithTx(tx).UpdateNotificationAppQuota(ctx, notificationdb.UpdateNotificationAppQuotaParams{
		OwnerUserID: ownerUserID, AppInstallationID: appInstanceID,
		UtcDate:          pgtype.Date{Time: update.UtcDate, Valid: true},
		DailyCount:       int32(update.DailyCount),
		BurstWindowStart: update.BurstWindowStart,
		BurstCount:       int32(update.BurstCount),
		UpdatedAt:        update.UpdatedAt,
	})
	if err != nil {
		return storeError("update app quota bucket", err)
	}
	if rows == 0 {
		return domain.ErrCorrupt
	}
	return nil
}
