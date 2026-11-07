package middleware

import (
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/log/global"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

type collectedLogs struct {
	mu      sync.Mutex
	records []sdklog.Record
}

func (c *collectedLogs) Export(_ context.Context, records []sdklog.Record) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, record := range records {
		c.records = append(c.records, record.Clone())
	}
	return nil
}
func (c *collectedLogs) Shutdown(context.Context) error   { return nil }
func (c *collectedLogs) ForceFlush(context.Context) error { return nil }

// The OTLP copy of "request completed" carries the same redacted fields as
// the stdout line plus the active span's trace/span IDs.
func TestRequestLogEmitsCorrelatedOTLPRecord(t *testing.T) {
	const capability = "SYNTHETIC_OTLP_LOG_CAPABILITY"
	prior := log.Writer()
	log.SetOutput(io.Discard)
	t.Cleanup(func() { log.SetOutput(prior) })
	exporter := &collectedLogs{}
	logs := sdklog.NewLoggerProvider(sdklog.WithProcessor(sdklog.NewSimpleProcessor(exporter)))
	global.SetLoggerProvider(logs)
	traces := sdktrace.NewTracerProvider()
	t.Cleanup(func() { _ = logs.Shutdown(context.Background()); _ = traces.Shutdown(context.Background()) })

	var spanContext struct{ trace, span string }
	router := chi.NewRouter()
	router.Use(chimiddleware.RequestID)
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, span := traces.Tracer("test").Start(r.Context(), "server")
			defer span.End()
			spanContext.trace, spanContext.span = span.SpanContext().TraceID().String(), span.SpanContext().SpanID().String()
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	})
	router.Use(RequestLog)
	router.Get("/api/public/docs/{token}", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) })
	request := httptest.NewRequest("GET", "/api/public/docs/"+capability+"?q="+capability, nil)
	request.Header.Set("X-Request-ID", capability)
	router.ServeHTTP(httptest.NewRecorder(), request)

	exporter.mu.Lock()
	defer exporter.mu.Unlock()
	if len(exporter.records) != 1 {
		t.Fatalf("records=%d, want 1", len(exporter.records))
	}
	record := exporter.records[0]
	if record.Body().Emit() != "request completed" || record.TraceID().String() != spanContext.trace ||
		record.SpanID().String() != spanContext.span {
		t.Fatalf("body=%q trace=%s span=%s", record.Body().Emit(), record.TraceID(), record.SpanID())
	}
	want := map[string]string{"method": "GET", "route": "/api/public/docs/{token}", "status": "200", "bytes": "2"}
	seen := map[string]bool{}
	record.WalkAttributes(func(kv attribute.KeyValue) bool {
		key, value := string(kv.Key), kv.Value.Emit()
		seen[key] = true
		if strings.Contains(value, capability) {
			t.Fatalf("attribute %s leaks the capability", key)
		}
		if expected, ok := want[key]; ok && value != expected {
			t.Fatalf("%s=%q, want %q", key, value, expected)
		}
		return true
	})
	for _, key := range []string{"method", "route", "status", "bytes", "duration_ms", "request_id_hash"} {
		if !seen[key] {
			t.Fatalf("missing attribute %s", key)
		}
	}
	if len(seen) != 6 {
		t.Fatalf("unexpected attributes: %v", seen)
	}
}
