// Package postgres tests that need no database: row-shape coverage of the
// descriptor projection, which a type switch silently drops otherwise.
package postgres

import (
	"strings"
	"testing"

	"github.com/yangtao121/workos/internal/runtime/surface/adapters/postgres/surfacedb"
	"github.com/yangtao121/workos/internal/runtime/surface/domain"
)

// TestSessionRowShapesCarryLaunchDescriptor pins surfacedbLaunchDescriptor to
// every sqlc row shape that returns a full session row. A missing case falls
// through to the empty descriptor — the atomic bridge-token rotation shipped
// exactly that bug, returning sessions with a lost launch descriptor.
func TestSessionRowShapesCarryLaunchDescriptor(t *testing.T) {
	want := domain.LaunchDescriptor{
		AppID: "notes-app", Version: "1.0.0",
		ManifestDigest: "sha256:" + strings.Repeat("a", 64),
		ArtifactID:     "0198d7ea-2110-7c42-b659-c5e4d73bc343",
		ArtifactDigest: "sha256:" + strings.Repeat("b", 64),
		Entrypoint:     "index.html",
	}
	columns := func() (appID, appVersion, manifestDigest, artifactID, artifactDigest, entrypoint string) {
		return want.AppID, want.Version, want.ManifestDigest, want.ArtifactID, want.ArtifactDigest, want.Entrypoint
	}
	appID, appVersion, manifestDigest, artifactID, artifactDigest, entrypoint := columns()
	cases := []struct {
		name string
		row  any
	}{
		{"GetSession", surfacedb.GetSessionRow{AppID: appID, AppVersion: appVersion, ManifestDigest: manifestDigest, ArtifactID: artifactID, ArtifactDigest: artifactDigest, Entrypoint: entrypoint}},
		{"GetActiveSession", surfacedb.GetActiveSessionRow{AppID: appID, AppVersion: appVersion, ManifestDigest: manifestDigest, ArtifactID: artifactID, ArtifactDigest: artifactDigest, Entrypoint: entrypoint}},
		{"GetActiveSessionByBridgeToken", surfacedb.GetActiveSessionByBridgeTokenRow{AppID: appID, AppVersion: appVersion, ManifestDigest: manifestDigest, ArtifactID: artifactID, ArtifactDigest: artifactDigest, Entrypoint: entrypoint}},
		{"RotateSessionBridgeToken", surfacedb.RotateSessionBridgeTokenRow{AppID: appID, AppVersion: appVersion, ManifestDigest: manifestDigest, ArtifactID: artifactID, ArtifactDigest: artifactDigest, Entrypoint: entrypoint}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := surfacedbLaunchDescriptor(tc.row); got != want {
				t.Fatalf("descriptor projection lost fields for %T: %+v", tc.row, got)
			}
		})
	}
}

// TestSessionRowShapesCarryGrantRevision pins surfacedbGrantRevision to every
// sqlc row shape that returns a full session row, mirroring the descriptor
// coverage above: a missing case silently yields epoch 0, which no validated
// session may ever carry (migration 012 backfills 1 and the CHECK rejects
// anything below it).
func TestSessionRowShapesCarryGrantRevision(t *testing.T) {
	const want int64 = 7
	cases := []struct {
		name string
		row  any
	}{
		{"GetSession", surfacedb.GetSessionRow{InstallationGrantRevision: want}},
		{"GetActiveSession", surfacedb.GetActiveSessionRow{InstallationGrantRevision: want}},
		{"GetActiveSessionByBridgeToken", surfacedb.GetActiveSessionByBridgeTokenRow{InstallationGrantRevision: want}},
		{"RotateSessionBridgeToken", surfacedb.RotateSessionBridgeTokenRow{InstallationGrantRevision: want}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := surfacedbGrantRevision(tc.row); got != want {
				t.Fatalf("grant revision projection lost the epoch for %T: %d", tc.row, got)
			}
		})
	}
	if got := surfacedbGrantRevision(struct{}{}); got != 0 {
		t.Fatalf("unknown row shape must fail closed to 0, got %d", got)
	}
}
