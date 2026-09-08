// Package domain holds the Build/Test job facts (ADR-0026). A job is the
// durable, idempotent unit that turns one immutable repair candidate into a
// verified build/test verdict; nothing here is ever a Go map.
package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

type State string

const (
	StateQueued    State = "queued"
	StateRunning   State = "running"
	StateSucceeded State = "succeeded"
	StateFailed    State = "failed"
	StateCancelled State = "cancelled"
)

func (s State) Terminal() bool {
	return s == StateSucceeded || s == StateFailed || s == StateCancelled
}

type Stage string

const (
	StageSubmit      Stage = "submit"
	StageMaterialize Stage = "materialize"
	StageBuild       Stage = "build"
	StageTest        Stage = "test"
	StageVerify      Stage = "verify"
)

type FailureReason string

const (
	FailureBuildFailed  FailureReason = "build-failed"
	FailureTestFailed   FailureReason = "test-failed"
	FailureTimeout      FailureReason = "timeout"
	FailureEngineFailed FailureReason = "engine-failed"
	FailureOutputBudget FailureReason = "output-budget"
	FailureInputDrift   FailureReason = "input-drift"
	FailureCancelled    FailureReason = "cancelled"
	FailureNone         FailureReason = ""
)

// ADR-0024 file bounds, enforced again at the executor boundary.
const (
	MaxFiles          = 128
	MaxFileBytes      = 256 * 1024
	MaxTotalBytes     = 512 * 1024
	MaxPathRunes      = 200
	MaxLogTailBytes   = 64 * 1024
	MaxLogLineRunes   = 4096
	maxCommandItems   = 16
	maxCommandRunes   = 4096
	maxBaseImageRunes = 256
)

var (
	ErrInvalid          = errors.New("build job is invalid")
	ErrNotFound         = errors.New("build job not found")
	ErrIdempotencyDrift = errors.New("build job replay input drifted")
	ErrStoreUnavailable = errors.New("build job store is temporarily unavailable")
)

type File struct {
	Path       string `json:"path"`
	Content    []byte `json:"content"`
	Executable bool   `json:"executable"`
}

// Payload is the exact immutable execution contract pinned at submit: the
// pinned base image, the fixed build/test argv and the candidate files. The
// canonical JSON digest binds replays to one input.
type Payload struct {
	BaseImage string   `json:"base_image"`
	BuildCmd  []string `json:"build_command"`
	TestCmd   []string `json:"test_command"`
	Files     []File   `json:"files"`
}

type Job struct {
	ID             string
	TaskID         string
	IncidentID     string
	OwnerUserID    string
	ProjectID      string
	InstallationID string
	InputDigest    string
	SourceBundleID string
	SourceDigest   string
	ManifestDigest string
	BaseImage      string
	Payload        Payload
	State          State
	Stage          Stage
	BuildExitCode  *int32
	TestExitCode   *int32
	FailureReason  FailureReason
	EngineFacts    json.RawMessage
	Attempts       int32
	LogTail        string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// ValidUUIDv7 matches the canonical resource id grammar (lowercase).
func ValidUUIDv7(value string) bool {
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
	return value[14] == '7' && (value[19] == '8' || value[19] == '9' || value[19] == 'a' || value[19] == 'b')
}

func ValidDigest(value string) bool {
	if len(value) != 71 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for _, c := range value[7:] {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// ValidateJob checks the durable job facts before any persistence.
func ValidateJob(job Job) error {
	for _, id := range []string{job.ID, job.TaskID, job.IncidentID, job.OwnerUserID, job.ProjectID, job.InstallationID, job.SourceBundleID} {
		if !ValidUUIDv7(id) {
			return ErrInvalid
		}
	}
	if !ValidDigest(job.InputDigest) || !ValidDigest(job.SourceDigest) || !ValidDigest(job.ManifestDigest) {
		return ErrInvalid
	}
	if err := ValidatePayload(job.Payload); err != nil {
		return err
	}
	if job.BaseImage == "" || utf8.RuneCountInString(job.BaseImage) > maxBaseImageRunes {
		return ErrInvalid
	}
	return nil
}

func ValidatePayload(payload Payload) error {
	if utf8.RuneCountInString(payload.BaseImage) == 0 || utf8.RuneCountInString(payload.BaseImage) > maxBaseImageRunes {
		return ErrInvalid
	}
	if err := validateCommand(payload.BuildCmd); err != nil {
		return err
	}
	if err := validateCommand(payload.TestCmd); err != nil {
		return err
	}
	return ValidateFiles(payload.Files)
}

func validateCommand(command []string) error {
	if len(command) == 0 || len(command) > maxCommandItems {
		return ErrInvalid
	}
	for _, argument := range command {
		if argument == "" || utf8.RuneCountInString(argument) > maxCommandRunes || !utf8.ValidString(argument) {
			return ErrInvalid
		}
		for _, r := range argument {
			if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
				return ErrInvalid
			}
		}
	}
	return nil
}

// ValidateFiles re-enforces the ADR-0024 candidate grammar: regular relative
// paths inside one tree, no links, bounded count and size.
func ValidateFiles(files []File) error {
	if len(files) == 0 || len(files) > MaxFiles {
		return ErrInvalid
	}
	total := 0
	seen := make(map[string]struct{}, len(files))
	for _, file := range files {
		if _, duplicate := seen[file.Path]; duplicate {
			return ErrInvalid
		}
		seen[file.Path] = struct{}{}
		if !ValidFilePath(file.Path) {
			return ErrInvalid
		}
		if len(file.Content) > MaxFileBytes || !utf8.Valid(file.Content) {
			return ErrInvalid
		}
		total += len(file.Content)
	}
	if total > MaxTotalBytes {
		return ErrInvalid
	}
	return nil
}

func ValidFilePath(value string) bool {
	if value == "" || utf8.RuneCountInString(value) > MaxPathRunes {
		return false
	}
	if path.IsAbs(value) || value != path.Clean(value) {
		return false
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
		for _, r := range segment {
			if r < 0x20 || r == 0x7f {
				return false
			}
		}
	}
	return true
}

// CanonicalPayload encodes the payload deterministically (sorted file order,
// sorted keys, no whitespace) and derives the input digest.
func CanonicalPayload(payload Payload) ([]byte, string, error) {
	ordered := make([]File, len(payload.Files))
	copy(ordered, payload.Files)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Path < ordered[j].Path })
	payload.Files = ordered
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, "", fmt.Errorf("encode build payload: %w", err)
	}
	// json.Marshal on the struct already emits sorted-by-declaration keys with
	// no whitespace; files are content-addressed by the sorted order above.
	sum := sha256.Sum256(encoded)
	return encoded, "sha256:" + hex.EncodeToString(sum[:]), nil
}

// SanitizeLogTail bounds and sanitizes captured output: at most 64 KiB of
// valid UTF-8, control characters folded, lines capped.
func SanitizeLogTail(output []byte) string {
	if len(output) > MaxLogTailBytes {
		output = output[len(output)-MaxLogTailBytes:]
	}
	lines := strings.Split(strings.ToValidUTF8(string(output), "�"), "\n")
	for index, line := range lines {
		runes := []rune(line)
		if len(runes) > MaxLogLineRunes {
			lines[index] = string(runes[:MaxLogLineRunes]) + "…"
		}
	}
	return strings.Join(lines, "\n")
}
