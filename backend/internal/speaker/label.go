// Package speaker holds the acoustic speaker-label conventions ("spk_N")
// shared by the summarize Lambda (which produces the labels) and the
// meeting service (which lets users rename them). Keeping the logic here
// -- rather than duplicated per-package -- is what makes label handling in
// both places consistent by construction instead of by convention.
package speaker

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

// MaxPerPart bounds n before offsetting -- without this, an n that itself
// already exceeds the offset step (implausible from _assign_speakers'
// normalization in practice, but not something this function can assume)
// would let one part's namespaced range collide with another's. Enforcing
// the bound makes collision-freedom a property of this function alone, not
// a fact about pyannote's typical output that could change.
const MaxPerPart = 1_000_000

// Namespace rewrites an acoustic spk_N label to be unique across parts by
// offsetting N by partIndex*MaxPerPart. Stays within spk_\d+ so the
// frontend's SpeakerMapEditor (UNMAPPED_PATTERN, speakerSortKey's parseInt)
// needs no changes. Non-spk_ labels pass through unchanged.
func Namespace(label string, partIndex int) string {
	if !strings.HasPrefix(label, "spk_") {
		return label
	}
	n, err := strconv.Atoi(strings.TrimPrefix(label, "spk_"))
	if err != nil {
		return label // not a spk_N label (e.g. a Korean 화자 fallback) -- leave as-is
	}
	// Clamp BEFORE computing the offset, and apply the offset uniformly
	// (including partIndex==0) so every part's output is confined to the
	// disjoint interval [partIndex*MaxPerPart, (partIndex+1)*MaxPerPart - 1]
	// regardless of the input n. _assign_speakers only ever produces
	// sequential labels starting at 0, so n reaching this clamp in practice
	// would mean something already went wrong upstream; the clamp exists so
	// out-of-range input degrades to a shared bucket within its own part
	// rather than colliding with another part's range.
	switch {
	case n < 0:
		n = 0
	case n >= MaxPerPart:
		n = MaxPerPart - 1
	}
	return "spk_" + strconv.Itoa(partIndex*MaxPerPart+n)
}

// wordChar reports whether r counts as part of a label token for boundary
// purposes. Go's regexp \b is ASCII-only ([0-9A-Za-z_]), which would never
// fire at the start of a label like "화자A" (rawFallbackSegments' Korean
// default label, also matched by the frontend's UNMAPPED_PATTERN) since 화
// isn't an ASCII word char -- so boundaries here are checked manually
// against this wider set instead of relying on \b.
func wordChar(r rune) bool {
	return r == '_' ||
		(r >= '0' && r <= '9') ||
		(r >= 'a' && r <= 'z') ||
		(r >= 'A' && r <= 'Z') ||
		(r >= '가' && r <= '힣')
}

// ReplaceLabel replaces whole-token occurrences of label in text with
// replacement. "Whole-token" means the character immediately before and
// after the match (if any) is not a wordChar -- so a label that's a textual
// prefix of another label (e.g. "spk_1" vs the namespaced "spk_1000000") is
// never partially matched, the way a plain strings.ReplaceAll would corrupt
// "spk_1000000" into "<replacement>000000". Only the label itself is
// consumed per match, not its boundary characters, so two occurrences
// separated by a single character (e.g. "spk_1 spk_1") are both found
// rather than the second one's leading boundary being eaten by the first
// match.
func ReplaceLabel(text, label, replacement string) string {
	if label == "" || text == "" {
		return text
	}
	var sb strings.Builder
	rest := text
	for {
		idx := strings.Index(rest, label)
		if idx < 0 {
			sb.WriteString(rest)
			return sb.String()
		}
		before := idx == 0
		if !before {
			r, _ := utf8.DecodeLastRuneInString(rest[:idx])
			before = !wordChar(r)
		}
		afterIdx := idx + len(label)
		after := afterIdx == len(rest)
		if !after {
			r, _ := utf8.DecodeRuneInString(rest[afterIdx:])
			after = !wordChar(r)
		}
		if before && after {
			sb.WriteString(rest[:idx])
			sb.WriteString(replacement)
			rest = rest[afterIdx:]
			continue
		}
		// Not a whole-token match (e.g. "spk_1" inside "spk_10") -- keep this
		// occurrence as-is and resume searching just past its first rune, so
		// the same non-boundary match isn't found again in an infinite loop.
		_, size := utf8.DecodeRuneInString(rest[idx:])
		sb.WriteString(rest[:idx+size])
		rest = rest[idx+size:]
	}
}
