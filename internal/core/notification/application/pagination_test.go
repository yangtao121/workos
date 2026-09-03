package application

import (
	"testing"
	"time"

	"github.com/yangtao121/workos/internal/core/notification/ports"
)

func TestPageTokenBindsSnapshotWatermark(t *testing.T) {
	owner := "0194a1f0-0000-7000-8000-000000000001"
	cursor := ports.Cursor{
		CreatedAt: time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC),
		ID:        "0194a1f0-0000-7000-8000-000000000002",
	}
	filter := ports.Filter{UnreadOnly: true}
	token, err := encodePageToken(cursor, owner, filter, 17)
	if err != nil {
		t.Fatal(err)
	}
	decoded, watermark, err := decodePageToken(token, owner, filter)
	if err != nil {
		t.Fatal(err)
	}
	if decoded != cursor || watermark != 17 {
		t.Fatalf("decoded token = %+v/%d, want %+v/17", decoded, watermark, cursor)
	}
	if _, _, err := decodePageToken(token, owner, ports.Filter{}); err == nil {
		t.Fatal("cross-filter token accepted")
	}
}
