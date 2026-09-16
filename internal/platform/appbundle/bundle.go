// Package domain defines the app-bundle.v1 release bundle format and the
// artifact entity (ADR-0033). The codec is the security boundary between
// untrusted bundle bytes and the Runtime-owned repository: every path, type,
// and size rule is enforced identically on encode and decode.
package appbundle

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	format "github.com/yangtao121/workos/internal/platform/bundleformat"
	"hash"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const blockSize = 512
const terminator = 2

// splitUSTAR splits a validated path into ustar name/prefix fields. Both
// bounds must hold: name = p[i+1:] fits in 100 bytes, prefix = p[:i] in 155.
func splitUSTAR(p string) (name, prefix string, err error) {
	if len(p) <= 100 {
		return p, "", nil
	}
	lower := len(p) - 101
	if lower < 0 {
		lower = 0
	}
	upper := 155
	if upper > len(p)-2 {
		upper = len(p) - 2
	}
	for i := lower; i <= upper; i++ {
		if p[i] == '/' {
			return p[i+1:], p[:i], nil
		}
	}
	return "", "", fmt.Errorf("%w: path %q cannot be split into ustar fields", format.ErrBundleInvalid, p)
}

func octal(field []byte, value int64) {
	s := fmt.Sprintf("%0*o", len(field)-1, value)
	copy(field, s)
	field[len(field)-1] = 0
}

func ustarHeader(entryPath string, isDir, executable bool, size int64) ([]byte, error) {
	header := make([]byte, blockSize)
	name, prefix, err := splitUSTAR(entryPath)
	if err != nil {
		return nil, err
	}
	copy(header[0:], name)
	mode := int64(0o644)
	if isDir || executable {
		mode = 0o755
	}
	octal(header[100:108], mode)
	octal(header[108:116], 0) // uid
	octal(header[116:124], 0) // gid
	if !isDir {
		octal(header[124:136], size)
	} else {
		octal(header[124:136], 0)
	}
	octal(header[136:148], 0) // mtime fixed to epoch
	for i := 148; i < 156; i++ {
		header[i] = ' '
	}
	if isDir {
		header[156] = '5'
	} else {
		header[156] = '0'
	}
	copy(header[257:], "ustar\x00")
	copy(header[263:], "00")
	copy(header[345:], prefix)
	sum := 0
	for _, b := range header {
		sum += int(b)
	}
	checksum := fmt.Sprintf("%06o", sum)
	copy(header[148:], checksum)
	header[154] = 0
	header[155] = ' '
	return header, nil
}

// EncodeDirectory walks a real output directory and writes the deterministic
// app-bundle.v1 stream to w. Symbolic links, special files, unreadable
// entries, absolute or escaping paths, and every size/count limit are
// rejected during the walk — never after the fact.
func EncodeDirectory(dir string, w io.Writer) (format.Stats, error) {
	var paths []string
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == dir {
			return nil
		}
		info, statErr := d.Info()
		if statErr != nil {
			return statErr
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: symbolic link %q in build output", format.ErrBundleInvalid, p)
		}
		if !info.Mode().IsRegular() && !d.IsDir() {
			return fmt.Errorf("%w: special file %q in build output", format.ErrBundleInvalid, p)
		}
		paths = append(paths, p)
		return nil
	})
	if err != nil {
		return format.Stats{}, err
	}
	sort.Slice(paths, func(i, j int) bool { return bundleSortKey(dir, paths[i]) < bundleSortKey(dir, paths[j]) })
	entries := make([]entry, 0, len(paths))
	var total int64
	files := 0
	for _, p := range paths {
		rel, relErr := filepath.Rel(dir, p)
		if relErr != nil {
			return format.Stats{}, relErr
		}
		rel = filepath.ToSlash(rel)
		if err := format.ValidBundlePath(rel); err != nil {
			return format.Stats{}, err
		}
		info, statErr := os.Lstat(p)
		if statErr != nil {
			return format.Stats{}, statErr
		}
		if info.IsDir() {
			entries = append(entries, entry{path: rel, isDir: true})
			continue
		}
		files++
		if files > format.MaxBundleFiles {
			return format.Stats{}, fmt.Errorf("%w: more than %d files", format.ErrBundleTooLarge, format.MaxBundleFiles)
		}
		if info.Size() > format.MaxFileBytes {
			return format.Stats{}, fmt.Errorf("%w: file %q exceeds %d bytes", format.ErrBundleTooLarge, rel, format.MaxFileBytes)
		}
		total += info.Size()
		if total > format.MaxTotalContentBytes {
			return format.Stats{}, fmt.Errorf("%w: bundle content exceeds %d bytes", format.ErrBundleTooLarge, format.MaxTotalContentBytes)
		}
		entries = append(entries, entry{path: rel, executable: info.Mode()&0o111 != 0, size: info.Size(), source: p})
	}
	return encodeEntries(w, entries)
}

type entry struct {
	path       string
	isDir      bool
	executable bool
	size       int64
	source     string // host file for encode
	content    []byte
}

func bundleSortKey(root, p string) string {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return p
	}
	return filepath.ToSlash(rel)
}

func encodeEntries(w io.Writer, entries []entry) (format.Stats, error) {
	digest := newDigest()
	sized := &countingWriter{w: io.MultiWriter(w, digest)}
	seen := ""
	var stats format.Stats
	for i := range entries {
		e := entries[i]
		if seen != "" && e.path <= seen {
			return format.Stats{}, fmt.Errorf("%w: path order violation at %q", format.ErrBundleInvalid, e.path)
		}
		seen = e.path
		header, err := ustarHeader(e.path, e.isDir, e.executable, e.size)
		if err != nil {
			return format.Stats{}, err
		}
		if _, err := sized.Write(header); err != nil {
			return format.Stats{}, err
		}
		stats.EncodedSize += blockSize
		if e.isDir {
			continue
		}
		stats.FileCount++
		stats.TotalBytes += e.size
		f, err := os.Open(e.source)
		if err != nil {
			return format.Stats{}, err
		}
		written, copyErr := io.CopyN(sized, f, e.size)
		closeErr := f.Close()
		if copyErr != nil {
			return format.Stats{}, copyErr
		}
		if written != e.size {
			return format.Stats{}, fmt.Errorf("%w: file %q changed size during encode", format.ErrBundleInvalid, e.path)
		}
		if closeErr != nil {
			return format.Stats{}, closeErr
		}
		if pad := align512(e.size) - e.size; pad > 0 {
			if _, err := sized.Write(make([]byte, pad)); err != nil {
				return format.Stats{}, err
			}
		}
		stats.EncodedSize += align512(e.size)
	}
	terminatorBytes := make([]byte, blockSize*terminator)
	if _, err := sized.Write(terminatorBytes); err != nil {
		return format.Stats{}, err
	}
	stats.EncodedSize += blockSize * terminator
	stats.Digest = sumDigest(digest)
	if stats.EncodedSize > format.MaxEncodedBundleBytes {
		return format.Stats{}, fmt.Errorf("%w: encoded bundle exceeds %d bytes", format.ErrBundleTooLarge, format.MaxEncodedBundleBytes)
	}
	return stats, nil
}

func align512(n int64) int64 {
	if n%blockSize == 0 {
		return n
	}
	return n + (blockSize - n%blockSize)
}

type countingWriter struct {
	w io.Writer
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

// Verify parses the complete bundle stream, enforces every format rule,
// recomputes the content digest, and compares it to expected. The stream
// must end exactly at the terminator.
func Verify(r io.Reader, expected string) (format.Stats, error) {
	return decode(r, expected, nil)
}

// Unpack is Verify plus extraction into dest (which must be empty or absent).
// Extracted files keep only the format's fixed permission grammar.
func Unpack(r io.Reader, expected, dest string) (format.Stats, error) {
	if info, err := os.Lstat(dest); err == nil {
		if !info.IsDir() {
			return format.Stats{}, fmt.Errorf("%w: unpack destination %q is not a directory", format.ErrBundleInvalid, dest)
		}
		entries, err := os.ReadDir(dest)
		if err != nil || len(entries) > 0 {
			return format.Stats{}, fmt.Errorf("%w: unpack destination %q is not empty", format.ErrBundleInvalid, dest)
		}
	}
	return decode(r, expected, &dest)
}

func decode(r io.Reader, expected string, dest *string) (format.Stats, error) {
	digest := newDigest()
	reader := io.TeeReader(r, digest)
	var stats format.Stats
	seen := ""
	created := map[string]bool{}
	for {
		header := make([]byte, blockSize)
		if _, err := io.ReadFull(reader, header); err != nil {
			return format.Stats{}, fmt.Errorf("%w: truncated header: %v", format.ErrBundleInvalid, err)
		}
		stats.EncodedSize += blockSize
		if isZeroBlock(header) {
			second := make([]byte, blockSize)
			if _, err := io.ReadFull(reader, second); err != nil {
				return format.Stats{}, fmt.Errorf("%w: truncated terminator: %v", format.ErrBundleInvalid, err)
			}
			stats.EncodedSize += blockSize
			if !isZeroBlock(second) {
				return format.Stats{}, fmt.Errorf("%w: malformed terminator", format.ErrBundleInvalid)
			}
			// Nothing but EOF may follow the terminator.
			var probe [1]byte
			if n, _ := reader.Read(probe[:]); n != 0 {
				return format.Stats{}, fmt.Errorf("%w: trailing bytes after terminator", format.ErrBundleInvalid)
			}
			break
		}
		if string(header[257:263]) != "ustar\x00" || string(header[263:265]) != "00" {
			return format.Stats{}, fmt.Errorf("%w: not a ustar archive", format.ErrBundleInvalid)
		}
		typeflag := header[156]
		if typeflag != '0' && typeflag != '5' {
			return format.Stats{}, fmt.Errorf("%w: entry type %q not allowed", format.ErrBundleInvalid, string(typeflag))
		}
		name := strings.TrimRight(string(header[0:100]), "\x00")
		prefix := strings.TrimRight(string(header[345:500]), "\x00")
		entryPath := name
		if prefix != "" {
			entryPath = prefix + "/" + name
		}
		if err := format.ValidBundlePath(entryPath); err != nil {
			return format.Stats{}, err
		}
		if seen != "" && entryPath <= seen {
			return format.Stats{}, fmt.Errorf("%w: unsorted or duplicate path %q", format.ErrBundleInvalid, entryPath)
		}
		seen = entryPath
		size, err := parseOctal(header[124:136])
		if err != nil {
			return format.Stats{}, fmt.Errorf("%w: bad size for %q", format.ErrBundleInvalid, entryPath)
		}
		mode, err := parseOctal(header[100:108])
		if err != nil {
			return format.Stats{}, fmt.Errorf("%w: bad mode for %q", format.ErrBundleInvalid, entryPath)
		}
		if typeflag == '5' {
			if size != 0 {
				return format.Stats{}, fmt.Errorf("%w: directory %q carries data", format.ErrBundleInvalid, entryPath)
			}
			if dest != nil {
				if err := createBundleDir(*dest, entryPath, created); err != nil {
					return format.Stats{}, err
				}
			}
			continue
		}
		stats.FileCount++
		if stats.FileCount > format.MaxBundleFiles {
			return format.Stats{}, fmt.Errorf("%w: more than %d files", format.ErrBundleTooLarge, format.MaxBundleFiles)
		}
		if size > format.MaxFileBytes {
			return format.Stats{}, fmt.Errorf("%w: file %q exceeds %d bytes", format.ErrBundleTooLarge, entryPath, format.MaxFileBytes)
		}
		stats.TotalBytes += size
		if stats.TotalBytes > format.MaxTotalContentBytes {
			return format.Stats{}, fmt.Errorf("%w: bundle content exceeds %d bytes", format.ErrBundleTooLarge, format.MaxTotalContentBytes)
		}
		remaining := io.LimitReader(reader, size)
		if dest != nil {
			if err := writeBundleFile(*dest, entryPath, mode&0o111 != 0, remaining, size, created); err != nil {
				return format.Stats{}, err
			}
		} else {
			if skipped, err := io.CopyN(io.Discard, remaining, size); err != nil || skipped != size {
				return format.Stats{}, fmt.Errorf("%w: truncated file %q", format.ErrBundleInvalid, entryPath)
			}
		}
		pad := align512(size) - size
		if pad > 0 {
			if _, err := io.CopyN(io.Discard, reader, pad); err != nil {
				return format.Stats{}, fmt.Errorf("%w: truncated padding for %q", format.ErrBundleInvalid, entryPath)
			}
		}
		stats.EncodedSize += align512(size)
	}
	if stats.EncodedSize > format.MaxEncodedBundleBytes {
		return format.Stats{}, fmt.Errorf("%w: encoded bundle exceeds %d bytes", format.ErrBundleTooLarge, format.MaxEncodedBundleBytes)
	}
	actual := sumDigest(digest)
	if expected != "" && actual != expected {
		return format.Stats{}, fmt.Errorf("%w: expected %s got %s", format.ErrDigestMismatch, expected, actual)
	}
	stats.Digest = actual
	return stats, nil
}

func createBundleDir(dest, rel string, created map[string]bool) error {
	// Parents are materialized implicitly; every path component is already
	// validated by format.ValidBundlePath.
	target := filepath.Join(dest, filepath.FromSlash(rel))
	if err := os.Mkdir(target, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	created[rel] = true
	return nil
}

func writeBundleFile(dest, rel string, executable bool, content io.Reader, size int64, created map[string]bool) error {
	target := filepath.Join(dest, filepath.FromSlash(rel))
	if parent := filepath.Dir(target); parent != dest {
		if err := os.MkdirAll(parent, 0o755); err != nil {
			return err
		}
	}
	mode := 0o644
	if executable {
		mode = 0o755
	}
	f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, os.FileMode(mode))
	if err != nil {
		return err
	}
	written, copyErr := io.CopyN(f, content, size)
	if copyErr == nil && written != size {
		copyErr = fmt.Errorf("%w: short write for %q", format.ErrBundleInvalid, rel)
	}
	if syncErr := f.Sync(); syncErr != nil && copyErr == nil {
		copyErr = syncErr
	}
	if closeErr := f.Close(); closeErr != nil && copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		_ = os.Remove(target)
		return copyErr
	}
	created[rel] = true
	return nil
}

func isZeroBlock(block []byte) bool {
	for _, b := range block {
		if b != 0 {
			return false
		}
	}
	return true
}

func parseOctal(field []byte) (int64, error) {
	s := strings.TrimRight(string(field), "\x00 ")
	if s == "" {
		return 0, nil
	}
	var value int64
	for _, c := range s {
		if c < '0' || c > '7' {
			return 0, fmt.Errorf("non-octal %q", s)
		}
		value = value*8 + int64(c-'0')
	}
	return value, nil
}

// VerifyFile is a convenience wrapper verifying a complete on-disk bundle.
func VerifyFile(path, expected string) (format.Stats, error) {
	f, err := os.Open(path)
	if err != nil {
		return format.Stats{}, err
	}
	defer func() { _ = f.Close() }()
	return Verify(f, expected)
}

// newDigest isolates the format's hash choice: sha256 over the full stream.
func newDigest() hash.Hash { return sha256.New() }

// sumDigest renders the running hash as a content digest string.
func sumDigest(h hash.Hash) string {
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}
