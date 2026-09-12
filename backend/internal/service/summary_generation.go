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

// Keep a list item and its continuation together without dropping sibling items.
func summaryClaimUnits(content string) []string {
	var units []string
	for _, block := range strings.Split(content, "\n\n") {
		var current []string
		indent := -1
		flush := func() {
			if len(current) > 0 {
				units = append(units, strings.Join(current, "\n"))
				current = nil
			}
		}
		for _, line := range strings.Split(block, "\n") {
			if match := summaryListStart.FindStringSubmatch(line); match != nil {
				depth := len(strings.ReplaceAll(match[1], "\t", "    "))
				if indent < 0 || depth <= indent {
					flush()
					indent = depth
				}
			} else if strings.HasPrefix(strings.TrimSpace(line), "#") {
				flush()
				indent = -1
			}
			current = append(current, line)
		}
		flush()
	}
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
