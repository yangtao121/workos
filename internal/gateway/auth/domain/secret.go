package domain

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"time"
)

// SecretBytes is the fixed entropy budget of every pairing secret and
// session token: 32 crypto/rand bytes.
const SecretBytes = 32

// PairingSecretTTLBounds are the accepted configuration bounds (ADR-0007).
const (
	TicketMinTTL    = time.Minute
	TicketMaxTTL    = 15 * time.Minute
	ChallengeMinTTL = 30 * time.Second
	ChallengeMaxTTL = 5 * time.Minute
	SessionMinTTL   = 5 * time.Minute
	SessionMaxTTL   = 30 * 24 * time.Hour
)

// EncodeSecret renders 32 random bytes as base64url without padding — the
// only secret grammar the pairing URL fragment and the session cookie use.
func EncodeSecret(raw []byte) (string, error) {
	if len(raw) != SecretBytes {
		return "", fmt.Errorf("%w: secret must be %d bytes", ErrAuthCorrupt, SecretBytes)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// ParsePairingSecret decodes the fragment secret with the strict grammar:
// base64url without padding, exactly 43 characters, exactly 32 bytes. Any
// deviation is an invalid request, not a lookup miss.
func ParsePairingSecret(raw string) ([]byte, error) {
	if len(raw) != 43 {
		return nil, fmt.Errorf("%w: pairing secret grammar", ErrInvalidRequest)
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || len(decoded) != SecretBytes {
		return nil, fmt.Errorf("%w: pairing secret grammar", ErrInvalidRequest)
	}
	return decoded, nil
}

// NewSecret returns 32 fresh crypto/rand bytes. The process fails closed if
// the platform entropy source is unavailable.
func NewSecret() ([]byte, error) {
	raw := make([]byte, SecretBytes)
	if _, err := rand.Read(raw); err != nil {
		return nil, fmt.Errorf("read crypto/rand entropy: %w", ErrStoreUnavailable)
	}
	return raw, nil
}
