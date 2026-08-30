// Package domain holds the Gateway-owned device authentication facts and
// their grammars: paired device credentials, pairing tickets, proof
// challenges, device sessions, and the versioned proof transcript. The
// package imports only the standard library — persistence, transport,
// entropy, and time live behind the application ports.
package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
)

// Sanitized verdict errors. Transport maps them with errors.Is onto the
// fixed public error matrix; their messages never carry request facts.
var (
	ErrInvalidRequest       = errors.New("invalid device authentication request")
	ErrAuthenticationFailed = errors.New("device authentication failed")
	ErrDeviceNotFound       = errors.New("device not found")
	ErrConflict             = errors.New("device changed / request conflict")
	ErrRateLimited          = errors.New("too many attempts")
	ErrStoreUnavailable     = errors.New("gateway auth unavailable")
	ErrAuthCorrupt          = errors.New("device authentication failed")
)

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// ValidUUID reports whether value is a canonical lowercase UUID as minted by
// the server. Client-supplied identifiers never pass through unvalidated.
func ValidUUID(value string) bool {
	return uuidPattern.MatchString(value)
}

// ValidUUIDv7 reports whether value is a canonical lowercase UUIDv7, the
// grammar every server-minted identifier and paging cursor uses.
func ValidUUIDv7(value string) bool {
	if !uuidPattern.MatchString(value) {
		return false
	}
	return value[14] == '7'
}

// DeviceClass is the bounded client device grammar stored with a credential.
type DeviceClass string

const (
	DeviceClassDesktop  DeviceClass = "desktop"
	DeviceClassTablet   DeviceClass = "tablet"
	DeviceClassFoldable DeviceClass = "foldable"
	DeviceClassPhone    DeviceClass = "phone"
)

// ParseDeviceClass accepts only the known enum values; unknown and
// unspecified classes are invalid requests.
func ParseDeviceClass(value string) (DeviceClass, error) {
	switch DeviceClass(value) {
	case DeviceClassDesktop, DeviceClassTablet, DeviceClassFoldable, DeviceClassPhone:
		return DeviceClass(value), nil
	default:
		return "", fmt.Errorf("%w: unknown device class", ErrInvalidRequest)
	}
}

// Device is one durable paired browser profile credential.
type Device struct {
	ID                  string
	OwnerID             string
	Name                string
	Class               DeviceClass
	PublicKeySPKI       []byte
	PublicKeyHash       string
	Revision            int64
	CreatedAt           time.Time
	LastAuthenticatedAt time.Time
	RevokedAt           *time.Time
}

// Active reports whether the credential is usable at now.
func (d Device) Active(now time.Time) bool {
	return d.RevokedAt == nil
}

func (d Device) RevokedAtIfSet() time.Time {
	if d.RevokedAt == nil {
		return time.Time{}
	}
	return *d.RevokedAt
}

// ValidateDeviceName trims surrounding Unicode whitespace and enforces the
// bounded device-name grammar: 1..80 Unicode code points, valid UTF-8, no
// C0/C1 control characters (which includes DEL).
func ValidateDeviceName(raw string) (string, error) {
	name := strings.TrimFunc(raw, unicode.IsSpace)
	if name == "" {
		return "", fmt.Errorf("%w: device name is empty", ErrInvalidRequest)
	}
	if len([]rune(name)) > 80 {
		return "", fmt.Errorf("%w: device name is too long", ErrInvalidRequest)
	}
	for _, r := range name {
		// Reject C0 control characters, DEL, and the C1 range outright;
		// every other code point is printable context for the UI.
		if r <= 0x9f && !(r >= 0x20 && r < 0x7f) {
			return "", fmt.Errorf("%w: device name has control characters", ErrInvalidRequest)
		}
	}
	return name, nil
}

const hashPattern = `^sha256:[0-9a-f]{64}$`

var hashRegex = regexp.MustCompile(hashPattern)

// ValidDigest reports whether value matches the canonical digest grammar.
func ValidDigest(value string) bool { return hashRegex.MatchString(value) }

// domainHash produces a domain-separated SHA-256 digest so a token of one
// kind can never be looked up in another kind's index.
func domainHash(tag string, value []byte) string {
	h := sha256.New()
	_, _ = h.Write([]byte(tag))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write(value)
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

const (
	pairingTicketHashTag = "workos.pairing-ticket/v1"
	deviceSessionHashTag = "workos.device-session/v1"
)

// HashPairingSecret digests a raw pairing secret for storage and lookup.
func HashPairingSecret(raw []byte) string { return domainHash(pairingTicketHashTag, raw) }

// HashSessionToken digests a raw session token for storage and lookup.
func HashSessionToken(raw []byte) string { return domainHash(deviceSessionHashTag, raw) }

// RevocationSnapshot is the immutable first-result snapshot persisted with
// every revocation idempotency key. result_version pins its shape.
type RevocationSnapshot struct {
	ResultVersion string `json:"result_version"`
	DeviceID      string `json:"device_id"`
	Name          string `json:"name"`
	Class         string `json:"device_class"`
	Revision      int64  `json:"revision"`
	RevokedAt     string `json:"revoked_at"`
}

// ParseRevocationSnapshot decodes a stored snapshot; corruption is an
// internal failure, never silently repaired.
func ParseRevocationSnapshot(raw json.RawMessage) (RevocationSnapshot, error) {
	var snapshot RevocationSnapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return RevocationSnapshot{}, fmt.Errorf("%w: revocation snapshot: %w", ErrAuthCorrupt, err)
	}
	if snapshot.ResultVersion != "v1" || !ValidUUID(snapshot.DeviceID) {
		return RevocationSnapshot{}, fmt.Errorf("%w: revocation snapshot shape", ErrAuthCorrupt)
	}
	return snapshot, nil
}

// IsVerdict reports whether err already carries one of the sanitized
// verdicts, so adapter wrapping never double-maps it.
func IsVerdict(err error) bool {
	return errors.Is(err, ErrInvalidRequest) || errors.Is(err, ErrAuthenticationFailed) ||
		errors.Is(err, ErrDeviceNotFound) || errors.Is(err, ErrConflict) ||
		errors.Is(err, ErrRateLimited) || errors.Is(err, ErrStoreUnavailable) ||
		errors.Is(err, ErrAuthCorrupt)
}
