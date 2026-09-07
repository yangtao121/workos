package domain

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestWorkspacePathGrammar(t *testing.T) {
	for _, path := range []string{"../a.md", "a/../b.md", "a\\b.md", "a\nb.md", "a\x00.md", "\xff.md", strings.Repeat("a", 256) + ".md", strings.Repeat("a/", WorkspaceMaxDepth) + "b.md"} {
		if _, err := WorkspaceRelPath(path); err == nil {
			t.Errorf("accepted %q", path)
		}
	}
	path := strings.Repeat("中", 80) + "/" + strings.Repeat("文", 80) + "/" + strings.Repeat("件", 80) + ".md"
	title, err := WorkspaceRelPath(path)
	if err != nil || !utf8.ValidString(title) || utf8.RuneCountInString(title) != 200 || !strings.HasSuffix(title, ".md") {
		t.Fatalf("title %q: %v", title, err)
	}
	if _, err := WorkspaceRelPath("..notes.md"); err != nil {
		t.Fatalf("valid dot-prefixed name: %v", err)
	}
	if ValidWorkspaceRoot("/") || ValidWorkspaceRoot("/invalid\xff") {
		t.Fatal("invalid root accepted")
	}
}
