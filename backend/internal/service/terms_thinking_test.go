package service

import (
	"encoding/json"
	"testing"
)

// Bedrock rejects {"type":"disabled"} on Claude Sonnet 5.5 ("send
// between_tools instead") and on Claude Opus 5.5 (thinking cannot be turned
// off), so every terms call failed with PROVIDER_FAILED (observed 2026-11-10).
func TestTermsRequestForModelThinking(t *testing.T) {
	base, err := buildTermsRequest(termsCallInput{})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		modelID string
		want    string // marshaled thinking, "" when omitted
	}{
		{"global.anthropic.claude-sonnet-5-5", `{"type":"between_tools"}`},
		{"apac.anthropic.claude-sonnet-5-5", `{"type":"between_tools"}`},
		{"global.anthropic.claude-opus-5-5", ""},
		{"global.anthropic.claude-fable-5-1", ""},
		{ClaudeHaikuModelID, `{"type":"disabled"}`},
		{"global.anthropic.claude-sonnet-5", `{"type":"disabled"}`},
		{"global.anthropic.claude-opus-4-8", `{"type":"disabled"}`},
	}
	for _, tt := range tests {
		t.Run(tt.modelID, func(t *testing.T) {
			got := termsRequestFor(base, tt.modelID)
			raw, err := json.Marshal(PrepareClaudeRequest(got, tt.modelID))
			if err != nil {
				t.Fatal(err)
			}
			var decoded map[string]json.RawMessage
			if err := json.Unmarshal(raw, &decoded); err != nil {
				t.Fatal(err)
			}
			if thinking := string(decoded["thinking"]); thinking != tt.want {
				t.Fatalf("thinking = %q, want %q", thinking, tt.want)
			}
			if base.Thinking == nil || base.Thinking.Type != "disabled" {
				t.Fatalf("caller's request was mutated: %+v", base.Thinking)
			}
		})
	}
}
