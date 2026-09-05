// Package localmount walks owner-bound local directories for the workspace
// knowledge slice (ADR-0017 §4). It is the only filesystem-touching piece:
// the application sees bounded facts (files, sanitized skip reasons) and the
// domain owns the grammar. Symlinks are never followed — a symlinked file or
// directory is a recorded skip, so a hostile mount cannot escape its root.
package localmount

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	indexerdomain "github.com/yangtao121/workos/internal/indexer/domain"
	"github.com/yangtao121/workos/internal/indexer/ports"
)

// Walker reads real mount roots into port facts.
type Walker struct{}

// NewWalker builds the local mount reader.
func NewWalker() *Walker { return &Walker{} }

// Walk reads the mount root into bounded file facts. A missing or non-dir
// root is a mount-level failure mapped to the fixed degraded categories; the
// caller records it on the source instead of silently keeping stale facts.
func (w *Walker) Walk(root string) (ports.MountResult, error) {
	info, err := os.Lstat(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return ports.MountResult{}, indexerdomain.ErrWorkspaceDegraded
		}
		return ports.MountResult{}, indexerdomain.ErrWorkspaceDegraded
	}
	if !info.IsDir() {
		return ports.MountResult{}, indexerdomain.ErrWorkspaceDegraded
	}
	result := ports.MountResult{}
	var walkErr error
	limitHit := false
	err = filepath.WalkDir(root, func(current string, entry fs.DirEntry, err error) error {
		if err != nil {
			walkErr = indexerdomain.ErrWorkspaceDegraded
			return err
		}
		rel, relErr := filepath.Rel(root, current)
		if relErr != nil {
			walkErr = indexerdomain.ErrWorkspaceDegraded
			return relErr
		}
		if rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		// Grammar and ignore verdicts apply to directories as well, so an
		// ignored tree is pruned rather than descended.
		base := filepath.Base(current)
		if indexerdomain.WorkspaceIgnoredName(base) {
			// One sanitized skip per ignored root entry; ignored trees are
			// pruned, never descended.
			result.Skips = append(result.Skips, ports.MountSkip{RelPath: rel, Reason: indexerdomain.SkipIgnored})
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			result.Skips = append(result.Skips, ports.MountSkip{RelPath: rel, Reason: indexerdomain.SkipSymlink})
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			result.Skips = append(result.Skips, ports.MountSkip{RelPath: rel, Reason: indexerdomain.SkipInvalid})
			return nil
		}
		if len(result.Files) >= indexerdomain.WorkspaceMaxFiles {
			limitHit = true
			return filepath.SkipAll
		}
		title, titleErr := indexerdomain.WorkspaceRelPath(rel)
		if titleErr != nil {
			result.Skips = append(result.Skips, ports.MountSkip{RelPath: rel, Reason: indexerdomain.SkipInvalid})
			return nil
		}
		if !indexerdomain.WorkspaceExtension(rel) {
			result.Skips = append(result.Skips, ports.MountSkip{RelPath: rel, Reason: indexerdomain.SkipExtension})
			return nil
		}
		content, readErr := os.ReadFile(current)
		if readErr != nil {
			result.Skips = append(result.Skips, ports.MountSkip{RelPath: rel, Reason: indexerdomain.SkipInvalid})
			return nil
		}
		if len(content) > indexerdomain.WorkspaceMaxFileBytes {
			result.Skips = append(result.Skips, ports.MountSkip{RelPath: rel, Reason: indexerdomain.SkipOversize})
			return nil
		}
		if indexerdomain.LooksBinary(content) {
			result.Skips = append(result.Skips, ports.MountSkip{RelPath: rel, Reason: indexerdomain.SkipBinary})
			return nil
		}
		result.Files = append(result.Files, ports.MountFile{
			RelPath: rel, Title: title, Content: content,
			Digest: digestBytes(content),
		})
		return nil
	})
	if err != nil || walkErr != nil {
		return ports.MountResult{}, indexerdomain.ErrWorkspaceDegraded
	}
	if limitHit {
		result.Skips = append(result.Skips, ports.MountSkip{RelPath: "", Reason: indexerdomain.SkipLimit})
	}
	sort.Slice(result.Files, func(i, j int) bool { return result.Files[i].RelPath < result.Files[j].RelPath })
	sort.Slice(result.Skips, func(i, j int) bool {
		if result.Skips[i].Reason != result.Skips[j].Reason {
			return result.Skips[i].Reason < result.Skips[j].Reason
		}
		return result.Skips[i].RelPath < result.Skips[j].RelPath
	})
	return result, nil
}

func digestBytes(content []byte) string {
	sum := sha256.Sum256(content)
	return "sha256:" + hex.EncodeToString(sum[:])
}
