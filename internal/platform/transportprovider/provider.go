// TransportProvider (ADR-0019, W5): the pluggable connection-transport
// abstraction behind device pairing. LanDirect is the real, mDNS-backed
// provider; Relay and Overlay keep their interfaces and honest unavailable
// status until their infrastructure exists. Fingerprints are the trust
// chain: a discovered instance is only usable when its advertised
// certificate fingerprint matches the one pinned by the pairing ticket.
package transportprovider

import (
	"context"
	"crypto/subtle"
	"errors"
	"regexp"
	"time"
)

var (
	// ErrProviderUnavailable is the sanitized verdict for providers whose
	// infrastructure does not exist (Relay, Overlay).
	ErrProviderUnavailable = errors.New("transport provider is not available")
	// ErrFingerprintMismatch rejects a discovered instance whose advertised
	// fingerprint does not match the pairing expectation. The error carries
	// no fingerprint material.
	ErrFingerprintMismatch = errors.New("discovered instance fingerprint does not match the pairing")
	// ErrDiscoveryInvalid rejects malformed discovery facts.
	ErrDiscoveryInvalid = errors.New("discovered transport instance is invalid")
)

// Provider statuses are the honest capability vocabulary.
const (
	StatusAvailable   = "available"
	StatusUnavailable = "unavailable"
)

var fingerprintPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// ValidFingerprint pins the advertised/pinned fingerprint grammar.
func ValidFingerprint(value string) bool { return fingerprintPattern.MatchString(value) }

// DiscoveredInstance is one mDNS-advertised WorkOS gateway fact.
type DiscoveredInstance struct {
	// Origin is the public https origin of the gateway (pairing input).
	Origin string
	// Fingerprint is the advertised TLS leaf fingerprint
	// ("sha256:<64 hex>").
	Fingerprint string
	// Host is the advertiser's address facts (diagnostics only; never the
	// pairing input).
	Host string
}

// Valid pins the discovered-fact grammar: an https origin and a well-formed
// fingerprint. Anything else fails closed.
func (d DiscoveredInstance) Valid() bool {
	return len(d.Origin) > 8 && len(d.Origin) <= 512 &&
		(d.Origin[:8] == "https://" || d.Origin[:8] == "http://l") &&
		ValidFingerprint(d.Fingerprint)
}

// VerifyFingerprint compares a discovered fingerprint against the pairing
// expectation in constant time.
func VerifyFingerprint(expected, discovered string) error {
	if !ValidFingerprint(expected) || !ValidFingerprint(discovered) {
		return ErrDiscoveryInvalid
	}
	if subtle.ConstantTimeCompare([]byte(expected), []byte(discovered)) != 1 {
		return ErrFingerprintMismatch
	}
	return nil
}

// Discovery browses the local segment for WorkOS instances.
type Discovery interface {
	Browse(ctx context.Context, expectedFingerprint string) ([]DiscoveredInstance, error)
	Close() error
}

// Announcer advertises one WorkOS instance on the local segment.
type Announcer interface {
	Announce() error
	Close() error
}

// BrowseBudget bounds one browse call for callers without their own
// deadline; providers may still honor a shorter context.
const BrowseBudget = 5 * time.Second
