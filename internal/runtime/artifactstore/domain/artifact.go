package domain

import (
	"errors"
	"regexp"
	"time"
)

// Artifact states (ADR-0033 section 6). Only ready bundles may back a
// version or a launch; unavailable is durable metadata whose bytes are gone.
const (
	StatePreparing   = "preparing"
	StateReady       = "ready"
	StateFailed      = "failed"
	StateUnavailable = "unavailable"
)

// Origins.
const (
	OriginBuildJob       = "build_job"
	OriginOperatorImport = "operator_import"
)

var (
	ErrNotFound        = errors.New("artifactstore: artifact not found")
	ErrConflict        = errors.New("artifactstore: artifact conflict")
	ErrUnavailable     = errors.New("artifactstore: artifact unavailable")
	ErrQuotaExceeded   = errors.New("artifactstore: owner quota exceeded")
	ErrStoreUnavailable = errors.New("artifactstore: store unavailable")
	ErrInvalidRequest  = errors.New("artifactstore: invalid request")
)

var (
	uuidPattern       = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	digestPattern     = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	appIDPattern      = regexp.MustCompile(`^[a-z][a-z0-9-]{2,62}$`)
)

// ValidUUID reports whether id is a UUIDv7 in canonical lowercase form.
func ValidUUID(id string) bool { return uuidPattern.MatchString(id) }

// ValidDigest reports whether d is a sha256 content digest.
func ValidDigest(d string) bool { return digestPattern.MatchString(d) }

// ValidAppID reports whether id matches the registry app id grammar.
func ValidAppID(id string) bool { return appIDPattern.MatchString(id) }

// Artifact is the durable metadata row for one release bundle.
type Artifact struct {
	ID               string
	OwnerUserID      string
	Digest           string
	Format           string
	SizeBytes        int64
	FileCount        int32
	State            string
	Origin           string
	IdempotencyKey   string
	AppID            string
	TaskID           string
	JobID            string
	IncidentID       string
	ProjectID        string
	InstallationID   string
	SourceBundleID   string
	SourceDigest     string
	ManifestDigest   string
	BaseImage        string
	BuildCommand     []string
	TestCommand      []string
	OutputDirectory  string
	CreatedAt        time.Time
	ReadyAt          *time.Time
	UpdatedAt        time.Time
}

// OwnerQuotaBytes is the per-owner cap over ready plus preparing bundles
// (ADR-0033 section 1). Fail closed: at the cap, new imports and builds are
// refused; ready bundles are never auto-deleted.
const OwnerQuotaBytes = int64(2 << 30)
