package telemetry

import (
	"context"

	"github.com/aws/smithy-go/tracing"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// awsTracerProvider adapts AWS SDK v2 (smithy) tracing to OpenTelemetry.
// It records one client span per SDK operation ("DynamoDB.GetItem") and
// drops the SDK's internal phase spans (serialize, sign, retry, transmit).
// Only allowlisted properties are copied: exception/API error messages,
// resolved endpoints and payload-derived values are never recorded.
// AWS calls carry no injected trace headers.
type awsTracerProvider struct{ tracer trace.Tracer }

type awsTracer struct{ tracer trace.Tracer }

type awsSpan struct {
	name string
	span trace.Span
}

// allowedAWSProperties maps SDK span properties to recorded attribute keys.
var allowedAWSProperties = map[string]string{
	"rpc.system":      "rpc.system",
	"rpc.service":     "rpc.service",
	"rpc.method":      "rpc.method",
	"api.error_code":  "aws.error_code",
	"api.error_fault": "aws.error_fault",
}

func (p awsTracerProvider) Tracer(string, ...tracing.TracerOption) tracing.Tracer {
	return awsTracer(p)
}

func (t awsTracer) StartSpan(ctx context.Context, name string, opts ...tracing.SpanOption) (context.Context, tracing.Span) {
	var options tracing.SpanOptions
	for _, opt := range opts {
		opt(&options)
	}
	if options.Kind != tracing.SpanKindClient {
		return ctx, nopAWSSpan{}
	}
	ctx, span := t.tracer.Start(ctx, name, trace.WithSpanKind(trace.SpanKindClient))
	for key, recorded := range allowedAWSProperties {
		if value, ok := options.Properties.Get(key).(string); ok {
			span.SetAttributes(attribute.String(recorded, value))
		}
	}
	return ctx, &awsSpan{name: name, span: span}
}

func (s *awsSpan) Name() string { return s.name }

func (s *awsSpan) Context() tracing.SpanContext {
	sc := s.span.SpanContext()
	return tracing.SpanContext{TraceID: sc.TraceID().String(), SpanID: sc.SpanID().String(), IsRemote: sc.IsRemote()}
}

// AddEvent is ignored: event properties are not allowlisted.
func (s *awsSpan) AddEvent(string, ...tracing.EventOption) {}

func (s *awsSpan) SetStatus(status tracing.SpanStatus) {
	switch status {
	case tracing.SpanStatusOK:
		s.span.SetStatus(codes.Ok, "")
	case tracing.SpanStatusError:
		s.span.SetStatus(codes.Error, "")
	}
}

func (s *awsSpan) SetProperty(k, v any) {
	key, ok := k.(string)
	if !ok {
		return
	}
	recorded, ok := allowedAWSProperties[key]
	if !ok {
		return
	}
	if value, ok := v.(string); ok {
		s.span.SetAttributes(attribute.String(recorded, value))
	}
}

func (s *awsSpan) End() { s.span.End() }

// nopAWSSpan stands in for SDK-internal phase spans.
type nopAWSSpan struct{}

func (nopAWSSpan) Name() string                            { return "" }
func (nopAWSSpan) Context() tracing.SpanContext            { return tracing.SpanContext{} }
func (nopAWSSpan) AddEvent(string, ...tracing.EventOption) {}
func (nopAWSSpan) SetStatus(tracing.SpanStatus)            {}
func (nopAWSSpan) SetProperty(any, any)                    {}
func (nopAWSSpan) End()                                    {}
