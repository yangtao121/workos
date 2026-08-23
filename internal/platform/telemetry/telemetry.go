// Package telemetry owns process-wide OpenTelemetry setup and HTTP
// instrumentation. Services remain fully functional when no exporter is set.
package telemetry

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
)

// Setup installs an OTLP/HTTP trace provider. With an empty endpoint it keeps
// the default no-op provider and returns a no-op shutdown function.
func Setup(ctx context.Context, service, endpoint string) (func(context.Context) error, error) {
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))
	if endpoint == "" {
		return func(context.Context) error { return nil }, nil
	}
	traceEndpoint, err := traceEndpointURL(endpoint)
	if err != nil {
		return nil, err
	}
	exporter, err := otlptracehttp.New(ctx, otlptracehttp.WithEndpointURL(traceEndpoint))
	if err != nil {
		return nil, fmt.Errorf("create OTLP trace exporter: %w", err)
	}
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
	otel.SetTracerProvider(provider)
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
	return otelhttp.NewHandler(handler, service, otelhttp.WithFilter(shouldTrace))
}

// HTTPClient propagates trace context on internal HTTP and Connect calls.
func HTTPClient() *http.Client {
	return &http.Client{Transport: otelhttp.NewTransport(http.DefaultTransport, otelhttp.WithFilter(shouldTrace))}
}

func shouldTrace(request *http.Request) bool {
	switch request.URL.Path {
	case "/healthz", "/readyz", "/workos.taskexecution.v1.TaskExecutionService/ClaimTask":
		return false
	default:
		return true
	}
}
