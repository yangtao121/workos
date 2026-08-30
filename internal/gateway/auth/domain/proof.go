package domain

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/x509"
	"fmt"
	"math/big"
)

// Proof grammar (ADR-0007): a single binary encoding shared byte-for-byte by
// the Go services and the TypeScript browser client.
//
//	ASCII domain separator: "workos.device-proof/v1"
//	purpose byte:           0x01 pairing | 0x02 session
//	then every field:       uint32 big-endian length || raw bytes
//
// Field order is fixed. Strings already carry a canonical grammar before
// encoding; there is no map JSON, protobuf JSON, concatenation, or vendor
// JWS involved.
const ProofDomainSeparator = "workos.device-proof/v1"

// ProofVersion is carried explicitly on wire challenges. It advances only
// with a new transcript contract; clients reject every unknown value.
const ProofVersion uint32 = 1

// ProofPurpose is the leading purpose byte of every transcript.
type ProofPurpose byte

const (
	PurposePairing ProofPurpose = 0x01
	PurposeSession ProofPurpose = 0x02
)

func (p ProofPurpose) label() string {
	switch p {
	case PurposePairing:
		return "pairing"
	case PurposeSession:
		return "session"
	default:
		return "unknown"
	}
}

// ProofFacts carries every value the transcript binds. Pairing proofs
// additionally bind the ticket identifier and the TLS leaf fingerprint that
// the ticket snapshot pinned.
type ProofFacts struct {
	PublicOrigin   string
	Purpose        ProofPurpose
	ChallengeID    string
	Nonce          []byte
	DeviceID       string
	PublicKeyHash  string
	TicketID       string
	TLSFingerprint string
}

// EncodeProof serializes the versioned proof transcript. Every field is
// grammar-checked first, so an encoded transcript is always canonical.
func EncodeProof(facts ProofFacts) ([]byte, error) {
	if facts.Purpose != PurposePairing && facts.Purpose != PurposeSession {
		return nil, fmt.Errorf("%w: unknown proof purpose", ErrInvalidRequest)
	}
	if facts.PublicOrigin == "" {
		return nil, fmt.Errorf("%w: proof origin is empty", ErrInvalidRequest)
	}
	if !ValidUUIDv7(facts.ChallengeID) {
		return nil, fmt.Errorf("%w: proof challenge id grammar", ErrInvalidRequest)
	}
	if len(facts.Nonce) != SecretBytes {
		return nil, fmt.Errorf("%w: proof nonce grammar", ErrInvalidRequest)
	}
	if !ValidUUIDv7(facts.DeviceID) {
		return nil, fmt.Errorf("%w: proof device id grammar", ErrInvalidRequest)
	}
	if !ValidDigest(facts.PublicKeyHash) {
		return nil, fmt.Errorf("%w: proof key digest grammar", ErrInvalidRequest)
	}
	fields := [][]byte{
		[]byte(facts.PublicOrigin),
		[]byte(facts.Purpose.label()),
		[]byte(facts.ChallengeID),
		facts.Nonce,
		[]byte(facts.DeviceID),
		[]byte(facts.PublicKeyHash),
	}
	if facts.Purpose == PurposePairing {
		if !ValidUUIDv7(facts.TicketID) {
			return nil, fmt.Errorf("%w: proof ticket id grammar", ErrInvalidRequest)
		}
		if !ValidDigest(facts.TLSFingerprint) {
			return nil, fmt.Errorf("%w: proof fingerprint grammar", ErrInvalidRequest)
		}
		fields = append(fields, []byte(facts.TicketID), []byte(facts.TLSFingerprint))
	}
	transcript := make([]byte, 0, len(ProofDomainSeparator)+1+64*len(fields))
	transcript = append(transcript, ProofDomainSeparator...)
	transcript = append(transcript, byte(facts.Purpose))
	for _, field := range fields {
		transcript = appendUint32(transcript, uint32(len(field)))
		transcript = append(transcript, field...)
	}
	return transcript, nil
}

func appendUint32(dst []byte, value uint32) []byte {
	return append(dst, byte(value>>24), byte(value>>16), byte(value>>8), byte(value))
}

// RawSignatureBytes is the fixed ECDSA signature width: 64-byte raw r||s.
const RawSignatureBytes = 64

var (
	// ErrBadSignatureShape covers DER, short, long, zero, and out-of-range
	// signatures: one fail-closed shape, no alternate encodings accepted.
	errSignatureShape   = fmt.Errorf("%w: signature grammar", ErrInvalidRequest)
	errUnknownPublicKey = fmt.Errorf("%w: public key grammar", ErrInvalidRequest)
)

// ParseP256SPKI parses a submitted SubjectPublicKeyInfo DER and enforces the
// canonical grammar: ECDSA over P-256 only, no trailing bytes, and byte
// equality with the re-marshaled canonical form. It returns the key and its
// SHA-256 thumbprint over the canonical DER.
func ParseP256SPKI(der []byte) (*ecdsa.PublicKey, string, error) {
	if len(der) == 0 || len(der) > 256 {
		return nil, "", errUnknownPublicKey
	}
	parsed, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return nil, "", errUnknownPublicKey
	}
	key, ok := parsed.(*ecdsa.PublicKey)
	if !ok || key.Curve != elliptic.P256() {
		return nil, "", errUnknownPublicKey
	}
	canonical, err := x509.MarshalPKIXPublicKey(key)
	if err != nil {
		return nil, "", fmt.Errorf("%w: canonical SPKI marshal: %w", ErrAuthCorrupt, err)
	}
	if !equalBytes(canonical, der) {
		return nil, "", errUnknownPublicKey
	}
	digest := sha256.Sum256(der)
	return key, "sha256:" + hexEncode(digest[:]), nil
}

// VerifyProof checks the 64-byte raw r||s ECDSA P-256/SHA-256 signature over
// the encoded transcript. Every malformed encoding fails closed.
func VerifyProof(key *ecdsa.PublicKey, facts ProofFacts, signature []byte) error {
	transcript, err := EncodeProof(facts)
	if err != nil {
		return err
	}
	if len(signature) != RawSignatureBytes {
		return errSignatureShape
	}
	r := new(big.Int).SetBytes(signature[:32])
	s := new(big.Int).SetBytes(signature[32:])
	if r.Sign() == 0 || s.Sign() == 0 {
		return errSignatureShape
	}
	digest := sha256.Sum256(transcript)
	if !ecdsa.Verify(key, digest[:], r, s) {
		return ErrAuthenticationFailed
	}
	return nil
}

func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var diff byte
	for i := range a {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
}

func hexEncode(src []byte) string {
	const hexDigits = "0123456789abcdef"
	dst := make([]byte, len(src)*2)
	for i, v := range src {
		dst[i*2] = hexDigits[v>>4]
		dst[i*2+1] = hexDigits[v&0x0f]
	}
	return string(dst)
}
