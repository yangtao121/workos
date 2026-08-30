package domain

import "time"

// Ticket is the pairing invitation state machine:
//
//	pending   — freshly rotated, claimable until expiry
//	claimed   — a browser profile locked it with a pending device identity
//	            and may rotate challenges until it completes or expires
//	completed — the pairing proof verified; terminal
//	revoked   — replaced by rotation or explicitly withdrawn; terminal
type TicketState string

const (
	TicketPending   TicketState = "pending"
	TicketClaimed   TicketState = "claimed"
	TicketCompleted TicketState = "completed"
	TicketRevoked   TicketState = "revoked"
)

// PairingTicket is one short-lived, single-use pairing invitation. The
// snapshot (public origin + TLS leaf fingerprint) is pinned at rotation so
// every proof binds exactly the TLS endpoint the operator displayed.
type PairingTicket struct {
	ID             string
	OwnerID        string
	SecretHash     string
	PublicOrigin   string
	TLSFingerprint string
	State          TicketState
	DeviceID       string
	PublicKeyHash  string
	ClaimedName    string
	ClaimedClass   string
	Attempts       int
	ExpiresAt      time.Time
	CreatedAt      time.Time
	ClaimedAt      *time.Time
	CompletedAt    *time.Time
	RevokedAt      *time.Time
}

// Claimable reports whether the ticket can still lock a pending device.
func (t PairingTicket) Claimable(now time.Time) bool {
	return t.State == TicketPending && now.Before(t.ExpiresAt)
}

// Recoverable reports whether a claimed ticket can rotate a new challenge
// for exactly the same pending device identity.
func (t PairingTicket) Recoverable(now time.Time) bool {
	return t.State == TicketClaimed && now.Before(t.ExpiresAt)
}

// PairingInfo is the operator-facing result of a successful rotation. The
// raw secret exists only inside this value: it is returned once to the
// admin/authenticated caller and never persisted or logged.
type PairingInfo struct {
	TicketID       string
	Secret         string
	PairingURL     string
	PublicOrigin   string
	TLSFingerprint string
	ExpiresAt      time.Time
}

// PairingURL renders the canonical QR URL shape. The secret lives only in
// the fragment, never in query or path.
func PairingURL(origin, secret, fingerprint string) string {
	return origin + "/pair#v=1&t=" + secret + "&fp=" + fingerprint
}
