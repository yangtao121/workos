// Package domain holds the runtime Surface Broker's invariants: the
// owner/device-bound surface session fact, its canonical request digest, and
// the request-boundary grammar. Domain never imports database, Connect, HTTP,
// or any other module's packages.
package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

var (
	// ErrInvalid marks a request that violates the surface grammar.
	ErrInvalid = errors.New("surface request is invalid")
	// ErrNotFound marks an unknown, foreign, closed, or expired session.
	ErrNotFound = errors.New("surface session is not available")
	// ErrIdempotencyConflict marks a create key consumed by a different
	// canonical request.
	ErrIdempotencyConflict = errors.New("surface idempotency key was used for a different request")
	// ErrUnsupported marks an installed app without a supported web bundle
	// launch descriptor.
	ErrUnsupported = errors.New("installed app has no supported surface")
	// ErrUnavailable marks a temporarily unreachable Core resolver or store.
	ErrUnavailable = errors.New("surface resolution is temporarily unavailable")
)

// RendererWebBundle is the only implemented surface renderer.
const RendererWebBundle = "web-bundle"

// MaxSessionIdempotencyKeyRunes bounds the create-command key.
const MaxSessionIdempotencyKeyRunes = 128

// LaunchDescriptor is the immutable Core-resolved launch fact snapshotted
// into the session. It is a display/cache fact only: every asset request is
// revalidated by Core against authoritative installation state.
type LaunchDescriptor struct {
	AppID          string
	Version        string
	ManifestDigest string
	ArtifactID     string
	ArtifactDigest string
	Entrypoint     string
}

// SurfaceSession is one owner/device-bound surface launch.
type SurfaceSession struct {
	ID             string
	OwnerUserID    string
	DeviceID       string
	IdempotencyKey string
	RequestDigest  string
	ProjectID      string
	AppInstanceID  string
	Renderer       string
	Descriptor     LaunchDescriptor
	Path           string
	CreatedAt      time.Time
	ExpiresAt      time.Time
	ClosedAt       *time.Time
}

// ValidSessionUUID reports whether value is a canonical lowercase hyphenated
// UUIDv7: 8-4-4-4-12 hex with the version nibble fixed at 7 and an RFC 4122
// variant. Non-canonical shapes (uppercase, v1/v4, wrong variant) are
// rejected so stored IDs, create references, close targets, and HTTP paths
// all share one strict grammar.
func ValidSessionUUID(value string) bool {
	return validUUIDv7(value)
}

// validUUIDv7 is the canonical identifier grammar for this module: 36
// characters, hyphens at the fixed positions, lowercase hex only, version
// nibble 7, and variant [89ab].
func validUUIDv7(value string) bool {
	if len(value) != 36 {
		return false
	}
	for index, c := range []byte(value) {
		switch index {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
				return false
			}
		}
	}
	if value[14] != '7' {
		return false
	}
	switch value[19] {
	case '8', '9', 'a', 'b':
		return true
	default:
		return false
	}
}

// ValidSessionIdempotencyKey enforces the command key grammar: valid UTF-8,
// no control characters, bounded length, never trimmed.
func ValidSessionIdempotencyKey(value string) bool {
	count := 0
	for _, r := range value {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			return false
		}
		count++
		if count > MaxSessionIdempotencyKeyRunes {
			return false
		}
	}
	return count > 0
}

// ValidDeviceClass accepts exactly the explicit device classes; unspecified
// is rejected because a surface is device-shaped by definition.
func ValidDeviceClass(value string) bool {
	switch value {
	case "desktop", "tablet", "foldable", "phone":
		return true
	default:
		return false
	}
}

// ValidPreferredRenderer accepts only unspecified or the implemented
// renderer.
func ValidPreferredRenderer(value string) bool {
	return value == "" || value == RendererWebBundle
}

// ViewportBounds are the accepted surface viewport extents.
const (
	minViewportExtent = 1
	maxViewportExtent = 16384
	maxPixelRatio     = 16
)

// ValidViewport guards the client-declared viewport. The pixel ratio must be
// finite: NaN and both infinities are rejected outright. Zero is the single
// canonical "unspecified" ratio — negative zero compares equal to zero and
// canonicalizes identically in the request digest, so the two encodings can
// never split one logical request into two idempotency outcomes.
func ValidViewport(width, height int32, pixelRatio float64) bool {
	if width < minViewportExtent || width > maxViewportExtent || height < minViewportExtent || height > maxViewportExtent {
		return false
	}
	if math.IsNaN(pixelRatio) || math.IsInf(pixelRatio, 0) {
		return false
	}
	if pixelRatio < 0 || pixelRatio > maxPixelRatio {
		return false
	}
	return true
}

// CreateRequestDigest digests the canonical create request: the trusted
// device identity, project, installation, device class, viewport, and
// renderer. The device ID arrives only from the gateway-injected identity
// context — it is never part of the public request body — and its inclusion
// makes one idempotency key mean exactly one (owner, device, request)
// combination: a replay from another device is a stable conflict, not a
// session lookup miss. Timestamps and random identifiers are never mixed in,
// so replays of the same intent match.
func CreateRequestDigest(deviceID, projectID, appInstanceID, deviceClass string, width, height int32, pixelRatio float64, renderer string) string {
	if pixelRatio == 0 {
		pixelRatio = 0 // collapse negative zero onto the canonical unspecified zero
	}
	canonical := fmt.Sprintf("workos.surface-create.v1|%s|%s|%s|%s|%d|%d|%g|%s",
		deviceID, projectID, appInstanceID, deviceClass, width, height, pixelRatio, renderer)
	sum := sha256.Sum256([]byte(canonical))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// SessionPath is the same-origin relative URL prefix of a session.
func SessionPath(sessionID string) string {
	return "/surfaces/" + sessionID + "/"
}

// maxAssetPathBytes bounds the normalized asset path in the HTTP boundary.
const maxAssetPathBytes = 240

// NormalizeAssetPath validates a raw HTTP asset path (before any percent
// decoding — encoded paths are rejected outright) and returns the clean
// relative path, or empty for the session root (the entrypoint). It fails
// closed on traversal, backslashes, dot segments, duplicate slashes, and
// control or non-ASCII bytes.
func NormalizeAssetPath(rawPath string) (string, error) {
	if rawPath == "" || rawPath == "/" {
		return "", nil
	}
	if len(rawPath) > maxAssetPathBytes {
		return "", ErrInvalid
	}
	if strings.ContainsAny(rawPath, "%\\\x00\r\n\t") || rawPath[0] == '/' {
		return "", ErrInvalid
	}
	segments := strings.Split(rawPath, "/")
	for _, segment := range segments {
		if segment == "" || segment == "." || segment == ".." {
			return "", ErrInvalid
		}
		for index := 0; index < len(segment); index++ {
			c := segment[index]
			if c < 0x21 || c > 0x7e {
				return "", ErrInvalid
			}
			if c == ':' || c == '?' || c == '#' || c == '&' || c == '=' {
				return "", ErrInvalid
			}
		}
	}
	return rawPath, nil
}
