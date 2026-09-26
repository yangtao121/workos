package application

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"

	"github.com/yangtao121/workos/internal/gateway/auth/domain"
	"github.com/yangtao121/workos/internal/gateway/auth/ports"
)

const (
	passwordSaltBytes  = 16
	passwordMemoryKiB  = 64 * 1024
	passwordIterations = 3
	passwordThreads    = 1
	passwordKeyBytes   = 32
)

// PasswordService uses the same trusted owner identity and secure cookie
// sessions as pairing mode, but its credential facts are Gateway-local.
type PasswordService struct {
	repo       ports.PasswordRepository
	ownerID    string
	sessionTTL time.Duration
	clock      ports.Clock
	entropy    Entropy
	ids        IDGenerator
	// Argon2id is deliberately bounded in parallel as well as by the public
	// rate limiter; otherwise a small burst could exhaust the Gateway.
	hashSlots chan struct{}
}

func NewPasswordService(repo ports.PasswordRepository, ownerID string, ttl time.Duration, clock ports.Clock, entropy Entropy, ids IDGenerator) (*PasswordService, error) {
	if !domain.ValidUUIDv7(ownerID) || ttl < domain.SessionMinTTL || ttl > domain.SessionMaxTTL {
		return nil, errors.New("invalid password auth configuration")
	}
	return &PasswordService{repo: repo, ownerID: ownerID, sessionTTL: ttl, clock: clock, entropy: entropy, ids: ids, hashSlots: make(chan struct{}, 4)}, nil
}

func validateUsername(raw string) (string, error) {
	if !utf8.ValidString(raw) {
		return "", domain.ErrInvalidRequest
	}
	value := strings.TrimSpace(raw)
	if value == "" || len([]rune(value)) > 80 {
		return "", domain.ErrInvalidRequest
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return "", domain.ErrInvalidRequest
		}
	}
	return value, nil
}

func validatePassword(password string) error {
	if !utf8.ValidString(password) || len([]byte(password)) < 12 || len([]byte(password)) > 1024 {
		return domain.ErrInvalidRequest
	}
	return nil
}

func (s *PasswordService) acquireHashSlot(ctx context.Context) error {
	select {
	case s.hashSlots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return domain.ErrStoreUnavailable
	}
}

func (s *PasswordService) releaseHashSlot() { <-s.hashSlots }

func (s *PasswordService) Ready(ctx context.Context) error {
	_, _, err := s.repo.Credential(ctx, s.ownerID)
	if err != nil {
		return fmt.Errorf("password auth is not configured or unavailable: %w", err)
	}
	return nil
}

func passwordHash(password string, salt []byte) string {
	key := argon2.IDKey([]byte(password), salt, passwordIterations, passwordMemoryKiB, passwordThreads, passwordKeyBytes)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", passwordMemoryKiB, passwordIterations, passwordThreads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key))
}

func verifyPassword(password, encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" || parts[2] != "v=19" ||
		parts[3] != "m=65536,t=3,p=1" {
		return false
	}
	salt, saltErr := base64.RawStdEncoding.DecodeString(parts[4])
	want, keyErr := base64.RawStdEncoding.DecodeString(parts[5])
	if saltErr != nil || keyErr != nil || len(salt) != passwordSaltBytes || len(want) != passwordKeyBytes {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, passwordIterations, passwordMemoryKiB, passwordThreads, passwordKeyBytes)
	return subtle.ConstantTimeCompare(got, want) == 1
}

// SetPassword rotates the only owner's password. The repository revokes all
// password sessions in the same transaction as replacing the hash.
func (s *PasswordService) SetPassword(ctx context.Context, username, password string) error {
	username, err := validateUsername(username)
	if err != nil {
		return err
	}
	if err := validatePassword(password); err != nil {
		return err
	}
	salt, err := s.entropy.Random(passwordSaltBytes)
	if err != nil {
		return err
	}
	if err := s.acquireHashSlot(ctx); err != nil {
		return err
	}
	hash := passwordHash(password, salt)
	s.releaseHashSlot()
	return s.repo.SetPassword(ctx, s.ownerID, username, hash, s.clock.Now())
}

// PasswordLogin is a successful one-time session issuance. Token is never
// sent in a protobuf body; the transport writes it as a Secure cookie.
type PasswordLogin struct {
	Device         domain.Device
	SessionToken   string
	SessionExpires time.Time
}

func (s *PasswordService) Login(ctx context.Context, username, password, deviceName, deviceClass string) (PasswordLogin, error) {
	name, err := domain.ValidateDeviceName(deviceName)
	if err != nil {
		return PasswordLogin{}, err
	}
	class, err := domain.ParseDeviceClass(deviceClass)
	if err != nil {
		return PasswordLogin{}, err
	}
	username, err = validateUsername(username)
	if err != nil {
		return PasswordLogin{}, domain.ErrAuthenticationFailed
	}
	if len(password) == 0 || len(password) > 1024 || !utf8.ValidString(password) {
		return PasswordLogin{}, domain.ErrAuthenticationFailed
	}
	storedName, hash, err := s.repo.Credential(ctx, s.ownerID)
	if err != nil {
		return PasswordLogin{}, err
	}
	if err := s.acquireHashSlot(ctx); err != nil {
		return PasswordLogin{}, err
	}
	valid := verifyPassword(password, hash)
	s.releaseHashSlot()
	if subtle.ConstantTimeCompare([]byte(username), []byte(storedName)) != 1 || !valid {
		return PasswordLogin{}, domain.ErrAuthenticationFailed
	}
	tokenRaw, err := s.entropy.Random(domain.SecretBytes)
	if err != nil {
		return PasswordLogin{}, err
	}
	token, err := domain.EncodeSecret(tokenRaw)
	if err != nil {
		return PasswordLogin{}, err
	}
	now := s.clock.Now()
	device := domain.Device{ID: s.ids.New(), OwnerID: s.ownerID, Name: name, Class: class, Revision: 1, CreatedAt: now, LastAuthenticatedAt: now}
	session := domain.DeviceSession{ID: s.ids.New(), OwnerID: s.ownerID, DeviceID: device.ID, TokenHash: domain.HashSessionToken(tokenRaw), CreatedAt: now, ExpiresAt: now.Add(s.sessionTTL)}
	if err := s.repo.CreatePasswordSession(ctx, s.ownerID, username, hash, device, session); err != nil {
		return PasswordLogin{}, err
	}
	return PasswordLogin{Device: device, SessionToken: token, SessionExpires: session.ExpiresAt}, nil
}

func (s *PasswordService) ResolveSession(ctx context.Context, token string) (domain.SessionIdentity, error) {
	raw, err := domain.ParsePairingSecret(token)
	if err != nil {
		return domain.SessionIdentity{}, domain.ErrAuthenticationFailed
	}
	session, device, err := s.repo.ResolvePasswordSession(ctx, domain.HashSessionToken(raw))
	if err != nil {
		return domain.SessionIdentity{}, err
	}
	if session.OwnerID != s.ownerID || device.OwnerID != s.ownerID || session.DeviceID != device.ID {
		return domain.SessionIdentity{}, domain.ErrAuthCorrupt
	}
	if !session.Active(s.clock.Now()) || !device.Active(s.clock.Now()) {
		return domain.SessionIdentity{}, domain.ErrAuthenticationFailed
	}
	return domain.SessionIdentity{OwnerID: s.ownerID, DeviceID: device.ID, SessionID: session.ID, ExpiresAt: session.ExpiresAt}, nil
}

func (s *PasswordService) CurrentDevice(ctx context.Context, identity domain.SessionIdentity) (domain.Device, error) {
	return s.repo.GetPasswordDevice(ctx, identity.OwnerID, identity.DeviceID)
}

func (s *PasswordService) ListDevices(ctx context.Context, identity domain.SessionIdentity, pageSize int, pageToken string) ([]domain.Device, string, error) {
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
			return nil, "", domain.ErrInvalidRequest
		}
		cursor = pageToken
	}
	devices, err := s.repo.ListPasswordDevices(ctx, identity.OwnerID, cursor, size+1)
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

func (s *PasswordService) RevokeDevice(ctx context.Context, identity domain.SessionIdentity, input RevokeDeviceInput) (domain.Device, bool, error) {
	if !domain.ValidUUIDv7(input.DeviceID) || !domain.ValidUUID(input.IdempotencyKey) || input.ExpectedRevision < 1 {
		return domain.Device{}, false, domain.ErrInvalidRequest
	}
	return s.repo.RevokePasswordDevice(ctx, ports.RevokeDeviceOp{
		OwnerID: identity.OwnerID, DeviceID: input.DeviceID, IdempotencyKey: input.IdempotencyKey,
		RequestDigest: revocationDigest(input.DeviceID, input.ExpectedRevision), ExpectedRevision: input.ExpectedRevision, Now: s.clock.Now(),
	})
}

func (s *PasswordService) Logout(ctx context.Context, identity domain.SessionIdentity) (time.Time, error) {
	now := s.clock.Now()
	return now, s.repo.LogoutPasswordSession(ctx, identity.OwnerID, identity.SessionID, now)
}
