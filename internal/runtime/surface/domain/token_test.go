package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

func TestNewBridgeTokenShapeAndEntropy(t *testing.T) {
	t.Parallel()
	seen := make(map[string]struct{}, 64)
	for index := 0; index < 64; index += 1 {
		token, err := NewBridgeToken()
		if err != nil {
			t.Fatalf("mint failed: %v", err)
		}
		// 32 raw bytes -> exactly 43 unpadded base64url characters.
		if len(token) != 43 || strings.ContainsAny(token, "+/=") {
			t.Fatalf("token shape invalid: %q", token)
		}
		if _, dup := seen[token]; dup {
			t.Fatal("token repetition within 64 mints (entropy failure)")
		}
		seen[token] = struct{}{}
	}
}

func TestValidBridgeTokenGrammar(t *testing.T) {
	t.Parallel()
	token, err := NewBridgeToken()
	if err != nil {
		t.Fatal(err)
	}
	if !ValidBridgeToken(token) {
		t.Fatal("minted token rejected by grammar")
	}
	for _, invalid := range []string{"", token + "x", token[:42], token[:42] + "@", token + "="} {
		if ValidBridgeToken(invalid) {
			t.Fatalf("invalid token accepted: %q", invalid)
		}
	}
}

func TestHashBridgeTokenAndConstantTimeMatch(t *testing.T) {
	t.Parallel()
	token, err := NewBridgeToken()
	if err != nil {
		t.Fatal(err)
	}
	digest := HashBridgeToken(token)
	if digest != "sha256:"+lowerHex64(t, token) {
		t.Fatalf("digest is not sha256 of the token: %s", digest)
	}
	if !BridgeTokenMatches(digest, HashBridgeToken(token)) {
		t.Fatal("same token must match")
	}
	other, _ := NewBridgeToken()
	if BridgeTokenMatches(digest, HashBridgeToken(other)) {
		t.Fatal("different token must not match")
	}
	// Empty stored digest (no credential minted / cleared on close) never
	// matches anything, and length mismatches short-circuit safely.
	if BridgeTokenMatches("", HashBridgeToken(token)) {
		t.Fatal("empty stored digest must never match")
	}
	if BridgeTokenMatches("sha256:abc", HashBridgeToken(token)) {
		t.Fatal("short digest must never match")
	}
}

func TestEffectiveBridgeCapabilitiesIntersection(t *testing.T) {
	t.Parallel()
	effective := EffectiveBridgeCapabilities([]string{
		"agent.task.run", "artifact.read", "agent.event.watch", "project.read",
	}, false)
	if len(effective) != 2 || effective[0] != "agent.event.watch" || effective[1] != "agent.task.run" {
		t.Fatalf("unexpected effective list: %v", effective)
	}
	// Unimplemented-but-granted capabilities never become effective.
	if effective := EffectiveBridgeCapabilities([]string{"artifact.write", "knowledge.read"}, false); len(effective) != 0 {
		t.Fatalf("unimplemented capabilities leaked: %v", effective)
	}
	if !BridgeCapabilityGranted(effective, "agent.task.run") {
		t.Fatal("granted check failed for member")
	}
	if BridgeCapabilityGranted(effective, "artifact.read") {
		t.Fatal("granted check passed for non-member")
	}
	if BridgeCapabilityGranted(nil, "agent.task.run") {
		t.Fatal("nil capability list grants nothing")
	}
}

func lowerHex64(t *testing.T, value string) string {
	t.Helper()
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// TestEffectiveBridgeCapabilitiesKnowledgeSearch pins the ADR-0013 rules:
// knowledge.search is negotiated only from a real `knowledge.read` grant AND
// a configured indexer adapter, and the grant name itself never appears.
func TestEffectiveBridgeCapabilitiesKnowledgeSearch(t *testing.T) {
	granted := []string{"knowledge.read"}
	if effective := EffectiveBridgeCapabilities(granted, false); len(effective) != 0 {
		t.Fatalf("knowledge.read without indexer negotiated %v", effective)
	}
	if effective := EffectiveBridgeCapabilities(granted, true); len(effective) != 1 || effective[0] != "knowledge.search" {
		t.Fatalf("knowledge.read with indexer = %v, want [knowledge.search]", effective)
	}
	if effective := EffectiveBridgeCapabilities([]string{"agent.task.run"}, true); len(effective) != 1 || effective[0] != "agent.task.run" {
		t.Fatalf("agent grant must not negotiate knowledge.search: %v", effective)
	}
}
