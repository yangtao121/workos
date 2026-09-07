package domain

import "time"

// PushRevocation contains no credential material or user content.
type PushRevocation struct {
	OwnerID    string
	DeviceID   string
	RevokedAt  time.Time
	ClaimToken string
}
