package manifestvalidator

import (
	"fmt"
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

// TestCredentialShapeRuleIsSharedByKeysAndValues pins the single
// implementation both checks rely on: one list of shapes, positive and
// negative, so a key can never be treated more permissively than a value.
func TestCredentialShapeRuleIsSharedByKeysAndValues(t *testing.T) {
	t.Parallel()
	cases := []struct {
		value  string
		shaped bool
	}{
		{"sk-zzzz0123456789abcdef", true},
		{"ghp-zzzz0123456789abcdef", true},
		{"eyJzzzzzz123456.eyJzzzzzz123456.zzsynthetic12345", true},
		{"AKIAZZZZ0123456789AB", true},
		{"-----BEGIN SYNTHETIC PRIVATE KEY-----", true},
		{"Bearer zzzz0123456789abcdefgh", true},
		{"note-taker", false},
		{"ski_rating", false},
		{"slack_channel", false},
		{"memory", false},
	}
	for _, testCase := range cases {
		if got := credentialShapedString(testCase.value); got != testCase.shaped {
			t.Fatalf("credentialShapedString(%q) = %v, want %v", testCase.value, got, testCase.shaped)
		}
	}
}

// TestValidatorRejectsCredentialShapedMappingKeys proves the trust boundary
// for credential material used as a mapping key: each shape is rejected by the
// credential-material policy (not as a duplicate key or a schema error), only
// the safe parent path is reported, and no normalized manifest survives. All
// fixtures are obviously synthetic.
func TestValidatorRejectsCredentialShapedMappingKeys(t *testing.T) {
	t.Parallel()
	validator := newValidator(t)
	const policyMessage = "mapping keys that look like credentials are not allowed in manifests"
	cases := []struct {
		name     string
		yaml     string
		wantPath string
	}{
		{
			name: "prefixed-token-shaped key in resources",
			yaml: strings.Replace(validUserManifest,
				"resources:\n  limits:\n    memory: 256\n",
				"resources:\n  limits:\n    memory: 256\n  \"sk-zzzz0123456789abcdef\": 1\n", 1),
			wantPath: "/resources",
		},
		{
			name: "jwt-shaped key in health",
			yaml: strings.Replace(validUserManifest,
				"health:\n  interval: 30\n",
				"health:\n  interval: 30\n  \"eyJzzzzzz123456.eyJzzzzzz123456.zzsynthetic12345\": 1\n", 1),
			wantPath: "/health",
		},
		{
			name: "aws-shaped key in maintainer",
			yaml: strings.Replace(validUserManifest,
				"maintainer:\n  name: Example\n",
				"maintainer:\n  name: Example\n  \"AKIAZZZZ0123456789AB\": 1\n", 1),
			wantPath: "/maintainer",
		},
		{
			name: "pem-header-shaped key in resources",
			yaml: strings.Replace(validUserManifest,
				"resources:\n  limits:\n    memory: 256\n",
				"resources:\n  limits:\n    memory: 256\n  \"-----BEGIN SYNTHETIC PRIVATE KEY-----\": 1\n", 1),
			wantPath: "/resources",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			result, violations := validator.Validate([]byte(testCase.yaml))
			if len(violations) == 0 {
				t.Fatalf("credential-shaped mapping key accepted: %#v", result)
			}
			if !violationMentions(violations, testCase.wantPath+": "+policyMessage) {
				t.Fatalf("expected the credential-material policy at %s, got %v", testCase.wantPath, violations)
			}
			// The violation must not echo the key itself: no prefix, payload
			// fragment, full key, or escaped form may survive.
			joined := strings.Join(violations, " ")
			for _, fragment := range []string{"sk-zzzz", "eyJ", "AKIA", "PRIVATE KEY", "zzzz", "synthetic", "~0", "~1"} {
				if strings.Contains(joined, fragment) {
					t.Fatalf("violation leaked the credential-shaped key: %q", joined)
				}
			}
			if result.ID != "" || result.Digest != "" || len(result.CanonicalJSON) != 0 {
				t.Fatalf("rejected manifest must not produce a normalized form: %#v", result)
			}
		})
	}

	// Neighboring non-credential keys in the same free-form blocks must stay
	// accepted: the shape rule matches credential formats, not letter
	// fragments.
	neighbors := strings.Replace(validUserManifest,
		"resources:\n  limits:\n    memory: 256\n",
		"resources:\n  limits:\n    memory: 256\n  short_code: ab12\n  ski_rating: 5\n  slack_channel: general\n", 1)
	if _, violations := validator.Validate([]byte(neighbors)); len(violations) != 0 {
		t.Fatalf("non-credential neighbor keys must be accepted, got %v", violations)
	}
}

func TestValidatorRejectsSecretShapedContentByPathOnly(t *testing.T) {
	t.Parallel()
	validator := newValidator(t)
	// Every case injects content into the single existing resources block of
	// a structurally and schema-valid manifest, so only the secret policy can
	// reject it. All values are obviously synthetic.
	withResource := func(extra string) string {
		return strings.Replace(validUserManifest,
			"resources:\n  limits:\n    memory: 256\n",
			"resources:\n  limits:\n    memory: 256\n"+extra, 1)
	}
	withHealth := func(extra string) string {
		return strings.Replace(validUserManifest, "health:\n  interval: 30\n", "health:\n  interval: 30\n"+extra, 1)
	}
	withMaintainer := func(extra string) string {
		return strings.Replace(validUserManifest, "maintainer:\n  name: Example\n", "maintainer:\n  name: Example\n"+extra, 1)
	}
	cases := []struct {
		name         string
		yaml         string
		wantPath     string
		wantContains string
	}{
		{
			name:         "password key in resources",
			yaml:         withResource("  password: synthetic-not-a-real-value\n"),
			wantPath:     "/resources/password",
			wantContains: "field names that hold secrets are not allowed in manifests",
		},
		{
			name:         "camelCase accessToken key in health",
			yaml:         withHealth("  accessToken: synthetic-not-a-real-value\n"),
			wantPath:     "/health/accessToken",
			wantContains: "field names that hold secrets are not allowed in manifests",
		},
		{
			name:         "camelCase clientSecret key in maintainer",
			yaml:         withMaintainer("  clientSecret: synthetic-not-a-real-value\n"),
			wantPath:     "/maintainer/clientSecret",
			wantContains: "field names that hold secrets are not allowed in manifests",
		},
		{
			name:         "camelCase credentialValue key",
			yaml:         withResource("  credentialValue: synthetic-not-a-real-value\n"),
			wantPath:     "/resources/credentialValue",
			wantContains: "field names that hold secrets are not allowed in manifests",
		},
		{
			name:         "camelCase awsSecretAccessKey key",
			yaml:         withResource("  awsSecretAccessKey: synthetic-not-a-real-value\n"),
			wantPath:     "/resources/awsSecretAccessKey",
			wantContains: "field names that hold secrets are not allowed in manifests",
		},
		{
			name:         "compound private key phrase",
			yaml:         withResource("  private_key: synthetic-not-a-real-value\n"),
			wantPath:     "/resources/private_key",
			wantContains: "field names that hold secrets are not allowed in manifests",
		},
		{
			name:         "pem-like value",
			yaml:         withResource("  license: \"-----BEGIN SYNTHETIC PRIVATE KEY-----MIIBsynthetic\"\n"),
			wantPath:     "/resources/license",
			wantContains: "values that look like credentials are not allowed in manifests",
		},
		{
			name:         "token-shaped synthetic value",
			yaml:         withResource("  contact: sk-zzzz0123456789abcdef\n"),
			wantPath:     "/resources/contact",
			wantContains: "values that look like credentials are not allowed in manifests",
		},
		{
			name:         "bearer-shaped synthetic value",
			yaml:         withResource("  hook: \"Bearer zzzz0123456789abcdefgh\"\n"),
			wantPath:     "/resources/hook",
			wantContains: "values that look like credentials are not allowed in manifests",
		},
		{
			name:         "jwt-shaped synthetic value",
			yaml:         withResource("  session: eyJzzzzzz123456.eyJzzzzzz123456.zzsynthetic12345\n"),
			wantPath:     "/resources/session",
			wantContains: "values that look like credentials are not allowed in manifests",
		},
		{
			name:         "aws-like synthetic value",
			yaml:         withResource("  account: AKIAZZZZ0123456789AB\n"),
			wantPath:     "/resources/account",
			wantContains: "values that look like credentials are not allowed in manifests",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, violations := validator.Validate([]byte(testCase.yaml))
			if len(violations) == 0 {
				t.Fatalf("secret-shaped content accepted in %s", testCase.name)
			}
			if !violationMentions(violations, testCase.wantPath) {
				t.Fatalf("expected a violation at %s, got %v", testCase.wantPath, violations)
			}
			if !violationMentions(violations, testCase.wantContains) {
				t.Fatalf("expected policy message %q, got %v", testCase.wantContains, violations)
			}
			for _, violation := range violations {
				if strings.Contains(violation, "synthetic") || strings.Contains(violation, "zzzz") ||
					strings.Contains(violation, "MIIB") || strings.Contains(violation, "AKIAZZ") {
					t.Fatalf("violation leaked the secret value: %q", violation)
				}
			}
		})
	}

	// Neighboring non-secret names inside the same free-form blocks must stay
	// accepted: the policy matches whole tokens, not letter fragments.
	neighbors := withResource("  keyboard_shortcut: ctrl-shift-k\n  monetization: off\n  sort_order: id\n  displayHint: compact\n")
	if _, violations := validator.Validate([]byte(neighbors)); len(violations) != 0 {
		t.Fatalf("non-secret neighbor names must be accepted, got %v", violations)
	}
}

func TestSecretKeyTokenization(t *testing.T) {
	t.Parallel()
	cases := []struct {
		key    string
		secret bool
	}{
		{"accessToken", true},
		{"access_token", true},
		{"clientSecret", true},
		{"client-secret", true},
		{"credentialValue", true},
		{"awsSecretAccessKey", true},
		{"aws_secret_access_key", true},
		{"apiKey", true},
		{"api_key", true},
		{"apikey", true},
		{"privateKey", true},
		{"private_key", true},
		{"X-Auth-Token", true},
		{"authToken", true},
		{"bearer", true},
		{"passwd", true},
		{"keyboard", false},
		{"keyboard_shortcut", false},
		{"monetization", false},
		{"sort_order", false},
		{"displayHint", false},
		{"maxRetries", false},
		{"name", false},
		{"api", false},
		{"key", false},
	}
	for _, testCase := range cases {
		if got := secretBearingKey(testCase.key); got != testCase.secret {
			t.Fatalf("secretBearingKey(%q) = %v, want %v", testCase.key, got, testCase.secret)
		}
	}
}

func TestValidatorRejectsUnsafeMappingKeys(t *testing.T) {
	t.Parallel()
	validator := newValidator(t)
	const wantMessage = "mapping keys must be valid UTF-8, control-free, and between 1 and 256 characters"
	cases := map[string]string{
		"control character in resources":  "resources:\n  limits:\n    memory: 256\n  \"bad\\u0001key\": 1\n",
		"control character in health":     "health:\n  interval: 30\n  \"bad\\u0007key\": 1\n",
		"control character in maintainer": "maintainer:\n  name: Example\n  \"bad\\u007fkey\": 1\n",
		"c1 control character":            "maintainer:\n  name: Example\n  \"bad\\u0085key\": 1\n",
		"oversize key":                    "resources:\n  limits:\n    memory: 256\n  \"" + strings.Repeat("k", 257) + `": 1` + "\n",
	}
	for name, block := range cases {
		t.Run(name, func(t *testing.T) {
			yaml := strings.Replace(validUserManifest, "resources:\n  limits:\n    memory: 256\n", block, 1)
			if strings.HasPrefix(block, "health:") {
				yaml = strings.Replace(validUserManifest, "health:\n  interval: 30\n", block, 1)
			} else if strings.HasPrefix(block, "maintainer:") {
				yaml = strings.Replace(validUserManifest, "maintainer:\n  name: Example\n", block, 1)
			}
			result, violations := validator.Validate([]byte(yaml))
			if len(violations) == 0 {
				t.Fatalf("unsafe mapping key accepted: %#v", result)
			}
			if !violationMentions(violations, wantMessage) {
				t.Fatalf("expected the key-safety rule, got %v", violations)
			}
			// The raw unsafe key must never appear in any violation: only the
			// parent path is reported.
			joined := strings.Join(violations, " ")
			if strings.Contains(joined, "\x01") || strings.Contains(joined, "\x07") ||
				strings.Contains(joined, "\x7f") || strings.Contains(joined, "bad") {
				t.Fatalf("violation leaked the unsafe key: %q", joined)
			}
			// The violation reports the parent path, never a child pointer
			// built from the unsafe key.
			for _, violation := range violations {
				if strings.Contains(violation, "u0001") || strings.Contains(violation, "u0085") {
					t.Fatalf("violation embedded the escaped key: %q", violation)
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

const validWebBundleManifestTemplate = `apiVersion: workos.app/v1
id: notes-bundle
name: Notes Bundle
version: 1.0.0
scope: user
runtime:
  type: web-bundle
  artifactId: 0198d7ea-2110-7c42-b659-c5e4d73bc343
  artifactDigest: sha256:%s
surfaces:
  - id: main
    renderer: web-bundle
    route: /
    adaptive: true
permissions: [artifact.read]
resources: {}
health: {}
maintainer: {}
`

func webBundleManifest(artifactID, digest string) []byte {
	rendered := fmt.Sprintf(validWebBundleManifestTemplate, digest)
	if artifactID != "" {
		rendered = strings.Replace(rendered, "artifactId: 0198d7ea-2110-7c42-b659-c5e4d73bc343", "artifactId: "+artifactID, 1)
	}
	return []byte(rendered)
}

func TestValidatorAcceptsWebBundleManifest(t *testing.T) {
	t.Parallel()
	validator := newValidator(t)
	manifest, violations := validator.Validate(webBundleManifest("", strings.Repeat("a", 64)))
	if len(violations) > 0 {
		t.Fatalf("valid web bundle manifest rejected: %v", violations)
	}
	if manifest.RuntimeType != "web-bundle" || manifest.WebBundle == nil {
		t.Fatalf("launch descriptor not projected: %+v", manifest)
	}
	if manifest.WebBundle.ArtifactID != "0198d7ea-2110-7c42-b659-c5e4d73bc343" ||
		manifest.WebBundle.ArtifactDigest != "sha256:"+strings.Repeat("a", 64) {
		t.Fatalf("descriptor mismatch: %+v", manifest.WebBundle)
	}
	if !domain.ValidWebBundleArtifactID(manifest.WebBundle.ArtifactID) {
		t.Fatal("schema and domain artifact grammar diverged")
	}
}

func TestValidatorEnforcesWebBundleCrossFieldRules(t *testing.T) {
	t.Parallel()
	validator := newValidator(t)
	cases := map[string][]byte{
		"missing digest": []byte(strings.Replace(string(webBundleManifest("", strings.Repeat("a", 64))),
			"  artifactDigest: sha256:"+strings.Repeat("a", 64)+"\n", "", 1)),
		"bad digest": webBundleManifest("", "ZZZ"+strings.Repeat("a", 61)),
		"missing id": webBundleManifest("not-a-uuid", strings.Repeat("a", 64)),
		"non-v7 id":  webBundleManifest("0198d7ea-1110-4c42-b659-c5e4d73bc343", strings.Repeat("a", 64)),
		"container image": []byte(strings.Replace(string(webBundleManifest("", strings.Repeat("a", 64))),
			"  artifactDigest:", "  image: busybox\n  artifactDigest:", 1)),
		"container command": []byte(strings.Replace(string(webBundleManifest("", strings.Repeat("a", 64))),
			"  artifactDigest:", "  command: [\"x\"]\n  artifactDigest:", 1)),
		"container port": []byte(strings.Replace(string(webBundleManifest("", strings.Repeat("a", 64))),
			"  artifactDigest:", "  port: 8080\n  artifactDigest:", 1)),
		"multiple surfaces": []byte(strings.Replace(string(webBundleManifest("", strings.Repeat("a", 64))),
			"surfaces:\n  - id: main", "surfaces:\n  - id: main", 1)),
		"wrong renderer": []byte(strings.Replace(string(webBundleManifest("", strings.Repeat("a", 64))),
			"renderer: web-bundle", "renderer: declarative", 1)),
	}
	// Add a second surface for the multiple-surfaces case explicitly.
	cases["multiple surfaces"] = []byte(strings.Replace(string(cases["multiple surfaces"]),
		"permissions:", "  - id: second\n    renderer: web-bundle\npermissions:", 1))
	for name, manifest := range cases {
		if _, violations := validator.Validate(manifest); len(violations) == 0 {
			t.Errorf("%s: manifest accepted", name)
		}
	}
}

func TestValidatorLegacyManifestsStillPass(t *testing.T) {
	t.Parallel()
	validator := newValidator(t)
	for name, manifest := range map[string]string{"user": validUserManifest, "project": validProjectManifest} {
		parsed, violations := validator.Validate([]byte(manifest))
		if len(violations) > 0 {
			t.Fatalf("%s legacy manifest regressed: %v", name, violations)
		}
		if parsed.WebBundle != nil {
			t.Fatalf("%s legacy manifest gained a bundle descriptor", name)
		}
	}
}
