// Package telemetry wires optional OpenTelemetry traces and logs for the API
// Lambda. It is disabled (no providers, no exporters, pass-through
// middleware) unless OTEL_EXPORTER_OTLP_ENDPOINT is set.
//
// Privacy contract: spans and log records carry only an allowlisted set of
// attribute keys (see allowedSpanAttributes). Request URLs, query strings,
// headers, bodies, identities and AWS payloads are never recorded; route
// patterns stand in for paths because public document tokens are bearer
// capabilities.
package telemetry

import (
	"context"
	"errors"
	"log"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aws/smithy-go/tracing"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/log/global"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

const (
	// EndpointEnv is the OTLP/HTTP base URL; empty disables telemetry.
	EndpointEnv = "OTEL_EXPORTER_OTLP_ENDPOINT"
	// ServiceNameEnv overrides the default service.name resource attribute.
	ServiceNameEnv = "OTEL_SERVICE_NAME"

	// FlushTimeout bounds the per-invocation flush that runs before the
	// Lambda response is returned.
	FlushTimeout = 150 * time.Millisecond
	// exportTimeout bounds every individual OTLP request, including the
	// batch processors' background exports.
	exportTimeout = 150 * time.Millisecond
	// failureBackoff is how long flushes are skipped after a failed or
	// timed-out flush, so an unavailable collector costs at most one bounded
	// wait per window instead of one per request.
	failureBackoff = 30 * time.Second

	maxQueueSize  = 2048
	maxExportSize = 512
	// batchInterval only matters for background exports; each invocation
	// force-flushes explicitly.
	batchInterval = 5 * time.Second

	instrumentationScope = "github.com/ttobak/backend/internal/telemetry"
	defaultServiceName   = "ttobak-api"
)

// Config selects the collector and resource identity.
type Config struct {
	Endpoint    string
	ServiceName string
}

// Telemetry owns the SDK providers for one process. A nil or disabled value
// is safe to use: every method degrades to a no-op.
type Telemetry struct {
	enabled    bool
	traces     *sdktrace.TracerProvider
	logs       *sdklog.LoggerProvider
	tracer     trace.Tracer
	propagator propagation.TextMapPropagator

	now       func() time.Time
	logf      func(string, ...any)
	skipUntil atomic.Int64 // unix nanoseconds; flushes are skipped before it
	failing   atomic.Bool
}

// Disabled returns a telemetry value that records nothing.
func Disabled() *Telemetry {
	return &Telemetry{tracer: noop.NewTracerProvider().Tracer(""), propagator: propagation.NewCompositeTextMapPropagator()}
}

// FromEnv builds telemetry from the environment and installs it as the
// global OpenTelemetry providers. It never fails: an invalid configuration
// is logged and telemetry stays disabled.
func FromEnv() *Telemetry {
	cfg := Config{Endpoint: os.Getenv(EndpointEnv), ServiceName: os.Getenv(ServiceNameEnv)}
	if strings.TrimSpace(cfg.Endpoint) == "" {
		return Disabled()
	}
	t, err := New(cfg)
	if err != nil {
		log.Printf("telemetry disabled: %v", err)
		return Disabled()
	}
	t.InstallGlobal()
	return t
}

// New creates enabled telemetry. Exporters are lazy: no connection is made
// until the first export.
func New(cfg Config) (*Telemetry, error) {
	base, err := parseEndpoint(cfg.Endpoint)
	if err != nil {
		return nil, err
	}
	serviceName := cfg.ServiceName
	if serviceName == "" {
		serviceName = defaultServiceName
	}
	attrs := []attribute.KeyValue{
		attribute.String("service.name", serviceName),
		attribute.String("cloud.provider", "aws"),
		attribute.String("faas.name", os.Getenv("AWS_LAMBDA_FUNCTION_NAME")),
		attribute.String("cloud.region", os.Getenv("AWS_REGION")),
	}
	res := resource.NewSchemaless(attrs...)

	traceExporter := otlptrace.NewUnstarted(traceClient{http: newOTLPHTTP(base + "/v1/traces")})
	logExporter := logExporter{http: newOTLPHTTP(base + "/v1/logs")}
	return newWithExporters(res, traceExporter, logExporter), nil
}

func newWithExporters(res *resource.Resource, spans sdktrace.SpanExporter, records sdklog.Exporter) *Telemetry {
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.AlwaysSample())),
		sdktrace.WithBatcher(spans,
			sdktrace.WithMaxQueueSize(maxQueueSize),
			sdktrace.WithMaxExportBatchSize(maxExportSize),
			sdktrace.WithBatchTimeout(batchInterval),
			sdktrace.WithExportTimeout(exportTimeout)),
	)
	lp := sdklog.NewLoggerProvider(
		sdklog.WithResource(res),
		sdklog.WithProcessor(sdklog.NewBatchProcessor(records,
			sdklog.WithMaxQueueSize(maxQueueSize),
			sdklog.WithExportMaxBatchSize(maxExportSize),
			sdklog.WithExportInterval(batchInterval),
			sdklog.WithExportTimeout(exportTimeout))),
	)
	return &Telemetry{
		enabled: true,
		traces:  tp,
		logs:    lp,
		tracer:  tp.Tracer(instrumentationScope),
		propagator: propagation.NewCompositeTextMapPropagator(
			propagation.TraceContext{}, propagation.Baggage{}),
		now:  time.Now,
		logf: log.Printf,
	}
}

func parseEndpoint(raw string) (string, error) {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" ||
		u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		// Never echo the raw value: it is configuration, but keep logs minimal.
		return "", errors.New("invalid " + EndpointEnv)
	}
	return raw, nil
}

// InstallGlobal registers the providers, propagator and a rate-limited
// error handler as the process-wide OpenTelemetry defaults.
func (t *Telemetry) InstallGlobal() {
	if !t.Enabled() {
		return
	}
	otel.SetTracerProvider(t.traces)
	otel.SetTextMapPropagator(t.propagator)
	global.SetLoggerProvider(t.logs)
	otel.SetErrorHandler(otel.ErrorHandlerFunc(t.reportFailure))
}

// Enabled reports whether spans and logs are exported.
func (t *Telemetry) Enabled() bool { return t != nil && t.enabled }

// Flush exports buffered spans and logs, waiting at most FlushTimeout. It
// never returns an error: a failure is logged once and later flushes are
// skipped for failureBackoff, so a missing collector cannot keep delaying
// requests. Queued data beyond the bounded queues is dropped.
func (t *Telemetry) Flush(ctx context.Context) {
	if !t.Enabled() {
		return
	}
	now := t.now()
	if now.UnixNano() < t.skipUntil.Load() {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), FlushTimeout)
	defer cancel()
	var wg sync.WaitGroup
	var traceErr, logErr error
	wg.Add(2)
	go func() { defer wg.Done(); traceErr = t.traces.ForceFlush(ctx) }()
	go func() { defer wg.Done(); logErr = t.logs.ForceFlush(ctx) }()
	wg.Wait()
	if err := errors.Join(traceErr, logErr); err != nil {
		t.skipUntil.Store(now.Add(failureBackoff).UnixNano())
		t.reportFailure(err)
		return
	}
	if t.failing.CompareAndSwap(true, false) {
		t.logf("telemetry export recovered")
	}
}

// reportFailure logs the first failure of an outage only.
func (t *Telemetry) reportFailure(err error) {
	if t.failing.CompareAndSwap(false, true) {
		t.logf("telemetry export failed; dropping telemetry until the collector recovers: %v", err)
	}
}

// Shutdown flushes and stops the providers (tests and local tools).
func (t *Telemetry) Shutdown(ctx context.Context) {
	if !t.Enabled() {
		return
	}
	_ = t.traces.Shutdown(ctx)
	_ = t.logs.Shutdown(ctx)
}

// AWSTracerProvider returns the tracer provider for AWS SDK v2 client
// options. Disabled telemetry returns smithy's no-op provider.
func (t *Telemetry) AWSTracerProvider() tracing.TracerProvider {
	if !t.Enabled() {
		return &tracing.NopTracerProvider{}
	}
	return awsTracerProvider{tracer: t.traces.Tracer(instrumentationScope + "/aws")}
}

// WrapInvocation flushes telemetry after each invocation returns, before the
// result is handed back to the Lambda runtime.
func WrapInvocation[In any](t *Telemetry, next func(context.Context, In) (any, error)) func(context.Context, In) (any, error) {
	if !t.Enabled() {
		return next
	}
	return func(ctx context.Context, in In) (any, error) {
		defer t.Flush(ctx)
		return next(ctx, in)
	}
}
