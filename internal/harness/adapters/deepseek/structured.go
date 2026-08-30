package deepseek

import (
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"
)

// reviewOutputVersion pins the structured review output contract.
const reviewOutputVersion = "workos.deepseek.review-output.v1"

// Structured summary/content bounds mirror (at equal or stricter strength)
// the Core Artifact module's canonical review bounds; Core revalidates every
// output again at materialization (ADR-0011).
const (
	maximumSummaryBytes     = 64 * 1024
	maximumArtifactBytes    = 512 * 1024
	maximumArtifactLines    = 20000
	maximumArtifactLineByte = 16 * 1024
)

// structuredArtifact is one validated candidate output.
type structuredArtifact struct {
	artifactType string
	content      string
}

// strictParseError marks a deterministic protocol failure. The message never
// carries raw model output.
func strictParseError() error {
	return protocolError("DeepSeek returned a malformed review output", nil)
}

// parseStructuredOutput strictly parses and validates the model's review
// output against the exact requested artifact type set. It accepts exactly
// one JSON object — no prefix, suffix, code fence, or prose — with exactly
// the version, a bounded summary, and an artifacts object whose key set
// equals the requested set. Unknown fields, duplicate keys, missing or extra
// artifacts, and any over-bound or control-bearing content fail closed.
func parseStructuredOutput(raw string, requested []string) (summary string, artifacts []structuredArtifact, err error) {
	if !utf8.ValidString(raw) {
		return "", nil, strictParseError()
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()

	token, decodeErr := decoder.Token()
	if decodeErr != nil {
		return "", nil, strictParseError()
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '{' {
		return "", nil, strictParseError()
	}

	var version, summaryValue string
	var artifactsRaw map[string]string
	seenTop := map[string]bool{}
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return "", nil, strictParseError()
		}
		key, ok := keyToken.(string)
		if !ok {
			return "", nil, strictParseError()
		}
		if seenTop[key] {
			return "", nil, strictParseError()
		}
		seenTop[key] = true
		switch key {
		case "version":
			if err := decoder.Decode(&version); err != nil {
				return "", nil, strictParseError()
			}
		case "summary":
			if err := decoder.Decode(&summaryValue); err != nil {
				return "", nil, strictParseError()
			}
		case "artifacts":
			artifactsRaw, err = decodeArtifactsObject(decoder)
			if err != nil {
				return "", nil, strictParseError()
			}
		default:
			return "", nil, strictParseError()
		}
	}
	// Consume the object's closing delimiter, then require the stream to be
	// exhausted: any trailing token (second value, prose, code fence) is a
	// failure.
	if closing, err := decoder.Token(); err != nil {
		return "", nil, strictParseError()
	} else if delimiter, ok := closing.(json.Delim); !ok || delimiter != '}' {
		return "", nil, strictParseError()
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return "", nil, strictParseError()
	}

	if version != reviewOutputVersion {
		return "", nil, strictParseError()
	}
	if !validSummary(summaryValue) {
		return "", nil, strictParseError()
	}
	if len(artifactsRaw) != len(requested) {
		return "", nil, strictParseError()
	}
	artifacts = make([]structuredArtifact, 0, len(requested))
	for _, artifactType := range requested {
		content, present := artifactsRaw[artifactType]
		if !present {
			return "", nil, strictParseError()
		}
		if !validArtifactContent(artifactType, content) {
			return "", nil, strictParseError()
		}
		artifacts = append(artifacts, structuredArtifact{artifactType: artifactType, content: content})
	}
	// Reject keys outside the requested set (extra aliases).
	for key := range artifactsRaw {
		found := false
		for _, artifactType := range requested {
			if key == artifactType {
				found = true
				break
			}
		}
		if !found {
			return "", nil, strictParseError()
		}
	}
	return summaryValue, artifacts, nil
}

func decodeArtifactsObject(decoder *json.Decoder) (map[string]string, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '{' {
		return nil, errUnexpectedToken
	}
	values := map[string]string{}
	seen := map[string]bool{}
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key, ok := keyToken.(string)
		if !ok {
			return nil, errUnexpectedToken
		}
		if seen[key] {
			return nil, errDuplicateKey
		}
		seen[key] = true
		var value string
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		values[key] = value
	}
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	return values, nil
}

var (
	errUnexpectedToken = errors.New("unexpected token")
	errDuplicateKey    = errors.New("duplicate key")
)

// validSummary bounds and sanitizes the summary: valid UTF-8, at most 64 KiB,
// no C0/C1 controls except the explicitly allowed LF and TAB.
func validSummary(summary string) bool {
	if summary == "" || len(summary) > maximumSummaryBytes || !utf8.ValidString(summary) {
		return false
	}
	for _, character := range summary {
		if character == '\n' || character == '\t' {
			continue
		}
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

// validArtifactContent applies the canonical review content grammar with the
// same bounds Core enforces at materialization: valid UTF-8, no NUL, no
// C0/C1 except LF and TAB, at most 512 KiB, at most 20k lines, and no line
// longer than 16 KiB UTF-8 bytes.
func validArtifactContent(artifactType, content string) bool {
	if content == "" || len(content) > maximumArtifactBytes || !utf8.ValidString(content) {
		return false
	}
	lines := 1 + strings.Count(content, "\n")
	if lines > maximumArtifactLines {
		return false
	}
	for _, line := range strings.Split(content, "\n") {
		if len(line) > maximumArtifactLineByte {
			return false
		}
	}
	for _, character := range content {
		if character == '\n' || character == '\t' {
			continue
		}
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

// buildStructuredDocument assembles the strict JSON output contract embedded
// in the task envelope when the task requests structured artifacts.
func outputContract(requested []string) map[string]any {
	return map[string]any{
		"version":            reviewOutputVersion,
		"required_artifacts": requested,
		"response_format":    "respond with exactly one JSON document matching this contract; no prose, no code fences",
	}
}
