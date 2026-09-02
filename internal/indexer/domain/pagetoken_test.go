package domain

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

func TestPageTokenCodecRoundTripAndAuthentication(t *testing.T) {
	t.Parallel()
	codec, err := NewPageTokenCodec([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	token := PageToken{
		OwnerUserID: "01999999-9999-7999-8999-000000000001",
		ProjectID:   "01999999-9999-7999-8999-000000000002",
		QueryDigest: "sha256:" + strings.Repeat("a", 64), RankingVersion: RankingVersion,
		GenerationID:    "01999999-9999-7999-8999-000000000003",
		SnapshotThrough: time.Unix(10, 0).UTC(), LastScore: 1.25,
		LastSourceCreated: time.Unix(9, 0).UTC(), LastSourceID: "01999999-9999-7999-8999-000000000004",
	}
	raw, err := codec.Encode(token)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := codec.Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.ProjectID != token.ProjectID || decoded.LastScore != token.LastScore {
		t.Fatalf("decoded token drifted: %#v", decoded)
	}

	payload, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		t.Fatal(err)
	}
	payload[len(payload)/2] ^= 1
	if _, err := codec.Decode(base64.RawURLEncoding.EncodeToString(payload)); err != ErrInvalidPageToken {
		t.Fatalf("tampered token error = %v", err)
	}
	other, _ := NewPageTokenCodec([]byte("abcdef0123456789abcdef0123456789"))
	if _, err := other.Decode(raw); err != ErrInvalidPageToken {
		t.Fatalf("wrong-key token error = %v", err)
	}
}

func TestPageTokenCodecRejectsInvalidConfigurationAndFacts(t *testing.T) {
	t.Parallel()
	if _, err := NewPageTokenCodec([]byte("short")); err == nil {
		t.Fatal("short key unexpectedly accepted")
	}
	codec, _ := NewPageTokenCodec([]byte("0123456789abcdef0123456789abcdef"))
	bad := PageToken{RankingVersion: RankingVersion}
	raw, err := codec.Encode(bad)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := codec.Decode(raw); err != ErrInvalidPageToken {
		t.Fatalf("invalid facts error = %v", err)
	}
}
