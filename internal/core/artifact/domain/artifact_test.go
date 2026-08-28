package domain

import (
	"fmt"
	"strings"
	"testing"
)

func files(inputs ...BundleFileInput) []BundleFileInput { return inputs }

func validFiles() []BundleFileInput {
	return []BundleFileInput{
		{Path: "index.html", Content: []byte("<!doctype html><title>App</title>")},
		{Path: "app.js", Content: []byte("document.title = 'app'")},
	}
}

func TestNormalizeWebBundleAcceptsAndSorts(t *testing.T) {
	t.Parallel()
	bundle, err := NormalizeWebBundle("index.html", validFiles())
	if err != nil {
		t.Fatalf("valid bundle rejected: %v", err)
	}
	if bundle.Entrypoint != "index.html" || len(bundle.Files) != 2 {
		t.Fatalf("unexpected bundle shape: %+v", bundle)
	}
	if bundle.Files[0].Path != "app.js" || bundle.Files[1].Path != "index.html" {
		t.Fatalf("files are not sorted by path: %+v", bundle.Files)
	}
	if bundle.Files[0].MediaType != "text/javascript; charset=utf-8" {
		t.Fatalf("server-derived media type missing: %q", bundle.Files[0].MediaType)
	}
}

func TestNormalizeWebBundleDigestIsOrderIndependentAndSensitive(t *testing.T) {
	t.Parallel()
	forward, err := NormalizeWebBundle("index.html", validFiles())
	if err != nil {
		t.Fatal(err)
	}
	reversed, err := NormalizeWebBundle("index.html", []BundleFileInput{
		{Path: "app.js", Content: []byte("document.title = 'app'")},
		{Path: "index.html", Content: []byte("<!doctype html><title>App</title>")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if forward.CanonicalDigest() != reversed.CanonicalDigest() {
		t.Fatal("submission order changed the canonical digest")
	}
	for name, mutated := range map[string][]BundleFileInput{
		"entrypoint": {validFiles()[0], {Path: "app.js", Content: []byte("x")}},
		"content":    {{Path: "index.html", Content: []byte("<!doctype html>")}, validFiles()[1]},
		"path":       {validFiles()[0], {Path: "app2.js", Content: []byte("document.title = 'app'")}},
		"add-file":   append(validFiles(), BundleFileInput{Path: "style.css", Content: []byte("body{}")}),
	} {
		other, err := NormalizeWebBundle("index.html", mutated)
		if err != nil {
			t.Fatalf("%s: mutation rejected: %v", name, err)
		}
		if other.CanonicalDigest() == forward.CanonicalDigest() {
			t.Fatalf("%s change did not change the digest", name)
		}
	}
}

func TestNormalizeWebBundleRejectsMalformedInput(t *testing.T) {
	t.Parallel()
	tooLong := strings.Repeat("a", MaxBundlePathBytes+1)
	cases := map[string]struct {
		entrypoint string
		files      []BundleFileInput
	}{
		"no files":            {"index.html", nil},
		"empty path":          {"index.html", files(BundleFileInput{Path: "", Content: []byte("x")})},
		"absolute path":       {"index.html", files(BundleFileInput{Path: "/etc/passwd", Content: []byte("x")})},
		"backslash":           {"index.html", files(BundleFileInput{Path: `assets\app.js`, Content: []byte("x")})},
		"dot segment":         {"index.html", files(BundleFileInput{Path: "../secret.txt", Content: []byte("x")})},
		"lone dot":            {"index.html", files(BundleFileInput{Path: "./app.js", Content: []byte("x")})},
		"double slash":        {"index.html", files(BundleFileInput{Path: "assets//app.js", Content: []byte("x")})},
		"trailing slash":      {"index.html", files(BundleFileInput{Path: "assets/", Content: []byte("x")})},
		"percent encoding":    {"index.html", files(BundleFileInput{Path: "a%2fb.js", Content: []byte("x")})},
		"unicode path":        {"index.html", files(BundleFileInput{Path: "应用.js", Content: []byte("x")})},
		"control character":   {"index.html", files(BundleFileInput{Path: "a\x00b.js", Content: []byte("x")})},
		"path too long":       {"index.html", files(BundleFileInput{Path: tooLong, Content: []byte("x")})},
		"duplicate path":      {"index.html", append(validFiles(), BundleFileInput{Path: "app.js", Content: []byte("dup")})},
		"case-fold collision": {"index.html", append(validFiles(), BundleFileInput{Path: "APP.JS", Content: []byte("dup")})},
		"unknown media":       {"index.html", files(BundleFileInput{Path: "app.exe", Content: []byte("x")})},
		"empty content":       {"index.html", files(BundleFileInput{Path: "app.js", Content: nil})},
		"entrypoint missing":  {"missing.html", validFiles()},
		"entrypoint not html": {"app.js", validFiles()},
		"entrypoint absolute": {"/index.html", validFiles()},
	}
	for name, input := range cases {
		if _, err := NormalizeWebBundle(input.entrypoint, input.files); err == nil {
			t.Errorf("%s: malformed bundle accepted", name)
		}
	}
}

func TestNormalizeWebBundleEnforcesLimits(t *testing.T) {
	t.Parallel()
	bundleFiles := func(count int) []BundleFileInput {
		result := make([]BundleFileInput, 0, count)
		for index := 0; index < count; index++ {
			if index == 0 {
				result = append(result, BundleFileInput{Path: "f000.html", Content: []byte("x")})
				continue
			}
			result = append(result, BundleFileInput{Path: fmt.Sprintf("f%03d.js", index), Content: []byte("x")})
		}
		return result
	}
	if _, err := NormalizeWebBundle("f000.html", bundleFiles(MaxBundleFiles)); err != nil {
		t.Fatalf("bundle at the file-count limit rejected: %v", err)
	}
	if _, err := NormalizeWebBundle("f000.html", bundleFiles(MaxBundleFiles+1)); err == nil {
		t.Fatal("bundle above the file-count limit accepted")
	}

	tooLargeFile := BundleFileInput{Path: "big.js", Content: make([]byte, MaxBundleFileBytes+1)}
	if _, err := NormalizeWebBundle("index.html", files(BundleFileInput{Path: "index.html", Content: []byte("x")}, tooLargeFile)); err == nil {
		t.Fatal("file above the single-file limit accepted")
	}

	total := []BundleFileInput{{Path: "index.html", Content: []byte("x")}}
	totalBytes := 1
	for index := 0; totalBytes+MaxBundleFileBytes <= MaxBundleTotalBytes; index++ {
		total = append(total, BundleFileInput{Path: fmt.Sprintf("f%03d.js", index), Content: make([]byte, MaxBundleFileBytes)})
		totalBytes += MaxBundleFileBytes
	}
	// One more maximum-size file crosses the total budget exactly over the
	// 2 MiB boundary.
	total = append(total, BundleFileInput{Path: "zzz.js", Content: make([]byte, MaxBundleFileBytes)})
	if _, err := NormalizeWebBundle("index.html", total); err == nil {
		t.Fatal("bundle above the total-size limit accepted")
	}
}

func TestCreateRequestDigestCoversTitleAndBundle(t *testing.T) {
	t.Parallel()
	base := CreateRequestDigest("title", "sha256:"+strings.Repeat("a", 64))
	if base == CreateRequestDigest("other", "sha256:"+strings.Repeat("a", 64)) {
		t.Fatal("title change did not change the request digest")
	}
	if base == CreateRequestDigest("title", "sha256:"+strings.Repeat("b", 64)) {
		t.Fatal("bundle digest change did not change the request digest")
	}
	if base != CreateRequestDigest("title", "sha256:"+strings.Repeat("a", 64)) {
		t.Fatal("identical request digests differ")
	}
	if !ValidArtifactDigest(base) {
		t.Fatal("request digest is not in canonical form")
	}
}

func TestValidBundleAssetPath(t *testing.T) {
	t.Parallel()
	valid := []string{"index.html", "assets/app.js", "assets/deep/x.css", "f000.html"}
	invalid := []string{"../x", "/x", "a//b", `a\b`, "a%2Fb", ".hidden", "a/.", "no-ext", "a.exe"}
	for _, path := range valid {
		if !ValidBundleAssetPath(path) {
			t.Errorf("valid asset path %q rejected", path)
		}
	}
	for _, path := range invalid {
		if ValidBundleAssetPath(path) {
			t.Errorf("invalid asset path %q accepted", path)
		}
	}
}

// TestValidArtifactUUIDIsCanonicalV7 pins the identifier grammar on the
// public Get/List boundaries: only canonical lowercase UUIDv7 identifiers
// pass, so v1/v4 shapes, wrong variants, and uppercase spellings are invalid
// arguments before storage is consulted.
func TestValidArtifactUUIDIsCanonicalV7(t *testing.T) {
	t.Parallel()
	canonical := "0198d7ea-2110-7c42-b659-c5e4d73bc337"
	if !ValidArtifactUUID(canonical) {
		t.Fatal("canonical UUIDv7 rejected")
	}
	for name, value := range map[string]string{
		"empty":       "",
		"uppercase":   "0198D7EA-2110-7C42-B659-C5E4D73BC337",
		"version 4":   "9f8ee16a-4b46-4a8e-a6cc-82919bf8d0a8",
		"version 1":   "0198d7ea-2110-1c42-b659-c5e4d73bc337",
		"bad variant": "0198d7ea-2110-7c42-c659-c5e4d73bc337",
		"nil variant": "0198d7ea-2110-7c42-0659-c5e4d73bc337",
		"short":       canonical[:35],
		"bad hyphen":  "0198d7ea-2110-7c42Xb659-c5e4d73bc337",
		"non-hex":     "0198d7ea-2110-7c42-b659-c5e4d73bc33g",
	} {
		if ValidArtifactUUID(value) {
			t.Errorf("%s (%q) accepted", name, value)
		}
	}
}
