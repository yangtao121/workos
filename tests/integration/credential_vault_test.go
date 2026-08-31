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

	"github.com/jackc/pgx/v5/pgxpool"

	agentpostgres "github.com/yangtao121/workos/internal/core/agent/adapters/postgres"
	agentdomain "github.com/yangtao121/workos/internal/core/agent/domain"
	credentialcipher "github.com/yangtao121/workos/internal/core/credential/adapters/cipher"
	credentialpostgres "github.com/yangtao121/workos/internal/core/credential/adapters/postgres"
	credentialapp "github.com/yangtao121/workos/internal/core/credential/application"
	credentialdomain "github.com/yangtao121/workos/internal/core/credential/domain"
	credentialports "github.com/yangtao121/workos/internal/core/credential/ports"
	"github.com/yangtao121/workos/internal/core/orchestration"
	"github.com/yangtao121/workos/internal/platform/ids"
	"github.com/yangtao121/workos/internal/platform/migrations"
)

// Synthetic-only secret: obviously fake, accepted by nothing external. The
// suite proves it never lands in the database as plaintext.
const vaultSecretMarker = "synthetic-vault-secret-not-a-real-key"

type vaultFixture struct {
	pool        *pgxpool.Pool
	owner       string
	project     string
	taskLeaseID string
	workerID    string
	cipher      *credentialcipher.Cipher
	vault       *credentialapp.Service
	repo        *credentialpostgres.Repository
	issuer      *orchestration.CredentialLeaseIssuer
	agents      *agentpostgres.Repository
}

func newVaultFixture(t *testing.T) *vaultFixture {
	t.Helper()
	dsn := scratchDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := migrations.Run(ctx, dsn); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	t.Cleanup(pool.Close)
	keyFile := writeVaultMasterKey(t)
	ciph, err := credentialcipher.Load(keyFile)
	if err != nil {
		t.Fatalf("load cipher: %v", err)
	}
	repo := credentialpostgres.New(pool)
	vault, err := credentialapp.New(repo, ciph)
	if err != nil {
		t.Fatalf("build vault: %v", err)
	}
	agents := agentpostgres.New(pool)
	issuer, err := orchestration.NewCredentialLeaseIssuer(pool, agents, repo, ciph, ids.UUIDv7{})
	if err != nil {
		t.Fatalf("build issuer: %v", err)
	}
	return &vaultFixture{pool: pool, owner: newVaultOwner(t, pool), cipher: ciph, vault: vault, repo: repo, issuer: issuer, agents: agents}
}

func writeVaultMasterKey(t *testing.T) string {
	t.Helper()
	key := make([]byte, 32)
	for index := range key {
		key[index] = byte(index + 1)
	}
	path := filepath.Join(t.TempDir(), "vault-master.key")
	if err := os.WriteFile(path, key, 0o600); err != nil {
		t.Fatalf("write master key: %v", err)
	}
	return path
}

func newVaultOwner(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	owner := ids.UUIDv7{}.New()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := pool.Exec(ctx, `INSERT INTO workos_core.users(id,kind,display_name,created_at) VALUES ($1,'owner','Vault Test Owner',$2) ON CONFLICT DO NOTHING`,
		owner, time.Now().UTC()); err != nil {
		t.Fatalf("seed owner: %v", err)
	}
	return owner
}

func (f *vaultFixture) put(t *testing.T, consumer string) credentialdomain.Credential {
	t.Helper()
	credential, err := f.vault.Put(context.Background(), credentialports.PutCommand{
		OwnerUserID: f.owner, ConsumerID: consumer, Purpose: credentialdomain.PurposeProviderAPIKeyV1,
		Label: "integration", Secret: []byte(vaultSecretMarker), IdempotencyKey: ids.UUIDv7{}.New(),
	})
	if err != nil {
		t.Fatalf("put credential: %v", err)
	}
	return credential
}

// TestVaultPutRotateRevokeLifecycle covers the admin write protocol against
// real PostgreSQL: single active credential per triple, revision discipline,
// keyed idempotency replay/conflict, and the plaintext-absence guarantee.
func TestVaultPutRotateRevokeLifecycle(t *testing.T) {
	f := newVaultFixture(t)
	ctx := context.Background()
	credential := f.put(t, "deepseek")
	if credential.Revision != 1 || credential.Status != credentialdomain.StatusActive || !credentialdomain.ValidCredentialID(credential.ID) {
		t.Fatalf("unexpected first credential: %#v", credential)
	}

	// A second active credential for the same triple is refused.
	if _, err := f.vault.Put(ctx, credentialports.PutCommand{
		OwnerUserID: f.owner, ConsumerID: "deepseek", Purpose: credentialdomain.PurposeProviderAPIKeyV1,
		Secret: []byte("another-synthetic"), IdempotencyKey: ids.UUIDv7{}.New(),
	}); !errors.Is(err, credentialdomain.ErrAlreadyExists) {
		t.Fatalf("second active credential was accepted: %v", err)
	}

	// Same key, same canonical request (including secret) replays exactly.
	key := ids.UUIDv7{}.New()
	first, err := f.vault.Put(ctx, credentialports.PutCommand{
		OwnerUserID: f.owner, ConsumerID: "generic-cli", Purpose: credentialdomain.PurposeProviderAPIKeyV1,
		Secret: []byte("second-consumer-secret"), Label: "l1", IdempotencyKey: key,
	})
	if err != nil {
		t.Fatalf("put second consumer: %v", err)
	}
	replayed, err := f.vault.Put(ctx, credentialports.PutCommand{
		OwnerUserID: f.owner, ConsumerID: "generic-cli", Purpose: credentialdomain.PurposeProviderAPIKeyV1,
		Secret: []byte("second-consumer-secret"), Label: "l1", IdempotencyKey: key,
	})
	if err != nil || replayed.ID != first.ID || replayed.Revision != first.Revision {
		t.Fatalf("idempotent replay diverged: %#v vs %#v err=%v", replayed, first, err)
	}
	if _, err := f.vault.Put(ctx, credentialports.PutCommand{
		OwnerUserID: f.owner, ConsumerID: "generic-cli", Purpose: credentialdomain.PurposeProviderAPIKeyV1,
		Secret: []byte("different-secret"), Label: "l1", IdempotencyKey: key,
	}); !errors.Is(err, credentialdomain.ErrIdempotencyConflict) {
		t.Fatalf("same key different request did not conflict: %v", err)
	}

	// Rotation keeps the logical ID, bumps the revision, single winner.
	rotateKey := ids.UUIDv7{}.New()
	rotated, err := f.vault.Rotate(ctx, credentialports.RotateCommand{
		OwnerUserID: f.owner, CredentialID: credential.ID, ExpectedRevision: 1,
		Secret: []byte(vaultSecretMarker + "-rotated"), IdempotencyKey: rotateKey,
	})
	if err != nil || rotated.ID != credential.ID || rotated.Revision != 2 {
		t.Fatalf("rotate verdict: %#v err=%v", rotated, err)
	}
	rotateReplay, err := f.vault.Rotate(ctx, credentialports.RotateCommand{
		OwnerUserID: f.owner, CredentialID: credential.ID, ExpectedRevision: 1,
		Secret: []byte(vaultSecretMarker + "-rotated"), IdempotencyKey: rotateKey,
	})
	if err != nil || rotateReplay.ID != rotated.ID || rotateReplay.Revision != rotated.Revision {
		t.Fatalf("rotate replay diverged: %#v vs %#v err=%v", rotateReplay, rotated, err)
	}
	if _, err := f.vault.Rotate(ctx, credentialports.RotateCommand{
		OwnerUserID: f.owner, CredentialID: credential.ID, ExpectedRevision: 2,
		Secret: []byte("different-rotation"), IdempotencyKey: rotateKey,
	}); !errors.Is(err, credentialdomain.ErrIdempotencyConflict) {
		t.Fatalf("consumed rotate key mutated a second revision: %v", err)
	}
	current, err := f.vault.ActiveCredential(ctx, f.owner, "deepseek", credentialdomain.PurposeProviderAPIKeyV1)
	if err != nil || current.Revision != 2 {
		t.Fatalf("rotate idempotency conflict changed durable revision: %#v err=%v", current, err)
	}
	if _, err := f.vault.Rotate(ctx, credentialports.RotateCommand{
		OwnerUserID: f.owner, CredentialID: credential.ID, ExpectedRevision: 1,
		Secret: []byte("stale-race"), IdempotencyKey: ids.UUIDv7{}.New(),
	}); !errors.Is(err, credentialdomain.ErrConflict) {
		t.Fatalf("stale expected-revision rotate was accepted: %v", err)
	}

	// Revocation is irreversible and bumps the revision once more.
	revokeKey := ids.UUIDv7{}.New()
	revoked, err := f.vault.Revoke(ctx, credentialports.RevokeCommand{
		OwnerUserID: f.owner, CredentialID: credential.ID, ExpectedRevision: 2, IdempotencyKey: revokeKey,
	})
	if err != nil || revoked.Status != credentialdomain.StatusRevoked || revoked.Revision != 3 {
		t.Fatalf("revoke verdict: %#v err=%v", revoked, err)
	}
	revokeReplay, err := f.vault.Revoke(ctx, credentialports.RevokeCommand{
		OwnerUserID: f.owner, CredentialID: credential.ID, ExpectedRevision: 2, IdempotencyKey: revokeKey,
	})
	if err != nil || revokeReplay.Status != revoked.Status || revokeReplay.Revision != revoked.Revision {
		t.Fatalf("revoke replay diverged: %#v vs %#v err=%v", revokeReplay, revoked, err)
	}
	if _, err := f.vault.ActiveCredential(ctx, f.owner, "deepseek", credentialdomain.PurposeProviderAPIKeyV1); !errors.Is(err, credentialdomain.ErrNotFound) {
		t.Fatalf("revoked credential still resolves active: %v", err)
	}

	// The database never holds the plaintext: scan every vault-owned byte
	// column for the synthetic marker.
	for _, table := range []string{"provider_credentials", "credential_admin_requests", "task_credential_leases"} {
		var count int
		row := f.pool.QueryRow(ctx, fmt.Sprintf(
			`SELECT count(*) FROM workos_core.%s t CROSS JOIN LATERAL (
			   SELECT to_jsonb(t) AS doc) cols WHERE doc::text LIKE $1`, table), "%"+vaultSecretMarker+"%")
		if err := row.Scan(&count); err != nil {
			t.Fatalf("scan %s for plaintext: %v", table, err)
		}
		if count != 0 {
			t.Fatalf("plaintext marker found in %s", table)
		}
	}
}

// TestVaultSealedMaterialFailsClosed proves ciphertext corruption, wrong
// AAD, and wrong keys never decrypt: the verdict is stored corruption, not a
// fallback.
func TestVaultSealedMaterialFailsClosed(t *testing.T) {
	f := newVaultFixture(t)
	ctx := context.Background()
	credential := f.put(t, "deepseek")
	other := newVaultFixture(t)
	_ = other
	var nonce, ciphertext []byte
	if err := f.pool.QueryRow(ctx, `SELECT nonce, ciphertext FROM workos_core.provider_credentials WHERE id = $1`,
		credential.ID).Scan(&nonce, &ciphertext); err != nil {
		t.Fatal(err)
	}
	aad := credentialports.SealAAD{
		OwnerUserID: f.owner, CredentialID: credential.ID,
		ConsumerID: "deepseek", Purpose: credentialdomain.PurposeProviderAPIKeyV1, Revision: 1,
	}
	plaintext, err := f.cipher.Open(credentialdomain.SealedMaterial{Nonce: nonce, Ciphertext: ciphertext}, aad)
	if err != nil || string(plaintext) != vaultSecretMarker {
		t.Fatalf("honest open failed: %v", err)
	}
	tampered := append([]byte(nil), ciphertext...)
	tampered[0] ^= 0x01
	if _, err := f.cipher.Open(credentialdomain.SealedMaterial{Nonce: nonce, Ciphertext: tampered}, aad); !errors.Is(err, credentialdomain.ErrCorrupt) {
		t.Fatalf("tampered ciphertext did not fail closed: %v", err)
	}
	wrongAAD := aad
	wrongAAD.Revision = 2
	if _, err := f.cipher.Open(credentialdomain.SealedMaterial{Nonce: nonce, Ciphertext: ciphertext}, wrongAAD); !errors.Is(err, credentialdomain.ErrCorrupt) {
		t.Fatalf("wrong AAD did not fail closed: %v", err)
	}
	wrongKeyFile := filepath.Join(t.TempDir(), "other-master.key")
	otherKey := make([]byte, 32)
	for index := range otherKey {
		otherKey[index] = byte(255 - index)
	}
	if err := os.WriteFile(wrongKeyFile, otherKey, 0o600); err != nil {
		t.Fatalf("write other master key: %v", err)
	}
	other = newVaultFixtureWithKey(t, wrongKeyFile)
	if _, err := other.cipher.Open(credentialdomain.SealedMaterial{Nonce: nonce, Ciphertext: ciphertext}, aad); !errors.Is(err, credentialdomain.ErrCorrupt) {
		t.Fatalf("wrong key did not fail closed: %v", err)
	}
	// Two seals of the same secret under the same identity must differ:
	// nonce uniqueness is observable.
	first, err := f.vault.Put(ctx, credentialports.PutCommand{
		OwnerUserID: f.owner, ConsumerID: "nonce-check", Purpose: credentialdomain.PurposeProviderAPIKeyV1,
		Secret: []byte("nonce"), IdempotencyKey: ids.UUIDv7{}.New(),
	})
	if err != nil {
		t.Fatal(err)
	}
	// The vault keeps a single row per logical credential: capture the
	// revision-1 material before the rotation, then compare against the
	// post-rotation row.
	var n1, c1 []byte
	if err := f.pool.QueryRow(ctx, `SELECT nonce, ciphertext FROM workos_core.provider_credentials WHERE id = $1 AND revision = 1`, first.ID).Scan(&n1, &c1); err != nil {
		t.Fatal(err)
	}
	if _, err := f.vault.Rotate(ctx, credentialports.RotateCommand{
		OwnerUserID: f.owner, CredentialID: first.ID, ExpectedRevision: 1,
		Secret: []byte("nonce"), IdempotencyKey: ids.UUIDv7{}.New(),
	}); err != nil {
		t.Fatal(err)
	}
	var n2, c2 []byte
	if err := f.pool.QueryRow(ctx, `SELECT nonce, ciphertext FROM workos_core.provider_credentials WHERE id = $1 AND revision = 2`, first.ID).Scan(&n2, &c2); err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(n1, n2) || bytes.Equal(c1, c2) {
		t.Fatal("re-sealing the same secret produced identical nonce or ciphertext")
	}

	// Database constraints are defense in depth, not the trust boundary: a
	// UUID-shaped row with the wrong UUID version is stored corruption and
	// must never escape through the metadata projection.
	if _, err := f.pool.Exec(ctx, `UPDATE workos_core.provider_credentials SET id = '550e8400-e29b-41d4-a716-446655440000' WHERE id = $1`, first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.vault.List(ctx, f.owner); !errors.Is(err, credentialdomain.ErrCorrupt) {
		t.Fatalf("corrupt stored credential metadata escaped: %v", err)
	}
}

func newVaultFixtureWithKey(t *testing.T, keyFile string) *vaultFixture {
	t.Helper()
	dsn := scratchDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := migrations.Run(ctx, dsn); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	t.Cleanup(pool.Close)
	ciph, err := credentialcipher.Load(keyFile)
	if err != nil {
		t.Fatalf("load cipher: %v", err)
	}
	repo := credentialpostgres.New(pool)
	vault, err := credentialapp.New(repo, ciph)
	if err != nil {
		t.Fatalf("build vault: %v", err)
	}
	return &vaultFixture{pool: pool, owner: newVaultOwner(t, pool), cipher: ciph, vault: vault, repo: repo}
}

// TestCredentialLeaseStateMachine drives the full lease protocol against
// real PostgreSQL: derive-from-lease, response-loss replay, foreign worker,
// renew with rotation/revocation verdicts, release idempotency, and expiry.
func TestCredentialLeaseStateMachine(t *testing.T) {
	f := newVaultFixture(t)
	ctx := context.Background()
	credential := f.put(t, "deepseek")

	// Build a queued task with a durable credential snapshot, then claim it
	// through the real Agent repository so an outbox-backed lease exists.
	f.claimTask(t, credential)

	leaseID := f.taskLeaseID
	worker := f.workerID
	grant, err := f.issuer.Acquire(ctx, leaseID, worker)
	if err != nil || !grant.Required || string(grant.Secret) != vaultSecretMarker || grant.CredentialRevision != 1 {
		t.Fatalf("acquire verdict: %#v err=%v", grant, err)
	}
	if grant.ExpiresAt.Before(time.Now().Add(20 * time.Second)) {
		t.Fatalf("credential lease not bounded by the task lease: %#v", grant)
	}
	// Response-loss replay: same task lease, same lease metadata, same
	// credential revision, secret still delivered exactly to this worker.
	replay, err := f.issuer.Acquire(ctx, leaseID, worker)
	if err != nil || replay.LeaseID != grant.LeaseID || replay.CredentialRevision != grant.CredentialRevision || string(replay.Secret) != vaultSecretMarker {
		t.Fatalf("replay verdict: %#v err=%v", replay, err)
	}
	// A corrupted durable lease must not be replayed from only its worker and
	// credential columns; every task/owner/provider fact is re-bound.
	if _, err := f.pool.Exec(ctx, `UPDATE workos_core.task_credential_leases SET task_id = $1 WHERE id = $2`, ids.UUIDv7{}.New(), grant.LeaseID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.issuer.Acquire(ctx, leaseID, worker); !errors.Is(err, credentialdomain.ErrLeaseLost) {
		t.Fatalf("corrupt task lease metadata replayed: %v", err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE workos_core.task_credential_leases SET task_id = $1 WHERE id = $2`, f.taskIDForLease(t, leaseID), grant.LeaseID); err != nil {
		t.Fatal(err)
	}
	// Foreign worker is refused.
	if _, err := f.issuer.Acquire(ctx, leaseID, "someone-else"); !errors.Is(err, credentialdomain.ErrLeaseLost) {
		t.Fatalf("foreign worker acquire was accepted: %v", err)
	}
	// Renew against the active task lease succeeds.
	verdict, err := f.issuer.Renew(ctx, grant.LeaseID, leaseID, worker)
	if err != nil || !verdict.Valid {
		t.Fatalf("renew verdict: %#v err=%v", verdict, err)
	}
	// Revoke → the next renew reports invalid so the worker kills the child.
	if _, err := f.vault.Revoke(ctx, credentialports.RevokeCommand{
		OwnerUserID: f.owner, CredentialID: credential.ID, ExpectedRevision: 1, IdempotencyKey: ids.UUIDv7{}.New(),
	}); err != nil {
		t.Fatal(err)
	}
	verdict, err = f.issuer.Renew(ctx, grant.LeaseID, leaseID, worker)
	if err != nil || verdict.Valid {
		t.Fatalf("renew after revoke was valid: %#v err=%v", verdict, err)
	}
	// Release is idempotent.
	if err := f.issuer.Release(ctx, grant.LeaseID, leaseID, worker); err != nil {
		t.Fatalf("release: %v", err)
	}
	if err := f.issuer.Release(ctx, grant.LeaseID, leaseID, worker); err != nil {
		t.Fatalf("idempotent release: %v", err)
	}
	if _, err := f.issuer.Renew(ctx, grant.LeaseID, leaseID, worker); err != nil {
		t.Fatalf("renew after release returned error instead of invalid verdict: %v", err)
	}
	// A fresh acquire against a task whose snapshot is revoked fails closed.
	second := f.claimTask(t, credential)
	_ = second
	// claimTask records the derived task lease id in the fixture.
	if _, err := f.issuer.Acquire(ctx, f.taskLeaseID, f.workerID); !errors.Is(err, credentialdomain.ErrLeaseLost) {
		t.Fatalf("acquire with revoked snapshot was accepted: %v", err)
	}

	// Expired credential leases sweep to expired status. The preceding
	// revocation freed the (owner, consumer, purpose) slot, so a fresh put
	// creates a new active credential for a new task snapshot.
	fresh := f.put(t, "deepseek")
	f.claimTask(t, fresh)
	expired := f.taskLeaseID
	grant2, err := f.issuer.Acquire(ctx, expired, f.workerID)
	if err != nil {
		t.Fatalf("acquire after rotation: %v", err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE workos_core.task_credential_leases SET expires_at = now() - interval '1 hour' WHERE id = $1`, grant2.LeaseID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.vault.SweepExpiredLeases(ctx); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := f.pool.QueryRow(ctx, `SELECT status FROM workos_core.task_credential_leases WHERE id = $1`, grant2.LeaseID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "expired" {
		t.Fatalf("expired lease status: %s", status)
	}
}

// claimTask seeds one queued task with a durable credential snapshot and
// claims it with the real Agent repository, exposing the derived task lease.
func (f *vaultFixture) claimTask(t *testing.T, credential credentialdomain.Credential) string {
	t.Helper()
	project := f.projectID(t)
	payload := fmt.Sprintf(`{"targetScope":{"projectId":%q},"role":"general","goal":"vault lease protocol"}`, project)
	task, err := f.agents.Create(context.Background(), agentdomain.Task{
		ID: ids.UUIDv7{}.New(), OwnerUserID: f.owner, ProjectID: project,
		Input: []byte(payload), State: agentdomain.StateQueued, ProviderID: "deepseek",
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		Credential: &agentdomain.CredentialSnapshot{CredentialID: credential.ID, Revision: credential.Revision},
	}, ids.UUIDv7{}.New())
	if err != nil {
		t.Fatalf("seed task: %v", err)
	}
	lease, err := f.agents.Claim(context.Background(), "vault-worker", 30*time.Second, ids.UUIDv7{}.New(), time.Now().UTC())
	if err != nil || lease == nil || lease.Task.ID != task.ID {
		t.Fatalf("claim task: lease=%#v err=%v", lease, err)
	}
	f.taskLeaseID = lease.ID
	f.workerID = "vault-worker"
	return task.ID
}

func (f *vaultFixture) projectID(t *testing.T) string {
	t.Helper()
	if f.project != "" {
		return f.project
	}
	id := ids.UUIDv7{}.New()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := f.pool.Exec(ctx, `INSERT INTO workos_core.projects(id,owner_user_id,idempotency_key,name,knowledge_collection_id,artifact_collection_id,revision,created_at,updated_at)
		VALUES ($1,$2,$3,'vault-fixture',$4,$4,1,$5,$5)`,
		id, f.owner, ids.UUIDv7{}.New(), ids.UUIDv7{}.New(), time.Now().UTC()); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	f.project = id
	return id
}

func (f *vaultFixture) taskIDForLease(t *testing.T, leaseID string) string {
	t.Helper()
	var taskID string
	if err := f.pool.QueryRow(context.Background(), `SELECT aggregate_id FROM workos_events.outbox WHERE lease_id = $1`, leaseID).Scan(&taskID); err != nil {
		t.Fatal(err)
	}
	return taskID
}

var _ = strings.TrimSpace
