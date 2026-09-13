package main

import (
	"encoding/json"
	"testing"
)

func TestSourceFramesRequireExplicitSupportedVersion(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want bool
	}{
		{"legacy", `{"action":"ask_live"}`, false},
		{"supported", `{"action":"ask_live","sourceFramesVersion":1}`, true},
		{"unsupported", `{"action":"ask_live","sourceFramesVersion":2}`, false},
		{"negative", `{"action":"ask_live","sourceFramesVersion":-1}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var message wsMessage
			if err := json.Unmarshal([]byte(tc.body), &message); err != nil {
				t.Fatal(err)
			}
			if message.supportsSourceFrames() != tc.want {
				t.Fatalf("source frame support = %v, want %v", message.supportsSourceFrames(), tc.want)
			}
		})
	}
}
