// Package webpush implements RFC 8291 single-record encryption and RFC 8292
// VAPID using Go's cryptographic primitives. Only notification ids enter it.
package webpush

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/yangtao121/workos/internal/core/notification/domain"
)

var errConfiguration = errors.New("web push configuration is invalid")
var errDelivery = errors.New("web push relay did not accept delivery")
var encoding = base64.RawURLEncoding.Strict()

type Sender struct {
	key       *ecdsa.PrivateKey
	publicKey string
	subject   string
	client    *http.Client
}

// Load reads a dedicated raw P-256 scalar from an owner-only regular file.
// It is independent of the vault key and is never provided to the browser.
func Load(path, subject string) (*Sender, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errConfiguration
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, errConfiguration
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil {
		return nil, errConfiguration
	}
	owner, ok := stat.Sys().(*syscall.Stat_t)
	if !ok || int(owner.Uid) != os.Geteuid() || !stat.Mode().IsRegular() || stat.Mode().Perm()&0o077 != 0 || stat.Size() != 32 {
		return nil, errConfiguration
	}
	raw, err := io.ReadAll(io.LimitReader(file, 33))
	if err != nil {
		return nil, errConfiguration
	}
	defer clear(raw)
	return New(raw, subject, &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }})
}

func New(raw []byte, subject string, client *http.Client) (*Sender, error) {
	private, err := ecdh.P256().NewPrivateKey(raw)
	if err != nil || client == nil {
		return nil, errConfiguration
	}
	contact, err := url.Parse(subject)
	if err != nil || len(subject) > 256 || (contact.Scheme != "mailto" && contact.Scheme != "https") || (contact.Scheme == "mailto" && contact.Opaque == "") || (contact.Scheme == "https" && contact.Host == "") {
		return nil, errConfiguration
	}
	x, y := elliptic.Unmarshal(elliptic.P256(), private.PublicKey().Bytes())
	key := &ecdsa.PrivateKey{PublicKey: ecdsa.PublicKey{Curve: elliptic.P256(), X: x, Y: y}, D: new(big.Int).SetBytes(raw)}
	// Never follow a relay redirect with subscription data or authorization.
	bounded := *client
	bounded.Timeout = 5 * time.Second
	bounded.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Sender{key: key, publicKey: encoding.EncodeToString(private.PublicKey().Bytes()), subject: subject, client: &bounded}, nil
}

func (s *Sender) PublicKey() string { return s.publicKey }

func (s *Sender) Deliver(ctx context.Context, sub domain.PushSubscription, payload domain.PushPayload) error {
	if !domain.ValidUUID(payload.NotificationID) || !domain.ValidPushEndpoint(domain.PushPlatformWebPush, sub.Endpoint) {
		return domain.ErrPushInvalid
	}
	endpoint, err := url.Parse(sub.Endpoint)
	if err != nil || endpoint.User != nil || endpoint.Fragment != "" {
		return domain.ErrPushInvalid
	}
	receiver, err := encoding.DecodeString(sub.P256DH)
	if err != nil {
		return domain.ErrPushInvalid
	}
	auth, err := encoding.DecodeString(sub.AuthSecret)
	if err != nil {
		return domain.ErrPushInvalid
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return errDelivery
	}
	ephemeral, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return errDelivery
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return errDelivery
	}
	encrypted, err := encrypt(body, receiver, auth, salt, ephemeral)
	if err != nil {
		return domain.ErrPushInvalid
	}
	authorization, err := s.authorization(endpoint, time.Now().UTC())
	if err != nil {
		return errDelivery
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, sub.Endpoint, bytes.NewReader(encrypted))
	if err != nil {
		return domain.ErrPushInvalid
	}
	request.Header.Set("Authorization", authorization)
	request.Header.Set("Content-Encoding", "aes128gcm")
	request.Header.Set("Content-Type", "application/octet-stream")
	request.Header.Set("TTL", "60")
	request.Header.Set("Urgency", "normal")
	response, err := s.client.Do(request)
	if err != nil {
		return errDelivery
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusGone || response.StatusCode == http.StatusNotFound {
		return domain.ErrPushExpired
	}
	if response.StatusCode != http.StatusCreated {
		return errDelivery
	}
	return nil
}

func (s *Sender) authorization(endpoint *url.URL, now time.Time) (string, error) {
	host := strings.ToLower(endpoint.Host)
	if endpoint.Port() == "443" {
		host = strings.TrimSuffix(host, ":443")
	}
	claims, err := json.Marshal(struct {
		Audience string `json:"aud"`
		Expires  int64  `json:"exp"`
		Subject  string `json:"sub"`
	}{"https://" + host, now.Add(12 * time.Hour).Unix(), s.subject})
	if err != nil {
		return "", err
	}
	token := encoding.EncodeToString([]byte(`{"typ":"JWT","alg":"ES256"}`)) + "." + encoding.EncodeToString(claims)
	digest := sha256.Sum256([]byte(token))
	r, v, err := ecdsa.Sign(rand.Reader, s.key, digest[:])
	if err != nil {
		return "", err
	}
	signature := make([]byte, 64)
	r.FillBytes(signature[:32])
	v.FillBytes(signature[32:])
	return "vapid t=" + token + "." + encoding.EncodeToString(signature) + ", k=" + s.publicKey, nil
}

// Web Push uses a single record, so all HKDF outputs fit one SHA-256 block.
func mac(key, value []byte) []byte {
	h := hmac.New(sha256.New, key)
	_, _ = h.Write(value)
	return h.Sum(nil)
}
func encrypt(body, receiver, auth, salt []byte, ephemeral *ecdh.PrivateKey) ([]byte, error) {
	if len(body) > 3993 || len(auth) != 16 || len(salt) != 16 {
		return nil, domain.ErrPushInvalid
	}
	public, err := ecdh.P256().NewPublicKey(receiver)
	if err != nil {
		return nil, err
	}
	shared, err := ephemeral.ECDH(public)
	if err != nil {
		return nil, err
	}
	info := append([]byte("WebPush: info\x00"), receiver...)
	info = append(info, ephemeral.PublicKey().Bytes()...)
	info = append(info, 1)
	ikm := mac(mac(auth, shared), info)
	prk := mac(salt, ikm)
	cek := mac(prk, []byte("Content-Encoding: aes128gcm\x00\x01"))[:16]
	nonce := mac(prk, []byte("Content-Encoding: nonce\x00\x01"))[:12]
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	header := make([]byte, 21, 86)
	copy(header, salt)
	binary.BigEndian.PutUint32(header[16:20], 4096)
	header[20] = 65
	header = append(header, ephemeral.PublicKey().Bytes()...)
	plain := append(bytes.Clone(body), 2)
	return aead.Seal(header, nonce, plain, nil), nil
}
