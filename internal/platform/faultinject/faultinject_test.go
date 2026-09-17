//go:build faultinject

package faultinject

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestArriveNoopsWithoutDir(t *testing.T) {
	t.Setenv(envDir, "")
	done := make(chan struct{})
	go func() {
		Arrive(context.Background(), "never")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("no-op arrive blocked")
	}
}

func TestDropReplyAbortsHTTP2AfterCommit(t *testing.T) {
	root := t.TempDir()
	t.Setenv(envDir, root)
	if err := os.WriteFile(filepath.Join(root, "drop-once-Commit"), []byte("1"), 0600); err != nil {
		t.Fatal(err)
	}
	var commits atomic.Int32
	server := httptest.NewUnstartedServer(DropReply(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		commits.Add(1)
		_, _ = w.Write([]byte(`{"committed":true}`))
	})))
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)
	response, err := server.Client().Post(server.URL+"/Commit", "application/json", nil)
	if err == nil {
		response.Body.Close()
		t.Fatal("HTTP/2 drop returned a fabricated response")
	}
	if commits.Load() != 1 {
		t.Fatal("reply was dropped before handler committed")
	}
	response, err = server.Client().Post(server.URL+"/Commit", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.ProtoMajor != 2 || response.StatusCode != http.StatusOK || commits.Load() != 2 {
		t.Fatal("one-shot drop affected the retry")
	}
}

func TestConcurrentDropClaimsExactlyOnce(t *testing.T) {
	root := t.TempDir()
	t.Setenv(envDir, root)
	if err := os.WriteFile(filepath.Join(root, "drop-once-Commit"), []byte("1"), 0600); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan bool, 32)
	for range cap(results) {
		go func() { <-start; results <- consumeDrop("/Commit") }()
	}
	close(start)
	claims := 0
	for range cap(results) {
		if <-results {
			claims++
		}
	}
	if claims != 1 {
		t.Fatalf("single marker claimed %d times", claims)
	}
}

func TestArriveWaitsForRelease(t *testing.T) {
	root := t.TempDir()
	t.Setenv(envDir, root)
	if err := os.WriteFile(filepath.Join(root, "wait-commit"), []byte("1"), 0o600); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		close(started)
		Arrive(context.Background(), "commit")
		close(finished)
	}()
	<-started
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(root, "arrived-commit")); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	select {
	case <-finished:
		t.Fatal("released before wait file was satisfied")
	default:
	}
	if err := os.WriteFile(filepath.Join(root, "release-commit"), []byte("1"), 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("did not resume after release")
	}
}

func TestDropReplyClosesAfterHandler(t *testing.T) {
	root := t.TempDir()
	t.Setenv(envDir, root)
	if err := os.WriteFile(filepath.Join(root, "drop-once-SubmitBuildTest"), []byte("1"), 0o600); err != nil {
		t.Fatal(err)
	}
	handled := make(chan struct{})
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(handled)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	server := httptest.NewServer(DropReply(inner))
	t.Cleanup(server.Close)
	response, err := http.Post(server.URL+"/workos.taskexecution.v1.BuildTestService/SubmitBuildTest", "application/json", nil)
	if err == nil {
		defer response.Body.Close()
		body, _ := io.ReadAll(response.Body)
		if len(body) > 0 && response.StatusCode == http.StatusOK {
			t.Fatalf("drop-reply still delivered body %q", body)
		}
	}
	select {
	case <-handled:
	case <-time.After(5 * time.Second):
		t.Fatal("handler must run before the reply is dropped")
	}
	if _, err := os.Stat(filepath.Join(root, "drop-once-SubmitBuildTest")); err == nil {
		t.Fatal("one-shot drop marker must be consumed")
	}
}
