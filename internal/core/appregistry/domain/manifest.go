package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
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
	"agent.task.run":       {},
	"agent.event.watch":    {},
	"artifact.read":        {},
	"artifact.write":       {},
	"knowledge.read":       {},
	"notifications.create": {},
	"project.read":         {},
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
	Container     *ContainerLaunch
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

// RuntimeTypeContainer marks manifests whose surface is served from a
// digest-pinned OCI image by the runtime host's supervised rootless engine.
// The container profile is strict: see ParseContainerLaunch and the validator's
// container policy (ADR-0006).
const RuntimeTypeContainer = "container"

// ContainerResourcePolicy is the App's requested resource policy. Every value
// is a request, never an authorization: the runtime adjudicates it against
// server-owned maxima (ADR-0006 §2) and persists the effective policy.
type ContainerResourcePolicy struct {
	CPUHardCores float64
	MemoryHighMB int64
	MemoryMaxMB  int64
	PidsMax      int64
}

// ContainerHealthPolicy is the App's requested health policy.
type ContainerHealthPolicy struct {
	HTTPPath       string
	StartupSeconds int64
	RestartLimit   int64
}

// ContainerLaunch is the launch descriptor a container manifest pins: the
// exact digest-pinned image reference, the bounded argv, the container port,
// and the requested resource/health policies. The digest is part of the
// canonical manifest bytes, so the manifest digest covers every field here.
type ContainerLaunch struct {
	Image     string
	Command   []string
	Port      int64
	Resources ContainerResourcePolicy
	Health    ContainerHealthPolicy
}

// ParseContainerLaunch extracts the container launch descriptor from canonical
// manifest bytes. It is the trusted internal read used when resolving
// installed instances; it re-validates every grammar so a stored manifest that
// violates its own profile is reported as corrupt (ok=false) instead of being
// launched. Unsupported runtime types are also ok=false.
func ParseContainerLaunch(canonical []byte) (ContainerLaunch, bool) {
	var document struct {
		Runtime struct {
			Type    string   `json:"type"`
			Image   string   `json:"image"`
			Command []string `json:"command"`
			Port    int64    `json:"port"`
		} `json:"runtime"`
		Resources struct {
			CPUHardCores float64 `json:"cpuHard"`
			MemoryHighMB int64   `json:"memoryHighMb"`
			MemoryMaxMB  int64   `json:"memoryMaxMb"`
			PidsMax      int64   `json:"pidsMax"`
		} `json:"resources"`
		Health struct {
			HTTPPath       string `json:"httpPath"`
			StartupSeconds int64  `json:"startupSeconds"`
			RestartLimit   int64  `json:"restartLimit"`
		} `json:"health"`
	}
	if err := json.Unmarshal(canonical, &document); err != nil {
		return ContainerLaunch{}, false
	}
	if document.Runtime.Type != RuntimeTypeContainer {
		return ContainerLaunch{}, false
	}
	if !ValidContainerImage(document.Runtime.Image) {
		return ContainerLaunch{}, false
	}
	if !ValidContainerCommand(document.Runtime.Command) {
		return ContainerLaunch{}, false
	}
	if document.Runtime.Port < 1 || document.Runtime.Port > 65535 {
		return ContainerLaunch{}, false
	}
	resources := ContainerResourcePolicy{
		CPUHardCores: document.Resources.CPUHardCores,
		MemoryHighMB: document.Resources.MemoryHighMB,
		MemoryMaxMB:  document.Resources.MemoryMaxMB,
		PidsMax:      document.Resources.PidsMax,
	}
	health := ContainerHealthPolicy{
		HTTPPath:       document.Health.HTTPPath,
		StartupSeconds: document.Health.StartupSeconds,
		RestartLimit:   document.Health.RestartLimit,
	}
	if !ValidContainerResourcePolicy(resources) || !ValidContainerHealthPolicy(health) {
		return ContainerLaunch{}, false
	}
	return ContainerLaunch{
		Image:     document.Runtime.Image,
		Command:   document.Runtime.Command,
		Port:      document.Runtime.Port,
		Resources: resources,
		Health:    health,
	}, true
}

// Container policy bounds. These mirror the validator's cross-field rules and
// are re-checked at parse time so a stored canonical manifest that violates
// its own profile can never resolve (ADR-0006 §2). cpuHardCores is decimal;
// every other quantity is an integer in its manifest field.
const (
	MinCPUHardCores    = 0.1
	MaxCPUHardCores    = 4.0
	MinMemoryHighMB    = 16
	MaxMemoryHighMB    = 1024
	MinMemoryMaxMB     = 32
	MaxMemoryMaxMB     = 2048
	MinPidsMax         = 8
	MaxPidsMax         = 512
	MinStartupSeconds  = 1
	MaxStartupSeconds  = 120
	MinRestartLimit    = 0
	MaxRestartLimit    = 8
	MaxCommandItems    = 16
	MaxCommandArgRunes = 4096
)

// ValidContainerImage enforces the exact immutable reference grammar:
// one "@sha256:" separator, a 64-character lowercase hex digest, a lowercase
// registry/repository name with an optional numeric registry port, and never
// a tag, credentials, or control characters. Tags are rejected outright: a
// mutable tag may never masquerade as an immutable pin.
func ValidContainerImage(value string) bool {
	reference, digest, ok := strings.Cut(value, "@sha256:")
	if !ok || reference == "" || len(digest) != 64 {
		return false
	}
	for _, c := range []byte(digest) {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	if strings.Contains(reference, "@") {
		// A second '@' can only be credential material (user:pass@host) or a
		// second pin; both are rejected.
		return false
	}
	host, path, hasPath := strings.Cut(reference, "/")
	if host == "" {
		return false
	}
	// The registry host may carry exactly one numeric port; the repository
	// path may not carry colons (which would be a tag in OCI grammar).
	if strings.Contains(path, ":") || strings.Contains(host, "@") {
		return false
	}
	if !validContainerHost(host) {
		return false
	}
	if hasPath {
		for _, component := range strings.Split(path, "/") {
			if !validContainerPathComponent(component) {
				return false
			}
		}
	}
	return true
}

func validContainerHost(host string) bool {
	hostOnly, port, hasPort := strings.Cut(host, ":")
	if hostOnly == "" {
		return false
	}
	for index := 0; index < len(hostOnly); index++ {
		c := hostOnly[index]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '.' && c != '-' && (c != '_' || index == 0) {
			return false
		}
	}
	if !hasPort {
		return true
	}
	if len(port) == 0 || len(port) > 5 {
		return false
	}
	for index := 0; index < len(port); index++ {
		if port[index] < '0' || port[index] > '9' {
			return false
		}
	}
	number, err := strconv.ParseUint(port, 10, 16)
	return err == nil && number > 0
}

func validContainerPathComponent(component string) bool {
	if component == "" || component == "." || component == ".." {
		return false
	}
	for index := 0; index < len(component); index++ {
		c := component[index]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '.' && c != '-' && c != '_' {
			return false
		}
	}
	return true
}

// ValidContainerCommand enforces the bounded argv grammar: at least one and
// at most MaxCommandItems arguments, each non-empty, bounded, valid UTF-8 and
// control-character free. The engine is always invoked via argv, so shell
// metacharacters are inert; a manifest "command string" cannot pass the
// schema's array type in the first place.
func ValidContainerCommand(command []string) bool {
	if len(command) == 0 || len(command) > MaxCommandItems {
		return false
	}
	for _, argument := range command {
		if argument == "" || utf8.RuneCountInString(argument) > MaxCommandArgRunes {
			return false
		}
		if !utf8.ValidString(argument) {
			return false
		}
		for _, r := range argument {
			if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
				return false
			}
		}
	}
	return true
}

// ValidContainerResourcePolicy enforces the manifest-side policy shape: every
// hard limit is finite and within the canonical bounds, and memory high never
// exceeds memory max.
func ValidContainerResourcePolicy(policy ContainerResourcePolicy) bool {
	if policy.CPUHardCores < MinCPUHardCores || policy.CPUHardCores > MaxCPUHardCores ||
		math.IsNaN(policy.CPUHardCores) || math.IsInf(policy.CPUHardCores, 0) {
		return false
	}
	if policy.MemoryHighMB < MinMemoryHighMB || policy.MemoryHighMB > MaxMemoryHighMB {
		return false
	}
	if policy.MemoryMaxMB < MinMemoryMaxMB || policy.MemoryMaxMB > MaxMemoryMaxMB {
		return false
	}
	if policy.MemoryHighMB > policy.MemoryMaxMB {
		return false
	}
	return policy.PidsMax >= MinPidsMax && policy.PidsMax <= MaxPidsMax
}

// ValidContainerHealthPolicy enforces the manifest-side health shape.
func ValidContainerHealthPolicy(policy ContainerHealthPolicy) bool {
	if len(policy.HTTPPath) == 0 || len(policy.HTTPPath) > 120 || policy.HTTPPath[0] != '/' {
		return false
	}
	for index := 0; index < len(policy.HTTPPath); index++ {
		c := policy.HTTPPath[index]
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') &&
			c != '/' && c != '.' && c != '_' && c != '-' {
			return false
		}
	}
	if policy.StartupSeconds < MinStartupSeconds || policy.StartupSeconds > MaxStartupSeconds {
		return false
	}
	return policy.RestartLimit >= MinRestartLimit && policy.RestartLimit <= MaxRestartLimit
}
