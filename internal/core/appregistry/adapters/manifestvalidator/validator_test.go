package manifestvalidator

import (
	"strings"
	"testing"

	"github.com/yangtao121/workos/internal/core/appregistry/domain"
	"github.com/yangtao121/workos/schemas"
)

const validUserManifest = `apiVersion: workos.app/v1
id: note-taker
name: Note Taker
version: 1.10.0
scope: user
runtime:
  type: container
  command: ["./serve"]
  port: 8080
surfaces:
  - id: main
    renderer: web-bundle
    route: /notes
permissions: [artifact.read, agent.task.run]
resources:
  limits:
    memory: 256
health:
  interval: 30
maintainer:
  name: Example
`

const validProjectManifest = `apiVersion: workos.app/v1
id: project-kanban
name: Project Kanban
version: 0.9.0
scope: project
runtime:
  type: background-service
  command: ["python", "board.py"]
surfaces:
  - id: board
    renderer: declarative
    adaptive: true
permissions: [project.read]
resources: {}
health: {}
maintainer: {}
`

func newValidator(t *testing.T) *Validator {
	t.Helper()
	validator, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return validator
}

func TestValidatorAcceptsCanonicalManifests(t *testing.T) {
	t.Parallel()
	validator := newValidator(t)
	for name, manifest := range map[string]string{"user": validUserManifest, "project": validProjectManifest} {
		result, violations := validator.Validate([]byte(manifest))
		if len(violations) != 0 {
			t.Fatalf("%s manifest rejected: %v", name, violations)
		}
		switch name {
		case "user":
			if result.ID != "note-taker" || result.Name != "Note Taker" || result.Version != "1.10.0" ||
				result.Scope != domain.ScopeUser || len(result.Permissions) != 2 {
				t.Fatalf("unexpected user projection: %#v", result)
			}
		case "project":
			if result.ID != "project-kanban" || result.Scope != domain.ScopeProject || result.Version != "0.9.0" {
				t.Fatalf("unexpected project projection: %#v", result)
			}
		}
		if !strings.HasPrefix(result.Digest, "sha256:") || len(result.CanonicalJSON) == 0 {
			t.Fatalf("digest/canonical missing: %#v", result)
		}
	}
}

func TestValidatorLoadsWithTheCanonicalSchemaFile(t *testing.T) {
	t.Parallel()
	// The embedded bytes must be the repository file at its canonical path;
	// a second rule source must never appear next to it.
	if !strings.Contains(string(schemas.AppManifestV1), `"workos.app/v1"`) {
		t.Fatal("embedded schema is not the canonical app manifest v1 schema")
	}
	if _, err := New(); err != nil {
		t.Fatalf("compile canonical schema: %v", err)
	}
}

func TestValidatorReportsSchemaViolationsSafely(t *testing.T) {
	t.Parallel()
	validator := newValidator(t)
	cases := []struct {
		name     string
		yaml     string
		wantPath string
	}{
		{
			name:     "wrong api version",
			yaml:     strings.Replace(validUserManifest, "workos.app/v1", "workos.app/v2", 1),
			wantPath: "/apiVersion",
		},
		{
			name:     "unknown root field",
			yaml:     validUserManifest + "unexpectedField: 1\n",
			wantPath: "",
		},
		{
			name:     "invalid app id",
			yaml:     strings.Replace(validUserManifest, "id: note-taker", "id: 1-Bad_ID", 1),
			wantPath: "/id",
		},
		{
			name:     "invalid semver",
			yaml:     strings.Replace(validUserManifest, "version: 1.10.0", "version: v1.10", 1),
			wantPath: "/version",
		},
		{
			name:     "empty prerelease identifier",
			yaml:     strings.Replace(validUserManifest, "version: 1.10.0", "version: 1.10.0-.rc1", 1),
			wantPath: "/version",
		},
		{
			name:     "invalid runtime type",
			yaml:     strings.Replace(validUserManifest, "type: container", "type: hypervisor", 1),
			wantPath: "/runtime/type",
		},
		{
			name:     "runtime unknown field",
			yaml:     strings.Replace(validUserManifest, "  port: 8080", "  port: 8080\n  privileged: true", 1),
			wantPath: "/runtime",
		},
		{
			name:     "invalid surface renderer",
			yaml:     strings.Replace(validUserManifest, "renderer: web-bundle", "renderer: native", 1),
			wantPath: "/surfaces/0/renderer",
		},
		{
			name:     "invalid permission pattern",
			yaml:     strings.Replace(validUserManifest, "permissions: [artifact.read, agent.task.run]", "permissions: [Artifact.Read]", 1),
			wantPath: "/permissions/0",
		},
		{
			name:     "duplicate permissions",
			yaml:     strings.Replace(validUserManifest, "permissions: [artifact.read, agent.task.run]", "permissions: [artifact.read, artifact.read]", 1),
			wantPath: "/permissions",
		},
		{
			name:     "name too long",
			yaml:     strings.Replace(validUserManifest, "name: Note Taker", "name: "+strings.Repeat("n", 81), 1),
			wantPath: "/name",
		},
		{
			name:     "port out of range",
			yaml:     strings.Replace(validUserManifest, "port: 8080", "port: 70000", 1),
			wantPath: "/runtime/port",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			result, violations := validator.Validate([]byte(testCase.yaml))
			if len(violations) == 0 {
				t.Fatalf("invalid manifest accepted: %#v", result)
			}
			if testCase.wantPath != "" && !violationMentions(violations, testCase.wantPath) {
				t.Fatalf("expected a violation at %s, got %v", testCase.wantPath, violations)
			}
			for _, violation := range violations {
				if len([]rune(violation)) > maxViolationRunes {
					t.Fatalf("violation exceeds length limit: %q", violation)
				}
			}
		})
	}
}

func TestValidatorRejectsUnsafeYAMLStructure(t *testing.T) {
	t.Parallel()
	validator := newValidator(t)
	cases := map[string]string{
		"empty document":     "",
		"multiple documents": validUserManifest + "---\nid: second\n",
		"non-map root":       "- one\n- two\n",
		"duplicate key":      validUserManifest + "name: Replaced\n",
		"anchor and alias":   validUserManifest + "extend:\n  base: &base\n    a: 1\n  use: *base\n",
		"non-string key":     validUserManifest + "42: answer\n",
		"merge key":          validUserManifest + "extend:\n  <<: {a: 1}\n",
		"custom tag":         validUserManifest + "secret: !Secret abc\n",
		"timestamp value":    validUserManifest + "stamp: 2023-01-01\n",
		"binary value":       validUserManifest + "blob: !!binary aGVsbG8=\n",
		"control character":  validUserManifest + "note: \"bad\\u0001char\"\n",
		"oversize":           validUserManifest + "pad: " + strings.Repeat("x", 256*1024) + "\n",
		"deep nesting":       validUserManifest + "deep: " + strings.Repeat("{a: ", 40) + "1" + strings.Repeat("}", 40) + "\n",
	}
	for name, yaml := range cases {
		t.Run(name, func(t *testing.T) {
			result, violations := validator.Validate([]byte(yaml))
			if len(violations) == 0 {
				t.Fatalf("unsafe YAML accepted: %#v", result)
			}
			if result.ID != "" || result.Digest != "" {
				t.Fatalf("unsafe YAML must not produce a manifest: %#v", result)
			}
		})
	}
}

func TestValidatorFailClosedOnTrustBoundary(t *testing.T) {
	t.Parallel()
	validator := newValidator(t)
	systemScope := strings.Replace(validProjectManifest, "scope: project", "scope: system", 1)
	if _, violations := validator.Validate([]byte(systemScope)); !violationMentions(violations, "/scope") {
		t.Fatalf("system scope must fail closed: %v", violations)
	}
	trusted := strings.Replace(validUserManifest, "type: container", "type: trusted", 1)
	if _, violations := validator.Validate([]byte(trusted)); !violationMentions(violations, "/runtime/type") {
		t.Fatalf("trusted runtime must fail closed: %v", violations)
	}
	unknownPermission := strings.Replace(validProjectManifest, "permissions: [project.read]", "permissions: [llm.unlimited]", 1)
	if _, violations := validator.Validate([]byte(unknownPermission)); !violationMentions(violations, "/permissions/0") {
		t.Fatalf("unknown capability must fail closed: %v", violations)
	}
}

func TestValidatorRejectsSecretShapedContentByPathOnly(t *testing.T) {
	t.Parallel()
	validator := newValidator(t)
	cases := map[string]string{
		"secret key name":    validUserManifest + "api_key: nothing\n",
		"password key":       validUserManifest + "resources:\n  password: hunter2value-that-is-long\n",
		"private key value":  validUserManifest + "resources:\n  note: " + "\"-----BEGIN RSA PRIVATE KEY-----MIIB\"\n",
		"token shaped value": validUserManifest + "resources:\n  note: sk-1234567890abcdef12345678\n",
	}
	for name, yaml := range cases {
		t.Run(name, func(t *testing.T) {
			_, violations := validator.Validate([]byte(yaml))
			if len(violations) == 0 {
				t.Fatalf("secret-shaped content accepted in %s", name)
			}
			for _, violation := range violations {
				if strings.Contains(violation, "hunter2value") || strings.Contains(violation, "sk-1234567890") ||
					strings.Contains(violation, "MIIB") {
					t.Fatalf("violation leaked the secret value: %q", violation)
				}
			}
		})
	}
}

func TestValidatorDigestIgnoresYAMLFormatting(t *testing.T) {
	t.Parallel()
	validator := newValidator(t)
	reformatted := "name: Note Taker\n" + strings.Replace(validUserManifest, "name: Note Taker\n", "", 1)
	first, violations := validator.Validate([]byte(validUserManifest))
	if len(violations) != 0 {
		t.Fatalf("original rejected: %v", violations)
	}
	second, violations := validator.Validate([]byte(reformatted))
	if len(violations) != 0 {
		t.Fatalf("reformatted rejected: %v", violations)
	}
	if first.Digest != second.Digest || string(first.CanonicalJSON) != string(second.CanonicalJSON) {
		t.Fatalf("YAML key order must not change the digest: %q vs %q", first.Digest, second.Digest)
	}

	// Permission order is a schema-declared set: reordering must not change
	// the digest either, while any semantic change must.
	reordered := strings.Replace(validUserManifest,
		"permissions: [artifact.read, agent.task.run]", "permissions: [agent.task.run, artifact.read]", 1)
	third, violations := validator.Validate([]byte(reordered))
	if len(violations) != 0 || third.Digest != first.Digest {
		t.Fatalf("permission order must not change the digest: %v %q vs %q", violations, third.Digest, first.Digest)
	}
	changed := strings.Replace(validUserManifest, "name: Note Taker", "name: Note Taker Pro", 1)
	fourth, violations := validator.Validate([]byte(changed))
	if len(violations) != 0 || fourth.Digest == first.Digest {
		t.Fatalf("semantic change must change the digest: %v", violations)
	}
}

func TestValidatorNameWhitespaceRule(t *testing.T) {
	t.Parallel()
	validator := newValidator(t)
	padded := strings.Replace(validUserManifest, "name: Note Taker", `name: " Note Taker "`, 1)
	if _, violations := validator.Validate([]byte(padded)); !violationMentions(violations, "/name") {
		t.Fatalf("padded name must be rejected, not trimmed: %v", violations)
	}
}

func TestValidatorBoundedViolations(t *testing.T) {
	t.Parallel()
	validator := newValidator(t)
	// One violation per unknown permission plus per-permission schema noise:
	// exceed the cap and assert the deterministic suppressed marker.
	var builder strings.Builder
	builder.WriteString("apiVersion: workos.app/v1\nid: violation-storm\nname: Storm\nversion: 1.0.0\nscope: user\nruntime:\n  type: container\nsurfaces:\n  - id: main\n    renderer: web-bundle\npermissions: [")
	for i := 0; i < 200; i++ {
		if i > 0 {
			builder.WriteString(", ")
		}
		builder.WriteString("unknown.capability" + strings.Repeat("x", i))
	}
	builder.WriteString("]\nresources: {}\nhealth: {}\nmaintainer: {}\n")
	_, violations := validator.Validate([]byte(builder.String()))
	if len(violations) == 0 {
		t.Fatal("expected violations")
	}
	if len(violations) > maxViolations {
		t.Fatalf("violations not capped: %d", len(violations))
	}
	if !strings.HasSuffix(violations[len(violations)-1], "more violations suppressed)") {
		t.Fatalf("expected a suppressed-count marker, got %v", violations)
	}
	// Entries before the marker are deterministically ordered; the marker
	// itself always closes the list.
	entries := violations[:len(violations)-1]
	for i := 1; i < len(entries); i++ {
		if entries[i-1] > entries[i] {
			t.Fatalf("violations are not deterministically ordered: %v", violations)
		}
	}
}

func violationMentions(violations []string, fragment string) bool {
	for _, violation := range violations {
		if strings.Contains(violation, fragment) {
			return true
		}
	}
	return false
}
