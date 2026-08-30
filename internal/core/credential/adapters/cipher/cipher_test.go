package cipher

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	credentialdomain "github.com/yangtao121/workos/internal/core/credential/domain"
	"github.com/yangtao121/workos/internal/core/credential/ports"
)

const testKey32 = "0123456789abcdef0123456789abcdef"

func writeKey(t *testing.T, contents string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "master.key")
	if err := os.WriteFile(path, []byte(contents), mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func testAAD() ports.SealAAD {
	return ports.SealAAD{
		OwnerUserID: "0198d7ea-2110-7c42-b659-c5e4d73bc337", CredentialID: "0198d7ea-2110-7c42-b659-c5e4d73bc338",
		ConsumerID: "deepseek", Purpose: credentialdomain.PurposeProviderAPIKeyV1, Revision: 1,
	}
}

func TestSealOpenRoundTrip(t *testing.T) {
	ciph, err := Load(writeKey(t, testKey32, 0o600))
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := ciph.Seal([]byte("synthetic secret"), testAAD())
	if err != nil {
		t.Fatal(err)
	}
	if len(sealed.Nonce) != 12 || len(sealed.Ciphertext) <= 16 {
		t.Fatalf("unexpected sealed shape: %#v", sealed)
	}
	plaintext, err := ciph.Open(sealed, testAAD())
	if err != nil || string(plaintext) != "synthetic secret" {
		t.Fatalf("round trip failed: %q %v", plaintext, err)
	}
}

func TestTamperedCiphertextFailsClosed(t *testing.T) {
	ciph, _ := Load(writeKey(t, testKey32, 0o600))
	sealed, _ := ciph.Seal([]byte("synthetic secret"), testAAD())
	tampered := append([]byte(nil), sealed.Ciphertext...)
	tampered[1] ^= 0x80
	if _, err := ciph.Open(credentialdomain.SealedMaterial{Nonce: sealed.Nonce, Ciphertext: tampered}, testAAD()); !errors.Is(err, credentialdomain.ErrCorrupt) {
		t.Fatalf("tampered ciphertext: %v", err)
	}
}

func TestWrongAADAndWrongKeyFailClosed(t *testing.T) {
	ciph, _ := Load(writeKey(t, testKey32, 0o600))
	sealed, _ := ciph.Seal([]byte("synthetic secret"), testAAD())
	wrong := testAAD()
	wrong.Revision = 2
	if _, err := ciph.Open(sealed, wrong); !errors.Is(err, credentialdomain.ErrCorrupt) {
		t.Fatalf("wrong revision AAD: %v", err)
	}
	other, err := Load(writeKey(t, "ffffffffffffffff0123456789abcdef", 0o600))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.Open(sealed, testAAD()); !errors.Is(err, credentialdomain.ErrCorrupt) {
		t.Fatalf("wrong key: %v", err)
	}
}

func TestNonceUniqueness(t *testing.T) {
	ciph, _ := Load(writeKey(t, testKey32, 0o600))
	first, _ := ciph.Seal([]byte("same"), testAAD())
	second, _ := ciph.Seal([]byte("same"), testAAD())
	if string(first.Nonce) == string(second.Nonce) || string(first.Ciphertext) == string(second.Ciphertext) {
		t.Fatal("nonce uniqueness violated")
	}
}

func TestRequestDigestIsKeyedAndStable(t *testing.T) {
	ciph, _ := Load(writeKey(t, testKey32, 0o600))
	digest := ciph.RequestDigest([]byte("canonical-request"))
	if len(digest) != len("workos.credential-request.v1:")+64 || digest[:len(digest)-64] != "workos.credential-request.v1:" {
		t.Fatalf("unexpected digest shape: %q", digest)
	}
	if !ciph.VerifyDigest([]byte("canonical-request"), digest) {
		t.Fatal("digest verification failed for identical input")
	}
	if ciph.VerifyDigest([]byte("canonical-requests"), digest) {
		t.Fatal("digest verification accepted different input")
	}
	other, _ := Load(writeKey(t, "ffffffffffffffff0123456789abcdef", 0o600))
	if other.VerifyDigest([]byte("canonical-request"), digest) {
		t.Fatal("digest verified under a different master key")
	}
}

func TestMasterKeyFileGrammar(t *testing.T) {
	if _, err := Load(writeKey(t, testKey32, 0o600)); err != nil {
		t.Fatalf("honest key file rejected: %v", err)
	}
	for name, path := range map[string]func() string{
		"short key":      func() string { return writeKey(t, "short", 0o600) },
		"oversize key":   func() string { return writeKey(t, testKey32+testKey32, 0o600) },
		"group readable": func() string { return writeKey(t, testKey32, 0o644) },
		"relative":       func() string { return "relative/path.key" },
		"missing":        func() string { return filepath.Join(t.TempDir(), "absent.key") },
	} {
		if _, err := Load(path()); err == nil {
			t.Fatalf("%s was accepted", name)
		}
	}
	symlink := filepath.Join(t.TempDir(), "link.key")
	if err := os.Symlink(writeKey(t, testKey32, 0o600), symlink); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(symlink); err == nil {
		t.Fatal("symlinked key file was accepted")
	}
}
