//go:build !faultinject

package faultinject

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestProductionIgnoresFaultConfiguration(t *testing.T) {
	root := t.TempDir()
	t.Setenv("WORKOS_FAULT_DIR", root)
	for _, name := range []string{"wait-commit", "expire-lease", "drop-once-Submit"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("1"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if Enabled() || SkipLeaseRenew() {
		t.Fatal("production build enabled fault injection")
	}
	Arrive(context.Background(), "commit")
	if _, err := os.Stat(filepath.Join(root, "arrived-commit")); !os.IsNotExist(err) {
		t.Fatalf("production hook accessed the fault directory: %v", err)
	}
	response := httptest.NewRecorder()
	DropReply(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	})).ServeHTTP(response, httptest.NewRequest("POST", "/Submit", nil))
	if response.Code != http.StatusAccepted {
		t.Fatal("production hook changed the response")
	}
}
