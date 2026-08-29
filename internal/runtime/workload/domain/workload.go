// Package domain holds the Workload Manager's invariants: the durable
// supervised workload fact, its server-owned effective policy, the state
// machine, and the request-boundary grammar. Domain never imports database,
// Connect, HTTP, engine SDKs, or any other module's packages.
package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

var (
	// ErrInvalid marks a request that violates the workload grammar.
	ErrInvalid = errors.New("workload request is invalid")
	// ErrNotFound marks an unknown or foreign workload.
	ErrNotFound = errors.New("workload is not available")
	// ErrIdempotencyConflict marks an operation key reused for a different
	// canonical command.
	ErrIdempotencyConflict = errors.New("workload operation key was used for a different command")
	// ErrUnsupported marks a launch request the runtime cannot execute under
	// its enforced profile (e.g. an unsupported restart target state).
	ErrUnsupported = errors.New("workload operation is not supported")
	// ErrUnavailable marks a temporarily unreachable store or engine.
	ErrUnavailable = errors.New("workload manager is temporarily unavailable")
	// ErrRunnerUnavailable marks a host whose verified rootless container
	// capability is missing. It is never degraded into a fallback: the
	// runtime refuses to run containers instead of using an unsafe engine
	// (ADR-0006 §3).
	ErrRunnerUnavailable = errors.New("verified rootless container capability is unavailable")
	// ErrImageMissing marks a launch whose exact digest-pinned image is not
	// present locally. The engine is never asked to pull.
	ErrImageMissing = errors.New("pinned image is not available locally")
	// ErrRestartLimitExhausted marks a restart refused because the persisted
	// effective restart limit is spent.
	ErrRestartLimitExhausted = errors.New("workload restart limit is exhausted")
	// ErrCorrupt marks a stored-fact invariant violation (endpoint/cgroup/
	// policy drift). It is sanitized Internal at transport, never surfaced
	// with detail.
	ErrCorrupt = errors.New("workload stored facts are inconsistent")
)

// States is the durable lifecycle state machine. pending is the reserved
// pre-engine fact; starting covers create/start/persist; running is the
// verified steady state; stopping/stopped are the ordered shutdown facts;
// failed marks a startup or runtime failure that no automatic actor retries.
type State string

const (
	StatePending  State = "pending"
	StateStarting State = "starting"
	StateRunning  State = "running"
	StateStopping State = "stopping"
	StateStopped  State = "stopped"
	StateFailed   State = "failed"
)

// Terminal reports whether the state closes the workload's active lifetime.
func (s State) Terminal() bool {
	return s == StateStopped || s == StateFailed
}

// Operation identifies the durable command kinds.
type Operation string

const (
	OperationEnsure    Operation = "ensure"
	OperationRestart   Operation = "restart"
	OperationTerminate Operation = "terminate"
)

// ErrorKind is the sanitized, fixed classification persisted with a failed
// operation result so replays reproduce the same verdict without content.
type ErrorKind string

const (
	ErrorNone           ErrorKind = ""
	ErrorInvalid        ErrorKind = "invalid"
	ErrorUnsupported    ErrorKind = "unsupported"
	ErrorConflict       ErrorKind = "conflict"
	ErrorLimitExhausted ErrorKind = "limit_exhausted"
	ErrorUnavailable    ErrorKind = "unavailable"
	ErrorFailed         ErrorKind = "failed"
)

// HealthVerdict is the bounded health grammar carried in observations.
const (
	HealthUnknown = "unknown"
	HealthOK      = "ok"
	HealthFailing = "failing"
)

// ExitCategory is the bounded exit classification carried in observations.
const (
	ExitNone    = "none"
	ExitExited  = "exited"
	ExitOOM     = "oom"
	ExitPIDs    = "pids"
	ExitUnknown = "unknown"
)

// PolicyVersion is the versioned server-owned maximum set below (ADR-0006
// §2). A future policy change ships a new version and a migration story; the
// column pins which maxima produced the persisted effective policy.
const PolicyVersion = "v1"

// Server maxima: the finite upper bounds of the effective policy. A request
// is clamped to min(request, max); a request can never obtain more host
// resources than these constants allow.
const (
	MaxCPUHardCores     = 4.0
	MaxMemoryHighMB     = int64(1024)
	MaxMemoryMaxMB      = int64(2048)
	MaxPidsMax          = int64(512)
	MaxStartupSeconds   = int64(120)
	MaxRestartLimit     = int64(8)
	MinMemoryHighMB     = int64(16)
	MinMemoryMaxMB      = int64(32)
	MinPidsMax          = int64(8)
	MinStartupSeconds   = int64(1)
	cgroupCPUPeriodUSec = int64(100000)
	maxCommandItems     = 16
	maxCommandArgRunes  = 4096
	maxWorkloadKeyRunes = 128
)

// RequestedPolicy is the App's requested policy as resolved from the pinned
// manifest (via the Core descriptor). It is a request, never an
// authorization.
type RequestedPolicy struct {
	CPUHardCores   float64
	MemoryHighMB   int64
	MemoryMaxMB    int64
	PidsMax        int64
	HTTPPath       string
	StartupSeconds int64
	RestartLimit   int64
}

// ValidRequested rejects requests outside the versioned canonical envelope.
// The registry already enforces the same bounds; the runtime re-checks
// because it never trusts another process's validation (fail closed on
// garbage instead of clamping unknown shapes).
func (r RequestedPolicy) Valid() bool {
	if r.CPUHardCores <= 0 || r.CPUHardCores > MaxCPUHardCores ||
		math.IsNaN(r.CPUHardCores) || math.IsInf(r.CPUHardCores, 0) {
		return false
	}
	if r.MemoryHighMB < MinMemoryHighMB || r.MemoryHighMB > MaxMemoryHighMB {
		return false
	}
	if r.MemoryMaxMB < MinMemoryMaxMB || r.MemoryMaxMB > MaxMemoryMaxMB {
		return false
	}
	if r.MemoryHighMB > r.MemoryMaxMB {
		return false
	}
	if r.PidsMax < MinPidsMax || r.PidsMax > MaxPidsMax {
		return false
	}
	if len(r.HTTPPath) == 0 || len(r.HTTPPath) > 120 || r.HTTPPath[0] != '/' {
		return false
	}
	for index := 0; index < len(r.HTTPPath); index++ {
		c := r.HTTPPath[index]
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') &&
			c != '/' && c != '.' && c != '_' && c != '-' {
			return false
		}
	}
	if r.StartupSeconds < MinStartupSeconds || r.StartupSeconds > MaxStartupSeconds {
		return false
	}
	return r.RestartLimit >= 0 && r.RestartLimit <= MaxRestartLimit
}

// EffectivePolicy is the server-adjudicated execution policy: every hard
// limit is finite, within the versioned maxima, and expressed in the exact
// units the engine and cgroup enforcement consume.
type EffectivePolicy struct {
	CPUQuotaUSec    int64
	MemoryHighBytes int64
	MemoryMaxBytes  int64
	PidsMax         int64
	StartupTimeout  time.Duration
	RestartLimit    int64
	HealthPath      string
}

// EffectiveFromRequested clamps the request into the effective policy. The
// CPU hard quota is expressed as quota microseconds over the fixed 100ms
// cgroup period; memory values are megabyte-granular by construction.
func EffectiveFromRequested(request RequestedPolicy) EffectivePolicy {
	cores := math.Min(request.CPUHardCores, MaxCPUHardCores)
	quota := int64(math.Round(cores * float64(cgroupCPUPeriodUSec)))
	if quota < 1 {
		quota = 1
	}
	highMB := minInt64(request.MemoryHighMB, MaxMemoryHighMB)
	maxMB := minInt64(request.MemoryMaxMB, MaxMemoryMaxMB)
	if maxMB < highMB {
		maxMB = highMB
	}
	return EffectivePolicy{
		CPUQuotaUSec:    quota,
		MemoryHighBytes: highMB * 1024 * 1024,
		MemoryMaxBytes:  maxMB * 1024 * 1024,
		PidsMax:         minInt64(request.PidsMax, MaxPidsMax),
		StartupTimeout:  time.Duration(minInt64(request.StartupSeconds, MaxStartupSeconds)) * time.Second,
		RestartLimit:    minInt64(request.RestartLimit, MaxRestartLimit),
		HealthPath:      request.HTTPPath,
	}
}

func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

// Workload is the durable supervised workload fact. ContainerID, Endpoint,
// and CgroupPath are host facts persisted after engine verification; they are
// never projected to public callers and never accepted from them.
type Workload struct {
	ID             string
	OwnerUserID    string
	ProjectID      string
	AppInstanceID  string
	AppID          string
	AppVersion     string
	ManifestDigest string
	Image          string
	Command        []string
	Port           int64
	Requested      RequestedPolicy
	Effective      EffectivePolicy
	Generation     int64
	State          State
	RestartCount   int64
	ContainerID    string
	ContainerName  string
	Endpoint       string
	CgroupPath     string
	HealthVerdict  string
	LastExit       string
	BaselineOOM    uint64
	BaselinePids   uint64
	LastVerifiedAt *time.Time
	LeaseOwner     string
	LeaseExpiresAt *time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
	StartedAt      *time.Time
	StoppedAt      *time.Time
}

// Active reports whether the workload holds its active slot.
func (w Workload) Active() bool { return !w.State.Terminal() }

// WorkloadOperation is the persisted result of one durable command.
type WorkloadOperation struct {
	WorkloadID       string
	OperationKey     string
	Operation        Operation
	RequestDigest    string
	ResultState      State
	ResultGeneration int64
	ErrorKind        ErrorKind
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// Completed reports whether the operation recorded a final verdict.
func (o WorkloadOperation) Completed() bool {
	return o.ResultState != "" || o.ErrorKind != ""
}

// Retryable reports whether a completed-with-transient-failure operation may
// be re-driven under the same key (failures never consume the key).
func (o WorkloadOperation) Retryable() bool {
	return o.ErrorKind == ErrorFailed || o.ErrorKind == ErrorUnavailable
}

// ContainerName derives the deterministic engine container name for one
// workload. The name is a pure function of the workload ID: concurrent or
// crash-recovered engine operations converge on the same object instead of
// creating duplicates.
func ContainerName(workloadID string) string {
	return "workos-wl-" + workloadID
}

// EngineLabels are the WorkOS ownership labels every container this runtime
// creates carries. Recovery adopts exactly the containers whose labels match
// the persisted row; unlabeled or foreign containers are never touched.
func EngineLabels(workload Workload) map[string]string {
	return map[string]string{
		"workos.managed":             "workos",
		"workos.workload.id":         workload.ID,
		"workos.workload.generation": strconv.FormatInt(workload.Generation, 10),
		"workos.owner":               workload.OwnerUserID,
		"workos.workload.instance":   workload.AppInstanceID,
	}
}

// OperationDigest derives the canonical command digest of one operation:
// same key + same canonical command replays; any difference is a stable
// conflict. The digest covers every input that changes what the command does.
func OperationDigest(operation Operation, workloadID, image string, command []string, port int64, policy RequestedPolicy) string {
	canonical := fmt.Sprintf("workos.workload-operation.v1|%s|%s|%s|%s|%d|%.6f|%d|%d|%d|%s|%d|%d",
		operation, workloadID, image, strings.Join(command, "\x1f"), port,
		policy.CPUHardCores, policy.MemoryHighMB, policy.MemoryMaxMB, policy.PidsMax,
		policy.HTTPPath, policy.StartupSeconds, policy.RestartLimit)
	sum := sha256.Sum256([]byte(canonical))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// ValidWorkloadID enforces the canonical UUIDv7 grammar for workload IDs.
func ValidWorkloadID(value string) bool {
	return validUUIDv7(value)
}

// ValidUUIDv7 is the shared canonical identifier grammar for this module.
func ValidUUIDv7(value string) bool { return validUUIDv7(value) }

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

// ValidOperationKey enforces the durable operation key grammar: valid UTF-8,
// no control characters, bounded length.
func ValidOperationKey(value string) bool {
	count := 0
	for _, r := range value {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			return false
		}
		count++
		if count > maxWorkloadKeyRunes {
			return false
		}
	}
	return count > 0
}

// ValidCommand enforces the bounded argv grammar at the runtime boundary.
func ValidCommand(command []string) bool {
	if len(command) == 0 || len(command) > maxCommandItems {
		return false
	}
	for _, argument := range command {
		if argument == "" || len(argument) > maxCommandArgRunes {
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

// ValidLoopbackEndpoint accepts exactly the server-derived endpoint shape the
// runtime persists: a loopback IPv4 address, a bounded port, nothing else.
// Anything else — host names, other IPs, paths, schemes — is corrupt.
func ValidLoopbackEndpoint(value string) bool {
	host, port, ok := strings.Cut(value, ":")
	if !ok || host != "127.0.0.1" {
		return false
	}
	number, err := strconv.Atoi(port)
	if err != nil || number < 1 || number > 65535 {
		return false
	}
	return true
}

// ValidCgroupPath validates an engine-reported cgroup v2 path against the
// runtime's own delegated subtree root. The path must stay inside the
// subtree, contain no traversal, and never be the subtree root itself (the
// runtime's own cgroup). Engine-reported paths that escape — including via
// symlink-style tricks or empty segments — are corrupt, never adopted.
func ValidCgroupPath(path, subtreeRoot string) bool {
	if path == "" || subtreeRoot == "" {
		return false
	}
	if !strings.HasSuffix(subtreeRoot, "/") {
		subtreeRoot += "/"
	}
	if path == strings.TrimSuffix(subtreeRoot, "/") {
		return false
	}
	if !strings.HasPrefix(path, subtreeRoot) {
		return false
	}
	rest := strings.TrimPrefix(path, subtreeRoot)
	for _, segment := range strings.Split(rest, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
		for index := 0; index < len(segment); index++ {
			c := segment[index]
			if c < 0x20 || c == 0x7f {
				return false
			}
		}
	}
	return true
}

// ValidImage enforces the exact digest-pinned reference grammar at the
// runtime boundary. It mirrors the registry-side rule as defense in depth:
// the runtime never trusts another process's validation, and a descriptor
// that fails this grammar is corrupt, never clamped or defaulted. Exactly one
// "@sha256:" separator with a 64-character lowercase hex digest; a lowercase
// repository with an optional numeric registry port; never a tag, never
// credentials.
func ValidImage(value string) bool {
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
		return false
	}
	host, path, _ := strings.Cut(reference, "/")
	if host == "" || strings.Contains(path, ":") {
		return false
	}
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
	return true
}

// ValidDescriptor enforces the full launch descriptor grammar the runtime
// accepts from the Core resolution. Anything outside it — including command
// shapes the engine could misread — is invalid input, never executed.
func ValidDescriptor(appID, appVersion, manifestDigest, image string, command []string, port int64, requested RequestedPolicy) bool {
	if !validAppID(appID) || len(appVersion) == 0 || len(appVersion) > 32 ||
		len(manifestDigest) != len("sha256:")+64 ||
		manifestDigest[:len("sha256:")] != "sha256:" {
		return false
	}
	for _, c := range []byte(manifestDigest[len("sha256:"):]) {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	if port < 1 || port > 65535 {
		return false
	}
	return ValidImage(image) && ValidCommand(command) && requested.Valid()
}

func validAppID(value string) bool {
	if len(value) < 3 || len(value) > 63 || value[0] < 'a' || value[0] > 'z' {
		return false
	}
	for index := 0; index < len(value); index++ {
		c := value[index]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return false
		}
	}
	return true
}

// ValidTerminateReason enforces the fixed reason vocabulary observations and
// operation records use.
func ValidTerminateReason(value string) bool {
	switch value {
	case "policy", "restart_limit", "uninstalled", "idle", "fail_safe":
		return true
	default:
		return false
	}
}

// ErrFailed marks a deterministic launch or runtime failure of the workload
// itself (not of the manager's infrastructure): the engine object exited,
// or the bounded startup window closed without a passing probe. Retryable by
// a fresh ensure key or a supervisor restart, never silently re-driven.
var ErrFailed = errors.New("workload launch failed")
