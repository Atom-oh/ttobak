package middleware

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
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ttobak/backend/internal/connector"
)

func TestPrivateBridgeUsesVerifiedSubjectThroughOriginAndAuth(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	spki, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
	keyID := "arn:aws:kms:ap-northeast-2:111111111111:key/00000000-0000-0000-0000-000000000000"
	enabled := true
	resolver := func(_ context.Context, u, c string) (connector.Grants, error) {
		return connector.Grants{UserID: u, ClientID: c, ClientEnabled: enabled, ClientMaximum: []string{"read"}}, nil
	}
	verifier, err := connector.NewVerifier(keyID, spki, "ttobak-api-bridge", resolver, nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	args := []byte(`{"method":"GET","path":"/api/meetings/meeting/reading?section=notes&limit=1","body":null}`)
	hash := sha256.Sum256(args)
	payload := []byte(fmt.Sprintf(`{"version":1,"protocol":"ttobak.connector","destination":"ttobak-api-bridge","sub":"verified-user","clientId":"client","capabilities":["read"],"approvalGeneration":0,"operation":"api.reading.get","argumentsSHA256":"%s","requestId":"request","issuedAt":%d,"expiresAt":%d,"upstreamExpiresAt":%d}`, hex.EncodeToString(hash[:]), now, now+60, now+600))
	digest := sha256.Sum256(payload)
	signature, err := rsa.SignPSS(rand.Reader, key, crypto.SHA256, digest[:], &rsa.PSSOptions{SaltLength: 32})
	if err != nil {
		t.Fatal(err)
	}
	envelope := struct {
		KeyID     string `json:"keyId"`
		Algorithm string `json:"algorithm"`
		Payload   string `json:"payload"`
		Signature string `json:"signature"`
	}{keyID, connector.SigningAlgorithm, base64.RawURLEncoding.EncodeToString(payload), base64.RawURLEncoding.EncodeToString(signature)}
	wire, _ := json.Marshal(envelope)
	frame := connector.InvokeFrame{Protocol: connector.InvokeProtocol, Operation: "api.reading.get", RequestID: "request", Delegation: connector.EncodeBridgeBytes(wire), Arguments: connector.EncodeBridgeBytes(args)}
	calls := 0
	router := NewOriginVerifier("", nil, false, "").Middleware(Auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if GetUserID(r.Context()) != "verified-user" || r.Header.Get("Authorization") != "" || len(GetUserGroups(r.Context())) != 0 {
			t.Fatal("private identity was substituted or elevated")
		}
		_, _ = w.Write([]byte(`{"notes":"synthetic authorized notes"}`))
	})))
	bridge := &connector.Bridge{Verify: verifier.Verify, Router: router}
	result := bridge.Invoke(context.Background(), connector.EncodeInvokeFrame(frame))
	if result.StatusCode != 200 || calls != 1 {
		t.Fatalf("private request failed: %+v calls=%d", result, calls)
	}
	for _, header := range []string{"Authorization", "x-amzn-oidc-data", "x-connector-principal", "x-origin-verify"} {
		request := httptest.NewRequest(http.MethodGet, "/api/meetings/meeting/reading", nil)
		request.Header.Set(header, string(wire))
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != 403 || calls != 1 {
			t.Fatal("caller header entered private context")
		}
	}
	bridge.Router = NewOriginVerifier("", nil, false, "").Middleware(Auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		enabled = false
		_, _ = w.Write([]byte("must-not-publish"))
	})))
	result = bridge.Invoke(context.Background(), connector.EncodeInvokeFrame(frame))
	if result.StatusCode != 403 || strings.Contains(result.Body, "must-not-publish") {
		t.Fatal("disabled client released private bytes")
	}
}
