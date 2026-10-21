package connector

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestSignedPrivateBridgeBindsSubjectRequestAndCurrentGrantBeforeBytes(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	spki, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
	keyID := "arn:aws:kms:ap-northeast-2:111111111111:key/00000000-0000-0000-0000-000000000000"
	now := time.Now().Unix()
	enabled := true
	resolver := func(_ context.Context, user, client string) (Grants, error) {
		return Grants{UserID: user, ClientID: client, ClientEnabled: enabled, ClientMaximum: []string{"read"}}, nil
	}
	verifier, _ := NewVerifier(keyID, spki, "ttobak-api-bridge", resolver, func() int64 { return now })
	args := []byte(`{"method":"GET","path":"/api/meetings/meeting/reading","body":null}`)
	argHash := sha256.Sum256(args)
	p := payload{Version: 1, Protocol: Protocol, Destination: "ttobak-api-bridge", Sub: "user",
		ClientID: "readclient", Capabilities: []string{"read"}, Operation: "api.reading.get",
		ArgumentsSHA256: hex.EncodeToString(argHash[:]), RequestID: "request", IssuedAt: now,
		ExpiresAt: now + 60, UpstreamExpiresAt: now + 600}
	raw, _ := json.Marshal(p)
	hash := sha256.Sum256(raw)
	signature, _ := rsa.SignPSS(rand.Reader, key, crypto.SHA256, hash[:], &rsa.PSSOptions{SaltLength: 32})
	wire, _ := json.Marshal(envelope{keyID, SigningAlgorithm,
		base64.RawURLEncoding.EncodeToString(raw), base64.RawURLEncoding.EncodeToString(signature)})
	frame := InvokeFrame{InvokeProtocol, "api.reading.get", "request", EncodeBridgeBytes(wire), EncodeBridgeBytes(args)}
	calls := 0
	bridge := &Bridge{Verify: verifier.Verify, Router: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		principal, ok := VerifiedPrincipal(r.Context())
		if !ok || principal.UserID() != "user" || r.Header.Get("Authorization") != "" {
			t.Fatal("identity/token boundary lost")
		}
		_, _ = w.Write([]byte(`{"text":"synthetic notes"}`))
	})}
	result := bridge.Invoke(context.Background(), EncodeInvokeFrame(frame))
	if result.StatusCode != 200 || calls != 1 {
		t.Fatalf("valid private request failed: %+v", result)
	}
	frame.Arguments = EncodeBridgeBytes([]byte(`{"method":"GET","path":"/api/meetings/other/reading","body":null}`))
	if got := bridge.Invoke(context.Background(), EncodeInvokeFrame(frame)); got.StatusCode != 403 || calls != 1 {
		t.Fatal("resource substitution reached reader")
	}
	frame.Arguments = EncodeBridgeBytes(args)
	frame.Arguments = EncodeBridgeBytes(append(append([]byte{}, args...), []byte(` {}`)...))
	if got := bridge.Invoke(context.Background(), EncodeInvokeFrame(frame)); got.StatusCode != 403 || calls != 1 {
		t.Fatal("trailing argument JSON reached reader")
	}
	frame.Arguments = EncodeBridgeBytes(args)
	bridge.Router = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		enabled = false
		_, _ = w.Write([]byte(`{"text":"must not escape after revocation"}`))
	})
	if got := bridge.Invoke(context.Background(), EncodeInvokeFrame(frame)); got.StatusCode != 403 || strings.Contains(got.Body, "must not") {
		t.Fatal("revocation during read published source bytes")
	}
}

func TestHTTPBodyCannotSelectPrivateLambdaProtocol(t *testing.T) {
	raw := []byte(`{"path":"/api/meetings","body":"{\"protocol\":\"ttobak.connector.invoke.v1\"}","requestContext":{}}`)
	if IsInvokeFrame(raw) {
		t.Fatal("HTTP body became a private invocation")
	}
	if (&Bridge{}).Invoke(context.Background(), raw).StatusCode != 503 {
		t.Fatal("missing verifier did not fail closed")
	}
}

func TestPrivateHTTPArgumentsRejectAmbiguousFieldsWithoutReencoding(t *testing.T) {
	valid := []byte(`{"method":"GET","path":"/api/meetings?cursor=한글&limit=10","body":null}`)
	input, ok := decodeHTTPArguments(valid)
	if !ok || input.Path != "/api/meetings?cursor=한글&limit=10" {
		t.Fatal("ordinary signed query bytes were rejected")
	}
	for _, raw := range []string{
		`{"method":"GET","method":"POST","path":"/api/meetings","body":null}`,
		`{"method":"GET","Path":"/api/meetings","body":null}`,
		`{"method":"GET","path":"/api/meetings","body":null,"authorization":"forged"}`,
		`{"method":"GET","path":"/api/meetings"}`,
		`{"method":"GET","path":"/api/meetings","body":null} {}`,
		`{"method":"GET","path":"/api/meetings","body":null` + string([]byte{0xff}) + `}`,
	} {
		if _, ok := decodeHTTPArguments([]byte(raw)); ok {
			t.Fatal("ambiguous or malformed argument object accepted")
		}
	}
}
