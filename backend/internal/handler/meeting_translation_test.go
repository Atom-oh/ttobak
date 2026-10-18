package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-lambda-go/events"
	chiadapter "github.com/awslabs/aws-lambda-go-api-proxy/chi"
	"github.com/go-chi/chi/v5"
	"github.com/ttobak/backend/internal/middleware"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/service"
)

type echoSegmentTranslator struct {
	mu    sync.Mutex
	texts []string
}

func (e *echoSegmentTranslator) TranslateSegment(_ context.Context, req service.TranslateRequest) (service.TranslateResult, error) {
	e.mu.Lock()
	e.texts = append(e.texts, req.Text)
	e.mu.Unlock()
	lines := strings.Split(req.Text, "\n")
	for i := range lines {
		lines[i] = "EN " + lines[i]
	}
	return service.TranslateResult{Text: strings.Join(lines, "\n"), Engine: service.TranslateEngineLLM}, nil
}

type fixedLimiter struct {
	over bool
	n    int64
}

func (l *fixedLimiter) Exceeds(_ context.Context, _, _ string, _ time.Duration, n, _ int64) (bool, service.WindowUsage) {
	l.n += n
	return l.over, service.WindowUsage{WindowEnd: time.Now().Add(90 * time.Second)}
}

type translationFixture struct {
	*readingFixture
	translator *echoSegmentTranslator
	limiter    *fixedLimiter
	adapter    *chiadapter.ChiLambda
}

func newTranslationFixture(t *testing.T, enabled bool) *translationFixture {
	f := &translationFixture{readingFixture: newReadingFixture(t), translator: &echoSegmentTranslator{}, limiter: &fixedLimiter{}}
	f.meeting.Content = "# 회의록\n\n- 예산을 [근거](transcript://s1) 기준으로 확정합니다"
	router := chi.NewRouter()
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if id := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "); id != "" {
				r = r.WithContext(context.WithValue(r.Context(), middleware.UserIDKey, id))
			}
			next.ServeHTTP(w, r)
		})
	})
	analysis := service.NewActionItemsAnalysisService(f.repo, service.NewMeetingService(f.repo), nil, nil)
	reading := service.NewMeetingReadingService(f.repo, analysis)
	svc := service.NewMeetingTranslationService(reading, f.translator, f.limiter, service.DefaultMeetingTranslateDailyBytes)
	router.Post("/api/meetings/{meetingId}/translation", NewMeetingTranslationHandler(svc, enabled).Post)
	f.adapter = chiadapter.New(router)
	return f
}

func (f *translationFixture) post(t *testing.T, body string) events.APIGatewayProxyResponse {
	t.Helper()
	response, err := f.adapter.ProxyWithContext(context.Background(), events.APIGatewayProxyRequest{
		Path: "/api/meetings/m1/translation", HTTPMethod: "POST", Body: body,
		Headers:        map[string]string{"Authorization": "Bearer " + f.user, "Content-Type": "application/json"},
		RequestContext: events.APIGatewayProxyRequestContext{Path: "/api/meetings/m1/translation"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func header(r events.APIGatewayProxyResponse, name string) string {
	if values := r.MultiValueHeaders[name]; len(values) > 0 {
		return values[0]
	}
	return r.Headers[name]
}

func errorCode(t *testing.T, r events.APIGatewayProxyResponse) string {
	t.Helper()
	var body struct {
		Error struct{ Code string } `json:"error"`
	}
	if err := json.Unmarshal([]byte(r.Body), &body); err != nil {
		t.Fatalf("not an API error: %s", r.Body)
	}
	return body.Error.Code
}

func TestMeetingTranslationAPITranslatesNotesWithoutPersisting(t *testing.T) {
	f := newTranslationFixture(t, true)
	r := f.post(t, `{"part":"notes","target":"en"}`)
	if r.StatusCode != 200 || len(r.Body) > service.MaxMeetingTranslationBytes || header(r, "Cache-Control") != "no-store" {
		t.Fatalf("status=%d headers=%v body=%s", r.StatusCode, r.Headers, r.Body)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(r.Body), &out); err != nil {
		t.Fatal(err)
	}
	if out["notes"] != "# EN 회의록\n\n- EN 예산을 [근거](transcript://s1) 기준으로 확정합니다" || out["persisted"] != false || out["complete"] != true {
		t.Fatalf("out = %+v", out)
	}
	if len(f.s3Keys) != 0 {
		t.Fatalf("notes translation hydrated transcripts: %v", f.s3Keys)
	}
	if f.limiter.n == 0 {
		t.Fatal("daily byte counter not charged")
	}
}

func TestMeetingTranslationAPITranscriptUsesSelectedSourceOnly(t *testing.T) {
	f := newTranslationFixture(t, true)
	f.meeting.TranscriptA = "[김]\n안녕하세요 여러분\n"
	r := f.post(t, `{"part":"transcript","target":"en"}`)
	if r.StatusCode != 200 {
		t.Fatalf("status=%d body=%s", r.StatusCode, r.Body)
	}
	if !strings.Contains(r.Body, `"speaker":"김"`) || !strings.Contains(r.Body, "EN 안녕하세요 여러분") {
		t.Fatalf("body=%s", r.Body)
	}
	for _, text := range f.translator.texts {
		if strings.Contains(text, "김") {
			t.Fatalf("speaker label sent: %q", text)
		}
	}
}

func TestMeetingTranslationAPIAuthorizationAndRestrictedSummary(t *testing.T) {
	f := newTranslationFixture(t, true)
	f.user = "stranger"
	if r := f.post(t, `{"part":"notes","target":"en"}`); r.StatusCode != 404 || errorCode(t, r) != "NOT_FOUND" || len(f.translator.texts) != 0 {
		t.Fatalf("non-reader: %d %s", r.StatusCode, r.Body)
	}
	f = newTranslationFixture(t, true)
	f.user = "viewer"
	f.share = &model.Share{OwnerID: "owner", MeetingID: "m1", Permission: "read", Origin: model.ShareOriginAccount}
	f.meeting.SharedToAccount = true
	f.meeting.AccountID = "acct"
	f.member = &model.AccountMember{AccountID: "acct", UserID: "viewer", SummaryPrivacyVersion: 1}
	if r := f.post(t, `{"part":"notes","target":"en"}`); r.StatusCode != 409 || errorCode(t, r) != "SHARING_REGEN_REQUIRED" || len(f.translator.texts) != 0 {
		t.Fatalf("restricted summary translated: %d %s", r.StatusCode, r.Body)
	}
	f.user = ""
	if r := f.post(t, `{"part":"notes","target":"en"}`); r.StatusCode != 401 {
		t.Fatalf("unauthenticated: %d", r.StatusCode)
	}
}

func TestMeetingTranslationAPIStaleCursor(t *testing.T) {
	f := newTranslationFixture(t, true)
	f.meeting.Content = strings.Repeat("회의 내용을 정리했습니다.\n", 600)
	r := f.post(t, `{"part":"notes","target":"en"}`)
	var first struct {
		NextCursor *string `json:"nextCursor"`
	}
	if r.StatusCode != 200 || json.Unmarshal([]byte(r.Body), &first) != nil || first.NextCursor == nil {
		t.Fatalf("no continuation: %d %s", r.StatusCode, r.Body[:min(len(r.Body), 200)])
	}
	body, _ := json.Marshal(map[string]string{"part": "notes", "target": "en", "cursor": *first.NextCursor})
	if r := f.post(t, string(body)); r.StatusCode != 200 {
		t.Fatalf("continuation rejected: %d %s", r.StatusCode, r.Body)
	}
	f.meeting.Content += "정정"
	if r := f.post(t, string(body)); r.StatusCode != 409 || errorCode(t, r) != "STALE_CURSOR" {
		t.Fatalf("stale cursor accepted: %d %s", r.StatusCode, r.Body)
	}
	body, _ = json.Marshal(map[string]string{"part": "transcript", "target": "en", "cursor": *first.NextCursor})
	if r := f.post(t, string(body)); r.StatusCode != 409 || errorCode(t, r) != "STALE_CURSOR" {
		t.Fatalf("notes cursor reused for transcript: %d %s", r.StatusCode, r.Body)
	}
}

func TestMeetingTranslationAPIRateLimitAndFlagOff(t *testing.T) {
	f := newTranslationFixture(t, true)
	f.limiter.over = true
	r := f.post(t, `{"part":"notes","target":"en"}`)
	if r.StatusCode != 429 || errorCode(t, r) != model.ErrCodeRateLimited || header(r, "Retry-After") == "" || len(f.translator.texts) != 0 {
		t.Fatalf("limit: %d %v %s", r.StatusCode, r.Headers, r.Body)
	}
	off := newTranslationFixture(t, false)
	if r := off.post(t, `{"part":"notes","target":"en"}`); r.StatusCode != 404 || errorCode(t, r) != "FEATURE_DISABLED" || off.reads != 0 {
		t.Fatalf("flag off: %d %s reads=%d", r.StatusCode, r.Body, off.reads)
	}
}

func TestMeetingTranslationAPIStrictBody(t *testing.T) {
	f := newTranslationFixture(t, true)
	for _, tc := range []struct {
		body   string
		status int
		code   string
	}{
		{`{"part":"notes","target":"en","extra":1}`, 400, "BAD_REQUEST"},
		{`{"part":"notes","target":"en"} {}`, 400, "BAD_REQUEST"},
		{`{"part":"notes","target":"ja"}`, 400, "BAD_REQUEST"},
		{`{"part":"summary","target":"en"}`, 400, "BAD_REQUEST"},
		{`{"part":"notes","target":"en","cursor":"!"}`, 400, "INVALID_CURSOR"},
		{`{"part":"notes","target":"en","cursor":"` + strings.Repeat("a", 2049) + `"}`, 400, "INVALID_CURSOR"},
		{`not json`, 400, "BAD_REQUEST"},
		{`{"part":"notes","target":"en","cursor":"` + strings.Repeat("a", 4096) + `"}`, 413, "BAD_REQUEST"},
	} {
		if r := f.post(t, tc.body); r.StatusCode != tc.status || errorCode(t, r) != tc.code {
			t.Fatalf("%.60s: status=%d body=%s", tc.body, r.StatusCode, r.Body)
		}
	}
	if f.reads != 0 || len(f.translator.texts) != 0 {
		t.Fatalf("invalid body reached storage: reads=%d", f.reads)
	}
}
