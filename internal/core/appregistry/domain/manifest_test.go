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

func TestParseContainerLaunchBundleRequiresArtifact(t *testing.T) {
	t.Parallel()
	image := "localhost/workos-fixture@sha256:" + strings.Repeat("a", 64)
	source := "0198c0de-0000-7000-8000-0000000000aa"
	digest := "sha256:" + strings.Repeat("b", 64)
	artifactID := "0198c0de-0000-7000-8000-0000000000cc"
	base := map[string]any{
		"runtime": map[string]any{
			"type": "container", "image": image, "command": []any{"/app/server"}, "port": int64(8080),
		},
		"resources": map[string]any{"cpuHard": 1.0, "memoryHighMb": int64(64), "memoryMaxMb": int64(96), "pidsMax": int64(32)},
		"health":    map[string]any{"httpPath": "/health", "startupSeconds": int64(10), "restartLimit": int64(2)},
		"build": map[string]any{
			"sourceBundleId": source, "sourceDigest": digest, "baseImage": image,
			"buildCommand": []any{"go", "build"}, "testCommand": []any{"go", "test"},
			"output": map[string]any{"format": "app-bundle.v1", "directory": "dist"},
		},
	}
	if _, ok := ParseContainerLaunch(canonicalOrFatal(t, base)); ok {
		t.Fatal("bundle profile without runtime.artifact must not parse as a launch")
	}
	withArtifact := base
	runtime := withArtifact["runtime"].(map[string]any)
	runtime["artifact"] = map[string]any{"id": artifactID, "digest": digest, "format": "app-bundle.v1"}
	launch, ok := ParseContainerLaunch(canonicalOrFatal(t, withArtifact))
	if !ok || launch.Artifact == nil || launch.Artifact.ID != artifactID {
		t.Fatalf("bundle profile with artifact must parse: ok=%v launch=%+v", ok, launch)
	}
	imageOnly := map[string]any{
		"runtime": map[string]any{
			"type": "container", "image": image, "command": []any{"/workos-fixture", "serve"}, "port": int64(8080),
		},
		"resources": map[string]any{"cpuHard": 1.0, "memoryHighMb": int64(64), "memoryMaxMb": int64(96), "pidsMax": int64(32)},
		"health":    map[string]any{"httpPath": "/health", "startupSeconds": int64(10), "restartLimit": int64(2)},
	}
	if launch, ok := ParseContainerLaunch(canonicalOrFatal(t, imageOnly)); !ok || launch.Artifact != nil {
		t.Fatalf("image-only must still parse without artifact: ok=%v %+v", ok, launch)
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
