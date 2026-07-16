// Package speaker holds the acoustic speaker-label conventions ("spk_N")
// shared by the summarize Lambda (which produces the labels) and the
// meeting service (which lets users rename them). Keeping the logic here
// -- rather than duplicated per-package -- is what makes label handling in
// both places consistent by construction instead of by convention.
package speaker

import (
	"regexp"
	"strconv"
	"strings"
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

// ReplaceLabel replaces whole-token occurrences of label in text with
// replacement, using word boundaries so a label that is a textual prefix of
// another label (e.g. "spk_1" vs the namespaced "spk_1000000") is never
// partially matched -- a plain strings.ReplaceAll would corrupt
// "spk_1000000" into "<replacement>000000".
func ReplaceLabel(text, label, replacement string) string {
	if label == "" || text == "" {
		return text
	}
	pattern := regexp.MustCompile(`\b` + regexp.QuoteMeta(label) + `\b`)
	return pattern.ReplaceAllString(text, replacement)
}
