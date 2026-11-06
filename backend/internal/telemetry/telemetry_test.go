package telemetry

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/smithy-go/tracing"
	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/baggage"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/resource"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	collectorlogs "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	collectortrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	"google.golang.org/protobuf/proto"
)

const canary = "SYNTHETIC_PRIVATE_CANARY_5f1c"

// discardLogs is a log exporter that keeps nothing.
type discardLogs struct{}

func (discardLogs) Export(context.Context, []sdklog.Record) error { return nil }
func (discardLogs) Shutdown(context.Context) error                { return nil }
func (discardLogs) ForceFlush(context.Context) error              { return nil }

func recordingTelemetry(t *testing.T) (*Telemetry, *tracetest.InMemoryExporter) {
	t.Helper()
	spans := tracetest.NewInMemoryExporter()
	tel := newWithExporters(resource.Empty(), spans, discardLogs{})
	t.Cleanup(func() { tel.Shutdown(context.Background()) })
	return tel, spans
}

func flushed(t *testing.T, tel *Telemetry, spans *tracetest.InMemoryExporter) tracetest.SpanStubs {
	t.Helper()
	if err := tel.traces.ForceFlush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}
	out := spans.GetSpans()
	spans.Reset()
	return out
}

func TestDisabledWithoutValidEndpoint(t *testing.T) {
	priorProvider := otel.GetTracerProvider()
	for _, endpoint := range []string{"", "  ", "ftp://collector", "http://", "http://user:pw@collector", "http://collector?x=1", "::bad"} {
		t.Run(endpoint, func(t *testing.T) {
			t.Setenv(EndpointEnv, endpoint)
			tel := FromEnv()
			if tel.Enabled() {
				t.Fatal("telemetry enabled")
			}
			next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
			if reflect.ValueOf(tel.Middleware(next)).Pointer() != reflect.ValueOf(next).Pointer() {
				t.Fatal("disabled middleware must be pass-through")
			}
			if _, ok := tel.AWSTracerProvider().(*tracing.NopTracerProvider); !ok {
				t.Fatal("disabled AWS tracer provider must be smithy's no-op")
			}
			handler := func(context.Context, int) (any, error) { return "ok", nil }
			if reflect.ValueOf(WrapInvocation(tel, handler)).Pointer() != reflect.ValueOf(handler).Pointer() {
				t.Fatal("disabled invocation wrapper must be the handler itself")
			}
			start := time.Now()
			tel.Flush(context.Background())
			if elapsed := time.Since(start); elapsed > time.Millisecond {
				t.Fatalf("disabled flush took %s", elapsed)
			}
			if otel.GetTracerProvider() != priorProvider {
				t.Fatal("disabled telemetry installed a global provider")
			}
		})
	}
}

var allowedServerKeys = map[string]bool{attrMethod: true, attrRoute: true, attrStatusCode: true, attrErrorType: true}

func TestServerSpansUseRoutePatternsAndAllowlistedAttributes(t *testing.T) {
	tel, spans := recordingTelemetry(t)
	router := chi.NewRouter()
	router.Use(tel.Middleware)
	router.Get("/api/public/docs/{token}", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	router.Route("/api/meetings/{meetingId}", func(r chi.Router) {
		r.Post("/notes", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusInternalServerError) })
	})
	router.Get("/api/health", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(canary)) })

	tests := []struct {
		name, method, path, span, route string
		status                          int
	}{
		{"public-capability", "GET", "/api/public/docs/" + canary + "?email=" + canary, "GET /api/public/docs/{token}", "/api/public/docs/{token}", 204},
		{"nested-500", "POST", "/api/meetings/" + canary + "/notes", "POST /api/meetings/{meetingId}/notes", "/api/meetings/{meetingId}/notes", 500},
		{"implicit-200", "GET", "/api/health", "GET /api/health", "/api/health", 200},
		{"unmatched", "GET", "/unknown/" + canary, "GET", "", 404},
		{"extension-method", canary, "/api/health", "_OTHER", "", 405},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, test.path, strings.NewReader(canary))
			request.Header.Set("Authorization", "Bearer "+canary)
			request.Header.Set("User-Agent", canary)
			router.ServeHTTP(httptest.NewRecorder(), request)
			got := flushed(t, tel, spans)
			if len(got) != 1 {
				t.Fatalf("spans=%d, want 1", len(got))
			}
			span := got[0]
			if span.Name != test.span || span.SpanKind != trace.SpanKindServer {
				t.Fatalf("span=%q kind=%v, want %q server", span.Name, span.SpanKind, test.span)
			}
			attrs := map[string]string{}
			for _, kv := range span.Attributes {
				key := string(kv.Key)
				if !allowedServerKeys[key] {
					t.Fatalf("attribute %q is not allowlisted", key)
				}
				attrs[key] = kv.Value.Emit()
			}
			if attrs[attrRoute] != test.route || attrs[attrStatusCode] != strconv.Itoa(test.status) {
				t.Fatalf("attrs=%v, want route %q status %d", attrs, test.route, test.status)
			}
			assertNoCanary(t, span)
		})
	}
}

func TestPropagationExtractsTraceparentAndBaggage(t *testing.T) {
	tel, spans := recordingTelemetry(t)
	var member string
	router := chi.NewRouter()
	router.Use(tel.Middleware)
	router.Get("/api/health", func(w http.ResponseWriter, r *http.Request) {
		member = baggage.FromContext(r.Context()).Member("tenant").Value()
	})
	request := httptest.NewRequest("GET", "/api/health", nil)
	request.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	request.Header.Set("baggage", "tenant=demo")
	router.ServeHTTP(httptest.NewRecorder(), request)
	got := flushed(t, tel, spans)
	if len(got) != 1 {
		t.Fatalf("spans=%d", len(got))
	}
	if got[0].SpanContext.TraceID().String() != "4bf92f3577b34da6a3ce929d0e0e4736" ||
		got[0].Parent.SpanID().String() != "00f067aa0ba902b7" || !got[0].Parent.IsRemote() {
		t.Fatalf("trace=%s parent=%s remote=%v", got[0].SpanContext.TraceID(), got[0].Parent.SpanID(), got[0].Parent.IsRemote())
	}
	if member != "demo" {
		t.Fatalf("baggage member=%q", member)
	}
}

var allowedAWSKeys = map[string]bool{"rpc.system": true, "rpc.service": true, "rpc.method": true, "aws.error_code": true, "aws.error_fault": true}

func TestAWSSpansRecordOperationsWithoutPayloads(t *testing.T) {
	tel, spans := recordingTelemetry(t)
	var fail atomic.Bool
	ddb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-amz-json-1.0")
		if fail.Load() {
			w.Header().Set("X-Amzn-ErrorType", "ValidationException")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"__type":"com.amazon.coral.validate#ValidationException","message":"bad ` + canary + `"}`))
			return
		}
		_, _ = w.Write([]byte(`{"Item":{"pk":{"S":"` + canary + `"}}}`))
	}))
	defer ddb.Close()
	client := dynamodb.New(dynamodb.Options{
		Region:           "ap-northeast-2",
		BaseEndpoint:     aws.String(ddb.URL),
		Credentials:      aws.AnonymousCredentials{},
		RetryMaxAttempts: 1,
		TracerProvider:   tel.AWSTracerProvider(),
	})
	input := &dynamodb.GetItemInput{TableName: aws.String("ttobak-main"),
		Key: map[string]types.AttributeValue{"pk": &types.AttributeValueMemberS{Value: canary}}}

	ctx, parent := tel.tracer.Start(context.Background(), "parent")
	for _, failing := range []bool{false, true} {
		fail.Store(failing)
		_, err := client.GetItem(ctx, input)
		if (err != nil) != failing {
			t.Fatalf("failing=%v err=%v", failing, err)
		}
	}
	parent.End()
	got := flushed(t, tel, spans)
	var operations []tracetest.SpanStub
	for _, span := range got {
		if span.Name != "parent" {
			operations = append(operations, span)
		}
	}
	if len(operations) != 2 {
		t.Fatalf("operation spans=%d, want 2 (internal SDK phases must be dropped)", len(operations))
	}
	for i, span := range operations {
		if span.Name != "DynamoDB.GetItem" || span.SpanKind != trace.SpanKindClient ||
			span.Parent.SpanID() != parent.SpanContext().SpanID() {
			t.Fatalf("span=%q kind=%v parent=%s", span.Name, span.SpanKind, span.Parent.SpanID())
		}
		keys := map[string]string{}
		for _, kv := range span.Attributes {
			if !allowedAWSKeys[string(kv.Key)] {
				t.Fatalf("attribute %q is not allowlisted", kv.Key)
			}
			keys[string(kv.Key)] = kv.Value.Emit()
		}
		if keys["rpc.method"] != "GetItem" || keys["rpc.service"] != "DynamoDB" {
			t.Fatalf("attrs=%v", keys)
		}
		if i == 1 && keys["aws.error_code"] != "ValidationException" {
			t.Fatalf("error attrs=%v", keys)
		}
		assertNoCanary(t, span)
	}
}

// blackhole is a collector that never answers within the flush budget.
func blackhole(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(time.Second):
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestFlushIsBoundedAndBacksOffWhenCollectorHangs(t *testing.T) {
	srv := blackhole(t)
	tel, err := New(Config{Endpoint: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		tel.Shutdown(ctx)
	}()
	var mu sync.Mutex
	var logged []string
	tel.logf = func(format string, _ ...any) { mu.Lock(); logged = append(logged, format); mu.Unlock() }
	clock := time.Now()
	tel.now = func() time.Time { return clock }

	handler := WrapInvocation(tel, func(ctx context.Context, _ struct{}) (any, error) {
		_, span := tel.tracer.Start(ctx, "work")
		span.End()
		EmitRequestCompleted(ctx, RequestCompletion{Method: "GET"})
		return "ok", nil
	})
	start := time.Now()
	result, err := handler(context.Background(), struct{}{})
	elapsed := time.Since(start)
	if result != "ok" || err != nil {
		t.Fatalf("result=%v err=%v", result, err)
	}
	if elapsed > FlushTimeout+100*time.Millisecond {
		t.Fatalf("invocation with hung collector took %s", elapsed)
	}
	t.Logf("hung-collector invocation: %s (budget %s)", elapsed, FlushTimeout)

	// Within the backoff window flushes are skipped entirely.
	start = time.Now()
	_, _ = handler(context.Background(), struct{}{})
	if skipped := time.Since(start); skipped > 10*time.Millisecond {
		t.Fatalf("flush during backoff took %s", skipped)
	}
	// After the window one more bounded attempt happens; still logged once.
	clock = clock.Add(failureBackoff + time.Second)
	start = time.Now()
	_, _ = handler(context.Background(), struct{}{})
	if retried := time.Since(start); retried > FlushTimeout+100*time.Millisecond {
		t.Fatalf("retry flush took %s", retried)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(logged) != 1 {
		t.Fatalf("failure logged %d times, want once: %v", len(logged), logged)
	}
}

// The collector decodes the official OTLP request messages, and the flush
// overhead with a responsive (loopback) collector is reported.
func TestFlushExportsOTLPProtobufWithHealthyCollector(t *testing.T) {
	var mu sync.Mutex
	var spanNames []string
	var logs []*logspb.LogRecord
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.Header.Get("Content-Type") != "application/x-protobuf" {
			t.Errorf("content type %q", r.Header.Get("Content-Type"))
		}
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/v1/traces":
			var request collectortrace.ExportTraceServiceRequest
			if err := proto.Unmarshal(body, &request); err != nil {
				t.Errorf("trace request: %v", err)
			}
			for _, rs := range request.ResourceSpans {
				for _, ss := range rs.ScopeSpans {
					for _, span := range ss.Spans {
						spanNames = append(spanNames, span.Name)
					}
				}
			}
		case "/v1/logs":
			var request collectorlogs.ExportLogsServiceRequest
			if err := proto.Unmarshal(body, &request); err != nil {
				t.Errorf("log request: %v", err)
			}
			for _, rl := range request.ResourceLogs {
				for _, sl := range rl.ScopeLogs {
					logs = append(logs, sl.LogRecords...)
				}
			}
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/x-protobuf")
	}))
	defer srv.Close()
	start := time.Now()
	tel, err := New(Config{Endpoint: srv.URL + "/"})
	if err != nil {
		t.Fatal(err)
	}
	initElapsed := time.Since(start)
	defer tel.Shutdown(context.Background())
	logger := tel.logs.Logger(instrumentationScope)
	const rounds = 50
	var total, worst time.Duration
	var lastTrace trace.TraceID
	for range rounds {
		ctx, span := tel.tracer.Start(context.Background(), "GET /api/health")
		_, child := tel.tracer.Start(ctx, "DynamoDB.GetItem")
		child.End()
		emitRequestCompleted(ctx, logger, RequestCompletion{Method: "GET", Route: "/api/health", Status: 200, Bytes: 2, RequestIDHash: "-"})
		span.End()
		lastTrace = span.SpanContext().TraceID()
		start := time.Now()
		tel.Flush(ctx)
		elapsed := time.Since(start)
		total += elapsed
		worst = max(worst, elapsed)
	}
	if tel.failing.Load() {
		t.Fatal("healthy collector reported failure")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(spanNames) != 2*rounds || len(logs) != rounds {
		t.Fatalf("collector decoded %d spans and %d logs, want %d and %d", len(spanNames), len(logs), 2*rounds, rounds)
	}
	last := logs[len(logs)-1]
	attrs := map[string]string{}
	for _, kv := range last.Attributes {
		attrs[kv.Key] = kv.Value.String()
	}
	if last.Body.GetStringValue() != "request completed" || !bytes.Equal(last.TraceId, lastTrace[:]) ||
		len(last.SpanId) != 8 || len(attrs) != 6 || last.SeverityText != "INFO" {
		t.Fatalf("log record body=%q trace=%x attrs=%v", last.Body.GetStringValue(), last.TraceId, attrs)
	}
	t.Logf("init=%s flush avg=%s worst=%s over %d rounds (loopback collector)", initElapsed, total/rounds, worst, rounds)
}

func BenchmarkNew(b *testing.B) {
	for b.Loop() {
		tel, err := New(Config{Endpoint: "http://127.0.0.1:1"})
		if err != nil {
			b.Fatal(err)
		}
		tel.Shutdown(context.Background())
	}
}

func assertNoCanary(t *testing.T, span tracetest.SpanStub) {
	t.Helper()
	if strings.Contains(span.Name, canary) {
		t.Fatalf("span name leaks private text: %q", span.Name)
	}
	for _, kv := range span.Attributes {
		if strings.Contains(kv.Value.Emit(), canary) {
			t.Fatalf("attribute %s leaks private text", kv.Key)
		}
	}
	for _, event := range span.Events {
		t.Fatalf("unexpected span event %q", event.Name)
	}
}
