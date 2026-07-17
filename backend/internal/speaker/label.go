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
// needs no changes. Non-spk_ labels (including a 화자X rawFallbackSegments
// default) pass through unchanged -- suffixing 화자X per-part to avoid a
// double-refine-failure collision was considered and rejected: the
// frontend's UNMAPPED_PATTERN (exact-match `^화자[A-Z]$`) and
// speakerSortKey (exact length-3 check) both silently stop recognizing a
// suffixed label like "화자A-1", which is a guaranteed, always-triggered
// regression traded against a low-probability edge case (refine failing
// for a chunk in two different parts of the same meeting).
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

func isDigit(r rune) bool { return r >= '0' && r <= '9' }

func isUpperLetter(r rune) bool { return r >= 'A' && r <= 'Z' }

func isHangul(r rune) bool { return r >= '가' && r <= '힣' }

// wordChar reports whether r counts as part of a generic label token for
// boundary purposes. Go's regexp \b is ASCII-only ([0-9A-Za-z_]), which
// would never fire at the start of a label like "화자A" (rawFallbackSegments'
// Korean default label, also matched by the frontend's UNMAPPED_PATTERN)
// since 화 isn't an ASCII word char -- so boundaries here are checked
// manually against this wider set instead of relying on \b. Used as the
// leading-boundary check for every label kind, and as the fallback
// trailing-boundary check for label shapes trailingBoundaryOK doesn't
// specifically recognize.
func wordChar(r rune) bool {
	return r == '_' || isDigit(r) ||
		(r >= 'a' && r <= 'z') ||
		(r >= 'A' && r <= 'Z') ||
		isHangul(r)
}

// isKoreanFallbackLabel reports whether label has the rawFallbackSegments
// "화자A" shape (frontend's UNMAPPED_PATTERN: 화자[A-Z], one letter only).
func isKoreanFallbackLabel(label string) bool {
	const prefix = "화자"
	if !strings.HasPrefix(label, prefix) {
		return false
	}
	rest := label[len(prefix):]
	r, size := utf8.DecodeRuneInString(rest)
	return size == len(rest) && isUpperLetter(r)
}

// trailingBoundaryOK reports whether r (the character immediately after a
// label match, if any) is a real token boundary -- i.e. NOT a continuation
// of a longer label of the same shape. This is narrower than wordChar on
// purpose: a broad Hangul-is-always-a-word-char rule blocks legitimate
// Korean grammatical particles glued directly onto a label ("spk_1이
// 말했다"), which is a regression against the old (boundary-unaware)
// strings.ReplaceAll for exactly the common case of LLM-generated Korean
// summaries/action items. Only the character class that could actually
// extend the specific label shape counts as blocking:
//   - "spk_N": only a following digit continues the number (spk_1 vs the
//     namespaced spk_1000000) -- a following Hangul particle is a boundary.
//   - "화자X": only a following uppercase letter would continue it (no such
//     multi-letter label exists today, but keep the same defensive shape)
//     -- a following digit or Hangul particle is a boundary.
//   - anything else: fall back to the broad wordChar check.
func trailingBoundaryOK(label string, r rune) bool {
	switch {
	case strings.HasPrefix(label, "spk_"):
		return !isDigit(r)
	case isKoreanFallbackLabel(label):
		return !isUpperLetter(r)
	default:
		return !wordChar(r)
	}
}

// ReplaceLabel replaces whole-token occurrences of label in text with
// replacement. "Whole-token" means the character immediately before the
// match (if any) is not a wordChar, and the character immediately after is
// a real boundary per trailingBoundaryOK -- so a label that's a textual
// prefix of another label (e.g. "spk_1" vs the namespaced "spk_1000000") is
// never partially matched, the way a plain strings.ReplaceAll would corrupt
// "spk_1000000" into "<replacement>000000", while a Korean particle
// attached directly to the label ("spk_1이") still matches. Only the label
// itself is consumed per match, not its boundary characters, so two
// occurrences separated by a single character (e.g. "spk_1 spk_1") are both
// found rather than the second one's leading boundary being eaten by the
// first match.
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
			after = trailingBoundaryOK(label, r)
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
