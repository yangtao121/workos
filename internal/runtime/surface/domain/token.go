// Bridge token and effective-capability invariants. The token is a random
// 256-bit CSPRNG secret: it is only ever compared through its sha256 digest
// with a constant-time comparison, and it never leaves the trusted host's
// memory once issued in the CreateSurface response. Domain never imports
// database, Connect, HTTP, or any other module's packages.
package domain

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
)

// ErrTokenEntropy marks a failed token mint (the CSPRNG is unavailable).
// Transport surfaces it as a sanitized Internal verdict.
var ErrTokenEntropy = errors.New("bridge token entropy source failed")

// bridgeTokenEntropyBytes is the token's raw entropy: 256 bits.
const bridgeTokenEntropyBytes = 32

// NewBridgeToken mints one opaque bearer secret: 32 CSPRNG bytes, canonical
// unpadded base64url (43 characters, no formatting fields, not parseable).
func NewBridgeToken() (string, error) {
	buf := make([]byte, bridgeTokenEntropyBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("%w: %v", ErrTokenEntropy, err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// HashBridgeToken derives the at-rest digest of a presented token. Only the
// digest is ever persisted; the plaintext token lives solely in the trusted
// host's memory and the CreateSurface response.
func HashBridgeToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// ValidBridgeToken is the request-boundary grammar for a presented token:
// the canonical unpadded base64url encoding of exactly 32 entropy bytes.
func ValidBridgeToken(value string) bool {
	if len(value) != base64.RawURLEncoding.EncodedLen(bridgeTokenEntropyBytes) {
		return false
	}
	_, err := base64.RawURLEncoding.Strict().DecodeString(value)
	return err == nil
}

// BridgeTokenMatches compares a presented token's digest against the stored
// digest in constant time. An empty stored digest (no token minted, cleared
// by close, or invalidated by rotation) never matches anything.
func BridgeTokenMatches(storedDigest, presentedDigest string) bool {
	if storedDigest == "" || len(storedDigest) != len(presentedDigest) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(storedDigest), []byte(presentedDigest)) == 1
}

// implementedBridgeCapabilities lists the bridge methods that have real
// executors in this slice, canonically sorted. Every other capability —
// artifact/project/knowledge — stays a stored grant fact and never becomes an
// effective bridge capability, no matter what the installation granted.
var implementedBridgeCapabilities = []string{
	"agent.event.watch",
	"agent.task.run",
}

// The canonical capability IDs this bridge can execute, mirrored from the
// registry vocabulary. Capability names are not secrets; the grant facts
// behind them are the authorization.
const (
	BridgeCapabilityAgentTaskRun    = "agent.task.run"
	BridgeCapabilityAgentEventWatch = "agent.event.watch"
)

// EffectiveBridgeCapabilities intersects the installation grant snapshot with
// the implemented bridge methods. The result is canonically sorted (the
// implemented list is sorted), duplicate-free, and shares its backing array
// with nothing: callers may hand it to persistent storage.
func EffectiveBridgeCapabilities(granted []string) []string {
	grantedSet := make(map[string]struct{}, len(granted))
	for _, capability := range granted {
		grantedSet[capability] = struct{}{}
	}
	effective := make([]string, 0, len(implementedBridgeCapabilities))
	for _, capability := range implementedBridgeCapabilities {
		if _, ok := grantedSet[capability]; ok {
			effective = append(effective, capability)
		}
	}
	sort.Strings(effective)
	return effective
}

// BridgeCapabilityGranted reports whether an effective capability list
// authorizes one bridge method.
func BridgeCapabilityGranted(capabilities []string, capability string) bool {
	for _, granted := range capabilities {
		if granted == capability {
			return true
		}
	}
	return false
}
