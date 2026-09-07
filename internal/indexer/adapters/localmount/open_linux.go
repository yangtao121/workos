package localmount

import (
	"errors"
	"os"

	domain "github.com/yangtao121/workos/internal/indexer/domain"
	"golang.org/x/sys/unix"
)

func openRoot(root string) (*os.File, error) {
	if !domain.ValidWorkspaceRoot(root) {
		return nil, domain.ErrWorkspaceInvalidPath
	}
	fd, err := unix.Openat2(unix.AT_FDCWD, root, &unix.OpenHow{
		Flags:   unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC,
		Resolve: unix.RESOLVE_NO_SYMLINKS,
	})
	if err == nil {
		return os.NewFile(uintptr(fd), "workspace root"), nil
	}
	switch {
	case errors.Is(err, unix.ELOOP):
		return nil, domain.ErrWorkspaceSymlinkEscape
	case errors.Is(err, unix.ENOENT):
		return nil, failure(domain.DegradedMountMissing)
	case errors.Is(err, unix.ENOTDIR):
		return nil, failure(domain.DegradedMountNotDir)
	case errors.Is(err, unix.ENOSYS), errors.Is(err, unix.EINVAL):
		return nil, failure(domain.DegradedUnavailable)
	default:
		return nil, failure(domain.DegradedReadFailed)
	}
}
func openDirectory(root *os.File, rel string) (*os.File, error) {
	return open(root, rel, unix.O_RDONLY|unix.O_DIRECTORY)
}
func openFile(root *os.File, rel string) (*os.File, error) { return open(root, rel, unix.O_RDONLY) }
func open(root *os.File, rel string, flags uint64) (*os.File, error) {
	fd, err := unix.Openat2(int(root.Fd()), rel, &unix.OpenHow{
		Flags:   flags | unix.O_CLOEXEC | unix.O_NONBLOCK,
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_XDEV,
	})
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), "workspace entry"), nil
}
