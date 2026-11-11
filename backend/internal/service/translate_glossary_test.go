package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func glossaryTerms() []GlossaryTerm {
	return []GlossaryTerm{
		{DisplayAs: "SageMaker", HeardAs: []string{"세이지메이커", "세이지 메이커"}, TermKey: "sagemaker"},
		{DisplayAs: "EKS", TermKey: "eks"},
		{DisplayAs: "여신심사", TermKey: "여신심사"},
		{DisplayAs: "Bedrock", HeardAs: []string{"베드락"}, TermKey: "bedrock"},
		{DisplayAs: "A", TermKey: "a"},
	}
}

func TestSelectTranslateGlossary(t *testing.T) {
	cases := []struct {
		name string
		text string
		want []TranslateGlossaryEntry
	}{
		{"empty text", "", nil},
		{"no term", "오늘 회의를 시작합니다", nil},
		{"latin term with a Korean particle", "SageMaker를 검토합니다", []TranslateGlossaryEntry{{Term: "SageMaker"}}},
		{"case-insensitive latin", "we use sagemaker today", []TranslateGlossaryEntry{{Term: "SageMaker"}}},
		{"heardAs maps to the display form", "세이지메이커 비용과 베드락", []TranslateGlossaryEntry{
			{Term: "SageMaker", Heard: []string{"세이지메이커"}}, {Term: "Bedrock", Heard: []string{"베드락"}}}},
		{"latin needs word boundaries", "GEEKS and WEEKS", nil},
		{"latin acronym on a boundary", "EKS 클러스터", []TranslateGlossaryEntry{{Term: "EKS"}}},
		{"hangul substring with particle", "여신심사를 자동화", []TranslateGlossaryEntry{{Term: "여신심사"}}},
		{"one-rune forms never match", "A plan", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SelectTranslateGlossary(glossaryTerms(), tc.text)
			gotJSON, _ := json.Marshal(got)
			wantJSON, _ := json.Marshal(tc.want)
			if string(gotJSON) != string(wantJSON) {
				t.Fatalf("got %s, want %s", gotJSON, wantJSON)
			}
		})
	}
}

func TestSelectTranslateGlossaryBounds(t *testing.T) {
	var terms []GlossaryTerm
	var text strings.Builder
	for i := 0; i < translateGlossaryMaxEntries+5; i++ {
		word := "Termx" + string(rune('a'+i))
		terms = append(terms, GlossaryTerm{DisplayAs: word, TermKey: word})
		text.WriteString(word + " ")
	}
	terms = append(terms, GlossaryTerm{DisplayAs: "bad\nterm", TermKey: "bad"})
	got := SelectTranslateGlossary(terms, text.String()+" bad\nterm")
	if len(got) != translateGlossaryMaxEntries {
		t.Fatalf("got %d entries, want %d", len(got), translateGlossaryMaxEntries)
	}
	for _, entry := range got {
		if strings.ContainsRune(entry.Term, '\n') {
			t.Fatalf("unsafe term %q selected", entry.Term)
		}
	}
}

type fakeGlossarySource struct {
	calls int
	terms []GlossaryTerm
	err   error
	owner string
	acct  string
}

func (f *fakeGlossarySource) GlossaryForMeeting(_ context.Context, ownerID, accountID string) ([]GlossaryTerm, error) {
	f.calls++
	f.owner, f.acct = ownerID, accountID
	return f.terms, f.err
}

func TestTranslateGlossaryCache(t *testing.T) {
	src := &fakeGlossarySource{terms: glossaryTerms()}
	svc := &TranslateService{}
	svc.SetGlossarySource(src)
	now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	svc.glossary.now = func() time.Time { return now }
	ctx := context.Background()
	if got := svc.glossary.terms(ctx, "u1"); len(got) != len(glossaryTerms()) || src.owner != "u1" || src.acct != "" {
		t.Fatalf("first read: %d terms owner=%q account=%q", len(got), src.owner, src.acct)
	}
	svc.glossary.terms(ctx, "u1")
	if src.calls != 1 {
		t.Fatalf("cached read called source %d times", src.calls)
	}
	now = now.Add(translateGlossaryTTL)
	svc.glossary.terms(ctx, "u1")
	if src.calls != 2 {
		t.Fatalf("expired entry was not re-read: %d calls", src.calls)
	}
	failing := &fakeGlossarySource{err: errors.New("boom")}
	svc.SetGlossarySource(failing)
	svc.glossary.now = func() time.Time { return now }
	if got := svc.glossary.terms(ctx, "u1"); got != nil {
		t.Fatalf("a failed read must yield no glossary, got %v", got)
	}
	svc.glossary.terms(ctx, "u1")
	if failing.calls != 2 {
		t.Fatalf("a failed read must not be cached: %d calls", failing.calls)
	}
}

func TestInterpreterRequestGlossaryRule(t *testing.T) {
	base := TranslateRequest{Text: "세이지메이커 비용", SourceLang: "ko", TargetLang: "en"}
	plain, err := buildInterpreterRequest(base)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(plain), "glossary") {
		t.Fatalf("a request without glossary must keep the base prompt: %s", plain)
	}
	withGlossary := base
	withGlossary.Glossary = SelectTranslateGlossary(glossaryTerms(), base.Text)
	body, err := buildInterpreterRequest(withGlossary)
	if err != nil {
		t.Fatal(err)
	}
	var decoded interpreterRequest
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(decoded.Messages[0].Content, interpreterGlossaryRule) {
		t.Fatalf("system prompt lacks the glossary rule: %q", decoded.Messages[0].Content)
	}
	var input interpreterInput
	if err := json.Unmarshal([]byte(decoded.Messages[1].Content), &input); err != nil {
		t.Fatal(err)
	}
	if len(input.Glossary) != 1 || input.Glossary[0].Term != "SageMaker" || input.Glossary[0].Heard[0] != "세이지메이커" {
		t.Fatalf("glossary input = %+v", input.Glossary)
	}
}

func TestTranslateSegmentFillsGlossaryOnlyForHigh(t *testing.T) {
	src := &fakeGlossarySource{terms: glossaryTerms()}
	inv := &fakeModelInvoker{body: chatResponse("Review the SageMaker cost", "stop")}
	svc := newTestTranslateService(&fakeTextTranslator{out: "SageMaker cost"}, inv, lunaID)
	svc.SetGlossarySource(src)
	ctx := context.Background()
	if _, err := svc.TranslateSegment(ctx, TranslateRequest{Text: "세이지메이커 비용", SourceLang: "ko", TargetLang: "en", Quality: TranslateQualityFast, OwnerID: "u1"}); err != nil {
		t.Fatal(err)
	}
	if src.calls != 0 {
		t.Fatalf("fast quality read the glossary %d times", src.calls)
	}
	result, err := svc.TranslateSegment(ctx, TranslateRequest{Text: "세이지메이커 비용", SourceLang: "ko", TargetLang: "en", Quality: TranslateQualityHigh, OwnerID: "u1"})
	if err != nil || result.Engine != TranslateEngineLLM {
		t.Fatalf("result %+v err %v", result, err)
	}
	if !strings.Contains(string(inv.input.Body), `\"glossary\":[{\"term\":\"SageMaker\",\"heard_as\":[\"세이지메이커\"]}]`) {
		t.Fatalf("interpreter body lacks glossary: %s", inv.input.Body)
	}
	if _, err := svc.TranslateSegment(ctx, TranslateRequest{Text: "세이지메이커 비용", SourceLang: "ko", TargetLang: "en", Quality: TranslateQualityHigh}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(inv.input.Body), "glossary") {
		t.Fatalf("a request without an owner must carry no glossary: %s", inv.input.Body)
	}
}
