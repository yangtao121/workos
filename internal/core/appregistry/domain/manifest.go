package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
)

// Scope is the manifest deployment scope. `system` exists in the canonical
// schema and the public enum, but public registration rejects it; system apps
// require a future trusted installation path.
type Scope string

const (
	ScopeUser    Scope = "user"
	ScopeProject Scope = "project"
	ScopeSystem  Scope = "system"
)

// knownPermissions is the single vendor-neutral capability vocabulary that
// public manifests may request. Unknown capability IDs fail closed. A listed
// permission is only a request; the Registry never mints grants or tokens.
var knownPermissions = map[string]struct{}{
	"agent.task.run":    {},
	"agent.event.watch": {},
	"artifact.read":     {},
	"artifact.write":    {},
	"knowledge.read":    {},
	"project.read":      {},
}

// KnownPermission reports whether the capability ID belongs to the vocabulary.
func KnownPermission(id string) bool {
	_, ok := knownPermissions[id]
	return ok
}

// Permissions returns the full known capability vocabulary, sorted.
func Permissions() []string {
	result := make([]string, 0, len(knownPermissions))
	for id := range knownPermissions {
		result = append(result, id)
	}
	sort.Strings(result)
	return result
}

// Manifest is the validated, immutable fact extracted from one manifest
// document. CanonicalJSON is the normalized manifest value (canonical JSON,
// permissions sorted) and Digest is derived from exactly those bytes.
// RuntimeType and WebBundle project the additive launch descriptor: they are
// derived from exactly the canonical bytes, never from a second schema.
type Manifest struct {
	ID            string
	Name          string
	Version       string
	Scope         Scope
	Permissions   []string
	RuntimeType   string
	WebBundle     *WebBundleRef
	CanonicalJSON []byte
	Digest        string
}

// RuntimeTypeWebBundle marks manifests whose single supported surface is an
// immutable web bundle artifact.
const RuntimeTypeWebBundle = "web-bundle"

// WebBundleRef is the launch descriptor a manifest pins: the owner's exact
// immutable artifact and its canonical digest. The digest is part of the
// canonical manifest bytes, so the manifest digest covers it.
type WebBundleRef struct {
	ArtifactID     string
	ArtifactDigest string
}

// ParseWebBundleRef extracts the launch descriptor from canonical manifest
// bytes. It is the trusted internal read used when resolving installed
// instances: anything unexpected is a corrupt invariant, reported by ok=false
// rather than an error with content.
func ParseWebBundleRef(canonical []byte) (WebBundleRef, bool) {
	var document struct {
		Runtime struct {
			Type           string `json:"type"`
			ArtifactID     string `json:"artifactId"`
			ArtifactDigest string `json:"artifactDigest"`
		} `json:"runtime"`
	}
	if err := json.Unmarshal(canonical, &document); err != nil {
		return WebBundleRef{}, false
	}
	if document.Runtime.Type != RuntimeTypeWebBundle {
		return WebBundleRef{}, false
	}
	if !ValidWebBundleArtifactID(document.Runtime.ArtifactID) ||
		!ValidWebBundleArtifactDigest(document.Runtime.ArtifactDigest) {
		return WebBundleRef{}, false
	}
	return WebBundleRef{
		ArtifactID:     document.Runtime.ArtifactID,
		ArtifactDigest: document.Runtime.ArtifactDigest,
	}, true
}

// ValidWebBundleArtifactID matches the schema's UUIDv7-shaped artifact id.
func ValidWebBundleArtifactID(value string) bool {
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
	// UUIDv7 markers: version nibble 7 and RFC variant.
	return value[14] == '7' && (value[19] == '8' || value[19] == '9' || value[19] == 'a' || value[19] == 'b')
}

// ValidWebBundleArtifactDigest matches the canonical sha256 digest shape.
func ValidWebBundleArtifactDigest(value string) bool {
	if len(value) != len(DigestPrefix)+64 || value[:len(DigestPrefix)] != DigestPrefix {
		return false
	}
	for _, c := range value[len(DigestPrefix):] {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// DigestPrefix is the fixed format of a manifest digest: sha256 lowercase hex.
const DigestPrefix = "sha256:"

// MaxManifestBytes bounds manifest input at transport and application layers.
const MaxManifestBytes = 256 * 1024

// ManifestDigest derives the deterministic manifest digest from canonical bytes.
func ManifestDigest(canonical []byte) string {
	sum := sha256.Sum256(canonical)
	return DigestPrefix + hex.EncodeToString(sum[:])
}

// CanonicalJSON encodes a JSON-compatible value tree deterministically: object
// keys sorted by byte order, integers as decimal int64, floats with
// strconv.FormatFloat('g', -1, 64), strings with encoding/json escaping, and
// arrays preserving order. It rejects values that are not JSON-compatible.
func CanonicalJSON(value any) ([]byte, error) {
	buffer := make([]byte, 0, 256)
	encoded, err := appendCanonical(buffer, value, 0)
	if err != nil {
		return nil, err
	}
	return encoded, nil
}

const canonicalMaxDepth = 64

func appendCanonical(buffer []byte, value any, depth int) ([]byte, error) {
	if depth > canonicalMaxDepth {
		return nil, fmt.Errorf("value exceeds canonical nesting depth")
	}
	switch typed := value.(type) {
	case nil:
		return append(buffer, "null"...), nil
	case bool:
		if typed {
			return append(buffer, "true"...), nil
		}
		return append(buffer, "false"...), nil
	case string:
		encoded, err := json.Marshal(typed)
		if err != nil {
			return nil, fmt.Errorf("encode string: %w", err)
		}
		return append(buffer, encoded...), nil
	case int64:
		return strconv.AppendInt(buffer, typed, 10), nil
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) {
			return nil, fmt.Errorf("non-finite numbers are not allowed")
		}
		return strconv.AppendFloat(buffer, typed, 'g', -1, 64), nil
	case []any:
		buffer = append(buffer, '[')
		for index, item := range typed {
			if index > 0 {
				buffer = append(buffer, ',')
			}
			var err error
			buffer, err = appendCanonical(buffer, item, depth+1)
			if err != nil {
				return nil, err
			}
		}
		return append(buffer, ']'), nil
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		buffer = append(buffer, '{')
		for index, key := range keys {
			if index > 0 {
				buffer = append(buffer, ',')
			}
			encodedKey, err := json.Marshal(key)
			if err != nil {
				return nil, fmt.Errorf("encode object key: %w", err)
			}
			buffer = append(buffer, encodedKey...)
			buffer = append(buffer, ':')
			buffer, err = appendCanonical(buffer, typed[key], depth+1)
			if err != nil {
				return nil, err
			}
		}
		return append(buffer, '}'), nil
	default:
		return nil, fmt.Errorf("value of type %T is not JSON-compatible", value)
	}
}
