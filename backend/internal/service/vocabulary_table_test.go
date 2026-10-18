package service

import (
	"fmt"
	"strings"
	"testing"

	"github.com/ttobak/backend/internal/model"
)

// Synthetic fixtures only.

func TestVocabularyTSVGolden(t *testing.T) {
	row := func(display, phrase, origin, state string, hits int, added int) model.DictTerm {
		return model.DictTerm{SK: "DICTTERM#" + display, DisplayAs: display, Phrase: phrase, Origin: origin, State: state,
			Hits: hits, TranscribeKo: phrase != "", AddedAt: fmt.Sprintf("2026-10-01T00:00:00.%06dZ", added)}
	}
	m, a, on := model.TermOriginManual, model.TermOriginAuto, model.TermStateActive
	rows := []model.DictTerm{
		row("Beta Node", "Beta-Node", a, on, 1, 3), row("Alpha Grid", "Alpha Grid", m, on, 0, 1),
		row("또박 노트", "또박-노트", a, on, 3, 0), row("AlphaGrid", "alpha-grid", a, on, 9, 0), // duplicate phrase
		row("TtobakSync", "Ttobak-Sync", m, on, 0, 2), row("EKS", "E.K.S.", m, on, 0, 4),
		row("S3 Bucket", "S3-Bucket", m, on, 0, 5), row("Delta", "Delta", a, model.TermStateSuggested, 5, 6),
		row("Gone", "Gone", m, model.TermStateDeleted, 0, 7), row("No Phrase", "", m, on, 0, 8),
	}
	head := "Phrase\tSoundsLike\tIPA\tDisplayAs\n"
	for lang, want := range map[string]string{
		model.VocabLangKo: head + "Alpha-Grid\t\t\tAlpha Grid\nTtobak-Sync\t\t\tTtobakSync\n또박-노트\t\t\t또박 노트\nBeta-Node\t\t\tBeta Node\n",
		model.VocabLangEn: head + "Alpha-Grid\t\t\tAlpha Grid\nTtobak-Sync\t\t\tTtobakSync\nE.K.S.\t\t\tEKS\nBeta-Node\t\t\tBeta Node\n",
	} {
		got, n := RenderVocabularyTSV(vocabularyEntries(rows, lang))
		if got != want || n != 4 {
			t.Fatalf("%s table:\n%q\nwant\n%q", lang, got, want)
		}
	}
	if !planVocabulary(rows, model.VocabLangEn).empty || planVocabulary(rows, model.VocabLangKo).empty {
		t.Fatal("en needs five terms, ko one")
	}
	var many []vocabEntry
	for i := 0; i < VocabMaxRows+10; i++ {
		many = append(many, vocabEntry{Phrase: "Term", DisplayAs: "T"})
	}
	if _, n := RenderVocabularyTSV(many); n != VocabMaxRows {
		t.Fatalf("row cap: %d", n)
	}
	long := strings.Repeat("가", 80)
	many = many[:400]
	for i := range many {
		many[i] = vocabEntry{Phrase: long, DisplayAs: long}
	}
	if body, n := RenderVocabularyTSV(many); len(body) > VocabMaxBytes || n == 400 {
		t.Fatalf("byte cap: %d bytes %d rows", len(body), n)
	}
}

func TestValidTranscribePhrase(t *testing.T) {
	for _, tc := range []struct {
		phrase string
		ko, en bool
	}{
		{"Alpha-Grid", true, true}, {"또박-노트", true, false},
		{"E.K.S.", false, true}, // ko-KR dotted acronyms await acceptance
		{"Dynamo-D.B.", false, true}, {"O'Neil", false, true},
		{"AB.C", false, false}, {".NET", false, false}, {"-Alpha", false, false}, {"Alpha-", false, false},
		{"'Alpha", false, false}, {"Alpha'", false, false}, {"Alpha--Grid", false, false}, {"A..B", false, false},
		{"S3", false, false}, {"Alpha Grid", false, false}, {"Alpha\tGrid", false, false}, {"ㄱㄴ", false, false},
		{"", false, false}, {strings.Repeat("a", 256), true, true}, {strings.Repeat("a", 257), false, false},
	} {
		if ValidTranscribePhrase(tc.phrase, model.VocabLangKo) != tc.ko || ValidTranscribePhrase(tc.phrase, model.VocabLangEn) != tc.en {
			t.Errorf("%q: want ko=%v en=%v", tc.phrase, tc.ko, tc.en)
		}
	}
	if TranscribePhrase("  Alpha   Grid ") != "Alpha-Grid" {
		t.Fatal("space runs become one hyphen")
	}
}
