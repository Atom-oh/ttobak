package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	ebtypes "github.com/aws/aws-sdk-go-v2/service/eventbridge/types"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

func TestCustomerBriefPublisherSendsOnlyIdentifiers(t *testing.T) {
	client := &actionEventStub{out: &eventbridge.PutEventsOutput{Entries: []ebtypes.PutEventsResultEntry{{EventId: aws.String("event")}}}}
	event := model.CustomerBriefRequested{AccountID: "acct", UserID: "user", RunID: "run"}
	if err := CustomerBriefPublisher(client)(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	entry := client.in.Entries[0]
	if len(client.in.Entries) != 1 || aws.ToString(entry.EventBusName) != "default" || aws.ToString(entry.Source) != "ttobak.analysis" ||
		aws.ToString(entry.DetailType) != "CustomerBriefRequested" || aws.ToString(entry.Detail) != `{"accountId":"acct","userId":"user","runId":"run"}` {
		t.Fatalf("wrong routing or detail: %+v", entry)
	}
	client.out.FailedEntryCount = 1
	if err := CustomerBriefPublisher(client)(context.Background(), event); !errors.Is(err, ErrCustomerBriefPublish) {
		t.Fatalf("failed entry: %v", err)
	}
	if err := CustomerBriefPublisher(nil)(context.Background(), event); !errors.Is(err, ErrCustomerBriefPublish) {
		t.Fatalf("nil client: %v", err)
	}
}

func TestCustomerBriefRequestPublishesOneIdentifierEvent(t *testing.T) {
	f := newBriefFixture(t)
	res, err := f.svc.Request(context.Background(), "user", "acct")
	if err != nil {
		t.Fatal(err)
	}
	if len(f.published) != 1 || f.published[0] != (model.CustomerBriefRequested{AccountID: "acct", UserID: "user", RunID: res.Run.RunID}) {
		t.Fatalf("published: %+v", f.published)
	}
	if res.Run.Status != model.AnalysisQueued || res.Run.RequestedAt == nil || !res.Run.RequestedAt.Equal(f.now) || res.Run.ErrorCode != "" || res.Brief != nil {
		t.Fatalf("response: %+v", res)
	}
	again, err := f.svc.Request(context.Background(), "user", "acct")
	if err != nil || again.Run.RunID != res.Run.RunID || len(f.published) != 1 {
		t.Fatalf("a live run must be returned without a second event: %v %+v %d", err, again, len(f.published))
	}
}

func TestCustomerBriefPublishFailureIsVisibleAndRetryable(t *testing.T) {
	f := newBriefFixture(t)
	publish := f.svc.publish
	f.svc.publish = func(context.Context, model.CustomerBriefRequested) error { return errors.New("down") }
	if _, err := f.svc.Request(context.Background(), "user", "acct"); !errors.Is(err, ErrCustomerBriefPublish) {
		t.Fatal(err)
	}
	got, err := f.svc.Get(context.Background(), "user", "acct")
	if err != nil || got.Run.Status != model.AnalysisFailed || got.Run.ErrorCode != model.AnalysisPublishFailed {
		t.Fatalf("an undelivered run must not stay queued: %v %+v", err, got)
	}
	f.svc.publish = publish
	if run := f.queue(t); run == "" || len(f.published) != 1 || f.repo.state.Status != model.AnalysisQueued {
		t.Fatalf("a failed delivery must be retryable: %+v", f.repo.state)
	}
}

func TestCustomerBriefGetStates(t *testing.T) {
	sealed := `{"nextMeeting":null,"situation":{"text":"상황","sources":[]},"sinceLast":[],"news":[],"questions":[],"terms":[]}`
	for name, tc := range map[string]struct {
		state             func(now time.Time) *model.CustomerBriefState
		status, errorCode string
		brief             bool
	}{
		"none": {func(time.Time) *model.CustomerBriefState { return nil }, "none", "", false},
		"queued": {func(now time.Time) *model.CustomerBriefState {
			return &model.CustomerBriefState{Status: model.AnalysisQueued, LeaseUntil: now.Add(time.Minute).UnixMilli(), ExpiresAt: now.Add(time.Hour).Unix()}
		}, model.AnalysisQueued, "", false},
		"running-with-brief": {func(now time.Time) *model.CustomerBriefState {
			return &model.CustomerBriefState{Status: model.AnalysisRunning, LeaseUntil: now.Add(time.Minute).UnixMilli(), Sealed: sealed,
				BriefExpiresAt: now.Add(time.Hour).Unix(), ExpiresAt: now.Add(time.Hour).Unix()}
		}, model.AnalysisRunning, "", true},
		"interrupted": {func(now time.Time) *model.CustomerBriefState {
			return &model.CustomerBriefState{Status: model.AnalysisRunning, LeaseUntil: now.Add(-time.Second).UnixMilli(), ExpiresAt: now.Add(time.Hour).Unix()}
		}, model.AnalysisFailed, model.AnalysisInterrupted, false},
		"succeeded": {func(now time.Time) *model.CustomerBriefState {
			return &model.CustomerBriefState{Status: model.AnalysisSucceeded, Sealed: sealed, GeneratedAt: now, BriefExpiresAt: now.Add(time.Hour).Unix(), ExpiresAt: now.Add(time.Hour).Unix()}
		}, model.AnalysisSucceeded, "", true},
		"failed-keeps-brief": {func(now time.Time) *model.CustomerBriefState {
			return &model.CustomerBriefState{Status: model.AnalysisFailed, ErrorCode: model.AnalysisSourceChanged, Sealed: sealed,
				BriefExpiresAt: now.Add(time.Hour).Unix(), ExpiresAt: now.Add(time.Hour).Unix()}
		}, model.AnalysisFailed, model.AnalysisSourceChanged, true},
		"brief-expired": {func(now time.Time) *model.CustomerBriefState {
			return &model.CustomerBriefState{Status: model.AnalysisFailed, ErrorCode: "TIMEOUT", Sealed: sealed,
				BriefExpiresAt: now.Unix(), ExpiresAt: now.Add(time.Hour).Unix()}
		}, model.AnalysisFailed, "TIMEOUT", false},
		"row-expired": {func(now time.Time) *model.CustomerBriefState {
			return &model.CustomerBriefState{Status: model.AnalysisSucceeded, Sealed: sealed, BriefExpiresAt: now.Add(time.Hour).Unix(), ExpiresAt: now.Unix()}
		}, "none", "", false},
		"legacy-row-ttl-is-brief-expiry": {func(now time.Time) *model.CustomerBriefState {
			return &model.CustomerBriefState{Status: model.AnalysisSucceeded, Sealed: sealed, ExpiresAt: now.Add(time.Hour).Unix()}
		}, model.AnalysisSucceeded, "", true},
	} {
		t.Run(name, func(t *testing.T) {
			f := newBriefFixture(t)
			if state := tc.state(f.now); state != nil {
				state.AccountID, state.UserID, state.RunID = "acct", "user", "run"
				f.repo.state = state
			}
			got, err := f.svc.Get(context.Background(), "user", "acct")
			if err != nil || got.Run.Status != tc.status || got.Run.ErrorCode != tc.errorCode || (got.Brief != nil) != tc.brief {
				t.Fatalf("%v %+v", err, got)
			}
			if tc.brief && (got.Brief.Content == nil || got.Brief.Content.Situation == nil) {
				t.Fatalf("content: %+v", got.Brief)
			}
		})
	}
}

func TestCustomerBriefErrorCodeAllowlist(t *testing.T) {
	now := time.Now()
	for stored, want := range map[string]string{
		"SOURCE_CHANGED": "SOURCE_CHANGED", "SOURCE_UNAVAILABLE": "SOURCE_UNAVAILABLE", "INVALID_OUTPUT": "INVALID_OUTPUT",
		"ACCESS_REVOKED": "ACCESS_REVOKED", "INTERRUPTED": "INTERRUPTED", "PUBLISH_FAILED": "PUBLISH_FAILED",
		"": "FAILED", "secret text from somewhere": "FAILED", "source_changed": "FAILED",
	} {
		run := customerBriefRun(&model.CustomerBriefState{RunID: "r", Status: model.AnalysisFailed, ErrorCode: stored}, now)
		if run.Status != model.AnalysisFailed || run.ErrorCode != want {
			t.Errorf("%q: %+v", stored, run)
		}
	}
	if run := customerBriefRun(&model.CustomerBriefState{Status: "weird", ErrorCode: "X"}, now); run.Status != model.AnalysisFailed || run.ErrorCode != "FAILED" {
		t.Fatalf("unknown status: %+v", run)
	}
	if run := customerBriefRun(&model.CustomerBriefState{Status: model.AnalysisSucceeded, ErrorCode: "STALE"}, now); run.ErrorCode != "" {
		t.Fatalf("succeeded carries no code: %+v", run)
	}
}

// An expiring brief must not stay readable through failed refreshes, even
// though the queued row's TTL is floored past the brief's expiry.
func TestCustomerBriefRefreshNeverExtendsBriefExpiry(t *testing.T) {
	for name, legacy := range map[string]bool{"current": false, "legacy": true} {
		t.Run(name, func(t *testing.T) {
			f := newBriefFixture(t)
			if err := f.svc.Process(context.Background(), "acct", "user", f.queue(t)); err != nil {
				t.Fatal(err)
			}
			expiry := f.repo.state.ExpiresAt
			if legacy {
				f.repo.state.BriefExpiresAt = 0
			} else if f.repo.state.BriefExpiresAt != expiry {
				t.Fatalf("completion must store briefExpiresAt: %+v", f.repo.state)
			}
			f.now = time.Unix(expiry, 0).Add(-time.Minute).UTC()
			f.genErr = errors.New("throttled")
			if err := f.svc.Process(context.Background(), "acct", "user", f.queue(t)); err != nil {
				t.Fatal(err)
			}
			if f.repo.state.BriefExpiresAt != expiry || f.repo.state.ExpiresAt < f.now.Add(customerBriefRunFloor).Unix() || f.repo.state.Sealed == "" {
				t.Fatalf("queue must pin the brief expiry and floor only the row TTL: %+v", f.repo.state)
			}
			if got, err := f.svc.Get(context.Background(), "user", "acct"); err != nil || got.Brief == nil {
				t.Fatalf("brief readable before its expiry: %v %+v", err, got)
			}
			f.now = time.Unix(expiry, 0).UTC()
			got, err := f.svc.Get(context.Background(), "user", "acct")
			if err != nil || got.Brief != nil || got.Run.Status != model.AnalysisFailed || got.Run.ErrorCode != model.AnalysisProviderFailed {
				t.Fatalf("brief must not outlive its own expiry: %v %+v", err, got)
			}
			f.genErr = nil
			f.queue(t)
			if !f.repo.cleared || f.repo.state.Sealed != "" || f.repo.state.BriefExpiresAt != 0 {
				t.Fatalf("a run queued over an expired brief clears it: %+v", f.repo.state)
			}
		})
	}
}

func TestCustomerBriefResponseStaysWithinBudget(t *testing.T) {
	f := newBriefFixture(t)
	source := model.CustomerBriefSource{Kind: "news", ID: "src:" + strings.Repeat("f", 64), Label: "기사"}
	brief := model.CustomerBrief{Situation: &model.CustomerBriefStatement{Sources: []model.CustomerBriefSource{source}}}
	brief.Situation.Text = strings.Repeat("<가&", (model.CustomerBriefMaxBytes-600)/15) // 15 escaped bytes each
	body, err := json.Marshal(brief)
	if err != nil || len(body) > model.CustomerBriefMaxBytes || len(body) < model.CustomerBriefMaxBytes-1000 {
		t.Fatalf("fixture size %d: %v", len(body), err)
	}
	f.repo.state = &model.CustomerBriefState{AccountID: "acct", UserID: "user", RunID: "6f1c7a52-8f0e-4c55-b3c2-1f2e3d4c5b6a", Status: model.AnalysisFailed,
		ErrorCode: "SOURCE_UNAVAILABLE", RequestedAt: f.now, GeneratedAt: f.now, Sealed: string(body),
		BriefExpiresAt: f.now.Add(time.Hour).Unix(), ExpiresAt: f.now.Add(time.Hour).Unix()}
	got, err := f.svc.Get(context.Background(), "user", "acct")
	if err != nil || got.Brief == nil {
		t.Fatal(err)
	}
	var encoded bytes.Buffer
	if err := json.NewEncoder(&encoded).Encode(got); err != nil {
		t.Fatal(err)
	}
	if encoded.Len() > model.CustomerBriefResponseMaxBytes {
		t.Fatalf("response %d bytes exceeds %d", encoded.Len(), model.CustomerBriefResponseMaxBytes)
	}
	f.repo.state.Sealed = `{"situation":{"text":"` + strings.Repeat("a", model.CustomerBriefMaxBytes) + `","sources":[]}}`
	if _, err := f.svc.Get(context.Background(), "user", "acct"); !errors.Is(err, repository.ErrCustomerBriefUnreadable) {
		t.Fatalf("oversized stored content: %v", err)
	}
}

func TestEffectiveTermKeysReadsLegacyBlobWithoutMigration(t *testing.T) {
	active := model.DictTerm{TermKey: TermKey("EKS"), State: model.TermStateActive, Origin: model.TermOriginManual}
	tombstone := model.DictTerm{TermKey: TermKey("Kafka"), State: "deleted"}
	pristine := model.DictTerm{TermKey: TermKey("MSA"), State: model.TermStateActive, Origin: model.TermOriginManual, LegacyMigrated: true}
	rows := []model.DictTerm{active, tombstone, pristine}
	blob := &model.UserDictionary{Terms: []model.DictionaryTerm{{Phrase: "Kafka"}, {Phrase: "카프카", DisplayAs: "Redis"}}}
	for name, tc := range map[string]struct {
		dict *model.UserDictionary
		want []string
	}{
		"no blob":       {nil, []string{TermKey("EKS"), TermKey("MSA")}},
		"migrated blob": {&model.UserDictionary{MigratedAt: "2026-01-01", Terms: blob.Terms}, []string{TermKey("EKS"), TermKey("MSA")}},
		// Before migration the blob is authoritative over pristine copies; a
		// tombstone hides its blob term.
		"unmigrated blob": {blob, []string{TermKey("EKS"), TermKey("Redis")}},
	} {
		got := effectiveTermKeys(tc.dict, rows)
		sort.Strings(got)
		sort.Strings(tc.want)
		if strings.Join(got, ",") != strings.Join(tc.want, ",") {
			t.Errorf("%s: got %v want %v", name, got, tc.want)
		}
	}
}
