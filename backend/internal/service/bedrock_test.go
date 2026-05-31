package service

import (
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
