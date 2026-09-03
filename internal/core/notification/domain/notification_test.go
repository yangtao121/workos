package domain

import (
	"strings"
	"testing"
	"time"
)

func validFact() SystemFact {
	return SystemFact{
		Kind: KindAgentTaskTerminal, OwnerUserID: "0194a1f0-0000-7000-8000-000000000001",
		Category: "completed", TargetID: "0194a1f0-0000-7000-8000-000000000002",
		SourceID: "task-abc",
	}
}

func TestPrepareSystemFactDerivesFiniteTemplates(t *testing.T) {
	cases := []struct {
		kind, category, severity, title, target string
	}{
		{KindAgentApprovalRequired, "", SeverityNormal, "Approval required", TargetApproval},
		{KindAgentTaskTerminal, "completed", SeverityNormal, "Task completed", TargetTask},
		{KindAgentTaskTerminal, "failed", SeverityNormal, "Task failed", TargetTask},
		{KindAgentTaskTerminal, "cancelled", SeverityNormal, "Task cancelled", TargetTask},
		{KindArtifactReviewCreated, "document.markdown.v1", SeverityNormal, "Review artifact ready", TargetArtifact},
		{KindReliabilityIncidentOpen, "critical", SeverityCritical, "Critical incident opened", TargetIncident},
		{KindReliabilityIncidentOpen, "warning", SeverityNormal, "Workload incident opened", TargetIncident},
	}
	for _, tc := range cases {
		fact := validFact()
		fact.Kind, fact.Category = tc.kind, tc.category
		notification, err := PrepareSystemFact(fact, time.Now().UTC())
		if err != nil {
			t.Fatalf("%s/%s: %v", tc.kind, tc.category, err)
		}
		if notification.Severity != tc.severity || notification.Title != tc.title || notification.TargetKind != tc.target {
			t.Fatalf("%s/%s: got %q/%q/%q", tc.kind, tc.category, notification.Severity, notification.Title, notification.TargetKind)
		}
		if notification.Origin != OriginSystem || notification.Body == "" {
			t.Fatalf("%s: origin/body invalid", tc.kind)
		}
		if !strings.HasPrefix(notification.SourceID, SourceKindApprovalPrefix) &&
			!strings.HasPrefix(notification.SourceID, SourceKindTaskPrefix) &&
			!strings.HasPrefix(notification.SourceID, SourceKindArtifactPrefix) &&
			!strings.HasPrefix(notification.SourceID, SourceKindIncidentPrefix) {
			t.Fatalf("%s: source id missing namespace prefix: %q", tc.kind, notification.SourceID)
		}
	}
}

func TestPrepareSystemFactRejectsUnknownAndMalformed(t *testing.T) {
	bad := validFact()
	bad.Kind = "app.instance.message" // system producers can never mint app facts
	if _, err := PrepareSystemFact(bad, time.Now().UTC()); err == nil {
		t.Fatal("app kind accepted for a system fact")
	}
	unknown := validFact()
	unknown.Category = "exploded"
	if _, err := PrepareSystemFact(unknown, time.Now().UTC()); err == nil {
		t.Fatal("unknown category accepted")
	}
	foreign := validFact()
	foreign.OwnerUserID = "not-a-uuid"
	if _, err := PrepareSystemFact(foreign, time.Now().UTC()); err == nil {
		t.Fatal("malformed owner accepted")
	}
	if _, err := PrepareSystemFact(validFact(), time.Time{}); err == nil {
		t.Fatal("zero occurrence time accepted")
	}
}

func TestValidStoredNotificationRejectsDrift(t *testing.T) {
	notification, err := PrepareSystemFact(validFact(), time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	notification.CreatedChangeSequence = 1
	if err := ValidStoredNotification(notification); err != nil {
		t.Fatalf("fresh fact invalid: %v", err)
	}
	drifted := notification
	drifted.Title = strings.ToUpper(drifted.Title)
	if err := ValidStoredNotification(drifted); err == nil {
		t.Fatal("title drift accepted")
	}
	unreadWithSeq := notification
	unreadWithSeq.ReadChangeSequence = 7
	if err := ValidStoredNotification(unreadWithSeq); err == nil {
		t.Fatal("unread fact with read sequence accepted")
	}
	read := notification
	read.ReadAt = CanonicalUTCTime(time.Now().UTC())
	read.ReadChangeSequence = 3
	if err := ValidStoredNotification(read); err != nil {
		t.Fatalf("read fact invalid: %v", err)
	}
	appBound := notification
	appBound.Kind = KindAppInstanceMessage
	if err := ValidStoredNotification(appBound); err == nil {
		t.Fatal("app kind without app binding accepted")
	}
	wrongSource := notification
	wrongSource.SourceProcess = SourceProcessReliability
	if err := ValidStoredNotification(wrongSource); err == nil {
		t.Fatal("wrong source process accepted")
	}
	badDigest := notification
	badDigest.SourceDigest = "sha256:not-hex"
	if err := ValidStoredNotification(badDigest); err == nil {
		t.Fatal("malformed source digest accepted")
	}
}

func TestValidIdempotencyKeyAndTime(t *testing.T) {
	if !ValidIdempotencyKey("read-once") {
		t.Fatal("valid key rejected")
	}
	if ValidIdempotencyKey("") || ValidIdempotencyKey(strings.Repeat("x", 129)) ||
		ValidIdempotencyKey("bad\x00key") || ValidIdempotencyKey("bad\u0085key") {
		t.Fatal("malformed key accepted")
	}
	stamp := time.Date(2026, 9, 2, 12, 0, 0, 123456789, time.UTC)
	if got := CanonicalUTCTime(stamp); got.Nanosecond() != 123456000 {
		t.Fatalf("canonical time not microsecond: %v", got)
	}
}

func TestPrepareIncidentPublicationRejectsInvalidFiniteFacts(t *testing.T) {
	fact := IncidentPublicationFact{
		OwnerUserID: "0194a1f0-0000-7000-8000-000000000001",
		ProjectID:   "0194a1f0-0000-7000-8000-000000000002",
		IncidentID:  "0194a1f0-0000-7000-8000-000000000003",
		Severity:    "critical", ActionOutcome: "pending",
		Digest:   SystemSourceDigest(KindReliabilityIncidentOpen, "critical", "publication-1"),
		SourceID: "publication-1",
	}
	if _, err := PrepareIncidentPublication(fact, time.Now().UTC()); err != nil {
		t.Fatalf("valid publication rejected: %v", err)
	}
	fact.ActionOutcome = "invented"
	if _, err := PrepareIncidentPublication(fact, time.Now().UTC()); err == nil {
		t.Fatal("unknown action outcome accepted")
	}
	fact.ActionOutcome = "pending"
	if _, err := PrepareIncidentPublication(fact, time.Time{}); err == nil {
		t.Fatal("zero occurrence time accepted")
	}
}
