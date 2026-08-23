package domain

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

func TestCanonicalJSONIsDeterministic(t *testing.T) {
	t.Parallel()
	value := map[string]any{
		"permissions": []any{"agent.event.watch", "agent.task.run"},
		"name":        "Notes",
		"port":        int64(8080),
		"ratio":       1.5,
		"flag":        true,
		"missing":     nil,
		"nested":      map[string]any{"z": int64(1), "a": int64(2)},
		"escaped":     "a\"b<c>&/",
	}
	first, err := CanonicalJSON(value)
	if err != nil {
		t.Fatalf("CanonicalJSON: %v", err)
	}
	second, err := CanonicalJSON(value)
	if err != nil {
		t.Fatalf("CanonicalJSON second run: %v", err)
	}
	if string(first) != string(second) {
		t.Fatalf("canonical encoding is not deterministic:\n%s\n%s", first, second)
	}
	var decoded map[string]any
	if err := json.Unmarshal(first, &decoded); err != nil {
		t.Fatalf("canonical output is not valid JSON: %v", err)
	}
	// Object keys must appear in the encoded bytes in sorted byte order.
	sortedKeys := []string{"escaped", "flag", "missing", "name", "nested", "permissions", "port", "ratio"}
	previousOffset := -1
	for _, key := range sortedKeys {
		offset := strings.Index(string(first), `"`+key+`":`)
		if offset < 0 {
			t.Fatalf("key %s missing from canonical output %s", key, first)
		}
		if previousOffset >= offset {
			t.Fatalf("canonical keys are not sorted: %s out of order in %s", key, first)
		}
		previousOffset = offset
	}
}

func TestCanonicalJSONRejectsNonJSONValues(t *testing.T) {
	t.Parallel()
	for name, value := range map[string]any{
		"unsigned int":   uint(1),
		"float32":        float32(1.5),
		"nan":            math.NaN(),
		"inf":            math.Inf(1),
		"nested invalid": map[string]any{"deep": []any{struct{}{}}},
	} {
		if _, err := CanonicalJSON(value); err == nil {
			t.Errorf("CanonicalJSON accepted %s", name)
		}
	}
}

func TestManifestDigestFormatAndSensitivity(t *testing.T) {
	t.Parallel()
	base := map[string]any{"id": "notes", "version": "1.0.0"}
	first := ManifestDigest(canonicalOrFatal(t, base))
	if len(first) != 71 || first[:7] != "sha256:" {
		t.Fatalf("unexpected digest format %q", first)
	}
	changed := map[string]any{"id": "notes", "version": "1.0.1"}
	second := ManifestDigest(canonicalOrFatal(t, changed))
	if first == second {
		t.Fatal("semantic change must change the digest")
	}
	same := ManifestDigest(canonicalOrFatal(t, map[string]any{"version": "1.0.0", "id": "notes"}))
	if first != same {
		t.Fatal("key order must not change the digest")
	}
}

func TestPermissionVocabularyIsCentral(t *testing.T) {
	t.Parallel()
	for _, capability := range []string{"agent.task.run", "artifact.read"} {
		if !KnownPermission(capability) {
			t.Errorf("expected %s to be a known capability", capability)
		}
	}
	for _, capability := range []string{"", "admin", "agent.task.run.evil", "model.access"} {
		if KnownPermission(capability) {
			t.Errorf("%s must not be a known capability", capability)
		}
	}
	all := Permissions()
	if len(all) == 0 {
		t.Fatal("vocabulary must not be empty")
	}
	for i := 1; i < len(all); i++ {
		if all[i-1] >= all[i] {
			t.Fatalf("vocabulary must be sorted: %v", all)
		}
	}
}

func canonicalOrFatal(t *testing.T, value map[string]any) []byte {
	t.Helper()
	encoded, err := CanonicalJSON(value)
	if err != nil {
		t.Fatalf("CanonicalJSON: %v", err)
	}
	return encoded
}
