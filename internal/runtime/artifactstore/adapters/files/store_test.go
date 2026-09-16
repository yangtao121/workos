package files

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/yangtao121/workos/internal/runtime/artifactstore/domain"
)

const testOwner = "0198c0de-0000-7000-8000-000000000001"

func encodeTree(t *testing.T, files map[string]string) []byte {
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
	var buf bytes.Buffer
	if _, err := domain.EncodeDirectory(root, &buf); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func digestOf(t *testing.T, raw []byte) string {
	t.Helper()
	stats, err := domain.Verify(bytes.NewReader(raw), "")
	if err != nil {
		t.Fatal(err)
	}
	return stats.Digest
}

func TestRootModeEnforced(t *testing.T) {
	if _, err := New(""); err == nil {
		t.Fatal("empty root must fail")
	}
	loose := t.TempDir()
	if err := os.Chmod(loose, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := New(loose); err == nil {
		t.Fatal("non-0700 root must fail")
	}
	store, err := New(filepath.Join(t.TempDir(), "repo"))
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(store.Root())
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("root mode: %v %o", err, info.Mode().Perm())
	}
}

func TestPromoteAndOpenVerified(t *testing.T) {
	store, err := New(filepath.Join(t.TempDir(), "repo"))
	if err != nil {
		t.Fatal(err)
	}
	raw := encodeTree(t, map[string]string{"dist/app": "hello"})
	digest := digestOf(t, raw)

	staging, err := store.TempFile(testOwner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := staging.Write(raw); err != nil {
		t.Fatal(err)
	}
	if _, err := staging.Finish(); err != nil {
		t.Fatal(err)
	}
	if err := store.PromoteFile(testOwner, digest, staging); err != nil {
		t.Fatal(err)
	}
	if !store.Has(testOwner, digest) {
		t.Fatal("promoted bundle missing")
	}
	path, _, err := store.OpenVerified(testOwner, digest)
	if err != nil {
		t.Fatal(err)
	}
	onDisk, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(onDisk, raw) {
		t.Fatalf("stored bytes differ: %v", err)
	}
}

func TestOpenVerifiedRejectsTamperedBytes(t *testing.T) {
	store, err := New(filepath.Join(t.TempDir(), "repo"))
	if err != nil {
		t.Fatal(err)
	}
	raw := encodeTree(t, map[string]string{"dist/app": "payload"})
	digest := digestOf(t, raw)
	staging, err := store.TempFile(testOwner)
	if err != nil {
		t.Fatal(err)
	}
	staging.Write(raw) //nolint:errcheck
	if _, err := staging.Finish(); err != nil {
		t.Fatal(err)
	}
	if err := store.PromoteFile(testOwner, digest, staging); err != nil {
		t.Fatal(err)
	}
	// Tamper with the durable bytes directly.
	path, _, err := store.OpenVerified(testOwner, digest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(append([]byte{}, raw...), 0x00), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.OpenVerified(testOwner, digest); err == nil {
		t.Fatal("tampered bundle must fail verification on open")
	}
}

func TestOpenVerifiedMissingFile(t *testing.T) {
	store, err := New(filepath.Join(t.TempDir(), "repo"))
	if err != nil {
		t.Fatal(err)
	}
	digest := "sha256:" + string(bytes.Repeat([]byte{'a'}, 64))
	if _, _, err := store.OpenVerified(testOwner, digest); err == nil {
		t.Fatal("missing bundle bytes must fail, never fabricate content")
	}
}

func TestCleanTempRemovesStagingOnly(t *testing.T) {
	store, err := New(filepath.Join(t.TempDir(), "repo"))
	if err != nil {
		t.Fatal(err)
	}
	raw := encodeTree(t, map[string]string{"dist/app": "x"})
	digest := digestOf(t, raw)
	// One promoted bundle and one leftover staging file.
	staging, err := store.TempFile(testOwner)
	if err != nil {
		t.Fatal(err)
	}
	staging.Write(raw) //nolint:errcheck
	if _, err := staging.Finish(); err != nil {
		t.Fatal(err)
	}
	if err := store.PromoteFile(testOwner, digest, staging); err != nil {
		t.Fatal(err)
	}
	leftover, err := store.TempFile(testOwner)
	if err != nil {
		t.Fatal(err)
	}
	leftover.Write(raw) //nolint:errcheck
	leftover.Finish()   //nolint:errcheck

	if err := store.CleanTemp(); err != nil {
		t.Fatal(err)
	}
	if !store.Has(testOwner, digest) {
		t.Fatal("cleaned promoted bundle; only staging files may be removed")
	}
	if _, err := os.Stat(leftover.Name()); !os.IsNotExist(err) {
		t.Fatal("staging file survived CleanTemp")
	}
}

func TestCrossOwnerIsolation(t *testing.T) {
	store, err := New(filepath.Join(t.TempDir(), "repo"))
	if err != nil {
		t.Fatal(err)
	}
	other := "0198c0de-0000-7000-8000-000000000002"
	raw := encodeTree(t, map[string]string{"app": "x"})
	digest := digestOf(t, raw)
	staging, err := store.TempFile(testOwner)
	if err != nil {
		t.Fatal(err)
	}
	staging.Write(raw) //nolint:errcheck
	staging.Finish()   //nolint:errcheck
	if err := store.PromoteFile(testOwner, digest, staging); err != nil {
		t.Fatal(err)
	}
	if store.Has(other, digest) {
		t.Fatal("bundle leaked across owners")
	}
}
