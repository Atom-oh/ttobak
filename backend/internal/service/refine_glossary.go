package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
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
// is dropped as a record. Segment in the result indexes refined.
//
// The model has already written its refined text, so a dropped claim can
// leave its replacement behind. When From is a real original span but To is
// not a glossary form, the replacement is reverted to From only if To occurs
// exactly once in the refined segment and nowhere in the lines it covers
// (so the span is located unambiguously and was introduced by the model).
//
// Invented glossary terms never survive (R11): afterwards, every occurrence
// of a glossary display form a refined segment gained over its source lines
// must be backed by a kept record (each record backs as many occurrences as
// its From lost). Otherwise invented is true and the caller re-runs the
// chunk on the plain prompt.
func (g *refineGlossary) verify(input []WhisperSegment, refined []RefinedSegment, counts []int, claimed []glossaryWireCorrection) (kept []model.TranscriptCorrection, invented bool) {
	owner := make([]int, 0, len(input))
	for out, n := range counts {
		for k := 0; k < n; k++ {
			owner = append(owner, out)
		}
	}
	if len(owner) != len(input) {
		return nil, true
	}
	covered := func(out int) string {
		var sb strings.Builder
		for source, o := range owner {
			if o == out {
				sb.WriteString(input[source].Text)
				sb.WriteString("\n")
			}
		}
		return sb.String()
	}
	seen := map[model.TranscriptCorrection]bool{}
	backed := map[int]map[string]int{} // refined index -> To -> occurrences
	for _, claim := range claimed {
		if claim.Segment == nil || *claim.Segment < 0 || *claim.Segment >= len(input) {
			continue
		}
		from, to := claim.From, claim.To
		if strings.TrimSpace(from) == "" || to == "" || from == to {
			continue
		}
		out := owner[*claim.Segment]
		fromOK := utf8.RuneCountInString(from) <= glossaryMaxSpanRunes && strings.Contains(input[*claim.Segment].Text, from)
		term, termOK := g.byDisplay[to]
		original := covered(out)
		switch {
		case fromOK && termOK && strings.Count(refined[out].Text, to) > strings.Count(original, to) &&
			strings.Count(refined[out].Text, from) < strings.Count(original, from):
			record := model.TranscriptCorrection{From: from, To: to, TermKey: term.TermKey, Segment: out}
			if !seen[record] {
				seen[record] = true
				kept = append(kept, record)
				if backed[out] == nil {
					backed[out] = map[string]int{}
				}
				backed[out][to] += strings.Count(original, from) - strings.Count(refined[out].Text, from)
			}
		case fromOK && !termOK && strings.Count(refined[out].Text, to) == 1 && !strings.Contains(original, to):
			refined[out].Text = strings.Replace(refined[out].Text, to, from, 1)
		}
	}
	for out := range refined {
		original := covered(out)
		for _, term := range g.terms {
			gained := strings.Count(refined[out].Text, term.DisplayAs) - strings.Count(original, term.DisplayAs)
			if gained > backed[out][term.DisplayAs] {
				return nil, true
			}
		}
	}
	return kept, false
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
// term key, at most MaxGlossaryTerms. An Account layer that cannot be
// authorized or read is dropped and logged; a Personal or Common read error
// is returned.
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
		add(glossaryTermOf(row))
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
// guard, unless it is already there or the aliases would fail validation.
func (s *DictionaryService) appendHeardAs(ctx context.Context, pk, termKey, from, heardKey, ownerID string, member *repository.MembershipCheck) bool {
	row, err := s.correctionStore.GetDictTerm(ctx, pk, termKey)
	if err != nil || row == nil || row.State != model.TermStateActive || knownHeardAs(row.HeardAs, heardKey) {
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
