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

// Transcribe custom vocabulary table rules (ADR-008). The table file
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

type vocabEntry struct {
	Phrase, DisplayAs string
	// spoken marks a row made from a term's Hangul spoken form.
	spoken bool
}

// LiveReplacementsMax caps the spoken-form pairs sent to live caption clients.
const LiveReplacementsMax = 500

// liveReplacements lists the ko-KR table's spoken-form rows as text pairs,
// in table order: a live caption that still spells the spoken form in Hangul
// is rewritten to the term by the client. Same eligibility and precedence as
// the vocabulary, so the two never disagree.
func liveReplacements(layers []vocabLayer) []model.LiveReplacement {
	var out []model.LiveReplacement
	for _, e := range layeredVocabularyEntries(layers, model.VocabLangKo) {
		if !e.spoken {
			continue
		}
		if len(out) == LiveReplacementsMax {
			break
		}
		out = append(out, model.LiveReplacement{Heard: strings.ReplaceAll(e.Phrase, "-", " "), DisplayAs: e.DisplayAs})
	}
	return out
}

// vocabPlan is the desired table for one language.
type vocabPlan struct {
	body        string
	count       int
	fingerprint string
	empty       bool
}

// vocabLayer is one dictionary layer's rows in vocabulary priority order.
type vocabLayer struct {
	name string
	rows []model.DictTerm
}

// vocabularyEntries orders eligible active terms (manual, hits, oldest),
// dedupes by Phrase (first DisplayAs wins) and drops invalid rows singly.
func vocabularyEntries(rows []model.DictTerm, lang string) []vocabEntry {
	return layeredVocabularyEntries([]vocabLayer{{rows: rows}}, lang)
}

// koSpokenMinSyllables keeps one-syllable spoken forms out: they match
// ordinary words too easily.
const koSpokenMinSyllables = 2

// koSpokenPhrases are the ko-KR phrases for a term's spoken forms: each
// heardAs written only in Hangul becomes one more row that displays as the
// term, so a Korean pronunciation of a foreign term ("클로드 코드") is
// recognized and shown in the term's own spelling ("Claude Code"). A Latin
// phrase alone leaves ko-KR to guess the pronunciation. Only terms a person
// added or edited qualify; a term nobody reviewed contributes its phrase only.
func koSpokenPhrases(row model.DictTerm) []string {
	if row.State != model.TermStateActive || (row.Origin != model.TermOriginManual && row.UserEditedAt == "") {
		return nil
	}
	var out []string
	for _, alias := range row.HeardAs {
		phrase := TranscribePhrase(alias)
		syllables := 0
		for _, r := range phrase {
			if r >= 0xAC00 && r <= 0xD7A3 {
				syllables++
			} else if r != '-' {
				syllables = -1
				break
			}
		}
		if syllables >= koSpokenMinSyllables && ValidTranscribePhrase(phrase, model.VocabLangKo) {
			out = append(out, phrase)
		}
	}
	return out
}

// hasVocabularyPhrase reports whether the row's own phrase is a candidate for lang.
func hasVocabularyPhrase(row model.DictTerm, lang string) bool {
	return row.State == model.TermStateActive && row.Phrase != "" &&
		(row.TranscribeKo || (lang == model.VocabLangEn && row.TranscribeEn))
}

// contributesVocabulary reports whether the row can add a row to a vocabulary
// table, so a write to it needs a rebuild.
func contributesVocabulary(row model.DictTerm) bool {
	return hasVocabularyPhrase(row, model.VocabLangKo) || hasVocabularyPhrase(row, model.VocabLangEn) || len(koSpokenPhrases(row)) > 0
}

// eligibleVocabularyRows keeps active rows with the language's recognition
// flag or, for ko-KR, a spoken form, ordered manual first, then hits, then oldest.
func eligibleVocabularyRows(rows []model.DictTerm, lang string) []model.DictTerm {
	eligible := make([]model.DictTerm, 0, len(rows))
	for _, row := range rows {
		if hasVocabularyPhrase(row, lang) || (lang == model.VocabLangKo && len(koSpokenPhrases(row)) > 0) {
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
	return eligible
}

// layeredVocabularyEntries concatenates the layers' eligible rows in layer
// order; an eligible termKey (then Phrase) already taken by an earlier layer
// is skipped, so the first layer wins. For ko-KR each term's spoken forms
// follow its own row; a spoken form never takes a phrase that is some term's
// own, whatever the order.
func layeredVocabularyEntries(layers []vocabLayer, lang string) []vocabEntry {
	eligible := make([][]model.DictTerm, len(layers))
	own := map[string]bool{}
	for i, layer := range layers {
		eligible[i] = eligibleVocabularyRows(layer.rows, lang)
		for _, row := range eligible[i] {
			if hasVocabularyPhrase(row, lang) {
				own[strings.ToLower(TranscribePhrase(row.Phrase))] = true
			}
		}
	}
	seenKey, seen := map[string]bool{}, map[string]bool{}
	var out []vocabEntry
	for i := range layers {
		for _, row := range eligible[i] {
			if row.TermKey != "" && seenKey[row.TermKey] {
				continue
			}
			display := strings.TrimSpace(row.DisplayAs)
			if !validVocabDisplay(display) {
				continue
			}
			added := false
			if phrase := TranscribePhrase(row.Phrase); hasVocabularyPhrase(row, lang) {
				if key := strings.ToLower(phrase); !seen[key] && ValidTranscribePhrase(phrase, lang) {
					seen[key], added = true, true
					out = append(out, vocabEntry{Phrase: phrase, DisplayAs: display})
				}
			}
			if lang == model.VocabLangKo {
				for _, phrase := range koSpokenPhrases(row) {
					if key := strings.ToLower(phrase); !seen[key] && !own[key] {
						seen[key], added = true, true
						out = append(out, vocabEntry{Phrase: phrase, DisplayAs: display, spoken: true})
					}
				}
			}
			if added && row.TermKey != "" {
				seenKey[row.TermKey] = true
			}
		}
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
	return planVocabularyLayers([]vocabLayer{{rows: rows}}, lang)
}

// planVocabularyLayers renders the merged table (Personal manual, Accounts,
// Personal auto, Common), cut at whole rows under VocabMaxRows and
// VocabMaxBytes; the fingerprint covers the merged table.
func planVocabularyLayers(layers []vocabLayer, lang string) vocabPlan {
	body, n := RenderVocabularyTSV(layeredVocabularyEntries(layers, lang))
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
