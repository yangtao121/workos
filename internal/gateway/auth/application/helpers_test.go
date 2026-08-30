package application

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/asn1"
	"math/big"
	"testing"

	"github.com/yangtao121/workos/internal/gateway/auth/domain"
)

// testKey is one real P-256 browser-profile stand-in: the tests sign with
// the private key exactly like WebCrypto would, and submit the SPKI.
type testKey struct {
	priv *ecdsa.PrivateKey
	spki []byte
	hash string
}

func newTestKey(t *testing.T) *testKey {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	spki, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	_, hash, err := domain.ParseP256SPKI(spki)
	if err != nil {
		t.Fatal(err)
	}
	return &testKey{priv: priv, spki: spki, hash: hash}
}

// sign encodes the facts and emits the fixed-width raw r||s signature the
// WebCrypto path produces.
func (k *testKey) sign(t *testing.T, facts domain.ProofFacts) []byte {
	t.Helper()
	transcript, err := domain.EncodeProof(facts)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(transcript)
	der, err := ecdsa.SignASN1(rand.Reader, k.priv, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	var inner struct {
		R, S *big.Int
	}
	if _, err := asn1.Unmarshal(der, &inner); err != nil {
		t.Fatal(err)
	}
	out := make([]byte, 64)
	padRight(inner.R, out[:32])
	padRight(inner.S, out[32:])
	return out
}

func padRight(value *big.Int, dst []byte) {
	raw := value.Bytes()
	if len(raw) > len(dst) {
		raw = raw[len(raw)-len(dst):]
	}
	copy(dst[len(dst)-len(raw):], raw)
}

// signSession builds the session-purpose facts and signs them.
func (k *testKey) signSession(t *testing.T, origin string, challenge ChallengeView, deviceID string) []byte {
	t.Helper()
	return k.sign(t, domain.ProofFacts{
		PublicOrigin: origin, Purpose: domain.PurposeSession,
		ChallengeID: challenge.ID, Nonce: challenge.Nonce, DeviceID: deviceID,
		PublicKeyHash: k.hash,
	})
}
