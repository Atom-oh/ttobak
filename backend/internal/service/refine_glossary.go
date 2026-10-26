package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

// MaxGlossaryTerms caps the glossary one meeting's refinement receives.
const MaxGlossaryTerms = 200

// glossaryRefineRule is the system-prompt rule for glossary corrections.
const glossaryRefineRule = "Glossary terms may have been mis-heard as similar-sounding words. Replace a span only when it is a clear mis-hearing of a glossary term in context. Never add a glossary term where no similar-sounding span exists. Never paraphrase. Return each replacement in `corrections`."

const refineGlossaryOutputSpec = `Output ONLY a JSON object: {"segments":[...],"corrections":[...]}.
Each segments element: {"speaker":"spk_0","text":"정제된 문장","startTime":0.0,"endTime":6.5,"sourceIds":[0,1]}
Each corrections element: {"from":"the replaced span exactly as in the input line","to":"the glossary display form exactly as listed","segment":0}, where segment is the sourceId of the input line that contains from. Use "corrections":[] when nothing was replaced.
Do NOT include any text outside the JSON object. No markdown fences.`

const (
	// glossaryMaxSpanRunes bounds a correction's from span.
	glossaryMaxSpanRunes = 80
	// glossaryMaxClaims bounds the corrections one chunk may claim.
	glossaryMaxClaims = 200
)

// GlossaryTerm is one dictionary term offered to transcript refinement.
// TermKey and Layer (the term's partition: USER#, ACCOUNT# or DICT#COMMON)
// let a correction be attributed back to the term.
type GlossaryTerm struct {
	DisplayAs string
	HeardAs   []string
	TermKey   string
	Layer     string
}

// refineGlossary is a meeting's glossary prepared for prompts and checks.
type refineGlossary struct {
	terms     []GlossaryTerm
	byDisplay map[string]GlossaryTerm
	// longer lists, per display form, the other glossary display forms that
	// contain it ("Alpha" -> "AlphaGrid"); count skips occurrences inside them.
	longer map[string][]string
}

// newRefineGlossary keeps terms whose display form is a valid single-line
// term; nil when none remain, which selects the plain refinement path.
func newRefineGlossary(terms []GlossaryTerm) *refineGlossary {
	g := &refineGlossary{byDisplay: map[string]GlossaryTerm{}}
	for _, term := range terms {
		if !promptSafeTerm(term.DisplayAs) || term.TermKey == "" || g.byDisplay[term.DisplayAs].DisplayAs != "" {
			continue
		}
		heard := make([]string, 0, len(term.HeardAs))
		for _, alias := range term.HeardAs {
			if promptSafeTerm(alias) && len(heard) < TermMaxHeardAs {
				heard = append(heard, alias)
			}
		}
		term.HeardAs = heard
		g.terms = append(g.terms, term)
		g.byDisplay[term.DisplayAs] = term
		if len(g.terms) == MaxGlossaryTerms {
			break
		}
	}
	g.longer = map[string][]string{}
	for _, a := range g.terms {
		for _, b := range g.terms {
			if a.DisplayAs != b.DisplayAs && strings.Contains(b.DisplayAs, a.DisplayAs) {
				g.longer[a.DisplayAs] = append(g.longer[a.DisplayAs], b.DisplayAs)
			}
		}
	}
	if len(g.terms) == 0 {
		return nil
	}
	return g
}

func promptSafeTerm(s string) bool {
	return s != "" && utf8.RuneCountInString(s) <= TermMaxRunes && checkTermRunes("term", s) == nil
}

// RefineTranscriptWithGlossary is RefineTranscriptWithQuality plus silent
// glossary correction (L3): the model may replace clear mis-hearings of
// glossary terms and lists each replacement, which Go verifies before
// recording it (at most model.MaxTranscriptCorrections per meeting). An
// empty glossary sends exactly the plain refinement requests and returns nil
// corrections; a glossary run returns a non-nil slice, so callers can tell
// which path ran.
func (s *BedrockService) RefineTranscriptWithGlossary(ctx context.Context, segments []WhisperSegment, glossary []GlossaryTerm) (string, []RefinedSegment, *model.TranscriptQuality, []model.TranscriptCorrection, error) {
	prepared := newRefineGlossary(glossary)
	if prepared == nil {
		text, refined, quality, err := s.RefineTranscriptWithQuality(ctx, segments)
		return text, refined, quality, nil, err
	}
	return s.refineTranscript(ctx, segments, prepared)
}

// buildGlossaryRefineSystemPrompt is the plain refinement prompt plus task 7
// (the glossary rule and terms) and the object output contract.
func buildGlossaryRefineSystemPrompt(preserveSpeakers bool, g *refineGlossary) string {
	var sb strings.Builder
	sb.WriteString(refineSystemPromptTasks(preserveSpeakers))
	sb.WriteString("\n7. **Fix glossary mis-hearings**: ")
	sb.WriteString(glossaryRefineRule)
	sb.WriteString("\n   Glossary (display form, then known mis-hearings):\n")
	for _, term := range g.terms {
		sb.WriteString("   - ")
		sb.WriteString(term.DisplayAs)
		if len(term.HeardAs) > 0 {
			sb.WriteString(" (heard as: ")
			sb.WriteString(strings.Join(term.HeardAs, ", "))
			sb.WriteString(")")
		}
		sb.WriteString("\n")
	}
	sb.WriteString("\n")
	sb.WriteString(refineSourceIDNote)
	sb.WriteString("\n")
	sb.WriteString(refineGlossaryOutputSpec)
	return sb.String()
}

// glossaryWireCorrection is one replacement the model claims; Segment is the
// chunk-local sourceId of the input line holding From.
type glossaryWireCorrection struct {
	From    string `json:"from"`
	To      string `json:"to"`
	Segment *int   `json:"segment"`
}

// parseGlossaryRefinement accepts the object contract, or the plain array
// (no corrections), validates the segments exactly like the plain path and
// returns how many source lines each refined segment covers.
func parseGlossaryRefinement(text string, input []WhisperSegment) ([]RefinedSegment, []int, []glossaryWireCorrection, error) {
	segmentsJSON := text
	var claimed []glossaryWireCorrection
	if strings.HasPrefix(strings.TrimSpace(text), "{") {
		var wire struct {
			Segments    json.RawMessage          `json:"segments"`
			Corrections []glossaryWireCorrection `json:"corrections"`
		}
		decoder := json.NewDecoder(strings.NewReader(text))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&wire) != nil {
			return nil, nil, nil, errRefinementParse
		}
		if decoder.Decode(new(any)) != io.EOF || len(wire.Segments) == 0 || len(wire.Corrections) > glossaryMaxClaims {
			return nil, nil, nil, errRefinementSchema
		}
		segmentsJSON, claimed = string(wire.Segments), wire.Corrections
	}
	refined, err := validateRefinementJSON(segmentsJSON, input)
	if err != nil {
		return nil, nil, nil, err
	}
	var sources []struct {
		SourceIDs []int `json:"sourceIds"`
	}
	if json.Unmarshal([]byte(segmentsJSON), &sources) != nil || len(sources) != len(refined) {
		return nil, nil, nil, errRefinementSchema
	}
	counts := make([]int, len(sources))
	for i, source := range sources {
		counts[i] = len(source.SourceIDs)
	}
	return refined, counts, claimed, nil
}

// verify is the authority over the model's claimed corrections. A claim is
// recorded only when From occurs in the original line it names, To is
// exactly a glossary display form, and the refined segment covering that
// line both lost an occurrence of From and gained one of To. Anything else
// is dropped as a record; verify never edits the model's text.
// Segment in the result indexes refined.
//
// Invented glossary terms never survive (R11): afterwards, every occurrence
// of a glossary display form a refined segment gained over its source lines
// must be backed by a kept claim, and each source span backs at most one
// occurrence. A claim consumes min(From occurrences the segment lost and no
// claim used yet, To occurrences still unbacked, free From spans in the
// segment's source lines, its named line first); a free span overlaps no
// span an earlier claim consumed, so two claims on one span cannot both
// pass, while one claim may back the same fix on several merged lines.
// Counting by occurrences is exact only when the claimed Froms of a segment
// are independent, so a segment whose distinct claimed Froms overlap in its
// source lines (or contain one another) is ambiguous and reads as invented.
// Display forms are counted outside longer glossary forms that contain them,
// so a fix to "AlphaGrid" never reads as a gained "Alpha", and a From inside
// its To (or a To inside its From) is counted outside the other. When
// invented is true the caller re-runs the chunk on the plain prompt.
func (g *refineGlossary) verify(input []WhisperSegment, refined []RefinedSegment, counts []int, claimed []glossaryWireCorrection) (kept []model.TranscriptCorrection, invented bool) {
	owner := make([]int, 0, len(input))
	lines := make([][]int, len(refined)) // refined index -> its source lines
	for out, n := range counts {
		for k := 0; k < n; k++ {
			lines[out] = append(lines[out], len(owner))
			owner = append(owner, out)
		}
	}
	if len(owner) != len(input) {
		return nil, true
	}
	covered := func(out int) string {
		var sb strings.Builder
		for _, source := range lines[out] {
			sb.WriteString(input[source].Text)
			sb.WriteString("\n")
		}
		return sb.String()
	}
	froms := map[int][]string{} // refined index -> distinct claimed Froms
	for _, claim := range claimed {
		if claim.Segment == nil || *claim.Segment < 0 || *claim.Segment >= len(input) || strings.TrimSpace(claim.From) == "" {
			continue
		}
		if _, ok := g.byDisplay[claim.To]; !ok {
			continue // never recorded, so it cannot make another claim ambiguous
		}
		out := owner[*claim.Segment]
		if !slices.Contains(froms[out], claim.From) {
			froms[out] = append(froms[out], claim.From)
		}
	}
	for out, list := range froms {
		if ambiguousFroms(covered(out), list) {
			return nil, true
		}
	}
	seen := map[model.TranscriptCorrection]bool{}
	backed := map[int]map[string]int{}   // refined index -> To -> occurrences
	consumed := map[int]map[string]int{} // refined index -> From -> lost occurrences used
	spans := map[int][][2]int{}          // input line -> consumed byte ranges
	for _, claim := range claimed {
		if claim.Segment == nil || *claim.Segment < 0 || *claim.Segment >= len(input) {
			continue
		}
		from, to := claim.From, claim.To
		if strings.TrimSpace(from) == "" || to == "" || from == to {
			continue
		}
		named := *claim.Segment
		out := owner[named]
		fromOK := utf8.RuneCountInString(from) <= glossaryMaxSpanRunes && strings.Contains(input[named].Text, from)
		term, termOK := g.byDisplay[to]
		original := covered(out)
		if !fromOK || !termOK {
			continue
		}
		// A From inside To ("Databrick" -> "Databricks") is counted outside To,
		// and a To inside From outside From, so neither hides the replacement.
		var fromOutside, toOutside []string
		if strings.Contains(to, from) {
			fromOutside = []string{to}
		}
		if strings.Contains(from, to) {
			toOutside = []string{from}
		}
		fromOrig, fromRef := countOutside(original, from, fromOutside), countOutside(refined[out].Text, from, fromOutside)
		toOrig, toRef := countOutside(original, to, append(toOutside, g.longer[to]...)), g.count(refined[out].Text, to)
		if toRef <= toOrig || fromRef >= fromOrig {
			continue
		}
		if backed[out] == nil {
			backed[out], consumed[out] = map[string]int{}, map[string]int{}
		}
		lost := fromOrig - fromRef - consumed[out][from]
		unbacked := toRef - toOrig - backed[out][to]
		type lineSpan struct {
			line int
			span [2]int
		}
		var free []lineSpan
		for _, line := range append([]int{named}, lines[out]...) {
			if line == named && len(free) > 0 {
				continue // the named line is listed first
			}
			for _, span := range freeSpans(input[line].Text, from, append(occurrences(input[line].Text, fromOutside), spans[line]...)) {
				free = append(free, lineSpan{line, span})
			}
		}
		credit := min(lost, unbacked, len(free))
		if credit <= 0 {
			continue // its spans already back other claims
		}
		for _, f := range free[:credit] {
			spans[f.line] = append(spans[f.line], f.span)
		}
		consumed[out][from] += credit
		backed[out][to] += credit
		if record := (model.TranscriptCorrection{From: from, To: to, TermKey: term.TermKey, Segment: out}); !seen[record] {
			seen[record] = true
			kept = append(kept, record)
		}
	}
	for out := range refined {
		for _, term := range g.terms {
			// The baseline leaves out source occurrences inside spans a claim
			// consumed ("Kubernetes" inside a replaced "Kubernetess"): those
			// were replaced, so they cannot also stand for a kept occurrence.
			baseline := 0
			for _, line := range lines[out] {
				text := input[line].Text
				baseline += len(freeSpans(text, term.DisplayAs, append(occurrences(text, g.longer[term.DisplayAs]), spans[line]...)))
			}
			if g.count(refined[out].Text, term.DisplayAs)-baseline > backed[out][term.DisplayAs] {
				return nil, true
			}
		}
	}
	return kept, false
}

// ambiguousFroms reports whether two distinct Froms contain one another or
// have overlapping occurrences in text.
func ambiguousFroms(text string, froms []string) bool {
	for i, a := range froms {
		for _, b := range froms[i+1:] {
			if strings.Contains(a, b) || strings.Contains(b, a) {
				return true
			}
			for _, x := range occurrences(text, []string{a}) {
				for _, y := range occurrences(text, []string{b}) {
					if x[0] < y[1] && y[0] < x[1] {
						return true
					}
				}
			}
		}
	}
	return false
}

// count is the number of occurrences of display in text that do not lie
// inside an occurrence of a longer glossary display form containing it.
func (g *refineGlossary) count(text, display string) int {
	return countOutside(text, display, g.longer[display])
}

// countOutside counts s's occurrences in text that overlap no occurrence of
// any of the longer strings.
func countOutside(text, s string, longer []string) int {
	return len(freeSpans(text, s, occurrences(text, longer)))
}

// occurrences returns the byte ranges of every (possibly overlapping)
// occurrence of each of strs in text.
func occurrences(text string, strs []string) [][2]int {
	var out [][2]int
	for _, s := range strs {
		for start := 0; s != ""; {
			i := strings.Index(text[start:], s)
			if i < 0 {
				break
			}
			out = append(out, [2]int{start + i, start + i + len(s)})
			start += i + 1
		}
	}
	return out
}

// freeSpans returns the byte ranges of from's non-overlapping occurrences in
// line that overlap none of the used ranges, in order.
func freeSpans(line, from string, used [][2]int) [][2]int {
	var free [][2]int
	for start := 0; ; {
		i := strings.Index(line[start:], from)
		if i < 0 {
			return free
		}
		span := [2]int{start + i, start + i + len(from)}
		overlaps := false
		for _, u := range used {
			if span[0] < u[1] && u[0] < span[1] {
				overlaps = true
				break
			}
		}
		if !overlaps {
			free = append(free, span)
		}
		start = span[1]
	}
}

// rebaseCorrections shifts chunk-local Segment indexes by offset.
func rebaseCorrections(corrections []model.TranscriptCorrection, offset int) []model.TranscriptCorrection {
	for i := range corrections {
		corrections[i].Segment += offset
	}
	return corrections
}

// GlossaryForMeeting returns the refinement glossary for one meeting owned
// by ownerID: Personal, then the meeting's Account (only while the owner is
// a current member), then Common; active rows only, first layer wins per
// term key, at most MaxGlossaryTerms. Common rows a user edited
// (userEditedAt set) are left out (R17): anyone may edit Common (L2), so an
// edit may bias vocabulary and Whisper but never drives a silent correction.
// An Account layer that cannot be authorized or read is dropped and logged; a
// Personal or Common read error is returned.
func (s *DictionaryService) GlossaryForMeeting(ctx context.Context, ownerID, accountID string) ([]GlossaryTerm, error) {
	if ownerID == "" {
		return nil, nil
	}
	dict, err := s.repo.GetDictionary(ctx, ownerID)
	if err != nil {
		return nil, fmt.Errorf("failed to get dictionary: %w", err)
	}
	personal, err := s.repo.ListDictTerms(ctx, model.PrefixUser+ownerID)
	if err != nil {
		return nil, fmt.Errorf("failed to get dictionary terms: %w", err)
	}
	var out []GlossaryTerm
	seen := map[string]bool{}
	add := func(term GlossaryTerm) {
		if len(out) < MaxGlossaryTerms && term.TermKey != "" && term.DisplayAs != "" && !seen[term.TermKey] {
			seen[term.TermKey] = true
			out = append(out, term)
		}
	}
	for _, t := range unionLegacyTerms(dict, personal) {
		add(GlossaryTerm{DisplayAs: termDisplay(t.DisplayAs, t.Phrase), HeardAs: splitSoundsLike(t.SoundsLike),
			TermKey: legacyTermKey(t), Layer: model.PrefixUser + ownerID})
	}
	if accountID != "" {
		if err := s.requireMember(ctx, accountID, ownerID); err != nil {
			log.Printf("transcript glossary: skipping account layer for user %s: %v", ownerID, err)
		} else if rows, err := s.repo.ListDictTerms(ctx, model.PrefixAccount+accountID); err != nil {
			log.Printf("transcript glossary: account terms read failed for user %s: %v", ownerID, err)
		} else {
			for _, row := range activeRows(rows) {
				add(glossaryTermOf(row))
			}
		}
	}
	common, err := s.repo.ListDictTerms(ctx, model.PrefixDictCommon)
	if err != nil {
		return nil, fmt.Errorf("failed to get common dictionary terms: %w", err)
	}
	for _, row := range activeRows(common) {
		if row.UserEditedAt == "" {
			add(glossaryTermOf(row))
		}
	}
	return out, nil
}

func glossaryTermOf(row model.DictTerm) GlossaryTerm {
	return GlossaryTerm{DisplayAs: termDisplay(row.DisplayAs, row.Phrase), HeardAs: append([]string(nil), row.HeardAs...), TermKey: row.TermKey, Layer: row.PK}
}

// activeRows lists active rows oldest first, like activeDisplays.
func activeRows(rows []model.DictTerm) []model.DictTerm {
	active := make([]model.DictTerm, 0, len(rows))
	for _, row := range rows {
		if row.State == model.TermStateActive {
			active = append(active, row)
		}
	}
	sort.SliceStable(active, func(i, j int) bool {
		if active[i].AddedAt != active[j].AddedAt {
			return active[i].AddedAt < active[j].AddedAt
		}
		return active[i].SK < active[j].SK
	})
	return active
}

func splitSoundsLike(soundsLike string) []string {
	var out []string
	for _, alias := range strings.Split(soundsLike, ",") {
		if alias = strings.TrimSpace(alias); alias != "" {
			out = append(out, alias)
		}
	}
	return out
}

// TermCorrectionStore backs heardAs feedback (repository.DictionaryRepository):
// a per-term counter of the distinct meetings in which one heard form was
// corrected to the term, and an append that is not a user edit.
type TermCorrectionStore interface {
	RecordTermCorrection(ctx context.Context, pk, termKey, heardKey, meetingID string, expiresAt int64, member *repository.MembershipCheck) (int, error)
	GetDictTerm(ctx context.Context, pk, termKey string) (*model.DictTerm, error)
	AppendHeardAs(ctx context.Context, pk, termKey, phrase, now string, expectedVersion int64, member *repository.MembershipCheck) error
}

// SetCorrectionStore enables heardAs feedback from transcript corrections;
// without it LearnFromCorrections does nothing.
func (s *DictionaryService) SetCorrectionStore(store TermCorrectionStore) {
	s.correctionStore = store
}

// correctionMinMeetings is how many distinct meetings must correct the same
// heard form to the same term before it becomes one of the term's heardAs.
const correctionMinMeetings = 2

// LearnFromCorrections counts each (term, heard form) of one meeting's
// corrections in the term's own partition and, once correctionMinMeetings
// distinct meetings agree, appends the heard form to the term's heardAs
// (an Account term under the owner's membership check). The append is not a
// user edit, so automatic merges keep applying to the term. Common terms are
// skipped entirely, with no counter row: Common is news-only and visible to
// every user (rulings R6, R12). Failures are logged and never fail the
// caller. It returns how many heardAs entries were added.
func (s *DictionaryService) LearnFromCorrections(ctx context.Context, ownerID, accountID, meetingID string, glossary []GlossaryTerm, corrections []model.TranscriptCorrection) int {
	if s.correctionStore == nil || ownerID == "" || meetingID == "" || len(corrections) == 0 {
		return 0
	}
	byKey := make(map[string]GlossaryTerm, len(glossary))
	for _, term := range glossary {
		if _, ok := byKey[term.TermKey]; !ok {
			byKey[term.TermKey] = term
		}
	}
	expiresAt := s.now().Unix() + model.TermCorrectionTTLSeconds
	seen := map[string]bool{}
	added, skipped := 0, 0
	for _, correction := range corrections {
		term, ok := byKey[correction.TermKey]
		heardKey := NormalizeTermKey(correction.From)
		if !ok || heardKey == "" || term.Layer == model.PrefixDictCommon || seen[term.Layer+"#"+term.TermKey+"#"+heardKey] {
			continue
		}
		seen[term.Layer+"#"+term.TermKey+"#"+heardKey] = true
		if knownHeardAs(term.HeardAs, heardKey) {
			continue
		}
		member, err := s.correctionMember(ctx, term.Layer, ownerID, accountID)
		if err != nil {
			skipped++
			continue
		}
		meetings, err := s.correctionStore.RecordTermCorrection(ctx, term.Layer, term.TermKey, heardKey, meetingID, expiresAt, member)
		if err != nil {
			log.Printf("transcript correction feedback: counter write failed for user %s: %v", ownerID, err)
			continue
		}
		if meetings < correctionMinMeetings {
			continue
		}
		if s.appendHeardAs(ctx, term.Layer, term.TermKey, correction.From, heardKey, ownerID, member) {
			added++
		}
	}
	if added > 0 || skipped > 0 {
		log.Printf("transcript correction feedback: user=%s meeting=%s heardAsAdded=%d skipped=%d", ownerID, meetingID, added, skipped)
	}
	return added
}

// correctionMember authorizes feedback for one term partition: the owner's
// own Personal layer, or the meeting's Account while the owner is a member.
func (s *DictionaryService) correctionMember(ctx context.Context, pk, ownerID, accountID string) (*repository.MembershipCheck, error) {
	switch {
	case pk == model.PrefixUser+ownerID:
		return nil, nil
	case accountID != "" && pk == model.PrefixAccount+accountID:
		if err := s.requireMember(ctx, accountID, ownerID); err != nil {
			return nil, err
		}
		return &repository.MembershipCheck{AccountID: accountID, UserID: ownerID}, nil
	}
	return nil, ErrForbidden
}

func knownHeardAs(heardAs []string, heardKey string) bool {
	for _, alias := range heardAs {
		if NormalizeTermKey(alias) == heardKey {
			return true
		}
	}
	return false
}

// appendHeardAs adds from to an active term's heardAs under its version
// guard, unless it is already there, the term is user-edited (a user's alias
// choice is never overridden) or the aliases would fail validation.
func (s *DictionaryService) appendHeardAs(ctx context.Context, pk, termKey, from, heardKey, ownerID string, member *repository.MembershipCheck) bool {
	row, err := s.correctionStore.GetDictTerm(ctx, pk, termKey)
	if err != nil {
		log.Printf("transcript correction feedback: term read failed for user %s: %v", ownerID, err)
		return false
	}
	if row == nil || row.State != model.TermStateActive || row.UserEditedAt != "" || knownHeardAs(row.HeardAs, heardKey) {
		return false
	}
	next, err := validateHeardAs(append(append([]string(nil), row.HeardAs...), from), NormalizeTermKey(termDisplay(row.DisplayAs, row.Phrase)))
	if err != nil || len(next) == 0 || NormalizeTermKey(next[len(next)-1]) != heardKey {
		return false
	}
	if err := s.correctionStore.AppendHeardAs(ctx, pk, termKey, next[len(next)-1], s.stamp(), row.Version, member); err != nil {
		log.Printf("transcript correction feedback: heardAs update failed for user %s: %v", ownerID, err)
		return false
	}
	return true
}
