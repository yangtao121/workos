package application

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/yangtao121/workos/internal/core/appregistry/domain"
)

func TestDeriveStagedManifestBindsBundleAndPreservesImageOnly(t *testing.T) {
	image := "localhost/workos-fixture@sha256:" + strings.Repeat("a", 64)
	sourceID := "0198c0de-0000-7000-8000-0000000000aa"
	sourceDigest := "sha256:" + strings.Repeat("b", 64)
	taskID := "0198c0de-0000-7000-8000-0000000000bb"
	artifactID := "0198c0de-0000-7000-8000-0000000000cc"
	artifactDigest := "sha256:" + strings.Repeat("c", 64)
	candidate := domain.SourceBundle{ID: sourceID, Digest: sourceDigest}
	imageOnly, err := domain.CanonicalJSON(map[string]any{
		"version": "1.0.0",
		"runtime": map[string]any{"type": "container", "image": image, "command": []any{"/bin/app"}, "port": int64(8080)},
		"build": map[string]any{
			"sourceBundleId": sourceID, "sourceDigest": sourceDigest, "baseImage": image,
			"buildCommand": []any{"go", "build"}, "testCommand": []any{"go", "test"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	derived, _, err := deriveStagedManifest(imageOnly, candidate, "1.0.0", taskID, "", "", "")
	if err != nil {
		t.Fatalf("image-only derive: %v", err)
	}
	var document map[string]any
	if err := json.Unmarshal(derived, &document); err != nil {
		t.Fatal(err)
	}
	if _, ok := document["runtime"].(map[string]any)["artifact"]; ok {
		t.Fatal("image-only staged manifest must not invent runtime.artifact")
	}
	withOutput, err := domain.CanonicalJSON(map[string]any{
		"version": "1.0.0",
		"runtime": map[string]any{"type": "container", "image": image, "command": []any{"/bin/app"}, "port": int64(8080)},
		"build": map[string]any{
			"sourceBundleId": sourceID, "sourceDigest": sourceDigest, "baseImage": image,
			"buildCommand": []any{"go", "build"}, "testCommand": []any{"go", "test"},
			"output": map[string]any{"format": "app-bundle.v1", "directory": "dist"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := deriveStagedManifest(withOutput, candidate, "1.0.0", taskID, "", "", ""); err == nil {
		t.Fatal("bundle recipe without artifact id must fail closed")
	}
	bound, _, err := deriveStagedManifest(withOutput, candidate, "1.0.0", taskID, artifactID, artifactDigest, "app-bundle.v1")
	if err != nil {
		t.Fatalf("bundle derive: %v", err)
	}
	if err := json.Unmarshal(bound, &document); err != nil {
		t.Fatal(err)
	}
	artifact := document["runtime"].(map[string]any)["artifact"].(map[string]any)
	if artifact["id"] != artifactID || artifact["digest"] != artifactDigest || artifact["format"] != "app-bundle.v1" {
		t.Fatalf("staged runtime.artifact missing: %+v", artifact)
	}
	if document["runtime"].(map[string]any)["image"] != image {
		t.Fatal("staged derivation must keep the original image pin")
	}
}
