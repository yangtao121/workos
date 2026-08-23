package telemetry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSetupWithOTLPEndpoint(t *testing.T) {
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer collector.Close()
	shutdown, err := Setup(context.Background(), "telemetry-test", collector.URL)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := shutdown(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestTraceEndpointUsesStandardOTLPPath(t *testing.T) {
	t.Parallel()
	for input, want := range map[string]string{
		"http://collector:4318":           "http://collector:4318/v1/traces",
		"https://collector/base/":         "https://collector/base/v1/traces",
		"http://collector:4318/v1/traces": "http://collector:4318/v1/traces",
	} {
		got, err := traceEndpointURL(input)
		if err != nil || got != want {
			t.Errorf("traceEndpointURL(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
	if _, err := traceEndpointURL("collector:4318"); err == nil {
		t.Fatal("expected endpoint without scheme to fail")
	}
}

func TestTelemetryFiltersProbeAndEmptyLeasePolling(t *testing.T) {
	t.Parallel()
	for _, path := range []string{
		"/healthz", "/readyz", "/workos.taskexecution.v1.TaskExecutionService/ClaimTask",
	} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		if shouldTrace(request) {
			t.Errorf("expected noisy path %s to be filtered", path)
		}
	}
	request := httptest.NewRequest(http.MethodPost, "/workos.agent.v1.AgentTaskService/SubmitTask", nil)
	if !shouldTrace(request) {
		t.Fatal("expected user-facing operation to be traced")
	}
}
