package webpush

import (
	"context"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yangtao121/workos/internal/core/notification/domain"
)

func TestDedicatedKeyFile(t *testing.T) {
	key, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "push.key")
	if err := os.WriteFile(path, key.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path, "mailto:push@example.invalid"); err != nil {
		t.Fatal(err)
	}
	alias := path + ".link"
	if err := os.Symlink(path, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(alias, "mailto:push@example.invalid"); err == nil {
		t.Fatal("symlink accepted")
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path, "mailto:push@example.invalid"); err == nil {
		t.Fatal("shared private key accepted")
	}
}

func decode(t *testing.T, value string) []byte {
	t.Helper()
	result, err := encoding.DecodeString(value)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

// Known-answer vector from RFC 8291 §5 and appendix A. This independently
// catches key order, delimiter, header, derivation and AEAD framing drift.
func TestRFC8291Vector(t *testing.T) {
	private, err := ecdh.P256().NewPrivateKey(decode(t, "yfWPiYE-n46HLnH0KqZOF1fJJU3MYrct3AELtAQ-oRw"))
	if err != nil {
		t.Fatal(err)
	}
	body, err := encrypt([]byte("When I grow up, I want to be a watermelon"),
		decode(t, "BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4"),
		decode(t, "BTBZMqHH6r4Tts7J_aSIgg"), decode(t, "DGv6ra1nlYgDCS1FRnbzlw"), private)
	if err != nil {
		t.Fatal(err)
	}
	want := "DGv6ra1nlYgDCS1FRnbzlwAAEABBBP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A_yl95bQpu6cVPTpK4Mqgkf1CXztLVBSt2Ks3oZwbuwXPXLWyouBWLVWGNWQexSgSxsj_Qulcy4a-fN"
	if encoding.EncodeToString(body) != want {
		t.Fatal("RFC 8291 ciphertext differs")
	}
}

func TestEncryptedRelayAndVAPID(t *testing.T) {
	private, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var sender *Sender
	verdict := http.StatusCreated
	relay := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 4097))
		if err != nil {
			t.Error(err)
		}
		if len(body) > 4096 || len(body) < 103 || strings.Contains(string(body), "notificationId") || r.Header.Get("Content-Encoding") != "aes128gcm" {
			t.Error("unencrypted or oversized relay payload")
		}
		parts := strings.Split(strings.TrimPrefix(r.Header.Get("Authorization"), "vapid t="), ", k=")
		if len(parts) != 2 || parts[1] != sender.PublicKey() {
			t.Error("invalid VAPID header")
			w.WriteHeader(400)
			return
		}
		jwt := strings.Split(parts[0], ".")
		if len(jwt) != 3 {
			t.Error("invalid token")
			return
		}
		var claims struct {
			Audience string `json:"aud"`
			Expires  int64  `json:"exp"`
			Subject  string `json:"sub"`
		}
		if err := json.Unmarshal(decode(t, jwt[1]), &claims); err != nil {
			t.Error(err)
		}
		if claims.Audience != "https://"+r.Host || claims.Subject != "mailto:push@example.invalid" || claims.Expires <= time.Now().Unix() || claims.Expires > time.Now().Add(24*time.Hour).Unix() {
			t.Error("invalid VAPID claims")
		}
		signature := decode(t, jwt[2])
		digest := sha256.Sum256([]byte(jwt[0] + "." + jwt[1]))
		if len(signature) != 64 || !ecdsa.Verify(&sender.key.PublicKey, digest[:], new(big.Int).SetBytes(signature[:32]), new(big.Int).SetBytes(signature[32:])) {
			t.Error("VAPID signature invalid")
		}
		w.Header().Set("Location", "https://example.invalid/must-not-follow")
		w.WriteHeader(verdict)
	}))
	defer relay.Close()
	sender, err = New(private.Bytes(), "mailto:push@example.invalid", relay.Client())
	if err != nil {
		t.Fatal(err)
	}
	receiver, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sub := domain.PushSubscription{Endpoint: relay.URL, Platform: domain.PushPlatformWebPush, P256DH: encoding.EncodeToString(receiver.PublicKey().Bytes()), AuthSecret: encoding.EncodeToString(make([]byte, 16))}
	payload := domain.PushPayload{NotificationID: "01999999-9999-7999-8999-000000000001"}
	if err := sender.Deliver(context.Background(), sub, payload); err != nil {
		t.Fatal(err)
	}
	for _, status := range []int{http.StatusInternalServerError, http.StatusTemporaryRedirect} {
		verdict = status
		if err := sender.Deliver(context.Background(), sub, payload); !errors.Is(err, errDelivery) {
			t.Fatalf("status %d: %v", status, err)
		}
	}
	verdict = http.StatusGone
	if err := sender.Deliver(context.Background(), sub, payload); !errors.Is(err, domain.ErrPushExpired) {
		t.Fatalf("gone: %v", err)
	}
	sub.P256DH = "invalid"
	if err := sender.Deliver(context.Background(), sub, payload); !errors.Is(err, domain.ErrPushInvalid) {
		t.Fatalf("bad key: %v", err)
	}
}
