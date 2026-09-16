// Continuity persistence (ADR-0031): the runtime-owned surface attachment and
// control lease rows of migration 059. Attach and RequestControl run as one
// transaction each — the lease row and the attachment control flags always
// commit together, which is what makes takeover atomic and late observations
// converge.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yangtao121/workos/internal/platform/dbtransient"
	"github.com/yangtao121/workos/internal/runtime/surface/adapters/postgres/surfacedb"
	"github.com/yangtao121/workos/internal/runtime/surface/domain"
	"github.com/yangtao121/workos/internal/runtime/surface/ports"
)

// ContinuityRepository implements ports.ContinuityStore over the runtime pool.
type ContinuityRepository struct {
	pool    *pgxpool.Pool
	queries *surfacedb.Queries
}

// NewContinuity builds the continuity store on the runtime-owned pool.
func NewContinuity(pool *pgxpool.Pool) *ContinuityRepository {
	return &ContinuityRepository{pool: pool, queries: surfacedb.New(pool)}
}

func attachmentFromRow(row surfacedb.WorkosRuntimeSurfaceAttachment) domain.SurfaceAttachment {
	attachment := domain.SurfaceAttachment{
		ID:                row.AttachmentID,
		WorkloadID:        row.WorkloadID,
		SurfaceSessionID:  row.SurfaceSessionID,
		OwnerUserID:       row.OwnerUserID,
		ProjectID:         row.ProjectID,
		DeviceID:          row.DeviceID,
		IdempotencyKey:    row.IdempotencyKey,
		Controls:          row.Controls,
		ControlGeneration: row.ControlGeneration,
		State:             domain.AttachmentState(row.State),
		AttachedAt:        row.AttachedAt.Time,
	}
	if row.ControlExpiresAt.Valid {
		expires := row.ControlExpiresAt.Time
		attachment.ControlExpiresAt = &expires
	}
	if row.DetachedAt.Valid {
		detached := row.DetachedAt.Time
		attachment.DetachedAt = &detached
	}
	return attachment
}

func leaseFromRow(row surfacedb.WorkosRuntimeSurfaceControlLease) domain.ControlLease {
	return domain.ControlLease{
		WorkloadID:             row.WorkloadID,
		OwnerUserID:            row.OwnerUserID,
		ControlGeneration:      row.ControlGeneration,
		ControllerAttachmentID: row.ControllerAttachmentID,
		ControllerDeviceID:     row.ControllerDeviceID,
		GrantedAt:              row.GrantedAt.Time,
		ExpiresAt:              row.ExpiresAt.Time,
	}
}

func insertAttachmentParams(attachment domain.SurfaceAttachment, until time.Time) surfacedb.InsertSurfaceAttachmentParams {
	return surfacedb.InsertSurfaceAttachmentParams{
		AttachmentID: attachment.ID, WorkloadID: attachment.WorkloadID,
		SurfaceSessionID: attachment.SurfaceSessionID, OwnerUserID: attachment.OwnerUserID,
		ProjectID: attachment.ProjectID, DeviceID: attachment.DeviceID,
		IdempotencyKey: attachment.IdempotencyKey,
		Controls:       false, ControlGeneration: 0,
		State:            string(domain.AttachmentStateAttached),
		AttachedAt:       timestamp(attachment.AttachedAt),
		ControlExpiresAt: pgtype.Timestamptz{},
	}
}

func (r *ContinuityRepository) Attach(ctx context.Context, command ports.AttachCommand) (domain.SurfaceAttachment, domain.ControlLease, error) {
	attachment := command.Attachment
	if attachment.AttachedAt.IsZero() {
		attachment.AttachedAt = time.Now().UTC()
	}
	now := attachment.AttachedAt
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return domain.SurfaceAttachment{}, domain.ControlLease{}, continuityStoreError("begin surface attach", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	queries := r.queries.WithTx(tx)

	inserted, err := queries.InsertSurfaceAttachment(ctx, insertAttachmentParams(attachment, command.Until))
	if err != nil {
		return domain.SurfaceAttachment{}, domain.ControlLease{}, continuityStoreError("insert surface attachment", err)
	}
	if inserted == 0 {
		// Idempotent replay: the stored row is the whole verdict. An attach
		// replay never alters an existing control lease.
		stored, err := queries.GetSurfaceAttachmentByKey(ctx, surfacedb.GetSurfaceAttachmentByKeyParams{
			OwnerUserID: attachment.OwnerUserID, IdempotencyKey: attachment.IdempotencyKey,
		})
		if err != nil {
			return domain.SurfaceAttachment{}, domain.ControlLease{}, continuityAttachmentError("query surface attachment", err)
		}
		if stored.WorkloadID != attachment.WorkloadID || stored.DeviceID != attachment.DeviceID || stored.ProjectID != attachment.ProjectID {
			return domain.SurfaceAttachment{}, domain.ControlLease{}, domain.ErrInvalid
		}
		lease, found, err := readLease(ctx, queries, attachment.WorkloadID)
		if err != nil {
			return domain.SurfaceAttachment{}, domain.ControlLease{}, continuityStoreError("query control lease", err)
		}
		if !found {
			lease = domain.ControlLease{WorkloadID: attachment.WorkloadID, OwnerUserID: attachment.OwnerUserID}
		}
		if err := tx.Commit(ctx); err != nil {
			return domain.SurfaceAttachment{}, domain.ControlLease{}, continuityStoreError("commit surface attach", err)
		}
		return attachmentFromRow(stored), lease, nil
	}

	leaseRow, err := queries.LockSurfaceControlLease(ctx, attachment.WorkloadID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// Grant-on-first-attach: with no lease yet, the first attachment
		// becomes controller of generation 1. Any later attach observes the
		// row and stays a plain observer.
		lease := domain.ControlLease{
			WorkloadID: attachment.WorkloadID, OwnerUserID: attachment.OwnerUserID,
			ControlGeneration: 1, ControllerAttachmentID: attachment.ID, ControllerDeviceID: attachment.DeviceID,
			GrantedAt: now, ExpiresAt: command.Until,
		}
		if err := queries.InsertSurfaceControlLease(ctx, leaseParams(lease)); err != nil {
			return domain.SurfaceAttachment{}, domain.ControlLease{}, continuityStoreError("grant first surface control", err)
		}
		if err := markControl(ctx, queries, attachment.OwnerUserID, attachment.ID, true, lease.ControlGeneration, command.Until); err != nil {
			return domain.SurfaceAttachment{}, domain.ControlLease{}, err
		}
		leaseRow = surfacedb.WorkosRuntimeSurfaceControlLease{
			WorkloadID: lease.WorkloadID, OwnerUserID: lease.OwnerUserID, ControlGeneration: lease.ControlGeneration,
			ControllerAttachmentID: lease.ControllerAttachmentID, ControllerDeviceID: lease.ControllerDeviceID,
			GrantedAt: timestamp(lease.GrantedAt), ExpiresAt: timestamp(lease.ExpiresAt),
		}
	case err != nil:
		return domain.SurfaceAttachment{}, domain.ControlLease{}, continuityStoreError("lock surface control lease", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.SurfaceAttachment{}, domain.ControlLease{}, continuityStoreError("commit surface attach", err)
	}
	stored, err := r.queries.GetSurfaceAttachment(ctx, surfacedb.GetSurfaceAttachmentParams{
		OwnerUserID: attachment.OwnerUserID, AttachmentID: attachment.ID,
	})
	if err != nil {
		return domain.SurfaceAttachment{}, domain.ControlLease{}, continuityAttachmentError("query inserted surface attachment", err)
	}
	return attachmentFromRow(stored), leaseFromRow(leaseRow), nil
}

func (r *ContinuityRepository) RequestControl(ctx context.Context, attachment domain.SurfaceAttachment, now, until time.Time) (ports.ControlVerdict, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return ports.ControlVerdict{}, continuityStoreError("begin surface control request", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	queries := r.queries.WithTx(tx)

	leaseRow, err := queries.LockSurfaceControlLease(ctx, attachment.WorkloadID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return ports.ControlVerdict{}, continuityStoreError("lock surface control lease", err)
	}
	verdict := ports.ControlVerdict{}
	if errors.Is(err, pgx.ErrNoRows) {
		// No lease yet (e.g. the ledger was never created): the explicit
		// request itself grants generation 1.
		lease := domain.ControlLease{
			WorkloadID: attachment.WorkloadID, OwnerUserID: attachment.OwnerUserID,
			ControlGeneration: 1, ControllerAttachmentID: attachment.ID, ControllerDeviceID: attachment.DeviceID,
			GrantedAt: now, ExpiresAt: until,
		}
		if err := queries.InsertSurfaceControlLease(ctx, leaseParams(lease)); err != nil {
			return ports.ControlVerdict{}, continuityStoreError("grant surface control", err)
		}
		verdict.Lease = lease
	} else {
		lease := leaseFromRow(leaseRow)
		if lease.Matches(&attachment) && lease.ControlValid(now) {
			// Renewal by the exact live controller: expiry moves, generation
			// stays — it can never steal or disturb another holder.
			if err := lease.Renew(now, until, &attachment); err != nil {
				return ports.ControlVerdict{}, err
			}
			if err := queries.UpdateSurfaceControlLease(ctx, updateLeaseParams(attachment.WorkloadID, lease)); err != nil {
				return ports.ControlVerdict{}, continuityStoreError("renew surface control", err)
			}
			verdict.Lease = lease
			verdict.Renewed = true
		} else {
			// Explicit takeover: succeeds from any previous state (active,
			// expired, or detached controller) and atomically advances the
			// generation, invalidating the previous controller's flags.
			lease.Takeover(now, until, &attachment)
			if err := queries.UpdateSurfaceControlLease(ctx, updateLeaseParams(attachment.WorkloadID, lease)); err != nil {
				return ports.ControlVerdict{}, continuityStoreError("take over surface control", err)
			}
			if leaseRow.ControllerAttachmentID != attachment.ID {
				if _, err := queries.ClearSurfaceAttachmentControl(ctx, surfacedb.ClearSurfaceAttachmentControlParams{
					OwnerUserID: attachment.OwnerUserID, AttachmentID: leaseRow.ControllerAttachmentID,
					ControlGeneration: lease.ControlGeneration,
				}); err != nil {
					return ports.ControlVerdict{}, continuityStoreError("invalidate previous surface controller", err)
				}
			}
			verdict.Lease = lease
		}
	}
	if err := markControl(ctx, queries, attachment.OwnerUserID, attachment.ID, true, verdict.Lease.ControlGeneration, until); err != nil {
		return ports.ControlVerdict{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ports.ControlVerdict{}, continuityStoreError("commit surface control request", err)
	}
	stored, err := r.queries.GetSurfaceAttachment(ctx, surfacedb.GetSurfaceAttachmentParams{
		OwnerUserID: attachment.OwnerUserID, AttachmentID: attachment.ID,
	})
	if err != nil {
		return ports.ControlVerdict{}, continuityAttachmentError("query surface attachment", err)
	}
	verdict.Attachment = attachmentFromRow(stored)
	return verdict, nil
}

func (r *ContinuityRepository) Detach(ctx context.Context, ownerUserID, attachmentID string, now time.Time) (domain.SurfaceAttachment, error) {
	if _, err := r.queries.DetachSurfaceAttachment(ctx, surfacedb.DetachSurfaceAttachmentParams{
		OwnerUserID: ownerUserID, AttachmentID: attachmentID, Now: timestamp(now),
	}); err != nil {
		return domain.SurfaceAttachment{}, continuityStoreError("detach surface attachment", err)
	}
	row, err := r.queries.GetSurfaceAttachment(ctx, surfacedb.GetSurfaceAttachmentParams{
		OwnerUserID: ownerUserID, AttachmentID: attachmentID,
	})
	if err != nil {
		return domain.SurfaceAttachment{}, continuityAttachmentError("query detached surface attachment", err)
	}
	return attachmentFromRow(row), nil
}

func (r *ContinuityRepository) GetAttachment(ctx context.Context, ownerUserID, attachmentID, deviceID string) (domain.SurfaceAttachment, error) {
	row, err := r.queries.GetControllerAttachment(ctx, surfacedb.GetControllerAttachmentParams{
		OwnerUserID: ownerUserID, AttachmentID: attachmentID, DeviceID: deviceID,
	})
	if err != nil {
		return domain.SurfaceAttachment{}, continuityAttachmentError("query surface attachment", err)
	}
	return attachmentFromRow(row), nil
}

func (r *ContinuityRepository) LiveAttachmentBySurfaceSession(ctx context.Context, ownerUserID, surfaceSessionID, deviceID string) (domain.SurfaceAttachment, error) {
	row, err := r.queries.GetLiveAttachmentBySurfaceSession(ctx, surfacedb.GetLiveAttachmentBySurfaceSessionParams{
		OwnerUserID: ownerUserID, SurfaceSessionID: surfaceSessionID, DeviceID: deviceID,
	})
	if err != nil {
		return domain.SurfaceAttachment{}, continuityAttachmentError("query live surface attachment", err)
	}
	return attachmentFromRow(row), nil
}

func (r *ContinuityRepository) Lease(ctx context.Context, workloadID string) (domain.ControlLease, bool, error) {
	return readLease(ctx, r.queries, workloadID)
}

func (r *ContinuityRepository) CountLiveAttachments(ctx context.Context, ownerUserID, projectID string) (map[string]int32, error) {
	rows, err := r.queries.CountLiveSurfaceAttachments(ctx, surfacedb.CountLiveSurfaceAttachmentsParams{
		OwnerUserID: ownerUserID, ProjectID: projectID,
	})
	if err != nil {
		return nil, continuityStoreError("count live surface attachments", err)
	}
	counts := make(map[string]int32, len(rows))
	for _, row := range rows {
		counts[row.WorkloadID] = int32(row.LiveAttachments)
	}
	return counts, nil
}

func (r *ContinuityRepository) ListLiveAttachmentWorkloads(ctx context.Context) ([]ports.LiveAttachmentWorkload, error) {
	rows, err := r.queries.ListLiveSurfaceAttachmentWorkloads(ctx)
	if err != nil {
		return nil, continuityStoreError("list live surface attachment workloads", err)
	}
	workloads := make([]ports.LiveAttachmentWorkload, 0, len(rows))
	for _, row := range rows {
		workloads = append(workloads, ports.LiveAttachmentWorkload{WorkloadID: row.WorkloadID, OwnerUserID: row.OwnerUserID})
	}
	return workloads, nil
}

func (r *ContinuityRepository) ExpireElapsedAttachments(ctx context.Context, now time.Time) ([]string, error) {
	rows, err := r.queries.ExpireElapsedSurfaceAttachments(ctx, timestamp(now))
	if err != nil {
		return nil, continuityStoreError("expire elapsed surface attachments", err)
	}
	return rows, nil
}

func (r *ContinuityRepository) ExpireAttachmentsForWorkloads(ctx context.Context, workloadIDs []string, now time.Time) error {
	if len(workloadIDs) == 0 {
		return nil
	}
	if _, err := r.queries.ExpireSurfaceAttachmentsForWorkloads(ctx, surfacedb.ExpireSurfaceAttachmentsForWorkloadsParams{
		WorkloadIds: workloadIDs, Now: timestamp(now),
	}); err != nil {
		return continuityStoreError("expire surface attachments of stopped workloads", err)
	}
	return nil
}

func readLease(ctx context.Context, queries *surfacedb.Queries, workloadID string) (domain.ControlLease, bool, error) {
	row, err := queries.GetSurfaceControlLease(ctx, workloadID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ControlLease{}, false, nil
		}
		return domain.ControlLease{}, false, continuityStoreError("query surface control lease", err)
	}
	return leaseFromRow(row), true, nil
}

func markControl(ctx context.Context, queries *surfacedb.Queries, ownerUserID, attachmentID string, controls bool, generation int64, until time.Time) error {
	if _, err := queries.MarkSurfaceAttachmentControl(ctx, surfacedb.MarkSurfaceAttachmentControlParams{
		OwnerUserID: ownerUserID, AttachmentID: attachmentID,
		Controls: controls, ControlGeneration: generation, ControlExpiresAt: expiryParam(controls, until),
	}); err != nil {
		return continuityStoreError("mark surface attachment control", err)
	}
	return nil
}

func leaseParams(lease domain.ControlLease) surfacedb.InsertSurfaceControlLeaseParams {
	return surfacedb.InsertSurfaceControlLeaseParams{
		WorkloadID: lease.WorkloadID, OwnerUserID: lease.OwnerUserID,
		ControlGeneration:      lease.ControlGeneration,
		ControllerAttachmentID: lease.ControllerAttachmentID, ControllerDeviceID: lease.ControllerDeviceID,
		GrantedAt: timestamp(lease.GrantedAt), ExpiresAt: timestamp(lease.ExpiresAt),
	}
}

func updateLeaseParams(workloadID string, lease domain.ControlLease) surfacedb.UpdateSurfaceControlLeaseParams {
	return surfacedb.UpdateSurfaceControlLeaseParams{
		WorkloadID: workloadID, ControlGeneration: lease.ControlGeneration,
		ControllerAttachmentID: lease.ControllerAttachmentID, ControllerDeviceID: lease.ControllerDeviceID,
		GrantedAt: timestamp(lease.GrantedAt), ExpiresAt: timestamp(lease.ExpiresAt),
	}
}

// expiryParam stores the control expiry only while the attachment holds
// control; observers carry SQL NULL so the sweep never expires them by a
// lease they never held.
func expiryParam(controls bool, until time.Time) pgtype.Timestamptz {
	if !controls {
		return pgtype.Timestamptz{}
	}
	return timestamp(until)
}

func continuityAttachmentError(operation string, err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ports.ErrContinuityNotFound
	}
	return continuityStoreError(operation, err)
}

func continuityStoreError(operation string, err error) error {
	if err == nil {
		return nil
	}
	if dbtransient.IsTransient(err) {
		return fmt.Errorf("%s: %w: %w", operation, ports.ErrContinuityStoreUnavailable, err)
	}
	return fmt.Errorf("%s: %w", operation, err)
}
