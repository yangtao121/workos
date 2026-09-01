package postgres

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/yangtao121/workos/internal/core/project/adapters/postgres/projectdb"
	"github.com/yangtao121/workos/internal/core/project/domain"
	"github.com/yangtao121/workos/internal/core/project/ports"
)

// These tests pin the pure decision helpers of the SetAppGrants transaction
// without a database: the stored-invariant fail-closed rules, the event
// payload contract, and the replay snapshot overlay. The SQL-level
// serialization is covered by the integration suite.

func invariantFixture() (domain.Installation, domain.PinnedApp) {
	pinned := domain.PinnedApp{
		AppID: "board-app", Version: "1.2.0", ManifestDigest: "sha256:" + repeatChar('a', 64), Scope: "user",
		Permissions: []string{"agent.event.watch", "agent.task.run", "artifact.read"},
	}
	installation := domain.Installation{
		ID: "01999999-9999-7999-8999-999999999993", AppID: "board-app", Version: "1.2.0",
		ManifestDigest:     pinned.ManifestDigest,
		GrantedPermissions: []string{"agent.task.run"},
		GrantRevision:      2,
	}
	return installation, pinned
}

func TestValidateStoredGrantInvariantAcceptsCanonicalSubsets(t *testing.T) {
	t.Parallel()
	installation, pinned := invariantFixture()
	if err := validateStoredGrantInvariant(installation, pinned); err != nil {
		t.Fatalf("canonical stored grant rejected: %v", err)
	}
	// The empty grant at epoch 1 is the canonical revoke-all state.
	installation.GrantedPermissions = nil
	installation.GrantRevision = 1
	if err := validateStoredGrantInvariant(installation, pinned); err != nil {
		t.Fatalf("empty stored grant rejected: %v", err)
	}
}

func TestValidateStoredGrantInvariantFailsClosed(t *testing.T) {
	t.Parallel()
	base, pinned := invariantFixture()
	cases := map[string]func(*domain.Installation){
		"unsorted grant":    func(i *domain.Installation) { i.GrantedPermissions = []string{"artifact.read", "agent.task.run"} },
		"duplicated grant":  func(i *domain.Installation) { i.GrantedPermissions = []string{"agent.task.run", "agent.task.run"} },
		"malformed entry":   func(i *domain.Installation) { i.GrantedPermissions = []string{"agent task run"} },
		"control character": func(i *domain.Installation) { i.GrantedPermissions = []string{"agent.task.run\x1b"} },
		"outside requested": func(i *domain.Installation) { i.GrantedPermissions = []string{"knowledge.read"} },
		"zero revision":     func(i *domain.Installation) { i.GrantRevision = 0 },
		"negative revision": func(i *domain.Installation) { i.GrantRevision = -3 },
	}
	for name, mutate := range cases {
		installation := base
		mutate(&installation)
		if err := validateStoredGrantInvariant(installation, pinned); !errors.Is(err, errGrantInvariantCorrupt) {
			t.Errorf("%s: must be the sanitized corruption verdict, got %v", name, err)
		}
	}
}

func TestAppGrantsUpdatedPayloadCarriesCompleteCanonicalSet(t *testing.T) {
	t.Parallel()
	installation, _ := invariantFixture()
	installation.GrantedPermissions = []string{"agent.task.run", "artifact.read"}
	installation.GrantRevision = 3
	payload, err := appGrantsUpdatedPayload(installation, 7, installation.GrantRevision, installation.GrantedPermissions)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	expected := map[string]any{
		"projectId": installation.ProjectID, "revision": float64(7), "installationId": installation.ID,
		"appId": installation.AppID, "version": installation.Version, "manifestDigest": installation.ManifestDigest,
		"grantRevision": float64(3), "grantedPermissions": []any{"agent.task.run", "artifact.read"},
	}
	// json.Marshal writes map keys in sorted order, so both sides encode
	// deterministically for a byte-exact comparison (slice values cannot be
	// compared with ==).
	encodedExpected, err := json.Marshal(expected)
	if err != nil {
		t.Fatal(err)
	}
	encodedActual, err := json.Marshal(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if string(encodedActual) != string(encodedExpected) {
		t.Fatalf("payload must be exactly the stable fact set:\n got %s\nwant %s", encodedActual, encodedExpected)
	}
	if appGrantsUpdatedEvent != "project.app.grants.updated.v1" {
		t.Fatalf("event type constant drifted: %q", appGrantsUpdatedEvent)
	}
}

func TestApplyRequestSnapshotOverlaysFirstResponseFacts(t *testing.T) {
	t.Parallel()
	tombstone := time.Date(2026, 8, 29, 0, 0, 0, 0, time.UTC)
	current := domain.Installation{
		ID: "01999999-9999-7999-8999-999999999993", OwnerUserID: "01999999-9999-7999-8999-999999999991",
		ProjectID: "01999999-9999-7999-8999-999999999992", AppID: "board-app",
		Version: "2.0.0", ManifestDigest: "sha256:" + repeatChar('b', 64),
		GrantedPermissions: []string{"agent.event.watch"},
		GrantRevision:      9,
		InstalledAt:        tombstone.Add(-time.Hour),
	}
	stored := projectdb.GetInstallationRequestRow{
		Command:                  "transition",
		RequestDigest:            "sha256:" + repeatChar('c', 64),
		InstallationID:           current.ID,
		ProjectRevision:          7,
		ResultUninstalledAt:      pgtype.Timestamptz{Time: tombstone, Valid: true},
		ResultGrantedPermissions: []string{"agent.task.run"},
		ResultGrantRevision:      2,
		ResultVersion:            "1.2.0",
		ResultManifestDigest:     "sha256:" + repeatChar('a', 64),
		CreatedAt:                pgtype.Timestamptz{Time: tombstone.Add(-time.Minute), Valid: true},
	}
	overlaid, err := applyRequestSnapshot(current, storedInstallationRequest(stored))
	if err != nil {
		t.Fatal(err)
	}
	if len(overlaid.GrantedPermissions) != 1 || overlaid.GrantedPermissions[0] != "agent.task.run" {
		t.Fatalf("snapshot grant must win over the mutated row: %v", overlaid.GrantedPermissions)
	}
	if overlaid.GrantRevision != 2 {
		t.Fatalf("snapshot epoch must win over the mutated row: %d", overlaid.GrantRevision)
	}
	if overlaid.UninstalledAt == nil || !overlaid.UninstalledAt.Equal(tombstone) {
		t.Fatalf("snapshot tombstone must be projected: %v", overlaid.UninstalledAt)
	}
	if overlaid.Version != "1.2.0" || overlaid.ManifestDigest != stored.ResultManifestDigest {
		t.Fatalf("snapshot pinned identity must win: %#v", overlaid)
	}
}

func TestValidateStoredInstallationRequestFailsClosed(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 29, 0, 0, 0, 0, time.UTC)
	base := ports.StoredInstallationRequest{
		Command: "rollback", RequestDigest: "sha256:" + repeatChar('c', 64),
		InstallationID: "01999999-9999-7999-8999-999999999993", ProjectRevision: 7,
		ResultGrantedPermissions: []string{"agent.task.run"}, ResultGrantRevision: 2,
		ResultVersion: "1.2.0", ResultManifestDigest: "sha256:" + repeatChar('a', 64),
		CreatedAt: now,
	}
	if err := validateStoredInstallationRequest(base); err != nil {
		t.Fatalf("valid request rejected: %v", err)
	}
	cases := map[string]func(*ports.StoredInstallationRequest){
		"unknown command":    func(s *ports.StoredInstallationRequest) { s.Command = "promote" },
		"bad request digest": func(s *ports.StoredInstallationRequest) { s.RequestDigest = "bad" },
		"wrong UUID version": func(s *ports.StoredInstallationRequest) { s.InstallationID = "01999999-9999-4999-8999-999999999993" },
		"unsorted grants": func(s *ports.StoredInstallationRequest) {
			s.ResultGrantedPermissions = []string{"artifact.read", "agent.task.run"}
		},
		"non-UTC created time": func(s *ports.StoredInstallationRequest) {
			s.CreatedAt = time.Date(2026, 8, 29, 8, 0, 0, 0, time.FixedZone("CST", 8*60*60))
		},
	}
	for name, mutate := range cases {
		stored := base
		mutate(&stored)
		if err := validateStoredInstallationRequest(stored); !errors.Is(err, domain.ErrInstallationCorrupt) {
			t.Errorf("%s: got %v", name, err)
		}
	}
}

func repeatChar(char byte, count int) string {
	value := make([]byte, count)
	for index := range value {
		value[index] = char
	}
	return string(value)
}
