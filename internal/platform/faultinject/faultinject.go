//go:build faultinject

// Package faultinject provides deterministic hooks only in explicit test builds.
// Normal builds cannot enable injection, even through environment variables.
package faultinject

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const envDir = "WORKOS_FAULT_DIR"

// Enabled reports whether the current process is running with a test fault directory.
func Enabled() bool {
	return strings.TrimSpace(os.Getenv(envDir)) != ""
}

func dir() string { return strings.TrimSpace(os.Getenv(envDir)) }

// Arrive records that a named persistence window was reached. If a wait file
// for that stage exists, it blocks until the matching release file appears,
// the context ends, or two minutes elapse. Timeouts log the stage name only.
func Arrive(ctx context.Context, stage string) {
	if !Enabled() || stage == "" {
		return
	}
	root := dir()
	wait := filepath.Join(root, "wait-"+stage)
	armed, err := os.ReadFile(wait)
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(root, "arrived-"+stage), armed, 0o600)
	release := filepath.Join(root, "release-"+stage)
	deadline := time.Now().Add(2 * time.Minute)
	for {
		current, err := os.ReadFile(wait)
		if err != nil || !bytes.Equal(current, armed) {
			return // Disarming/rearming cannot strand a previous waiter.
		}
		if ctx != nil && ctx.Err() != nil {
			slog.Info("faultinject wait interrupted", "stage", stage)
			return
		}
		if _, err := os.Stat(release); err == nil {
			return
		}
		if time.Now().After(deadline) {
			slog.Info("faultinject wait timed out", "stage", stage)
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// SkipLeaseRenew reports that the current holder must stop extending its lease
// so a competing worker can take over after TTL.
func SkipLeaseRenew() bool {
	if !Enabled() {
		return false
	}
	_, err := os.Stat(filepath.Join(dir(), "expire-lease"))
	return err == nil
}

func consumeDrop(path string) bool {
	if !Enabled() || path == "" {
		return false
	}
	method := path[strings.LastIndex(path, "/")+1:]
	marker := filepath.Join(dir(), "drop-once-"+method)
	// Removal is the one-shot claim; concurrent callers must not all consume
	// the same marker after observing it with Stat.
	return os.Remove(marker) == nil
}

type capturedWriter struct {
	header http.Header
	code   int
}

func (w *capturedWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}
func (w *capturedWriter) Write(p []byte) (int, error) {
	if w.code == 0 {
		w.code = http.StatusOK
	}
	return len(p), nil
}
func (w *capturedWriter) WriteHeader(code int) {
	if w.code == 0 {
		w.code = code
	}
}

// DropReply wraps a handler so a one-shot drop file causes the real handler
// and database commit to finish, then the caller never receives the bytes.
func DropReply(next http.Handler) http.Handler {
	if next == nil {
		return http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !consumeDrop(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		captured := &capturedWriter{}
		next.ServeHTTP(captured, r)
		// net/http closes HTTP/1 connections and resets HTTP/2 streams for
		// ErrAbortHandler. Returning normally would fabricate an empty 200
		// on transports without Hijacker (notably the private HTTP/2 RPCs).
		panic(http.ErrAbortHandler)
	})
}
