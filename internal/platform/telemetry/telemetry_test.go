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
