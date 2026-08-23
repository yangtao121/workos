package manifestvalidator

import "regexp"

// secretKeyPattern matches field names that suggest secret-bearing content.
// It is a security policy, not a credential detector: manifests must declare
// capabilities, never carry credential material. Violations report only the
// field path, never the key or value. This cannot replace a Credential Vault
// or DLP pipeline.
var secretKeyPattern = regexp.MustCompile(
	`(?i)(?:^|[^a-z])(?:api[-_]?key|apikey|secret|secrets|password|passwd|pwd|token|credential|credentials|private[-_]?key|auth|authorization|bearer)(?:[^a-z]|$)`,
)

// secretValuePatterns match string values shaped like common credential
// formats (private key headers, prefixed API tokens, bearer credentials,
// AWS access key IDs, JWTs). Like the key pattern, matches fail closed and
// are reported by path only.
var secretValuePatterns = []*regexp.Regexp{
	regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----`),
	regexp.MustCompile(`^(?:sk|rk|pk|ghp|gho|ghu|xoxb|xoxp|ak)-[A-Za-z0-9_=-]{16,}$`),
	regexp.MustCompile(`(?i)^bearer\s+[A-Za-z0-9._~+/-]{16,}={0,2}$`),
	regexp.MustCompile(`^AKIA[0-9A-Z]{16}$`),
	regexp.MustCompile(`^eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{5,}$`),
}
