package deepseek

import (
	"strings"
	"testing"
)

const validTwo = `{"version":"workos.deepseek.review-output.v1","summary":"structured review completed","artifacts":{"document.markdown.v1":"# doc\nbody\n","code.unified-diff.v1":"diff --git a/x b/x\n"}}`

func TestParseStructuredOutputAcceptsExactDocument(t *testing.T) {
	summary, artifacts, err := parseStructuredOutput(validTwo, []string{"document.markdown.v1", "code.unified-diff.v1"})
	if err != nil {
		t.Fatalf("honest document rejected: %v", err)
	}
	if summary != "structured review completed" || len(artifacts) != 2 {
		t.Fatalf("unexpected parse: %q %d", summary, len(artifacts))
	}
	if artifacts[0].artifactType != "document.markdown.v1" || artifacts[0].content != "# doc\nbody\n" {
		t.Fatalf("order/content drift: %#v", artifacts[0].content)
	}
}

func TestParseStructuredOutputRejectsEveryMalformation(t *testing.T) {
	requested := []string{"document.markdown.v1", "code.unified-diff.v1"}
	base := `{"version":"workos.deepseek.review-output.v1","summary":"ok","artifacts":{"document.markdown.v1":"# doc\n","code.unified-diff.v1":"diff\n"}}`
	cases := map[string]string{
		"prose prefix":       "Here is the review: " + base,
		"prose suffix":       base + "\nI hope this helps!",
		"code fence":         "```json\n" + base + "\n```",
		"trailing value":     base + ` {"version":"x"}`,
		"unknown field":      `{"version":"workos.deepseek.review-output.v1","summary":"ok","extra":1,"artifacts":{"document.markdown.v1":"# doc\n","code.unified-diff.v1":"diff\n"}}`,
		"missing summary":    `{"version":"workos.deepseek.review-output.v1","artifacts":{"document.markdown.v1":"# doc\n","code.unified-diff.v1":"diff\n"}}`,
		"missing artifacts":  `{"version":"workos.deepseek.review-output.v1","summary":"ok"}`,
		"extra artifact":     `{"version":"workos.deepseek.review-output.v1","summary":"ok","artifacts":{"document.markdown.v1":"# doc\n","code.unified-diff.v1":"diff\n","image.v1":"zz"}}`,
		"missing artifact":   `{"version":"workos.deepseek.review-output.v1","summary":"ok","artifacts":{"document.markdown.v1":"# doc\n"}}`,
		"wrong version":      `{"version":"workos.deepseek.review-output.v2","summary":"ok","artifacts":{"document.markdown.v1":"# doc\n","code.unified-diff.v1":"diff\n"}}`,
		"array document":     `[{"version":"workos.deepseek.review-output.v1"}]`,
		"control in summary": `{"version":"workos.deepseek.review-output.v1","summary":"bad\u0001value","artifacts":{"document.markdown.v1":"# doc\n","code.unified-diff.v1":"diff\n"}}`,
		"NUL in content":     `{"version":"workos.deepseek.review-output.v1","summary":"ok","artifacts":{"document.markdown.v1":"a\u0000b\n","code.unified-diff.v1":"diff\n"}}`,
		"oversize summary":   `{"version":"workos.deepseek.review-output.v1","summary":"` + strings.Repeat("s", 65*1024) + `","artifacts":{"document.markdown.v1":"# doc\n","code.unified-diff.v1":"diff\n"}}`,
		"oversize content":   `{"version":"workos.deepseek.review-output.v1","summary":"ok","artifacts":{"document.markdown.v1":"` + strings.Repeat("x", 513*1024) + `\n","code.unified-diff.v1":"diff\n"}}`,
	}
	for name, raw := range cases {
		if _, _, err := parseStructuredOutput(raw, requested); err == nil {
			t.Fatalf("%s was accepted", name)
		}
	}
	// Duplicate keys (syntactically valid JSON, semantically ambiguous).
	dup := `{"version":"workos.deepseek.review-output.v1","version":"workos.deepseek.review-output.v1","summary":"ok","artifacts":{"document.markdown.v1":"# doc\n","code.unified-diff.v1":"diff\n"}}`
	if _, _, err := parseStructuredOutput(dup, requested); err == nil {
		t.Fatal("duplicate top-level key was accepted")
	}
	dupArtifact := `{"version":"workos.deepseek.review-output.v1","summary":"ok","artifacts":{"document.markdown.v1":"a\n","document.markdown.v1":"b\n","code.unified-diff.v1":"diff\n"}}`
	if _, _, err := parseStructuredOutput(dupArtifact, requested); err == nil {
		t.Fatal("duplicate artifact key was accepted")
	}
}

func TestParseStructuredOutputSingleRequest(t *testing.T) {
	single := `{"version":"workos.deepseek.review-output.v1","summary":"one","artifacts":{"document.markdown.v1":"# only\n"}}`
	if _, artifacts, err := parseStructuredOutput(single, []string{"document.markdown.v1"}); err != nil || len(artifacts) != 1 {
		t.Fatalf("single-output document rejected: %v", err)
	}
	// A document carrying the second type is an extra artifact for a
	// single-type request.
	if _, _, err := parseStructuredOutput(validTwo, []string{"document.markdown.v1"}); err == nil {
		t.Fatal("extra artifact accepted for single-type request")
	}
}

func TestStructuredKeyTitleIsAdapterOwned(t *testing.T) {
	key, title := structuredKeyTitle("document.markdown.v1")
	if key != "document" || title == "" {
		t.Fatalf("markdown key/title: %q %q", key, title)
	}
	key, title = structuredKeyTitle("code.unified-diff.v1")
	if key != "patch" || title == "" {
		t.Fatalf("diff key/title: %q %q", key, title)
	}
}
