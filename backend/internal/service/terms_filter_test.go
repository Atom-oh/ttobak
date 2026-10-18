package service

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/ttobak/backend/internal/model"
)

// All fixtures are synthetic; none come from a real meeting.
var filterSources = TermSources{
	Transcript: "오늘은 다이나모 디비 테이블을 봅니다. DynamoDB 파티션 키는 고객 ID로 하고요, " +
		"다시 DynamoDB 인덱스 얘기를 하면 쿠버네티스 클러스터는 EKS 로 갑니다. " +
		"쿠버네티스를 이번 분기에 올리고 파게이트 비용은 두 weeks 뒤에 봅니다, weeks 단위로요.",
	Notes:     "DynamoDB 설계 검토. Fargate 비용 확인. Kinesis 도입 보류.",
	Documents: []TermSourceDocumentText{{ID: "doc-1", Text: "Amazon Bedrock 사용 가이드"}},
}

func transcriptEvidence(q string) []TermEvidence {
	return []TermEvidence{{Source: TermSourceTranscript, Quote: q}}
}

func TestFilterCandidatesRules(t *testing.T) {
	tests := []struct {
		name      string
		cand      TermCandidate
		wantKept  bool
		wantCount TermFilterCounts
		check     func(t *testing.T, got FilteredTerm)
	}{
		{
			name: "grounded Latin term with alias",
			cand: TermCandidate{DisplayAs: "DynamoDB", HeardAs: []string{"다이나모 디비"}, Confidence: 0.9,
				Evidence: transcriptEvidence("DynamoDB 파티션 키는")},
			wantKept: true,
			check: func(t *testing.T, got FilteredTerm) {
				if !reflect.DeepEqual(got.SourceTypes, []string{TermSourceTranscript, TermSourceNotes}) {
					t.Fatalf("sourceTypes = %v", got.SourceTypes)
				}
				if got.Phrase != "Dynamo-D.B." || got.TranscribeKo || !got.TranscribeEn {
					t.Fatalf("phrase = %q ko=%v en=%v", got.Phrase, got.TranscribeKo, got.TranscribeEn)
				}
				if got.TermKey != TermKey("DynamoDB") || !reflect.DeepEqual(got.HeardAs, []string{"다이나모 디비"}) {
					t.Fatalf("got %+v", got)
				}
			},
		},
		{
			name:      "below confidence",
			cand:      TermCandidate{DisplayAs: "DynamoDB", Confidence: 0.5, Evidence: transcriptEvidence("DynamoDB")},
			wantCount: TermFilterCounts{Confidence: 1},
		},
		{
			name:      "NaN confidence",
			cand:      TermCandidate{DisplayAs: "DynamoDB", Confidence: math.NaN(), Evidence: transcriptEvidence("DynamoDB")},
			wantCount: TermFilterCounts{Confidence: 1},
		},
		{
			name:      "confidence above one",
			cand:      TermCandidate{DisplayAs: "DynamoDB", Confidence: 1.5, Evidence: transcriptEvidence("DynamoDB")},
			wantCount: TermFilterCounts{Confidence: 1},
		},
		{
			name:      "blocklisted display",
			cand:      TermCandidate{DisplayAs: "Meeting", Confidence: 0.95, Evidence: transcriptEvidence("DynamoDB")},
			wantCount: TermFilterCounts{Blocklist: 1},
		},
		{
			name:      "blocklisted alias",
			cand:      TermCandidate{DisplayAs: "DynamoDB", HeardAs: []string{"그러니까"}, Confidence: 0.95, Evidence: transcriptEvidence("DynamoDB")},
			wantCount: TermFilterCounts{Blocklist: 1},
		},
		{
			name:      "date shape",
			cand:      TermCandidate{DisplayAs: "2026-10-18", Confidence: 0.95, Evidence: transcriptEvidence("DynamoDB")},
			wantCount: TermFilterCounts{Shape: 1},
		},
		{
			name:      "shared validator rejects tab",
			cand:      TermCandidate{DisplayAs: "Dynamo\tDB", Confidence: 0.95, Evidence: transcriptEvidence("DynamoDB")},
			wantCount: TermFilterCounts{Shape: 1},
		},
		{
			name:      "alias equal to own key",
			cand:      TermCandidate{DisplayAs: "DynamoDB", HeardAs: []string{"dynamo db"}, Confidence: 0.95, Evidence: transcriptEvidence("DynamoDB")},
			wantCount: TermFilterCounts{Shape: 1},
		},
		{
			name:      "quote not in named source",
			cand:      TermCandidate{DisplayAs: "DynamoDB", Confidence: 0.95, Evidence: transcriptEvidence("DynamoDB 는 버립니다")},
			wantCount: TermFilterCounts{Grounding: 1},
		},
		{
			name:      "quote names another source",
			cand:      TermCandidate{DisplayAs: "DynamoDB", Confidence: 0.95, Evidence: []TermEvidence{{Source: TermSourceNotes, Quote: "DynamoDB 파티션 키는"}}},
			wantCount: TermFilterCounts{Grounding: 1},
		},
		{
			name:      "quote omits the term",
			cand:      TermCandidate{DisplayAs: "DynamoDB", Confidence: 0.95, Evidence: transcriptEvidence("테이블을 봅니다")},
			wantCount: TermFilterCounts{Grounding: 1},
		},
		{
			name:      "no evidence",
			cand:      TermCandidate{DisplayAs: "DynamoDB", Confidence: 0.95},
			wantCount: TermFilterCounts{Grounding: 1},
		},
		{
			name:      "unknown evidence source",
			cand:      TermCandidate{DisplayAs: "DynamoDB", Confidence: 0.95, Evidence: []TermEvidence{{Source: "caption", Quote: "DynamoDB"}}},
			wantCount: TermFilterCounts{Grounding: 1},
		},
		{
			name:      "alias not in transcript",
			cand:      TermCandidate{DisplayAs: "DynamoDB", HeardAs: []string{"다이너모"}, Confidence: 0.95, Evidence: transcriptEvidence("DynamoDB")},
			wantCount: TermFilterCounts{Grounding: 1},
		},
		{
			name:      "once in transcript only; weeks is not EKS",
			cand:      TermCandidate{DisplayAs: "EKS", Confidence: 0.95, Evidence: transcriptEvidence("클러스터는 EKS 로")},
			wantCount: TermFilterCounts{Grounding: 1},
		},
		{
			name:     "Hangul term twice in transcript with particle",
			cand:     TermCandidate{DisplayAs: "쿠버네티스", Confidence: 0.9, Evidence: transcriptEvidence("쿠버네티스 클러스터는")},
			wantKept: true,
			check: func(t *testing.T, got FilteredTerm) {
				if got.Phrase != "쿠버네티스" || !got.TranscribeKo || got.TranscribeEn {
					t.Fatalf("phrase = %q ko=%v en=%v", got.Phrase, got.TranscribeKo, got.TranscribeEn)
				}
			},
		},
		{
			name:      "notes-only term absent from transcript",
			cand:      TermCandidate{DisplayAs: "Kinesis", Confidence: 0.95, Evidence: []TermEvidence{{Source: TermSourceNotes, Quote: "Kinesis 도입"}}},
			wantCount: TermFilterCounts{Grounding: 1},
		},
		{
			name: "notes term heard in transcript",
			cand: TermCandidate{DisplayAs: "Fargate", HeardAs: []string{"파게이트"}, Confidence: 0.9,
				Evidence: []TermEvidence{{Source: TermSourceNotes, Quote: "Fargate 비용"}}},
			wantKept: true,
			check: func(t *testing.T, got FilteredTerm) {
				if !reflect.DeepEqual(got.SourceTypes, []string{TermSourceTranscript, TermSourceNotes}) {
					t.Fatalf("sourceTypes = %v", got.SourceTypes)
				}
			},
		},
		{
			name: "document term",
			cand: TermCandidate{DisplayAs: "Amazon Bedrock", Confidence: 0.9,
				Evidence: []TermEvidence{{Source: TermSourceDocument, DocumentID: "doc-1", Quote: "amazon  bedrock 사용"}}},
			wantKept: true,
			check: func(t *testing.T, got FilteredTerm) {
				if !reflect.DeepEqual(got.SourceTypes, []string{TermSourceDocument}) || got.Phrase != "Amazon-Bedrock" || !got.TranscribeKo || !got.TranscribeEn {
					t.Fatalf("got %+v", got)
				}
			},
		},
		{
			name: "document evidence with unknown id",
			cand: TermCandidate{DisplayAs: "Amazon Bedrock", Confidence: 0.9,
				Evidence: []TermEvidence{{Source: TermSourceDocument, DocumentID: "doc-2", Quote: "Amazon Bedrock"}}},
			wantCount: TermFilterCounts{Grounding: 1},
		},
		{
			name: "model phrase spelling another term is ignored",
			cand: TermCandidate{DisplayAs: "DynamoDB", Phrase: "Cassandra", Confidence: 0.9,
				Evidence: transcriptEvidence("DynamoDB 인덱스")},
			wantKept: true,
			check: func(t *testing.T, got FilteredTerm) {
				if got.Phrase != "Dynamo-D.B." {
					t.Fatalf("phrase = %q", got.Phrase)
				}
			},
		},
		{
			name: "valid model phrase is used",
			cand: TermCandidate{DisplayAs: "DynamoDB", Phrase: "Dynamo-DB", Confidence: 0.9,
				Evidence: transcriptEvidence("DynamoDB 인덱스")},
			wantKept: true,
			check: func(t *testing.T, got FilteredTerm) {
				if got.Phrase != "Dynamo-DB" || !got.TranscribeKo || !got.TranscribeEn {
					t.Fatalf("got %+v", got)
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, counts := FilterCandidates([]TermCandidate{tc.cand}, filterSources, 0.85)
			want := tc.wantCount
			want.Input = 1
			if tc.wantKept {
				want.Kept = 1
			}
			if counts != want {
				t.Fatalf("counts = %+v, want %+v", counts, want)
			}
			if len(got) != want.Kept {
				t.Fatalf("kept %d terms, want %d", len(got), want.Kept)
			}
			if tc.check != nil {
				tc.check(t, got[0])
			}
		})
	}
}

func TestFilterCandidatesBoundsJudgedInSource(t *testing.T) {
	sources := TermSources{
		Transcript: "EKS 클러스터를 씁니다. EKS 노드는 두 weeks 뒤에 늘립니다. 쿠버네티스 버전은 쿠버네티스를 따릅니다.",
	}
	tests := []struct {
		name     string
		cand     TermCandidate
		wantKept bool
	}{
		{"bounded quote names EKS", TermCandidate{DisplayAs: "EKS", Confidence: 0.9, Evidence: transcriptEvidence("EKS 노드는")}, true},
		{"quote cut from weeks", TermCandidate{DisplayAs: "EKS", Confidence: 0.9, Evidence: transcriptEvidence("eks 뒤에")}, false},
		{"Hangul term with particle", TermCandidate{DisplayAs: "쿠버네티스", Confidence: 0.9, Evidence: transcriptEvidence("쿠버네티스를")}, true},
		{"Hangul fragment inside a word", TermCandidate{DisplayAs: "버네티스", Confidence: 0.9, Evidence: transcriptEvidence("버네티스 버전은")}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, counts := FilterCandidates([]TermCandidate{tc.cand}, sources, 0.85)
			if (len(got) == 1) != tc.wantKept {
				t.Fatalf("kept %d, want kept=%v; counts %+v", len(got), tc.wantKept, counts)
			}
			if !tc.wantKept && counts.Grounding != 1 {
				t.Fatalf("counts = %+v, want a grounding drop", counts)
			}
		})
	}
}

func TestMatchTextHangulLeftBound(t *testing.T) {
	src := newMatchText("쿠버네티스를 쓰고 쿠버네티스 클러스터")
	if got := src.count(newMatchText("버네티스"), true); got != 0 {
		t.Fatalf("fragment count = %d, want 0", got)
	}
	if got := src.count(newMatchText("쿠버네티스"), true); got != 2 {
		t.Fatalf("term count = %d, want 2", got)
	}
	if got := newMatchText("쿠버네티스EKS").count(newMatchText("EKS"), true); got != 1 {
		t.Fatalf("Latin after Hangul count = %d, want 1", got)
	}
}

func TestFilterCandidatesDefaultConfidence(t *testing.T) {
	cands := []TermCandidate{
		{DisplayAs: "DynamoDB", Confidence: 0.86, Evidence: transcriptEvidence("DynamoDB")},
		{DisplayAs: "쿠버네티스", Confidence: 0.84, Evidence: transcriptEvidence("쿠버네티스")},
	}
	for _, min := range []float64{0, -1, 2, math.NaN()} {
		got, counts := FilterCandidates(cands, filterSources, min)
		if len(got) != 1 || got[0].DisplayAs != "DynamoDB" || counts.Confidence != 1 {
			t.Fatalf("min %v: got %d terms, counts %+v", min, len(got), counts)
		}
	}
}

func TestFilterCandidatesDuplicateKeepsBest(t *testing.T) {
	cands := []TermCandidate{
		{DisplayAs: "Dynamo DB", Confidence: 0.88, Evidence: transcriptEvidence("DynamoDB")},
		{DisplayAs: "DynamoDB", Confidence: 0.93, Evidence: transcriptEvidence("DynamoDB")},
	}
	got, counts := FilterCandidates(cands, filterSources, 0.85)
	if len(got) != 1 || got[0].DisplayAs != "DynamoDB" || counts.Duplicate != 1 || counts.Kept != 1 {
		t.Fatalf("got %+v counts %+v", got, counts)
	}
}

func TestFilterCandidatesCapAndRanking(t *testing.T) {
	var words []string
	var transcript strings.Builder
	for i := 0; i < 17; i++ {
		w := "Zorb" + strings.Repeat(string(rune('a'+i)), 3)
		words = append(words, w)
		fmt.Fprintf(&transcript, "%s 그리고 %s. ", w, w)
	}
	sources := TermSources{Transcript: transcript.String(), Notes: words[16] + " 메모"}
	var cands []TermCandidate
	for i, w := range words {
		cands = append(cands, TermCandidate{DisplayAs: w, Confidence: 0.86 + float64(i%8)*0.01, Evidence: transcriptEvidence(w)})
	}
	got, counts := FilterCandidates(cands, sources, 0.85)
	if len(got) != AutoTermsMaxNewPerRun || counts.Cap != 2 || counts.Kept != 15 || counts.Input != 17 {
		t.Fatalf("got %d, counts %+v", len(got), counts)
	}
	for i := 1; i < len(got); i++ {
		if got[i].Confidence > got[i-1].Confidence {
			t.Fatalf("not ranked by confidence at %d", i)
		}
	}
	// words[0], words[8] and words[16] share the lowest confidence; words[16]
	// has two source types, so it outranks them and the other two are capped.
	last := got[len(got)-1]
	if last.DisplayAs != words[16] || len(last.SourceTypes) != 2 {
		t.Fatalf("last kept = %q %v", last.DisplayAs, last.SourceTypes)
	}
	for _, g := range got {
		if g.DisplayAs == words[0] || g.DisplayAs == words[8] {
			t.Fatalf("%q should have been capped", g.DisplayAs)
		}
	}
}

func TestTermFilterCountsCarryNoText(t *testing.T) {
	typ := reflect.TypeOf(TermFilterCounts{})
	for i := 0; i < typ.NumField(); i++ {
		if typ.Field(i).Type.Kind() != reflect.Int {
			t.Fatalf("field %s is %s; counts must be numeric only", typ.Field(i).Name, typ.Field(i).Type)
		}
	}
	_, counts := FilterCandidates([]TermCandidate{{DisplayAs: "비밀프로젝트명", Confidence: 0.99}}, filterSources, 0.85)
	b, _ := json.Marshal(counts)
	if strings.Contains(string(b), "비밀") {
		t.Fatalf("counts leaked text: %s", b)
	}
}

func TestAutoTermShapeOK(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"12345", false},
		{"1,234", false},
		{"2026-10-18", false},
		{"2026.10", false},
		{"10/18/2026", false},
		{"10월 18일", false},
		{"10:30 회의", false},
		{"Oct 18", false},
		{"18 October", false},
		{"May 5", false},
		{"Sept. 3", false},
		{"3rd June", false},
		{"december 25", false},
		{"JUnit 5", true},
		{"MariaDB 10", true},
		{"Decimal 128", true},
		{"Octane 2", true},
		{"Novell 6", true},
		{"Augmented 3", true},
		{"ops@example.com", false},
		{"@channel", false},
		{"https://example", false},
		{"www.example", false},
		{"ttobak.com", false},
		{"socket.io", false},
		{"ASP.NET", true},
		{"VB.NET", true},
		{"Socket.IO", true},
		{"Character.AI", true},
		{"010-1234-5678", false},
		{"1234 5678 9012 3456", false},
		{"a b c d e f g", false},
		{"Alpha Beta Gamma Delta Epsilon Zeta", true},
		{"가", false},
		{"또박", true},
		{"AI", true},
		{"Ai", false},
		{"Go", false},
		{"Git", true},
		{"S3", false},
		{"EC2", true},
		{"GPT-4o", true},
		{"Python 3.12", true},
		{"1분기 실적", true},
		{"쿠버네티스EKS", true},
	}
	for _, tc := range tests {
		if got := autoTermShapeOK(tc.in); got != tc.want {
			t.Errorf("autoTermShapeOK(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestTermsBlocklist(t *testing.T) {
	if n := len(termsBlocklist); n < 380 {
		t.Fatalf("blocklist has %d entries, want about 400", n)
	}
	for _, w := range []string{"그러니까", "음음", "어어", "um", "Uh-huh", "you know", "Meeting", "회의록", "감사합니다", "the"} {
		if !TermBlocklisted(w) {
			t.Errorf("%q should be blocklisted", w)
		}
	}
	for _, w := range []string{"DynamoDB", "쿠버네티스", "Bedrock", "또박", "Fargate", "Kinesis", "Amazon"} {
		if TermBlocklisted(w) {
			t.Errorf("%q must not be blocklisted", w)
		}
	}
	if _, err := ValidateTermInput("Alpha Grid", []string{"you know"}, ""); err == nil {
		t.Fatal("manual heardAs on the blocklist must be rejected")
	}
}

func TestDeriveTranscribePhrase(t *testing.T) {
	long := strings.Repeat("Abcdefghij", 26)
	// 247 lowercase letters plus " ABCDEF": the dotted join is 260 characters,
	// the undotted join 254.
	longWord := strings.Repeat("abcdefghij", 24) + "abcdefg"
	tests := []struct {
		in, lang, want string
	}{
		{"DynamoDB", model.VocabLangKo, "Dynamo-D.B."},
		{"DynamoDB", model.VocabLangEn, "Dynamo-D.B."},
		{"EKS", model.VocabLangEn, "E.K.S."},
		{"Amazon Bedrock", model.VocabLangEn, "Amazon-Bedrock"},
		{"AWSLambda", model.VocabLangEn, "A.W.S.-Lambda"},
		{"AWS Lambda", model.VocabLangEn, "A.W.S.-Lambda"},
		{"Amazon EKS", model.VocabLangEn, "Amazon-E.K.S."},
		{"NASA", model.VocabLangEn, "N.A.S.A."},
		{"Amazon EKS Anywhere", model.VocabLangKo, "Amazon-E.K.S.-Anywhere"},
		{longWord + " ABCDEF", model.VocabLangEn, longWord + "-ABCDEF"},
		{"camelCaseWord", model.VocabLangEn, "camel-Case-Word"},
		{"KUBERNETES", model.VocabLangEn, "KUBERNETES"},
		{"X", model.VocabLangEn, "X"},
		{"Node.js", model.VocabLangEn, "Node-js"},
		{"step_functions", model.VocabLangEn, "step-functions"},
		{"쿠버네티스", model.VocabLangKo, "쿠버네티스"},
		{"쿠버네티스", model.VocabLangEn, ""},
		{"쿠버네티스EKS", model.VocabLangKo, "쿠버네티스-E.K.S."},
		{"또박 싱크", model.VocabLangKo, "또박-싱크"},
		{"O'Reilly", model.VocabLangEn, "O'Reilly"},
		{"O'Reilly", model.VocabLangKo, ""},
		{"EC2", model.VocabLangEn, ""},
		{"C++", model.VocabLangEn, ""},
		{"東京", model.VocabLangKo, ""},
		{"DynamoDB", "ja-JP", ""},
		{long, model.VocabLangEn, ""},
	}
	for _, tc := range tests {
		got := deriveTranscribePhrase(tc.in, tc.lang)
		if got != tc.want {
			t.Errorf("deriveTranscribePhrase(%q, %s) = %q, want %q", tc.in, tc.lang, got, tc.want)
		}
		if got != "" && !validTranscribePhrase(got, tc.lang) {
			t.Errorf("deriveTranscribePhrase(%q, %s) = %q fails validTranscribePhrase", tc.in, tc.lang, got)
		}
	}
}

func TestValidTranscribePhrase(t *testing.T) {
	tests := []struct {
		in     string
		ko, en bool
	}{
		{"Dynamo-D.B.", true, true},
		{"A.B.", true, true},
		{"E.K.S.", true, true},
		{"AB.C", false, false},
		{"Node.js", false, false},
		{".net", false, false},
		{"-dash", false, false},
		{"'quote", false, false},
		{"dash-", false, false},
		{"quote'", false, false},
		{"it's", false, true},
		{"a--b", false, false},
		{"A..B", false, false},
		{"abc1", false, false},
		{"two words", false, false},
		{"가나다", true, false},
		{"또박-싱크", true, false},
		{"Ünicode", false, false},
		{"", false, false},
		{strings.Repeat("a", 256), true, true},
		{strings.Repeat("a", 257), false, false},
	}
	for _, tc := range tests {
		if got := validTranscribePhrase(tc.in, model.VocabLangKo); got != tc.ko {
			t.Errorf("ko %q = %v, want %v", tc.in, got, tc.ko)
		}
		if got := validTranscribePhrase(tc.in, model.VocabLangEn); got != tc.en {
			t.Errorf("en %q = %v, want %v", tc.in, got, tc.en)
		}
	}
}

func TestTranscribePhraseForEligibility(t *testing.T) {
	tests := []struct {
		display, modelPhrase, phrase string
		ko, en                       bool
	}{
		{"Kubernetes", "", "Kubernetes", true, true},
		{"DynamoDB", "", "Dynamo-D.B.", false, true}, // ko dotted acronyms gated off
		{"EKS", "", "E.K.S.", false, true},
		{"쿠버네티스", "", "쿠버네티스", true, false},
		{"쿠버네티스EKS", "", "쿠버네티스-E.K.S.", false, false}, // Hangul: ko only, gated
		{"Kubernetes", "Kuber-netes", "Kuber-netes", true, true},
		{"Kubernetes", "Docker", "Kubernetes", true, true},
		{"Kubernetes", "Kuber--netes", "Kubernetes", true, true},
		{"O'Reilly", "", "O'Reilly", false, true},
		{"EC2", "", "", false, false},
		{"東京", "", "", false, false},
	}
	for _, tc := range tests {
		phrase, ko, en := transcribePhraseFor(tc.display, tc.modelPhrase)
		if phrase != tc.phrase || ko != tc.ko || en != tc.en {
			t.Errorf("transcribePhraseFor(%q, %q) = %q %v %v, want %q %v %v", tc.display, tc.modelPhrase, phrase, ko, en, tc.phrase, tc.ko, tc.en)
		}
	}
}
