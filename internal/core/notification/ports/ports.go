// Ports of the Core notification module. The transaction-scoped sink is the
// only way another Core module projects a notification: it always joins the
// caller's source-mutation transaction, so the fact commits exactly when its
// source commits and a notification failure rolls the source back
// (ADR-0014). The command store and read store are consumed by the module's
// application services only.
package ports

import (
	"context"
	"errors"
	"time"

	"github.com/yangtao121/workos/internal/core/notification/domain"
	"github.com/yangtao121/workos/internal/platform/dbtx"
)

// ErrStoreUnavailable marks a temporarily unreachable notification store.
var ErrStoreUnavailable = errors.New("notification store is temporarily unavailable")

// ErrNoAppQuotaBucket marks an installation without a quota bucket yet.
var ErrNoAppQuotaBucket = errors.New("app quota bucket does not exist")

// ErrAppNotificationDenied is the single sanitized denial verdict for a
// missing installation, a stale grant epoch, or a missing
// notifications.create grant. The neutral authorizer maps every module
// verdict onto it so the caller can never distinguish why.
var ErrAppNotificationDenied = errors.New("app notification is not authorized")

// AppInstallationFacts is the authoritative app identity derived by the
// neutral installation authorizer.
type AppInstallationFacts struct {
	AppID string
}

// AppRequestRecord is one consumed app create idempotency key with its
// versioned first-response snapshot.
type AppRequestRecord struct {
	OwnerUserID    string
	AppInstanceID  string
	IdempotencyKey string
	RequestDigest  string
	ResultVersion  int32
	Result         []byte
	CreatedAt      time.Time
}

// AppQuotaBucket is one installation's atomic quota state.
type AppQuotaBucket struct {
	UtcDate          time.Time
	DailyCount       int
	BurstWindowStart time.Time
	BurstCount       int
}

// AppQuotaUpdate is one guarded reservation write.
type AppQuotaUpdate struct {
	UtcDate          time.Time
	DailyCount       int
	BurstWindowStart time.Time
	BurstCount       int
	UpdatedAt        time.Time
}

// ErrSourceDigestDrift marks a receipt whose stored digest differs from the
// replayed source digest: same source, different fact — stored corruption or
// contract violation, never an update.
var ErrSourceDigestDrift = errors.New("notification source digest drifted")

// TxSink appends exactly-once notification projections inside the caller's
// source-mutation transaction. The receipt arbitration makes replays,
// concurrent duplicates, and response losses no-ops; any real failure must
// fail the caller's transaction (notification facts are a hard requirement
// for system sources per ADR-0014).
type TxSink interface {
	AppendSystemNotification(ctx context.Context, tx dbtx.Tx, fact domain.SystemFact, occurredAt time.Time) (domain.Notification, error)
}

// NotificationStore is the durable authority over facts, the owner change
// stream, and read state.
type NotificationStore interface {
	// SerializeRequestTx takes a transaction-scoped lock for one canonical
	// command/source key. It closes the check-then-insert race without a
	// process-local mutex; hash collisions only add harmless serialization.
	SerializeRequestTx(ctx context.Context, tx dbtx.Tx, key string) error
	// AppendTx projects one prepared notification inside the caller's
	// transaction: notification row + CREATED change + source receipt, with
	// the change sequence allocated from the owner counter. Replay-safe:
	// an existing receipt (same digest) returns the stored fact; a digest
	// drift is ErrSourceDigestDrift.
	AppendTx(ctx context.Context, tx dbtx.Tx, notification domain.Notification) (domain.Notification, bool, error)
	// OwnerNotification reads one owner-scoped fact with revalidation.
	OwnerNotification(ctx context.Context, ownerUserID, notificationID string) (domain.Notification, error)
	// ListPage reads one keyset page (already limit+1 probed by the
	// repository) plus the owner unread count and current watermark.
	ListPage(ctx context.Context, ownerUserID string, filter Filter, cursor Cursor, limit int) (Page, error)
	// Summary returns the owner unread count, monotonic owner-sequence
	// watermark, and sweep boundary from one database snapshot.
	Summary(ctx context.Context, ownerUserID string) (Summary, error)
	// MarkReadTx applies the read projection for a locked set of facts
	// inside the caller's transaction and appends the READ changes.
	MarkReadTx(ctx context.Context, tx dbtx.Tx, ownerUserID string, notifications []domain.Notification, now time.Time) ([]domain.Notification, error)
	// UnreadTx counts unread facts inside the caller's transaction, so the
	// read command's first response reflects its own committed effect.
	UnreadTx(ctx context.Context, tx dbtx.Tx, ownerUserID string) (int64, error)
	// LockForRead locks the given owner-scoped facts for a read command.
	// Missing facts are absent from the result.
	LockForRead(ctx context.Context, tx dbtx.Tx, ownerUserID string, ids []string) ([]domain.Notification, error)
	// GetReadRequestTx returns a consumed read-command key inside the caller's
	// transaction.
	GetReadRequestTx(ctx context.Context, tx dbtx.Tx, ownerUserID, idempotencyKey string) (ReadRequestRecord, bool, error)
	// SaveReadRequest consumes a read-command key inside the caller's
	// transaction.
	SaveReadRequest(ctx context.Context, tx dbtx.Tx, record ReadRequestRecord) error
	// ChangesAfter reads the change stream after a cursor (bounded batch).
	ChangesAfter(ctx context.Context, ownerUserID string, after int64, limit int) ([]domain.Change, error)
	// StreamGap reports the authoritative sweep watermark for the owner.
	StreamGap(ctx context.Context, ownerUserID string) (int64, error)
	// LastOwnerSequenceTx reports the owner counter's current sequence inside
	// the caller's transaction (the just-allocated created sequence).
	LastOwnerSequenceTx(ctx context.Context, tx dbtx.Tx, ownerUserID string) (int64, error)
	// GetAppRequestTx returns a consumed app create key inside the caller's
	// transaction.
	GetAppRequestTx(ctx context.Context, tx dbtx.Tx, ownerUserID, appInstanceID, idempotencyKey string) (AppRequestRecord, bool, error)
	// SaveAppRequest consumes an app create key inside the caller's
	// transaction.
	SaveAppRequest(ctx context.Context, tx dbtx.Tx, record AppRequestRecord) error
	// LockAppQuota locks the installation's bucket row for reservation.
	LockAppQuota(ctx context.Context, tx dbtx.Tx, ownerUserID, appInstanceID string) (AppQuotaBucket, error)
	// InsertAppQuota creates the installation's bucket row inside the
	// caller's transaction.
	InsertAppQuota(ctx context.Context, tx dbtx.Tx, ownerUserID, appInstanceID string, now time.Time) (AppQuotaBucket, error)
	// UpdateAppQuota writes the reserved bucket state inside the caller's
	// transaction.
	UpdateAppQuota(ctx context.Context, tx dbtx.Tx, ownerUserID, appInstanceID string, update AppQuotaUpdate) error
	// SweepRead retires one bounded batch of old read facts and advances
	// the per-owner sweep watermark. It never touches unread facts.
	SweepRead(ctx context.Context, cutoff time.Time, maxBatch int) (int, error)
}

// Filter is the bounded owner list filter.
type Filter struct {
	ProjectID  string
	UnreadOnly bool
	Kind       string
}

// Cursor is the keyset continuation (created_at, id); zero time means the
// first page.
type Cursor struct {
	CreatedAt time.Time
	ID        string
}

// Page is one explicit list page with its owner snapshot facts.
type Page struct {
	Notifications []domain.Notification
	UnreadCount   int64
	Watermark     int64
	// HasMore reports whether another page exists (limit+1 probe consumed
	// by the repository before returning).
	HasMore    bool
	NextCursor Cursor
}

// Summary is the owner-wide badge snapshot.
type Summary struct {
	UnreadCount int64
	Watermark   int64
	Swept       int64
}

// ReadRequestRecord is one consumed read-command idempotency key with its
// versioned first-response snapshot.
type ReadRequestRecord struct {
	OwnerUserID    string
	IdempotencyKey string
	RequestDigest  string
	ResultVersion  int32
	Result         []byte
	CreatedAt      time.Time
}
