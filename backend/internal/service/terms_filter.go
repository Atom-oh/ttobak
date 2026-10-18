package service

import (
	"math"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"

	"github.com/ttobak/backend/internal/model"
)

// Automatic term filter configuration (design A3). The filters are pure and
// deterministic; FilterCandidates is not wired to any caller yet.
const (
	DefaultAutoTermsMinConfidence = 0.85
	AutoTermsMaxNewPerRun         = 15
	autoTermMaxWords              = 6
	transcribePhraseMaxChars      = 256
	transcribeAcronymMinLetters   = 2
	transcribeAcronymMaxLetters   = 6
)

// transcribeKoDottedAcronymsAccepted stays false until rollout acceptance
// item 2 records that ko-KR custom vocabularies accept dotted acronyms.
const transcribeKoDottedAcronymsAccepted = false

// Evidence source types named by an extraction candidate.
const (
	TermSourceTranscript = "transcript"
	TermSourceNotes      = "notes"
	TermSourceDocument   = "document"
)

// TermEvidence is one quote the model cites for a candidate. DocumentID names
// the document when Source is TermSourceDocument.
type TermEvidence struct {
	Source     string
	DocumentID string
	Quote      string
}

// TermCandidate is one model-proposed term before filtering.
type TermCandidate struct {
	DisplayAs  string
	HeardAs    []string
	Type       string
	Phrase     string
	Confidence float64
	Evidence   []TermEvidence
}

// TermSourceDocumentText is one owner-uploaded document's verified text.
type TermSourceDocumentText struct {
	ID   string
	Text string
}

// TermSources is the text the candidates must be grounded in.
type TermSources struct {
	Transcript string
	Notes      string
	Documents  []TermSourceDocumentText
}

// FilteredTerm is a candidate that passed every rule.
type FilteredTerm struct {
	DisplayAs    string
	HeardAs      []string
	Type         string
	TermKey      string
	Phrase       string
	TranscribeKo bool
	TranscribeEn bool
	Confidence   float64
	SourceTypes  []string
}

// TermFilterCounts reports per-rule drops. It holds numbers only, never text,
// so it is safe to log.
type TermFilterCounts struct {
	Input      int `json:"input"`
	Kept       int `json:"kept"`
	Confidence int `json:"droppedConfidence"`
	Shape      int `json:"droppedShape"`
	Blocklist  int `json:"droppedBlocklist"`
	Grounding  int `json:"droppedGrounding"`
	Duplicate  int `json:"droppedDuplicate"`
	Cap        int `json:"droppedCap"`
}

// FilterCandidates applies the deterministic auto-term rules in order:
// confidence, shape (shared validator plus auto-only shape rules), blocklist,
// grounding, duplicate keys, Transcribe phrase derivation and the per-run cap.
// A failing candidate is dropped on its own. A minConfidence outside (0, 1]
// falls back to DefaultAutoTermsMinConfidence.
func FilterCandidates(candidates []TermCandidate, sources TermSources, minConfidence float64) ([]FilteredTerm, TermFilterCounts) {
	if math.IsNaN(minConfidence) || minConfidence <= 0 || minConfidence > 1 {
		minConfidence = DefaultAutoTermsMinConfidence
	}
	counts := TermFilterCounts{Input: len(candidates)}
	idx := newTermSourceIndex(sources)
	type ranked struct {
		term  FilteredTerm
		order int
	}
	byKey := map[string]int{}
	var kept []ranked
	for i, c := range candidates {
		if math.IsNaN(c.Confidence) || c.Confidence < minConfidence || c.Confidence > 1 {
			counts.Confidence++
			continue
		}
		if termsBlocklistedCandidate(c) {
			counts.Blocklist++
			continue
		}
		v, err := ValidateTermInput(c.DisplayAs, c.HeardAs, c.Type)
		if err != nil || !autoTermShapeOK(v.DisplayAs) {
			counts.Shape++
			continue
		}
		sourceTypes, ok := idx.ground(v, c.Evidence)
		if !ok {
			counts.Grounding++
			continue
		}
		phrase, ko, en := transcribePhraseFor(v.DisplayAs, c.Phrase)
		t := FilteredTerm{
			DisplayAs: v.DisplayAs, HeardAs: v.HeardAs, Type: v.Type, TermKey: v.TermKey,
			Phrase: phrase, TranscribeKo: ko, TranscribeEn: en,
			Confidence: c.Confidence, SourceTypes: sourceTypes,
		}
		if prev, dup := byKey[t.TermKey]; dup {
			counts.Duplicate++
			if betterTerm(t, kept[prev].term) {
				kept[prev] = ranked{term: t, order: kept[prev].order}
			}
			continue
		}
		byKey[t.TermKey] = len(kept)
		kept = append(kept, ranked{term: t, order: i})
	}
	sort.SliceStable(kept, func(a, b int) bool {
		switch {
		case betterTerm(kept[a].term, kept[b].term):
			return true
		case betterTerm(kept[b].term, kept[a].term):
			return false
		}
		return kept[a].order < kept[b].order
	})
	if len(kept) > AutoTermsMaxNewPerRun {
		counts.Cap = len(kept) - AutoTermsMaxNewPerRun
		kept = kept[:AutoTermsMaxNewPerRun]
	}
	out := make([]FilteredTerm, 0, len(kept))
	for _, r := range kept {
		out = append(out, r.term)
	}
	counts.Kept = len(out)
	return out, counts
}

// betterTerm ranks by confidence, then by source-type count.
func betterTerm(a, b FilteredTerm) bool {
	if a.Confidence != b.Confidence {
		return a.Confidence > b.Confidence
	}
	return len(a.SourceTypes) > len(b.SourceTypes)
}

// termsBlocklistedCandidate matches the blocklist on the term key and on every
// heardAs (after NFKC, as the shared validator normalizes them).
func termsBlocklistedCandidate(c TermCandidate) bool {
	if termsBlocklist[NormalizeTermKey(normalizeTermText(c.DisplayAs))] {
		return true
	}
	for _, h := range c.HeardAs {
		if termsBlocklist[NormalizeTermKey(normalizeTermText(h))] {
			return true
		}
	}
	return false
}

// TermBlocklisted reports whether a display form or alias is a blocklisted
// common word.
func TermBlocklisted(s string) bool {
	return termsBlocklist[NormalizeTermKey(normalizeTermText(s))]
}

var (
	termDatePatterns = []*regexp.Regexp{
		regexp.MustCompile(`\b\d{4}\s*[./-]\s*\d{1,2}\b`),
		regexp.MustCompile(`\b\d{1,2}\s*[./-]\s*\d{1,2}\s*[./-]\s*\d{2,4}\b`),
		regexp.MustCompile(`\b\d{1,2}:\d{2}\b`),
		regexp.MustCompile(`\d+\s*(년|월|일)`),
		regexp.MustCompile(`(?i)\b(jan|feb|mar|apr|may|jun|jul|aug|sep|sept|oct|nov|dec)[a-z]*\.?\s+\d{1,4}\b`),
		regexp.MustCompile(`(?i)\b\d{1,2}(st|nd|rd|th)?\s+(jan|feb|mar|apr|may|jun|jul|aug|sep|sept|oct|nov|dec)[a-z]*\b`),
	}
	termEmailPattern = regexp.MustCompile(`[^\s@]+@[^\s@]+\.[^\s@]+`)
	termURLPattern   = regexp.MustCompile(`(?i)(\b[a-z][a-z0-9+.-]*://|\bwww\.|\b[a-z0-9-]+\.(com|net|org|io|co|kr|ai|dev|app|me|biz|info|xyz)\b)`)
	// Seven or more digits, optionally separated by spaces, '-' or '.', look
	// like phone, account or card numbers.
	termLongDigitRun = regexp.MustCompile(`\d(?:[\s.-]?\d){6,}`)
)

// autoTermShapeOK applies the auto-only shape rules to a validated displayAs.
func autoTermShapeOK(display string) bool {
	if len(strings.Fields(display)) > autoTermMaxWords {
		return false
	}
	if strings.ContainsRune(display, '@') || termEmailPattern.MatchString(display) || termURLPattern.MatchString(display) {
		return false
	}
	if termLongDigitRun.MatchString(display) {
		return false
	}
	for _, p := range termDatePatterns {
		if p.MatchString(display) {
			return false
		}
	}
	var hangul, latin, other, upper int
	for _, r := range display {
		switch {
		case isHangulSyllable(r):
			hangul++
		case isASCIILetter(r):
			latin++
			if r >= 'A' && r <= 'Z' {
				upper++
			}
		case unicode.IsLetter(r):
			other++
		}
	}
	letters := hangul + latin + other
	switch {
	case letters == 0:
		return false // digits or punctuation only
	case hangul == letters:
		return hangul >= 2
	case latin == letters:
		if latin < 2 {
			return false
		}
		return latin >= 3 || upper == latin
	}
	return true
}

func isHangulSyllable(r rune) bool { return r >= 0xAC00 && r <= 0xD7A3 }
func isASCIILetter(r rune) bool    { return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') }
func isASCIIAlnum(r rune) bool     { return isASCIILetter(r) || (r >= '0' && r <= '9') }

// matchText is source text folded for grounding: NFKC, lowercase, with the
// term-key separators (whitespace, - . _ ·) removed but remembered as word
// boundaries, so "Dynamo DB" matches "dynamodb" without "eks" matching "weeks".
type matchText struct {
	runes     []rune
	sepBefore []bool
}

func newMatchText(s string) matchText {
	var m matchText
	pending := true
	for _, r := range strings.ToLower(norm.NFKC.String(s)) {
		if unicode.IsSpace(r) || r == '-' || r == '.' || r == '_' || r == '·' {
			pending = true
			continue
		}
		m.runes = append(m.runes, r)
		m.sepBefore = append(m.sepBefore, pending)
		pending = false
	}
	return m
}

func (m matchText) boundaryAt(i int) bool { return i >= len(m.runes) || m.sepBefore[i] }

// count returns non-overlapping occurrences of needle. With wordBounds set, a
// match may not extend a Latin/digit run on either side; Hangul matches may
// carry particles (josa).
func (m matchText) count(needle matchText, wordBounds bool) int {
	n := len(needle.runes)
	if n == 0 || n > len(m.runes) {
		return 0
	}
	found := 0
	for i := 0; i+n <= len(m.runes); {
		if !runesEqual(m.runes[i:i+n], needle.runes) || (wordBounds && !m.boundsOK(i, n)) {
			i++
			continue
		}
		found++
		i += n
	}
	return found
}

func (m matchText) boundsOK(i, n int) bool {
	left := i == 0 || m.boundaryAt(i) || !(isASCIIAlnum(m.runes[i-1]) && isASCIIAlnum(m.runes[i]))
	j := i + n
	right := j >= len(m.runes) || m.boundaryAt(j) || !(isASCIIAlnum(m.runes[j-1]) && isASCIIAlnum(m.runes[j]))
	return left && right
}

func runesEqual(a, b []rune) bool {
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

type termSourceIndex struct {
	transcript matchText
	notes      matchText
	docs       map[string]matchText
}

func newTermSourceIndex(s TermSources) termSourceIndex {
	idx := termSourceIndex{transcript: newMatchText(s.Transcript), notes: newMatchText(s.Notes), docs: map[string]matchText{}}
	for _, d := range s.Documents {
		if d.ID != "" {
			idx.docs[d.ID] = newMatchText(d.Text)
		}
	}
	return idx
}

// ground applies the grounding rules and returns the source types in which the
// term itself is attested:
//   - at least one quote; each is a normalized substring of its named source
//     and contains the term or one of its heardAs;
//   - every heardAs occurs in the transcript (its key differs from the term's,
//     which ValidateTermInput already enforces);
//   - displayAs occurs in notes or a document, or at least twice in the
//     transcript;
//   - notes-only evidence must also occur in the transcript (as the term or a
//     heardAs).
func (idx termSourceIndex) ground(v ValidatedTerm, evidence []TermEvidence) ([]string, bool) {
	if len(evidence) == 0 {
		return nil, false
	}
	display := newMatchText(v.DisplayAs)
	forms := []matchText{display}
	for _, h := range v.HeardAs {
		hm := newMatchText(h)
		if idx.transcript.count(hm, true) == 0 {
			return nil, false
		}
		forms = append(forms, hm)
	}
	for _, e := range evidence {
		src, ok := idx.source(e)
		if !ok {
			return nil, false
		}
		quote := newMatchText(e.Quote)
		if len(quote.runes) == 0 || src.count(quote, false) == 0 || !quoteNamesTerm(quote, forms) {
			return nil, false
		}
	}
	inTranscript := idx.transcript.count(display, true)
	inNotes := idx.notes.count(display, true) > 0
	inDocs := false
	for _, d := range idx.docs {
		if d.count(display, true) > 0 {
			inDocs = true
			break
		}
	}
	if !inNotes && !inDocs && inTranscript < 2 {
		return nil, false
	}
	heardInTranscript := len(v.HeardAs) > 0 // every heardAs was checked above
	if inNotes && !inDocs && inTranscript == 0 && !heardInTranscript {
		return nil, false
	}
	var types []string
	if inTranscript > 0 || heardInTranscript {
		types = append(types, TermSourceTranscript)
	}
	if inNotes {
		types = append(types, TermSourceNotes)
	}
	if inDocs {
		types = append(types, TermSourceDocument)
	}
	return types, true
}

func (idx termSourceIndex) source(e TermEvidence) (matchText, bool) {
	switch e.Source {
	case TermSourceTranscript:
		return idx.transcript, true
	case TermSourceNotes:
		return idx.notes, true
	case TermSourceDocument:
		d, ok := idx.docs[e.DocumentID]
		return d, ok
	}
	return matchText{}, false
}

func quoteNamesTerm(quote matchText, forms []matchText) bool {
	for _, f := range forms {
		if quote.count(f, true) > 0 {
			return true
		}
	}
	return false
}

// transcribeEligibility: Hangul-containing terms are ko only; Latin-only terms
// are ko and en; any other script is ineligible.
func transcribeEligibility(display string) (ko, en bool) {
	hasHangul, hasLatin := false, false
	for _, r := range display {
		switch {
		case isHangulSyllable(r):
			hasHangul = true
		case isASCIILetter(r):
			hasLatin = true
		case unicode.IsLetter(r):
			return false, false
		}
	}
	if hasHangul {
		return true, false
	}
	return hasLatin, hasLatin
}

// transcribePhraseFor picks the model phrase when it passes the same validator
// and spells the same term, otherwise derives one. The phrase is kept when it
// is valid for an eligible language; the flags additionally apply the ko-KR
// dotted-acronym gate. An ineligible term keeps Phrase "" and both flags
// false; it remains usable for hints, glossaries and aliases.
func transcribePhraseFor(display, modelPhrase string) (string, bool, bool) {
	koOK, enOK := transcribeEligibility(display)
	if !koOK && !enOK {
		return "", false, false
	}
	check := func(p string) (validKo, validEn bool) {
		return koOK && validTranscribePhrase(p, model.VocabLangKo), enOK && validTranscribePhrase(p, model.VocabLangEn)
	}
	phrase := ""
	if p := strings.TrimSpace(modelPhrase); p != "" && NormalizeTermKey(p) == NormalizeTermKey(display) {
		if vk, ve := check(p); vk || ve {
			phrase = p
		}
	}
	if phrase == "" && koOK {
		phrase = deriveTranscribePhrase(display, model.VocabLangKo)
	}
	if phrase == "" && enOK {
		phrase = deriveTranscribePhrase(display, model.VocabLangEn)
	}
	if phrase == "" {
		return "", false, false
	}
	validKo, validEn := check(phrase)
	ko := validKo && (transcribeKoDottedAcronymsAccepted || !strings.Contains(phrase, "."))
	return phrase, ko, validEn
}

// deriveTranscribePhrase builds an AWS custom-vocabulary Phrase from a display
// form: words and camelCase or script boundaries are joined with '-', and an
// all-caps segment of 2-6 letters is spelled as a dotted acronym
// (DynamoDB -> Dynamo-D.B., EKS -> E.K.S.). It returns "" when the result does
// not satisfy validTranscribePhrase for lang.
func deriveTranscribePhrase(display, lang string) string {
	var segments []string
	for _, word := range strings.FieldsFunc(norm.NFKC.String(display), func(r rune) bool {
		return unicode.IsSpace(r) || r == '-' || r == '_' || r == '.' || r == '·' || r == '/'
	}) {
		for _, seg := range splitPhraseWord(word) {
			segments = append(segments, dotAcronym(seg))
		}
	}
	phrase := strings.Join(segments, "-")
	if !validTranscribePhrase(phrase, lang) {
		return ""
	}
	return phrase
}

type runeClass int

const (
	classOther runeClass = iota
	classLower
	classUpper
	classHangul
)

func classOf(r rune) runeClass {
	switch {
	case r >= 'a' && r <= 'z':
		return classLower
	case r >= 'A' && r <= 'Z':
		return classUpper
	case isHangulSyllable(r):
		return classHangul
	}
	return classOther
}

// splitPhraseWord splits at Latin/Hangul boundaries, lower->Upper boundaries,
// and before the last capital of an all-caps run followed by lowercase
// ("AWSLambda" -> "AWS", "Lambda"). Other characters stay attached so the
// validator can reject them.
func splitPhraseWord(word string) []string {
	rs := []rune(word)
	var out []string
	start := 0
	for i := 1; i < len(rs); i++ {
		prev, cur := classOf(rs[i-1]), classOf(rs[i])
		split := false
		switch {
		case (prev == classHangul) != (cur == classHangul) && prev != classOther && cur != classOther:
			split = true
		case prev == classLower && cur == classUpper:
			split = true
		case prev == classUpper && cur == classUpper && i+1 < len(rs) && classOf(rs[i+1]) == classLower:
			split = true
		}
		if split {
			out = append(out, string(rs[start:i]))
			start = i
		}
	}
	return append(out, string(rs[start:]))
}

func dotAcronym(seg string) string {
	n := utf8.RuneCountInString(seg)
	if n < transcribeAcronymMinLetters || n > transcribeAcronymMaxLetters {
		return seg
	}
	for _, r := range seg {
		if r < 'A' || r > 'Z' {
			return seg
		}
	}
	var b strings.Builder
	for _, r := range seg {
		b.WriteRune(r)
		b.WriteByte('.')
	}
	return b.String()
}

// validTranscribePhrase checks the AWS custom-vocabulary table rules for a
// Phrase: ko-KR charset [A-Za-z\-.가-힣], en-US [A-Za-z'\-.]; no digits or
// spaces; at most 256 characters; no leading '.', apostrophe or '-'; no
// trailing apostrophe or '-'; no "--" or ".."; a period only directly after a
// single letter.
func validTranscribePhrase(p, lang string) bool {
	if p == "" || utf8.RuneCountInString(p) > transcribePhraseMaxChars {
		return false
	}
	rs := []rune(p)
	for _, r := range rs {
		switch {
		case isASCIILetter(r), r == '-', r == '.':
		case r == '\'' && lang == model.VocabLangEn:
		case isHangulSyllable(r) && lang == model.VocabLangKo:
		default:
			return false
		}
	}
	if lang != model.VocabLangKo && lang != model.VocabLangEn {
		return false
	}
	if first := rs[0]; first == '.' || first == '\'' || first == '-' {
		return false
	}
	if last := rs[len(rs)-1]; last == '\'' || last == '-' {
		return false
	}
	if strings.Contains(p, "--") || strings.Contains(p, "..") {
		return false
	}
	for i, r := range rs {
		if r != '.' {
			continue
		}
		// The character before must be a letter that is itself a single-letter
		// unit: preceded by the start, '-', '.', or another single letter's
		// period. "A.B." passes; "AB.C" and "Node.js" fail.
		if i == 0 || !isLetterRune(rs[i-1]) {
			return false
		}
		if i >= 2 && rs[i-2] != '-' && rs[i-2] != '.' {
			return false
		}
	}
	return true
}

func isLetterRune(r rune) bool { return isASCIILetter(r) || isHangulSyllable(r) }
