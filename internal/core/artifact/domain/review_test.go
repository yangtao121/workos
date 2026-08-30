package domain

import (
	"bytes"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

var (
	validStoredTime = time.Unix(1756500000, 0).UTC()
	zeroTime        = time.Time{}
)

func TestReviewTypeRegistry(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		raw       string
		wantType  string
		wantMedia string
		wantOK    bool
	}{
		{TypeMarkdown, TypeMarkdown, MediaTypeMarkdown, true},
		{TypeUnifiedDiff, TypeUnifiedDiff, MediaTypeUnifiedDiff, true},
		{"", "", "", false},
		{"app.web-bundle.v1", "", "", false},
		{"document.markdown.v2", "", "", false},
		{"DOCUMENT.MARKDOWN.V1", "", "", false},
	} {
		gotType, gotMedia, ok := ReviewType(tc.raw)
		if ok != tc.wantOK || gotType != tc.wantType || gotMedia != tc.wantMedia {
			t.Fatalf("ReviewType(%q) = (%q, %q, %v)", tc.raw, gotType, gotMedia, ok)
		}
		if IsReviewType(tc.raw) != tc.wantOK {
			t.Fatalf("IsReviewType(%q) mismatch", tc.raw)
		}
	}
}

func TestValidReviewOutputKey(t *testing.T) {
	t.Parallel()
	for value, want := range map[string]bool{
		"a": true, "document": true, "patch-v1.2_3": true,
		strings.Repeat("a", 64): true,
		"":                      false, "Document": false, "1document": false, "-lead": false,
		"has space": false, "slash/space": false, "unicodeé": false,
		strings.Repeat("a", 65): false,
	} {
		if got := ValidReviewOutputKey(value); got != want {
			t.Fatalf("ValidReviewOutputKey(%q) = %v", value, got)
		}
	}
}

func TestNormalizeReviewTitle(t *testing.T) {
	t.Parallel()
	title, ok := NormalizeReviewTitle("  Fake Harness Review Document \n")
	if !ok || title != "Fake Harness Review Document" {
		t.Fatalf("trim normalization failed: %q %v", title, ok)
	}
	long := strings.Repeat("题", MaxArtifactTitleRunes)
	if got, ok := NormalizeReviewTitle(long); !ok || got != long {
		t.Fatalf("bounded unicode title rejected: %v", ok)
	}
	if _, ok := NormalizeReviewTitle(strings.Repeat("题", MaxArtifactTitleRunes+1)); ok {
		t.Fatal("over-long title accepted")
	}
	for _, invalid := range []string{"", "   ", "line\nbreak", "tab\ttitle", "nul\x00title", "\x7f", "\x01ctrl"} {
		if _, ok := NormalizeReviewTitle(invalid); ok {
			t.Fatalf("invalid title accepted: %q", invalid)
		}
	}
}

func TestNormalizeReviewContentCanonicalization(t *testing.T) {
	t.Parallel()
	normalized, err := NormalizeReviewContent(TypeMarkdown, []byte("first\r\nsecond\nthird\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(normalized.Content, []byte("first\nsecond\nthird\n")) {
		t.Fatalf("CRLF was not normalized: %q", normalized.Content)
	}
	if normalized.ByteCount != len(normalized.Content) || normalized.LineCount != 3 {
		t.Fatalf("unexpected counts: %d/%d", normalized.ByteCount, normalized.LineCount)
	}
	same, err := NormalizeReviewContent(TypeMarkdown, []byte("first\nsecond\nthird\n"))
	if err != nil || !bytes.Equal(normalized.Content, same.Content) || normalized.Digest != same.Digest {
		t.Fatalf("same logical content produced different digests: %s vs %s", normalized.Digest, same.Digest)
	}
	changed, err := NormalizeReviewContent(TypeMarkdown, []byte("first\nsecond\nthird\n\n"))
	if err != nil || changed.Digest == normalized.Digest {
		t.Fatal("meaningful content change did not change the digest")
	}
}

func TestNormalizeReviewContentRejectsInvalidFacts(t *testing.T) {
	t.Parallel()
	for name, content := range map[string][]byte{
		"empty":            {},
		"nul":              {'a', 0, 'b'},
		"bare CR":          {'a', '\r', 'b'},
		"vertical tab":     {'a', 0x0b, 'b'},
		"bell":             {'a', 0x07, 'b'},
		"del":              {'a', 0x7f, 'b'},
		"c1 control":       {'a', 0x85, 'b'},
		"invalid utf8":     {0xff, 0xfe},
		"over byte limit":  bytes.Repeat([]byte("a"), MaxReviewContentBytes+1),
		"over line limit":  append(bytes.Repeat([]byte("x\n"), MaxReviewContentLines), 'x'),
		"over line length": append(bytes.Repeat([]byte("a"), MaxReviewLineBytes+1), '\n'),
	} {
		if _, err := NormalizeReviewContent(TypeMarkdown, content); err == nil {
			t.Fatalf("invalid %s content accepted", name)
		}
	}
	for name, content := range map[string][]byte{
		"at byte limit":  bytes.Repeat(append(bytes.Repeat([]byte("a"), MaxReviewLineBytes-1), '\n'), 32),
		"at line limit":  append(bytes.Repeat([]byte("x\n"), MaxReviewContentLines-1), 'x'),
		"at line length": append(bytes.Repeat([]byte("a"), MaxReviewLineBytes), '\n'),
		"tabs and lf":    {'a', '\t', '\n', 'b'},
		"trailing lf":    []byte("line\n"),
		"no trailing lf": []byte("line"),
		"single newline": []byte("\n"),
	} {
		if _, err := NormalizeReviewContent(TypeMarkdown, content); err != nil {
			t.Fatalf("valid %s content rejected: %v", name, err)
		}
	}
}

func TestNormalizeReviewContentRejectsUnknownType(t *testing.T) {
	t.Parallel()
	if _, err := NormalizeReviewContent("app.web-bundle.v1", []byte("x")); err == nil {
		t.Fatal("unknown canonical type accepted")
	}
}

func TestReviewDigestGoldenVectors(t *testing.T) {
	t.Parallel()
	content := []byte("# Title\n\nBody text.\n")
	digest := ReviewContentDigest(TypeMarkdown, content)
	if !ValidArtifactDigest(digest) {
		t.Fatalf("digest violates the canonical shape: %q", digest)
	}
	other := ReviewContentDigest(TypeUnifiedDiff, content)
	if other == digest {
		t.Fatal("type is not covered by the digest")
	}
	// Deterministic across calls.
	if digest != ReviewContentDigest(TypeMarkdown, content) {
		t.Fatal("digest is not deterministic")
	}

	request := ReviewOutputRequestDigest("0198d7ea-0000-7000-8000-000000000001", "0198d7ea-0000-7000-8000-000000000002",
		"document", "Title", digest)
	if !ValidArtifactDigest(request) {
		t.Fatalf("request digest violates the canonical shape: %q", request)
	}
	sameTaskOtherProject := ReviewOutputRequestDigest("0198d7ea-0000-7000-8000-000000000009", "0198d7ea-0000-7000-8000-000000000002",
		"document", "Title", digest)
	if sameTaskOtherProject == request {
		t.Fatal("project is not covered by the request digest")
	}
}

func TestValidStoredFactValidation(t *testing.T) {
	t.Parallel()
	valid := ReviewArtifact{
		ID: "0198d7ea-0000-7000-8000-000000000001", OwnerUserID: "owner",
		ProjectID: "0198d7ea-0000-7000-8000-000000000002", SourceTask: "0198d7ea-0000-7000-8000-000000000003",
		OutputKey: "document", Type: TypeMarkdown, Title: "Title", MediaType: MediaTypeMarkdown,
		Digest: ReviewContentDigest(TypeMarkdown, []byte("x")), ByteCount: 1, LineCount: 1,
		CreatedAt: validStoredTime,
	}
	if !ValidStoredReviewFact(valid) {
		t.Fatal("valid stored fact rejected")
	}
	corruptions := []func(*ReviewArtifact){
		func(f *ReviewArtifact) { f.ID = "not-a-uuid" },
		func(f *ReviewArtifact) { f.ProjectID = "" },
		func(f *ReviewArtifact) { f.SourceTask = "0198d7ea-0000-7000-8000-000000000003 " },
		func(f *ReviewArtifact) { f.OutputKey = "UPPER" },
		func(f *ReviewArtifact) { f.MediaType = MediaTypeUnifiedDiff },
		func(f *ReviewArtifact) { f.Digest = "sha256:short" },
		func(f *ReviewArtifact) { f.ByteCount = 0 },
		func(f *ReviewArtifact) { f.LineCount = MaxReviewContentLines + 1 },
		func(f *ReviewArtifact) { f.CreatedAt = zeroTime },
		func(f *ReviewArtifact) { f.Title = "" },
	}
	for index, corrupt := range corruptions {
		fact := valid
		corrupt(&fact)
		if ValidStoredReviewFact(fact) {
			t.Fatalf("corrupted stored fact %d accepted", index)
		}
	}
}

func TestValidStoredArtifactCoversBothSubtypes(t *testing.T) {
	t.Parallel()
	bundle := Artifact{
		ID: "0198d7ea-0000-7000-8000-000000000001", OwnerUserID: "owner", Type: TypeWebBundle,
		Title: "App", MediaType: MediaTypeBundle, ContentRef: "wbbnd:0198d7ea-0000-7000-8000-00000000abcd",
		Digest: "sha256:" + strings.Repeat("a", 64), FileCount: 1, TotalSizeBytes: 1, CreatedAt: validStoredTime,
	}
	if !ValidStoredArtifact(bundle) {
		t.Fatal("valid bundle metadata rejected")
	}
	review := Artifact{
		ID: "0198d7ea-0000-7000-8000-000000000002", OwnerUserID: "owner", Type: TypeMarkdown,
		Title: "Doc", MediaType: MediaTypeMarkdown, Digest: "sha256:" + strings.Repeat("b", 64),
		FileCount: 1, TotalSizeBytes: 5, CreatedAt: validStoredTime,
		ProjectID: "0198d7ea-0000-7000-8000-000000000003", SourceTaskID: "0198d7ea-0000-7000-8000-000000000004",
	}
	if !ValidStoredArtifact(review) {
		t.Fatal("valid review metadata rejected")
	}
	reviewMissingProject := review
	reviewMissingProject.SourceTaskID = ""
	if ValidStoredArtifact(reviewMissingProject) {
		t.Fatal("review metadata without provenance accepted")
	}
	bundleWithProject := bundle
	bundleWithProject.ProjectID = review.ProjectID
	if ValidStoredArtifact(bundleWithProject) {
		t.Fatal("bundle metadata with project provenance accepted")
	}
	if ValidStoredArtifact(Artifact{ID: review.ID, Type: "unknown.type"}) {
		t.Fatal("unknown subtype accepted")
	}
	_ = utf8.Valid
}
