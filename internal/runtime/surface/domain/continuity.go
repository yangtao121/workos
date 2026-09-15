package domain

import (
	"errors"
	"time"
)

// Surface attachment and control facts (ADR-0031). Attachments are per-device
// access relations to supervised workloads; control is a single-controller
// epoch advanced only by explicit takeover.
var (
	ErrAttachmentNotFound     = errors.New("surface attachment not found")
	ErrAttachmentDetached     = errors.New("surface attachment is detached")
	ErrControlDenied          = errors.New("surface control is held by another attachment")
	ErrControlExpired         = errors.New("surface control lease expired")
	ErrWorkloadNotRunning     = errors.New("surface workload is not running")
	ErrWorkloadNotRestartable = errors.New("surface workload kind is not restartable; start a new session")
)

type AttachmentState string

const (
	AttachmentStateAttached AttachmentState = "attached"
	AttachmentStateDetached AttachmentState = "detached"
	AttachmentStateExpired  AttachmentState = "expired"
)

func (s AttachmentState) Live() bool {
	return s == AttachmentStateAttached
}

// SurfaceAttachment is one device's access relation to one workload.
type SurfaceAttachment struct {
	ID                string
	WorkloadID        string
	SurfaceSessionID  string
	OwnerUserID       string
	ProjectID         string
	DeviceID          string
	IdempotencyKey    string
	Controls          bool
	ControlGeneration int64
	State             AttachmentState
	AttachedAt        time.Time
	ControlExpiresAt  *time.Time
	DetachedAt        *time.Time
}

// ControlLease is the single controller epoch of one workload. The holder is
// an exact attachment bound to a real device identity; renewal by the same
// device extends the lease while any other holder stays in control.
type ControlLease struct {
	WorkloadID             string
	OwnerUserID            string
	ControlGeneration      int64
	ControllerAttachmentID string
	ControllerDeviceID     string
	GrantedAt              time.Time
	ExpiresAt              time.Time
}

// ControlValid reports whether the lease is live for the given instant.
func (l *ControlLease) ControlValid(now time.Time) bool {
	return now.Before(l.ExpiresAt)
}

// Matches reports whether an attachment is the exact current controller.
func (l *ControlLease) Matches(attachment *SurfaceAttachment) bool {
	return l.ControllerAttachmentID == attachment.ID && l.ControllerDeviceID == attachment.DeviceID
}

// Renew extends the lease. Only the exact current controller may renew; the
// same device renewing must never steal control that another attachment
// holds, so a controller whose device re-attaches later re-acquires control
// only through explicit takeover.
func (l *ControlLease) Renew(now, until time.Time, attachment *SurfaceAttachment) error {
	if !l.Matches(attachment) {
		return ErrControlDenied
	}
	if !l.ControlValid(now) {
		return ErrControlExpired
	}
	if !until.After(now) {
		return ErrControlExpired
	}
	l.ExpiresAt = until
	return nil
}

// Takeover atomically advances the control generation to a new attachment.
// It succeeds from any state (previous controller detached, expired, or
// actively holding control), which is what makes concurrent takeover attempts
// converge: each winner observation sees its own generation, and every loser
// observation is invalidated by the generation check on the data path.
func (l *ControlLease) Takeover(now, until time.Time, attachment *SurfaceAttachment) {
	l.ControlGeneration++
	l.ControllerAttachmentID = attachment.ID
	l.ControllerDeviceID = attachment.DeviceID
	l.GrantedAt = now
	l.ExpiresAt = until
	attachment.Controls = true
	attachment.ControlGeneration = l.ControlGeneration
}

// InvalidateController clears the given attachment's control flag when it
// was the previous controller at that generation; stale flags from older
// generations are dropped as well because only the lease decides.
func (l *ControlLease) InvalidateController(attachment *SurfaceAttachment) {
	if attachment.ControlGeneration <= l.ControlGeneration {
		attachment.Controls = false
	}
}

// Detach releases only this attachment's connection resources. It never
// touches the workload; when the detaching attachment held control, the
// lease keeps running until it expires or another device takes over.
func (a *SurfaceAttachment) Detach(now time.Time) error {
	if !a.State.Live() {
		return ErrAttachmentDetached
	}
	a.State = AttachmentStateDetached
	a.Controls = false
	detached := now
	a.DetachedAt = &detached
	return nil
}
