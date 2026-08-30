package domain

import "time"

// DeviceSession is one opaque bearer session. Only the domain-separated
// token hash is ever persisted; the raw token exists exactly once — inside
// the Set-Cookie header of the response that created it.
type DeviceSession struct {
	ID         string
	OwnerID    string
	DeviceID   string
	TokenHash  string
	CreatedAt  time.Time
	ExpiresAt  time.Time
	LastSeenAt *time.Time
	RevokedAt  *time.Time
}

// Active reports whether the session may authorize a request at now.
// Expiry is absolute: activity never extends it.
func (s DeviceSession) Active(now time.Time) bool {
	return s.RevokedAt == nil && now.Before(s.ExpiresAt)
}

// SessionIdentity is the trusted per-request identity the Gateway derives
// from a validated session and injects upstream. It is created only by the
// application service; request input can never construct one.
type SessionIdentity struct {
	OwnerID   string
	DeviceID  string
	SessionID string
	ExpiresAt time.Time
}
