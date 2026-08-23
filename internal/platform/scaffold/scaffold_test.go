package scaffold

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCreateModuleBuildsBoundariesWithoutOverwriting(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	target, err := CreateModule(root, "workos-core", "calendar")
	if err != nil {
		t.Fatal(err)
	}
	for _, layer := range []string{"domain", "application", "ports", "adapters", "transport"} {
		if _, err := os.Stat(filepath.Join(target, layer, "doc.go")); err != nil {
			t.Errorf("missing %s layer: %v", layer, err)
		}
	}
	if _, err := CreateModule(root, "workos-core", "calendar"); err == nil {
		t.Fatal("expected existing module to be rejected")
	}
}

func TestCreateModuleRejectsUnknownProcessAndUnsafeName(t *testing.T) {
	t.Parallel()
	for _, test := range []struct{ process, name string }{
		{"other", "calendar"},
		{"workos-core", "../calendar"},
		{"workos-core", "Calendar"},
	} {
		if _, err := CreateModule(t.TempDir(), test.process, test.name); err == nil {
			t.Errorf("expected %q/%q to be rejected", test.process, test.name)
		}
	}
}
