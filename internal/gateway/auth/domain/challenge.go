package domain

import "time"

// ChallengePurpose distinguishes the two proof flows.
type ChallengePurpose string

const (
	ChallengePairing ChallengePurpose = "pairing"
	ChallengeSession ChallengePurpose = "session"
)

// ChallengeResult is the consumption verdict. result + consumed_at are set
// together by a guarded single-shot UPDATE.
type ChallengeResult string

const (
	ChallengeVerified ChallengeResult = "verified"
	ChallengeFailed   ChallengeResult = "failed"
)

// Challenge is one single-use proof challenge. Session challenges issued for
// unknown devices are persisted with an empty DeviceID: they burn attempts
// and consumption exactly like real ones, so response shapes and timing stay
// aligned without revealing device existence.
type Challenge struct {
	ID            string
	Purpose       ChallengePurpose
	DeviceID      string
	TicketID      string
	PublicKeyHash string
	Nonce         []byte
	Attempts      int
	ExpiresAt     time.Time
	CreatedAt     time.Time
	ConsumedAt    *time.Time
	ConsumedByDev string
	Result        ChallengeResult
}

// Usable reports whether the challenge can still be consumed at now.
func (c Challenge) Usable(now time.Time) bool {
	return c.ConsumedAt == nil && now.Before(c.ExpiresAt)
}
