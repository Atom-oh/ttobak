package handler

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

	"github.com/ttobak/backend/internal/middleware"
	"github.com/ttobak/backend/internal/service"
)

type fakeSegmentTranslator struct {
	calls int
	got   service.TranslateRequest
	out   service.TranslateResult
	err   error
}

func (f *fakeSegmentTranslator) TranslateSegment(_ context.Context, req service.TranslateRequest) (service.TranslateResult, error) {
	f.calls++
	f.got = req
	return f.out, f.err
}

func postTranslate(h *TranslateHandler, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/translate", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.Translate(rec, req)
	return rec
}

func TestTranslateHandlerRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"malformed json", `{`},
		{"missing text", `{"targetLang":"en"}`},
		{"blank text", `{"text":"   ","targetLang":"en"}`},
		{"missing target", `{"text":"hi"}`},
		{"oversized text", `{"text":"` + strings.Repeat("a", translateMaxTextBytes+1) + `","targetLang":"ko"}`},
		{"unknown target", `{"text":"hi","targetLang":"tlh"}`},
		{"auto is not a target", `{"text":"hi","targetLang":"auto"}`},
		{"bilingual sentinel is client-only", `{"text":"hi","targetLang":"ko-en"}`},
		{"unknown source", `{"text":"hi","sourceLang":"xx","targetLang":"ko"}`},
		{"same language", `{"text":"hi","sourceLang":"en","targetLang":"en"}`},
		{"unknown quality", `{"text":"hi","targetLang":"ko","quality":"ultra"}`},
		{"too many context items", `{"text":"hi","targetLang":"ko","context":["a","b","c"]}`},
		{"context too long", `{"text":"hi","targetLang":"ko","context":["` + strings.Repeat("a", translateMaxContextBytes+1) + `"]}`},
		{"body over limit", `{"text":"hi","targetLang":"ko","pad":"` + strings.Repeat("a", translateMaxBodyBytes) + `"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := &fakeSegmentTranslator{}
			rec := postTranslate(NewTranslateHandler(svc), tt.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
			if svc.calls != 0 {
				t.Fatal("service called for an invalid request")
			}
		})
	}
}

func TestTranslateHandlerSuccessAndDefaults(t *testing.T) {
	svc := &fakeSegmentTranslator{out: service.TranslateResult{Text: "hello", Engine: service.TranslateEngineLLM}}
	rec := postTranslate(NewTranslateHandler(svc), `{"text":"안녕","targetLang":"en","quality":"high","context":["앞 문장"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var res map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res["limited"] != false {
		t.Fatalf("limited = %v, want false", res["limited"])
	}
	want := map[string]string{"translatedText": "hello", "sourceLang": "auto", "targetLang": "en", "quality": "high", "engine": "llm"}
	for k, v := range want {
		if res[k] != v {
			t.Fatalf("%s = %q, want %q (response %v)", k, res[k], v, res)
		}
	}
	if svc.got.Quality != service.TranslateQualityHigh || len(svc.got.Context) != 1 || svc.got.SourceLang != "auto" {
		t.Fatalf("service request = %+v", svc.got)
	}

	// The pre-existing request shape must keep working and stay on the fast engine.
	svc = &fakeSegmentTranslator{out: service.TranslateResult{Text: "hello", Engine: service.TranslateEngineTranslate}}
	rec = postTranslate(NewTranslateHandler(svc), `{"text":"안녕","sourceLang":"ko","targetLang":"en"}`)
	if rec.Code != http.StatusOK || svc.got.Quality != service.TranslateQualityFast {
		t.Fatalf("legacy request: status=%d quality=%q", rec.Code, svc.got.Quality)
	}
}

func TestTranslateHandlerDoesNotLeakServiceErrors(t *testing.T) {
	svc := &fakeSegmentTranslator{err: errors.New("AccessDenied: secret-arn arn:aws:bedrock:us-east-1:111:secret")}
	rec := postTranslate(NewTranslateHandler(svc), `{"text":"비밀 회의 내용","targetLang":"en"}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "secret") || strings.Contains(body, "AccessDenied") || strings.Contains(body, "비밀") {
		t.Fatalf("error response leaks details: %s", body)
	}
	if !strings.Contains(body, "INTERNAL_ERROR") {
		t.Fatalf("error shape lost: %s", body)
	}
}

type limiterCall struct {
	userID, bucket string
	limit          int64
}

// fakeTranslateLimiter reports a bucket as exceeded when listed in over.
type fakeTranslateLimiter struct {
	over  map[string]bool
	calls []limiterCall
	end   time.Time
}

func (f *fakeTranslateLimiter) Exceeds(_ context.Context, userID, bucket string, _ time.Duration, _ int64, limit int64) (bool, service.WindowUsage) {
	f.calls = append(f.calls, limiterCall{userID, bucket, limit})
	return f.over[bucket], service.WindowUsage{WindowEnd: f.end}
}

func postTranslateAs(h *TranslateHandler, userID, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/translate", strings.NewReader(body))
	if userID != "" {
		req = req.WithContext(context.WithValue(req.Context(), middleware.UserIDKey, userID))
	}
	rec := httptest.NewRecorder()
	h.Translate(rec, req)
	return rec
}

func TestTranslateHandlerRateLimits(t *testing.T) {
	now := time.Date(2026, 10, 18, 4, 59, 30, 0, time.UTC)
	tests := []struct {
		name        string
		quality     string
		over        map[string]bool
		userID      string
		wantStatus  int
		wantQuality string
		wantLimited bool
		wantBuckets []string
	}{
		{"fast under limit", "fast", nil, "u1", http.StatusOK, "fast", false, []string{"translate"}},
		{"high under both limits", "high", nil, "u1", http.StatusOK, "high", false, []string{"translate", "translate-high"}},
		{"high over high limit downgrades", "high", map[string]bool{"translate-high": true}, "u1", http.StatusOK, "fast", true, []string{"translate", "translate-high"}},
		{"fast is never charged to the high bucket", "fast", map[string]bool{"translate-high": true}, "u1", http.StatusOK, "fast", false, []string{"translate"}},
		{"over hourly limit is rejected", "high", map[string]bool{"translate": true}, "u1", http.StatusTooManyRequests, "", false, []string{"translate"}},
		{"missing identity is not counted", "high", map[string]bool{"translate": true}, "", http.StatusOK, "high", false, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := &fakeSegmentTranslator{out: service.TranslateResult{Text: "hello", Engine: service.TranslateEngineTranslate}}
			limiter := &fakeTranslateLimiter{over: tt.over, end: now.Add(30 * time.Second)}
			h := NewTranslateHandler(svc)
			h.now = func() time.Time { return now }
			h.SetRateLimits(limiter, TranslateLimits{Hourly: 3000, HighHourly: 1500})
			rec := postTranslateAs(h, tt.userID, `{"text":"안녕","targetLang":"en","quality":"`+tt.quality+`"}`)
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
			var buckets []string
			for _, call := range limiter.calls {
				buckets = append(buckets, call.bucket)
				if call.userID != tt.userID {
					t.Fatalf("limiter user = %q", call.userID)
				}
				want := int64(3000)
				if call.bucket == "translate-high" {
					want = 1500
				}
				if call.limit != want {
					t.Fatalf("%s limit = %d, want %d", call.bucket, call.limit, want)
				}
			}
			if strings.Join(buckets, ",") != strings.Join(tt.wantBuckets, ",") {
				t.Fatalf("buckets = %v, want %v", buckets, tt.wantBuckets)
			}
			if tt.wantStatus == http.StatusTooManyRequests {
				if svc.calls != 0 {
					t.Fatal("a rejected request must not reach the translator")
				}
				if !strings.Contains(rec.Body.String(), `"RATE_LIMITED"`) || rec.Header().Get("Retry-After") != "30" {
					t.Fatalf("429 shape: retry-after=%q body=%s", rec.Header().Get("Retry-After"), rec.Body.String())
				}
				return
			}
			if svc.got.Quality != tt.wantQuality {
				t.Fatalf("service quality = %q, want %q", svc.got.Quality, tt.wantQuality)
			}
			var res map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
				t.Fatal(err)
			}
			if res["limited"] != tt.wantLimited || res["quality"] != tt.wantQuality {
				t.Fatalf("response = %v", res)
			}
		})
	}
}

func TestTranslateHandlerMeteringOmitsTextAndUser(t *testing.T) {
	var buf bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(previous)
	svc := &fakeSegmentTranslator{out: service.TranslateResult{Text: "synthetic output", Engine: service.TranslateEngineTranslate, FallbackReason: service.TranslateFallbackLLMError}}
	h := NewTranslateHandler(svc)
	h.SetRateLimits(&fakeTranslateLimiter{}, TranslateLimits{Hourly: 1, HighHourly: 1})
	rec := postTranslateAs(h, "user-secret-id", `{"text":"합성 문장","targetLang":"en","quality":"high"}`)
	log.SetOutput(previous)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	out := buf.String()
	if strings.Contains(out, "합성") || strings.Contains(out, "synthetic output") || strings.Contains(out, "user-secret-id") {
		t.Fatalf("metering leaks content or identity: %s", out)
	}
	line := out[strings.Index(out, "{"):]
	var meter map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &meter); err != nil {
		t.Fatalf("metering line is not JSON: %q", out)
	}
	for key, want := range map[string]any{"evt": "translate", "q": "high", "engine": "translate", "src": "auto", "tgt": "en",
		"inBytes": float64(len("합성 문장")), "outBytes": float64(len("synthetic output")), "limited": false, "fallbackReason": "llm-error"} {
		if meter[key] != want {
			t.Fatalf("%s = %v, want %v (line %s)", key, meter[key], want, line)
		}
	}
	if _, ok := meter["ms"]; !ok {
		t.Fatal("metering line has no latency")
	}
}
