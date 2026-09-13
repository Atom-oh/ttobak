package service

import (
	"errors"
	"regexp"
	"strings"
	"unicode"
)

// ErrResummaryNoSource rejects empty or unhydrated summary evidence.
var ErrResummaryNoSource = errors.New("no saved summary source")

var ErrSummaryConflict = errors.New("summary source changed")

const summaryCitationNotice = "> 문서 인용을 확인할 수 없는 내용을 제외했습니다."

var summaryListStart = regexp.MustCompile(`^([ \t]*)(?:[-+*]|\d+[.)])\s+`)
var summaryInlineLink = regexp.MustCompile(`\[TS:[^\]]*\]|\[[^\]]*\]\([^)]*\)`)

// Slice original bytes: preserve separators, list continuations and fenced blocks.
func summaryClaimUnits(content string) []string {
	var units []string
	start, offset, indent, fence := 0, 0, -1, ""
	flush := func(end int) {
		if end > start {
			units = append(units, content[start:end])
			start = end
		}
	}
	for _, line := range strings.SplitAfter(content, "\n") {
		text := strings.TrimSpace(line)
		if fence != "" {
			if strings.HasPrefix(text, fence) && strings.Trim(text, fence[:1]) == "" {
				fence = ""
				flush(offset + len(line))
			}
		} else if strings.HasPrefix(text, "```") || strings.HasPrefix(text, "~~~") {
			flush(offset)
			fence = text[:len(text)-len(strings.TrimLeft(text, text[:1]))]
			indent = -1
		} else if text == "" {
			flush(offset + len(line))
			indent = -1
		} else if match := summaryListStart.FindStringSubmatch(line); match != nil {
			depth := len(strings.ReplaceAll(match[1], "\t", "    "))
			if indent < 0 || depth <= indent {
				flush(offset)
				indent = depth
			}
		} else if strings.HasPrefix(text, "#") {
			flush(offset)
			indent = -1
		}
		offset += len(line)
	}
	flush(offset)
	return units
}

func summaryHasBody(content string) bool {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == summaryCitationNotice || strings.HasPrefix(line, "#") ||
			strings.HasPrefix(line, "```") || strings.HasPrefix(line, "~~~") {
			continue
		}
		line = summaryInlineLink.ReplaceAllString(line, "")
		line = summaryListStart.ReplaceAllString(line, "")
		for _, marker := range []string{"[ ]", "[x]", "[X]"} {
			line = strings.TrimPrefix(line, marker)
		}
		for _, r := range line {
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				return true
			}
		}
	}
	return false
}
