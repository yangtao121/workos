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
	ID                string
	OwnerUserID       string
	ProjectID         string // optional; global agent tasks have none
	Kind              string
	Severity          string
	Origin            string
	Title             string
	Body              string
	TargetKind        string
	TargetID          string
	AppID             string // app origin only
	AppInstallationID string // app origin only
	SourceProcess     string
	SourceID          string
	SourceDigest      string
	CreatedAt         time.Time
	// CreatedChangeSequence is the immutable CREATED revision. A stored
	// notification always has one; prepared producer input receives it only
	// when the PostgreSQL adapter allocates the owner sequence in AppendTx.
	CreatedChangeSequence int64
	ReadAt                time.Time // zero while unread
	ReadChangeSequence    int64     // 0 while unread
}

// Read reports whether the fact has been read by its owner.
func (n Notification) Read() bool { return !n.ReadAt.IsZero() }

// Revision is the latest durable change applied to this notification.
func (n Notification) Revision() int64 {
	if n.ReadChangeSequence > n.CreatedChangeSequence {
		return n.ReadChangeSequence
	}
	return n.CreatedChangeSequence
}

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
	return strings.IndexFunc(key, controlRune) < 0
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
	if body != "" && strings.Count(body, "\n")+1 > MaxAppBodyLines {
		return false
	}
	return true
}

func controlRune(r rune) bool { return r < 0x20 || (r >= 0x7f && r <= 0x9f) }

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
	if occurredAt.IsZero() {
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
	if !ValidKind(n.Kind) || (n.Origin != OriginSystem && n.Origin != OriginApp) ||
		n.Severity == "" || n.TargetKind == "" {
		return fmt.Errorf("step vocab: %w", ErrCorrupt)
	}
	if n.CreatedAt.IsZero() || n.CreatedChangeSequence <= 0 || n.SourceID == "" || !validSHA256(n.SourceDigest) {
		return fmt.Errorf("step source facts: %w", ErrCorrupt)
	}
	if !validStoredText(n.Title, n.Body) {
		return fmt.Errorf("step text: %w", ErrCorrupt)
	}
	if n.Severity != SeverityNormal && n.Severity != SeverityCritical {
		return fmt.Errorf("step severity: %w", ErrCorrupt)
	}
	appOrigin := n.Origin == OriginApp
	if appOrigin != (n.AppInstallationID != "") || appOrigin != (n.AppID != "") {
		return fmt.Errorf("step app fields: %w", ErrCorrupt)
	}
	if appOrigin && (!ValidUUID(n.AppInstallationID) || n.ProjectID == "") {
		return fmt.Errorf("step app binding: %w", ErrCorrupt)
	}
	// The app kind and the app origin are one fact: a system-origin
	// app.instance.message row is as inconsistent as an app-origin system
	// kind.
	if (n.Kind == KindAppInstanceMessage) != appOrigin {
		return fmt.Errorf("step kind/origin coherence: %w", ErrCorrupt)
	}
	if appOrigin && n.TargetKind != TargetApp {
		return fmt.Errorf("step app target: %w", ErrCorrupt)
	}
	if !n.ReadAt.IsZero() {
		if n.ReadChangeSequence <= n.CreatedChangeSequence {
			return fmt.Errorf("step read seq: %w", ErrCorrupt)
		}
		if n.ReadAt.Before(n.CreatedAt) {
			return fmt.Errorf("step read time: %w", ErrCorrupt)
		}
	} else if n.ReadChangeSequence != 0 {
		return fmt.Errorf("step read coherence: %w (seq %d)", ErrCorrupt, n.ReadChangeSequence)
	}
	// The stored kind/target pair must still match the finite vocabulary.
	// App-instance facts derive their text from the app, not a template, so
	// their shape is enforced by the app-origin checks above instead.
	if n.Kind != KindAppInstanceMessage {
		if !validStoredSystemTemplate(n) {
			return fmt.Errorf("step template: %w", ErrCorrupt)
		}
		expectedSource := SourceProcessCore
		if n.Kind == KindReliabilityIncidentOpen {
			expectedSource = SourceProcessReliability
		}
		if n.SourceProcess != expectedSource {
			return fmt.Errorf("step system source: %w", ErrCorrupt)
		}
	} else if n.SourceProcess != SourceProcessCore {
		return fmt.Errorf("step app source: %w", ErrCorrupt)
	}
	return nil
}

func validStoredSystemTemplate(n Notification) bool {
	categories := []string{""}
	switch n.Kind {
	case KindAgentTaskTerminal:
		categories = []string{"completed", "failed", "cancelled"}
	case KindArtifactReviewCreated:
		categories = []string{"document.markdown.v1", "code.unified-diff.v1"}
	case KindReliabilityIncidentOpen:
		categories = []string{"info", "warning", "critical"}
	}
	for _, category := range categories {
		severity, title, body, target, err := SystemTemplate(n.Kind, category)
		if err == nil && n.Severity == severity && n.Title == title && n.Body == body && n.TargetKind == target {
			return true
		}
	}
	return false
}

func validSHA256(value string) bool {
	if len(value) != 71 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	_, err := hex.DecodeString(value[7:])
	return err == nil
}

// Incident publication projection (ADR-0014). The fact arrives from the
// reliability-host private source service; every field is revalidated here
// and the source digest is the reliability publication's own versioned
// digest, so a same-source/different-digest replay is contract violation.

// IncidentPublicationFact is the neutral incident publication input.
type IncidentPublicationFact struct {
	OwnerUserID   string
	ProjectID     string
	IncidentID    string
	Severity      string // info | warning | critical
	ActionOutcome string // pending | restarted | stopped | failed
	Digest        string
	SourceID      string
}

// ValidWorkerIdentity checks the bounded internal consumer identity grammar
// (1..128 ASCII [-a-z0-9._]); it is persisted as the claim lease owner and
// is never accepted from browsers or apps.
func ValidWorkerIdentity(workerID string) bool {
	if len(workerID) == 0 || len(workerID) > 128 {
		return false
	}
	for _, r := range workerID {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '.' || r == '_':
		default:
			return false
		}
	}
	return true
}

// PrepareIncidentPublication validates the publication fact and derives the
// stored notification. The severity category drives the finite template;
// the outcome snapshot is audit-only and never changes the projection.
func PrepareIncidentPublication(fact IncidentPublicationFact, occurredAt time.Time) (Notification, error) {
	if !ValidUUID(fact.OwnerUserID) || !ValidUUID(fact.ProjectID) || !ValidUUID(fact.IncidentID) {
		return Notification{}, ErrInvalid
	}
	if fact.SourceID == "" || !validSHA256(fact.Digest) || occurredAt.IsZero() {
		return Notification{}, ErrInvalid
	}
	switch fact.ActionOutcome {
	case "pending", "restarted", "stopped", "failed":
	default:
		return Notification{}, ErrInvalid
	}
	severity, title, body, targetKind, err := SystemTemplate(KindReliabilityIncidentOpen, fact.Severity)
	if err != nil {
		return Notification{}, ErrInvalid
	}
	id, err := uuid.NewV7()
	if err != nil {
		return Notification{}, fmt.Errorf("%w: mint incident notification id", ErrInvalid)
	}
	return Notification{
		ID: id.String(), OwnerUserID: fact.OwnerUserID, ProjectID: fact.ProjectID,
		Kind: KindReliabilityIncidentOpen, Severity: severity, Origin: OriginSystem,
		Title: title, Body: body, TargetKind: targetKind, TargetID: fact.IncidentID,
		SourceProcess: SourceProcessReliability, SourceID: SourceKindIncidentPrefix + fact.SourceID,
		SourceDigest: fact.Digest, CreatedAt: CanonicalUTCTime(occurredAt),
	}, nil
}

// AppNotificationFact is the neutral app create input after the neutral
// authorizer verified the installation. The app identity is authoritative
// Core fact, never client input.
type AppNotificationFact struct {
	OwnerUserID   string
	ProjectID     string
	AppInstanceID string
	AppID         string
	// IdempotencyKey is part of the source identity: two distinct keys are
	// two distinct app intents, even with identical text.
	IdempotencyKey string
	Title          string
	Body           string
}

// PrepareAppNotification derives the stored app-origin fact: kind
// app.instance.message, severity normal, project-scoped, target app bound
// to the installation. The source identity is the request mapping so two
// app keys can never alias one source fact.
func PrepareAppNotification(fact AppNotificationFact, occurredAt time.Time) (Notification, error) {
	if !ValidUUID(fact.OwnerUserID) || !ValidUUID(fact.ProjectID) || !ValidUUID(fact.AppInstanceID) {
		return Notification{}, ErrInvalid
	}
	if occurredAt.IsZero() || !validAppID(fact.AppID) || !ValidIdempotencyKey(fact.IdempotencyKey) || !validStoredText(fact.Title, fact.Body) {
		return Notification{}, ErrInvalid
	}
	id, err := uuid.NewV7()
	if err != nil {
		return Notification{}, fmt.Errorf("%w: mint app notification id", ErrInvalid)
	}
	return Notification{
		ID: id.String(), OwnerUserID: fact.OwnerUserID, ProjectID: fact.ProjectID,
		Kind: KindAppInstanceMessage, Severity: SeverityNormal, Origin: OriginApp,
		Title: fact.Title, Body: fact.Body, TargetKind: TargetApp,
		TargetID: fact.AppInstanceID, AppID: fact.AppID,
		AppInstallationID: fact.AppInstanceID,
		SourceProcess:     SourceProcessCore,
		SourceID:          SourceKindAppRequestPrefix + fact.AppInstanceID + ":" + fact.IdempotencyKey,
		// The source digest re-derives from the normalized stored text, so
		// stored drift breaks replay loudly instead of silently aliasing.
		SourceDigest: appNotificationSourceDigest(fact.AppInstanceID, fact.Title, fact.Body),
		CreatedAt:    CanonicalUTCTime(occurredAt),
	}, nil
}

// appNotificationSourceDigest re-derives the app request digest from the
// normalized stored text, so stored drift breaks replay loudly.
func appNotificationSourceDigest(appInstanceID, title, body string) string {
	canonical := fmt.Sprintf("workos.app-notification-source.v1|%s|%s|%s", appInstanceID, title, body)
	sum := sha256.Sum256([]byte(canonical))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func validAppID(appID string) bool {
	if len(appID) < 3 || len(appID) > 63 {
		return false
	}
	for _, r := range appID {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '-':
		default:
			return false
		}
	}
	return true
}
