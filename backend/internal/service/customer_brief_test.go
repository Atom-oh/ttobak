package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

type fakeBriefRepo struct {
	account   *model.Account
	members   map[string]bool
	state     *model.CustomerBriefState
	completes int
	cleared   bool
}

func (f *fakeBriefRepo) GetAccount(context.Context, string) (*model.Account, error) {
	return f.account, nil
}
func (f *fakeBriefRepo) GetMember(_ context.Context, accountID, userID string) (*model.AccountMember, error) {
	if !f.members[userID] {
		return nil, nil
	}
	return &model.AccountMember{AccountID: accountID, UserID: userID}, nil
}
func (f *fakeBriefRepo) GetCustomerBrief(context.Context, string, string) (*model.CustomerBriefState, error) {
	if f.state == nil {
		return nil, nil
	}
	copy := *f.state
	return &copy, nil
}
func (f *fakeBriefRepo) OpenCustomerBrief(_ context.Context, s *model.CustomerBriefState) ([]byte, error) {
	return []byte(s.Sealed), nil
}
func (f *fakeBriefRepo) matches(s *model.CustomerBriefState) bool {
	return f.state != nil && s != nil && f.state.RunID == s.RunID && f.state.Status == s.Status && f.state.LeaseUntil == s.LeaseUntil && f.state.InputHash == s.InputHash
}
func (f *fakeBriefRepo) QueueCustomerBrief(_ context.Context, prior, next *model.CustomerBriefState, clear bool) error {
	if (prior == nil) != (f.state == nil) || prior != nil && !f.matches(prior) {
		return repository.ErrConditionFailed
	}
	stored := *next
	if f.state != nil && !clear {
		stored.Sealed, stored.GeneratedAt, stored.ResultHash, stored.ModelID = f.state.Sealed, f.state.GeneratedAt, f.state.ResultHash, f.state.ModelID
	}
	f.cleared = clear
	f.state = &stored
	return nil
}
func (f *fakeBriefRepo) StartCustomerBrief(_ context.Context, s *model.CustomerBriefState, hash string, _ time.Time, lease int64) error {
	if !f.matches(s) || !f.members[s.UserID] {
		return repository.ErrConditionFailed
	}
	f.state.Status, f.state.LeaseUntil, f.state.InputHash = model.AnalysisRunning, lease, hash
	return nil
}
func (f *fakeBriefRepo) FailCustomerBrief(_ context.Context, s *model.CustomerBriefState, code string, _ time.Time) error {
	if !f.matches(s) {
		return repository.ErrConditionFailed
	}
	f.state.Status, f.state.ErrorCode, f.state.LeaseUntil = model.AnalysisFailed, code, 0
	return nil
}
func (f *fakeBriefRepo) CompleteCustomerBrief(_ context.Context, s *model.CustomerBriefState, content []byte, hash, modelID string, now time.Time) error {
	if !f.matches(s) || !f.members[s.UserID] {
		return repository.ErrConditionFailed
	}
	f.completes++
	f.state.Status, f.state.ErrorCode, f.state.LeaseUntil = model.AnalysisSucceeded, "", 0
	f.state.Sealed, f.state.ResultHash, f.state.ModelID, f.state.GeneratedAt = string(content), hash, modelID, now
	f.state.ExpiresAt = now.Add(model.CustomerBriefTTL).Unix()
	return nil
}

type briefFixture struct {
	repo      *fakeBriefRepo
	svc       *CustomerBriefService
	now       time.Time
	published []model.CustomerBriefRequested
	output    string
	genErr    error
	onModel   func()
	news      []model.CrawledDocument
	prompt    string
}

func newBriefFixture(t *testing.T) *briefFixture {
	t.Helper()
	f := &briefFixture{now: time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)}
	f.repo = &fakeBriefRepo{account: &model.Account{AccountID: "acct", Name: "하나은행"}, members: map[string]bool{"user": true}}
	f.news = []model.CrawledDocument{{SourceID: "src", DocHash: "doc1", Title: "하나은행 클라우드 전환", Summary: "하나은행이 EKS 기반 MSA 전환을 발표했다.", Source: "전자신문", CrawledAt: f.now.Add(-time.Hour).Unix()}}
	sources := CustomerBriefSources{
		Insights: func(_ context.Context, userID, accountID string, from, to time.Time) ([]model.AccountInsightDTO, error) {
			if !f.repo.members[userID] {
				return nil, ErrForbidden
			}
			return []model.AccountInsightDTO{{Type: "need", Text: "배치 지연이 주요 문제", SourceType: "meeting", SourceID: "m1", OccurredAt: f.now.Add(-48 * time.Hour)}}, nil
		},
		Meetings: func(context.Context, string, string) ([]model.MeetingRefDTO, error) {
			return []model.MeetingRefDTO{{MeetingID: "m1", Title: "주간 정례 회의", Date: f.now.Add(-48 * time.Hour)}}, nil
		},
		News: func(context.Context, string, time.Time, time.Time) ([]model.CrawledDocument, error) {
			return f.news, nil
		},
		Events: func(context.Context, string, string, time.Time, int) ([]model.AccountEvent, error) {
			return []model.AccountEvent{{EventID: "ev1", Title: "분기 리뷰", StartAt: f.now.Add(24 * time.Hour).Format(time.RFC3339)}}, nil
		},
		Calendar: func(context.Context, string, string, time.Time, time.Time, int32) ([]model.CalendarEvent, error) {
			return []model.CalendarEvent{{EventID: "c1", AccountID: "acct", Title: "킥오프", StartsAt: f.now.Add(48 * time.Hour)}}, nil
		},
		Terms: func(_ context.Context, pk string) ([]model.DictTerm, error) {
			if pk == "ACCOUNT#acct" {
				return []model.DictTerm{{TermKey: TermKey("EKS"), State: model.TermStateActive}}, nil
			}
			return nil, nil
		},
	}
	generate := func(context.Context, string, string) (string, error) {
		if f.onModel != nil {
			f.onModel()
		}
		return f.output, f.genErr
	}
	publish := func(_ context.Context, e model.CustomerBriefRequested) error {
		f.published = append(f.published, e)
		return nil
	}
	f.svc = NewCustomerBriefService(f.repo, sources, generate, publish)
	f.svc.now = func() time.Time { return f.now }
	f.svc.generate = func(ctx context.Context, system, prompt string) (string, error) {
		f.prompt = prompt
		return generate(ctx, system, prompt)
	}
	f.output = `{"situation":{"text":"MSA 전환 검토 중","refs":["I1","N1"]},
		"sinceLast":[{"text":"배치 지연 개선 논의","refs":["M1"]},{"text":"근거 없는 주장","refs":["X9"]}],
		"news":[{"text":"EKS 기반 전환 발표","why":"컨테이너 수요","refs":["N1"]},{"text":"뉴스 인용 없음","why":"x","refs":["M1"]}],
		"questions":[{"text":"전환 일정은?","refs":["N1","E1"]},{"text":"인용 없음","refs":[]}],
		"terms":[{"term":"MSA","gloss":"마이크로서비스 아키텍처","refs":["N1"]},{"term":"EKS","gloss":"관리형 쿠버네티스","refs":["N1"]},{"term":"Kafka","gloss":"메시징","refs":["N1"]}]}`
	return f
}

func (f *briefFixture) queue(t *testing.T) string {
	t.Helper()
	res, err := f.svc.Request(context.Background(), "user", "acct")
	if err != nil {
		t.Fatal(err)
	}
	return res.RunID
}

func (f *briefFixture) brief(t *testing.T) *model.CustomerBrief {
	t.Helper()
	var brief model.CustomerBrief
	if err := json.Unmarshal([]byte(f.repo.state.Sealed), &brief); err != nil {
		t.Fatal(err)
	}
	return &brief
}

func TestCustomerBriefRefusesNonMembers(t *testing.T) {
	f := newBriefFixture(t)
	run := f.queue(t)
	f.repo.members = map[string]bool{}
	if _, err := f.svc.Request(context.Background(), "user", "acct"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("request: %v", err)
	}
	if _, err := f.svc.Get(context.Background(), "user", "acct"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("get: %v", err)
	}
	if err := f.svc.Process(context.Background(), "acct", "user", run); err != nil {
		t.Fatal(err)
	}
	if f.repo.state.ErrorCode != "ACCESS_REVOKED" || f.repo.completes != 0 || f.prompt != "" {
		t.Fatalf("non-member process must fail before reading: %+v", f.repo.state)
	}
	f.repo.account = nil
	if _, err := f.svc.Get(context.Background(), "user", "acct"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing account: %v", err)
	}
}

func TestCustomerBriefRequestLeases(t *testing.T) {
	f := newBriefFixture(t)
	run := f.queue(t)
	if again := f.queue(t); again != run || len(f.published) != 1 {
		t.Fatalf("live lease must return the existing run: %s %d", again, len(f.published))
	}
	f.now = f.now.Add(customerBriefQueuedLease + time.Second)
	if replaced := f.queue(t); replaced == run || len(f.published) != 2 || f.published[1].RunID != replaced {
		t.Fatalf("expired lease must be replaced: %s %+v", replaced, f.published)
	}
}

func TestCustomerBriefPublishFailureFailsRun(t *testing.T) {
	f := newBriefFixture(t)
	f.svc.publish = func(context.Context, model.CustomerBriefRequested) error { return errors.New("down") }
	if _, err := f.svc.Request(context.Background(), "user", "acct"); !errors.Is(err, ErrCustomerBriefPublish) {
		t.Fatal(err)
	}
	if f.repo.state.Status != model.AnalysisFailed || f.repo.state.ErrorCode != model.AnalysisPublishFailed {
		t.Fatalf("%+v", f.repo.state)
	}
}

func TestCustomerBriefProcessIgnoresStaleEvents(t *testing.T) {
	f := newBriefFixture(t)
	run := f.queue(t)
	if err := f.svc.Process(context.Background(), "acct", "user", "other-run"); err != nil || f.repo.state.Status != model.AnalysisQueued {
		t.Fatalf("wrong run must be a no-op: %v %+v", err, f.repo.state)
	}
	f.repo.state.Status = model.AnalysisRunning
	if err := f.svc.Process(context.Background(), "acct", "user", run); err != nil || f.repo.state.Status != model.AnalysisRunning || f.prompt != "" {
		t.Fatalf("non-queued run must be a no-op: %v %+v", err, f.repo.state)
	}
}

func TestCustomerBriefPublishesOnlyCitedStatements(t *testing.T) {
	f := newBriefFixture(t)
	run := f.queue(t)
	if err := f.svc.Process(context.Background(), "acct", "user", run); err != nil {
		t.Fatal(err)
	}
	if f.repo.state.Status != model.AnalysisSucceeded || f.repo.state.ModelID != ClaudeOpusModelID || f.repo.state.ExpiresAt != f.now.Add(model.CustomerBriefTTL).Unix() {
		t.Fatalf("%+v", f.repo.state)
	}
	brief := f.brief(t)
	if brief.Situation == nil || len(brief.Situation.Sources) != 2 || brief.Situation.Sources[1] != (model.CustomerBriefSource{Kind: "news", ID: "src:doc1", Label: "하나은행 클라우드 전환"}) {
		t.Fatalf("situation: %+v", brief.Situation)
	}
	if len(brief.SinceLast) != 1 || len(brief.News) != 1 || len(brief.Questions) != 1 {
		t.Fatalf("unresolved or uncited statements must be dropped: %+v", brief)
	}
	if brief.SinceLast[0].Sources[0].ID != "m1" || brief.SinceLast[0].Sources[0].Label != "주간 정례 회의" {
		t.Fatalf("labels and ids come from inputs: %+v", brief.SinceLast[0])
	}
	if brief.NextMeeting == nil || brief.NextMeeting.Source.ID != "ev1" || brief.NextMeeting.Title != "분기 리뷰" {
		t.Fatalf("next meeting: %+v", brief.NextMeeting)
	}
	if len(brief.Terms) != 2 || brief.Terms[0].Term != "MSA" || brief.Terms[0].InDictionary || !brief.Terms[1].InDictionary {
		t.Fatalf("terms must occur in cited text and carry dictionary state: %+v", brief.Terms)
	}
	if !strings.Contains(f.prompt, "<data>") || strings.Contains(f.prompt, "src:doc1") {
		t.Fatalf("prompt must use opaque refs only")
	}
	got, err := f.svc.Get(context.Background(), "user", "acct")
	if err != nil || got.Brief == nil || got.Status != model.AnalysisSucceeded || got.GeneratedAt == nil {
		t.Fatalf("get: %v %+v", err, got)
	}
}

func TestCustomerBriefFailuresKeepPreviousBrief(t *testing.T) {
	for name, tc := range map[string]struct {
		setup func(*briefFixture)
		code  string
	}{
		"provider":  {func(f *briefFixture) { f.genErr = errors.New("throttled") }, model.AnalysisProviderFailed},
		"truncated": {func(f *briefFixture) { f.genErr = fmt.Errorf("%w: max_tokens", ErrInvalidAnalysisResponse) }, model.AnalysisInvalidOutput},
		"non-json":  {func(f *briefFixture) { f.output = "브리프입니다" }, model.AnalysisInvalidOutput},
		"unknown":   {func(f *briefFixture) { f.output = `{"situation":null,"extra":1}` }, model.AnalysisInvalidOutput},
		"trailing":  {func(f *briefFixture) { f.output = `{"situation":null} {}` }, model.AnalysisInvalidOutput},
		"revoked":   {func(f *briefFixture) { f.onModel = func() { f.repo.members = map[string]bool{} } }, "ACCESS_REVOKED"},
		"changed": {func(f *briefFixture) {
			f.onModel = func() {
				f.news = append(f.news, model.CrawledDocument{SourceID: "src", DocHash: "doc2", Title: "새 기사", CrawledAt: f.now.Add(-time.Minute).Unix()})
			}
		}, model.AnalysisSourceChanged},
		"oversized": {func(f *briefFixture) {
			label := strings.Repeat("가", 90)
			f.news = nil
			for i := 0; i < 4; i++ {
				f.news = append(f.news, model.CrawledDocument{SourceID: "src", DocHash: fmt.Sprintf("d%d", i), Title: label, CrawledAt: f.now.Add(-time.Hour).Unix()})
			}
			text := strings.Repeat("나", 200)
			refs := `["N1","N2","N3","N4"]`
			var parts []string
			for i := 0; i < 6; i++ {
				parts = append(parts, fmt.Sprintf(`{"text":"%s%d","refs":%s}`, text, i, refs))
			}
			list := strings.Join(parts, ",")
			news := strings.Repeat(fmt.Sprintf(`{"text":"%s","why":"%s","refs":%s},`, text, text, refs), 5)
			f.output = fmt.Sprintf(`{"situation":null,"sinceLast":[%s],"questions":[%s],"news":[%s],"terms":[]}`, list, list, strings.TrimSuffix(news, ","))
		}, "OUTPUT_TOO_LARGE"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newBriefFixture(t)
			if err := f.svc.Process(context.Background(), "acct", "user", f.queue(t)); err != nil {
				t.Fatal(err)
			}
			previous := f.repo.state.Sealed
			tc.setup(f)
			f.repo.members["user"] = true
			if err := f.svc.Process(context.Background(), "acct", "user", f.queue(t)); err != nil {
				t.Fatal(err)
			}
			if f.repo.state.Status != model.AnalysisFailed || f.repo.state.ErrorCode != tc.code {
				t.Fatalf("want %s: %s %s", tc.code, f.repo.state.Status, f.repo.state.ErrorCode)
			}
			if f.repo.completes != 1 || f.repo.state.Sealed != previous || previous == "" {
				t.Fatal("a failed run must keep the previous brief")
			}
		})
	}
}

func TestCustomerBriefExpiredRowReadsAsAbsent(t *testing.T) {
	f := newBriefFixture(t)
	if err := f.svc.Process(context.Background(), "acct", "user", f.queue(t)); err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(model.CustomerBriefTTL + time.Second)
	got, err := f.svc.Get(context.Background(), "user", "acct")
	if err != nil || got.Brief != nil || got.Status != model.AnalysisUnknown {
		t.Fatalf("expired row must read as absent: %v %+v", err, got)
	}
	f.queue(t)
	if !f.repo.cleared || f.repo.state.Sealed != "" {
		t.Fatal("a new run on an expired row must drop the expired brief")
	}
	f.repo.state.ExpiresAt = f.now.Unix()
	if err := f.svc.Process(context.Background(), "acct", "user", f.repo.state.RunID); err != nil || f.repo.state.Status != model.AnalysisQueued {
		t.Fatalf("an expired row is absent to the worker: %v %+v", err, f.repo.state)
	}
}
