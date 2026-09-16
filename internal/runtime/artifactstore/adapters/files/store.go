// Package files is the runtime-owned on-disk bundle repository (ADR-0033
// section 1): 0700 private root, per-owner directories, content-addressed
// files, atomic durable promotion, controlled descriptor access only.
package files

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/yangtao121/workos/internal/runtime/artifactstore/domain"
	"github.com/yangtao121/workos/internal/runtime/artifactstore/ports"
)

const (
	rootDirMode = 0o700
	fileMode    = 0o600
)

// Store implements ports.BundleFiles.
type Store struct {
	root string
}

// New validates and prepares the repository root. It is created with 0700;
// an existing root must already be 0700 and owned by the current user.
func New(root string) (*Store, error) {
	if root == "" {
		return nil, errors.New("artifactstore: empty root")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(absolute)
	switch {
	case err == nil:
		if !info.IsDir() {
			return nil, fmt.Errorf("artifactstore: root %s is not a directory", absolute)
		}
		if info.Mode().Perm() != rootDirMode {
			return nil, fmt.Errorf("artifactstore: root %s must have mode 0700, has %o", absolute, info.Mode().Perm())
		}
	case errors.Is(err, os.ErrNotExist):
		if err := os.MkdirAll(absolute, rootDirMode); err != nil {
			return nil, err
		}
		// MkdirAll applies umask; enforce the exact mode.
		if err := os.Chmod(absolute, rootDirMode); err != nil {
			return nil, err
		}
	default:
		return nil, err
	}
	return &Store{root: absolute}, nil
}

func (s *Store) ownerDir(owner string) (string, error) {
	if !domain.ValidUUID(owner) {
		return "", fmt.Errorf("%w: owner %q is not a UUIDv7", domain.ErrInvalidRequest, owner)
	}
	return filepath.Join(s.root, owner), nil
}

func (s *Store) contentPath(owner, digest string) (string, error) {
	if !domain.ValidDigest(digest) {
		return "", fmt.Errorf("%w: digest %q malformed", domain.ErrInvalidRequest, digest)
	}
	dir, err := s.ownerDir(owner)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, strings.TrimPrefix(digest, "sha256:")+".bundle"), nil
}

type stagingFile struct {
	file *os.File
}

func (f *stagingFile) Write(p []byte) (int, error) { return f.file.Write(p) }

func (f *stagingFile) Seek(offset int64, whence int) (int64, error) { return f.file.Seek(offset, whence) }

func (f *stagingFile) Name() string { return f.file.Name() }

func (f *stagingFile) Finish() (int64, error) {
	if err := f.file.Sync(); err != nil {
		_ = f.file.Close()
		return 0, err
	}
	info, err := f.file.Stat()
	if err != nil {
		_ = f.file.Close()
		return 0, err
	}
	if err := f.file.Close(); err != nil {
		return 0, err
	}
	return info.Size(), nil
}

func (f *stagingFile) Discard() {
	_ = f.file.Close()
	_ = os.Remove(f.file.Name())
}

func (s *Store) TempFile(owner string) (ports.StagingFile, error) {
	dir, err := s.ownerDir(owner)
	if err != nil {
		return nil, err
	}
	tmpDir := filepath.Join(dir, "tmp")
	if err := os.MkdirAll(tmpDir, rootDirMode); err != nil {
		return nil, err
	}
	if err := os.Chmod(tmpDir, rootDirMode); err != nil {
		return nil, err
	}
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(filepath.Join(tmpDir, hex.EncodeToString(suffix[:])+".part"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, fileMode)
	if err != nil {
		return nil, err
	}
	return &stagingFile{file: file}, nil
}

func (s *Store) PromoteFile(owner, digest string, staging ports.StagingFile) error {
	target, err := s.contentPath(owner, digest)
	if err != nil {
		staging.Discard()
		return err
	}
	if _, statErr := os.Lstat(target); statErr == nil {
		// Content-addressed duplicate: the bytes on disk already are this
		// digest's canonical content; drop the staging copy.
		staging.Discard()
		return nil
	}
	if err := os.Rename(staging.Name(), target); err != nil {
		staging.Discard()
		return err
	}
	if err := fsyncDir(filepath.Dir(target)); err != nil {
		return err
	}
	if err := os.Chmod(target, fileMode); err != nil {
		return err
	}
	return nil
}

func (s *Store) OpenVerified(owner, digest string) (string, int64, error) {
	path, err := s.contentPath(owner, digest)
	if err != nil {
		return "", 0, err
	}
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", 0, fmt.Errorf("%w: bundle bytes missing for %s", domain.ErrUnavailable, digest)
		}
		return "", 0, err
	}
	defer func() { _ = file.Close() }()
	stats, verifyErr := domain.Verify(file, digest)
	if verifyErr != nil {
		return "", 0, verifyErr
	}
	return path, stats.EncodedSize, nil
}

func (s *Store) Has(owner, digest string) bool {
	path, err := s.contentPath(owner, digest)
	if err != nil {
		return false
	}
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular()
}

func (s *Store) CleanTemp() error {
	owners, err := os.ReadDir(s.root)
	if err != nil {
		return err
	}
	for _, owner := range owners {
		if !owner.IsDir() {
			continue
		}
		tmpDir := filepath.Join(s.root, owner.Name(), "tmp")
		entries, readErr := os.ReadDir(tmpDir)
		if readErr != nil {
			if errors.Is(readErr, os.ErrNotExist) {
				continue
			}
			return readErr
		}
		for _, entry := range entries {
			if err := os.RemoveAll(filepath.Join(tmpDir, entry.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

func fsyncDir(dir string) error {
	handle, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer func() { _ = handle.Close() }()
	return handle.Sync()
}

// Root exposes the absolute root for wiring and diagnostics.
func (s *Store) Root() string { return s.root }

// Enforce interface compliance.
var _ ports.BundleFiles = (*Store)(nil)

// ensure io.Writer interface usage
var _ io.Writer = (*stagingFile)(nil)
