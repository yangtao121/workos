package domain

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	MaxSourceFiles      = 128
	MaxSourceFileBytes  = 256 * 1024
	MaxSourceTotalBytes = 512 * 1024
)

var ErrSourceCorrupt = errors.New("stored app source bundle is inconsistent")

type SourceFile struct {
	Path       string `json:"path"`
	Content    []byte `json:"content"`
	Executable bool   `json:"executable"`
}

type SourceBundle struct {
	ID, OwnerUserID, IdempotencyKey, Digest string
	Files                                   []SourceFile
	TotalSizeBytes                          int64
	CreatedAt                               time.Time
}

func ValidSourceID(id string) bool {
	return ValidUUID(id) && id == strings.ToLower(id) && id[14] == '7' && strings.ContainsRune("89ab", rune(id[19]))
}

// NormalizeSource owns a copy of the file bytes. Order has no semantic effect;
// exact path spelling, contents and the executable bit do affect identity.
func NormalizeSource(files []SourceFile) ([]SourceFile, string, int64, error) {
	if len(files) == 0 || len(files) > MaxSourceFiles {
		return nil, "", 0, ErrInvalid
	}
	normalized := make([]SourceFile, 0, len(files))
	paths := make(map[string]bool, len(files))
	var total int64
	for _, file := range files {
		if !validSourcePath(file.Path) || paths[file.Path] || len(file.Content) > MaxSourceFileBytes {
			return nil, "", 0, ErrInvalid
		}
		total += int64(len(file.Content))
		if total > MaxSourceTotalBytes {
			return nil, "", 0, ErrInvalid
		}
		paths[file.Path] = true
		normalized = append(normalized, SourceFile{Path: file.Path, Content: append([]byte{}, file.Content...), Executable: file.Executable})
	}
	for name := range paths {
		for parent := name; strings.Contains(parent, "/"); {
			parent = parent[:strings.LastIndexByte(parent, '/')]
			if paths[parent] {
				return nil, "", 0, ErrInvalid
			}
		}
	}
	sort.Slice(normalized, func(i, j int) bool { return normalized[i].Path < normalized[j].Path })
	canonical, err := json.Marshal(normalized)
	if err != nil {
		return nil, "", 0, ErrInvalid
	}
	sum := sha256.Sum256(append([]byte("workos.app-source/v1\n"), canonical...))
	return normalized, "sha256:" + hex.EncodeToString(sum[:]), total, nil
}

func validSourcePath(value string) bool {
	if value == "" || len(value) > 240 || !utf8.ValidString(value) || strings.ContainsAny(value, "\\:") || strings.ContainsFunc(value, unicode.IsControl) {
		return false
	}
	parts := strings.Split(value, "/")
	if len(parts) > 16 {
		return false
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || strings.EqualFold(part, ".git") {
			return false
		}
	}
	return true
}

func ValidateSourceBundle(bundle SourceBundle) error {
	_, offset := bundle.CreatedAt.Zone()
	if offset != 0 {
		return ErrSourceCorrupt
	}
	if !ValidSourceID(bundle.ID) || !ValidSourceID(bundle.OwnerUserID) || !ValidIdempotencyKey(bundle.IdempotencyKey) || bundle.CreatedAt.IsZero() || bundle.CreatedAt.Year() < 1 || bundle.CreatedAt.Year() > 9999 || !bundle.CreatedAt.Equal(bundle.CreatedAt.Truncate(time.Microsecond)) {
		return ErrSourceCorrupt
	}
	normalized, digest, total, err := NormalizeSource(bundle.Files)
	if err != nil || bundle.Digest != digest || bundle.TotalSizeBytes != total {
		return ErrSourceCorrupt
	}
	actual, _ := json.Marshal(bundle.Files)
	canonical, _ := json.Marshal(normalized)
	if !bytes.Equal(actual, canonical) {
		return ErrSourceCorrupt
	}
	return nil
}
