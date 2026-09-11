package service

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/ttobak/backend/internal/model"
)

// extractSpeakerSegments keeps pronunciation items only; sentence punctuation
// remains in Results.Transcripts[0].Transcript, which becomes TranscriptA.
const legacyTranscribeText = "안녕하세요, 반갑습니다. 예산은 1.5억원입니다. 승인하지 않았습니다!"
const legacyTranscribeSegments = `[{"id":"a-12","speaker":"spk_0","text":"안녕하세요 반갑습니다 예산은 1.5억원입니다","startTime":12,"endTime":20},{"id":"a-22","speaker":"spk_1","text":"승인하지 않았습니다","startTime":22,"endTime":24}]`

func TestTranscriptSegmentsForText_LegacyPunctuation(t *testing.T) {
	for _, tt := range []struct {
		name       string
		transcript string
		texts      []string
		want       []string
	}{
		{
			name:       "Transcribe pronunciation items omit sentence punctuation",
			transcript: legacyTranscribeText,
			texts:      []string{"안녕하세요 반갑습니다 예산은 1.5억원입니다", "승인하지 않았습니다"},
			want:       []string{"안녕하세요, 반갑습니다. 예산은 1.5억원입니다.", "승인하지 않았습니다!"},
		},
		{
			name:       "questions and negation retain current punctuation",
			transcript: "Approved? No, not yet.",
			texts:      []string{"Approved", "No not yet"},
			want:       []string{"Approved?", "No, not yet."},
		},
		{
			name:       "punctuation at speaker boundary belongs to preceding word",
			transcript: "Yes, no.",
			texts:      []string{"Yes", "no"},
			want:       []string{"Yes,", "no."},
		},
		{
			name:       "source whitespace is preserved within reconstructed segments",
			transcript: " \tYes,\tno.\nMaybe?\n",
			texts:      []string{"Yes no", "Maybe"},
			want:       []string{"Yes,\tno.", "Maybe?"},
		},
		{
			name:       "Unicode sentence boundaries",
			transcript: "확인했나요？ 아직입니다。",
			texts:      []string{"확인했나요", "아직입니다"},
			want:       []string{"확인했나요？", "아직입니다。"},
		},
		{
			name:       "punctuation does not erase matching numbers or symbols",
			transcript: "Budget: -1.5%, then 12,000. C++ remains.",
			texts:      []string{"Budget -1.5% then 12,000", "C++ remains"},
			want:       []string{"Budget: -1.5%, then 12,000.", "C++ remains."},
		},
		{
			name:       "exact plain segments still work",
			transcript: "Approved? No, not yet.",
			texts:      []string{"Approved?", "No, not yet."},
			want:       []string{"Approved?", "No, not yet."},
		},
		{
			name:       "exact refined speaker groups still work",
			transcript: "[spk_0]\nApproved?\n\n[spk_1]\nNo, not yet.",
			texts:      []string{"Approved?", "No, not yet."},
			want:       []string{"Approved?", "No, not yet."},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			input := make([]speakerSegment, len(tt.texts))
			for i, text := range tt.texts {
				input[i] = speakerSegment{
					ID: fmt.Sprintf("seg-%d", i), Speaker: fmt.Sprintf("spk_%d", i), Text: text,
					StartTime: float64(i * 10), EndTime: float64(i*10 + 5),
				}
			}
			raw, err := json.Marshal(input)
			if err != nil {
				t.Fatal(err)
			}
			got := transcriptSegmentsForText(tt.transcript, string(raw))
			if len(got) != len(input) {
				t.Fatalf("legacy segments were discarded: got=%+v", got)
			}
			for i, text := range tt.want {
				want := input[i]
				want.Text = text
				if got[i] != want {
					t.Errorf("segment %d = %+v, want %+v", i, got[i], want)
				}
			}
		})
	}
}

func TestTranscriptSegmentsForText_RejectsChangedWordsAndSymbols(t *testing.T) {
	for _, tt := range []struct {
		name, transcript, segment string
	}{
		{"decimal digits joined", "Budget is 1.5 million.", "Budget is 15 million"},
		{"decimal split into words", "Budget is 1.5 million.", "Budget is 1 5 million"},
		{"thousands separator removed", "Budget is 12,000.", "Budget is 12000"},
		{"time separator removed", "Meet at 10:30.", "Meet at 1030"},
		{"negative sign removed", "Change is -5.", "Change is 5"},
		{"percent removed", "Rate is 10%.", "Rate is 10"},
		{"currency removed", "Price is $10.", "Price is 10"},
		{"symbol removed", "Use C++.", "Use C"},
		{"range collapsed", "Use 10-15 nodes.", "Use 1015 nodes"},
		{"numeric edit", "Budget is 200.", "Budget is 100"},
		{"English negation omitted", "We did not approve.", "We did approve"},
		{"Korean negation omitted", "승인하지 않았습니다.", "승인했습니다"},
		{"contraction changed", "We can't approve.", "We cant approve"},
		{"punctuation already present conflicts", "Approved?", "Approved!"},
		{"internal dot is not a sentence boundary", "Use example.com.", "Use examplecom"},
		{"partial final sentence", "We approve. Budget stays pending.", "We approve"},
		{"missing first words", "We do not approve.", "not approve"},
		{"obsolete extra words", "We approve.", "We approve next week"},
		{"partial word cannot match", "It is notable.", "It is not able"},
		{"speaker label edit", "[Kim]\nApproved.", "Approved"},
		{"non-boundary symbol", "Approved ★.", "Approved"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			raw, err := json.Marshal([]speakerSegment{{ID: "old", Speaker: "spk_0", Text: tt.segment, StartTime: 12, EndTime: 20}})
			if err != nil {
				t.Fatal(err)
			}
			if got := transcriptSegmentsForText(tt.transcript, string(raw)); got != nil {
				t.Fatalf("changed or incomplete source received old anchors: %+v", got)
			}
		})
	}
}

func TestTranscriptSegmentsForText_PartialLegacySegments(t *testing.T) {
	const raw = `[{"id":"first","speaker":"spk_0","text":"First statement","startTime":0,"endTime":2},{"id":"last","speaker":"spk_1","text":"Last statement","startTime":10,"endTime":12}]`
	for _, source := range []string{
		"First statement. Missing context. Last statement.",
		"First statement. Last statement. Additional decision.",
		"Last statement. First statement.",
	} {
		if got := transcriptSegmentsForText(source, raw); got != nil {
			t.Errorf("partial or reordered segments accepted for %q: %+v", source, got)
		}
	}
}

func TestGetMeetingDetail_LegacySegmentsRetainCurrentPunctuation(t *testing.T) {
	repo := newMockMeetingRepo()
	repo.addMeeting(&model.Meeting{
		MeetingID: "m-1", UserID: "owner-1", Status: model.StatusDone,
		TranscriptA: legacyTranscribeText, TranscriptSegments: legacyTranscribeSegments,
	})
	detail, err := newMeetingServiceWithRepo(repo).GetMeetingDetail(context.Background(), "owner-1", "m-1")
	if err != nil {
		t.Fatal(err)
	}
	var got []speakerSegment
	if err := json.Unmarshal(detail.Transcription, &got); err != nil {
		t.Fatalf("legacy speaker view missing: %v", err)
	}
	want := []speakerSegment{
		{ID: "a-12", Speaker: "spk_0", Text: "안녕하세요, 반갑습니다. 예산은 1.5억원입니다.", StartTime: 12, EndTime: 20},
		{ID: "a-22", Speaker: "spk_1", Text: "승인하지 않았습니다!", StartTime: 22, EndTime: 24},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("detail must return reconstructed text with the original anchors: got=%+v", got)
	}
	if detail.TranscriptA != legacyTranscribeText || repo.meetingsByID["m-1"].TranscriptSegments != legacyTranscribeSegments {
		t.Fatal("read-time reconstruction must not rewrite stored sources")
	}
}

func TestSummarizeTranscript_LegacySegmentsPreservePunctuationAndAnchors(t *testing.T) {
	prompt, content, err := summarizeNoteSource(t, &model.Meeting{
		MeetingID: "m-1", UserID: "owner-1", TranscriptA: legacyTranscribeText,
		TranscriptSegments: legacyTranscribeSegments,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"[spk_0 12초~20초] 안녕하세요, 반갑습니다. 예산은 1.5억원입니다.",
		"[spk_1 22초~24초] 승인하지 않았습니다!",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("summary lost current punctuation or speaker context: %q", prompt)
		}
	}
	if content != "요약 [00:12](transcript://a-12)" {
		t.Errorf("legacy transcript anchor lost: %q", content)
	}
}

func TestUpdateSpeakers_MergedNamesRetainDetailAndSummaryAnchors(t *testing.T) {
	const source = "[spk_0]\n예산은 1.5억원입니다.\n\n[spk_1]\n승인하지 않았습니다!\n\n[spk_2]\nC++ 비용은 -5%입니다.\n\n[spk_0]\n다음 주에 확인합니다."
	const merged = "[Kim]\n예산은 1.5억원입니다.\n\n[Kim]\n승인하지 않았습니다!\n\n[Lee]\nC++ 비용은 -5%입니다.\n\n[Kim]\n다음 주에 확인합니다."
	segments := []speakerSegment{
		{ID: "a-12", Speaker: "spk_0", Text: "예산은 1.5억원입니다.", StartTime: 12, EndTime: 20},
		{ID: "a-22", Speaker: "spk_1", Text: "승인하지 않았습니다!", StartTime: 22, EndTime: 24},
		{ID: "a-26", Speaker: "spk_2", Text: "C++ 비용은 -5%입니다.", StartTime: 26, EndTime: 30},
		{ID: "a-32", Speaker: "spk_0", Text: "다음 주에 확인합니다.", StartTime: 32, EndTime: 34},
	}
	raw, err := json.Marshal(segments)
	if err != nil {
		t.Fatal(err)
	}
	repo := newMockMeetingRepo()
	repo.addMeeting(&model.Meeting{
		MeetingID: "m-1", UserID: "owner-1", Status: model.StatusDone,
		TranscriptA: source, TranscriptSegments: string(raw),
	})
	svc := newMeetingServiceWithRepo(repo)
	_, err = svc.UpdateSpeakers(context.Background(), "owner-1", "m-1", &model.UpdateSpeakersRequest{
		SpeakerMap: map[string]string{"spk_0": "Kim", "spk_1": "Kim", "spk_2": "Lee"},
	})
	if err != nil {
		t.Fatal(err)
	}
	stored := repo.meetingsByID["m-1"]
	if stored.TranscriptA != merged {
		t.Fatalf("fixture must exercise the real repeated-header write: %q", stored.TranscriptA)
	}
	segments[0].Speaker, segments[1].Speaker, segments[2].Speaker, segments[3].Speaker = "Kim", "Kim", "Lee", "Kim"
	detail, err := svc.GetMeetingDetail(context.Background(), "owner-1", "m-1")
	if err != nil {
		t.Fatal(err)
	}
	var rendered []speakerSegment
	if err := json.Unmarshal(detail.Transcription, &rendered); err != nil {
		t.Fatalf("merging speakers hid the detail segments: %v", err)
	}
	if !reflect.DeepEqual(rendered, segments) {
		t.Errorf("merging speakers changed text, IDs or timestamps: %+v", rendered)
	}
	request, content, _, err := summarizeNoteSourceResponse(t, stored,
		`{"content":[{"type":"text","text":"첫 내용 [TS:12]\n둘째 내용 [TS:22]\n검토 [TS:32]"}],"stop_reason":"end_turn"}`)
	if err != nil {
		t.Fatal(err)
	}
	for _, anchor := range []string{"[00:12](transcript://a-12)", "[00:22](transcript://a-22)", "[00:32](transcript://a-32)"} {
		if !strings.Contains(content, anchor) {
			t.Errorf("merged speaker lost summary anchor %q: %q", anchor, content)
		}
	}
	prompt := request.Messages[0].Content[0].Text
	for _, line := range []string{
		"[Kim 12초~20초] 예산은 1.5억원입니다.",
		"[Kim 22초~24초] 승인하지 않았습니다!",
		"[Lee 26초~30초] C++ 비용은 -5%입니다.",
	} {
		if !strings.Contains(prompt, line) {
			t.Errorf("merged speaker source missing from prompt: %q", prompt)
		}
	}
	if detail.TranscriptA != merged || stored.TranscriptA != merged {
		t.Fatal("comparison normalization must not rewrite stored or returned raw text")
	}
}

func TestTranscriptSegmentsForText_RepeatedGroupedHeaderValidation(t *testing.T) {
	segments := []speakerSegment{
		{ID: "first", Speaker: "Kim", Text: "Budget is 1.5.", StartTime: 0, EndTime: 2},
		{ID: "second", Speaker: "Kim", Text: "Not approved!", StartTime: 2, EndTime: 4},
		{ID: "third", Speaker: "Lee", Text: "C++ stays.", StartTime: 4, EndTime: 6},
		{ID: "fourth", Speaker: "Kim", Text: "Later.", StartTime: 6, EndTime: 8},
	}
	raw, err := json.Marshal(segments)
	if err != nil {
		t.Fatal(err)
	}
	const repeated = "[Kim]\nBudget is 1.5.\n\n[Kim]\nNot approved!\n\n[Lee]\nC++ stays.\n\n[Kim]\nLater."
	for _, tt := range []struct {
		name, source string
		valid        bool
	}{
		{"merged adjacent blocks", repeated, true},
		{"canonical grouped form", "[Kim]\nBudget is 1.5. Not approved!\n\n[Lee]\nC++ stays.\n\n[Kim]\nLater.", true},
		{"CRLF headers", strings.ReplaceAll(repeated, "\n", "\r\n"), true},
		{"different recognized speaker", strings.Replace(repeated, "[Kim]\nNot", "[Lee]\nNot", 1), false},
		{"unknown speaker", strings.Replace(repeated, "[Kim]\nNot", "[Unknown]\nNot", 1), false},
		{"changed first speaker", strings.Replace(repeated, "[Kim]", "[Park]", 1), false},
		{"case changed speaker", strings.Replace(repeated, "[Kim]\nNot", "[kim]\nNot", 1), false},
		{"reordered labels", "[Lee]\nBudget is 1.5.\n\n[Kim]\nNot approved! C++ stays. Later.", false},
		{"inline bracket text is not a redundant header", strings.Replace(repeated, "1.5.\n\n[Kim]", "1.5. [Kim]", 1), false},
		{"unknown bracketed content cannot disappear", strings.Replace(repeated, "Not approved!", "[Decision]\nNot approved!", 1), false},
		{"same header inside a sentence cannot disappear", strings.Replace(repeated, "Not approved!", "Not [Kim] approved!", 1), false},
		{"numeric edit", strings.Replace(repeated, "1.5", "15", 1), false},
		{"symbol edit", strings.Replace(repeated, "C++", "C", 1), false},
		{"negation edit", strings.Replace(repeated, "Not approved!", "Approved!", 1), false},
		{"punctuation edit", strings.Replace(repeated, "Not approved!", "Not approved?", 1), false},
		{"partial text", strings.TrimSuffix(repeated, "Later."), false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := transcriptSegmentsForText(tt.source, string(raw))
			if tt.valid {
				if !reflect.DeepEqual(got, segments) {
					t.Errorf("valid merged groups lost original segment data: %+v", got)
				}
			} else if got != nil {
				t.Errorf("changed headers or body were accepted: %+v", got)
			}
		})
	}
}

func TestTranscriptSegmentsForText_GroupedLiteralBracketsRemainText(t *testing.T) {
	segments := []speakerSegment{
		{ID: "first", Speaker: "Kim", Text: "The slide says:\n[Kim]\n[Decision]\nBudget is 1.5.", StartTime: 0, EndTime: 2},
		{ID: "second", Speaker: "Kim", Text: "[Kim]\nNot approved!", StartTime: 2, EndTime: 4},
	}
	raw, err := json.Marshal(segments)
	if err != nil {
		t.Fatal(err)
	}
	const source = "[Kim]\nThe slide says:\n[Kim]\n[Decision]\nBudget is 1.5.\n\n[Kim]\n[Kim]\nNot approved!"
	if got := transcriptSegmentsForText(source, string(raw)); !reflect.DeepEqual(got, segments) {
		t.Errorf("literal bracketed body text was discarded as speaker headers: %+v", got)
	}
	plain := "The slide says:\n[Kim]\n[Decision]\nBudget is 1.5.\n[Kim]\nNot approved!"
	if got := transcriptSegmentsForText(plain, string(raw)); !reflect.DeepEqual(got, segments) {
		t.Errorf("group normalization affected plain bracketed body text: %+v", got)
	}
}
