package middleware

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
)

const currentProof = "synthetic-current-proof-1234567890"
const previousProof = "synthetic-previous-proof-1234567890"

type fakeOriginSecret struct {
	raw     string
	err     error
	calls   int
	version string
}

func (f *fakeOriginSecret) GetSecretValue(ctx context.Context, input *secretsmanager.GetSecretValueInput, options ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error) {
	f.calls++
	f.version = aws.ToString(input.VersionId)
	if input.SecretId == nil || *input.SecretId != "synthetic-secret-arn" {
		panic("secret identity changed")
	}
	config := secretsmanager.Options{}
	for _, option := range options {
		option(&config)
	}
	if config.Retryer == nil || config.Retryer.MaxAttempts() != 1 {
		panic("origin lookup must not retry")
	}
	if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 3*time.Second {
		panic("origin lookup is not bounded")
	}
	return &secretsmanager.GetSecretValueOutput{SecretString: aws.String(f.raw)}, f.err
}

func proofDocument() originProofSet {
	return originProofSet{SchemaVersion: 1, Current: currentProof, Previous: previousProof,
		RotationStartedAt: 1000, PreviousExpiresAt: 1030,
		Generation: strings.Repeat("a", 32), Phase: "rotating"}
}

func marshalProof(value originProofSet) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}

func TestOriginRotationExpiryAndCacheAreCheckedPerRequest(t *testing.T) {
	client := &fakeOriginSecret{raw: marshalProof(proofDocument())}
	now := int64(1010)
	verifier := NewOriginVerifier("synthetic-secret-arn", client, false, strings.Repeat("b", 32))
	verifier.now = func() time.Time { return time.Unix(now, 0) }
	allowed := func(value string) bool {
		return verifier.allows(context.Background(), http.Header{"X-Origin-Verify": {value}})
	}
	if !allowed(currentProof) || !allowed(previousProof) || client.calls != 1 {
		t.Fatal("consumer-first overlap or cache failed")
	}
	now = 1030
	if allowed(previousProof) || !allowed(currentProof) || client.calls != 1 {
		t.Fatal("previous proof survived its expiry in a still-valid cache")
	}
	now = 1071
	client.err = errors.New("private service detail")
	if allowed(currentProof) || allowed(previousProof) {
		t.Fatal("expired cache or legacy value used after failed ARN read")
	}
}

func TestOriginHeaderMissingDuplicateCombinedAndMalformedFailClosed(t *testing.T) {
	client := &fakeOriginSecret{raw: marshalProof(proofDocument())}
	verifier := NewOriginVerifier("synthetic-secret-arn", client, false, strings.Repeat("b", 32))
	verifier.now = func() time.Time { return time.Unix(1010, 0) }
	for _, headers := range []http.Header{
		nil, {"X-Origin-Verify": {}}, {"X-Origin-Verify": {currentProof, currentProof}},
		{"X-Origin-Verify": {currentProof}, "x-origin-verify": {currentProof}},
		{"X-Origin-Verify": {currentProof + "," + currentProof}},
		{"X-Origin-Verify": {" " + currentProof}}, {"X-Origin-Verify": {"short"}},
		{"X-Origin-Verify": {strings.Repeat("x", 129)}},
	} {
		if verifier.allows(context.Background(), headers) {
			t.Fatal("malformed or duplicate proof admitted")
		}
	}
	if client.calls != 0 {
		t.Fatal("invalid request caused a secret lookup")
	}
}

func TestOriginSecretMetadataRejectsUnboundedOrMalformedRotation(t *testing.T) {
	mutations := []func(*originProofSet){
		func(v *originProofSet) { v.SchemaVersion = 2 },
		func(v *originProofSet) { v.Generation = "unbounded" },
		func(v *originProofSet) { v.Previous = v.Current },
		func(v *originProofSet) { v.PreviousExpiresAt = v.RotationStartedAt + originMaxOverlapSeconds + 1 },
		func(v *originProofSet) { v.RotationStartedAt = 0 },
		func(v *originProofSet) { v.PreviousExpiresAt = v.RotationStartedAt },
		func(v *originProofSet) { v.Phase = "active" },
		func(v *originProofSet) { v.Current = "bad,combined-proof" },
	}
	for _, mutate := range mutations {
		value := proofDocument()
		mutate(&value)
		if _, ok := parseOriginProofSet(marshalProof(value)); ok {
			t.Fatal("invalid secret metadata admitted")
		}
	}
	stable := proofDocument()
	stable.Phase, stable.Previous, stable.PreviousExpiresAt = "active", "", 0
	for _, raw := range []string{`{}`, `null`, strings.Repeat("x", 2049), `{"schemaVersion":1}`,
		strings.Replace(marshalProof(stable), `"previous":""`, `"previous":null`, 1)} {
		if _, ok := parseOriginProofSet(raw); ok {
			t.Fatal("invalid secret schema admitted")
		}
	}
}

func TestOriginRequiresARNAndVersionOutsideExplicitLocalDevelopment(t *testing.T) {
	t.Setenv("ORIGIN_VERIFY_SECRET", previousProof)
	headers := http.Header{"X-Origin-Verify": {previousProof}}
	for _, value := range []*OriginVerifier{nil, NewOriginVerifier("", nil, false, ""),
		NewOriginVerifier("synthetic-secret-arn", nil, true, ""),
		NewOriginVerifier("synthetic-secret-arn", &fakeOriginSecret{raw: marshalProof(proofDocument())}, false, "")} {
		if value.allows(context.Background(), headers) {
			t.Fatal("legacy environment or missing pin admitted request")
		}
	}
	if !NewOriginVerifier("", nil, true, "").allows(context.Background(), nil) {
		t.Fatal("explicit local mode unavailable")
	}
}

func TestOriginCanceledLookupAndHTTPFailureRevealNoSecrets(t *testing.T) {
	client := &fakeOriginSecret{raw: marshalProof(proofDocument())}
	verifier := NewOriginVerifier("synthetic-secret-arn", client, false, strings.Repeat("b", 32))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if verifier.allows(ctx, http.Header{"X-Origin-Verify": {currentProof}}) || client.calls != 0 {
		t.Fatal("canceled lookup continued")
	}
	w := httptest.NewRecorder()
	verifier.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("failed origin reached application")
	})).ServeHTTP(w, httptest.NewRequest("GET", "/api/public/docs/synthetic-capability", nil))
	if w.Code != 403 || strings.Contains(w.Body.String(), "synthetic") || strings.Contains(w.Body.String(), "private") {
		t.Fatal("origin failure leaked values or admitted request")
	}
}

func TestOriginProtocolMarkerRequiresVerifiedHTTP(t *testing.T) {
	verifier := NewOriginVerifier("synthetic-secret-arn", &fakeOriginSecret{raw: marshalProof(proofDocument())}, false, strings.Repeat("b", 32))
	verifier.now = func() time.Time { return time.Unix(1010, 0) }
	for _, status := range []int{200, 401, 404, 405} {
		r := httptest.NewRequest("GET", "/api/public/docs/__origin_migration_probe__", nil)
		r.Header.Set("X-Origin-Verify", previousProof)
		w := httptest.NewRecorder()
		verifier.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
		})).ServeHTTP(w, r)
		if w.Code != status || w.Header().Get("X-Ttobak-Origin-Protocol") != "1" {
			t.Fatal("verified early response lost protocol marker or changed status")
		}
	}
	w := httptest.NewRecorder()
	verifier.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("unverified request reached application")
	})).ServeHTTP(w, httptest.NewRequest("GET", "/api/public/docs/__origin_migration_probe__", nil))
	if w.Code != 403 || w.Header().Get("X-Ttobak-Origin-Protocol") != "" {
		t.Fatal("unverified request advertised the verified protocol")
	}
}

func TestOriginReadPinsImmutableVersion(t *testing.T) {
	client := &fakeOriginSecret{raw: marshalProof(proofDocument())}
	pin := strings.Repeat("b", 32)
	v := NewOriginVerifier("synthetic-secret-arn", client, false, pin)
	v.now = func() time.Time { return time.Unix(1010, 0) }
	if !v.allows(context.Background(), http.Header{"X-Origin-Verify": {currentProof}}) || client.version != pin {
		t.Fatal("immutable version pin was not sent to Secrets Manager")
	}
}

func TestOriginDiagnosticsContainOnlyFixedFailureCategories(t *testing.T) {
	for _, scenario := range []struct {
		category string
		version  string
		client   *fakeOriginSecret
	}{
		{"configuration_invalid", "", &fakeOriginSecret{}},
		{"secret_unavailable", strings.Repeat("b", 32), &fakeOriginSecret{err: errors.New("private provider message")}},
		{"secret_invalid", strings.Repeat("b", 32), &fakeOriginSecret{raw: "private malformed document"}},
	} {
		t.Run(scenario.category, func(t *testing.T) {
			var output bytes.Buffer
			previous := log.Writer()
			log.SetOutput(&output)
			defer log.SetOutput(previous)
			v := NewOriginVerifier("synthetic-secret-arn", scenario.client, false, scenario.version)
			if v.allows(context.Background(), http.Header{"X-Origin-Verify": {currentProof}}) {
				t.Fatal("failure was authorized")
			}
			if !strings.Contains(output.String(), "category="+scenario.category) ||
				strings.Contains(output.String(), "private") || strings.Contains(output.String(), currentProof) ||
				strings.Contains(output.String(), "synthetic-secret-arn") {
				t.Fatal("diagnostic category missing or value leaked")
			}
		})
	}
}
