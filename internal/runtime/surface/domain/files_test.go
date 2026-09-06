package domain

import "testing"

func TestFileReferenceGrammar(t *testing.T) {
	for _, value := range []string{"", "/etc/passwd", "../x", "notes/../../x", "a//b", "a/./b", ".env", "x/.git/y", "a\\b", "a:b", "a\x00b", "a/b/c/d/e/f/g/h/i"} {
		if ValidFilePath(value, false) {
			t.Fatalf("accepted %q", value)
		}
	}
	for _, value := range []string{"notes.txt", "notes/设计.md", "space name.txt"} {
		if !ValidFilePath(value, false) {
			t.Fatalf("rejected %q", value)
		}
	}
	if !ValidFilePath("", true) {
		t.Fatal("root directory rejected")
	}
	if got := WorkspaceCapabilities([]string{"files.read", "files.write"}, false); len(got) != 2 || got[0] != "files.pick" || got[1] != "files.read" {
		t.Fatal(got)
	}
	if got := WorkspaceCapabilities([]string{"files.read", "files.write"}, true); len(got) != 3 {
		t.Fatal(got)
	}
	if got := WorkspaceCapabilities([]string{"project.read", "artifact.write"}, true); len(got) != 0 {
		t.Fatal(got)
	}
}
