package service

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestReadingWindowsRequireExactCurrentTextAndRealTimes(t *testing.T) {
	segments := []speakerSegment{
		{ID: "first", Speaker: "김", Text: "예산 1.5.", StartTime: 0, EndTime: 2},
		{ID: "second", Speaker: "김", Text: "[결정] C++ 보류😀.", StartTime: 2, EndTime: 4},
	}
	raw, _ := json.Marshal(segments)
	for _, source := range []string{
		"예산 1.5.\n[결정] C++ 보류😀.\n",
		"[김]\n예산 1.5.\n\n[김]\n[결정] C++ 보류😀.\n",
	} {
		windows := readingWindows(source, string(raw))
		if len(windows) != 2 {
			t.Fatalf("valid segments unavailable: %q", source)
		}
		if windows[0].start != 0 || windows[0].end != windows[1].start || windows[1].end != utf8.RuneCountInString(source) {
			t.Fatalf("windows omit raw characters: %+v", windows)
		}
		for _, changed := range []string{strings.Replace(source, "1.5", "15", 1), strings.Replace(source, "C++", "C", 1), source + "extra"} {
			if readingWindows(changed, string(raw)) != nil {
				t.Fatal("stale segments supplied timing")
			}
		}
	}
	for _, invalid := range []string{
		`[{"text":"text"}]`,
		`[{"text":"text","startTime":null,"endTime":1}]`,
		`[{"text":"text","startTime":2,"endTime":1}]`,
		`[{"text":"text","startTime":-1,"endTime":1}]`,
	} {
		if readingWindows("text", invalid) != nil {
			t.Fatalf("invented time from %s", invalid)
		}
	}
}
