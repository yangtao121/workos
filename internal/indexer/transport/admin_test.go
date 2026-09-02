package transport

import (
	"testing"
	"time"

	indexerapp "github.com/yangtao121/workos/internal/indexer/application"
)

func TestAdminProjectionRedactsRawScopeIdentifiers(t *testing.T) {
	t.Parallel()
	now := time.Unix(1, 0).UTC()
	projected := rebuildJobProto(indexerapp.RebuildJobView{
		ID: "01999999-9999-7999-8999-000000000061", Scope: "project",
		OwnerUserID: "01999999-9999-7999-8999-000000000062",
		ProjectID:   "01999999-9999-7999-8999-000000000063",
		State:       "requested", TargetGeneration: "01999999-9999-7999-8999-000000000064",
		CreatedAt: now, UpdatedAt: now,
	})
	if projected.GetOwnerUserId() != "" || projected.GetProjectId() != "" {
		t.Fatalf("admin projection leaked scope identifiers: %#v", projected)
	}
}

func TestSearchTimeProjectionDoesNotFabricateYearOne(t *testing.T) {
	t.Parallel()
	if got := formatSearchTime(time.Time{}); got != "" {
		t.Fatalf("zero time projected as %q", got)
	}
}
