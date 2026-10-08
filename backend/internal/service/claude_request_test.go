package service

import (
	"encoding/json"
	"testing"
)

func TestPrepareClaudeRequestDisablesThinkingOnlyForHaiku(t *testing.T) {
	base := ClaudeRequest{AnthropicVersion: "bedrock-2023-05-31", MaxTokens: 2048, System: "s"}
	tests := []struct {
		name    string
		modelID string
		want    bool
	}{
		{"haiku", ClaudeHaikuModelID, true},
		{"sonnet", ClaudeSonnetModelID, false},
		{"opus", ClaudeOpusModelID, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := PrepareClaudeRequest(base, tt.modelID)
			raw, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			var decoded map[string]json.RawMessage
			if err := json.Unmarshal(raw, &decoded); err != nil {
				t.Fatal(err)
			}
			thinking, present := decoded["thinking"]
			if present != tt.want || (present && string(thinking) != `{"type":"disabled"}`) {
				t.Fatalf("thinking = %s (present=%v), want present=%v", thinking, present, tt.want)
			}
			if base.Thinking != nil {
				t.Fatal("caller's request was mutated")
			}
		})
	}
}
