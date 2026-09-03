package transport

import (
	"testing"
	"time"

	"github.com/yangtao121/workos/internal/core/notification/domain"
)

func TestNotificationProtoUsesLatestDurableRevision(t *testing.T) {
	fact := domain.Notification{
		ID:                    "0194a1f0-0000-7000-8000-000000000001",
		CreatedAt:             time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC),
		CreatedChangeSequence: 7,
	}
	if got := notificationProto(fact).GetRevision(); got != 7 {
		t.Fatalf("unread revision = %d, want 7", got)
	}
	fact.ReadChangeSequence = 11
	if got := notificationProto(fact).GetRevision(); got != 11 {
		t.Fatalf("read revision = %d, want 11", got)
	}
}
