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
