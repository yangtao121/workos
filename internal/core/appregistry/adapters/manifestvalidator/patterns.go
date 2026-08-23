package manifestvalidator

import (
	"regexp"
	"strings"
	"unicode"
)

// Secret-bearing field names are detected by tokenizing the key: snake_case,
// kebab-case, and other non-alphanumeric boundaries start a new token, and so
// does each camelCase hump. Tokens are then matched exactly against the
// vocabulary below, so "monetization" or "keyboard" can never trip a substring
// rule while "accessToken", "client_secret", and "awsSecretAccessKey" all
// resolve to known secret tokens. This is a security policy, not a credential
// detector: manifests must declare capabilities, never carry credential
// material. Violations report only the field path, never the key or value, and
// this cannot replace a Credential Vault or DLP pipeline.
var secretTokenVocabulary = map[string]bool{
	"secret": true, "secrets": true, "password": true, "passwd": true, "pwd": true,
	"passphrase": true, "token": true, "tokens": true, "credential": true,
	"credentials": true, "apikey": true, "privatekey": true, "auth": true,
	"authorization": true, "authorisation": true, "bearer": true,
}

// secretTokenPhrases are token sequences that only count when their tokens
// appear adjacent, covering compound names whose parts are harmless alone.
var secretTokenPhrases = [][]string{
	{"api", "key"},
	{"api", "secret"},
	{"private", "key"},
}

func secretBearingKey(key string) bool {
	tokens := splitNameTokens(key)
	for _, token := range tokens {
		if secretTokenVocabulary[token] {
			return true
		}
	}
	for _, phrase := range secretTokenPhrases {
		if containsTokenSequence(tokens, phrase) {
			return true
		}
	}
	return false
}

func containsTokenSequence(tokens, phrase []string) bool {
	for start := 0; start+len(phrase) <= len(tokens); start++ {
		matched := true
		for offset, want := range phrase {
			if tokens[start+offset] != want {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

// splitNameTokens splits an identifier into lowercase tokens at
// non-alphanumeric boundaries and camelCase humps ("awsSecretAccessKey" →
// aws, secret, access, key).
func splitNameTokens(name string) []string {
	runes := []rune(name)
	tokens := make([]string, 0, 4)
	var current []rune
	flush := func() {
		if len(current) > 0 {
			tokens = append(tokens, strings.ToLower(string(current)))
			current = current[:0]
		}
	}
	for index, r := range runes {
		switch {
		case !unicode.IsLetter(r) && !unicode.IsDigit(r):
			flush()
		case unicode.IsUpper(r):
			if len(current) > 0 {
				previous := current[len(current)-1]
				nextIsLower := index+1 < len(runes) && unicode.IsLower(runes[index+1])
				if unicode.IsLower(previous) || unicode.IsDigit(previous) || (unicode.IsUpper(previous) && nextIsLower) {
					flush()
				}
			}
			current = append(current, r)
		default:
			current = append(current, r)
		}
	}
	flush()
	return tokens
}

// credentialShapePatterns match strings shaped like common credential formats
// (private key headers, prefixed API tokens, bearer credentials, AWS access
// key IDs, JWTs). They are the single credential-shape rule, applied to both
// string values and mapping keys so the two checks can never drift apart.
// Like the name policy, matches fail closed and are reported by path only.
// Fixtures only ever use obviously synthetic values shaped like these formats.
var credentialShapePatterns = []*regexp.Regexp{
	regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----`),
	regexp.MustCompile(`^(?:sk|rk|pk|ghp|gho|ghu|xoxb|xoxp|ak)-[A-Za-z0-9_=-]{16,}$`),
	regexp.MustCompile(`(?i)^bearer\s+[A-Za-z0-9._~+/-]{16,}={0,2}$`),
	regexp.MustCompile(`^AKIA[0-9A-Z]{16}$`),
	regexp.MustCompile(`^eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{5,}$`),
}

// credentialShapedString reports whether the string itself is shaped like a
// known credential format. It backs both the value scan and the mapping-key
// guard: a key that carries credential material is exactly as dangerous as a
// value that does.
func credentialShapedString(value string) bool {
	for _, pattern := range credentialShapePatterns {
		if pattern.MatchString(value) {
			return true
		}
	}
	return false
}
