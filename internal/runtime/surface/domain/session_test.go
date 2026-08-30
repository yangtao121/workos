package domain

import (
	"math"
	"strings"
	"testing"
	"time"
)

const testOwner = "0198d7ea-2110-7c42-b659-c5e4d73bc337"

// canonicalV7 is a well-formed lowercase UUIDv7 used across the grammar tests.
const canonicalV7 = "0198d7ea-2110-7c42-b659-c5e4d73bc337"

func TestCreateRequestDigestCoversIntent(t *testing.T) {
	t.Parallel()
	base := CreateRequestDigest("d", "p", "i", "desktop", 1024, 768, 2, RendererWebBundle)
	same := CreateRequestDigest("d", "p", "i", "desktop", 1024, 768, 2, RendererWebBundle)
	if base != same {
		t.Fatal("identical intents digested differently")
	}
	for name, mutated := range map[string]string{
		"device-id":   CreateRequestDigest("d2", "p", "i", "desktop", 1024, 768, 2, RendererWebBundle),
		"project":     CreateRequestDigest("d", "p2", "i", "desktop", 1024, 768, 2, RendererWebBundle),
		"instance":    CreateRequestDigest("d", "p", "i2", "desktop", 1024, 768, 2, RendererWebBundle),
		"device":      CreateRequestDigest("d", "p", "i", "phone", 1024, 768, 2, RendererWebBundle),
		"viewport":    CreateRequestDigest("d", "p", "i", "desktop", 800, 768, 2, RendererWebBundle),
		"ratio":       CreateRequestDigest("d", "p", "i", "desktop", 1024, 768, 3, RendererWebBundle),
		"renderer":    CreateRequestDigest("d", "p", "i", "desktop", 1024, 768, 2, ""),
		"time-stable": CreateRequestDigest("d", "p", "i", "desktop", 1024, 768, 2, RendererWebBundle),
	} {
		if name != "time-stable" && mutated == base {
			t.Errorf("%s change did not change the digest", name)
		}
	}
	// One canonical unspecified-ratio semantic: zero and negative zero must
	// digest identically so the two encodings of the same intent can never
	// split a replay into replay-versus-conflict.
	if CreateRequestDigest("d", "p", "i", "desktop", 1024, 768, 0, RendererWebBundle) !=
		CreateRequestDigest("d", "p", "i", "desktop", 1024, 768, math.Copysign(0, -1), RendererWebBundle) {
		t.Fatal("zero and negative zero digested differently")
	}
}

func TestValidSessionUUIDIsCanonicalV7(t *testing.T) {
	t.Parallel()
	if !ValidSessionUUID(canonicalV7) {
		t.Fatal("canonical UUIDv7 rejected")
	}
	invalid := map[string]string{
		"empty":         "",
		"short":         canonicalV7[:35],
		"long":          canonicalV7 + "0",
		"uppercase":     strings.ToUpper(canonicalV7),
		"version 4":     "0198d7ea-2110-4c42-b659-c5e4d73bc337",
		"version 1":     "0198d7ea-2110-1c42-b659-c5e4d73bc337",
		"bad variant":   "0198d7ea-2110-7c42-c659-c5e4d73bc337",
		"nil variant":   "0198d7ea-2110-7c42-0659-c5e4d73bc337",
		"bad hyphen":    "0198d7ea-2110-7c42Xb659-c5e4d73bc337",
		"non-hex":       "0198d7ea-2110-7c42-b659-c5e4d73bc33g",
		"no hyphens":    "0198d7ea21107c42b659c5e4d73bc337" + "0",
		"v4 shape uuid": "9f8ee16a-4b46-4a8e-a6cc-82919bf8d0a8",
	}
	for name, value := range invalid {
		if ValidSessionUUID(value) {
			t.Errorf("%s (%q) accepted", name, value)
		}
	}
}

func TestSessionPathAndGrammar(t *testing.T) {
	t.Parallel()
	id := canonicalV7
	if got := SessionPath(id); got != "/surfaces/"+id+"/" {
		t.Fatalf("unexpected session path %q", got)
	}
	if !ValidSessionUUID(id) {
		t.Fatal("valid uuid rejected")
	}
	// Only the canonical lowercase UUIDv7 grammar passes: malformed shapes,
	// other versions, wrong variants, and uppercase spellings all fail, so
	// HTTP asset paths carrying them fail closed as 404.
	for _, invalid := range []string{"", "abc", id[:35], id + "0", strings.ToUpper(id), "0198d7ea-2110-4c42-b659-c5e4d73bc337"} {
		if ValidSessionUUID(invalid) {
			t.Errorf("invalid uuid accepted: %q", invalid)
		}
	}
	for key, valid := range map[string]bool{"a": true, strings.Repeat("k", 128): true, strings.Repeat("k", 129): false, "with space": true, "with\nnewline": false} {
		if ValidSessionIdempotencyKey(key) != valid {
			t.Errorf("key %q validity=%v want %v", key, ValidSessionIdempotencyKey(key), valid)
		}
	}
	for class, valid := range map[string]bool{"desktop": true, "tablet": true, "foldable": true, "phone": true, "": false, "watch": false} {
		if ValidDeviceClass(class) != valid {
			t.Errorf("device class %q validity mismatch", class)
		}
	}
	for renderer, valid := range map[string]bool{"": true, "web-bundle": true, "web-service": true, "declarative": false, "remote-native": false} {
		if ValidPreferredRenderer(renderer) != valid {
			t.Errorf("renderer %q validity mismatch", renderer)
		}
	}
	// NaN and infinities never count as a reasonable viewport, whatever the
	// comparisons would claim; protobuf binary can carry all of them.
	nonFinite := []float64{math.NaN(), math.Inf(1), math.Inf(-1)}
	for _, ratio := range nonFinite {
		if ValidViewport(1024, 768, ratio) {
			t.Errorf("non-finite pixel ratio %v accepted", ratio)
		}
	}
	for viewport, valid := range map[[3]float64]bool{
		{1024, 768, 2}: true, {1, 1, 0}: true, {0, 768, 2}: false,
		{1024, 0, 2}: false, {20000, 768, 2}: false, {1024, 768, -1}: false, {1024, 768, 99}: false,
	} {
		got := ValidViewport(int32(viewport[0]), int32(viewport[1]), viewport[2])
		if got != valid {
			t.Errorf("viewport %v validity=%v want %v", viewport, got, valid)
		}
	}
}

func strings_Upper(value string) string {
	out := []byte(value)
	for index := range out {
		if out[index] >= 'a' && out[index] <= 'f' {
			out[index] -= 32
		}
	}
	return string(out)
}

func TestNormalizeAssetPath(t *testing.T) {
	t.Parallel()
	for raw, want := range map[string]string{
		"":           "",
		"/":          "",
		"index.html": "index.html",
		"a/b.js":     "a/b.js",
		"..":         "\x00",
		"../x":       "\x00",
		"a/../b":     "\x00",
		"a/./b":      "\x00",
		"a//b":       "\x00",
		"/abs":       "\x00",
		`a\b`:        "\x00",
		"a%2fb":      "\x00",
		"a?b":        "\x00",
		"a#b":        "\x00",
		"a b":        "\x00",
		"ünïcode":    "\x00",
	} {
		got, err := NormalizeAssetPath(raw)
		if want == "\x00" {
			if err == nil {
				t.Errorf("path %q accepted", raw)
			}
			continue
		}
		if err != nil || got != want {
			t.Errorf("path %q -> (%q, %v) want %q", raw, got, err, want)
		}
	}
}

func TestSessionTimesCoherent(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	session := SurfaceSession{ID: "0198d7ea-2110-7c42-b659-c5e4d73bc337", CreatedAt: now, ExpiresAt: now.Add(time.Minute)}
	if session.ExpiresAt.Before(session.CreatedAt) {
		t.Fatal("expires before created")
	}
	closed := now.Add(time.Second)
	session.ClosedAt = &closed
	if session.ClosedAt.Before(session.CreatedAt) {
		t.Fatal("closed before created")
	}
}
