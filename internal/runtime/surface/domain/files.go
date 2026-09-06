package domain

import (
	"errors"
	"strings"
	"unicode/utf8"
)

const MaxFileBytes = 32 * 1024
const FilePageSize = 20
const MaxDirectoryEntries = 1000

var ErrFileConflict = errors.New("file changed since it was read")
var ErrFileLimit = errors.New("workspace file limit exceeded")

type FileRef struct{ ProjectID, Path, ETag string }
type FileEntry struct {
	Ref       FileRef
	Directory bool
	Size      int64
}
type FilePage struct {
	Entries   []FileEntry
	NextAfter string
}

func ValidFilePath(value string, directory bool) bool {
	if value == "" {
		return directory
	}
	if !utf8.ValidString(value) || len(value) > 1024 || strings.ContainsAny(value, "\\:") {
		return false
	}
	parts := strings.Split(value, "/")
	if len(parts) > 8 {
		return false
	}
	for _, part := range parts {
		if part == "" || strings.HasPrefix(part, ".") || len(part) > 255 {
			return false
		}
		for _, r := range part {
			if r < 32 || (r >= 127 && r <= 159) {
				return false
			}
		}
	}
	return true
}
func ValidFileETag(value string, create bool) bool {
	if value == "" {
		return create
	}
	if len(value) != 71 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for _, r := range value[7:] {
		if !(r >= '0' && r <= '9') && !(r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}
func WorkspaceCapabilities(granted []string, writable bool) []string {
	var capabilities []string
	if BridgeCapabilityGranted(granted, "files.read") {
		capabilities = append(capabilities, "files.pick", "files.read")
	}
	if writable && BridgeCapabilityGranted(granted, "files.write") {
		capabilities = append(capabilities, "files.write")
	}
	return capabilities
}
