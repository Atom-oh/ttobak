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

func TestVocabularyTableValidTranscribePhrase(t *testing.T) {
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

func TestVocabularyHeardAsRows(t *testing.T) {
	m, a, on := model.TermOriginManual, model.TermOriginAuto, model.TermStateActive
	row := func(display, phrase, origin string, ko bool, added int, heard ...string) model.DictTerm {
		return model.DictTerm{SK: "DICTTERM#" + display, TermKey: NormalizeTermKey(display), DisplayAs: display, Phrase: phrase,
			Origin: origin, State: on, TranscribeKo: ko, HeardAs: heard, AddedAt: fmt.Sprintf("2026-10-01T00:00:00.%06dZ", added)}
	}
	edited := row("Plan Mode", "Plan-Mode", a, true, 6, "플랜 모드")
	edited.UserEditedAt = "2026-10-02T00:00:00Z"
	suggested := row("Draft Term", "Draft-Term", m, true, 7, "드래프트 텀")
	suggested.State = model.TermStateSuggested
	rows := []model.DictTerm{
		// A Latin alias, a one-syllable alias and an alias with a digit add no row.
		row("Claude Code", "Claude-Code", m, true, 1, "클로드 코드", "cloud code", "클", "클로드 2"),
		// The main phrase is not valid for ko-KR; the spoken form still is.
		row("S3", "S3", m, false, 2, "에스쓰리"),
		// An alias never takes another term's own phrase, in either order.
		row("Other", "Other", m, true, 3, "또박 노트", "또박노트", "클로드-코드"),
		row("또박 노트", "또박-노트", m, true, 4),
		// Automatic terms the user never touched keep their aliases out.
		row("Auto Term", "Auto-Term", a, true, 5, "오토 텀"),
		edited, suggested,
		// An empty phrase keeps the whole term out of the vocabulary, spoken forms included.
		row("Opt Out", "", m, false, 8, "옵트 아웃"),
	}
	head := "Phrase\tSoundsLike\tIPA\tDisplayAs\n"
	wantKo := head + "Claude-Code\t\t\tClaude Code\n클로드-코드\t\t\tClaude Code\n에스쓰리\t\t\tS3\nOther\t\t\tOther\n" +
		"또박-노트\t\t\t또박 노트\nAuto-Term\t\t\tAuto Term\nPlan-Mode\t\t\tPlan Mode\n플랜-모드\t\t\tPlan Mode\n"
	if got, _ := RenderVocabularyTSV(vocabularyEntries(rows, model.VocabLangKo)); got != wantKo {
		t.Fatalf("ko table:\n%q\nwant\n%q", got, wantKo)
	}
	// Spoken Korean forms are ko-KR rows only.
	if got, _ := RenderVocabularyTSV(vocabularyEntries(rows, model.VocabLangEn)); strings.ContainsAny(got, "클에플") {
		t.Fatalf("en table carries a Hangul alias:\n%q", got)
	}
	// An alias-only edit changes the table, so it must count as a vocabulary change.
	if !contributesVocabulary(rows[1]) || contributesVocabulary(suggested) || contributesVocabulary(row("X", "", m, false, 9, "엑스 텀")) {
		t.Fatal("contributesVocabulary: an active term with a spoken form contributes; a suggestion or an empty term does not")
	}
}

func TestLiveReplacements(t *testing.T) {
	m, on := model.TermOriginManual, model.TermStateActive
	row := func(display, phrase string, ko bool, added int, heard ...string) model.DictTerm {
		return model.DictTerm{SK: "DICTTERM#" + display, TermKey: NormalizeTermKey(display), DisplayAs: display, Phrase: phrase,
			Origin: m, State: on, TranscribeKo: ko, HeardAs: heard, AddedAt: fmt.Sprintf("2026-10-01T00:00:00.%06dZ", added)}
	}
	layers := []vocabLayer{
		{name: "personal", rows: []model.DictTerm{
			row("Claude Code", "Claude-Code", true, 1, "클로드 코드"),
			row("S3", "S3", false, 2, "에스쓰리"),
			// A term that is itself Hangul needs no replacement.
			row("또박 노트", "또박-노트", true, 3),
		}},
		// A later layer cannot retarget a spoken form an earlier layer claimed.
		{name: "common", rows: []model.DictTerm{row("Cloud Code", "Cloud-Code", true, 4, "클로드 코드", "클라우드 코드")}},
	}
	got := liveReplacements(layers)
	want := []model.LiveReplacement{
		{Heard: "클로드 코드", DisplayAs: "Claude Code"}, {Heard: "에스쓰리", DisplayAs: "S3"}, {Heard: "클라우드 코드", DisplayAs: "Cloud Code"},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
	var many []model.DictTerm
	for i := 0; i < LiveReplacementsMax+20; i++ {
		// Distinct two-syllable spoken forms.
		heard := string([]rune{rune(0xAC00 + i), rune(0xAC00 + i)})
		many = append(many, row(fmt.Sprintf("Term%c%c", 'a'+i/26%26, 'a'+i%26)+strings.Repeat("x", i/676), "Digit-9", false, i, heard))
	}
	if n := len(liveReplacements([]vocabLayer{{rows: many}})); n != LiveReplacementsMax {
		t.Fatalf("cap: %d", n)
	}
}
