package service

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func refinementResponse(text, stop string) string {
	body, _ := json.Marshal(map[string]any{
		"content": []map[string]string{{"type": "text", "text": text}}, "stop_reason": stop,
	})
	return string(body)
}

func TestRefinementRejectsIncompleteOrUnboundOutput(t *testing.T) {
	input := []WhisperSegment{{Start: 0, End: 2, Text: "비용은 100입니다", Speaker: "spk_0"},
		{Start: 3, End: 4, Text: "아니요 200으로 수정합니다", Speaker: "spk_0"}}
	valid := `[{"speaker":"spk_0","text":"비용은 100입니다. 아니요 200으로 수정합니다","startTime":0,"endTime":4,"sourceIds":[0,1]}]`
	tests := []struct {
		name, text, stop string
		want             error
	}{
		{"truncated-valid-json", valid, "max_tokens", errRefinementIncomplete},
		{"missing-completion", valid, "", errRefinementIncomplete},
		{"tool-stop", valid, "tool_use", errRefinementIncomplete},
		{"stop-sequence", valid, "stop_sequence", errRefinementIncomplete},
		{"omission", strings.Replace(valid, "[0,1]", "[0]", 1), "end_turn", errRefinementTimeline},
		{"missing-source", strings.Replace(valid, `,"sourceIds":[0,1]`, "", 1), "end_turn", errRefinementSchema},
		{"duplicate-source", strings.Replace(valid, "[0,1]", "[0,0]", 1), "end_turn", errRefinementCoverage},
		{"null-source", strings.Replace(valid, "[0,1]", "[null,1]", 1), "end_turn", errRefinementCoverage},
		{"foreign-source", strings.Replace(valid, "[0,1]", "[0,2]", 1), "end_turn", errRefinementCoverage},
		{"reordered-source", strings.Replace(valid, "[0,1]", "[1,0]", 1), "end_turn", errRefinementCoverage},
		{"past-end", strings.Replace(valid, `"endTime":4`, `"endTime":9999`, 1), "end_turn", errRefinementTimeline},
		{"negative-start", strings.Replace(valid, `"startTime":0`, `"startTime":-1`, 1), "end_turn", errRefinementTimeline},
		{"reversed", strings.Replace(valid, `"endTime":4`, `"endTime":-1`, 1), "end_turn", errRefinementTimeline},
		{"zero-duration", strings.Replace(valid, `"endTime":4`, `"endTime":0`, 1), "end_turn", errRefinementTimeline},
		{"missing-start", strings.Replace(valid, `"startTime":0,`, "", 1), "end_turn", errRefinementSchema},
		{"null-start", strings.Replace(valid, `"startTime":0`, `"startTime":null`, 1), "end_turn", errRefinementSchema},
		{"blank-text", strings.Replace(valid, "비용은 100입니다. 아니요 200으로 수정합니다", " ", 1), "end_turn", errRefinementSchema},
		{"unknown-field", strings.Replace(valid, `"speaker":`, `"extra":true,"speaker":`, 1), "end_turn", errRefinementParse},
		{"overflow", strings.Replace(valid, `"endTime":4`, `"endTime":1e999`, 1), "end_turn", errRefinementParse},
		{"trailing-json", valid + " []", "end_turn", errRefinementSchema},
		{"fences", "```json\n" + valid + "\n```", "end_turn", errRefinementParse},
		{"null", "null", "end_turn", errRefinementSchema},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			svc := summaryTestService(noteSourceHTTPClient(func(*http.Request) (*http.Response, error) {
				return indexHTTPResponse(200, refinementResponse(test.text, test.stop), nil), nil
			}))
			if _, err := svc.refineChunk(context.Background(), input, 0, 1, nil); !errors.Is(err, test.want) {
				t.Fatalf("error=%v, want %v", err, test.want)
			}
			text, segments, quality, err := svc.RefineTranscriptWithQuality(context.Background(), input)
			if err != nil || !strings.Contains(text, input[1].Text) || len(segments) != 2 ||
				quality.Refinement != "raw" || !quality.Degraded || quality.FallbackChunks != 1 ||
				quality.SpeakerProvenance != "acoustic" {
				t.Fatalf("raw content/status lost: quality=%+v err=%v", quality, err)
			}
		})
	}
	// A plausible time range cannot compensate for an omitted later utterance.
	omitted := `[{"speaker":"spk_0","text":"비용은 100입니다","startTime":0,"endTime":2,"sourceIds":[0]}]`
	if _, err := validateRefinementJSON(omitted, input); !errors.Is(err, errRefinementCoverage) {
		t.Fatalf("missing correction accepted: %v", err)
	}
}

func TestRefinementRejectsInvalidInputBeforeInvokingModel(t *testing.T) {
	for _, input := range [][]WhisperSegment{
		nil, {{Start: 2, End: 1, Text: "source"}}, {{Start: 0, End: math.Inf(1), Text: "source"}},
		{{Start: math.NaN(), End: 2, Text: "source"}}, {{Start: -1, End: 2, Text: "source"}},
		{{Start: 2, End: 3, Text: "later"}, {Start: 0, End: 1, Text: "earlier"}},
		{{Start: 0, End: 1, Text: " "}},
	} {
		svc := summaryTestService(noteSourceHTTPClient(func(*http.Request) (*http.Response, error) {
			t.Fatal("invalid source reached model")
			return nil, nil
		}))
		_, segments, quality, err := svc.RefineTranscriptWithQuality(context.Background(), input)
		if len(normalizeRefinementInput(input)) == 0 {
			if !errors.Is(err, errRefinementInput) || len(segments) != 0 {
				t.Fatal("empty source retained segments")
			}
			continue
		}
		if err != nil || len(segments) != len(input) || !quality.Degraded || quality.Refinement != "raw" {
			t.Fatal("invalid source lost raw speech")
		}
		for _, segment := range segments {
			if segment.Untimed && (segment.StartTime != 0 || segment.EndTime != 0) {
				t.Fatal("untimed speech inherited fabricated timing")
			}
		}
	}
}

func TestRefinementMaintainsAcousticAuthorityAndStructuralCoverage(t *testing.T) {
	input := []WhisperSegment{{Start: 0.12345, End: 1, Text: "first", Speaker: "spk_0"},
		{Start: 1, End: 2, Text: "second", Speaker: "spk_1"}}
	text := `[{"speaker":"spk_1","text":"first","startTime":0.12345,"endTime":1,"sourceIds":[0]},{"speaker":"spk_0","text":"second","startTime":1,"endTime":2,"sourceIds":[1]}]`
	svc := summaryTestService(noteSourceHTTPClient(func(*http.Request) (*http.Response, error) {
		return indexHTTPResponse(200, refinementResponse(text, "end_turn"), nil), nil
	}))
	_, segments, quality, err := svc.RefineTranscriptWithQuality(context.Background(), input)
	if err != nil || segments[0].Speaker != "spk_0" || segments[1].Speaker != "spk_1" ||
		quality.Refinement != "validated" || quality.Degraded || quality.SpeakerProvenance != "acoustic" {
		t.Fatalf("acoustic assignment changed: %+v %+v %v", segments, quality, err)
	}
	if !strings.Contains(buildRefineSegmentLines(input, true), "0.12345") {
		t.Fatal("prompt rounded away source time precision")
	}
	overlap := []WhisperSegment{{Start: 0, End: 2, Text: "a"}, {Start: 1, End: 3, Text: "b"}}
	valid := `[{"speaker":"spk_0","text":"a","startTime":0,"endTime":2,"sourceIds":[0]},{"speaker":"spk_1","text":"b","startTime":1,"endTime":3,"sourceIds":[1]}]`
	if _, err := validateRefinementJSON(valid, overlap); err != nil {
		t.Fatal("legitimate source overlap was rejected")
	}
}

func TestRefinementParallelTracksMixedFallbackAndSpeakerProvenance(t *testing.T) {
	// Six time-separated chunks force the actual parallel worker path.
	var input []WhisperSegment
	for i := 0; i < 6; i++ {
		input = append(input, WhisperSegment{Start: float64(i * 301), End: float64(i*301 + 1), Text: "원문"})
	}
	line := regexp.MustCompile(`\[sourceId=0\]\[([0-9]+)-([0-9]+)\]`)
	svc := summaryTestService(noteSourceHTTPClient(func(request *http.Request) (*http.Response, error) {
		var payload ClaudeRequest
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		match := line.FindStringSubmatch(payload.Messages[0].Content[0].Text)
		if len(match) != 3 {
			t.Error("missing source identity")
			return nil, errors.New("fixture")
		}
		start, _ := strconv.Atoi(match[1])
		stop := "end_turn"
		if start == 602 {
			stop = "max_tokens"
		}
		out := `[{"speaker":"spk_1","text":"정제","startTime":` + match[1] + `,"endTime":` + match[2] + `,"sourceIds":[0]}]`
		return indexHTTPResponse(200, refinementResponse(out, stop), nil), nil
	}))
	_, segments, quality, err := svc.RefineTranscriptWithQuality(context.Background(), input)
	if err != nil || len(segments) != 6 || segments[2].Text != "원문" || quality.TotalChunks != 6 ||
		quality.FallbackChunks != 1 || quality.Refinement != "partial" || !quality.Degraded || quality.SpeakerProvenance != "mixed" {
		t.Fatalf("parallel quality mismatch: %+v %v", quality, err)
	}
	for i, segment := range segments {
		if segment.StartTime != input[i].Start {
			t.Fatal("parallel collection reordered timestamps")
		}
	}
}

func TestRefinementInferenceCannotBypassPartSpeakerNamespaces(t *testing.T) {
	for _, label := range []string{"invented name", "spk_-1", "spk_1000000", "spk_00"} {
		svc := summaryTestService(noteSourceHTTPClient(func(*http.Request) (*http.Response, error) {
			text := `[{"speaker":"` + label + `","text":"source","startTime":0,"endTime":1,"sourceIds":[0]}]`
			return indexHTTPResponse(200, refinementResponse(text, "end_turn"), nil), nil
		}))
		_, err := svc.refineChunk(context.Background(), []WhisperSegment{{Start: 0, End: 1, Text: "source"}}, 0, 1, nil)
		if !errors.Is(err, errRefinementSchema) {
			t.Fatalf("unsafe inferred label accepted: %s", label)
		}
	}
}

func TestRefinementNormalizationKeepsShortSpeechAndDropsBlankText(t *testing.T) {
	input := []WhisperSegment{{Start: 0, End: 0, Text: "네."}, {Start: 0.5, End: 0.5, Text: "  "}, {Start: 1, End: 2, Text: "next"}}
	got := normalizeRefinementInput(input)
	if len(got) != 2 || got[0].Text != "네." || got[0].End != refinementMinDuration || got[1].Text != "next" {
		t.Fatalf("normalized input = %+v", got)
	}
	if input[0].End != 0 {
		t.Fatal("normalization mutated the caller's segments")
	}
	if !validRefinementInput(got) {
		t.Fatal("normalized input should be valid")
	}
}

func TestRefinementReachesModelWhenOnlyAZeroLengthSegmentNeedsWidening(t *testing.T) {
	input := []WhisperSegment{{Start: 0, End: 0, Text: "네."}, {Start: 1, End: 2, Text: "second", Speaker: "spk_1"}}
	text := `[{"speaker":"spk_0","text":"네.","startTime":0,"endTime":0.01,"sourceIds":[0]},{"speaker":"spk_1","text":"second","startTime":1,"endTime":2,"sourceIds":[1]}]`
	calls := 0
	svc := summaryTestService(noteSourceHTTPClient(func(*http.Request) (*http.Response, error) {
		calls++
		return indexHTTPResponse(200, refinementResponse(text, "end_turn"), nil), nil
	}))
	_, segments, quality, err := svc.RefineTranscriptWithQuality(context.Background(), input)
	if err != nil || calls != 1 || quality.Refinement != "validated" || len(segments) != 2 || segments[0].Text != "네." {
		t.Fatalf("zero-length segment disabled refinement: calls=%d segments=%+v quality=%+v err=%v", calls, segments, quality, err)
	}
}

func TestMixedInvalidTimingKeepsEveryWordAndValidAcousticTime(t *testing.T) {
	svc := summaryTestService(noteSourceHTTPClient(func(*http.Request) (*http.Response, error) { t.Fatal("invalid chunk reached model"); return nil, nil }))
	text, segments, quality, err := svc.RefineTranscriptWithQuality(context.Background(), []WhisperSegment{
		{Start: 1, End: 2, Text: "first", Speaker: "spk_0"}, {Start: 9, End: 8, Text: "preserved", Speaker: "spk_1"}, {Start: 3, End: 4, Text: "last", Speaker: "spk_0"},
	})
	if err != nil || len(segments) != 3 || !strings.Contains(text, "first") || !strings.Contains(text, "preserved") || !strings.Contains(text, "last") || !segments[1].Untimed || segments[1].StartTime != 0 || segments[0].StartTime != 1 || segments[2].StartTime != 3 || quality.Timing != "partial" || quality.UntimedSegments != 1 {
		t.Fatal("raw speech/timing was discarded or invented", err, quality)
	}
}

func TestRefinementCompletionSkipsVerifiedReasoningBlocks(t *testing.T) {
	answer := `[{"speaker":"spk_0","text":"안녕하세요","startTime":0,"endTime":1,"sourceIds":[0]}]`
	text, _ := json.Marshal(answer)
	for block, want := range map[string]error{
		`{"type":"thinking","thinking":"plan","signature":"sig"}`: nil,
		`{"type":"redacted_thinking","data":"opaque"}`:            nil,
		`{"type":"thinking","thinking":"plan"}`:                   errRefinementIncomplete,
		`{"type":"redacted_thinking","data":""}`:                  errRefinementIncomplete,
		`{"type":"tool_use","id":"t","name":"x","input":{}}`:      errRefinementIncomplete,
	} {
		body := `{"stop_reason":"end_turn","content":[` + block + `,{"type":"text","text":` + string(text) + `}]}`
		got, err := refinementCompletion([]byte(body))
		if !errors.Is(err, want) || want == nil && got != answer {
			t.Fatalf("%s: got %q, %v; want %v", block, got, err, want)
		}
	}
}
