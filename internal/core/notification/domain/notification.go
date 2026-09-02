// Package domain holds the Core notification fact model (ADR-0014). Facts
// are owner-scoped durable rows with a finite kind vocabulary, server-derived
// bounded inert text, and a finite typed action target. Nothing here imports
// databases, transports, other modules, or their entities: producers deliver
// neutral SystemFact values through the tx-scoped sink port, and this module
// derives every persisted field from the finite vocabulary alone.
package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

// Notification kinds. The exact strings are the stored contract (migration
// 029 CHECK) and the wire contract (additive proto enum); a new kind needs a
// new ADR and can never reuse these facts.
const (
	KindAgentApprovalRequired   = "agent.approval.required"
	KindAgentTaskTerminal       = "agent.task.terminal"
	KindArtifactReviewCreated   = "artifact.review.created"
	KindReliabilityIncidentOpen = "reliability.incident.opened"
	KindAppInstanceMessage      = "app.instance.message"
)

// Severities.
const (
	SeverityNormal   = "normal"
	SeverityCritical = "critical"
)

// Origins.
const (
	OriginSystem = "system"
	OriginApp    = "app"
)

// Target kinds — the finite typed action surfaces.
const (
	TargetApproval = "approval"
	TargetTask     = "task"
	TargetArtifact = "artifact"
	TargetIncident = "incident"
	TargetApp      = "app"
)

// Source processes.
const (
	SourceProcessCore        = "workos-core"
	SourceProcessReliability = "reliability-host"
)

// Source identities. source_id is the stable identity of the producing
// source fact; together with source_process it is the exactly-once
// projection key.
const (
	SourceKindApprovalPrefix   = "agent-approval:"
	SourceKindTaskPrefix       = "agent-task-terminal:"
	SourceKindArtifactPrefix   = "review-artifact:"
	SourceKindIncidentPrefix   = "incident-publication:"
	SourceKindAppRequestPrefix = "app-request:"
)

var (
	ErrInvalid = errors.New("notification fact is invalid")
	// ErrCorrupt marks stored drift: immutable facts cannot change, so any
	// violation found on read or idempotent replay is corruption answered
	// with a sanitized failure, never a silent repair.
	ErrCorrupt = errors.New("stored notification failed validation")
	// ErrNotFound marks an unknown or foreign fact. Like every lookup miss
	// it is a sanitized failure without existence detail.
	ErrNotFound = errors.New("notification is not available")
	// ErrAlreadyRead marks a no-op read command for an already-read fact.
	ErrAlreadyRead = errors.New("notification is already read")
)

// Notification is one durable owner-scoped fact.
type Notification struct {
	ID                 string
	OwnerUserID        string
	ProjectID          string // optional; global agent tasks have none
	Kind               string
	Severity           string
	Origin             string
	Title              string
	Body               string
	TargetKind         string
	TargetID           string
	AppID              string // app origin only
	AppInstallationID  string // app origin only
	SourceProcess      string
	SourceID           string
	SourceDigest       string
	CreatedAt          time.Time
	ReadAt             time.Time // zero while unread
	ReadChangeSequence int64     // 0 while unread
}

// Read reports whether the fact has been read by its owner.
func (n Notification) Read() bool { return !n.ReadAt.IsZero() }

// Change is one durable owner-wide change-stream entry.
type Change struct {
	OwnerUserID    string
	ChangeSequence int64
	NotificationID string
	ChangeType     string // "created" | "read"
	Revision       int64
	OccurredAt     time.Time
	Notification   Notification // joined fact for the event payload
}

// Change types (stored CHECK).
const (
	ChangeCreated = "created"
	ChangeRead    = "read"
)

// SystemFact is the neutral producer input for the three Core system
// producers. It carries only stable identities and finite categories —
// never goals, provider output, artifact content, telemetry, or credentials.
// Title/body/target are derived here from the finite kind vocabulary.
type SystemFact struct {
	Kind        string
	OwnerUserID string
	ProjectID   string // optional
	// TargetID is the canonical UUID the typed action resolves to
	// (approval / task / artifact id). The incident projection assigns it
	// from the publication.
	TargetID string
	// Category is the finite per-kind category used for the derived text
	// (terminal state, artifact type, incident severity category).
	Category string
	// SourceID is the stable source-fact identity (without prefix); the
	// sink composes the stored namespaced source id.
	SourceID string
}

// AppText is a validated bounded app-supplied text field.
type AppText struct {
	Title string
	Body  string
}

// App bounds (ADR-0014): code points, bytes, and line counts are all
// bounded; invalid UTF-8, NUL, C0/C1 control characters (except LF), and
// CR are rejected before any persistence.
const (
	MaxAppTitleCodePoints = 120
	MaxAppTitleBytes      = 512
	MaxAppBodyCodePoints  = 500
	MaxAppBodyBytes       = 2048
	MaxAppBodyLines       = 16
)

// ValidUUID reports whether the value is a canonical lowercase UUIDv7 string.
func ValidUUID(value string) bool {
	parsed, err := uuid.Parse(value)
	if err != nil || parsed.String() != value {
		return false
	}
	return parsed.Version() == 7 && parsed.Variant() == uuid.RFC4122
}

// CanonicalUTCTime truncates to database microsecond precision so stored
// facts replay byte-exactly after a restart.
func CanonicalUTCTime(value time.Time) time.Time {
	return value.UTC().Truncate(time.Microsecond)
}

// ValidKind reports whether the kind is in the finite vocabulary.
func ValidKind(kind string) bool {
	switch kind {
	case KindAgentApprovalRequired, KindAgentTaskTerminal, KindArtifactReviewCreated,
		KindReliabilityIncidentOpen, KindAppInstanceMessage:
		return true
	}
	return false
}

// ValidIdempotencyKey checks the bounded key grammar (1..128 characters,
// valid UTF-8, no control characters).
func ValidIdempotencyKey(key string) bool {
	if len(key) == 0 || len(key) > 128 || !utf8.ValidString(key) {
		return false
	}
	return strings.IndexFunc(key, func(r rune) bool { return r < 0x20 || r == 0x7f }) < 0
}

// validStoredText revalidates derived or app-supplied text on every read:
// bounded code points and bytes, valid UTF-8, no NUL/C0/C1 (LF allowed in
// bodies).
func validStoredText(title, body string) bool {
	if title == "" || !utf8.ValidString(title) || !utf8.ValidString(body) {
		return false
	}
	if utf8.RuneCountInString(title) > MaxAppTitleCodePoints || len(title) > MaxAppTitleBytes {
		return false
	}
	if utf8.RuneCountInString(body) > MaxAppBodyCodePoints || len(body) > MaxAppBodyBytes {
		return false
	}
	if strings.ContainsFunc(title, controlRune) {
		return false
	}
	if strings.ContainsFunc(body, func(r rune) bool { return controlRune(r) && r != '\n' }) {
		return false
	}
	if strings.Count(body, "\n") > MaxAppBodyLines {
		return false
	}
	return true
}

func controlRune(r rune) bool { return r < 0x20 || r == 0x7f }

// SystemTemplate derives the server-owned title/body/target for a system
// fact from the finite kind + category vocabulary. Unknown combinations are
// invalid producer input, never user-visible content.
func SystemTemplate(kind, category string) (severity, title, body, targetKind string, err error) {
	switch kind {
	case KindAgentApprovalRequired:
		return SeverityNormal, "Approval required",
			"A project agent task is waiting for your approval.", TargetApproval, nil
	case KindAgentTaskTerminal:
		switch category {
		case "completed":
			return SeverityNormal, "Task completed", "A project agent task finished successfully.", TargetTask, nil
		case "failed":
			return SeverityNormal, "Task failed", "A project agent task ended in failure.", TargetTask, nil
		case "cancelled":
			return SeverityNormal, "Task cancelled", "A project agent task was cancelled.", TargetTask, nil
		}
	case KindArtifactReviewCreated:
		switch category {
		case "document.markdown.v1", "code.unified-diff.v1":
			return SeverityNormal, "Review artifact ready",
				"A project review artifact is ready for your review.", TargetArtifact, nil
		}
	case KindReliabilityIncidentOpen:
		switch category {
		case "critical":
			return SeverityCritical, "Critical incident opened",
				"A critical workload incident requires attention.", TargetIncident, nil
		case "warning":
			return SeverityNormal, "Workload incident opened",
				"A workload incident was opened by the supervisor.", TargetIncident, nil
		case "info":
			return SeverityNormal, "Workload incident opened",
				"A workload incident was recorded.", TargetIncident, nil
		}
	}
	return "", "", "", "", fmt.Errorf("%w: unknown system kind/category %q/%q", ErrInvalid, kind, category)
}

// PrepareSystemFact validates a producer fact and derives every persisted
// field. The digest is the versioned canonical digest of the source
// fields; any change to them changes the digest and is treated as contract
// violation on replay, never as an update.
func PrepareSystemFact(fact SystemFact, occurredAt time.Time) (Notification, error) {
	if !ValidKind(fact.Kind) || !ValidUUID(fact.OwnerUserID) || !ValidUUID(fact.TargetID) || fact.SourceID == "" {
		return Notification{}, ErrInvalid
	}
	if fact.ProjectID != "" && !ValidUUID(fact.ProjectID) {
		return Notification{}, ErrInvalid
	}
	severity, title, body, targetKind, err := SystemTemplate(fact.Kind, fact.Category)
	if err != nil {
		return Notification{}, err
	}
	sourceName := systemSourceName(fact.Kind, fact.SourceID)
	if sourceName == "" {
		return Notification{}, ErrInvalid
	}
	id, err := uuid.NewV7()
	if err != nil {
		return Notification{}, fmt.Errorf("%w: mint notification id", ErrInvalid)
	}
	return Notification{
		ID: id.String(), OwnerUserID: fact.OwnerUserID, ProjectID: fact.ProjectID,
		Kind: fact.Kind, Severity: severity, Origin: OriginSystem,
		Title: title, Body: body, TargetKind: targetKind, TargetID: fact.TargetID,
		SourceProcess: SourceProcessCore, SourceID: sourceName,
		SourceDigest: SystemSourceDigest(fact.Kind, fact.Category, fact.SourceID),
		CreatedAt:    CanonicalUTCTime(occurredAt),
	}, nil
}

func systemSourceName(kind, sourceID string) string {
	switch kind {
	case KindAgentApprovalRequired:
		return SourceKindApprovalPrefix + sourceID
	case KindAgentTaskTerminal:
		return SourceKindTaskPrefix + sourceID
	case KindArtifactReviewCreated:
		return SourceKindArtifactPrefix + sourceID
	case KindReliabilityIncidentOpen:
		return SourceKindIncidentPrefix + sourceID
	case KindAppInstanceMessage:
		return SourceKindAppRequestPrefix + sourceID
	}
	return ""
}

// SystemSourceDigest builds the versioned canonical digest over the finite
// source fields.
func SystemSourceDigest(kind, category, sourceID string) string {
	canonical := fmt.Sprintf("workos.notification-source.v1|%s|%s|%s", kind, category, sourceID)
	sum := sha256.Sum256([]byte(canonical))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// ValidStoredNotification revalidates a stored row on every read and every
// idempotent replay. Any drift of an immutable fact is corruption, never a
// repairable state.
func ValidStoredNotification(n Notification) error {
	if !ValidUUID(n.ID) || !ValidUUID(n.OwnerUserID) || !ValidUUID(n.TargetID) {
		return fmt.Errorf("step ids: %w", ErrCorrupt)
	}
	if n.ProjectID != "" && !ValidUUID(n.ProjectID) {
		return fmt.Errorf("step project: %w", ErrCorrupt)
	}
	if !ValidKind(n.Kind) || n.Origin == "" || n.Severity == "" || n.TargetKind == "" {
		return fmt.Errorf("step vocab: %w", ErrCorrupt)
	}
	if n.CreatedAt.IsZero() || n.SourceID == "" || len(n.SourceDigest) != 71 || !strings.HasPrefix(n.SourceDigest, "sha256:") {
		return fmt.Errorf("step source facts: %w", ErrCorrupt)
	}
	if !validStoredText(n.Title, n.Body) {
		return fmt.Errorf("step text: %w", ErrCorrupt)
	}
	appOrigin := n.Origin == OriginApp
	if appOrigin != (n.AppInstallationID != "") || appOrigin != (n.AppID != "") {
		return ErrCorrupt
	}
	if appOrigin && (!ValidUUID(n.AppInstallationID) || n.ProjectID == "") {
		return fmt.Errorf("step app binding: %w", ErrCorrupt)
	}
	if !n.ReadAt.IsZero() {
		if n.ReadChangeSequence <= 0 || n.ReadAt.Before(n.CreatedAt) {
			return ErrCorrupt
		}
	} else if n.ReadChangeSequence != 0 {
		return fmt.Errorf("step read coherence: %w (seq %d)", ErrCorrupt, n.ReadChangeSequence)
	}
	// The stored kind/target pair must still match the finite vocabulary.
	if _, _, _, targetKind, err := SystemTemplate(n.Kind, storedCategoryFor(n)); err != nil || targetKind != n.TargetKind {
		return fmt.Errorf("step template: %w", ErrCorrupt)
	}
	return nil
}

// storedCategoryFor recovers the finite category a stored fact was created
// with, for shape revalidation only.
func storedCategoryFor(n Notification) string {
	switch n.Kind {
	case KindAgentTaskTerminal:
		switch n.Title {
		case "Task completed":
			return "completed"
		case "Task failed":
			return "failed"
		case "Task cancelled":
			return "cancelled"
		}
	case KindArtifactReviewCreated:
		return "document.markdown.v1"
	case KindReliabilityIncidentOpen:
		if n.Severity == SeverityCritical {
			return "critical"
		}
		return "warning"
	}
	return ""
}
