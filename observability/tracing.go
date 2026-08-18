// Package observability wires the two things every service needs to be
// debuggable in production: structured logging and distributed tracing.
//
// # Logging
//
// [NewLogger] builds the platform's slog logger; [LoggerFrom] reads the
// per-request logger the SDK's HTTP middleware installs.
//
// # Tracing
//
// [NewTracing] configures an OpenTelemetry tracer provider and returns it —
// deliberately without installing it as the process-global provider, because a
// library has no business overwriting tracing configuration its host
// application may already own. Hand the provider to the SDK components that
// need it:
//
//	tr, err := observability.NewTracing(ctx, observability.TracingConfig{
//	    ServiceName: "inventory",
//	    Endpoint:    os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"),
//	})
//	if err != nil {
//	    return err
//	}
//	defer tr.Close(context.Background())
//
//	svc := service.New(manifest, service.WithTracing(tr))
//
// If you do want the global provider set — because you also use OTel directly,
// or a third-party library does — say so explicitly with [WithGlobal].
package observability

import (
	"context"
	"fmt"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// Exporter selects where spans are sent.
type Exporter string

const (
	// ExporterAuto picks OTLP when TracingConfig.Endpoint is set and stdout
	// otherwise. This is the default and suits both dev and production.
	ExporterAuto Exporter = ""
	// ExporterOTLP sends spans to a collector over OTLP. Requires Endpoint.
	ExporterOTLP Exporter = "otlp"
	// ExporterStdout pretty-prints spans to stdout. Development only.
	ExporterStdout Exporter = "stdout"
	// ExporterNone disables tracing. The returned Tracing is inert but safe to
	// use and to Close.
	ExporterNone Exporter = "none"
)

// TracingConfig configures [NewTracing].
//
// Every field is explicit — nothing is read from the environment. Read the
// standard OTEL_* variables yourself if you want env-driven configuration, so
// the decision stays visible in your service's startup code:
//
//	cfg := observability.TracingConfig{
//	    ServiceName: "inventory",
//	    Endpoint:    os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"),
//	}
type TracingConfig struct {
	// ServiceName is reported as the OTel service.name resource attribute.
	// Required.
	ServiceName string
	// ServiceVersion is reported as service.version. Optional.
	ServiceVersion string
	// Exporter selects the span destination. Zero value is [ExporterAuto].
	Exporter Exporter
	// Endpoint is the OTLP collector address. Required for [ExporterOTLP];
	// when set under [ExporterAuto] it selects OTLP.
	Endpoint string
	// Protocol chooses the OTLP wire protocol. A value starting with "http"
	// selects OTLP/HTTP; anything else, including empty, selects OTLP/gRPC.
	Protocol string
	// Global installs the resulting provider and propagator as the process-wide
	// OpenTelemetry defaults. Off by default — see [WithGlobal].
	Global bool
}

// Tracing is a configured tracer provider and its shutdown.
type Tracing struct {
	provider trace.TracerProvider
	shutdown func(context.Context) error
	// Propagator is the W3C trace-context + baggage propagator this service
	// should use, so context flows across HTTP and gRPC hops.
	Propagator propagation.TextMapPropagator
}

// WithGlobal marks a config as installing the process-global OpenTelemetry
// tracer provider and propagator.
//
// Do this only in a service you own end to end — a binary whose main you wrote.
// It overwrites whatever tracing configuration is already installed, which is
// why it is not the default.
func WithGlobal(cfg TracingConfig) TracingConfig {
	cfg.Global = true
	return cfg
}

// NewTracing configures tracing for a service and returns it. Call Close on
// shutdown to flush buffered spans.
//
// It does not touch process-global OpenTelemetry state unless
// TracingConfig.Global is set.
func NewTracing(ctx context.Context, cfg TracingConfig) (*Tracing, error) {
	if cfg.ServiceName == "" {
		return nil, fmt.Errorf("observability: TracingConfig.ServiceName is required")
	}

	propagator := propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	)

	exporter := cfg.Exporter
	if exporter == ExporterAuto {
		if cfg.Endpoint != "" {
			exporter = ExporterOTLP
		} else {
			exporter = ExporterStdout
		}
	}

	if exporter == ExporterNone {
		t := &Tracing{
			provider:   noop.NewTracerProvider(),
			shutdown:   func(context.Context) error { return nil },
			Propagator: propagator,
		}
		t.installGlobal(cfg.Global)
		return t, nil
	}

	exp, err := newExporter(ctx, exporter, cfg)
	if err != nil {
		return nil, err
	}

	attrs := []attribute.KeyValue{attribute.String("service.name", cfg.ServiceName)}
	if cfg.ServiceVersion != "" {
		attrs = append(attrs, attribute.String("service.version", cfg.ServiceVersion))
	}
	res, err := resource.New(ctx, resource.WithAttributes(attrs...))
	if err != nil {
		return nil, fmt.Errorf("observability: build resource: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(res),
	)
	t := &Tracing{provider: tp, shutdown: tp.Shutdown, Propagator: propagator}
	t.installGlobal(cfg.Global)
	return t, nil
}

func newExporter(ctx context.Context, e Exporter, cfg TracingConfig) (sdktrace.SpanExporter, error) {
	switch e {
	case ExporterStdout:
		return stdouttrace.New(stdouttrace.WithPrettyPrint())
	case ExporterOTLP:
		if cfg.Endpoint == "" {
			return nil, fmt.Errorf("observability: Exporter %q requires Endpoint", e)
		}
		if strings.HasPrefix(cfg.Protocol, "http") {
			return otlptracehttp.New(ctx, otlptracehttp.WithEndpointURL(cfg.Endpoint))
		}
		return otlptracegrpc.New(ctx, otlptracegrpc.WithEndpointURL(cfg.Endpoint))
	default:
		return nil, fmt.Errorf("observability: unknown exporter %q", e)
	}
}

func (t *Tracing) installGlobal(global bool) {
	if !global {
		return
	}
	otel.SetTracerProvider(t.provider)
	otel.SetTextMapPropagator(t.Propagator)
}

// Provider returns the tracer provider. Pass it to SDK constructors that accept
// one, or use it directly to create tracers.
func (t *Tracing) Provider() trace.TracerProvider { return t.provider }

// Close flushes buffered spans and shuts the exporter down. Safe to call on a
// Tracing built with [ExporterNone].
func (t *Tracing) Close(ctx context.Context) error { return t.shutdown(ctx) }
