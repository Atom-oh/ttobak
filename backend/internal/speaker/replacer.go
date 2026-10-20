package speaker

import (
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

// Replacer applies a mapping to the original text once. A replacement name that
// is also another speaker's label must not be renamed again or grow recursively.
type Replacer struct {
	pattern *regexp.Regexp
	names   map[string]string
}

func NewReplacer(mapping map[string]string) *Replacer {
	names := make(map[string]string, len(mapping))
	labels := make([]string, 0, len(mapping))
	for label, name := range mapping {
		if label != "" && name != "" {
			names[label] = name
			labels = append(labels, label)
		}
	}
	// Prefer a full name/label over another label that is its prefix.
	sort.Slice(labels, func(i, j int) bool {
		if len(labels[i]) != len(labels[j]) {
			return len(labels[i]) > len(labels[j])
		}
		return labels[i] < labels[j]
	})
	for i := range labels {
		labels[i] = regexp.QuoteMeta(labels[i])
	}
	replacer := &Replacer{names: names}
	if len(labels) > 0 {
		replacer.pattern = regexp.MustCompile(strings.Join(labels, "|"))
	}
	return replacer
}

func (r *Replacer) Replace(text string) string {
	if r.pattern == nil || text == "" {
		return text
	}
	var result strings.Builder
	written := 0
	for _, match := range r.pattern.FindAllStringIndex(text, -1) {
		start, end := match[0], match[1]
		label := text[start:end]
		if start > 0 {
			before, _ := utf8.DecodeLastRuneInString(text[:start])
			if wordChar(before) {
				continue
			}
		}
		if end < len(text) {
			after, _ := utf8.DecodeRuneInString(text[end:])
			if !trailingBoundaryOK(label, after) {
				continue
			}
		}
		result.WriteString(text[written:start])
		result.WriteString(r.names[label])
		written = end
	}
	result.WriteString(text[written:])
	return result.String()
}
