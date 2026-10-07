package observability

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
)

const scope = "smartfactory/observability"

var propagator = propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{})

type runtimeOptions struct {
	spanExporter sdktrace.SpanExporter
	metricReader sdkmetric.Reader
}
type Option func(*runtimeOptions)

// WithSpanExporter and WithMetricReader allow deterministic tests or a host-managed exporter.
func WithSpanExporter(exporter sdktrace.SpanExporter) Option {
	return func(o *runtimeOptions) { o.spanExporter = exporter }
}
func WithMetricReader(reader sdkmetric.Reader) Option {
	return func(o *runtimeOptions) { o.metricReader = reader }
}

type Runtime struct {
	traces   trace.TracerProvider
	metrics  metric.MeterProvider
	shutdown func(context.Context) error
	flush    func(context.Context) error
	once     sync.Once
	closeErr error
}

// New creates process-owned providers. Install is an explicit separate action.
func New(ctx context.Context, config Config, options ...Option) (*Runtime, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if config.Disabled {
		return &Runtime{traces: tracenoop.NewTracerProvider(), metrics: metricnoop.NewMeterProvider()}, nil
	}
	o := runtimeOptions{}
	for _, option := range options {
		option(&o)
	}
	if config.ExportTimeout == 0 {
		config.ExportTimeout = 5 * time.Second
	}
	if config.MetricInterval == 0 {
		config.MetricInterval = 30 * time.Second
	}
	if o.spanExporter == nil && config.TraceExporter == "otlp" {
		exporter, err := otlptracehttp.New(ctx, otlptracehttp.WithEndpointURL(signalEndpoint(config.Endpoint, "traces")), otlptracehttp.WithTimeout(config.ExportTimeout))
		if err != nil {
			return nil, fmt.Errorf("create OTLP trace exporter: %w", err)
		}
		o.spanExporter = exporter
	}
	if o.metricReader == nil && config.MetricExporter == "otlp" {
		exporter, err := otlpmetrichttp.New(ctx, otlpmetrichttp.WithEndpointURL(signalEndpoint(config.Endpoint, "metrics")), otlpmetrichttp.WithTimeout(config.ExportTimeout))
		if err != nil {
			if o.spanExporter != nil {
				_ = o.spanExporter.Shutdown(ctx)
			}
			return nil, fmt.Errorf("create OTLP metric exporter: %w", err)
		}
		o.metricReader = sdkmetric.NewPeriodicReader(exporter, sdkmetric.WithInterval(config.MetricInterval), sdkmetric.WithTimeout(config.ExportTimeout))
	}
	res := resource.NewSchemaless(
		attribute.String("service.name", config.ServiceName), attribute.String("service.version", config.ServiceVersion),
		attribute.String("service.instance.id", config.NodeID), attribute.String("deployment.environment.name", config.Environment),
	)
	traceOptions := []sdktrace.TracerProviderOption{sdktrace.WithResource(res), sdktrace.WithSampler(sampler(config))}
	if o.spanExporter != nil {
		traceOptions = append(traceOptions, sdktrace.WithBatcher(o.spanExporter, sdktrace.WithExportTimeout(config.ExportTimeout)))
	}
	tp := sdktrace.NewTracerProvider(traceOptions...)
	metricOptions := []sdkmetric.Option{sdkmetric.WithResource(res)}
	if o.metricReader != nil {
		metricOptions = append(metricOptions, sdkmetric.WithReader(o.metricReader))
	}
	mp := sdkmetric.NewMeterProvider(metricOptions...)
	return &Runtime{traces: tp, metrics: mp, shutdown: func(ctx context.Context) error { return errors.Join(tp.Shutdown(ctx), mp.Shutdown(ctx)) }, flush: func(ctx context.Context) error { return errors.Join(tp.ForceFlush(ctx), mp.ForceFlush(ctx)) }}, nil
}

// Install is called once during service startup, before serving requests.
func (r *Runtime) Install() {
	otel.SetTracerProvider(r.traces)
	otel.SetMeterProvider(r.metrics)
	otel.SetTextMapPropagator(propagator)
}

func (r *Runtime) Shutdown(ctx context.Context) error {
	if r == nil {
		return nil
	}
	r.once.Do(func() {
		if r.shutdown != nil {
			r.closeErr = r.shutdown(ctx)
		}
	})
	return r.closeErr
}

func (r *Runtime) ForceFlush(ctx context.Context) error {
	if r == nil || r.flush == nil {
		return nil
	}
	return r.flush(ctx)
}

func (r *Runtime) StartOperation(ctx context.Context, operation string, identity Identity) (context.Context, func(error)) {
	return startOperation(ctx, r.traces.Tracer(scope), r.metrics.Meter(scope), operation, identity)
}

func StartOperation(ctx context.Context, operation string, identity Identity) (context.Context, func(error)) {
	return startOperation(ctx, otel.Tracer(scope), otel.Meter(scope), operation, identity)
}

func signalEndpoint(endpoint, signal string) string {
	u, _ := url.Parse(endpoint)
	u.Path = strings.TrimRight(u.Path, "/") + "/v1/" + signal
	u.RawPath = ""
	return u.String()
}

func sampler(config Config) sdktrace.Sampler {
	switch config.Sampler {
	case "always_on":
		return sdktrace.AlwaysSample()
	case "always_off":
		return sdktrace.NeverSample()
	case "traceidratio":
		return sdktrace.TraceIDRatioBased(config.SampleRatio)
	case "parentbased_always_on":
		return sdktrace.ParentBased(sdktrace.AlwaysSample())
	case "parentbased_always_off":
		return sdktrace.ParentBased(sdktrace.NeverSample())
	default:
		return sdktrace.ParentBased(sdktrace.TraceIDRatioBased(config.SampleRatio))
	}
}
