package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/ttobak/backend/internal/middleware"
)

const testSecretARN = "arn:aws:secretsmanager:ap-northeast-2:111111111111:secret:ws-origin-abcdef"
const testMethodARN = "arn:aws:execute-api:ap-northeast-2:111111111111:api/production/$connect"

type secretFixture struct {
	value   string
	failure bool
	calls   atomic.Int32
}

func newSecretFixture(t *testing.T, fixture *secretFixture) *originVerifier {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fixture.calls.Add(1)
		if r.Method != http.MethodPost || r.Header.Get("X-Amz-Target") != "secretsmanager.GetSecretValue" {
			t.Errorf("unexpected Secrets Manager request: %s %s", r.Method, r.Header.Get("X-Amz-Target"))
		}
		if !strings.Contains(r.Header.Get("Authorization"), "/secretsmanager/aws4_request") {
			t.Error("request did not use the real SDK SigV4 service")
		}
		var input struct {
			SecretID string `json:"SecretId"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil || input.SecretID != testSecretARN {
			t.Errorf("wrong secret identity: %+v, %v", input, err)
		}
		w.Header().Set("Content-Type", "application/x-amz-json-1.1")
		if fixture.failure {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"__type":"InternalServiceError","Message":"synthetic private failure detail"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"SecretString": fixture.value})
	}))
	t.Cleanup(server.Close)
	client := secretsmanager.NewFromConfig(aws.Config{
		Region:      "ap-northeast-2",
		Credentials: credentials.NewStaticCredentialsProvider("synthetic", "synthetic", ""),
		HTTPClient:  server.Client(),
	}, func(options *secretsmanager.Options) { options.BaseEndpoint = aws.String(server.URL) })
	return newOriginVerifier(testSecretARN, client)
}

func proxyEvent(value string) events.APIGatewayCustomAuthorizerRequestTypeRequest {
	return events.APIGatewayCustomAuthorizerRequestTypeRequest{
		MethodArn:             testMethodARN,
		Headers:               map[string]string{originHeaderName: value},
		QueryStringParameters: map[string]string{"token": "synthetic-valid-jwt"},
	}
}

func TestProxyRequiresBothOriginAndCurrentJWT(t *testing.T) {
	fixture := &secretFixture{value: strings.Repeat("a", 64)}
	origin := newSecretFixture(t, fixture)
	oldPool, oldToken, oldOrigin := cognitoUserPoolID, verifyToken, cloudFrontOrigin
	t.Cleanup(func() { cognitoUserPoolID, verifyToken, cloudFrontOrigin = oldPool, oldToken, oldOrigin })
	cognitoUserPoolID, cloudFrontOrigin = "test-pool", origin
	tokenCalls := 0
	verifyToken = func(token string) (*middleware.ALBOIDCClaims, error) {
		tokenCalls++
		if token != "synthetic-valid-jwt" {
			return nil, errors.New("synthetic private token detail")
		}
		return &middleware.ALBOIDCClaims{Sub: "synthetic-user"}, nil
	}
	for i := 0; i < 2; i++ {
		response, err := handler(context.Background(), proxyEvent(fixture.value))
		if err != nil || response.PrincipalID != "synthetic-user" || response.Context["userId"] != "synthetic-user" {
			t.Fatalf("valid proxy request rejected: %+v, %v", response, err)
		}
		statement := response.PolicyDocument.Statement[0]
		if statement.Effect != "Allow" || len(statement.Resource) != 1 || statement.Resource[0] != testMethodARN {
			t.Fatalf("authorizer policy is not scoped to the connect ARN: %+v", statement)
		}
		encoded, _ := json.Marshal(response)
		if bytes.Contains(encoded, []byte(fixture.value)) {
			t.Fatal("origin proof leaked into authorizer response")
		}
	}
	if fixture.calls.Load() != 1 || tokenCalls != 2 {
		t.Fatalf("expected cached secret and per-connect JWT verification: secret=%d jwt=%d", fixture.calls.Load(), tokenCalls)
	}
	event := proxyEvent(fixture.value)
	event.QueryStringParameters["token"] = "synthetic-invalid-jwt"
	var logs bytes.Buffer
	oldOutput := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(oldOutput) })
	response, _ := handler(context.Background(), event)
	if response.PolicyDocument.Statement[0].Effect != "Deny" || tokenCalls != 3 {
		t.Fatal("origin proof bypassed JWT verification")
	}
	if strings.Contains(logs.String(), "private token detail") || strings.Contains(logs.String(), event.QueryStringParameters["token"]) {
		t.Fatal("JWT failure logged private input")
	}
}

func TestOriginHeaderCanonicalizationAndMismatch(t *testing.T) {
	value := strings.Repeat("a", 64)
	for _, test := range []struct {
		name    string
		headers map[string]string
		multi   map[string][]string
		allow   bool
	}{
		{"missing", nil, nil, false},
		{"wrong", map[string]string{originHeaderName: strings.Repeat("b", 64)}, nil, false},
		{"short", map[string]string{originHeaderName: "not-a-proof"}, nil, false},
		{"case", map[string]string{"X-Origin-Verify": value}, nil, true},
		{"duplicate-case", map[string]string{"X-Origin-Verify": value, originHeaderName: value}, nil, false},
		{"multi-single", nil, map[string][]string{"X-Origin-Verify": {value}}, true},
		{"matching-multi", map[string]string{originHeaderName: value}, map[string][]string{originHeaderName: {value}}, true},
		{"multi-duplicate", nil, map[string][]string{originHeaderName: {value, value}}, false},
		{"conflicting-multi", map[string]string{originHeaderName: value}, map[string][]string{originHeaderName: {strings.Repeat("b", 64)}}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := &secretFixture{value: value}
			verifier := newSecretFixture(t, fixture)
			event := proxyEvent(value)
			event.Headers, event.MultiValueHeaders = test.headers, test.multi
			if got := verifier.verify(context.Background(), event).allowed(); got != test.allow {
				t.Fatalf("allowed=%v, want %v", got, test.allow)
			}
		})
	}
}

func TestSecretCacheExpiresAndNeverUsesStaleValueOnFailure(t *testing.T) {
	fixture := &secretFixture{value: strings.Repeat("a", 64)}
	verifier := newSecretFixture(t, fixture)
	now := time.Unix(1000, 0)
	verifier.now = func() time.Time { return now }
	if !verifier.verify(context.Background(), proxyEvent(fixture.value)).allowed() {
		t.Fatal("initial proof rejected")
	}
	old := fixture.value
	fixture.value = strings.Repeat("b", 64)
	now = now.Add(time.Minute + time.Second)
	if verifier.verify(context.Background(), proxyEvent(old)).allowed() || !verifier.verify(context.Background(), proxyEvent(fixture.value)).allowed() {
		t.Fatal("expired proof was reused or new proof was rejected")
	}
	fixture.failure = true
	now = now.Add(time.Minute + time.Second)
	if verifier.verify(context.Background(), proxyEvent(fixture.value)).allowed() {
		t.Fatal("expired cache bypassed a failed secret refresh")
	}
	if fixture.calls.Load() != 3 {
		t.Fatalf("expected one SDK attempt per refresh, got %d", fixture.calls.Load())
	}
}

func TestSecretFailuresAndMissingConfigurationFailClosed(t *testing.T) {
	for _, value := range []string{"", "short", strings.Repeat("a", 63) + "\n", strings.Repeat("a", 65)} {
		fixture := &secretFixture{value: value}
		if newSecretFixture(t, fixture).verify(context.Background(), proxyEvent(strings.Repeat("a", 64))).allowed() {
			t.Fatal("malformed secret accepted")
		}
	}
	fixture := &secretFixture{value: strings.Repeat("a", 64)}
	verifier := newSecretFixture(t, fixture)
	verifier.secretARN = ""
	if verifier.verify(context.Background(), proxyEvent(fixture.value)).allowed() || fixture.calls.Load() != 0 {
		t.Fatal("missing configuration used the secret service or failed open")
	}
}

func TestConcurrentConnectsShareOnlyTheSecretCache(t *testing.T) {
	fixture := &secretFixture{value: strings.Repeat("a", 64)}
	verifier := newSecretFixture(t, fixture)
	var wait sync.WaitGroup
	for i := 0; i < 12; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if !verifier.verify(context.Background(), proxyEvent(fixture.value)).allowed() {
				t.Error("valid concurrent proof rejected")
			}
		}()
	}
	wait.Wait()
	if fixture.calls.Load() != 1 {
		t.Fatalf("concurrent cache misses were not bounded: %d", fixture.calls.Load())
	}
}

type deadlineSecretClient struct{ t *testing.T }

func (client deadlineSecretClient) GetSecretValue(ctx context.Context, _ *secretsmanager.GetSecretValueInput, _ ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error) {
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) > 3*time.Second {
		client.t.Error("secret read has no bounded deadline")
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestSecretReadRespectsCallerDeadline(t *testing.T) {
	verifier := newOriginVerifier(testSecretARN, deadlineSecretClient{t})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if verifier.verify(ctx, proxyEvent(strings.Repeat("a", 64))).allowed() {
		t.Fatal("canceled secret lookup failed open")
	}
}
