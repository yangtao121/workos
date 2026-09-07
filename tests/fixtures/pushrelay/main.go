// Test-only TLS receiver for the Core outbox → Chromium push acceptance gate.
// It owns fresh receiver keys; it is not a production relay or a seventh service.
package main

import (
	"bytes"
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
	"flag"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

var encoding = base64.RawURLEncoding.Strict()
var errInvalid = errors.New("invalid encrypted push fixture request")

type subscription struct {
	Endpoint   string `json:"endpoint"`
	P256DH     string `json:"p256dh"`
	AuthSecret string `json:"authSecret"`
	PublicKey  string `json:"publicKey"`
}

type delivery struct {
	Payload     string `json:"payload"`
	Attempts    int    `json:"attempts"`
	Ciphertexts int    `json:"ciphertexts"`
	hashes      map[[32]byte]bool
}

type relay struct {
	mu         sync.Mutex
	receiver   *ecdh.PrivateKey
	auth       []byte
	sub        subscription
	Allow      bool                 `json:"allow"`
	Invalid    int                  `json:"invalid"`
	Deliveries map[string]*delivery `json:"deliveries"`
	Accepted   map[string]string    `json:"accepted"`
}

func main() {
	dir := flag.String("dir", "", "owned fixture directory")
	address := flag.String("address", "", "loopback TLS address")
	initOnly := flag.Bool("init", false, "write fresh test keys and subscription")
	flag.Parse()
	host, _, err := net.SplitHostPort(*address)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() || *dir == "" {
		fmt.Fprintln(os.Stderr, "push relay requires a fixture directory and loopback address")
		os.Exit(1)
	}
	if *initOnly {
		err = initialize(*dir, *address)
	} else {
		err = serve(*dir)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "push relay fixture failed:", err)
		os.Exit(1)
	}
}

func initialize(dir, address string) error {
	for _, name := range []string{"core", "relay", "public"} {
		if err := os.MkdirAll(filepath.Join(dir, name), 0o700); err != nil {
			return err
		}
	}
	vapid, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	receiver, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	auth := make([]byte, 16)
	if _, err := rand.Read(auth); err != nil {
		return err
	}
	sub := subscription{"https://" + address + "/send", encoding.EncodeToString(receiver.PublicKey().Bytes()), encoding.EncodeToString(auth), encoding.EncodeToString(vapid.PublicKey().Bytes())}
	data, err := json.Marshal(sub)
	if err != nil {
		return err
	}
	for file, data := range map[string][]byte{"core/push.key": vapid.Bytes(), "relay/receiver.key": receiver.Bytes(), "relay/auth.key": auth, "public/subscription.json": data} {
		if err := os.WriteFile(filepath.Join(dir, file), data, 0o600); err != nil {
			return err
		}
	}
	return nil
}

func serve(dir string) error {
	data, err := os.ReadFile(filepath.Join(dir, "public/subscription.json"))
	if err != nil {
		return err
	}
	r := &relay{Deliveries: map[string]*delivery{}, Accepted: map[string]string{}}
	if err := json.Unmarshal(data, &r.sub); err != nil {
		return err
	}
	raw, err := os.ReadFile(filepath.Join(dir, "relay/receiver.key"))
	if err != nil {
		return err
	}
	r.receiver, err = ecdh.P256().NewPrivateKey(raw)
	if err != nil {
		return err
	}
	r.auth, err = os.ReadFile(filepath.Join(dir, "relay/auth.key"))
	if err != nil {
		return err
	}
	if len(r.auth) != 16 {
		return errInvalid
	}
	address := strings.TrimSuffix(strings.TrimPrefix(r.sub.Endpoint, "https://"), "/send")
	server := &http.Server{Addr: address, Handler: r, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, MaxHeaderBytes: 8192}
	return server.ListenAndServeTLS(filepath.Join(dir, "tls/leaf.crt"), filepath.Join(dir, "tls/leaf.key"))
}

func (r *relay) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()
	w.Header().Set("Cache-Control", "no-store")
	switch request.Method + " " + request.URL.Path {
	case "GET /state":
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(r)
	case "POST /allow":
		r.Allow = true
		w.WriteHeader(http.StatusNoContent)
	case "POST /send":
		body, err := io.ReadAll(io.LimitReader(request.Body, 4097))
		if err != nil || request.Header.Get("Content-Encoding") != "aes128gcm" || request.Header.Get("TTL") != "60" || r.verifyVAPID(request) != nil {
			r.Invalid++
			http.Error(w, "invalid push", http.StatusBadRequest)
			return
		}
		plain, err := decrypt(r.receiver, r.auth, body)
		if err != nil {
			r.Invalid++
			http.Error(w, "invalid push", http.StatusBadRequest)
			return
		}
		var payload map[string]string
		if json.Unmarshal(plain, &payload) != nil || len(payload) != 1 {
			r.Invalid++
			http.Error(w, "invalid payload", http.StatusBadRequest)
			return
		}
		id := payload["notificationId"]
		parsed, err := uuid.Parse(id)
		if err != nil || parsed.Version() != 7 || parsed.String() != id {
			r.Invalid++
			http.Error(w, "invalid notification", http.StatusBadRequest)
			return
		}
		if len(r.Deliveries) >= 128 {
			http.Error(w, "fixture limit", http.StatusTooManyRequests)
			return
		}
		current := r.Deliveries[id]
		if current == nil {
			current = &delivery{Payload: string(plain), hashes: map[[32]byte]bool{}}
			r.Deliveries[id] = current
		}
		current.Attempts++
		current.hashes[sha256.Sum256(body)] = true
		current.Ciphertexts = len(current.hashes)
		if !r.Allow {
			http.Error(w, "fixture relay outage", http.StatusServiceUnavailable)
			return
		}
		r.Accepted[id] = string(plain)
		w.WriteHeader(http.StatusCreated)
	default:
		http.NotFound(w, request)
	}
}

func (r *relay) verifyVAPID(request *http.Request) error {
	header := request.Header.Get("Authorization")
	if !strings.HasPrefix(header, "vapid t=") {
		return errInvalid
	}
	parts := strings.Split(strings.TrimPrefix(header, "vapid t="), ", k=")
	if len(parts) != 2 || parts[1] != r.sub.PublicKey {
		return errInvalid
	}
	jwt := strings.Split(parts[0], ".")
	if len(jwt) != 3 {
		return errInvalid
	}
	raw, err := encoding.DecodeString(jwt[0])
	if err != nil || string(raw) != `{"typ":"JWT","alg":"ES256"}` {
		return errInvalid
	}
	raw, err = encoding.DecodeString(jwt[1])
	if err != nil {
		return errInvalid
	}
	var claims struct {
		Audience string `json:"aud"`
		Subject  string `json:"sub"`
		Expires  int64  `json:"exp"`
	}
	if json.Unmarshal(raw, &claims) != nil || claims.Audience != strings.TrimSuffix(r.sub.Endpoint, "/send") || claims.Subject != "mailto:push@example.invalid" || claims.Expires <= time.Now().Unix() || claims.Expires > time.Now().Add(24*time.Hour).Unix() {
		return errInvalid
	}
	signature, err := encoding.DecodeString(jwt[2])
	if err != nil || len(signature) != 64 {
		return errInvalid
	}
	public, err := encoding.DecodeString(r.sub.PublicKey)
	if err != nil {
		return errInvalid
	}
	x, y := elliptic.Unmarshal(elliptic.P256(), public)
	if x == nil {
		return errInvalid
	}
	digest := sha256.Sum256([]byte(jwt[0] + "." + jwt[1]))
	if !ecdsa.Verify(&ecdsa.PublicKey{Curve: elliptic.P256(), X: x, Y: y}, digest[:], new(big.Int).SetBytes(signature[:32]), new(big.Int).SetBytes(signature[32:])) {
		return errInvalid
	}
	return nil
}

// Receiver-side RFC 8291 derivation, checked against the published ciphertext.
func decrypt(receiver *ecdh.PrivateKey, auth, body []byte) ([]byte, error) {
	if len(body) < 103 || len(body) > 4096 || len(auth) != 16 || binary.BigEndian.Uint32(body[16:20]) != 4096 || body[20] != 65 {
		return nil, errInvalid
	}
	sender, err := ecdh.P256().NewPublicKey(body[21:86])
	if err != nil {
		return nil, errInvalid
	}
	shared, err := receiver.ECDH(sender)
	if err != nil {
		return nil, errInvalid
	}
	hmac256 := func(key, message []byte) []byte {
		h := hmac.New(sha256.New, key)
		_, _ = h.Write(message)
		return h.Sum(nil)
	}
	info := append([]byte("WebPush: info\x00"), receiver.PublicKey().Bytes()...)
	info = append(info, sender.Bytes()...)
	combined := hmac256(hmac256(auth, shared), append(info, 1))
	prk := hmac256(body[:16], combined)
	key := hmac256(prk, []byte("Content-Encoding: aes128gcm\x00\x01"))[:16]
	nonce := hmac256(prk, []byte("Content-Encoding: nonce\x00\x01"))[:12]
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, errInvalid
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, errInvalid
	}
	plain, err := aead.Open(nil, nonce, body[86:], nil)
	if err != nil {
		return nil, errInvalid
	}
	plain = bytes.TrimRight(plain, "\x00")
	if len(plain) == 0 || plain[len(plain)-1] != 2 {
		return nil, errInvalid
	}
	return plain[:len(plain)-1], nil
}
