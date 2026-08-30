package domain

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/asn1"
	"encoding/hex"
	"math/big"
	"strings"
	"testing"
	"time"
)

// The cross-language fixture: the TypeScript browser client and the Go
// server must encode byte-identical transcripts for these exact values.
var transcriptFixture = ProofFacts{
	PublicOrigin:   "https://workos.example",
	Purpose:        PurposePairing,
	ChallengeID:    "0198d7ea-2110-7c42-b659-c5e4d73bc301",
	Nonce:          bytesOf(0x14, 32),
	DeviceID:       "0198d7ea-2110-7c42-b659-c5e4d73bc302",
	PublicKeyHash:  "sha256:" + strings.Repeat("ab", 32),
	TicketID:       "0198d7ea-2110-7c42-b659-c5e4d73bc303",
	TLSFingerprint: "sha256:" + strings.Repeat("cd", 32),
}

func bytesOf(value byte, n int) []byte {
	raw := make([]byte, n)
	for i := range raw {
		raw[i] = value
	}
	return raw
}

// TestEncodeProofFixtureVector pins the shared binary transcript encoding:
// separator, purpose byte, then uint32-BE-length-prefixed fields, with the
// SHA-256 the TypeScript client test asserts independently.
func TestEncodeProofFixtureVector(t *testing.T) {
	transcript, err := EncodeProof(transcriptFixture)
	if err != nil {
		t.Fatal(err)
	}
	if len(transcript) != 366 {
		t.Fatalf("unexpected transcript length %d", len(transcript))
	}
	if string(transcript[:22]) != ProofDomainSeparator {
		t.Fatalf("missing domain separator prefix: %q", string(transcript[:22]))
	}
	if transcript[22] != byte(PurposePairing) {
		t.Fatalf("missing purpose byte: %x", transcript[22])
	}
	digest := sha256.Sum256(transcript)
	if got := hex.EncodeToString(digest[:]); got != "c857b751ae958ac27c6a0de976d8beb51808d59b8878ec6f85abc56215347713" {
		t.Fatalf("fixture vector drifted: %s", got)
	}
}

// TestEncodeProofGrammar pins the field grammar: any drift is rejected
// before encoding, and session transcripts are shorter than pairing ones.
func TestEncodeProofGrammar(t *testing.T) {
	session := ProofFacts{
		PublicOrigin:  "https://workos.example",
		Purpose:       PurposeSession,
		ChallengeID:   transcriptFixture.ChallengeID,
		Nonce:         transcriptFixture.Nonce,
		DeviceID:      transcriptFixture.DeviceID,
		PublicKeyHash: transcriptFixture.PublicKeyHash,
	}
	sessionTranscript, err := EncodeProof(session)
	if err != nil {
		t.Fatal(err)
	}
	pairingTranscript, err := EncodeProof(transcriptFixture)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessionTranscript) >= len(pairingTranscript) {
		t.Fatal("session transcript must omit ticket and fingerprint fields")
	}
	if sessionTranscript[22] != byte(PurposeSession) {
		t.Fatal("session purpose byte missing")
	}

	for name, mutate := range map[string]func(*ProofFacts){
		"unknown purpose":  func(f *ProofFacts) { f.Purpose = ProofPurpose(0x09) },
		"empty origin":     func(f *ProofFacts) { f.PublicOrigin = "" },
		"bad challenge id": func(f *ProofFacts) { f.ChallengeID = "not-a-uuid" },
		"short nonce":      func(f *ProofFacts) { f.Nonce = bytesOf(1, 31) },
		"bad device id":    func(f *ProofFacts) { f.DeviceID = "0198D7EA-2110-7C42-B659-C5E4D73BC302" },
		"bad key digest":   func(f *ProofFacts) { f.PublicKeyHash = "sha256:xyz" },
		"bad ticket id":    func(f *ProofFacts) { f.TicketID = "" },
		"bad fingerprint":  func(f *ProofFacts) { f.TLSFingerprint = "md5:aa" },
		"non-v7 challenge": func(f *ProofFacts) { f.ChallengeID = "0198d7ea-2110-6c42-b659-c5e4d73bc301" },
	} {
		facts := transcriptFixture
		mutate(&facts)
		if _, err := EncodeProof(facts); err == nil {
			t.Errorf("invalid transcript accepted: %s", name)
		}
	}
}

// TestParseP256SPKICanonicalGrammar covers the key grammar: P-256 SPKI only,
// canonical bytes only, with the SHA-256 thumbprint over the exact DER.
func TestParseP256SPKICanonicalGrammar(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	parsed, digest, err := ParseP256SPKI(der)
	if err != nil {
		t.Fatalf("canonical P-256 SPKI rejected: %v", err)
	}
	if parsed.Curve != elliptic.P256() || parsed.X.Cmp(key.X) != 0 {
		t.Fatal("parsed key does not match")
	}
	digestHex := sha256.Sum256(der)
	if digest != "sha256:"+hex.EncodeToString(digestHex[:]) {
		t.Fatal("thumbprint is not the SHA-256 of the canonical DER")
	}

	p384, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	p384DER, _ := x509.MarshalPKIXPublicKey(&p384.PublicKey)
	rsa := struct{}{} // placeholder to keep the rejection table aligned
	_ = rsa
	for name, bad := range map[string][]byte{
		"P-384":       p384DER,
		"empty":       {},
		"oversize":    bytesOf(0x30, 257),
		"garbage":     bytesOf(0x00, 32),
		"trailing":    append(append([]byte{}, der...), 0x00),
		"flipped bit": flipBit(der, 40),
	} {
		if _, _, err := ParseP256SPKI(bad); err == nil {
			t.Errorf("noncanonical public key accepted: %s", name)
		}
	}
}

func flipBit(src []byte, index int) []byte {
	dst := append([]byte{}, src...)
	dst[index] ^= 0x01
	return dst
}

// TestVerifyProofRawSignature covers the 64-byte raw r||s interop with
// crypto/ecdsa and the fail-closed handling of every malformed shape.
func TestVerifyProofRawSignature(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	parsed, keyHash, err := ParseP256SPKI(der)
	if err != nil {
		t.Fatal(err)
	}
	facts := transcriptFixture
	facts.PublicKeyHash = keyHash
	facts.TicketID = "0198d7ea-2110-7c42-b659-c5e4d73bc303"
	facts.TLSFingerprint = transcriptFixture.TLSFingerprint
	transcript, err := EncodeProof(facts)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(transcript)
	signature, err := ecdsa.SignASN1(rand.Reader, key, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	// VerifyASN1 output must be rejected: only raw 64-byte r||s is legal.
	if err := VerifyProof(parsed, facts, signature); err == nil {
		t.Fatal("DER signature accepted")
	}
	// Build the raw signature from the DER components via a raw sign.
	raw, err := rawSign(key, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyProof(parsed, facts, raw); err != nil {
		t.Fatalf("raw signature rejected: %v", err)
	}

	wrongKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	wrongDER, _ := x509.MarshalPKIXPublicKey(&wrongKey.PublicKey)
	wrongParsed, _, _ := ParseP256SPKI(wrongDER)
	if err := VerifyProof(wrongParsed, facts, raw); err == nil {
		t.Fatal("signature from another key accepted")
	}

	tampered := append([]byte{}, raw...)
	tampered[10] ^= 0x01
	if err := VerifyProof(parsed, facts, tampered); err == nil {
		t.Fatal("tampered signature accepted")
	}
	for name, bad := range map[string][]byte{
		"empty":     {},
		"short":     bytesOf(1, 63),
		"long":      bytesOf(1, 65),
		"zero r":    zeroComponent(raw, true),
		"zero s":    zeroComponent(raw, false),
		"max range": bytesOf(0xff, 64),
	} {
		if err := VerifyProof(parsed, facts, bad); err == nil {
			t.Errorf("malformed signature accepted: %s", name)
		}
	}
	// A fact change must invalidate the signature.
	other := facts
	other.Nonce = bytesOf(0x15, 32)
	if err := VerifyProof(parsed, other, raw); err == nil {
		t.Fatal("signature valid over modified transcript facts")
	}
}

// rawSign re-encodes a standard DER signature as the fixed-width raw r||s
// form the browser emits, mirroring WebCrypto's output shape.
func rawSign(key *ecdsa.PrivateKey, digest []byte) ([]byte, error) {
	sig, err := ecdsa.SignASN1(rand.Reader, key, digest)
	if err != nil {
		return nil, err
	}
	var inner struct {
		R, S *big.Int
	}
	if _, err := asn1.Unmarshal(sig, &inner); err != nil {
		return nil, err
	}
	out := make([]byte, 64)
	rightPadded(inner.R, out[:32])
	rightPadded(inner.S, out[32:])
	return out, nil
}

func rightPadded(value *big.Int, dst []byte) {
	raw := value.Bytes()
	if len(raw) > len(dst) {
		raw = raw[len(raw)-len(dst):]
	}
	copy(dst[len(dst)-len(raw):], raw)
}

func zeroComponent(src []byte, r bool) []byte {
	dst := append([]byte{}, src...)
	if r {
		for i := 0; i < 32; i++ {
			dst[i] = 0
		}
	} else {
		for i := 32; i < 64; i++ {
			dst[i] = 0
		}
	}
	return dst
}

// TestSecretGrammar pins the strict secret grammar and the domain
// separation of the two hash helpers.
func TestSecretGrammar(t *testing.T) {
	raw := bytesOf(7, SecretBytes)
	encoded, err := EncodeSecret(raw)
	if err != nil || len(encoded) != 43 || strings.Contains(encoded, "=") {
		t.Fatalf("secret encoding grammar: %q err=%v", encoded, err)
	}
	decoded, err := ParsePairingSecret(encoded)
	if err != nil || len(decoded) != SecretBytes {
		t.Fatalf("secret parse failed: %v", err)
	}
	for _, bad := range []string{"", "short", encoded + "=", strings.Repeat("A", 42), strings.Repeat("A", 44), strings.Repeat("+/", 21) + "A"} {
		if _, err := ParsePairingSecret(bad); err == nil {
			t.Errorf("invalid secret grammar accepted: %q", bad)
		}
	}
	ticketHash := HashPairingSecret(raw)
	sessionHash := HashSessionToken(raw)
	if ticketHash == sessionHash {
		t.Fatal("domain-separated hashes collide across kinds")
	}
	if ticketHash == HashPairingSecret(bytesOf(8, SecretBytes)) {
		t.Fatal("different secrets share one digest")
	}
}

// TestDeviceNameGrammar pins the bounded name grammar.
func TestDeviceNameGrammar(t *testing.T) {
	name, err := ValidateDeviceName("  Laptop –研发机 \u2009")
	if err != nil || name != "Laptop –研发机" {
		t.Fatalf("trim behavior: %q err=%v", name, err)
	}
	if _, err := ValidateDeviceName("   "); err == nil {
		t.Fatal("blank name accepted")
	}
	if _, err := ValidateDeviceName(strings.Repeat("字", 81)); err == nil {
		t.Fatal("over-long name accepted")
	}
	if _, err := ValidateDeviceName("ok\u0007bell"); err == nil {
		t.Fatal("control character accepted")
	}
	if _, err := ValidateDeviceName("ok\x7f"); err == nil {
		t.Fatal("DEL accepted")
	}
}

// TestParseDeviceClass pins the bounded class grammar.
func TestParseDeviceClass(t *testing.T) {
	for _, valid := range []string{"desktop", "tablet", "foldable", "phone"} {
		if _, err := ParseDeviceClass(valid); err != nil {
			t.Errorf("valid class rejected: %s", valid)
		}
	}
	for _, invalid := range []string{"", "DESKTOP", "watch", "unspecified"} {
		if _, err := ParseDeviceClass(invalid); err == nil {
			t.Errorf("invalid class accepted: %q", invalid)
		}
	}
}

// TestSessionExpirySemantics pins absolute expiry and revocation behavior.
func TestSessionExpirySemantics(t *testing.T) {
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	session := DeviceSession{ExpiresAt: now.Add(time.Hour)}
	if !session.Active(now) || session.Active(now.Add(2*time.Hour)) {
		t.Fatal("absolute expiry semantics broken")
	}
	revoked := now.Add(30 * time.Minute)
	session.RevokedAt = &revoked
	if session.Active(now.Add(time.Hour)) {
		t.Fatal("revoked session still active")
	}
}
