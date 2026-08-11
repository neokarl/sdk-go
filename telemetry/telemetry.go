// Package telemetry wires OpenTelemetry tracing for a service. Call Init once at
// startup with the service name; the SDK's HTTP and gRPC servers/clients are
// already instrumented (otelecho / otelgrpc), so all this does is set up the
// exporter, resource, and W3C trace-context propagator, then return a shutdown.
//
// Exporter selection (env):
//   - OTEL_EXPORTER_OTLP_ENDPOINT set → OTLP (a collector, Jaeger, or Elastic
//     APM Server). Protocol from OTEL_EXPORTER_OTLP_PROTOCOL: "http/protobuf"
//     (or "http/json") → OTLP/HTTP, anything else → OTLP/gRPC (default).
//   - otherwise                       → stdout (traces visible in dev, zero infra)
//   - OTEL_TRACES=off                 → no-op (tracing disabled)
package telemetry

import (
	"context"
	"os"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// Init configures the global tracer provider and propagator for serviceName and
// returns a shutdown function to flush spans on exit.
func Init(ctx context.Context, serviceName string) (func(context.Context) error, error) {
	// W3C traceparent + baggage, so context flows across HTTP and gRPC hops.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))

	if os.Getenv("OTEL_TRACES") == "off" {
		otel.SetTracerProvider(noop.NewTracerProvider())
		return func(context.Context) error { return nil }, nil
	}

	var (
		exp sdktrace.SpanExporter
		err error
	)
	switch {
	case os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == "":
		exp, err = stdouttrace.New(stdouttrace.WithPrettyPrint())
	case strings.HasPrefix(os.Getenv("OTEL_EXPORTER_OTLP_PROTOCOL"), "http"):
		exp, err = otlptracehttp.New(ctx)
	default:
		exp, err = otlptracegrpc.New(ctx)
	}
	if err != nil {
		return nil, err
	}

	res, _ := resource.New(ctx, resource.WithAttributes(
		attribute.String("service.name", serviceName),
	))
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)
	return tp.Shutdown, nil
}
