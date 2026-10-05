package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
	var res map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
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
