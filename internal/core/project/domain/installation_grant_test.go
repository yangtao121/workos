package domain

import (
	"errors"
	"strings"
	"testing"
)

func TestCanonicalInstallationGrantSortsAndValidates(t *testing.T) {
	t.Parallel()
	requested := []string{"agent.task.run", "agent.event.watch", "artifact.read"}
	granted, err := CanonicalInstallationGrant(
		mustCanonicalGrantShape(t, []string{"artifact.read", "agent.task.run"}),
		requested,
	)
	if err != nil {
		t.Fatalf("valid grant rejected: %v", err)
	}
	if len(granted) != 2 || granted[0] != "agent.task.run" || granted[1] != "artifact.read" {
		t.Fatalf("grant not canonically sorted: %v", granted)
	}
}

func TestCanonicalInstallationGrantEmptyIsValid(t *testing.T) {
	t.Parallel()
	granted, err := CanonicalGrantShape(nil)
	if err != nil || len(granted) != 0 {
		t.Fatalf("empty grant must be valid: %v %v", granted, err)
	}
}

func TestCanonicalInstallationGrantRejectsDuplicates(t *testing.T) {
	t.Parallel()
	_, err := CanonicalGrantShape([]string{"agent.task.run", "agent.task.run"})
	if !errors.Is(err, ErrInvalidGrant) {
		t.Fatalf("duplicate grant verdict: %v", err)
	}
}

func TestCanonicalInstallationGrantRejectsMalformedCapabilityIDs(t *testing.T) {
	t.Parallel()
	for _, id := range []string{"", "Agent.Task.Run", "agent task", "a", "x_y", "_cap.back"} {
		if _, err := CanonicalGrantShape([]string{id}); !errors.Is(err, ErrInvalidGrant) {
			t.Errorf("malformed capability %q accepted: %v", id, err)
		}
	}
}

func TestCanonicalInstallationGrantRejectsUnrequestedCapabilities(t *testing.T) {
	t.Parallel()
	_, err := CanonicalInstallationGrant(
		[]string{"knowledge.read"},
		[]string{"agent.task.run", "artifact.read"},
	)
	if !errors.Is(err, ErrGrantNotRequested) {
		t.Fatalf("unrequested grant verdict: %v", err)
	}
}

func mustCanonicalGrantShape(t *testing.T, granted []string) []string {
	t.Helper()
	canonical, err := CanonicalGrantShape(granted)
	if err != nil {
		t.Fatalf("canonical grant shape: %v", err)
	}
	return canonical
}

func TestInstallationRequestDigestWithGrantsKeepsLegacyCompatibility(t *testing.T) {
	t.Parallel()
	base := InstallationRequestDigest("install", "proj", "app", "1.0.0", "", 3)
	withEmpty := InstallationRequestDigestWithGrants("install", "proj", "app", "1.0.0", "", 3, nil)
	if base != withEmpty {
		t.Fatal("empty grant digest must equal the legacy digest for replay compatibility")
	}
	if !ValidInstallationManifestDigest(base) {
		t.Fatalf("digest shape invalid: %s", base)
	}

	granted := InstallationRequestDigestWithGrants("install", "proj", "app", "1.0.0", "", 3, []string{"agent.task.run"})
	if granted == base {
		t.Fatal("non-empty grant must change the digest")
	}
	// Canonical ordering makes client order irrelevant to the adjudication.
	reordered := InstallationRequestDigestWithGrants("install", "proj", "app", "1.0.0", "", 3, []string{"agent.task.run", "artifact.read"})
	canonical := InstallationRequestDigestWithGrants("install", "proj", "app", "1.0.0", "", 3, []string{"artifact.read", "agent.task.run"})
	if reordered != canonical {
		t.Fatal("grant digest must be order independent")
	}
	// A different grant for the same key is a different request.
	other := InstallationRequestDigestWithGrants("install", "proj", "app", "1.0.0", "", 3, []string{"artifact.read"})
	if other == granted {
		t.Fatal("different grants must produce different digests")
	}
	if strings.Contains(granted[len("sha256:"):], "agent") {
		t.Fatal("digest must be opaque")
	}
}
