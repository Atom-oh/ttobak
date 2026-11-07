package telemetry

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/resource"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
)

// OTLP/HTTP protobuf transport.
//
// The stock otlptracehttp/otlploghttp exporters import the OTLP collector
// service packages, which link gRPC and golang.org/x/net/trace. Its
// html/template use disables the linker's method dead-code elimination and
// grew the API bootstrap from ~30 MB to ~67 MB. This transport sends the same
// wire format (ExportTraceServiceRequest / ExportLogsServiceRequest, field 1
// repeated) using only the message packages; span conversion stays in the
// official otlptrace package.

const maxResponseBytes = 64 << 10

type otlpHTTP struct {
	url    string
	client *http.Client
}

func newOTLPHTTP(url string) *otlpHTTP {
	return &otlpHTTP{url: url, client: &http.Client{
		Timeout: exportTimeout,
		Transport: &http.Transport{
			Proxy:               nil,
			MaxIdleConnsPerHost: 2,
			// Shorter than the ALB's 60s idle timeout so a thawed Lambda
			// rarely reuses a connection the ALB already closed.
			IdleConnTimeout: 30 * time.Second,
		},
	}}
}

// post sends a request whose field 1 repeats the given messages.
func (c *otlpHTTP) post(ctx context.Context, messages []proto.Message) error {
	var body []byte
	for _, message := range messages {
		encoded, err := proto.Marshal(message)
		if err != nil {
			return fmt.Errorf("otlp encode: %w", err)
		}
		body = protowire.AppendTag(body, 1, protowire.BytesType)
		body = protowire.AppendBytes(body, encoded)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("otlp request: %w", err)
	}
	request.Header.Set("Content-Type", "application/x-protobuf")
	response, err := c.client.Do(request)
	if err != nil {
		return fmt.Errorf("otlp export: %w", err)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxResponseBytes))
	_ = response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return fmt.Errorf("otlp export: HTTP %d", response.StatusCode)
	}
	return nil
}

// traceClient is an otlptrace.Client over otlpHTTP.
type traceClient struct{ http *otlpHTTP }

var _ otlptrace.Client = traceClient{}

func (traceClient) Start(context.Context) error { return nil }

func (c traceClient) Stop(context.Context) error {
	c.http.client.CloseIdleConnections()
	return nil
}

func (c traceClient) UploadTraces(ctx context.Context, spans []*tracepb.ResourceSpans) error {
	if len(spans) == 0 {
		return nil
	}
	messages := make([]proto.Message, len(spans))
	for i, rs := range spans {
		messages[i] = rs
	}
	return c.http.post(ctx, messages)
}

// logExporter is an sdklog.Exporter over otlpHTTP.
type logExporter struct{ http *otlpHTTP }

var _ sdklog.Exporter = logExporter{}

func (e logExporter) Export(ctx context.Context, records []sdklog.Record) error {
	if len(records) == 0 {
		return nil
	}
	return e.http.post(ctx, []proto.Message{resourceLogs(records)})
}

func (e logExporter) Shutdown(context.Context) error {
	e.http.client.CloseIdleConnections()
	return nil
}

func (logExporter) ForceFlush(context.Context) error { return nil }

// resourceLogs converts records from one LoggerProvider (one resource),
// grouped by instrumentation scope in first-seen order.
func resourceLogs(records []sdklog.Record) *logspb.ResourceLogs {
	out := &logspb.ResourceLogs{Resource: resourceProto(records[0].Resource())}
	scopes := map[string]*logspb.ScopeLogs{}
	for i := range records {
		record := &records[i]
		scope := record.InstrumentationScope()
		group, ok := scopes[scope.Name+"\x00"+scope.Version]
		if !ok {
			group = &logspb.ScopeLogs{Scope: &commonpb.InstrumentationScope{Name: scope.Name, Version: scope.Version}}
			scopes[scope.Name+"\x00"+scope.Version] = group
			out.ScopeLogs = append(out.ScopeLogs, group)
		}
		group.LogRecords = append(group.LogRecords, logRecord(record))
	}
	return out
}

func logRecord(record *sdklog.Record) *logspb.LogRecord {
	converted := &logspb.LogRecord{
		TimeUnixNano:         unixNano(record.Timestamp()),
		ObservedTimeUnixNano: unixNano(record.ObservedTimestamp()),
		SeverityNumber:       logspb.SeverityNumber(record.Severity()),
		SeverityText:         record.SeverityText(),
		Body:                 anyValue(record.Body()),
		EventName:            record.EventName(),
		Flags:                uint32(record.TraceFlags()),
	}
	if id := record.TraceID(); id.IsValid() {
		converted.TraceId = id[:]
	}
	if id := record.SpanID(); id.IsValid() {
		converted.SpanId = id[:]
	}
	record.WalkAttributes(func(kv attribute.KeyValue) bool {
		converted.Attributes = append(converted.Attributes, keyValue(kv))
		return true
	})
	converted.DroppedAttributesCount = uint32(record.DroppedAttributes())
	return converted
}

func resourceProto(res *resource.Resource) *resourcepb.Resource {
	out := &resourcepb.Resource{}
	if res == nil {
		return out
	}
	for _, kv := range res.Attributes() {
		out.Attributes = append(out.Attributes, keyValue(kv))
	}
	return out
}

func keyValue(kv attribute.KeyValue) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: string(kv.Key), Value: anyValue(kv.Value)}
}

// anyValue converts scalar values; other kinds are rendered as strings
// (this exporter only carries the allowlisted request-completed fields).
func anyValue(value attribute.Value) *commonpb.AnyValue {
	switch value.Type() {
	case attribute.EMPTY:
		return nil
	case attribute.BOOL:
		return &commonpb.AnyValue{Value: &commonpb.AnyValue_BoolValue{BoolValue: value.AsBool()}}
	case attribute.INT64:
		return &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: value.AsInt64()}}
	case attribute.FLOAT64:
		return &commonpb.AnyValue{Value: &commonpb.AnyValue_DoubleValue{DoubleValue: value.AsFloat64()}}
	case attribute.STRING:
		return &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: value.AsString()}}
	default:
		return &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: value.Emit()}}
	}
}

func unixNano(t time.Time) uint64 {
	if t.IsZero() {
		return 0
	}
	return uint64(t.UnixNano())
}
