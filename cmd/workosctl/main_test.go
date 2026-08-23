package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProbeReadiness(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/readyz" {
			t.Errorf("unexpected path %q", request.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	if err := probeReadiness(context.Background(), server.Client(), server.URL+"/"); err != nil {
		t.Fatal(err)
	}
}

func TestProbeReadinessRejectsDegradedService(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	if err := probeReadiness(context.Background(), server.Client(), server.URL); err == nil {
		t.Fatal("expected unavailable readiness to fail")
	}
}
