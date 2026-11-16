package service

import (
	"context"
	"testing"

	"github.com/ttobak/backend/internal/model"
)

func TestVocabularyViewLiveName(t *testing.T) {
	ready := model.VocabularyState{Status: model.VocabStatusReady, ActiveReady: true, ActiveName: "ttobak-vocab-0123456789abcdef", TermCount: 3}
	cases := []struct {
		name  string
		state *model.VocabularyState
		live  bool
		want  string
	}{
		{"disabled never names", &ready, false, ""},
		{"ready active slot is named", &ready, true, "ttobak-vocab-0123456789abcdef"},
		{"not ready is not named", func() *model.VocabularyState { s := ready; s.ActiveReady = false; return &s }(), true, ""},
		{"missing name is not named", func() *model.VocabularyState { s := ready; s.ActiveName = ""; return &s }(), true, ""},
		{"emptied language stops advertising", func() *model.VocabularyState { s := ready; s.EmptySince = "2026-11-11T00:00:00Z"; return &s }(), true, ""},
		{"in-flight build keeps serving the READY active slot", func() *model.VocabularyState {
			s := ready
			s.Status = model.VocabStatusBuilding
			s.InactiveName = "ttobak-vocab-fedcba9876543210"
			return &s
		}(), true, "ttobak-vocab-0123456789abcdef"},
		{"no state", nil, true, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := vocabularyView(tc.state, tc.live)
			if got.LiveName != tc.want {
				t.Fatalf("LiveName = %q, want %q", got.LiveName, tc.want)
			}
		})
	}
}

func TestViewsFromUsesLiveSetting(t *testing.T) {
	ready := &model.VocabularyState{Status: model.VocabStatusReady, ActiveReady: true, ActiveName: "ttobak-vocab-0123456789abcdef"}
	svc := &DictionaryService{}
	if got := svc.viewsFrom("u1", stateRead{state: ready}, stateRead{}).Ko.LiveName; got != "" {
		t.Fatalf("default service named %q", got)
	}
	svc.SetLiveVocabulary(true)
	if got := svc.viewsFrom("u1", stateRead{state: ready}, stateRead{}).Ko.LiveName; got != "ttobak-vocab-0123456789abcdef" {
		t.Fatalf("enabled service named %q", got)
	}
}

func TestDictionaryGetServesLiveReplacementsOnlyWhenLive(t *testing.T) {
	fx := newBuilderFixture(t, "a")
	ctx := context.Background()
	if _, err := fx.svc.CreateTerm(ctx, "u1", model.CreateDictTermRequest{DisplayAs: "Claude Code", HeardAs: []string{"클로드 코드"}}); err != nil {
		t.Fatal(err)
	}
	resp, err := fx.svc.GetDictionaryWithTerms(ctx, "u1")
	if err != nil || len(resp.LiveReplacements) != 0 {
		t.Fatalf("live off must send none: %v %+v", err, resp)
	}
	fx.svc.SetLiveVocabulary(true)
	resp, err = fx.svc.GetDictionaryWithTerms(ctx, "u1")
	if err != nil || len(resp.LiveReplacements) != 1 ||
		resp.LiveReplacements[0] != (model.LiveReplacement{Heard: "클로드 코드", DisplayAs: "Claude Code"}) {
		t.Fatalf("live on: %v %+v", err, resp.LiveReplacements)
	}
}
