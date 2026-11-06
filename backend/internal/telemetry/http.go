package telemetry

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	otellog "go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/log/global"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// Span attribute keys. Tests assert that recorded spans use only these.
const (
	attrMethod     = "http.request.method"
	attrRoute      = "http.route"
	attrStatusCode = "http.response.status_code"
	attrErrorType  = "error.type"
)

// Middleware starts a server span per request. It must run inside the chi
// router so the matched route pattern is available when the span ends. The
// span name is "METHOD /route/{pattern}" ("METHOD" alone when unmatched),
// never the request path, which can carry public document capabilities.
// Only the trace and parent span IDs of an incoming W3C traceparent are used
// (see remoteParent); tracestate, baggage and the sampled flag are ignored.
func (t *Telemetry) Middleware(next http.Handler) http.Handler {
	if !t.Enabled() {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := remoteParent(r.Context(), r.Header)
		method := spanMethod(r.Method)
		ctx, span := t.tracer.Start(ctx, method,
			trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(attribute.String(attrMethod, method)))
		wrapped := chimiddleware.NewWrapResponseWriter(w, r.ProtoMajor)
		defer func() {
			status := wrapped.Status()
			if status == 0 {
				status = http.StatusOK
			}
			if route := RoutePattern(r); route != "" {
				span.SetName(method + " " + route)
				span.SetAttributes(attribute.String(attrRoute, route))
			}
			span.SetAttributes(attribute.Int(attrStatusCode, status))
			if status >= http.StatusInternalServerError {
				span.SetAttributes(attribute.String(attrErrorType, strconv.Itoa(status)))
				span.SetStatus(codes.Error, "")
			}
			span.End()
		}()
		next.ServeHTTP(wrapped, r.WithContext(ctx))
	})
}

// remoteParent adopts only the trace ID and parent span ID of a valid
// traceparent header. The parent is rebuilt with an empty TraceState (client
// tracestate would otherwise be exported verbatim) and marked sampled, so a
// public caller cannot attach text to spans or suppress tracing. Baggage is
// never extracted.
func remoteParent(ctx context.Context, header http.Header) context.Context {
	values := header.Values("traceparent")
	if len(values) != 1 {
		return ctx
	}
	carrier := propagation.MapCarrier{"traceparent": values[0]}
	parsed := trace.SpanContextFromContext(propagation.TraceContext{}.Extract(context.Background(), carrier))
	if !parsed.IsValid() {
		return ctx
	}
	return trace.ContextWithRemoteSpanContext(ctx, trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    parsed.TraceID(),
		SpanID:     parsed.SpanID(),
		TraceFlags: trace.FlagsSampled,
		Remote:     true,
	}))
}

// RoutePattern returns the matched chi route pattern, or "" when unmatched.
func RoutePattern(r *http.Request) string {
	if rctx := chi.RouteContext(r.Context()); rctx != nil {
		return rctx.RoutePattern()
	}
	return ""
}

// spanMethod mirrors the request log: an extension method can itself carry a
// capability, so only standard methods are recorded.
func spanMethod(method string) string {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut,
		http.MethodPatch, http.MethodDelete, http.MethodConnect, http.MethodOptions, http.MethodTrace:
		return method
	default:
		return "_OTHER"
	}
}

// RequestCompletion is the content of the "request completed" log line.
type RequestCompletion struct {
	Method        string
	Route         string
	Status        int
	Bytes         int
	Duration      time.Duration
	RequestIDHash string
}

var requestLogger = global.Logger("github.com/ttobak/backend/internal/middleware")

// EmitRequestCompleted mirrors the "request completed" stdout line as an OTLP
// log record correlated with the active span. Without an installed logger
// provider it is a no-op.
func EmitRequestCompleted(ctx context.Context, c RequestCompletion) {
	emitRequestCompleted(ctx, requestLogger, c)
}

func emitRequestCompleted(ctx context.Context, requestLogger otellog.Logger, c RequestCompletion) {
	if !requestLogger.Enabled(ctx, otellog.EnabledParameters{Severity: otellog.SeverityInfo}) {
		return
	}
	var record otellog.Record
	record.SetTimestamp(time.Now())
	record.SetSeverity(otellog.SeverityInfo)
	record.SetSeverityText("INFO")
	record.SetBody(attribute.StringValue("request completed"))
	record.AddAttributes(
		attribute.String("method", c.Method),
		attribute.String("route", c.Route),
		attribute.Int("status", c.Status),
		attribute.Int("bytes", c.Bytes),
		attribute.Int64("duration_ms", c.Duration.Milliseconds()),
		attribute.String("request_id_hash", c.RequestIDHash),
	)
	requestLogger.Emit(ctx, record)
}
