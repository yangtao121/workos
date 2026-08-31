// Package cipher implements the Credential Vault's authenticated-encryption
// boundary: AES-256-GCM under a 32-byte master key loaded from a dedicated
// key file, plus the master-key-derived HMAC idempotency digest. The master
// key never leaves this process; nothing derived from it is persisted except
// the keyed request digests, which do not enable offline guessing of
// secrets.
package cipher

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/yangtao121/workos/internal/core/credential/domain"
	"github.com/yangtao121/workos/internal/core/credential/ports"
)

// sealFormatVersion is bound into every AEAD as associated data so a
// ciphertext sealed under one format can never be replayed as another.
const sealFormatVersion = "workos.credential-seal.v1"

// digestFormatVersion prefixes the keyed idempotency digest.
const digestFormatVersion = "workos.credential-request.v1"

// derivation domain-separation labels. Both are stable constants: rotating
// the master key re-derives both keys (online master-key rotation is an
// explicit non-goal recorded in ADR-0009).
var (
	aeadKeyInfo    = []byte("workos-credential-vault.v1:aead-key")
	digestKeyInfo  = []byte("workos-credential-vault.v1:request-digest-key")
	derivationSalt = []byte("workos-credential-vault.v1:key-derivation")
)

// Cipher is the vault's only crypto adapter.
type Cipher struct {
	aead      cipher.AEAD
	digestKey []byte
}

// Load reads the master key from keyFile and derives the AEAD and digest
// keys. The file must be an absolute, cleaned, regular non-symlink file
// owned by this process's effective user, readable only by its owner (no
// group/world permission bits at all), and contain exactly 32 raw bytes —
// never hex, never base64, never multiple concatenated keys.
func Load(keyFile string) (*Cipher, error) {
	if keyFile == "" || !filepath.IsAbs(keyFile) || filepath.Clean(keyFile) != keyFile {
		return nil, errors.New("credential master key file must be an absolute, cleaned path")
	}
	file, err := os.OpenFile(keyFile, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("credential master key file is unavailable: %w", err)
	}
	defer file.Close() //nolint:errcheck -- read-only handle
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("inspect credential master key file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("credential master key file must be a regular file, not a symlink")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("credential master key file must not be group or world accessible")
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && stat.Uid != uint32(os.Geteuid()) {
		return nil, errors.New("credential master key file must be owned by the core process user")
	}
	if info.Size() != 32 {
		return nil, errors.New("credential master key file must contain exactly 32 raw bytes")
	}
	master, err := readExact(file, 32)
	if err != nil {
		return nil, err
	}
	digestKey := deriveKey(master, digestKeyInfo)
	aeadKey := deriveKey(master, aeadKeyInfo)
	overwrite(master)
	block, err := aes.NewCipher(aeadKey)
	overwrite(aeadKey)
	if err != nil {
		overwrite(digestKey)
		return nil, errors.New("credential master key is invalid")
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		overwrite(digestKey)
		return nil, errors.New("credential AEAD is unavailable")
	}
	return &Cipher{aead: aead, digestKey: digestKey}, nil
}

// deriveKey is a compact HMAC-SHA256 extract-and-expand under a fixed salt.
// The master key itself is never used directly as either derived key.
func deriveKey(master, info []byte) []byte {
	extract := hmac.New(sha256.New, derivationSalt)
	extract.Write(master)
	prk := extract.Sum(nil)
	expand := hmac.New(sha256.New, prk)
	expand.Write(info)
	expand.Write([]byte{1})
	derived := expand.Sum(nil)
	overwrite(prk)
	return derived
}

// Seal implements ports.Cipher.
func (c *Cipher) Seal(plaintext []byte, aad ports.SealAAD) (domain.SealedMaterial, error) {
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return domain.SealedMaterial{}, errors.New("credential nonce generation failed")
	}
	sealed := domain.SealedMaterial{
		Nonce:      nonce,
		Ciphertext: c.aead.Seal(nil, nonce, plaintext, aadBytes(aad)),
	}
	// Best-effort buffer hygiene: the plaintext slice handed to Seal is
	// overwritten when the caller owns it. Go cannot formally guarantee
	// zeroization of every runtime, exec, or string copy — documented in
	// ADR-0009 as an exposure-window reduction, not a guarantee.
	overwrite(plaintext)
	return sealed, nil
}

// Open implements ports.Cipher. Authentication failure is stored corruption
// and never falls back to plaintext.
func (c *Cipher) Open(material domain.SealedMaterial, aad ports.SealAAD) ([]byte, error) {
	plaintext, err := c.aead.Open(nil, material.Nonce, material.Ciphertext, aadBytes(aad))
	if err != nil {
		return nil, domain.ErrCorrupt
	}
	return plaintext, nil
}

// RequestDigest implements ports.Cipher.
func (c *Cipher) RequestDigest(canonical []byte) string {
	mac := hmac.New(sha256.New, c.digestKey)
	mac.Write(canonical)
	return digestFormatVersion + ":" + hex.EncodeToString(mac.Sum(nil))
}

// VerifyDigest reports in constant time whether canonical produces exactly
// the stored digest.
func (c *Cipher) VerifyDigest(canonical []byte, stored string) bool {
	expected := c.RequestDigest(canonical)
	return subtle.ConstantTimeCompare([]byte(expected), []byte(stored)) == 1
}

func aadBytes(aad ports.SealAAD) []byte {
	return []byte(fmt.Sprintf("%s\x1f%s\x1f%s\x1f%s\x1f%s\x1f%d",
		sealFormatVersion, aad.OwnerUserID, aad.CredentialID, aad.ConsumerID, aad.Purpose, aad.Revision))
}

func readExact(file *os.File, size int64) ([]byte, error) {
	master := make([]byte, size)
	read, err := io.ReadFull(file, master)
	if err != nil || int64(read) != size {
		overwrite(master)
		return nil, errors.New("credential master key file must contain exactly 32 raw bytes")
	}
	var extra [1]byte
	if read, err := file.Read(extra[:]); read != 0 || !errors.Is(err, io.EOF) {
		overwrite(master)
		return nil, errors.New("credential master key file must contain exactly 32 raw bytes")
	}
	return master, nil
}

func overwrite(buffer []byte) {
	for index := range buffer {
		buffer[index] = 0
	}
}
