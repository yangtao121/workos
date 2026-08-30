// Package application orchestrates the Gateway-owned device authentication
// flows: pairing ticket rotation, pairing proof, session proof, session
// resolution, and device management. All concurrency is adjudicated by the
// repository's PostgreSQL transactions; this layer owns grammar validation,
// verdict mapping, and the bounded process-local rate limiter.
package application

import (
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/yangtao121/workos/internal/gateway/auth/domain"
	"github.com/yangtao121/workos/internal/gateway/auth/ports"
)

// Page bounds for device listings.
const (
	DefaultPageSize = 50
	MaxPageSize     = 100
	// MaxAttempts is the durable per-object failure budget shared by
	// tickets and challenges; the repository enforces the same bound.
	MaxAttempts = 10
	// LastSeenInterval bounds how often the per-request gate may write the
	// last-seen timestamp; asset bursts never turn into per-request writes.
	LastSeenInterval = time.Minute
)

// Config carries the deployment facts the flows bind to. TLSFingerprint is
// the SHA-256 of the leaf certificate the Gateway actually serves.
type Config struct {
	OwnerID        string
	PublicOrigin   string
	TLSFingerprint string
	TicketTTL      time.Duration
	ChallengeTTL   time.Duration
	SessionTTL     time.Duration
}

func (c Config) validate() error {
	if !domain.ValidUUIDv7(c.OwnerID) {
		return errors.New("gateway auth owner id must be a canonical UUIDv7")
	}
	if c.PublicOrigin == "" {
		return errors.New("gateway auth public origin is required")
	}
	if !domain.ValidDigest(c.TLSFingerprint) {
		return errors.New("gateway auth TLS fingerprint must be sha256:<64 lowercase hex>")
	}
	if c.TicketTTL < domain.TicketMinTTL || c.TicketTTL > domain.TicketMaxTTL {
		return errors.New("pairing ticket TTL must be between 1m and 15m")
	}
	if c.ChallengeTTL < domain.ChallengeMinTTL || c.ChallengeTTL > domain.ChallengeMaxTTL {
		return errors.New("proof challenge TTL must be between 30s and 5m")
	}
	if c.SessionTTL < domain.SessionMinTTL || c.SessionTTL > domain.SessionMaxTTL {
		return errors.New("device session TTL must be between 5m and 30d")
	}
	return nil
}

// IDGenerator mints server-owned canonical UUIDv7 identifiers.
type IDGenerator interface {
	New() string
}

// Entropy is the process CSPRNG source.
type Entropy interface {
	Random(n int) ([]byte, error)
}

// Service is the single application service behind the public pairing,
// device, and private admin transports.
type Service struct {
	repo    ports.Repository
	cfg     Config
	clock   ports.Clock
	entropy Entropy
	ids     IDGenerator
	limiter *RateLimiter
}

func New(repo ports.Repository, cfg Config, clock ports.Clock, entropy Entropy, generator IDGenerator, limiter *RateLimiter) (*Service, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &Service{repo: repo, cfg: cfg, clock: clock, entropy: entropy, ids: generator, limiter: limiter}, nil
}

// Ready exposes store health for the production readiness probe.
func (s *Service) Ready(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := s.repo.Ready(ctx); err != nil {
		return fmt.Errorf("%w: %w", domain.ErrStoreUnavailable, err)
	}
	return nil
}

// RotatePairingTicket mints a new pairing invitation for ownerID and
// invalidates every outstanding one. The raw secret exists only in the
// returned value.
func (s *Service) RotatePairingTicket(ctx context.Context, ownerID string) (domain.PairingInfo, error) {
	if !domain.ValidUUIDv7(ownerID) {
		return domain.PairingInfo{}, fmt.Errorf("%w: owner id grammar", domain.ErrInvalidRequest)
	}
	raw, err := s.entropy.Random(domain.SecretBytes)
	if err != nil {
		return domain.PairingInfo{}, err
	}
	secret, err := domain.EncodeSecret(raw)
	if err != nil {
		return domain.PairingInfo{}, err
	}
	now := s.clock.Now()
	ticket := domain.PairingTicket{
		ID:             s.ids.New(),
		OwnerID:        ownerID,
		SecretHash:     domain.HashPairingSecret(raw),
		PublicOrigin:   s.cfg.PublicOrigin,
		TLSFingerprint: s.cfg.TLSFingerprint,
		State:          domain.TicketPending,
		ExpiresAt:      now.Add(s.cfg.TicketTTL),
		CreatedAt:      now,
	}
	if err := s.repo.RotatePairingTicket(ctx, ticket); err != nil {
		return domain.PairingInfo{}, err
	}
	return domain.PairingInfo{
		TicketID:       ticket.ID,
		Secret:         secret,
		PairingURL:     domain.PairingURL(s.cfg.PublicOrigin, secret, s.cfg.TLSFingerprint),
		PublicOrigin:   s.cfg.PublicOrigin,
		TLSFingerprint: s.cfg.TLSFingerprint,
		ExpiresAt:      ticket.ExpiresAt,
	}, nil
}

// BeginPairingInput carries the strict-grammar pairing request.
type BeginPairingInput struct {
	PairingSecret string
	PublicKeySPKI []byte
	DeviceName    string
	DeviceClass   string
}

// ChallengeView is the response-facing challenge fact.
type ChallengeView struct {
	ID        string
	Nonce     []byte
	ExpiresAt time.Time
}

// BeginPairingResult carries the server-minted pending device identity, the
// bounded challenge, and the ticket binding the transcript pins.
type BeginPairingResult struct {
	DeviceID  string
	Challenge ChallengeView
	TicketID  string
}

// BeginPairing locks (or recovers) the ticket for one browser profile key
// and issues a pairing challenge. Unknown tickets, foreign keys, and stale
// snapshots all collapse into the same sanitized failure.
func (s *Service) BeginPairing(ctx context.Context, input BeginPairingInput) (BeginPairingResult, error) {
	rawSecret, err := domain.ParsePairingSecret(input.PairingSecret)
	if err != nil {
		return BeginPairingResult{}, err
	}
	_, keyHash, err := domain.ParseP256SPKI(input.PublicKeySPKI)
	if err != nil {
		return BeginPairingResult{}, err
	}
	name, err := domain.ValidateDeviceName(input.DeviceName)
	if err != nil {
		return BeginPairingResult{}, err
	}
	class, err := domain.ParseDeviceClass(input.DeviceClass)
	if err != nil {
		return BeginPairingResult{}, err
	}
	now := s.clock.Now()
	ticket, err := s.repo.LoadTicketBySecretHash(ctx, domain.HashPairingSecret(rawSecret), now)
	if err != nil {
		return BeginPairingResult{}, domain.ErrAuthenticationFailed
	}
	if ticket.Attempts >= MaxAttempts {
		return BeginPairingResult{}, domain.ErrRateLimited
	}
	// A ticket pinned to a previous certificate or origin can never be
	// completed; it fails exactly like an unknown secret.
	if ticket.PublicOrigin != s.cfg.PublicOrigin || ticket.TLSFingerprint != s.cfg.TLSFingerprint {
		return BeginPairingResult{}, s.failTicket(ctx, ticket.ID)
	}
	var deviceID string
	switch {
	case ticket.Claimable(now):
		deviceID = s.ids.New()
		claimed, claimErr := s.repo.ClaimPairingTicket(ctx, ticket.ID, ticket.OwnerID, deviceID, keyHash, name, string(class), now)
		if claimErr != nil {
			return BeginPairingResult{}, s.failTicket(ctx, ticket.ID)
		}
		ticket = claimed
		deviceID = ticket.DeviceID
	case ticket.Recoverable(now):
		if ticket.PublicKeyHash != keyHash || ticket.ClaimedName != name || ticket.ClaimedClass != string(class) {
			return BeginPairingResult{}, s.failTicket(ctx, ticket.ID)
		}
		deviceID = ticket.DeviceID
	default:
		return BeginPairingResult{}, s.failTicket(ctx, ticket.ID)
	}
	challenge, err := s.newChallenge(ctx, domain.ChallengePairing, deviceID, ticket.ID, keyHash)
	if err != nil {
		return BeginPairingResult{}, err
	}
	return BeginPairingResult{DeviceID: deviceID, Challenge: challenge, TicketID: ticket.ID}, nil
}

func (s *Service) failTicket(ctx context.Context, ticketID string) error {
	_ = s.repo.FailTicketAttempt(ctx, ticketID)
	return domain.ErrAuthenticationFailed
}

func (s *Service) failChallenge(ctx context.Context, challengeID string) error {
	_ = s.repo.FailChallengeAttempt(ctx, challengeID)
	return domain.ErrAuthenticationFailed
}

func (s *Service) newChallenge(ctx context.Context, purpose domain.ChallengePurpose, deviceID, ticketID, keyHash string) (ChallengeView, error) {
	nonce, err := s.entropy.Random(domain.SecretBytes)
	if err != nil {
		return ChallengeView{}, err
	}
	now := s.clock.Now()
	challenge := domain.Challenge{
		ID:            s.ids.New(),
		Purpose:       purpose,
		DeviceID:      deviceID,
		TicketID:      ticketID,
		PublicKeyHash: keyHash,
		Nonce:         nonce,
		ExpiresAt:     now.Add(s.cfg.ChallengeTTL),
		CreatedAt:     now,
	}
	if err := s.repo.CreateChallenge(ctx, challenge); err != nil {
		return ChallengeView{}, err
	}
	return ChallengeView{ID: challenge.ID, Nonce: nonce, ExpiresAt: challenge.ExpiresAt}, nil
}

// CompletePairingInput carries the completed proof submission. The public
// key travels again so verification needs no secret-dependent state.
type CompletePairingInput struct {
	DeviceID      string
	ChallengeID   string
	PublicKeySPKI []byte
	Signature     []byte
}

// PairingCompletion is the authenticated outcome: the device view and the
// one-time session cookie material.
type PairingCompletion struct {
	Device         domain.Device
	SessionToken   string
	SessionExpires time.Time
}

// CompletePairing verifies the canonical proof in one transaction and, on
// success, creates the durable credential plus exactly one active session.
func (s *Service) CompletePairing(ctx context.Context, input CompletePairingInput) (PairingCompletion, error) {
	if !domain.ValidUUIDv7(input.DeviceID) || !domain.ValidUUIDv7(input.ChallengeID) {
		return PairingCompletion{}, fmt.Errorf("%w: identifier grammar", domain.ErrInvalidRequest)
	}
	key, keyHash, err := domain.ParseP256SPKI(input.PublicKeySPKI)
	if err != nil {
		return PairingCompletion{}, err
	}
	now := s.clock.Now()
	challenge, err := s.repo.LoadChallenge(ctx, input.ChallengeID)
	if err != nil {
		return PairingCompletion{}, domain.ErrAuthenticationFailed
	}
	if challenge.Purpose != domain.ChallengePairing || challenge.DeviceID != input.DeviceID ||
		challenge.TicketID == "" || challenge.PublicKeyHash != keyHash {
		return PairingCompletion{}, s.failChallenge(ctx, input.ChallengeID)
	}
	if challenge.Attempts >= MaxAttempts {
		return PairingCompletion{}, domain.ErrRateLimited
	}
	ticket, err := s.repo.LoadTicket(ctx, challenge.TicketID, s.cfg.OwnerID)
	if err != nil {
		return PairingCompletion{}, s.failChallenge(ctx, input.ChallengeID)
	}
	if ticket.State != domain.TicketClaimed || ticket.DeviceID != input.DeviceID ||
		ticket.PublicKeyHash != keyHash || !ticket.Recoverable(now) {
		return PairingCompletion{}, s.failChallenge(ctx, input.ChallengeID)
	}
	facts := domain.ProofFacts{
		PublicOrigin:   ticket.PublicOrigin,
		Purpose:        domain.PurposePairing,
		ChallengeID:    challenge.ID,
		Nonce:          challenge.Nonce,
		DeviceID:       ticket.DeviceID,
		PublicKeyHash:  keyHash,
		TicketID:       ticket.ID,
		TLSFingerprint: ticket.TLSFingerprint,
	}
	if err := domain.VerifyProof(key, facts, input.Signature); err != nil {
		_ = s.repo.FailChallengeAttempt(ctx, challenge.ID)
		_ = s.repo.FailTicketAttempt(ctx, ticket.ID)
		if errors.Is(err, domain.ErrAuthenticationFailed) {
			return PairingCompletion{}, err
		}
		return PairingCompletion{}, err
	}
	tokenRaw, err := s.entropy.Random(domain.SecretBytes)
	if err != nil {
		return PairingCompletion{}, err
	}
	token, err := domain.EncodeSecret(tokenRaw)
	if err != nil {
		return PairingCompletion{}, err
	}
	device, session, err := s.repo.CompletePairing(ctx, ports.CompletePairingOp{
		TicketID:         ticket.ID,
		OwnerID:          ticket.OwnerID,
		DeviceID:         input.DeviceID,
		DeviceName:       ticket.ClaimedName,
		DeviceClass:      ticket.ClaimedClass,
		PublicKeySPKI:    input.PublicKeySPKI,
		PublicKeyHash:    keyHash,
		ChallengeID:      challenge.ID,
		SessionID:        s.ids.New(),
		SessionTokenHash: domain.HashSessionToken(tokenRaw),
		SessionExpiresAt: now.Add(s.cfg.SessionTTL),
		Now:              now,
	})
	if err != nil {
		return PairingCompletion{}, err
	}
	return PairingCompletion{Device: device, SessionToken: token, SessionExpires: session.ExpiresAt}, nil
}

// BeginDeviceSession issues a challenge for the device proof. Unknown
// devices receive an indistinguishable decoy challenge.
func (s *Service) BeginDeviceSession(ctx context.Context, deviceID string) (ChallengeView, error) {
	if !domain.ValidUUIDv7(deviceID) {
		return ChallengeView{}, fmt.Errorf("%w: identifier grammar", domain.ErrInvalidRequest)
	}
	device, err := s.repo.LoadActiveDevice(ctx, deviceID)
	if err != nil {
		if !errors.Is(err, domain.ErrAuthenticationFailed) && !errors.Is(err, domain.ErrDeviceNotFound) {
			return ChallengeView{}, err
		}
		nonce, nonceErr := s.entropy.Random(domain.SecretBytes)
		if nonceErr != nil {
			return ChallengeView{}, nonceErr
		}
		now := s.clock.Now()
		decoy := domain.Challenge{
			ID:            s.ids.New(),
			Purpose:       domain.ChallengeSession,
			PublicKeyHash: decoyKeyHash(nonce),
			Nonce:         nonce,
			ExpiresAt:     now.Add(s.cfg.ChallengeTTL),
			CreatedAt:     now,
		}
		if createErr := s.repo.CreateChallenge(ctx, decoy); createErr != nil {
			return ChallengeView{}, createErr
		}
		return ChallengeView{ID: decoy.ID, Nonce: nonce, ExpiresAt: decoy.ExpiresAt}, nil
	}
	return s.newChallenge(ctx, domain.ChallengeSession, device.ID, "", device.PublicKeyHash)
}

// decoyKeyHash renders a deterministic-looking filler digest for decoy
// challenges; it binds no secret and is never used for verification.
func decoyKeyHash(nonce []byte) string {
	digest := sha256.Sum256(nonce)
	return "sha256:" + hex.EncodeToString(digest[:])
}

// CompleteSessionInput carries the session proof submission.
type CompleteSessionInput struct {
	DeviceID    string
	ChallengeID string
	Signature   []byte
}

// CompleteDeviceSession verifies the stored credential's public key and
// rotates the device's single active session.
func (s *Service) CompleteDeviceSession(ctx context.Context, input CompleteSessionInput) (PairingCompletion, error) {
	if !domain.ValidUUIDv7(input.DeviceID) || !domain.ValidUUIDv7(input.ChallengeID) {
		return PairingCompletion{}, fmt.Errorf("%w: identifier grammar", domain.ErrInvalidRequest)
	}
	now := s.clock.Now()
	challenge, err := s.repo.LoadChallenge(ctx, input.ChallengeID)
	if err != nil {
		return PairingCompletion{}, domain.ErrAuthenticationFailed
	}
	if challenge.Purpose != domain.ChallengeSession {
		return PairingCompletion{}, s.failChallenge(ctx, input.ChallengeID)
	}
	if challenge.Attempts >= MaxAttempts {
		return PairingCompletion{}, domain.ErrRateLimited
	}
	device, err := s.repo.LoadActiveDevice(ctx, input.DeviceID)
	if err != nil {
		_ = s.repo.ConsumeChallenge(ctx, input.ChallengeID, "", domain.ChallengeFailed, now)
		return PairingCompletion{}, domain.ErrAuthenticationFailed
	}
	if challenge.DeviceID != "" && challenge.DeviceID != input.DeviceID {
		_ = s.repo.ConsumeChallenge(ctx, input.ChallengeID, "", domain.ChallengeFailed, now)
		return PairingCompletion{}, domain.ErrAuthenticationFailed
	}
	if challenge.PublicKeyHash != device.PublicKeyHash {
		_ = s.repo.ConsumeChallenge(ctx, input.ChallengeID, "", domain.ChallengeFailed, now)
		return PairingCompletion{}, domain.ErrAuthenticationFailed
	}
	facts := domain.ProofFacts{
		PublicOrigin:  s.cfg.PublicOrigin,
		Purpose:       domain.PurposeSession,
		ChallengeID:   challenge.ID,
		Nonce:         challenge.Nonce,
		DeviceID:      device.ID,
		PublicKeyHash: device.PublicKeyHash,
	}
	key, err := parseSPKIPublicKey(device.PublicKeySPKI)
	if err != nil {
		return PairingCompletion{}, err
	}
	if err := domain.VerifyProof(key, facts, input.Signature); err != nil {
		_ = s.repo.FailChallengeAttempt(ctx, challenge.ID)
		if errors.Is(err, domain.ErrAuthenticationFailed) {
			return PairingCompletion{}, err
		}
		return PairingCompletion{}, err
	}
	tokenRaw, err := s.entropy.Random(domain.SecretBytes)
	if err != nil {
		return PairingCompletion{}, err
	}
	token, err := domain.EncodeSecret(tokenRaw)
	if err != nil {
		return PairingCompletion{}, err
	}
	device, session, err := s.repo.CompleteSession(ctx, ports.CompleteSessionOp{
		DeviceID:         device.ID,
		ChallengeID:      challenge.ID,
		SessionID:        s.ids.New(),
		SessionTokenHash: domain.HashSessionToken(tokenRaw),
		SessionExpiresAt: now.Add(s.cfg.SessionTTL),
		Now:              now,
	})
	if err != nil {
		return PairingCompletion{}, err
	}
	return PairingCompletion{Device: device, SessionToken: token, SessionExpires: session.ExpiresAt}, nil
}

// parseSPKIPublicKey re-parses a stored canonical SPKI for verification;
// a stored key that no longer parses is corruption, not a rejection.
func parseSPKIPublicKey(spki []byte) (*ecdsa.PublicKey, error) {
	key, _, err := domain.ParseP256SPKI(spki)
	if err != nil {
		return nil, fmt.Errorf("%w: stored public key: %w", domain.ErrAuthCorrupt, err)
	}
	return key, nil
}

// ResolveSession is the per-request gate: it resolves the cookie token to a
// trusted identity with no process-local caching, so a committed revocation
// blocks the very next request. It also best-effort touches last-seen under
// the bounded threshold.
func (s *Service) ResolveSession(ctx context.Context, rawToken string) (domain.SessionIdentity, error) {
	tokenRaw, err := domain.ParsePairingSecret(rawToken)
	if err != nil {
		return domain.SessionIdentity{}, domain.ErrAuthenticationFailed
	}
	session, device, err := s.repo.ResolveSession(ctx, domain.HashSessionToken(tokenRaw))
	if err != nil {
		// A transient store outage must surface as Unavailable, never as an
		// authentication failure that would loop the client into a re-pair.
		if errors.Is(err, domain.ErrStoreUnavailable) {
			return domain.SessionIdentity{}, err
		}
		return domain.SessionIdentity{}, domain.ErrAuthenticationFailed
	}
	now := s.clock.Now()
	if !session.Active(now) || !device.Active(now) {
		return domain.SessionIdentity{}, domain.ErrAuthenticationFailed
	}
	if session.OwnerID != s.cfg.OwnerID || device.OwnerID != s.cfg.OwnerID || session.DeviceID != device.ID {
		return domain.SessionIdentity{}, fmt.Errorf("%w: session binding", domain.ErrAuthCorrupt)
	}
	identity := domain.SessionIdentity{OwnerID: session.OwnerID, DeviceID: device.ID, SessionID: session.ID, ExpiresAt: session.ExpiresAt}
	s.repo.TouchSessionLastSeen(ctx, session.ID, now, now.Add(-LastSeenInterval))
	return identity, nil
}

// CurrentDevice resolves the sanitized view of the calling device.
func (s *Service) CurrentDevice(ctx context.Context, identity domain.SessionIdentity, sessionExpires time.Time) (domain.Device, time.Time, error) {
	device, err := s.repo.LoadActiveDevice(ctx, identity.DeviceID)
	if err != nil {
		return domain.Device{}, time.Time{}, domain.ErrDeviceNotFound
	}
	if device.OwnerID != identity.OwnerID {
		return domain.Device{}, time.Time{}, fmt.Errorf("%w: device binding", domain.ErrAuthCorrupt)
	}
	return device, sessionExpires, nil
}

// ListDevices returns the owner-scoped sanitized page. Page normalization
// lives here so the transport cannot widen it.
func (s *Service) ListDevices(ctx context.Context, identity domain.SessionIdentity, pageSize int, pageToken string) ([]domain.Device, string, error) {
	size := DefaultPageSize
	if pageSize > 0 {
		size = pageSize
	}
	if size > MaxPageSize {
		size = MaxPageSize
	}
	cursor := maxUUID
	if pageToken != "" {
		if !domain.ValidUUIDv7(pageToken) {
			return nil, "", fmt.Errorf("%w: page token grammar", domain.ErrInvalidRequest)
		}
		cursor = pageToken
	}
	devices, err := s.repo.ListDevices(ctx, identity.OwnerID, cursor, size+1)
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(devices) > size {
		devices = devices[:size]
		next = devices[size-1].ID
	}
	return devices, next, nil
}

const maxUUID = "ffffffff-ffff-ffff-ffff-ffffffffffff"

// RevokeDeviceInput is the owner-scoped idempotent revocation request.
type RevokeDeviceInput struct {
	DeviceID         string
	IdempotencyKey   string
	ExpectedRevision int64
}

// RevokeDevice revokes the credential, all its sessions, and persists the
// result snapshot in one transaction.
func (s *Service) RevokeDevice(ctx context.Context, identity domain.SessionIdentity, input RevokeDeviceInput) (domain.Device, bool, error) {
	if !domain.ValidUUIDv7(input.DeviceID) {
		return domain.Device{}, false, fmt.Errorf("%w: identifier grammar", domain.ErrInvalidRequest)
	}
	if !domain.ValidUUID(input.IdempotencyKey) {
		return domain.Device{}, false, fmt.Errorf("%w: idempotency key grammar", domain.ErrInvalidRequest)
	}
	if input.ExpectedRevision < 1 {
		return domain.Device{}, false, fmt.Errorf("%w: expected revision grammar", domain.ErrInvalidRequest)
	}
	return s.repo.RevokeDevice(ctx, ports.RevokeDeviceOp{
		OwnerID:          identity.OwnerID,
		DeviceID:         input.DeviceID,
		IdempotencyKey:   input.IdempotencyKey,
		RequestDigest:    revocationDigest(input.DeviceID, input.ExpectedRevision),
		ExpectedRevision: input.ExpectedRevision,
		Now:              s.clock.Now(),
	})
}

// revocationDigest pins the canonical request shape under the idempotency
// key; same key with a different request is a stable conflict.
func revocationDigest(deviceID string, revision int64) string {
	h := sha256.New()
	_, _ = h.Write([]byte("workos.device-revoke/v1\n"))
	_, _ = h.Write([]byte(deviceID))
	_, _ = h.Write([]byte("\n"))
	_, _ = h.Write([]byte(strconv.FormatInt(revision, 10)))
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// Logout revokes exactly the current session, keeping the local device key
// usable for a later proof re-authentication.
func (s *Service) Logout(ctx context.Context, identity domain.SessionIdentity) error {
	return s.repo.Logout(ctx, identity.SessionID, identity.OwnerID, s.clock.Now())
}
