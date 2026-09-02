// App notification ingest application service (ADR-0014): the Core-private
// authority behind the runtime App Bridge `notifications.create` method.
// Every call re-verifies the active installation through the neutral port,
// then adjudicates idempotency, quota, and the notification projection in
// one PostgreSQL transaction: replays return the versioned first response
// exactly, conflicts are stable, quota exhaustion never consumes the key,
// and failures leave zero side effects.
package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/yangtao121/workos/internal/core/notification/domain"
	"github.com/yangtao121/workos/internal/core/notification/ports"
	"github.com/yangtao121/workos/internal/platform/dbtx"
)

// App quota bounds (ADR-0014): a short burst window plus a UTC daily hard
// cap, enforced atomically in PostgreSQL. The values are deliberately
// conservative; system notifications never consume app quota.
const (
	AppBurstWindowSeconds = 60
	AppBurstMax           = 10
	AppDailyMax           = 200
)

var ErrAppExhausted = errors.New("app notification allowance is exhausted")

// AppInstallationAuthorizer is the neutral port over the installation
// authority. It must re-verify the active installation, the exact current
// grant revision, and the notifications.create grant; denial is sanitized
// and leaves zero side effects.
type AppInstallationAuthorizer interface {
	AuthorizeAppNotification(ctx context.Context, ownerUserID, projectID, appInstanceID string, installationGrantRevision int64) (ports.AppInstallationFacts, error)
}

// CreateAppNotificationInput is one bounded create command.
type CreateAppNotificationInput struct {
	OwnerUserID               string
	ProjectID                 string
	AppInstanceID             string
	InstallationGrantRevision int64
	IdempotencyKey            string
	Title                     string
	Body                      string
}

// CreateAppNotificationResult is the first response or its exact replay.
type CreateAppNotificationResult struct {
	Notification   domain.Notification
	ChangeSequence int64
	UnreadCount    int64
}

// appRequestSnapshot is the versioned first-response snapshot.
type appRequestSnapshot struct {
	Version        int                 `json:"version"`
	Notification   domain.Notification `json:"notification"`
	ChangeSequence int64               `json:"change_sequence"`
	UnreadCount    int64               `json:"unread_count"`
}

// CreateAppNotification adjudicates one app create command.
func (s *Service) CreateAppNotification(ctx context.Context, input CreateAppNotificationInput) (CreateAppNotificationResult, error) {
	if !domain.ValidUUID(input.OwnerUserID) || !domain.ValidUUID(input.ProjectID) || !domain.ValidUUID(input.AppInstanceID) {
		return CreateAppNotificationResult{}, ErrInvalid
	}
	if input.InstallationGrantRevision <= 0 || !domain.ValidIdempotencyKey(input.IdempotencyKey) {
		return CreateAppNotificationResult{}, ErrInvalid
	}
	title, body, err := ValidAppText(input.Title, input.Body)
	if err != nil {
		return CreateAppNotificationResult{}, ErrInvalid
	}
	digest := appNotificationDigest(input.ProjectID, input.AppInstanceID, title, body)
	now := domain.CanonicalUTCTime(s.now())

	if record, found, err := s.store.GetAppRequest(ctx, input.OwnerUserID, input.AppInstanceID, input.IdempotencyKey); err != nil {
		return CreateAppNotificationResult{}, err
	} else if found {
		// Replay-first: a consumed key is adjudicated before anything else.
		if record.RequestDigest != digest || record.ResultVersion != 1 {
			return CreateAppNotificationResult{}, ErrConflict
		}
		var first appRequestSnapshot
		if err := json.Unmarshal(record.Result, &first); err != nil {
			return CreateAppNotificationResult{}, fmt.Errorf("decode app notification first response: %w", domain.ErrCorrupt)
		}
		if first.Version != 1 {
			return CreateAppNotificationResult{}, domain.ErrCorrupt
		}
		if err := domain.ValidStoredNotification(first.Notification); err != nil {
			return CreateAppNotificationResult{}, err
		}
		if first.Notification.OwnerUserID != input.OwnerUserID || first.Notification.ProjectID != input.ProjectID ||
			first.Notification.AppInstallationID != input.AppInstanceID {
			return CreateAppNotificationResult{}, domain.ErrCorrupt
		}
		return CreateAppNotificationResult{
			Notification: first.Notification, ChangeSequence: first.ChangeSequence, UnreadCount: first.UnreadCount,
		}, nil
	}

	facts, err := s.appAuthorizer.AuthorizeAppNotification(ctx, input.OwnerUserID, input.ProjectID, input.AppInstanceID, input.InstallationGrantRevision)
	if err != nil {
		return CreateAppNotificationResult{}, err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return CreateAppNotificationResult{}, storeFailure("begin app notification", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if err := s.reserveAppQuotaTx(ctx, tx, input.OwnerUserID, input.AppInstanceID, now); err != nil {
		return CreateAppNotificationResult{}, err
	}
	notification, err := domain.PrepareAppNotification(domain.AppNotificationFact{
		OwnerUserID: input.OwnerUserID, ProjectID: input.ProjectID,
		AppInstanceID: input.AppInstanceID, AppID: facts.AppID,
		IdempotencyKey: input.IdempotencyKey,
		Title:          title, Body: body,
	}, now)
	if err != nil {
		return CreateAppNotificationResult{}, err
	}
	projected, created, err := s.store.AppendTx(ctx, tx, notification)
	if err != nil {
		return CreateAppNotificationResult{}, err
	}
	if !created {
		// A same-source replay inside a fresh app key is a contract
		// violation: two distinct app keys must never share a source fact.
		return CreateAppNotificationResult{}, domain.ErrCorrupt
	}
	sequence, err := s.store.LastOwnerSequenceTx(ctx, tx, input.OwnerUserID)
	if err != nil {
		return CreateAppNotificationResult{}, err
	}
	unread, err := s.store.UnreadTx(ctx, tx, input.OwnerUserID)
	if err != nil {
		return CreateAppNotificationResult{}, err
	}
	result := CreateAppNotificationResult{Notification: projected, ChangeSequence: sequence, UnreadCount: unread}
	snapshot, err := json.Marshal(appRequestSnapshot{
		Version: 1, Notification: projected, ChangeSequence: sequence, UnreadCount: unread,
	})
	if err != nil {
		return CreateAppNotificationResult{}, fmt.Errorf("encode app notification first response: %w", domain.ErrCorrupt)
	}
	if err := s.store.SaveAppRequest(ctx, tx, ports.AppRequestRecord{
		OwnerUserID: input.OwnerUserID, AppInstanceID: input.AppInstanceID,
		IdempotencyKey: input.IdempotencyKey, RequestDigest: digest,
		ResultVersion: 1, Result: snapshot, CreatedAt: now,
	}); err != nil {
		return CreateAppNotificationResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return CreateAppNotificationResult{}, storeFailure("commit app notification", err)
	}
	return result, nil
}

// reserveAppQuotaTx locks the installation's bucket row and atomically
// reserves one slot inside the burst window and the UTC daily cap. A
// crossed bound fails the whole transaction and never consumes the key.
func (s *Service) reserveAppQuotaTx(ctx context.Context, tx dbtx.Tx, ownerUserID, appInstanceID string, now time.Time) error {
	bucket, err := s.store.LockAppQuota(ctx, tx, ownerUserID, appInstanceID)
	if errors.Is(err, ports.ErrNoAppQuotaBucket) {
		bucket, err = s.store.InsertAppQuota(ctx, tx, ownerUserID, appInstanceID, now)
		if err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	utcDay := now.Truncate(24 * time.Hour)
	dailyCount := bucket.DailyCount
	burstCount := bucket.BurstCount
	windowStart := bucket.BurstWindowStart
	if !bucket.UtcDate.Equal(utcDay) {
		dailyCount = 0
	}
	if windowStart.IsZero() || now.Sub(windowStart) >= AppBurstWindowSeconds*time.Second {
		windowStart = now
		burstCount = 0
	}
	if dailyCount+1 > AppDailyMax || burstCount+1 > AppBurstMax {
		return ErrAppExhausted
	}
	return s.store.UpdateAppQuota(ctx, tx, ownerUserID, appInstanceID, ports.AppQuotaUpdate{
		UtcDate: utcDay, DailyCount: dailyCount + 1,
		BurstWindowStart: windowStart, BurstCount: burstCount + 1, UpdatedAt: now,
	})
}

// ValidAppText validates and normalizes the bounded app text. Everything is
// inert plain text: valid UTF-8, no NUL/C0/C1 (LF allowed in bodies), code
// point, byte, and line bounds.
func ValidAppText(rawTitle, rawBody string) (string, string, error) {
	if !utf8.ValidString(rawTitle) || !utf8.ValidString(rawBody) {
		return "", "", domain.ErrInvalid
	}
	title := strings.TrimSpace(rawTitle)
	if utf8.RuneCountInString(title) < 1 || utf8.RuneCountInString(title) > domain.MaxAppTitleCodePoints ||
		len(title) > domain.MaxAppTitleBytes || strings.IndexFunc(title, isControl) >= 0 {
		return "", "", domain.ErrInvalid
	}
	body := strings.ReplaceAll(rawBody, "\r\n", "\n")
	body = strings.TrimRight(body, "\n")
	if utf8.RuneCountInString(body) > domain.MaxAppBodyCodePoints || len(body) > domain.MaxAppBodyBytes {
		return "", "", domain.ErrInvalid
	}
	if strings.IndexFunc(body, func(r rune) bool { return isControl(r) && r != '\n' }) >= 0 {
		return "", "", domain.ErrInvalid
	}
	if strings.Count(body, "\n") > domain.MaxAppBodyLines {
		return "", "", domain.ErrInvalid
	}
	return title, body, nil
}

func isControl(r rune) bool { return r < 0x20 || r == 0x7f }

func appNotificationDigest(projectID, appInstanceID, title, body string) string {
	canonical := fmt.Sprintf("workos.app-notification-create.v1|%s|%s|%s|%s", projectID, appInstanceID, title, body)
	sum := sha256.Sum256([]byte(canonical))
	return "sha256:" + hex.EncodeToString(sum[:])
}
