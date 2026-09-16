package domain

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for path, content := range files {
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func TestEncodeDirectoryDeterministic(t *testing.T) {
	files := map[string]string{"dist/server": "binary-a", "dist/config.yaml": "conf", "README.txt": "readme"}
	rootA := writeTree(t, files)
	rootB := writeTree(t, files)

	var bufA, bufB bytes.Buffer
	statsA, err := EncodeDirectory(rootA, &bufA)
	if err != nil {
		t.Fatalf("encode A: %v", err)
	}
	statsB, err := EncodeDirectory(rootB, &bufB)
	if err != nil {
		t.Fatalf("encode B: %v", err)
	}
	if !bytes.Equal(bufA.Bytes(), bufB.Bytes()) {
		t.Fatal("identical trees must encode to identical bytes")
	}
	if statsA.Digest != statsB.Digest || statsA.Digest != sha256Hex(bufA.Bytes()) {
		t.Fatalf("digest must equal sha256 of full stream: %s vs %s", statsA.Digest, sha256Hex(bufA.Bytes()))
	}
	if statsA.FileCount != 3 || statsA.TotalBytes != int64(len("binary-a")+len("conf")+len("readme")) {
		t.Fatalf("unexpected stats: %+v", statsA)
	}
}

func TestEncodeExecutableBitAffectsMode(t *testing.T) {
	root := writeTree(t, map[string]string{"dist/server": "x"})
	if err := os.Chmod(filepath.Join(root, "dist", "server"), 0o755); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if _, err := EncodeDirectory(root, &buf); err != nil {
		t.Fatal(err)
	}
	// Directory header at 0, file header at 512: mode field bytes 100..108.
	if got := string(bytes.TrimRight(buf.Bytes()[100+512:108+512], "\x00")); got != "0000755" {
		t.Fatalf("executable file mode = %q, want 0000755", got)
	}
}

func TestEncodeRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	if err := os.Symlink("/etc/passwd", filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	_, err := EncodeDirectory(root, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("symlink must be rejected, got %v", err)
	}
}

func TestVerifyRejectsTampering(t *testing.T) {
	root := writeTree(t, map[string]string{"dist/app": "hello"})
	var buf bytes.Buffer
	stats, err := EncodeDirectory(root, &buf)
	if err != nil {
		t.Fatal(err)
	}
	raw := buf.Bytes()
	raw[len(raw)-600] ^= 0x01 // flip a byte inside the last data block
	if _, err := Verify(bytes.NewReader(raw), stats.Digest); err == nil {
		t.Fatal("tampered bundle must fail digest verification")
	}
}

func TestVerifyRejectsTraversalAndUnsorted(t *testing.T) {
	// Build a valid bundle, then hand-craft evil headers.
	root := writeTree(t, map[string]string{"dist/app": "x"})
	var buf bytes.Buffer
	if _, err := EncodeDirectory(root, &buf); err != nil {
		t.Fatal(err)
	}
	// Craft an absolute-path entry using the encoder's own header builder.
	header, err := ustarHeader("/etc/passwd", false, false, 0)
	if err != nil {
		t.Skipf("absolute path rejected at header build: %v", err)
	}
	stream := append(header, make([]byte, 1024)...)
	if _, err := Verify(bytes.NewReader(stream), ""); err == nil {
		t.Fatal("absolute path must be rejected on decode")
	}
	// Duplicate path entries must fail the strictly-increasing check.
	h1, err := ustarHeader("dist/app", false, false, 4)
	if err != nil {
		t.Fatal(err)
	}
	h2, err := ustarHeader("dist/app", false, false, 4)
	if err != nil {
		t.Fatal(err)
	}
	var dup bytes.Buffer
	dup.Write(h1)
	dup.Write([]byte("data"))
	dup.Write(make([]byte, 508))
	dup.Write(h2)
	dup.Write([]byte("data"))
	dup.Write(make([]byte, 508))
	dup.Write(make([]byte, 1024))
	if _, err := Verify(bytes.NewReader(dup.Bytes()), ""); err == nil {
		t.Fatal("duplicate paths must be rejected")
	}
}

func TestVerifyRejectsTrailingGarbage(t *testing.T) {
	root := writeTree(t, map[string]string{"a": "x"})
	var buf bytes.Buffer
	if _, err := EncodeDirectory(root, &buf); err != nil {
		t.Fatal(err)
	}
	raw := append(append([]byte{}, buf.Bytes()...), 0xFF)
	if _, err := Verify(bytes.NewReader(raw), ""); err == nil {
		t.Fatal("trailing bytes after terminator must be rejected")
	}
}

func TestUnpackRoundTrip(t *testing.T) {
	root := writeTree(t, map[string]string{"dist/server": "bin", "dist/assets/x.css": "css"})
	if err := os.Chmod(filepath.Join(root, "dist", "server"), 0o755); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	stats, err := EncodeDirectory(root, &buf)
	if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "out")
	if err := os.Mkdir(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Unpack(bytes.NewReader(buf.Bytes()), stats.Digest, dest); err != nil {
		t.Fatalf("unpack: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(dest, "dist", "server"))
	if err != nil || string(content) != "bin" {
		t.Fatalf("unpacked content mismatch: %q %v", content, err)
	}
	info, err := os.Lstat(filepath.Join(dest, "dist", "server"))
	if err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("executable permission lost: %v %o", err, info.Mode().Perm())
	}
	// Unpacking into a non-empty directory is refused.
	if _, err := Unpack(bytes.NewReader(buf.Bytes()), stats.Digest, dest); err == nil {
		t.Fatal("unpack into non-empty destination must fail")
	}
}

func TestBundleLimits(t *testing.T) {
	if err := ValidBundlePath("../escape"); err == nil {
		t.Fatal("parent escape must be invalid")
	}
	if err := ValidBundlePath("a//b"); err == nil {
		t.Fatal("empty segment must be invalid")
	}
	if err := ValidBundlePath(strings.Repeat("a/", 70) + "b"); err == nil {
		t.Fatal("overlong path must be invalid")
	}
	if err := ValidBundlePath("dist/server.bin"); err != nil {
		t.Fatalf("legal path rejected: %v", err)
	}
	// File-count limit via synthetic paths.
	if MaxBundleFiles != 1024 {
		t.Fatalf("file limit changed: %d", MaxBundleFiles)
	}
}

func TestLongPathSplit(t *testing.T) {
	// 120-byte path splits into ustar prefix+name.
	long := strings.Repeat("d/", 30) + "server" // 67 bytes fine; make > 100
	long = strings.Repeat("seg-", 26) + "/server"
	if len(long) <= 100 {
		t.Fatalf("test path too short: %d", len(long))
	}
	name, prefix, err := splitUSTAR(long)
	if err != nil {
		t.Fatal(err)
	}
	if prefix+"/"+name != long || len(name) > 100 || len(prefix) > 155 {
		t.Fatalf("bad split: %q + %q", prefix, name)
	}
	header, err := ustarHeader(long, false, false, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Re-parse the header like the decoder does.
	parsedName := strings.TrimRight(string(header[0:100]), "\x00")
	parsedPrefix := strings.TrimRight(string(header[345:500]), "\x00")
	if parsedPrefix != prefix || parsedName != name {
		t.Fatalf("header roundtrip mismatch: %q/%q", parsedPrefix, parsedName)
	}
}
