// Package workspace implements the Linux-only, explicitly bound project
// filesystem. Every pathname is resolved by openat2, never check-then-open.
package workspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/yangtao121/workos/internal/runtime/surface/domain"
	"github.com/yangtao121/workos/internal/runtime/surface/ports"
	"golang.org/x/sys/unix"
)

type binding struct {
	root     *os.File
	writable bool
	gate     chan struct{}
}
type Store struct{ mounts map[ports.FileScope]*binding }

func New(mounts []Mount) (*Store, error) {
	s := &Store{mounts: make(map[ports.FileScope]*binding)}
	fail := func() (*Store, error) { s.Close(); return nil, domain.ErrUnavailable }
	if len(mounts) > 32 {
		return fail()
	}
	for i, m := range mounts {
		if !domain.ValidSessionUUID(m.OwnerUserID) || !domain.ValidSessionUUID(m.ProjectID) || !filepath.IsAbs(m.Path) || filepath.Clean(m.Path) != m.Path || m.Path == "/" {
			return fail()
		}
		for _, other := range mounts[:i] {
			if m.Path == other.Path || strings.HasPrefix(m.Path, other.Path+"/") || strings.HasPrefix(other.Path, m.Path+"/") {
				return fail()
			}
		}
		scope := ports.FileScope{OwnerUserID: m.OwnerUserID, ProjectID: m.ProjectID}
		if s.mounts[scope] != nil {
			return fail()
		}
		fd, err := unix.Openat2(unix.AT_FDCWD, m.Path, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_NO_SYMLINKS})
		if err != nil {
			return fail()
		}
		root := os.NewFile(uintptr(fd), "workspace")
		var stat unix.Stat_t
		if unix.Fstat(fd, &stat) != nil || stat.Uid != uint32(os.Geteuid()) || stat.Mode&0022 != 0 {
			root.Close()
			return fail()
		}
		s.mounts[scope] = &binding{root: root, writable: !m.ReadOnly, gate: make(chan struct{}, 1)}
	}
	return s, nil
}
func (s *Store) Close() {
	for _, b := range s.mounts {
		_ = b.root.Close()
	}
}
func (s *Store) Access(scope ports.FileScope) (bool, bool) {
	b := s.mounts[scope]
	return b != nil, b != nil && b.writable
}

func (s *Store) acquire(ctx context.Context, scope ports.FileScope) (*binding, func(), error) {
	b := s.mounts[scope]
	if ctx.Err() != nil {
		return nil, nil, domain.ErrUnavailable
	}
	if b == nil {
		return nil, nil, domain.ErrUnavailable
	}
	lockCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	select {
	case b.gate <- struct{}{}:
	case <-lockCtx.Done():
		cancel()
		return nil, nil, domain.ErrUnavailable
	}
	release := func() { _ = unix.Flock(int(b.root.Fd()), unix.LOCK_UN); <-b.gate; cancel() }
	for {
		err := unix.Flock(int(b.root.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return b, release, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) {
			release()
			return nil, nil, domain.ErrUnavailable
		}
		select {
		case <-lockCtx.Done():
			release()
			return nil, nil, domain.ErrUnavailable
		case <-time.After(10 * time.Millisecond):
		}
	}
}
func open(root *os.File, name string, flags uint64) (*os.File, error) {
	if name == "" {
		name = "."
	}
	fd, err := unix.Openat2(int(root.Fd()), name, &unix.OpenHow{Flags: flags | unix.O_CLOEXEC | unix.O_NONBLOCK, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_XDEV})
	if err != nil {
		return nil, mapError(err)
	}
	return os.NewFile(uintptr(fd), "workspace file"), nil
}
func mapError(err error) error {
	switch {
	case errors.Is(err, unix.ENOENT):
		return domain.ErrNotFound
	case errors.Is(err, unix.ELOOP), errors.Is(err, unix.EXDEV), errors.Is(err, unix.ENOTDIR), errors.Is(err, unix.EACCES):
		return domain.ErrPermissionDenied
	default:
		return domain.ErrUnavailable
	}
}
func digest(data []byte) string {
	hash := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(hash[:])
}
func read(root *os.File, name string) ([]byte, error) {
	file, err := open(root, name, unix.O_RDONLY)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil {
		return nil, domain.ErrUnavailable
	}
	if !stat.Mode().IsRegular() {
		return nil, domain.ErrPermissionDenied
	}
	if stat.Size() > domain.MaxFileBytes {
		return nil, domain.ErrFileLimit
	}
	data, err := io.ReadAll(io.LimitReader(file, domain.MaxFileBytes+1))
	if err != nil {
		return nil, domain.ErrUnavailable
	}
	if len(data) > domain.MaxFileBytes {
		return nil, domain.ErrFileLimit
	}
	return data, nil
}
func (s *Store) Read(ctx context.Context, scope ports.FileScope, ref domain.FileRef) ([]byte, error) {
	if ref.ProjectID != scope.ProjectID || !domain.ValidFilePath(ref.Path, false) || !domain.ValidFileETag(ref.ETag, false) {
		return nil, domain.ErrInvalid
	}
	b, release, err := s.acquire(ctx, scope)
	if err != nil {
		return nil, err
	}
	defer release()
	data, err := read(b.root, ref.Path)
	if err != nil {
		return nil, err
	}
	if digest(data) != ref.ETag {
		return nil, domain.ErrFileConflict
	}
	return data, nil
}
func (s *Store) List(ctx context.Context, scope ports.FileScope, directory, after string) (domain.FilePage, error) {
	if !domain.ValidFilePath(directory, true) || !domain.ValidFilePath(after, true) || strings.Contains(after, "/") {
		return domain.FilePage{}, domain.ErrInvalid
	}
	b, release, err := s.acquire(ctx, scope)
	if err != nil {
		return domain.FilePage{}, err
	}
	defer release()
	dir, err := open(b.root, directory, unix.O_RDONLY|unix.O_DIRECTORY)
	if err != nil {
		return domain.FilePage{}, err
	}
	defer dir.Close()
	entries, err := dir.ReadDir(domain.MaxDirectoryEntries + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return domain.FilePage{}, domain.ErrUnavailable
	}
	if len(entries) > domain.MaxDirectoryEntries {
		return domain.FilePage{}, domain.ErrFileLimit
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	page := domain.FilePage{}
	for _, entry := range entries {
		if ctx.Err() != nil {
			return domain.FilePage{}, domain.ErrUnavailable
		}
		name := entry.Name()
		logical := path.Join(directory, name)
		if name <= after || !domain.ValidFilePath(logical, false) || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		ref := domain.FileRef{ProjectID: scope.ProjectID, Path: logical}
		var size int64
		if !entry.IsDir() {
			data, readErr := read(dir, name)
			if errors.Is(readErr, domain.ErrFileLimit) || errors.Is(readErr, domain.ErrPermissionDenied) || errors.Is(readErr, domain.ErrNotFound) {
				continue
			}
			if readErr != nil {
				return domain.FilePage{}, readErr
			}
			size = int64(len(data))
			ref.ETag = digest(data)
		}
		if len(page.Entries) == domain.FilePageSize {
			page.NextAfter = path.Base(page.Entries[len(page.Entries)-1].Ref.Path)
			break
		}
		page.Entries = append(page.Entries, domain.FileEntry{Ref: ref, Directory: entry.IsDir(), Size: size})
	}
	return page, nil
}
func (s *Store) Write(ctx context.Context, scope ports.FileScope, ref domain.FileRef, data []byte) (domain.FileRef, error) {
	if ref.ProjectID != scope.ProjectID || !domain.ValidFilePath(ref.Path, false) || !domain.ValidFileETag(ref.ETag, true) {
		return domain.FileRef{}, domain.ErrInvalid
	}
	if len(data) > domain.MaxFileBytes {
		return domain.FileRef{}, domain.ErrFileLimit
	}
	b, release, err := s.acquire(ctx, scope)
	if err != nil {
		return domain.FileRef{}, err
	}
	defer release()
	if !b.writable {
		return domain.FileRef{}, domain.ErrPermissionDenied
	}
	parent, name := path.Split(ref.Path)
	parent = strings.TrimSuffix(parent, "/")
	dir, err := open(b.root, parent, unix.O_RDONLY|unix.O_DIRECTORY)
	if err != nil {
		return domain.FileRef{}, err
	}
	defer dir.Close()

	entries, listErr := dir.ReadDir(domain.MaxDirectoryEntries + 2)
	if listErr != nil && !errors.Is(listErr, io.EOF) {
		return domain.FileRef{}, domain.ErrUnavailable
	}
	count := len(entries)
	// These names are reserved for this adapter; App paths cannot start with a dot.
	for _, entry := range entries {
		id, ok := strings.CutPrefix(entry.Name(), ".workos-write-")
		if ok && domain.ValidSessionUUID(id) {
			var stat unix.Stat_t
			if unix.Fstatat(int(dir.Fd()), entry.Name(), &stat, unix.AT_SYMLINK_NOFOLLOW) == nil && stat.Uid == uint32(os.Geteuid()) && stat.Mode&unix.S_IFMT == unix.S_IFREG {
				if unix.Unlinkat(int(dir.Fd()), entry.Name(), 0) != nil {
					return domain.FileRef{}, domain.ErrUnavailable
				}
				count--
			}
		}
	}
	old, err := read(dir, name)
	if errors.Is(err, domain.ErrNotFound) {
		if count >= domain.MaxDirectoryEntries {
			return domain.FileRef{}, domain.ErrFileLimit
		}
		if ref.ETag != "" {
			return domain.FileRef{}, domain.ErrFileConflict
		}
	} else if err != nil {
		return domain.FileRef{}, err
	} else if digest(old) != ref.ETag {
		return domain.FileRef{}, domain.ErrFileConflict
	}
	mode := os.FileMode(0600)
	if err == nil {
		var stat unix.Stat_t
		if unix.Fstatat(int(dir.Fd()), name, &stat, unix.AT_SYMLINK_NOFOLLOW) != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG {
			return domain.FileRef{}, domain.ErrPermissionDenied
		}
		mode = os.FileMode(stat.Mode & 0777)
	}
	id, err := uuid.NewV7()
	if err != nil {
		return domain.FileRef{}, domain.ErrUnavailable
	}
	temp := ".workos-write-" + id.String()
	fd, err := unix.Openat(int(dir.Fd()), temp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return domain.FileRef{}, domain.ErrUnavailable
	}
	file := os.NewFile(uintptr(fd), "workspace write")
	defer file.Close()
	defer unix.Unlinkat(int(dir.Fd()), temp, 0)
	if _, err = file.Write(data); err != nil {
		return domain.FileRef{}, domain.ErrUnavailable
	}
	if file.Chmod(mode) != nil || file.Sync() != nil || ctx.Err() != nil {
		return domain.FileRef{}, domain.ErrUnavailable
	}
	if unix.Renameat(int(dir.Fd()), temp, int(dir.Fd()), name) != nil {
		return domain.FileRef{}, domain.ErrUnavailable
	}
	if dir.Sync() != nil {
		return domain.FileRef{}, domain.ErrUnavailable
	}
	ref.ETag = digest(data)
	return ref, nil
}
