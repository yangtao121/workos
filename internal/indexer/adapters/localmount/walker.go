// Package localmount reads owner-bound directories into bounded index facts.
// Every path is resolved beneath an open root without following symlinks.
package localmount

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"sort"

	domain "github.com/yangtao121/workos/internal/indexer/domain"
	"github.com/yangtao121/workos/internal/indexer/ports"
)

type Walker struct{}

func NewWalker() *Walker { return &Walker{} }

func (w *Walker) ValidateRoot(root string) error {
	file, err := openRoot(root)
	if err != nil {
		return err
	}
	return file.Close()
}

// Walk returns facts only after a complete pass. Errors must never be used
// as an authoritative absent-file list for projection tombstones.
func (w *Walker) Walk(ctx context.Context, root string) (ports.MountResult, error) {
	if err := ctx.Err(); err != nil {
		return ports.MountResult{}, err
	}
	file, err := openRoot(root)
	if err != nil {
		if errors.Is(err, domain.ErrWorkspaceSymlinkEscape) {
			err = failure(domain.DegradedMountUnsafe)
		}
		return ports.MountResult{}, err
	}
	defer file.Close()
	scan := scan{ctx: ctx, root: file}
	if err := scan.directory(file, ""); err != nil {
		return ports.MountResult{}, err
	}
	sort.Slice(scan.result.Files, func(i, j int) bool { return scan.result.Files[i].RelPath < scan.result.Files[j].RelPath })
	sort.Slice(scan.result.Skips, func(i, j int) bool {
		a, b := scan.result.Skips[i], scan.result.Skips[j]
		if a.Reason != b.Reason {
			return a.Reason < b.Reason
		}
		return a.RelPath < b.RelPath
	})
	return scan.result, nil
}

type scan struct {
	ctx            context.Context
	root           *os.File
	result         ports.MountResult
	entries, bytes int
}

func (s *scan) skip(rel, reason string) {
	s.result.Skips = append(s.result.Skips, ports.MountSkip{RelPath: rel, Reason: reason})
}
func failure(reason string) error { return &domain.WorkspaceFailure{Reason: reason} }

func (s *scan) directory(dir *os.File, prefix string) error {
	entries, err := dir.ReadDir(domain.WorkspaceMaxEntries - s.entries + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return failure(domain.DegradedReadFailed)
	}
	s.entries += len(entries)
	if s.entries > domain.WorkspaceMaxEntries {
		return failure(domain.DegradedScanLimit)
	}
	for _, entry := range entries {
		if err := s.ctx.Err(); err != nil {
			return err
		}
		rel := entry.Name()
		if prefix != "" {
			rel = prefix + "/" + rel
		}
		title, err := domain.WorkspaceRelPath(rel)
		if err != nil {
			// A pruned directory makes the pass incomplete; do not delete its old facts.
			if entry.IsDir() {
				return failure(domain.DegradedScanLimit)
			}
			s.skip("", domain.SkipInvalid)
			continue
		}
		if domain.WorkspaceIgnoredName(entry.Name()) {
			s.skip(rel, domain.SkipIgnored)
			continue
		}
		if entry.Type()&os.ModeSymlink != 0 {
			s.skip(rel, domain.SkipSymlink)
			continue
		}
		if entry.IsDir() {
			child, err := openDirectory(s.root, rel)
			if err != nil {
				return failure(domain.DegradedReadFailed)
			}
			err = s.directory(child, rel)
			_ = child.Close()
			if err != nil {
				return err
			}
			continue
		}
		if !entry.Type().IsRegular() {
			s.skip(rel, domain.SkipInvalid)
			continue
		}
		if !domain.WorkspaceExtension(rel) {
			s.skip(rel, domain.SkipExtension)
			continue
		}
		if err := s.read(rel, title); err != nil {
			return err
		}
	}
	return nil
}

func (s *scan) read(rel, title string) error {
	file, err := openFile(s.root, rel)
	if err != nil {
		return failure(domain.DegradedReadFailed)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return failure(domain.DegradedReadFailed)
	}
	if !info.Mode().IsRegular() {
		return failure(domain.DegradedReadFailed)
	}
	if info.Size() > domain.WorkspaceMaxFileBytes {
		s.skip(rel, domain.SkipOversize)
		return nil
	}
	content, err := io.ReadAll(io.LimitReader(file, domain.WorkspaceMaxFileBytes+1))
	if err != nil {
		return failure(domain.DegradedReadFailed)
	}
	s.bytes += len(content)
	if s.bytes > domain.WorkspaceMaxTotalBytes {
		return failure(domain.DegradedScanLimit)
	}
	if len(content) > domain.WorkspaceMaxFileBytes {
		s.skip(rel, domain.SkipOversize)
		return nil
	}
	if domain.LooksBinary(content) {
		s.skip(rel, domain.SkipBinary)
		return nil
	}
	if len(s.result.Files) >= domain.WorkspaceMaxFiles {
		return failure(domain.DegradedScanLimit)
	}
	sum := sha256.Sum256(content)
	s.result.Files = append(s.result.Files, ports.MountFile{
		RelPath: rel, Title: title, Content: content, Digest: "sha256:" + hex.EncodeToString(sum[:]),
	})
	return nil
}
