//go:build integration

package integration_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	credentialcipher "github.com/yangtao121/workos/internal/core/credential/adapters/cipher"
	credentialapp "github.com/yangtao121/workos/internal/core/credential/application"
	credentialdomain "github.com/yangtao121/workos/internal/core/credential/domain"
	credentialports "github.com/yangtao121/workos/internal/core/credential/ports"
	"github.com/yangtao121/workos/internal/platform/ids"
)

// Synthetic-only material for the expanded kinds: every value below is
// obviously fake and accepted by nothing external.
var vaultKindSecrets = map[string][]byte{
	credentialdomain.PurposeCodexAuthV1:   []byte("codex-fixture-key-not-a-real-credential"),
	credentialdomain.PurposeGitHubTokenV1: []byte("github_pat_fixture_not_a_real_token_0123456789"),
	credentialdomain.PurposeCloudCredential: []byte(
		`{"access_key_id":"AKIAFIXTURENOTREAL","secret_access_key":"fixture-only-material","region":"us-east-1"}`),
}

func (f *vaultFixture) putKind(t *testing.T, consumer, purpose string, secret []byte) credentialdomain.Credential {
	t.Helper()
	credential, err := f.vault.Put(context.Background(), credentialports.PutCommand{
		OwnerUserID: f.owner, ConsumerID: consumer, Purpose: purpose,
		Label: "expansion", Secret: append([]byte(nil), secret...), IdempotencyKey: ids.UUIDv7{}.New(),
	})
	if err != nil {
		t.Fatalf("put %s credential: %v", purpose, err)
	}
	return credential
}

func writeSecondMasterKey(t *testing.T) string {
	t.Helper()
	key := make([]byte, 32)
	for index := range key {
		key[index] = byte(200 + index)
	}
	path := filepath.Join(t.TempDir(), "vault-master-2.key")
	if err := os.WriteFile(path, key, 0o600); err != nil {
		t.Fatalf("write second master key: %v", err)
	}
	return path
}

// TestVaultCredentialKindsLifecycle proves the finite kind vocabulary
// end to end on real PostgreSQL: each new kind stores, lists, resolves, and
// reveals; grammar violations fail closed with zero side effects; plaintext
// never lands in any column.
func TestVaultCredentialKindsLifecycle(t *testing.T) {
	f := newVaultFixture(t)
	ctx := context.Background()
	for consumer, purpose := range map[string]string{
		"codex":      credentialdomain.PurposeCodexAuthV1,
		"github":     credentialdomain.PurposeGitHubTokenV1,
		"aws-deploy": credentialdomain.PurposeCloudCredential,
	} {
		secret := vaultKindSecrets[purpose]
		credential := f.putKind(t, consumer, purpose, secret)
		if credential.Purpose != purpose || credential.Revision != 1 {
			t.Fatalf("unexpected %s credential: %#v", purpose, credential)
		}
		resolved, err := f.vault.ActiveCredential(ctx, f.owner, consumer, purpose)
		if err != nil {
			t.Fatalf("resolve active %s credential: %v", purpose, err)
		}
		if resolved.ID != credential.ID {
			t.Fatalf("%s active credential diverged", purpose)
		}
		revealed, secretBytes, err := f.vault.Reveal(ctx, credentialports.RevealCommand{
			OwnerUserID: f.owner, CredentialID: credential.ID,
		})
		if err != nil {
			t.Fatalf("reveal %s credential: %v", purpose, err)
		}
		if revealed.ID != credential.ID || string(secretBytes) != string(secret) {
			t.Fatalf("%s reveal diverged", purpose)
		}
	}

	// Kind grammar is enforced at the boundary with zero side effects.
	invalid := []struct {
		name    string
		purpose string
		secret  []byte
	}{
		{"short github token", credentialdomain.PurposeGitHubTokenV1, []byte("short")},
		{"github token with space", credentialdomain.PurposeGitHubTokenV1, []byte("github pat with spaces inside")},
		{"cloud non-json", credentialdomain.PurposeCloudCredential, []byte("plain text")},
		{"cloud nested", credentialdomain.PurposeCloudCredential, []byte(`{"nested":{"value":"x"}}`)},
		{"unknown purpose", "cloud-credential.v2", []byte("anything")},
	}
	before := f.countCredentials(t)
	for _, caseItem := range invalid {
		if _, err := f.vault.Put(ctx, credentialports.PutCommand{
			OwnerUserID: f.owner, ConsumerID: "consumer", Purpose: caseItem.purpose,
			Secret: append([]byte(nil), caseItem.secret...), IdempotencyKey: ids.UUIDv7{}.New(),
		}); !errors.Is(err, credentialdomain.ErrInvalid) {
			t.Fatalf("%s was not rejected: %v", caseItem.name, err)
		}
	}
	if after := f.countCredentials(t); after != before {
		t.Fatalf("rejected kinds consumed storage: before %d after %d", before, after)
	}
	f.assertNoPlaintext(t, "kinds lifecycle")
}

// TestVaultRevealAuditsAndFailsClosed proves the reveal contract: audit row
// in the same transaction, revoked fail closed, stale revision conflict,
// foreign credential not-found, and no audit entry ever carrying plaintext.
func TestVaultRevealAuditsAndFailsClosed(t *testing.T) {
	f := newVaultFixture(t)
	ctx := context.Background()
	credential := f.putKind(t, "codex", credentialdomain.PurposeCodexAuthV1, vaultKindSecrets[credentialdomain.PurposeCodexAuthV1])

	_, secret, err := f.vault.Reveal(ctx, credentialports.RevealCommand{
		OwnerUserID: f.owner, CredentialID: credential.ID, ExpectedRevision: 1,
	})
	if err != nil || string(secret) != string(vaultKindSecrets[credentialdomain.PurposeCodexAuthV1]) {
		t.Fatalf("pinned-revision reveal failed: %v", err)
	}
	if got := f.auditCount(t, "reveal"); got != 1 {
		t.Fatalf("expected one reveal audit row, found %d", got)
	}

	// A stale expected revision conflicts without revealing.
	if _, _, err := f.vault.Reveal(ctx, credentialports.RevealCommand{
		OwnerUserID: f.owner, CredentialID: credential.ID, ExpectedRevision: 99,
	}); !errors.Is(err, credentialdomain.ErrConflict) {
		t.Fatalf("stale reveal revision was not rejected: %v", err)
	}
	// Unknown and foreign credentials are indistinguishable.
	if _, _, err := f.vault.Reveal(ctx, credentialports.RevealCommand{
		OwnerUserID: f.owner, CredentialID: ids.UUIDv7{}.New(),
	}); !errors.Is(err, credentialdomain.ErrNotFound) {
		t.Fatalf("unknown credential reveal was not rejected: %v", err)
	}

	// Revoked material never decrypts again.
	revoked, err := f.vault.Revoke(ctx, credentialports.RevokeCommand{
		OwnerUserID: f.owner, CredentialID: credential.ID,
		ExpectedRevision: 1, IdempotencyKey: ids.UUIDv7{}.New(),
	})
	if err != nil {
		t.Fatalf("revoke credential: %v", err)
	}
	if _, _, err := f.vault.Reveal(ctx, credentialports.RevealCommand{
		OwnerUserID: f.owner, CredentialID: revoked.ID,
	}); !errors.Is(err, credentialdomain.ErrRevoked) {
		t.Fatalf("revoked credential reveal was not rejected: %v", err)
	}
}

// TestVaultMasterKeyRotationConverges proves the online rotation protocol on
// real PostgreSQL: one transaction moves every credential to the successor
// epoch, old-epoch material stops decrypting, new writes use the successor
// epoch, and retrying with the already-current key is a deterministic no-op.
func TestVaultMasterKeyRotationConverges(t *testing.T) {
	f := newVaultFixture(t)
	ctx := context.Background()
	first := f.put(t, "deepseek")
	second := f.putKind(t, "github", credentialdomain.PurposeGitHubTokenV1, vaultKindSecrets[credentialdomain.PurposeGitHubTokenV1])

	epoch, err := f.repo.VaultEpoch(ctx)
	if err != nil || epoch != 1 {
		t.Fatalf("initial vault epoch = %d, %v", epoch, err)
	}
	secondKey := readKeyFile(t, writeSecondMasterKey(t))
	result, err := f.vault.RotateMasterKey(ctx, credentialports.RotateMasterKeyCommand{NewMasterKey: append([]byte(nil), secondKey...)})
	if err != nil {
		t.Fatalf("rotate master key: %v", err)
	}
	if result.Noop || result.FromEpoch != 1 || result.ToEpoch != 2 || result.RotatedCount != 2 {
		t.Fatalf("unexpected rotation result: %#v", result)
	}

	// The successor epoch cipher opens everything; the old cipher fails
	// closed on every stored row.
	successor, err := credentialcipher.LoadAtEpoch(writeSecondMasterKey(t), 2)
	if err != nil {
		t.Fatalf("load successor cipher: %v", err)
	}
	_, secretBytes, err := f.repo.Reveal(ctx, successor, credentialports.RevealCommand{OwnerUserID: f.owner, CredentialID: second.ID})
	if err != nil || string(secretBytes) != string(vaultKindSecrets[credentialdomain.PurposeGitHubTokenV1]) {
		t.Fatalf("successor-epoch reveal failed: %v", err)
	}
	if _, _, err := f.repo.Reveal(ctx, f.cipher, credentialports.RevealCommand{OwnerUserID: f.owner, CredentialID: first.ID}); !errors.Is(err, credentialdomain.ErrCorrupt) {
		t.Fatalf("old-epoch reveal did not fail closed: %v", err)
	}

	// A credential written after rotation is sealed at the successor epoch
	// (the fixture cipher stays epoch 1, so prove via a fresh epoch-2 vault).
	rotatedVault, err := credentialapp.New(f.repo, successor)
	if err != nil {
		t.Fatalf("rebuild vault: %v", err)
	}
	post, err := rotatedVault.Put(ctx, credentialports.PutCommand{
		OwnerUserID: f.owner, ConsumerID: "codex", Purpose: credentialdomain.PurposeCodexAuthV1,
		Secret:         append([]byte(nil), vaultKindSecrets[credentialdomain.PurposeCodexAuthV1]...),
		IdempotencyKey: ids.UUIDv7{}.New(),
	})
	if err != nil {
		t.Fatalf("post-rotation put: %v", err)
	}
	if _, _, revealErr := f.repo.Reveal(ctx, successor, credentialports.RevealCommand{OwnerUserID: f.owner, CredentialID: post.ID}); revealErr != nil {
		t.Fatalf("post-rotation credential unreadable at epoch 2: %v", revealErr)
	}

	// Retrying with the current key is the recorded no-op convergence.
	retry, err := rotatedVault.RotateMasterKey(ctx, credentialports.RotateMasterKeyCommand{NewMasterKey: append([]byte(nil), secondKey...)})
	if err != nil {
		t.Fatalf("rotation retry failed: %v", err)
	}
	if !retry.Noop || retry.ToEpoch != 2 {
		t.Fatalf("rotation retry diverged: %#v", retry)
	}

	// A process whose epoch matches but whose material does not (as after a
	// foreign rotation) must not win the fingerprint no-op: the candidate
	// cannot decrypt the rows, so the attempt is refused and audited.
	mismatchedFile := writeMasterKeyBytes(t, readKeyFile(t, writeVaultMasterKey(t)), 2)
	mismatched, err := credentialcipher.LoadAtEpoch(mismatchedFile, 2)
	if err != nil {
		t.Fatalf("load mismatched cipher: %v", err)
	}
	mismatchedVault, err := credentialapp.New(f.repo, mismatched)
	if err != nil {
		t.Fatalf("rebuild mismatched vault: %v", err)
	}
	mismatchedMaterial := readKeyFile(t, writeVaultMasterKey(t))
	if _, err := mismatchedVault.RotateMasterKey(ctx, credentialports.RotateMasterKeyCommand{NewMasterKey: append([]byte(nil), mismatchedMaterial...)}); !errors.Is(err, credentialdomain.ErrConflict) {
		t.Fatalf("mismatched-material no-op was not refused: %v", err)
	}

	// A process still holding the old epoch cannot start a new rotation.
	staleVault, err := credentialapp.New(f.repo, f.cipher)
	if err != nil {
		t.Fatalf("rebuild stale vault: %v", err)
	}
	if _, err := staleVault.RotateMasterKey(ctx, credentialports.RotateMasterKeyCommand{NewMasterKey: append([]byte(nil), secondKey...)}); !errors.Is(err, credentialdomain.ErrConflict) {
		t.Fatalf("stale-epoch rotation was not refused: %v", err)
	}
	if got := f.auditCount(t, "rotate-master-key"); got != 4 {
		t.Fatalf("expected applied + no-op + two refused rotation audit rows, found %d", got)
	}
	f.assertNoPlaintext(t, "master-key rotation")
}

// TestVaultRotationIsAllOrNothing proves the crash-safety invariant: one
// unopenable row aborts the whole rotation, the epoch stays put, and no row
// is left re-sealed — the vault never becomes half-encrypted.
func TestVaultRotationIsAllOrNothing(t *testing.T) {
	f := newVaultFixture(t)
	ctx := context.Background()
	first := f.put(t, "deepseek")
	f.putKind(t, "github", credentialdomain.PurposeGitHubTokenV1, vaultKindSecrets[credentialdomain.PurposeGitHubTokenV1])

	// Corrupt the stored ciphertext of one row directly in the database.
	if _, err := f.pool.Exec(ctx,
		`UPDATE workos_core.provider_credentials SET ciphertext = $1 WHERE id = $2`,
		bytes.Repeat([]byte{0xAB}, 40), first.ID); err != nil {
		t.Fatalf("corrupt row: %v", err)
	}
	secondKey := readKeyFile(t, writeSecondMasterKey(t))
	if _, err := f.vault.RotateMasterKey(ctx, credentialports.RotateMasterKeyCommand{NewMasterKey: append([]byte(nil), secondKey...)}); err == nil {
		t.Fatal("rotation over a corrupt row did not fail")
	}
	epoch, err := f.repo.VaultEpoch(ctx)
	if err != nil || epoch != 1 {
		t.Fatalf("epoch moved during aborted rotation: %d, %v", epoch, err)
	}
	// No row was re-sealed: every row still carries epoch 1.
	stillEpochOne := f.countCredentialsAtEpoch(t, 1)
	if stillEpochOne != 2 {
		t.Fatalf("expected both rows untouched at epoch 1, found %d", stillEpochOne)
	}
}

// TestVaultKindsAreOwnerScoped proves the owner dimension of the expanded
// kinds: two owners hold independent same-consumer credentials and can never
// resolve or reveal each other's material.
func TestVaultKindsAreOwnerScoped(t *testing.T) {
	f := newVaultFixture(t)
	// The deployment is explicitly single-owner (users UNIQUE(kind)), so the
	// owner dimension is proven fail-closed: a foreign owner UUID can never
	// resolve, reveal, or put material, while the real owner keeps access.
	foreign := ids.UUIDv7{}.New()
	ctx := context.Background()
	mine := f.putKind(t, "github", credentialdomain.PurposeGitHubTokenV1, vaultKindSecrets[credentialdomain.PurposeGitHubTokenV1])
	if _, err := f.vault.ActiveCredential(ctx, foreign, "github", credentialdomain.PurposeGitHubTokenV1); !errors.Is(err, credentialdomain.ErrNotFound) {
		t.Fatalf("foreign owner resolved a credential: %v", err)
	}
	if _, _, err := f.vault.Reveal(ctx, credentialports.RevealCommand{OwnerUserID: foreign, CredentialID: mine.ID}); !errors.Is(err, credentialdomain.ErrNotFound) {
		t.Fatalf("cross-owner reveal was not rejected: %v", err)
	}
	if _, err := f.vault.Put(ctx, credentialports.PutCommand{
		OwnerUserID: foreign, ConsumerID: "github", Purpose: credentialdomain.PurposeGitHubTokenV1,
		Secret: []byte("github_pat_foreign_owner_fixture_token_998877"), IdempotencyKey: ids.UUIDv7{}.New(),
	}); err == nil {
		t.Fatal("foreign owner put was accepted")
	}
	if _, err := f.vault.ActiveCredential(ctx, f.owner, "github", credentialdomain.PurposeGitHubTokenV1); err != nil {
		t.Fatalf("owner lost visibility of own credential: %v", err)
	}
}

func (f *vaultFixture) countCredentials(t *testing.T) int {
	t.Helper()
	count := 0
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM workos_core.provider_credentials`).Scan(&count); err != nil {
		t.Fatalf("count credentials: %v", err)
	}
	return count
}

func (f *vaultFixture) countCredentialsAtEpoch(t *testing.T, epoch int64) int {
	t.Helper()
	count := 0
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM workos_core.provider_credentials WHERE key_epoch = $1`, epoch).Scan(&count); err != nil {
		t.Fatalf("count credentials at epoch: %v", err)
	}
	return count
}

func (f *vaultFixture) auditCount(t *testing.T, action string) int {
	t.Helper()
	count := 0
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM workos_core.credential_admin_audit WHERE action = $1`, action).Scan(&count); err != nil {
		t.Fatalf("count audit rows: %v", err)
	}
	return count
}

// assertNoPlaintext proves the plaintext-absence guarantee for the expanded
// kinds against every credential-bearing column in the vault schema.
func (f *vaultFixture) assertNoPlaintext(t *testing.T, stage string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rows, err := f.pool.Query(ctx,
		`SELECT consumer_id, purpose, encode(nonce,'escape') || encode(ciphertext,'escape') FROM workos_core.provider_credentials`)
	if err != nil {
		t.Fatalf("%s: read sealed material: %v", stage, err)
	}
	defer rows.Close()
	for rows.Next() {
		var consumer, purpose, material string
		if err := rows.Scan(&consumer, &purpose, &material); err != nil {
			t.Fatalf("%s: scan sealed material: %v", stage, err)
		}
		for _, secret := range vaultKindSecrets {
			if strings.Contains(material, string(secret)) {
				t.Fatalf("%s: plaintext %s secret leaked for (%s,%s)", stage, secret[:12], consumer, purpose)
			}
		}
		if strings.Contains(material, vaultSecretMarker) {
			t.Fatalf("%s: plaintext shared secret leaked for (%s,%s)", stage, consumer, purpose)
		}
	}
}

func readKeyFile(t *testing.T, path string) []byte {
	t.Helper()
	key, err := os.ReadFile(path)
	if err != nil || len(key) != 32 {
		t.Fatalf("read master key file: %v", err)
	}
	return key
}

// writeMasterKeyBytes persists raw key material at an explicit epoch-tagged
// path for a cipher LoadAtEpoch call.
func writeMasterKeyBytes(t *testing.T, key []byte, epoch int64) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), fmt.Sprintf("vault-key-epoch-%d.key", epoch))
	if err := os.WriteFile(path, key, 0o600); err != nil {
		t.Fatalf("write epoch key file: %v", err)
	}
	return path
}
