// Package telemetry owns process-wide OpenTelemetry setup and HTTP
// instrumentation. Services remain fully functional when no exporter is set.
package telemetry

import (
	"crypto/tls"

	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	oteltrace "go.opentelemetry.io/otel/trace"
)

// currentProvider is the provider Setup installed, if any. Handlers resolve
// through it explicitly: otelhttp captures the provider at construction
// time, so the instrumentation in Handler must see the same provider Setup
// configured, never an import-order-dependent global.
var (
	currentProvider oteltrace.TracerProvider
	otelLogger      = slog.Default().With("component", "telemetry")
)

// Setup installs an OTLP/HTTP trace provider. With an empty endpoint it keeps
// the default no-op provider and returns a no-op shutdown function.
func Setup(ctx context.Context, service, endpoint string) (func(context.Context) error, error) {
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))
	// Export/retry failures are otherwise silent: route them through the
	// standard logger so OTLP delivery problems are diagnosable (ADR-0016).
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		otelLogger.Error("telemetry export failed", "error", err)
	}))
	if endpoint == "" {
		return func(context.Context) error { return nil }, nil
	}
	traceEndpoint, err := traceEndpointURL(endpoint)
	if err != nil {
		return nil, err
	}
	otlpExporter, err := otlptracehttp.New(ctx, otlptracehttp.WithEndpointURL(traceEndpoint))
	if err != nil {
		return nil, fmt.Errorf("create OTLP trace exporter: %w", err)
	}
	exporter := &boundsExporter{next: otlpExporter}
	resources, err := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(semconv.SchemaURL, semconv.ServiceName(service)),
	)
	if err != nil {
		return nil, fmt.Errorf("create telemetry resource: %w", err)
	}
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(resources),
	)
	provider.RegisterSpanProcessor(NewExportCounter())
	otel.SetTracerProvider(provider)
	currentProvider = provider
	return provider.Shutdown, nil
}

func traceEndpointURL(endpoint string) (string, error) {
	value, err := url.Parse(endpoint)
	if err != nil || (value.Scheme != "http" && value.Scheme != "https") || value.Host == "" {
		return "", fmt.Errorf("invalid OTLP endpoint %q", endpoint)
	}
	value.Path = strings.TrimRight(value.Path, "/")
	if !strings.HasSuffix(value.Path, "/v1/traces") {
		value.Path += "/v1/traces"
	}
	return value.String(), nil
}

// Handler instruments inbound HTTP and Connect requests while preserving the
// original handler's protocol behavior.
func Handler(service string, handler http.Handler) http.Handler {
	options := []otelhttp.Option{otelhttp.WithFilter(shouldTrace)}
	if currentProvider != nil {
		options = append(options, otelhttp.WithTracerProvider(currentProvider))
	}
	return otelhttp.NewHandler(handler, service, options...)
}

// HTTPClient propagates trace context on internal HTTP and Connect calls.
func HTTPClient() *http.Client {
	return &http.Client{Transport: otelhttp.NewTransport(http.DefaultTransport, otelhttp.WithFilter(shouldTrace))}
}

// HTTPClientWithTLS builds a client whose base transport carries an explicit
// TLS configuration (the private mTLS execution channel) while keeping the
// same trace propagation behavior as HTTPClient.
func HTTPClientWithTLS(tlsConfig *tls.Config) *http.Client {
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.TLSClientConfig = tlsConfig
	return &http.Client{Transport: otelhttp.NewTransport(base, otelhttp.WithFilter(shouldTrace))}
}

func shouldTrace(request *http.Request) bool {
	switch request.URL.Path {
	case "/healthz", "/readyz", "/workos.taskexecution.v1.TaskExecutionService/ClaimTask":
		return false
	default:
		return true
	}
}

// ---------------------------------------------------------------------------
// In-process export budget (ADR-0016 §4). The collector is an external
// dependency, so sanitization must happen before export: the OTLP exporter is
// wrapped so every exported span is trimmed to a bounded attribute set with
// bounded string values, and every dropped attribute is counted and marked on
// the span itself.
// ---------------------------------------------------------------------------

// MaxSpanAttributes bounds the attribute set of one span.
const (
	MaxSpanAttributes      = 64
	MaxAttributeValueBytes = 512
)

// DroppedAttributesKey marks, on spans that exceeded the attribute budget,
// how many attributes the exporter wrapper removed before export.
const DroppedAttributesKey = "workos.telemetry.attributes_dropped"

// ExportCounter observes completed export batches so capability discovery
// and tests can prove spans really flow (ADR-0016 §4).
var ExportCounter atomic.Uint64

// NewExportCounter constructs the batch observer.
func NewExportCounter() sdktrace.SpanProcessor { return exportCounter{} }

type exportCounter struct{}

func (exportCounter) OnStart(context.Context, sdktrace.ReadWriteSpan) {}
func (exportCounter) OnEnd(sdktrace.ReadOnlySpan)                     {}
func (exportCounter) Shutdown(context.Context) error                  { return nil }
func (exportCounter) ForceFlush(context.Context) error                { return nil }

// trimSpanAttributes enforces the attribute budget: at most
// MaxSpanAttributes entries, string values truncated to
// MaxAttributeValueBytes. It returns the kept set and how many attributes
// were dropped outright.
func trimSpanAttributes(attributes []attribute.KeyValue) ([]attribute.KeyValue, int) {
	kept := make([]attribute.KeyValue, 0, len(attributes))
	dropped := 0
	for index, kv := range attributes {
		if index >= MaxSpanAttributes {
			dropped += len(attributes) - MaxSpanAttributes
			break
		}
		if kv.Value.Type() == attribute.STRING {
			if value := kv.Value.AsString(); len(value) > MaxAttributeValueBytes {
				kv.Value = attribute.StringValue(value[:MaxAttributeValueBytes])
			}
		}
		kept = append(kept, kv)
	}
	return kept, dropped
}

// boundsExporter wraps the OTLP exporter with the ADR-0016 §4 export budget.
type boundsExporter struct {
	next    sdktrace.SpanExporter
	dropped atomic.Uint64
}

// trimmedSpan delegates everything to the original span except the
// attribute set, which is replaced with the budget-enforced one.
type trimmedSpan struct {
	sdktrace.ReadOnlySpan
	attributes []attribute.KeyValue
}

func (s *trimmedSpan) Attributes() []attribute.KeyValue { return s.attributes }

func (e *boundsExporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	trimmed := make([]sdktrace.ReadOnlySpan, 0, len(spans))
	for _, span := range spans {
		kept, dropped := trimSpanAttributes(span.Attributes())
		if dropped > 0 {
			e.dropped.Add(uint64(dropped))
			kept = append(kept, attribute.KeyValue{
				Key: attribute.Key(DroppedAttributesKey), Value: attribute.Int64Value(int64(dropped)),
			})
		}
	}
	return e.next.ExportSpans(ctx, trimmed)
}

func (e *boundsExporter) Shutdown(ctx context.Context) error { return e.next.Shutdown(ctx) }
