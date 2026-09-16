// Package domain defines the app-bundle.v1 release bundle format and the
// artifact entity (ADR-0033). The codec is the security boundary between
// untrusted bundle bytes and the Runtime-owned repository: every path, type,
// and size rule is enforced identically on encode and decode.
package bundleformat

import (
	"errors"
	"fmt"
	"path"
	"strings"
)

const (
	// BundleFormat is the only release bundle format this Runtime accepts.
	BundleFormat = "app-bundle.v1"

	// Limits (ADR-0033 section 1, fail closed).
	MaxBundleFiles        = 1024
	MaxFileBytes          = int64(32 << 20)
	MaxTotalContentBytes  = int64(128 << 20)
	MaxPathBytes          = 128
	MaxEncodedBundleBytes = int64(132 << 20)

	blockSize  = 512
	terminator = 2 // zero blocks ending the stream
)

var (
	ErrBundleInvalid  = errors.New("artifactstore: invalid bundle")
	ErrBundleTooLarge = errors.New("artifactstore: bundle exceeds limits")
	ErrDigestMismatch = errors.New("artifactstore: bundle digest mismatch")
)

// Stats summarizes one verified bundle stream.
type Stats struct {
	Digest      string
	FileCount   int
	TotalBytes  int64
	EncodedSize int64
}

// ValidBundlePath enforces the canonical relative path grammar: forward
// slashes only, no absolute paths, no "." or ".." or empty segments, no
// trailing/duplicate separators, bounded length.
func ValidBundlePath(p string) error {
	if p == "" {
		return fmt.Errorf("%w: empty path", ErrBundleInvalid)
	}
	if len(p) > MaxPathBytes {
		return fmt.Errorf("%w: path %q exceeds %d bytes", ErrBundleInvalid, p, MaxPathBytes)
	}
	if strings.HasPrefix(p, "/") {
		return fmt.Errorf("%w: absolute path %q", ErrBundleInvalid, p)
	}
	if strings.ContainsRune(p, 0) || strings.Contains(p, `\`) {
		return fmt.Errorf("%w: NUL byte in path %q", ErrBundleInvalid, p)
	}
	if p != path.Clean(p) {
		return fmt.Errorf("%w: non-canonical path %q", ErrBundleInvalid, p)
	}
	for _, segment := range strings.Split(p, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return fmt.Errorf("%w: illegal segment in path %q", ErrBundleInvalid, p)
		}
	}
	return nil
}
