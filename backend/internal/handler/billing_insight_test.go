package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/ttobak/backend/internal/middleware"
	"github.com/ttobak/backend/internal/repository"
	"github.com/ttobak/backend/internal/service"
)

type capturePutClient struct {
	inputs []*dynamodb.PutItemInput
	err    error
}

func (c *capturePutClient) PutItem(_ context.Context, in *dynamodb.PutItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	c.inputs = append(c.inputs, in)
	return &dynamodb.PutItemOutput{}, c.err
}

func newBillingInsightTestHandler(t *testing.T, rawURL string, client *capturePutClient) *BillingInsightHandler {
	t.Helper()
	origin, _ := service.ParseBillingInsightOrigin(rawURL)
	repo := repository.NewBillingHandoffRepository(client, "ttobak-main")
	return NewBillingInsightHandler(service.NewBillingInsightService(repo, origin))
}

type billingIdentity struct {
	id, email string
	verified  *bool
}

func billingRequest(identity billingIdentity, body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/api/billing-insight/handoff", strings.NewReader(body))
	ctx := req.Context()
	if identity.id != "" {
		ctx = context.WithValue(ctx, middleware.UserIDKey, identity.id)
	}
	if identity.email != "" {
		ctx = context.WithValue(ctx, middleware.UserEmailKey, identity.email)
	}
	if identity.verified != nil {
		ctx = context.WithValue(ctx, middleware.UserEmailVerifiedKey, *identity.verified)
	}
	return req.WithContext(ctx)
}

func TestBillingInsightHandoffRejections(t *testing.T) {
	yes, no := true, false
	full := billingIdentity{"caller", "caller@example.com", &yes}
	for _, tc := range []struct {
		name     string
		url      string
		identity billingIdentity
		body     string
		status   int
		code     string
	}{
		{"feature unset", "", full, "", 404, "NOT_FOUND"},
		{"http url", "http://insight.example.com", full, "", 404, "NOT_FOUND"},
		{"url with path", "https://insight.example.com/app", full, "", 404, "NOT_FOUND"},
		{"url with query", "https://insight.example.com/?x=1", full, "", 404, "NOT_FOUND"},
		{"url with fragment", "https://insight.example.com/#x", full, "", 404, "NOT_FOUND"},
		{"url without host", "https:///", full, "", 404, "NOT_FOUND"},
		{"no user", "https://insight.example.com", billingIdentity{"", "caller@example.com", &yes}, "", 401, "UNAUTHORIZED"},
		{"no email", "https://insight.example.com", billingIdentity{"caller", "", &yes}, "", 403, "FORBIDDEN"},
		{"unverified email", "https://insight.example.com", billingIdentity{"caller", "caller@example.com", &no}, "", 403, "FORBIDDEN"},
		{"oversized body", "https://insight.example.com", full, strings.Repeat("x", 1025), 400, "BAD_REQUEST"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &capturePutClient{}
			h := newBillingInsightTestHandler(t, tc.url, client)
			w := httptest.NewRecorder()
			h.CreateHandoff(w, billingRequest(tc.identity, tc.body))
			var body struct {
				Error struct{ Code string } `json:"error"`
			}
			_ = json.Unmarshal(w.Body.Bytes(), &body)
			if w.Code != tc.status || body.Error.Code != tc.code {
				t.Fatalf("status=%d code=%q body=%s", w.Code, body.Error.Code, w.Body.String())
			}
			if len(client.inputs) != 0 {
				t.Fatal("rejected request must not store a hand-off")
			}
			if w.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("Cache-Control = %q", w.Header().Get("Cache-Control"))
			}
		})
	}
}

var handoffCodePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

func TestBillingInsightHandoffSuccess(t *testing.T) {
	for _, configured := range []string{"https://insight.example.com", "https://insight.example.com/"} {
		t.Run(configured, func(t *testing.T) {
			client := &capturePutClient{}
			h := newBillingInsightTestHandler(t, configured, client)
			before := time.Now().Unix()
			w := httptest.NewRecorder()
			// Missing email_verified (not a verified false) is accepted; the
			// body is ignored and cannot change identity.
			h.CreateHandoff(w, billingRequest(billingIdentity{"caller", "caller@example.com", nil}, `{"sub":"victim"}`))
			after := time.Now().Unix()
			if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("status=%d headers=%v body=%s", w.Code, w.Header(), w.Body.String())
			}
			var body map[string]string
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || len(body) != 1 {
				t.Fatalf("body=%s err=%v", w.Body.String(), err)
			}
			const prefix = "https://insight.example.com/login/ttobak?code="
			if !strings.HasPrefix(body["url"], prefix) {
				t.Fatalf("url = %q", body["url"])
			}
			code := strings.TrimPrefix(body["url"], prefix)
			if !handoffCodePattern.MatchString(code) {
				t.Fatalf("code %q is not 43 base64url chars", code)
			}
			if u, err := url.Parse(body["url"]); err != nil || u.Query().Get("code") != code {
				t.Fatalf("url does not round-trip: %v", err)
			}
			if len(client.inputs) != 1 {
				t.Fatalf("puts = %d", len(client.inputs))
			}
			item := client.inputs[0].Item
			sum := sha256.Sum256([]byte(code))
			str := func(name string) string {
				s, _ := item[name].(*types.AttributeValueMemberS)
				if s == nil {
					return ""
				}
				return s.Value
			}
			if str("PK") != "HANDOFF#"+hex.EncodeToString(sum[:]) || str("SK") != "HANDOFF" {
				t.Fatalf("key = %q / %q", str("PK"), str("SK"))
			}
			if str("target") != "billing-insight" || str("sub") != "caller" || str("email") != "caller@example.com" {
				t.Fatalf("item = %v", item)
			}
			ttlAttr, _ := item["pendingShareExpiresAt"].(*types.AttributeValueMemberN)
			if ttlAttr == nil {
				t.Fatal("missing TTL")
			}
			ttl, _ := strconv.ParseInt(ttlAttr.Value, 10, 64)
			if ttl < before+60 || ttl > after+60 {
				t.Fatalf("ttl %d outside [%d,%d]", ttl, before+60, after+60)
			}
			for name := range item {
				if strings.HasPrefix(name, "GSI") {
					t.Fatalf("unexpected GSI attribute %s", name)
				}
				if v, ok := item[name].(*types.AttributeValueMemberS); ok && strings.Contains(v.Value, code) {
					t.Fatalf("plaintext code stored in %s", name)
				}
			}
		})
	}
}

func TestBillingInsightHandoffStoreFailures(t *testing.T) {
	for _, err := range []error{&types.ConditionalCheckFailedException{}, errors.New("throttled")} {
		client := &capturePutClient{err: err}
		h := newBillingInsightTestHandler(t, "https://insight.example.com", client)
		w := httptest.NewRecorder()
		h.CreateHandoff(w, billingRequest(billingIdentity{"caller", "caller@example.com", nil}, ""))
		if w.Code != 500 || strings.Contains(w.Body.String(), "code=") {
			t.Fatalf("err=%v status=%d body=%s", err, w.Code, w.Body.String())
		}
	}
}
