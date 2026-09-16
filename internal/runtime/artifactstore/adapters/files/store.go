// Package files is the runtime-owned on-disk bundle repository (ADR-0033
// section 1): 0700 private root, per-owner directories, content-addressed
// files, atomic durable promotion, controlled descriptor access only.
package files

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/yangtao121/workos/internal/platform/appbundle"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

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
	if err := privateDir(absolute); err != nil {
		return nil, err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil || resolved != absolute {
		return nil, errors.New("artifactstore: symlink root is forbidden")
	}
	return &Store{root: absolute}, nil
}

func (s *Store) ownerDir(owner string) (string, error) {
	if !domain.ValidUUID(owner) {
		return "", fmt.Errorf("%w: owner %q is not a UUIDv7", domain.ErrInvalidRequest, owner)
	}
	dir := filepath.Join(s.root, owner)
	if err := privateDir(dir); err != nil {
		return "", err
	}
	return dir, nil
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

func (f *stagingFile) Seek(offset int64, whence int) (int64, error) {
	return f.file.Seek(offset, whence)
}

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
	if err := privateDir(tmpDir); err != nil {
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
	// Link is an atomic no-replace promotion. Existing content must verify;
	// neither a corrupt file nor a symlink is proof of a duplicate.
	if err := os.Link(staging.Name(), target); err != nil {
		if errors.Is(err, os.ErrExist) {
			if _, _, verifyErr := s.OpenVerified(owner, digest); verifyErr != nil {
				return verifyErr
			}
			staging.Discard()
			return nil
		}
		return err
	}
	staging.Discard()
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
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", 0, fmt.Errorf("%w: bundle bytes missing for %s", domain.ErrUnavailable, digest)
		}
		return "", 0, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != fileMode {
		return "", 0, domain.ErrUnavailable
	}
	stats, verifyErr := appbundle.Verify(file, digest)
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
		if !domain.ValidUUID(owner.Name()) {
			continue
		}
		unlock, err := s.LockOwner(context.Background(), owner.Name())
		if err != nil {
			return err
		}
		err = func() error {
			defer unlock()
			tmpDir := filepath.Join(s.root, owner.Name(), "tmp")
			entries, readErr := os.ReadDir(tmpDir)
			if readErr != nil {
				if errors.Is(readErr, os.ErrNotExist) {
					return nil
				}
				return readErr
			}
			for _, entry := range entries {
				if err := os.RemoveAll(filepath.Join(tmpDir, entry.Name())); err != nil {
					return err
				}
			}
			return nil
		}()
		if err != nil {
			return err
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

// privateDir rejects links, foreign ownership and writable ancestor components
// inside the runtime repository. The root itself is checked when opening it.
func privateDir(dir string) error {
	if err := os.Mkdir(dir, rootDirMode); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || info.Mode().Perm() != rootDirMode || !ok || stat.Uid != uint32(os.Geteuid()) {
		return errors.New("artifactstore: repository directory must be private and owned by runtime")
	}
	return nil
}

// LockOwner serializes imports, commits and recovery across runtime processes.
func (s *Store) LockOwner(ctx context.Context, owner string) (func(), error) {
	dir, err := s.ownerDir(owner)
	if err != nil {
		return nil, err
	}
	file, err := os.OpenFile(filepath.Join(dir, ".lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, fileMode)
	if err != nil {
		return nil, err
	}
	for {
		if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err == nil {
			return func() { _ = unix.Flock(int(file.Fd()), unix.LOCK_UN); _ = file.Close() }, nil
		} else if !errors.Is(err, unix.EWOULDBLOCK) {
			file.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			file.Close()
			return nil, ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// UsageBytes includes staging files and orphaned durable files after a DB error.
func (s *Store) UsageBytes(owner string) (int64, error) {
	dir, err := s.ownerDir(owner)
	if err != nil {
		return 0, err
	}
	var total int64
	err = filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return domain.ErrUnavailable
		}
		if !entry.IsDir() {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return domain.ErrUnavailable
			}
			total += info.Size()
		}
		return nil
	})
	return total, err
}
