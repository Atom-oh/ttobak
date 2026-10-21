package connector

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

const InvokeProtocol = "ttobak.connector.invoke.v1"
const MaxBridgeResponseBytes = 14000

type InvokeFrame struct {
	Protocol   string `json:"protocol"`
	Operation  string `json:"operation"`
	RequestID  string `json:"requestId"`
	Delegation string `json:"delegation"`
	Arguments  string `json:"arguments"`
}
type HTTPArguments struct {
	Method string          `json:"method"`
	Path   string          `json:"path"`
	Body   json.RawMessage `json:"body"`
}
type InvokeResult struct {
	StatusCode int    `json:"statusCode"`
	Body       string `json:"body"`
}
type principalContextKey struct{}

func decodeHTTPArguments(raw []byte) (HTTPArguments, bool) {
	var input HTTPArguments
	if !utf8.Valid(raw) {
		return input, false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return input, false
	}
	fields := map[string]json.RawMessage{}
	for decoder.More() {
		token, err := decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok || fields[key] != nil || (key != "method" && key != "path" && key != "body") {
			return input, false
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return input, false
		}
		fields[key] = value
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') || len(fields) != 3 {
		return input, false
	}
	if decoder.Decode(new(any)) != io.EOF ||
		json.Unmarshal(fields["method"], &input.Method) != nil ||
		json.Unmarshal(fields["path"], &input.Path) != nil {
		return input, false
	}
	input.Body = fields["body"]
	return input, true
}

// WithVerifiedPrincipal is used only after Verify; external headers cannot
// construct a Go context or populate Principal's private fields.
func WithVerifiedPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalContextKey{}, p)
}
func VerifiedPrincipal(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalContextKey{}).(Principal)
	return p, ok && p.userID != ""
}

// VerifiedReadRequest recognizes only a verifier-created, still-current
// principal whose operation matches this exact routed read. It does not accept
// headers, query parameters, or API Gateway authorizer fields.
func VerifiedReadRequest(r *http.Request) (Principal, bool) {
	p, ok := VerifiedPrincipal(r.Context())
	op, err := HTTPOperation(r.Method, r.URL.EscapedPath())
	return p, ok && err == nil && AvailableReadOperation(op) &&
		p.Require(op, "ttobak-api-bridge", time.Now().Unix()) == nil
}

func IsInvokeFrame(raw []byte) bool {
	var marker struct {
		Protocol string `json:"protocol"`
	}
	return json.Unmarshal(raw, &marker) == nil && marker.Protocol == InvokeProtocol
}

type Bridge struct {
	Verify func(context.Context, []byte, Request) (Principal, error)
	Router http.Handler
	Mutate func(context.Context, Principal, json.RawMessage) InvokeResult
}

type limitedResponse struct {
	header   http.Header
	body     bytes.Buffer
	status   int
	overflow bool
}

func (w *limitedResponse) Header() http.Header { return w.header }
func (w *limitedResponse) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}
func (w *limitedResponse) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.status = 200
	}
	if w.body.Len()+len(data) > MaxBridgeResponseBytes {
		w.overflow = true
		return len(data), nil
	}
	if !w.overflow {
		_, _ = w.body.Write(data)
	}
	return len(data), nil
}
func bridgeFailure(status int, code string) InvokeResult {
	body, _ := json.Marshal(map[string]any{"error": map[string]string{"code": code, "message": "Connector request rejected"}})
	return InvokeResult{status, string(body)}
}

func (b *Bridge) Invoke(ctx context.Context, raw []byte) InvokeResult {
	if b == nil || b.Verify == nil || b.Router == nil || len(raw) > 2*(MaxArgumentBytes+MaxWireBytes) {
		return bridgeFailure(503, "POLICY_UNAVAILABLE")
	}
	var frame InvokeFrame
	if !canonicalJSON(raw, &frame) || frame.Protocol != InvokeProtocol {
		return bridgeFailure(400, "INVALID_DELEGATION")
	}
	wire, ok := decodeBase64(frame.Delegation, MaxWireBytes)
	if !ok {
		return bridgeFailure(400, "INVALID_DELEGATION")
	}
	args, ok := decodeBase64(frame.Arguments, MaxArgumentBytes)
	if !ok {
		return bridgeFailure(400, "INVALID_DELEGATION")
	}
	// Reject duplicate/unknown/missing fields while preserving the exact signed
	// argument bytes, including Unicode and ordinary query delimiters.
	input, ok := decodeHTTPArguments(args)
	if !ok || len(input.Path) > 8192 {
		return bridgeFailure(403, "CAPABILITY_DENIED")
	}
	// ParseRequestURI treats a literal '#' as part of the request target, while
	// NewRequest treats it as a fragment. Reject it before either interpretation.
	if strings.Contains(input.Path, "#") {
		return bridgeFailure(400, "REQUEST_MISMATCH")
	}
	u, err := url.ParseRequestURI(input.Path)
	if err != nil || u.IsAbs() || u.Host != "" || u.Fragment != "" || u.RawPath != "" {
		return bridgeFailure(400, "REQUEST_MISMATCH")
	}
	op, err := HTTPOperation(input.Method, u.EscapedPath())
	mutation := op == "api.document.putDocument" && input.Method == http.MethodPost && input.Path == "/api/documents"
	if err != nil || op != frame.Operation || (!mutation && !AvailableReadOperation(op)) ||
		(!mutation && (input.Method != http.MethodGet || !bytes.Equal(input.Body, []byte("null")))) {
		return bridgeFailure(403, "CAPABILITY_DENIED")
	}
	request := Request{frame.Operation, frame.RequestID, args}
	principal, err := b.Verify(ctx, wire, request)
	if err != nil || principal.Require(op, "ttobak-api-bridge", time.Now().Unix()) != nil {
		return bridgeFailure(403, "CAPABILITY_DENIED")
	}
	if mutation {
		if b.Mutate == nil || principal.Require(op, "ttobak-api-bridge", time.Now().Unix()) != nil {
			return bridgeFailure(403, "CAPABILITY_DENIED")
		}
		return b.Mutate(ctx, principal, input.Body)
	}
	httpRequest, err := http.NewRequestWithContext(WithVerifiedPrincipal(ctx, principal),
		input.Method, input.Path, strings.NewReader(""))
	if err != nil {
		return bridgeFailure(400, "REQUEST_MISMATCH")
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	writer := &limitedResponse{header: make(http.Header)}
	b.Router.ServeHTTP(writer, httpRequest)
	// A disabled client/revoked grant or expired delegation cannot publish bytes
	// after an expensive reader. Resource/current-source checks remain in services.
	if _, err := b.Verify(ctx, wire, request); err != nil {
		return bridgeFailure(403, "CAPABILITY_DENIED")
	}
	if writer.overflow {
		return bridgeFailure(413, "RESULT_TOO_LARGE")
	}
	if writer.status == 0 {
		writer.status = 200
	}
	return InvokeResult{writer.status, writer.body.String()}
}

func EncodeInvokeFrame(frame InvokeFrame) []byte {
	raw, _ := json.Marshal(frame)
	return raw
}
func EncodeBridgeBytes(raw []byte) string { return base64.RawURLEncoding.EncodeToString(raw) }
