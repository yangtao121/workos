// Package application owns the Credential Vault's write protocol: canonical
// request digests, idempotency replay/conflict adjudication, grammar
// validation, and the lease coordination port consumed by the private
// transport. Secret material appears only as an argument and is sealed (or
// digest-only) before anything persists.
package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/yangtao121/workos/internal/core/credential/domain"
	"github.com/yangtao121/workos/internal/core/credential/ports"
)

// Service is the admin face of the vault. Lease issuance/renewal/release is
// coordinated by the composition layer (orchestration) because it must share
// one transaction with the Agent module's task-lease authority; this service
// deliberately never sees task leases.
type Service struct {
	repository ports.Repository
	cipher     ports.Cipher
	now        func() time.Time
}

func New(repository ports.Repository, ciph ports.Cipher) (*Service, error) {
	if repository == nil || ciph == nil {
		return nil, errors.New("credential vault requires a repository and cipher")
	}
	return &Service{repository: repository, cipher: ciph, now: func() time.Time { return time.Now().UTC() }}, nil
}

// Put validates and stores one new credential. Replay: the same key and the
// same canonical request (including the secret bytes) return the exact first
// metadata; the same key with any other request is a stable conflict.
// Failures never consume the key.
func (s *Service) Put(ctx context.Context, command ports.PutCommand) (domain.Credential, error) {
	command.OwnerUserID = strings.TrimSpace(command.OwnerUserID)
	command.ConsumerID = strings.TrimSpace(command.ConsumerID)
	command.Purpose = strings.TrimSpace(command.Purpose)
	command.Label = strings.TrimSpace(command.Label)
	command.IdempotencyKey = strings.TrimSpace(command.IdempotencyKey)
	switch {
	case command.OwnerUserID == "":
		return domain.Credential{}, domain.ErrInvalid
	case !domain.ValidConsumerID(command.ConsumerID):
		return domain.Credential{}, domain.ErrInvalid
	case !domain.ValidPurpose(command.Purpose):
		return domain.Credential{}, domain.ErrInvalid
	case !domain.ValidLabel(command.Label):
		return domain.Credential{}, domain.ErrInvalid
	case !domain.ValidSecret(command.Purpose, command.Secret):
		return domain.Credential{}, domain.ErrInvalid
	case !domain.ValidIdempotencyKey(command.IdempotencyKey):
		return domain.Credential{}, domain.ErrInvalid
	}
	command.Now = domain.CanonicalUTCTime(s.now())
	canonical, digest := s.canonicalRequest("put", map[string]string{
		"consumer": command.ConsumerID, "purpose": command.Purpose, "label": command.Label,
	}, command.Secret)
	defer overwrite(command.Secret)
	defer overwrite(canonical)
	command.RequestDigest = digest
	return s.mutate(ctx, command.IdempotencyKey, command.OwnerUserID, canonical, func() (domain.Credential, error) {
		return s.repository.Put(ctx, s.cipher, command)
	})
}

// Rotate replaces the secret of one logical credential under the expected
// revision; concurrent rotations are single-winner.
func (s *Service) Rotate(ctx context.Context, command ports.RotateCommand) (domain.Credential, error) {
	command.OwnerUserID = strings.TrimSpace(command.OwnerUserID)
	command.CredentialID = strings.TrimSpace(command.CredentialID)
	command.Label = strings.TrimSpace(command.Label)
	command.IdempotencyKey = strings.TrimSpace(command.IdempotencyKey)
	switch {
	case command.OwnerUserID == "" || !domain.ValidCredentialID(command.CredentialID):
		return domain.Credential{}, domain.ErrInvalid
	case !domain.ValidLabel(command.Label):
		return domain.Credential{}, domain.ErrInvalid
	case !domain.ValidSecretBytes(command.Secret):
		// The stored purpose decides the kind-specific grammar; it is
		// re-enforced inside the repository transaction after the row is
		// locked (ADR-0015). The boundary enforces the shared byte rules.
		return domain.Credential{}, domain.ErrInvalid
	case !domain.ValidRevision(command.ExpectedRevision):
		return domain.Credential{}, domain.ErrInvalid
	case !domain.ValidIdempotencyKey(command.IdempotencyKey):
		return domain.Credential{}, domain.ErrInvalid
	}
	command.Now = domain.CanonicalUTCTime(s.now())
	canonical, digest := s.canonicalRequest("rotate", map[string]string{
		"credential": command.CredentialID, "label": command.Label,
		"expected_revision": fmt.Sprintf("%d", command.ExpectedRevision),
	}, command.Secret)
	defer overwrite(command.Secret)
	defer overwrite(canonical)
	command.RequestDigest = digest
	return s.mutate(ctx, command.IdempotencyKey, command.OwnerUserID, canonical, func() (domain.Credential, error) {
		return s.repository.Rotate(ctx, s.cipher, command)
	})
}

// Revoke irreversibly revokes one credential under the expected revision.
func (s *Service) Revoke(ctx context.Context, command ports.RevokeCommand) (domain.Credential, error) {
	command.OwnerUserID = strings.TrimSpace(command.OwnerUserID)
	command.CredentialID = strings.TrimSpace(command.CredentialID)
	command.IdempotencyKey = strings.TrimSpace(command.IdempotencyKey)
	switch {
	case command.OwnerUserID == "" || !domain.ValidCredentialID(command.CredentialID):
		return domain.Credential{}, domain.ErrInvalid
	case !domain.ValidRevision(command.ExpectedRevision):
		return domain.Credential{}, domain.ErrInvalid
	case !domain.ValidIdempotencyKey(command.IdempotencyKey):
		return domain.Credential{}, domain.ErrInvalid
	}
	command.Now = domain.CanonicalUTCTime(s.now())
	canonical, digest := s.canonicalRequest("revoke", map[string]string{
		"credential":        command.CredentialID,
		"expected_revision": fmt.Sprintf("%d", command.ExpectedRevision),
	}, nil)
	defer overwrite(canonical)
	command.RequestDigest = digest
	return s.mutate(ctx, command.IdempotencyKey, command.OwnerUserID, canonical, func() (domain.Credential, error) {
		return s.repository.Revoke(ctx, command)
	})
}

// List returns the owner's credential metadata projections, oldest first by
// stable ordering inside the adapter.
func (s *Service) List(ctx context.Context, ownerUserID string) ([]domain.Credential, error) {
	ownerUserID = strings.TrimSpace(ownerUserID)
	if ownerUserID == "" {
		return nil, domain.ErrInvalid
	}
	return s.repository.List(ctx, ownerUserID)
}

// Reveal decrypts one active credential exactly once for the local operator
// and commits the audit row before returning (ADR-0015). ExpectedRevision
// zero accepts the current revision.
func (s *Service) Reveal(ctx context.Context, command ports.RevealCommand) (domain.Credential, []byte, error) {
	command.OwnerUserID = strings.TrimSpace(command.OwnerUserID)
	command.CredentialID = strings.TrimSpace(command.CredentialID)
	switch {
	case command.OwnerUserID == "" || !domain.ValidCredentialID(command.CredentialID):
		return domain.Credential{}, nil, domain.ErrInvalid
	case command.ExpectedRevision < 0 || (!domain.ValidRevision(command.ExpectedRevision) && command.ExpectedRevision != 0):
		return domain.Credential{}, nil, domain.ErrInvalid
	}
	command.Now = domain.CanonicalUTCTime(s.now())
	return s.repository.Reveal(ctx, s.cipher, command)
}

// RotateMasterKey re-seals every credential under the successor epoch
// (ADR-0015). The raw successor key arrives from the local admin socket,
// must be exactly 32 bytes, and is consumed inside the cipher boundary.
// Retrying with the already-current key is a deterministic no-op success,
// which is what a lost response after a committed rotation converges to.
func (s *Service) RotateMasterKey(ctx context.Context, command ports.RotateMasterKeyCommand) (ports.RotateMasterKeyResult, error) {
	if len(command.NewMasterKey) != 32 {
		return ports.RotateMasterKeyResult{}, domain.ErrInvalid
	}
	command.Now = domain.CanonicalUTCTime(s.now())
	currentEpoch, err := s.repository.VaultEpoch(ctx)
	if err != nil {
		return ports.RotateMasterKeyResult{}, err
	}
	if s.cipher.Epoch() != currentEpoch {
		// This process holds a stale epoch key (another instance already
		// rotated). It cannot verify anything; fail closed and audit.
		if recordErr := s.repository.RecordAudit(ctx, ports.AuditEntry{
			Action: "rotate-master-key", KeyEpoch: currentEpoch,
			Result: "refused stale process epoch", OccurredAt: command.Now,
		}); recordErr != nil {
			return ports.RotateMasterKeyResult{}, recordErr
		}
		return ports.RotateMasterKeyResult{}, domain.ErrConflict
	}
	// The candidate derivation consumes its copy of the key material; the
	// original bytes stay intact for the successor derivation below.
	candidate, err := s.cipher.WithEpoch(append([]byte(nil), command.NewMasterKey...), currentEpoch)
	if err != nil {
		return ports.RotateMasterKeyResult{}, domain.ErrInvalid
	}
	if candidate.KeyFingerprint() == s.cipher.KeyFingerprint() {
		// Equal fingerprints prove equal material — but never that the
		// material is the vault's live key (the epoch alone cannot, after a
		// foreign rotation). Prove the candidate opens every stored row
		// before answering the deterministic no-op.
		if err := s.repository.VerifyCurrentKey(ctx, candidate); err != nil {
			if recordErr := s.repository.RecordAudit(ctx, ports.AuditEntry{
				Action: "rotate-master-key", KeyEpoch: currentEpoch,
				Result: "refused key cannot decrypt", OccurredAt: command.Now,
			}); recordErr != nil {
				return ports.RotateMasterKeyResult{}, recordErr
			}
			return ports.RotateMasterKeyResult{}, domain.ErrConflict
		}
		// The rotation already committed. Audit the convergence and answer
		// success without rewriting a single row.
		if err := s.repository.RecordAudit(ctx, ports.AuditEntry{
			Action: "rotate-master-key", KeyEpoch: currentEpoch,
			Result: "no-op already current", OccurredAt: command.Now,
		}); err != nil {
			return ports.RotateMasterKeyResult{}, err
		}
		return ports.RotateMasterKeyResult{
			FromEpoch: currentEpoch, ToEpoch: currentEpoch, Noop: true,
		}, nil
	}
	next, err := s.cipher.WithEpoch(command.NewMasterKey, currentEpoch+1)
	if err != nil {
		return ports.RotateMasterKeyResult{}, domain.ErrInvalid
	}
	result, err := s.repository.RotateMasterKey(ctx, s.cipher, next, command)
	if errors.Is(err, domain.ErrConflict) {
		// The epoch moved underneath us (a concurrent rotation won). Refused
		// vault-wide attempts are audited before failing the operator.
		if recordErr := s.repository.RecordAudit(ctx, ports.AuditEntry{
			Action: "rotate-master-key", KeyEpoch: currentEpoch + 1,
			Result: "refused epoch moved", OccurredAt: command.Now,
		}); recordErr != nil {
			return ports.RotateMasterKeyResult{}, recordErr
		}
		return ports.RotateMasterKeyResult{}, err
	}
	return result, err
}

// ActiveCredential resolves the owner's current active credential for one
// consumer and purpose. Unknown/revoked are indistinguishable ErrNotFound.
func (s *Service) ActiveCredential(ctx context.Context, ownerUserID, consumerID, purpose string) (domain.Credential, error) {
	if ownerUserID == "" || !domain.ValidConsumerID(consumerID) || !domain.ValidPurpose(purpose) {
		return domain.Credential{}, domain.ErrInvalid
	}
	return s.repository.ActiveCredential(ctx, ownerUserID, consumerID, purpose)
}

// SnapshotVerifier is the port other Core modules use to re-verify that a
// task's credential snapshot still points at the active credential revision.
type SnapshotVerifier interface {
	VerifySnapshot(ctx context.Context, ownerUserID, consumerID, credentialID string, revision int64) error
}

// AsSnapshotVerifier exposes the exact-snapshot re-verification used by
// approval decisions and task admission.
func (s *Service) AsSnapshotVerifier() SnapshotVerifier { return verifier{s} }

// Cipher exposes the vault's crypto boundary to the composition layer so the
// lease issuer can open sealed material inside its own transaction.
func (s *Service) Cipher() ports.Cipher { return s.cipher }

type verifier struct{ service *Service }

func (v verifier) VerifySnapshot(ctx context.Context, ownerUserID, consumerID, credentialID string, revision int64) error {
	if !domain.ValidCredentialID(credentialID) || !domain.ValidRevision(revision) {
		return domain.ErrInvalid
	}
	credential, err := v.service.repository.CredentialByID(ctx, ownerUserID, credentialID)
	if errors.Is(err, domain.ErrNotFound) {
		return domain.ErrLeaseLost
	}
	if err != nil {
		return err
	}
	if credential.Status != domain.StatusActive || credential.Revision != revision ||
		credential.ConsumerID != consumerID {
		return domain.ErrLeaseLost
	}
	return nil
}

// SweepExpiredLeases marks active credential leases past expiry. It is a
// bounded housekeeping call driven by the composition root; correctness of
// the state machine never depends on it (every read also fails closed).
func (s *Service) SweepExpiredLeases(ctx context.Context) (int64, error) {
	return s.repository.ExpireStaleTaskCredentialLeases(ctx, s.now())
}

// mutate runs one admin write with the shared replay protocol: a consumed
// key re-adjudicates from the stored mapping — same canonical request
// (verified by its keyed digest) replays the versioned first response, any
// other request is a stable conflict — and every failure path leaves the key
// unconsumed.
func (s *Service) mutate(ctx context.Context, idempotencyKey, ownerUserID string, canonical []byte, apply func() (domain.Credential, error)) (domain.Credential, error) {
	credential, err := apply()
	switch {
	case err == nil:
		return credential, nil
	case errors.Is(err, ports.ErrKeyConsumed):
		record, found, readErr := s.repository.GetCredentialRequest(ctx, ownerUserID, idempotencyKey)
		if readErr != nil {
			return domain.Credential{}, readErr
		}
		if !found {
			// The physical arbitration lost but no mapping is visible: the
			// facts diverged from the protocol.
			return domain.Credential{}, domain.ErrCorrupt
		}
		if !s.cipher.VerifyDigest(canonical, record.RequestDigest) {
			return domain.Credential{}, domain.ErrIdempotencyConflict
		}
		return replayCredential(record, ownerUserID)
	case errors.Is(err, ports.ErrActiveExists):
		return domain.Credential{}, domain.ErrAlreadyExists
	default:
		return domain.Credential{}, err
	}
}

// replayCredential decodes the versioned first-response snapshot.
func replayCredential(record ports.RequestRecord, ownerUserID string) (domain.Credential, error) {
	var credential domain.Credential
	if err := json.Unmarshal(record.Result, &credential); err != nil ||
		!domain.ValidCredential(credential) || credential.OwnerUserID != ownerUserID {
		return domain.Credential{}, domain.ErrCorrupt
	}
	return credential, nil
}

// canonicalRequest builds the versioned canonical request bytes and their
// keyed digest. Field maps are marshalled by encoding/json, which sorts map
// keys, keeping the encoding deterministic. The secret bytes are appended
// under the HMAC itself: a leaked database row cannot verify guesses
// offline.
func (s *Service) canonicalRequest(command string, fields map[string]string, secret []byte) ([]byte, string) {
	canonical := map[string]any{"command": command, "version": "workos.credential-admin.v1", "fields": fields}
	body, err := json.Marshal(canonical)
	if err != nil {
		// Marshalling constrained string maps cannot fail; treat any failure
		// as the sanitized invalid verdict downstream.
		return nil, ""
	}
	payload := make([]byte, 0, len(body)+len(secret)+1)
	payload = append(payload, body...)
	payload = append(payload, 0x1f)
	payload = append(payload, secret...)
	return payload, s.cipher.RequestDigest(payload)
}

func overwrite(buffer []byte) {
	for index := range buffer {
		buffer[index] = 0
	}
}
