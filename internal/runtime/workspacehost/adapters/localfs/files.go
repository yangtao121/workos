package localfs

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/yangtao121/workos/internal/platform/ids"
	"github.com/yangtao121/workos/internal/runtime/workspacehost/domain"
)

const maxBytes = 256 * 1024

type Files struct{ mu sync.Mutex }

func failure(code string) domain.Result { return domain.Result{"error": code} }
func version(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}
func str(a map[string]any, k string) string { s, _ := a[k].(string); return s }
func safePath(s string) (string, error) {
	if strings.ContainsRune(s, 0) || len(s) > 4096 {
		return "", domain.ErrInvalid
	}
	if strings.HasPrefix(s, "/workspace/") {
		s = strings.TrimPrefix(s, "/workspace/")
	} else if s == "/workspace" {
		s = "."
	} else if path.IsAbs(s) {
		return "", domain.ErrDenied
	}
	for _, part := range strings.Split(s, "/") {
		if part == ".." {
			return "", domain.ErrDenied
		}
	}
	s = path.Clean(s)
	return s, nil
}
func read(root *os.Root, name string) ([]byte, error) {
	f, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, domain.ErrDenied
	}
	data, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxBytes {
		return nil, domain.ErrInvalid
	}
	return data, nil
}
func metadata(root *os.Root, name string) (domain.Result, error) {
	info, err := root.Lstat(name)
	if os.IsNotExist(err) {
		return domain.Result{"absent": true}, nil
	}
	if err != nil {
		return nil, err
	}
	typ := "other"
	v := fmt.Sprintf("%d:%d:%d", info.Mode(), info.Size(), info.ModTime().UnixNano())
	if info.IsDir() {
		typ = "directory"
	} else if info.Mode().IsRegular() {
		typ = "file"
		if info.Size() <= maxBytes {
			data, err := read(root, name)
			if err != nil {
				return nil, err
			}
			v = version(data)
		}
	} else if info.Mode()&os.ModeSymlink != 0 {
		typ = "symlink"
	}
	return domain.Result{"type": typ, "size": info.Size(), "version": v}, nil
}
func (f *Files) Execute(ctx context.Context, rootPath string, readOnly bool, op domain.Operation) (domain.Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, domain.ErrUnavailable
	}
	defer root.Close()
	name, err := safePath(str(op.Arguments, "path"))
	if err != nil {
		return nil, err
	}
	// Deny symlinks, including internal aliases, so target/version identity is
	// stable. os.Root independently prevents a concurrent path-swap escape.
	parts := strings.Split(name, "/")
	for i := range parts {
		info, e := root.Lstat(strings.Join(parts[:i+1], "/"))
		if e == nil && info.Mode()&os.ModeSymlink != 0 {
			return failure("FS_PERMISSION_DENIED"), nil
		}
		if e != nil && !os.IsNotExist(e) {
			return failure("FS_IO_ERROR"), nil
		}
	}
	target := func(p string) map[string]any {
		display := "/workspace"
		if p != "." {
			display += "/" + p
		}
		return map[string]any{"targetKey": display, "displayPath": display}
	}
	switch op.Name {
	case "fs.resolve":
		return domain.Result(target(name)), nil
	case "fs.stat":
		return metadata(root, name)
	case "fs.list":
		dir, err := root.Open(name)
		if err != nil {
			return failure("FS_NOT_FOUND"), nil
		}
		defer dir.Close()
		entries, err := dir.ReadDir(1025)
		if err != nil && !errors.Is(err, io.EOF) {
			return failure("FS_NOT_DIRECTORY"), nil
		}
		if len(entries) > 1024 {
			return failure("FS_TOO_LARGE"), nil
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		result := make([]any, 0, len(entries))
		for _, entry := range entries {
			child := path.Join(name, entry.Name())
			info, err := metadata(root, child)
			if err != nil {
				return nil, err
			}
			info["name"] = entry.Name()
			info["target"] = target(child)
			if info["type"] == "symlink" {
				info["type"] = "other"
			}
			result = append(result, map[string]any(info))
		}
		return domain.Result{"entries": result}, nil
	case "fs.read":
		data, err := read(root, name)
		if os.IsNotExist(err) {
			return failure("FS_NOT_FOUND"), nil
		}
		if err != nil {
			return failure("FS_TOO_LARGE"), nil
		}
		if str(op.Arguments, "encoding") == "base64" {
			return domain.Result{"content": base64.StdEncoding.EncodeToString(data), "version": version(data)}, nil
		}
		if !utf8.Valid(data) || strings.ContainsRune(string(data), 0) {
			return failure("FS_NOT_TEXT"), nil
		}
		return domain.Result{"content": string(data), "version": version(data)}, nil
	case "fs.write", "fs.edit":
		if readOnly {
			return failure("FS_PERMISSION_DENIED"), nil
		}
		before, err := read(root, name)
		exists := err == nil
		if err != nil && !os.IsNotExist(err) {
			return failure("FS_NOT_TEXT"), nil
		}
		if exists && (!utf8.Valid(before) || strings.ContainsRune(string(before), 0)) {
			return failure("FS_NOT_TEXT"), nil
		}
		guard := str(op.Arguments, "guard")
		expected := str(op.Arguments, "version")
		if guard == "createIfAbsent" && exists {
			return failure("FS_NOT_OBSERVED"), nil
		}
		if guard == "replaceIfVersion" && (!exists || version(before) != expected) {
			return failure("FS_STALE_VERSION"), nil
		}
		if guard != "" && guard != "createIfAbsent" && guard != "replaceIfVersion" {
			return nil, domain.ErrInvalid
		}
		content := str(op.Arguments, "content")
		result := domain.Result{"before": nil, "operation": "create"}
		if exists {
			result["before"] = string(before)
			result["operation"] = "update"
		}
		if op.Name == "fs.edit" {
			if !exists || (expected != "" && version(before) != expected) {
				return failure("FS_STALE_VERSION"), nil
			}
			old, newText := str(op.Arguments, "oldString"), str(op.Arguments, "newString")
			if old == "" {
				return nil, domain.ErrInvalid
			}
			n := strings.Count(string(before), old)
			if n == 0 {
				return failure("FS_EDIT_NOT_FOUND"), nil
			}
			all, _ := op.Arguments["replaceAll"].(bool)
			if n > 1 && !all {
				return failure("FS_AMBIGUOUS_EDIT"), nil
			}
			content = strings.ReplaceAll(string(before), old, newText)
		}
		if len(content) > maxBytes || !utf8.ValidString(content) || strings.ContainsRune(content, 0) {
			return failure("FS_TOO_LARGE"), nil
		}
		temporary := path.Join(path.Dir(name), ".workos-write-"+(ids.UUIDv7{}).New())
		mode := os.FileMode(0600)
		if exists {
			info, statErr := root.Stat(name)
			if statErr != nil {
				return failure("FS_IO_ERROR"), nil
			}
			mode = info.Mode().Perm()
		}
		file, err := root.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
		if err != nil {
			return failure("FS_IO_ERROR"), nil
		}
		defer root.Remove(temporary)
		err = file.Chmod(mode)
		if err == nil {
			_, err = file.WriteString(content)
		}
		if err == nil {
			err = file.Sync()
		}
		closeErr := file.Close()
		if err != nil || closeErr != nil {
			return failure("FS_IO_ERROR"), nil
		}
		if guard == "createIfAbsent" {
			err = root.Link(temporary, name)
		} else {
			err = root.Rename(temporary, name)
		}
		if os.IsExist(err) {
			return failure("FS_NOT_OBSERVED"), nil
		}
		if err != nil {
			return failure("FS_IO_ERROR"), nil
		}
		result["after"] = content
		result["version"] = version([]byte(content))
		return result, nil
	default:
		return nil, domain.ErrInvalid
	}
}
