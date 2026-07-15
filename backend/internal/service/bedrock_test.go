package service

import (
	"strings"
	"testing"

	"github.com/ttobak/backend/internal/model"
)

func TestParseMeetingInsights_KeepsValidDropsInvalid(t *testing.T) {
	raw := "```json\n" + `[
	  {"type":"risk","text":"PoC 일정 지연 가능"},
	  {"type":"opportunity","text":"ROSA 확대 여지","entities":["ROSA"]},
	  {"type":"bogus","text":"버려야 함"},
	  {"type":"need","text":"   "}
	]` + "\n```"
	got, err := parseMeetingInsights(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 valid insights, got %d (%+v)", len(got), got)
	}
	if got[0].Type != model.InsightRisk || got[1].Type != model.InsightOpportunity {
		t.Errorf("unexpected types: %+v", got)
	}
	if got[0].ID == "" || got[1].ID == "" {
		t.Error("expected IDs assigned")
	}
}

func TestParseMeetingInsights_BadJSON(t *testing.T) {
	_, err := parseMeetingInsights("not json")
	if err == nil {
		t.Error("expected error on bad JSON")
	}
}

func TestParseMeetingInsights_Empty(t *testing.T) {
	got, err := parseMeetingInsights("[]")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty, got %+v", got)
	}
}

func TestHasAcousticSpeakers(t *testing.T) {
	if hasAcousticSpeakers([]WhisperSegment{{Text: "a"}, {Text: "b"}}) {
		t.Error("expected false when no segment has a Speaker")
	}
	if !hasAcousticSpeakers([]WhisperSegment{{Text: "a"}, {Text: "b", Speaker: "spk_1"}}) {
		t.Error("expected true when any segment has a Speaker")
	}
}

func TestBuildRefineSystemPrompt_PreserveMode(t *testing.T) {
	prompt := buildRefineSystemPrompt(true)

	if !strings.Contains(prompt, "AUTHORITATIVE") {
		t.Error("preserve-mode prompt should instruct the model to treat given labels as authoritative")
	}
	if strings.Contains(prompt, "Infer speaker turns") {
		t.Error("preserve-mode prompt should not contain the infer-mode instruction")
	}
}

func TestBuildRefineSystemPrompt_InferMode(t *testing.T) {
	prompt := buildRefineSystemPrompt(false)

	if !strings.Contains(prompt, "Infer speaker turns") {
		t.Error("infer-mode prompt should instruct the model to infer speaker turns from text")
	}
	if strings.Contains(prompt, "AUTHORITATIVE") {
		t.Error("infer-mode prompt should not contain the preserve-mode instruction")
	}
}

func TestBuildRefineSegmentLines_PreserveModeIncludesSpeakerPrefix(t *testing.T) {
	segments := []WhisperSegment{
		{Start: 0.0, End: 1.5, Text: "안녕하세요", Speaker: "spk_0"},
		{Start: 1.5, End: 3.0, Text: "네 반갑습니다", Speaker: "spk_1"},
	}

	lines := buildRefineSegmentLines(segments, true)

	if !strings.Contains(lines, "spk_0: 안녕하세요") {
		t.Errorf("expected speaker-prefixed line, got: %s", lines)
	}
	if !strings.Contains(lines, "spk_1: 네 반갑습니다") {
		t.Errorf("expected speaker-prefixed line, got: %s", lines)
	}
}

func TestBuildRefineSegmentLines_InferModeOmitsSpeakerPrefix(t *testing.T) {
	segments := []WhisperSegment{
		{Start: 0.0, End: 1.5, Text: "안녕하세요"},
	}

	lines := buildRefineSegmentLines(segments, false)

	if strings.Contains(lines, "spk_") {
		t.Errorf("infer-mode lines should not contain a speaker prefix, got: %s", lines)
	}
	if !strings.Contains(lines, "안녕하세요") {
		t.Errorf("expected text in output, got: %s", lines)
	}
}
