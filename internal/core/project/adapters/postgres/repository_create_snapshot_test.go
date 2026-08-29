package postgres

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/yangtao121/workos/internal/core/project/domain"
)

// Fail-closed coverage for the persisted first-response snapshot decoder.
// The snapshot column is authoritative for create replays, so a row that
// decodes cleanly but violates any create-time invariant must surface as an
// opaque internal error — never as a plausible Project response. Every
// corrupt fixture below is produced by mutating an otherwise valid snapshot
// in exactly one field, so each failure names its own invariant.
func TestDecodeCreateResultFailsClosedOnInvariantDamage(t *testing.T) {
	t.Parallel()
	owner := "01999999-9999-7999-8999-999999999911"
	projectID := "01999999-9999-7999-8999-999999999912"
	now := time.Date(2026, 8, 29, 9, 0, 0, 0, time.UTC)
	valid := domain.Project{
		ID: projectID, OwnerUserID: owner, Name: "Snapshot Probe", Icon: "◈",
		WorkspaceRefs: []domain.WorkspaceRef{
			{ID: "ws-1", Kind: "WORKSPACE_KIND_LOCAL_GIT", URI: "file:///repos/x"},
		},
		InstalledAppIDs:       []string{},
		KnowledgeCollectionID: "01999999-9999-7999-8999-999999999913",
		ArtifactCollectionID:  "01999999-9999-7999-8999-999999999914",
		Revision:              1, CreatedAt: now, UpdatedAt: now,
	}
	columns, err := encodeCreateResult(valid)
	if err != nil {
		t.Fatalf("encode valid snapshot: %v", err)
	}
	// The digest that would be stored next to this snapshot: the canonical
	// request digest of the fixture's request-bearing fields.
	columnsDigest := domain.CreateRequestDigest(valid.Name, valid.Icon, valid.WorkspaceRefs, valid.HarnessBinding)

	mustFail := func(name, value string) {
		t.Helper()
		if _, err := decodeCreateResult([]byte(value), owner, columnsDigest); err == nil {
			t.Errorf("%s: corrupt snapshot must fail closed, got a Project", name)
		}
	}

	// Structurally broken payloads stay errors.
	mustFail("invalid json", `{"result_version":`)
	mustFail("unsupported version", strings.Replace(string(columns.Result), `"result_version":"1"`, `"result_version":"2"`, 1))

	// Semantically damaged payloads: each mutates one create-time invariant.
	damage := func(name string, mutate func(*snapshotProject)) {
		t.Helper()
		var snapshot createResultSnapshot
		if err := json.Unmarshal(columns.Result, &snapshot); err != nil {
			t.Fatalf("%s: decode fixture: %v", name, err)
		}
		mutate(&snapshot.Project)
		body, err := json.Marshal(snapshot)
		if err != nil {
			t.Fatalf("%s: encode fixture: %v", name, err)
		}
		mustFail(name, string(body))
	}

	damage("foreign owner", func(s *snapshotProject) { s.OwnerUserID = "01999999-9999-7999-8999-99999999991f" })
	damage("empty owner", func(s *snapshotProject) { s.OwnerUserID = "" })
	damage("non-v7 project id", func(s *snapshotProject) { s.ID = "01999999-9999-4999-8999-999999999912" })
	damage("wrong-variant project id", func(s *snapshotProject) { s.ID = "01999999-9999-7999-c999-999999999912" })
	damage("non-v7 knowledge collection", func(s *snapshotProject) { s.KnowledgeCollectionID = "not-a-uuid" })
	damage("empty artifact collection", func(s *snapshotProject) { s.ArtifactCollectionID = "" })
	damage("revision zero", func(s *snapshotProject) { s.Revision = 0 })
	damage("revision five", func(s *snapshotProject) { s.Revision = 5 })
	damage("archived first response", func(s *snapshotProject) {
		archived := now.Add(time.Minute).Format(time.RFC3339Nano)
		s.ArchivedAt = &archived
	})
	damage("installed apps present", func(s *snapshotProject) { s.InstalledAppIDs = []string{"app-1"} })
	damage("default agent role set", func(s *snapshotProject) { s.DefaultAgentRole = "reviewer" })
	damage("empty name", func(s *snapshotProject) { s.Name = "" })
	damage("control-character name", func(s *snapshotProject) { s.Name = "A\nB" })
	damage("over-limit name", func(s *snapshotProject) { s.Name = strings.Repeat("◈", domain.MaxNameRunes+1) })
	damage("invalid icon", func(s *snapshotProject) { s.Icon = strings.Repeat("◈", domain.MaxIconRunes+1) })
	damage("unspecified workspace kind", func(s *snapshotProject) {
		s.WorkspaceRefs[0].Kind = "WORKSPACE_KIND_UNSPECIFIED"
	})
	damage("empty workspace uri", func(s *snapshotProject) { s.WorkspaceRefs[0].URI = "" })
	damage("invalid binding policy", func(s *snapshotProject) {
		s.HarnessBinding = &domain.HarnessBinding{ProviderID: "fake", InstancePolicy: "magic", ResourcePolicyID: "r"}
	})
	damage("unparseable created_at", func(s *snapshotProject) { s.CreatedAt = "yesterday" })
	damage("updated before created", func(s *snapshotProject) {
		s.UpdatedAt = now.Add(-time.Hour).Format(time.RFC3339Nano)
	})
	damage("updated after created", func(s *snapshotProject) {
		s.UpdatedAt = now.Add(time.Hour).Format(time.RFC3339Nano)
	})
	damage("zero timestamps", func(s *snapshotProject) { s.CreatedAt, s.UpdatedAt = "0001-01-01T00:00:00Z", "0001-01-01T00:00:00Z" })

	// A snapshot whose request-bearing fields were replaced with a
	// different but individually legal value no longer reproduces the
	// stored digest and must never be served as a replay.
	damage("renamed snapshot", func(s *snapshotProject) { s.Name = "Tampered Name" })
	damage("re-iconed snapshot", func(s *snapshotProject) { s.Icon = "◆" })
	damage("mutated snapshot refs", func(s *snapshotProject) { s.WorkspaceRefs[0].ReadOnly = true })

	// The untouched snapshot still decodes, so the matrix above fails on its
	// mutations and not on the fixture shape itself.
	if _, err := decodeCreateResult(columns.Result, owner, columnsDigest); err != nil {
		t.Fatalf("valid snapshot must decode: %v", err)
	}
	// A snapshot served under the wrong digest — the digest of a different
	// canonical request — fails closed even though the snapshot itself is
	// internally consistent.
	otherDigest := domain.CreateRequestDigest(valid.Name+" other", valid.Icon, valid.WorkspaceRefs, valid.HarnessBinding)
	if _, err := decodeCreateResult(columns.Result, owner, otherDigest); err == nil {
		t.Fatal("snapshot adjudicated against a foreign digest must fail closed")
	}
}

// A create with a non-null harness binding must replay exactly: the binding
// is a digest-covered request field, so the decoder has to restore it before
// recomputing the digest. This round trip is the regression for the ordering
// bug where the binding was re-attached only after the digest check — legal
// bound replays came back Internal because the recompute saw a nil binding.
func TestDecodeCreateResultRoundTripsHarnessBinding(t *testing.T) {
	t.Parallel()
	owner := "01999999-9999-7999-8999-999999999915"
	now := time.Date(2026, 8, 29, 10, 0, 0, 0, time.UTC)
	binding := &domain.HarnessBinding{
		ProviderID: "fake", InstancePolicy: "ephemeral", ProfileID: "preset-1",
		CredentialRef: "cred-ref", ResourcePolicyID: "foundation",
	}
	valid := domain.Project{
		ID: "01999999-9999-7999-8999-999999999916", OwnerUserID: owner, Name: "Bound Snapshot",
		WorkspaceRefs: []domain.WorkspaceRef{
			{ID: "ws-1", Kind: "WORKSPACE_KIND_LOCAL_DIRECTORY", URI: "file:///work/x"},
		},
		HarnessBinding:        binding,
		InstalledAppIDs:       []string{},
		KnowledgeCollectionID: "01999999-9999-7999-8999-999999999917",
		ArtifactCollectionID:  "01999999-9999-7999-8999-999999999918",
		Revision:              1, CreatedAt: now, UpdatedAt: now,
	}
	columns, err := encodeCreateResult(valid)
	if err != nil {
		t.Fatalf("encode bound snapshot: %v", err)
	}
	digest := domain.CreateRequestDigest(valid.Name, valid.Icon, valid.WorkspaceRefs, valid.HarnessBinding)

	decoded, err := decodeCreateResult(columns.Result, owner, digest)
	if err != nil {
		t.Fatalf("bound snapshot must round trip: %v", err)
	}
	if decoded.HarnessBinding == nil || *decoded.HarnessBinding != *binding {
		t.Fatalf("binding must survive the round trip verbatim: %+v", decoded.HarnessBinding)
	}

	// Stripping the binding from a bound snapshot is well-formed but no
	// longer reproduces the digest: fail closed.
	var snapshot createResultSnapshot
	if err := json.Unmarshal(columns.Result, &snapshot); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	snapshot.Project.HarnessBinding = nil
	body, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("encode fixture: %v", err)
	}
	if _, err := decodeCreateResult(body, owner, digest); err == nil {
		t.Fatal("bound snapshot served without its binding must fail the digest cross-check")
	}
}
