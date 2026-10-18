package main

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
	"github.com/ttobak/backend/internal/service"
)

func TestExtractMeetingIDFromTranscriptKey(t *testing.T) {
	cases := []struct {
		name string
		key  string
		want string
	}{
		{"plain transcript", "transcripts/abc-123.json", "abc-123"},
		{"nova transcript", "transcripts/abc-123-nova.json", "abc-123"},
		{"part key rejected", "transcripts/abc-123_part_001.json", ""},
		// The repository's overflow spill writes transcripts/{id}/{field}.txt
		// under the same EventBridge-matched prefix — nested keys must never
		// be mistaken for a meeting id (a garbage id skips the status guard
		// and errors the invocation on every long meeting's pipeline run).
		{"spill transcriptA key rejected", "transcripts/abc-123/transcriptA.txt", ""},
		{"spill segments key rejected", "transcripts/abc-123/transcriptSegments.txt", ""},
		{"versioned spill key rejected", "transcripts/abc-123/transcriptA.0123456789abcdef0123456789abcdef.txt", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := extractMeetingIDFromTranscriptKey(c.key); got != c.want {
				t.Fatalf("extractMeetingIDFromTranscriptKey(%q) = %q, want %q", c.key, got, c.want)
			}
		})
	}
}

func TestTranscriptHasNoSpeech(t *testing.T) {
	cases := []struct {
		name   string
		parsed *parsedTranscript
		want   bool
	}{
		{"nil", nil, false},
		{"blank text, no segments", &parsedTranscript{Text: "  \n"}, true},
		{"blank whisper segments", &parsedTranscript{Whisper: []service.WhisperSegment{{Text: " "}, {Text: ""}}}, true},
		{"text present", &parsedTranscript{Text: "안녕하세요"}, false},
		{"speaker segment present", &parsedTranscript{Segments: []TranscriptSegmentOut{{Text: "x"}}}, false},
		{"whisper text present", &parsedTranscript{Whisper: []service.WhisperSegment{{Text: "네"}}}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := transcriptHasNoSpeech(c.parsed); got != c.want {
				t.Fatalf("transcriptHasNoSpeech = %v, want %v", got, c.want)
			}
		})
	}
}

func TestHandleNoSpeechMarksWithoutSummarizing(t *testing.T) {
	original := markNoSpeech
	t.Cleanup(func() { markNoSpeech = original })
	snapshot := &model.SummarySnapshot{Meeting: &model.Meeting{UserID: "u", MeetingID: "m"}}
	calls := 0
	markNoSpeech = func(_ context.Context, got *model.SummarySnapshot) error {
		calls++
		if got != snapshot {
			t.Fatal("mark must use the captured transcript fence")
		}
		return nil
	}
	// repo and bedrockService are nil here: reaching StartTranscriptSummary
	// or any Bedrock call would panic, so a clean return proves neither ran.
	handled, err := handleNoSpeech(context.Background(), snapshot, &parsedTranscript{Text: ""})
	if !handled || err != nil || calls != 1 {
		t.Fatalf("handled=%v err=%v calls=%d", handled, err, calls)
	}
	handled, err = handleNoSpeech(context.Background(), snapshot, &parsedTranscript{Text: "speech"})
	if handled || err != nil || calls != 1 {
		t.Fatalf("speech must continue to summary: handled=%v err=%v calls=%d", handled, err, calls)
	}
	markNoSpeech = func(context.Context, *model.SummarySnapshot) error { return repository.ErrConditionFailed }
	if handled, err := handleNoSpeech(context.Background(), snapshot, &parsedTranscript{}); !handled || err != nil {
		t.Fatalf("a superseded fence is not a retryable error: handled=%v err=%v", handled, err)
	}
	markNoSpeech = func(context.Context, *model.SummarySnapshot) error { return errors.New("throttled") }
	if handled, err := handleNoSpeech(context.Background(), snapshot, &parsedTranscript{}); !handled || err == nil {
		t.Fatal("a transient mark failure must be retried by Lambda")
	}

	// A transcript replaced after download is superseded: no mark at all.
	installTranscriptFixture(t, retryHTTP(func(request *http.Request) (*http.Response, error) {
		if request.Method != "HEAD" {
			t.Fatal("revalidation read source content")
		}
		return transcriptFixtureResponse(200, "", http.Header{"Etag": {`"new"`}}), nil
	}))
	markNoSpeech = func(context.Context, *model.SummarySnapshot) error { t.Fatal("stale bytes marked"); return nil }
	stale := &parsedTranscript{Objects: []model.SummaryObject{{Bucket: "bucket", Key: "transcripts/m.json", ETag: `"old"`}}}
	if handled, err := handleNoSpeech(context.Background(), snapshot, stale); !handled || err != nil {
		t.Fatalf("replaced transcript: handled=%v err=%v", handled, err)
	}
}
