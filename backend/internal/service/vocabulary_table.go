package service

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
	"unicode/utf8"

	smithy "github.com/aws/smithy-go"
	"golang.org/x/text/unicode/norm"

	"github.com/ttobak/backend/internal/model"
)

// Transcribe custom vocabulary table rules (ADR-008 amendment). The table file
// has four columns; SoundsLike and IPA stay empty.
const (
	VocabTSVHeader = "Phrase\tSoundsLike\tIPA\tDisplayAs\n"
	VocabMaxRows   = 1500
	VocabMaxBytes  = 48 << 10
	// VocabEnMinTerms is the en-US build threshold; fewer terms build nothing.
	VocabEnMinTerms = 5
	vocabDisplayMax = 256

	// koDottedAcronymsAccepted stays false until rollout acceptance records
	// that ko-KR accepts dotted acronyms (E.K.S.); until then such ko phrases
	// are dropped so one row cannot fail the whole build.
	koDottedAcronymsAccepted = false
)

func isASCIILetter(r rune) bool { return (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') }

// TranscribePhrase is the minimal phrase derivation: NFKC, trimmed, and
// space runs joined with "-" (Transcribe phrases cannot contain spaces).
func TranscribePhrase(raw string) string {
	return strings.Join(strings.Fields(norm.NFKC.String(raw)), "-")
}

// ValidTranscribePhrase applies the table rules: ko-KR [A-Za-z-.] plus Hangul
// syllables, en-US [A-Za-z'-.]; at most 256; no leading . ' -, no trailing
// ' -, no "--" or ".."; periods only after a single letter (E.K.S.).
func ValidTranscribePhrase(phrase, lang string) bool {
	runes := []rune(phrase)
	if len(runes) == 0 || len(runes) > TermPhraseMaxRunes {
		return false
	}
	for i, r := range runes {
		switch {
		case isASCIILetter(r), r == '-', r == '.':
		case r == '\'' && lang == model.VocabLangEn:
		case r >= 0xAC00 && r <= 0xD7A3 && lang == model.VocabLangKo:
		default:
			return false
		}
		if i > 0 && (r == '-' || r == '.') && runes[i-1] == r {
			return false
		}
		if r == '.' {
			if i == 0 || !isASCIILetter(runes[i-1]) || (i >= 2 && runes[i-2] != '-' && runes[i-2] != '.') {
				return false
			}
			if lang == model.VocabLangKo && !koDottedAcronymsAccepted {
				return false
			}
		}
	}
	first, last := runes[0], runes[len(runes)-1]
	return first != '.' && first != '\'' && first != '-' && last != '\'' && last != '-'
}

// validVocabDisplay accepts an optional DisplayAs without control, format or
// line-separator characters (a tab would break the table).
func validVocabDisplay(s string) bool {
	return checkTermRunes("displayAs", s) == nil && utf8.RuneCountInString(s) <= vocabDisplayMax
}

type vocabEntry struct{ Phrase, DisplayAs string }

// vocabPlan is the desired table for one language.
type vocabPlan struct {
	body        string
	count       int
	fingerprint string
	empty       bool
}

// vocabularyEntries orders eligible active terms (manual, hits, oldest),
// dedupes by Phrase (first DisplayAs wins) and drops invalid rows singly.
func vocabularyEntries(rows []model.DictTerm, lang string) []vocabEntry {
	eligible := make([]model.DictTerm, 0, len(rows))
	for _, row := range rows {
		if row.State != model.TermStateActive || row.Phrase == "" {
			continue
		}
		if row.TranscribeKo || (lang == model.VocabLangEn && row.TranscribeEn) {
			eligible = append(eligible, row)
		}
	}
	sort.SliceStable(eligible, func(i, j int) bool {
		a, b := eligible[i], eligible[j]
		if (a.Origin == model.TermOriginManual) != (b.Origin == model.TermOriginManual) {
			return a.Origin == model.TermOriginManual
		}
		if a.Hits != b.Hits {
			return a.Hits > b.Hits
		}
		if a.AddedAt != b.AddedAt {
			return a.AddedAt < b.AddedAt
		}
		return a.SK < b.SK
	})
	seen := map[string]bool{}
	var out []vocabEntry
	for _, row := range eligible {
		phrase := TranscribePhrase(row.Phrase)
		display := strings.TrimSpace(row.DisplayAs)
		key := strings.ToLower(phrase)
		if seen[key] || !ValidTranscribePhrase(phrase, lang) || !validVocabDisplay(display) {
			continue
		}
		seen[key] = true
		out = append(out, vocabEntry{Phrase: phrase, DisplayAs: display})
	}
	return out
}

// RenderVocabularyTSV writes the four-column table, stopping at
// VocabMaxRows rows or VocabMaxBytes bytes. It returns the rows written.
func RenderVocabularyTSV(entries []vocabEntry) (string, int) {
	var b strings.Builder
	b.WriteString(VocabTSVHeader)
	n := 0
	for _, e := range entries {
		line := e.Phrase + "\t\t\t" + e.DisplayAs + "\n"
		if n == VocabMaxRows || b.Len()+len(line) > VocabMaxBytes {
			break
		}
		b.WriteString(line)
		n++
	}
	return b.String(), n
}

func planVocabulary(rows []model.DictTerm, lang string) vocabPlan {
	body, n := RenderVocabularyTSV(vocabularyEntries(rows, lang))
	min := 1
	if lang == model.VocabLangEn {
		min = VocabEnMinTerms
	}
	if n < min {
		return vocabPlan{empty: true}
	}
	sum := sha256.Sum256([]byte(body))
	return vocabPlan{body: body, count: n, fingerprint: hex.EncodeToString(sum[:16])}
}

type vocabErrKind int

const (
	vocabErrOther vocabErrKind = iota
	vocabErrNotFound
	vocabErrBadRequest
	vocabErrConflict
	vocabErrLimit
)

// classifyVocabularyError maps Transcribe errors by error code.
func classifyVocabularyError(err error) vocabErrKind {
	var apiErr smithy.APIError
	if !errors.As(err, &apiErr) {
		return vocabErrOther
	}
	return map[string]vocabErrKind{"NotFoundException": vocabErrNotFound, "BadRequestException": vocabErrBadRequest,
		"ConflictException": vocabErrConflict, "LimitExceededException": vocabErrLimit}[apiErr.ErrorCode()]
}

// vocabularyMissing: NotFound, or the BadRequest returned for an unknown name
// (a wrong guess makes CreateVocabulary fail safely with ConflictException).
func vocabularyMissing(err error) bool {
	kind := classifyVocabularyError(err)
	return kind == vocabErrNotFound || kind == vocabErrBadRequest
}
