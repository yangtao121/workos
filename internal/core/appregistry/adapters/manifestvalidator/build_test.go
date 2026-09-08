package manifestvalidator

import (
	"strings"
	"testing"
)

const fixtureBuild = `build:
  sourceBundleId: 01999999-9999-7999-8999-999999999991
  sourceDigest: sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
  baseImage: localhost/toolchain@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
  buildCommand: ["go", "build", "-o", "app", "."]
  testCommand: ["go", "test", "./..."]
`

func TestBuildRecipeIsValidatedAndCoveredByManifestDigest(t *testing.T) {
	validator, err := New()
	if err != nil {
		t.Fatal(err)
	}
	first, violations := validator.Validate([]byte(validUserManifest + fixtureBuild))
	if len(violations) != 0 || first.Build == nil || first.Build.SourceBundleID != "01999999-9999-7999-8999-999999999991" || strings.Join(first.Build.TestCommand, " ") != "go test ./..." {
		t.Fatalf("valid build rejected: %v", violations)
	}
	second, violations := validator.Validate([]byte(validUserManifest + strings.Replace(fixtureBuild, `"test"`, `"vet"`, 1)))
	if len(violations) != 0 || second.Digest == first.Digest {
		t.Fatalf("test command not pinned by digest: %v", violations)
	}
	for _, pair := range [][2]string{
		{"01999999-9999-7999-8999-999999999991", "01999999-9999-4999-8999-999999999991"},
		{"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "sha256:bad"},
		{"localhost/toolchain@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "localhost/toolchain:latest"},
		{`testCommand: ["go", "test", "./..."]`, `testCommand: "true"`},
		{`testCommand: ["go", "test", "./..."]`, `testCommand: []`},
		{`testCommand: ["go", "test", "./..."]`, `testCommand: ["go\u0000"]`},
	} {
		if _, violations := validator.Validate([]byte(validUserManifest + strings.Replace(fixtureBuild, pair[0], pair[1], 1))); len(violations) == 0 {
			t.Fatalf("invalid recipe accepted: %q", pair[0])
		}
	}
	if _, violations := validator.Validate([]byte(validUserManifest + fixtureBuild + "  network: host\n")); len(violations) == 0 {
		t.Fatal("unrecognized build authority accepted")
	}
}
