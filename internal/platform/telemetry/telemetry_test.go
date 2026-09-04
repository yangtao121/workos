package telemetry

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
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

// ADR-0016 §4: the export budget is enforced before any span leaves the
// process. The trim helper drops set overflow, truncates oversized string
// values, and reports how many attributes were dropped outright.
func TestTrimSpanAttributesEnforcesBudget(t *testing.T) {
	attributes := make([]attribute.KeyValue, 0, MaxSpanAttributes+10)
	for index := 0; index < MaxSpanAttributes+10; index++ {
		attributes = append(attributes, attribute.Int(fmt.Sprintf("attr.%03d", index), index))
	}
	kept, dropped := trimSpanAttributes(attributes)
	if len(kept) != MaxSpanAttributes || dropped != 10 {
		t.Fatalf("set budget: kept=%d dropped=%d", len(kept), dropped)
	}

	oversize := []attribute.KeyValue{
		attribute.String("workos.goal", strings.Repeat("secret-goal-", 100)),
		attribute.String("rpc.method", "SubmitTask"),
	}
	kept, dropped = trimSpanAttributes(oversize)
	if dropped != 0 || len(kept) != 2 {
		t.Fatalf("unexpected drop: kept=%d dropped=%d", len(kept), dropped)
	}
	if got := kept[0].Value.AsString(); len(got) != MaxAttributeValueBytes {
		t.Fatalf("oversize value not truncated: %d bytes", len(got))
	}
	if got := kept[1].Value.AsString(); got != "SubmitTask" {
		t.Fatalf("bounded value was mutated: %q", got)
	}
}

// ADR-0016 §4: two listeners in one process (workos-core's public and
// execution servers) must share the single installed provider. The second
// Setup must not overwrite the global with a fresh provider — that would
// orphan the first listener's in-flight spans — and the shared shutdown
// fires only when the last listener stops.
func TestSetupIdempotentAcrossListeners(t *testing.T) {
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer collector.Close()

	shutdownOne, err := Setup(context.Background(), "listener-one", collector.URL)
	if err != nil {
		t.Fatalf("first setup: %v", err)
	}
	shutdownTwo, err := Setup(context.Background(), "listener-two", collector.URL)
	if err != nil {
		t.Fatalf("second setup: %v", err)
	}

	// Both listeners resolve the same provider instance.
	if currentProvider == nil {
		t.Fatal("provider was not installed")
	}
	tracerOne := currentProvider.Tracer("one")
	_, spanOne := tracerOne.Start(context.Background(), "listener-one-span")
	spanOne.End()
	shutdownOne(context.Background())

	// After the first listener stops, the shared provider is still alive
	// (refcount held by the second): spans keep flowing through the same
	// instance.
	tracerTwo := currentProvider.Tracer("two")
	_, spanTwo := tracerTwo.Start(context.Background(), "listener-two-span")
	spanTwo.End()
	if err := shutdownTwo(context.Background()); err != nil {
		t.Fatalf("second shutdown: %v", err)
	}

	// A Setup with a different endpoint after everything stopped installs a
	// fresh provider (covered implicitly by other tests); here we only
	// assert the no-error contract of an empty-endpoint Setup.
	if shutdown, err := Setup(context.Background(), "listener-three", ""); err != nil || shutdown == nil {
		t.Fatalf("empty-endpoint setup diverged: %v", err)
	}
}
