package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

// Automatic dictionary merge (design A4). TermsAnalysisService.Process runs
// MergeExtractedTerms (the owner's personal scope) after the deterministic
// filters, then MergeScoped into the meeting's Account when that layer is on.
// Account writes carry the owner's membership ConditionCheck; Common is never
// written from meetings, KB documents or research (it takes news terms only).
// ProcessSource merges KB/research terms through MergeScoped.
const (
	// MaxAutoTermsPerScope caps unedited automatic (active or suggested)
	// terms; the lowest-scoring are evicted after a merge.
	MaxAutoTermsPerScope = 300
	// MaxObservationsPerTerm keeps the newest observation rows of a term.
	MaxObservationsPerTerm = 10
	// termMergeAttempts bounds re-reads after a version conflict.
	termMergeAttempts = 3
	maxTermHeardAs    = 5
	recentTermWindow  = 30 * 24 * time.Hour
)

// TermsMergeStore is the dictionary persistence the merger uses.
// *repository.DictionaryRepository implements it.
type TermsMergeStore interface {
	ListDictTerms(ctx context.Context, pk string) ([]model.DictTerm, error)
	GetDictTerm(ctx context.Context, pk, termKey string) (*model.DictTerm, error)
	ListDictObs(ctx context.Context, pk, termKey string) ([]model.DictObservation, error)
	CreateAutoTermChecked(ctx context.Context, term *model.DictTerm, obs *model.DictObservation, member *repository.MembershipCheck) error
	MergeAutoObservation(ctx context.Context, in repository.DictMergeInput) error
	PromoteSuggestedTerm(ctx context.Context, in repository.DictMergeInput, content repository.DictPromotion) error
	EvictAutoTerm(ctx context.Context, pk, termKey string, expectedVersion int64) (repository.DictWriteResult, error)
	DeleteDictObs(ctx context.Context, pk, sk string) error
}

var _ TermsMergeStore = (*repository.DictionaryRepository)(nil)

// ExtractedTerm is one candidate that already passed the filters.
type ExtractedTerm struct {
	DisplayAs    string
	Phrase       string
	Type         string
	HeardAs      []string
	SourceTypes  []string
	Confidence   float64
	TranscribeKo bool
	TranscribeEn bool
}

// TermsMeetingRef identifies the source meeting of a merge. Tainted means a
// non-owner edited a source (A2.1): new terms are only suggested and
// existing terms are not reinforced. Accepted is the meeting's prior
// acceptedTerms list, which the merge extends; a key already in it means the
// meeting was counted for that term, so a pruned observation is re-recorded
// without another hit.
type TermsMeetingRef struct {
	OwnerID           string
	MeetingID         string
	SummaryGeneration int64
	NotesSafe         bool
	Tainted           bool
	Accepted          []model.TermsAcceptedTerm
}

// TermsScope is the partition a merge writes. Member is required for an
// ACCOUNT# partition and must name the source owner; it is ignored otherwise.
type TermsScope struct {
	PK     string
	Member *repository.MembershipCheck
}

// TermsSourceRef is the evidence of a scoped merge: Kind/Ref name the source
// (DictSourceMeeting with Ref = meeting ID, DictSourceKB with Ref = object
// key, DictSourceResearch with Ref = research ID) and OwnerID the user whose
// content it is. NotesSafe, Tainted, Accepted and SummaryGeneration are as on
// TermsMeetingRef.
type TermsSourceRef struct {
	Kind, Ref, OwnerID string
	NotesSafe, Tainted bool
	Accepted           []model.TermsAcceptedTerm
	SummaryGeneration  int64
}

// TermsMergeResult reports a merge. Accepted is the union of the prior list
// and every term this meeting now has an observation for; it is complete even
// when an error is returned, so the caller can record it on failure.
// CleanupErr joins failed best-effort observation deletions.
type TermsMergeResult struct {
	Counters   model.TermsCounters
	Accepted   []model.TermsAcceptedTerm
	CleanupErr error
}

// TermsMerger applies the A4 merge rules.
type TermsMerger struct {
	store TermsMergeStore
	now   func() time.Time
}

// NewTermsMerger builds a merger over store.
func NewTermsMerger(store TermsMergeStore) *TermsMerger {
	return &TermsMerger{store: store, now: func() time.Time { return time.Now().UTC() }}
}

type mergeCandidate struct {
	ExtractedTerm
	key string
}

type termOutcome int

const (
	outcomeSkipped  termOutcome = iota
	outcomeObserved             // this meeting now has an observation row
)

// MergeExtractedTerms merges candidates into scopePK for one meeting:
//
//	none                    -> CreateAutoTermChecked (active, or suggested when tainted), hits=1
//	tombstone               -> skip
//	manual or userEditedAt  -> skip
//	suggested, untainted    -> PromoteSuggestedTerm (version CAS) + observation;
//	                           the tainted content is replaced and hits restarts at 1
//	auto active, untainted  -> MergeAutoObservation (version CAS)
//	existing, tainted       -> skip
//
// A heardAs whose key is another term's key is dropped. Version conflicts
// and transaction conflicts re-read and retry up to three attempts; an already
// recorded observation is a no-op, and a meeting already in ref.Accepted whose
// observation was pruned is recorded again without a hit. Afterwards the scope's automatic terms are capped at 300 and each
// observed term keeps at most 10 observations.
func (m *TermsMerger) MergeExtractedTerms(ctx context.Context, scopePK string, candidates []ExtractedTerm, ref TermsMeetingRef) (*TermsMergeResult, error) {
	if ref.OwnerID == "" || ref.MeetingID == "" || scopePK != model.PrefixUser+ref.OwnerID {
		return nil, fmt.Errorf("%w: terms merge supports only the owner's personal scope", ErrInvalidInput)
	}
	return m.MergeScoped(ctx, TermsScope{PK: scopePK}, candidates, TermsSourceRef{
		Kind: model.DictSourceMeeting, Ref: ref.MeetingID, OwnerID: ref.OwnerID, NotesSafe: ref.NotesSafe,
		Tainted: ref.Tainted, Accepted: ref.Accepted, SummaryGeneration: ref.SummaryGeneration,
	})
}

// validateScope accepts the source owner's personal scope, or an Account scope
// whose membership check names that owner, for a meeting, KB or research
// source. Common is refused: none of these sources write it (R6).
func validateScope(scope TermsScope, ref TermsSourceRef) error {
	switch ref.Kind {
	case model.DictSourceMeeting, model.DictSourceKB, model.DictSourceResearch:
	default:
		return fmt.Errorf("%w: terms merge does not take this source kind", ErrInvalidInput)
	}
	if ref.OwnerID == "" || ref.Ref == "" {
		return fmt.Errorf("%w: terms merge needs an owner and a source ref", ErrInvalidInput)
	}
	kind, accountID, ok := model.TermScopeOf(scope.PK)
	switch {
	case !ok || kind == model.TermScopeCommon:
		return fmt.Errorf("%w: terms merge does not write this scope", ErrInvalidInput)
	case kind == model.TermScopeUser && scope.PK != model.PrefixUser+ref.OwnerID:
		return fmt.Errorf("%w: terms merge writes only the owner's personal scope", ErrInvalidInput)
	case kind == model.TermScopeAccount && (scope.Member == nil || scope.Member.AccountID != accountID || scope.Member.UserID != ref.OwnerID):
		return fmt.Errorf("%w: account terms merge needs the owner's membership check", ErrInvalidInput)
	}
	return nil
}

// MergeScoped is MergeExtractedTerms for any scope validateScope accepts. An
// Account write the membership check refuses ends the merge with
// repository.ErrNotAccountMember. The auto cap applies per partition.
func (m *TermsMerger) MergeScoped(ctx context.Context, scope TermsScope, candidates []ExtractedTerm, ref TermsSourceRef) (*TermsMergeResult, error) {
	if err := validateScope(scope, ref); err != nil {
		return nil, err
	}
	if err := model.ValidateTermsAccepted(ref.Accepted); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	scopePK := scope.PK
	result := &TermsMergeResult{Accepted: append([]model.TermsAcceptedTerm(nil), ref.Accepted...)}
	result.Counters.Candidates = len(candidates)
	merged := m.prepare(candidates, result)

	rows, err := m.store.ListDictTerms(ctx, scopePK)
	if err != nil {
		return result, fmt.Errorf("failed to list dictionary terms: %w", err)
	}
	byKey := make(map[string]*model.DictTerm, len(rows))
	termKeys := make(map[string]bool, len(rows)+len(merged))
	for i := range rows {
		byKey[rows[i].TermKey] = &rows[i]
		if rows[i].State == model.TermStateActive {
			termKeys[rows[i].TermKey] = true
		}
	}
	for _, c := range merged {
		termKeys[c.key] = true
	}

	var cleanup []error
	for _, c := range merged {
		// Only a written observation takes an acceptedTerms slot, so the
		// room check runs per candidate, after earlier skips freed theirs.
		if acceptedIndex(result.Accepted, c.key) < 0 && len(result.Accepted) >= model.MaxTermsAccepted {
			result.Counters.Capped++
			continue
		}
		outcome, err := m.mergeOne(ctx, scope, c, byKey[c.key], termKeys, ref, result, &cleanup)
		if outcome == outcomeObserved {
			addAccepted(result, c.key, c.SourceTypes)
		}
		if err != nil {
			result.CleanupErr = errors.Join(cleanup...)
			return result, err
		}
	}
	if err := m.enforceAutoCap(ctx, scopePK, result, &cleanup); err != nil {
		result.CleanupErr = errors.Join(cleanup...)
		return result, err
	}
	result.CleanupErr = errors.Join(cleanup...)
	return result, nil
}

// prepare drops structurally invalid candidates and folds duplicates of a key
// into the first.
func (m *TermsMerger) prepare(candidates []ExtractedTerm, result *TermsMergeResult) []mergeCandidate {
	index := map[string]int{}
	var merged []mergeCandidate
	for _, c := range candidates {
		key := TermKey(c.DisplayAs)
		if key == "" || strings.TrimSpace(c.DisplayAs) == "" || !validSourceTypes(c.SourceTypes) {
			result.Counters.Invalid++
			continue
		}
		if i, ok := index[key]; ok {
			prior := &merged[i]
			prior.HeardAs = append(prior.HeardAs, c.HeardAs...)
			prior.SourceTypes = unionStrings(prior.SourceTypes, c.SourceTypes)
			if c.Confidence > prior.Confidence {
				prior.Confidence = c.Confidence
			}
			continue
		}
		index[key] = len(merged)
		c.SourceTypes = unionStrings(nil, c.SourceTypes)
		merged = append(merged, mergeCandidate{ExtractedTerm: c, key: key})
	}
	return merged
}

func validSourceTypes(types []string) bool {
	if len(types) == 0 {
		return false
	}
	for _, s := range types {
		if !model.ValidTermSourceType(s) {
			return false
		}
	}
	return true
}

func acceptedIndex(list []model.TermsAcceptedTerm, key string) int {
	for i := range list {
		if list[i].TermKey == key {
			return i
		}
	}
	return -1
}

func addAccepted(result *TermsMergeResult, key string, sources []string) {
	if i := acceptedIndex(result.Accepted, key); i >= 0 {
		result.Accepted[i].SourceTypes = unionStrings(result.Accepted[i].SourceTypes, sources)
		return
	}
	result.Accepted = append(result.Accepted, model.TermsAcceptedTerm{TermKey: key, SourceTypes: unionStrings(nil, sources)})
}

// unionStrings appends the members of b missing from a, keeping order.
func unionStrings(a, b []string) []string {
	out := append([]string(nil), a...)
	for _, s := range b {
		found := false
		for _, have := range out {
			if have == s {
				found = true
				break
			}
		}
		if !found {
			out = append(out, s)
		}
	}
	return out
}

// cleanHeardAs dedupes aliases by key, dropping blanks, the term's own key and
// any key of another term, and keeps at most five.
func cleanHeardAs(ownKey string, termKeys map[string]bool, aliases []string, dropped *int) []string {
	seen := map[string]bool{}
	var out []string
	for _, alias := range aliases {
		alias = strings.TrimSpace(alias)
		key := TermKey(alias)
		if key == "" || key == ownKey || seen[key] {
			continue
		}
		seen[key] = true
		if termKeys[key] {
			*dropped++
			continue
		}
		if len(out) < maxTermHeardAs {
			out = append(out, alias)
		}
	}
	return out
}

func (m *TermsMerger) mergeOne(ctx context.Context, scope TermsScope, c mergeCandidate, term *model.DictTerm, termKeys map[string]bool, ref TermsSourceRef, result *TermsMergeResult, cleanup *[]error) (termOutcome, error) {
	// A meeting observation is keyed by and checked against its meeting; a
	// KB/research one has no meeting and is keyed by a digest of its ref.
	pk, meetingID := scope.PK, ref.Ref
	if ref.Kind != model.DictSourceMeeting {
		meetingID = ""
	}
	obsSource := model.DictObsSourceID(ref.Kind, ref.Ref)
	counters := &result.Counters
	obsHeard := cleanHeardAs(c.key, termKeys, c.HeardAs, &counters.AliasesDropped)
	for attempt := 0; attempt < termMergeAttempts; attempt++ {
		if attempt > 0 {
			fresh, err := m.store.GetDictTerm(ctx, pk, c.key)
			if err != nil {
				return outcomeSkipped, fmt.Errorf("failed to re-read dictionary term: %w", err)
			}
			term = fresh
		}
		now := m.now().Format(model.DictTimeLayout)
		source := model.DictSourceRef{Kind: ref.Kind, Ref: ref.Ref, At: now}
		obs := &model.DictObservation{
			PK: pk, SK: model.DictObsSK(c.key, obsSource), TermKey: c.key, MeetingID: meetingID,
			SourceKind: ref.Kind, SourceRef: ref.Ref,
			OwnerID: ref.OwnerID, HeardAs: obsHeard, SourceTypes: c.SourceTypes,
			SummaryGeneration: ref.SummaryGeneration, NotesSafe: ref.NotesSafe, Tainted: ref.Tainted,
			CreatedAt: now, EntityType: model.EntityTypeDictObservation,
		}
		var err error
		switch {
		case term == nil:
			err = m.store.CreateAutoTermChecked(ctx, newAutoTerm(pk, c, obsHeard, ref, source, now), obs, scope.Member)
			if err == nil {
				counters.Created++
				if ref.Tainted {
					counters.Suggested++
				}
				return outcomeObserved, nil
			}
			if errors.Is(err, repository.ErrDictObservationExists) {
				// No term but an observation: a leftover of an eviction
				// whose cleanup failed. Remove it and create again.
				if delErr := m.store.DeleteDictObs(ctx, pk, obs.SK); delErr != nil {
					return outcomeSkipped, fmt.Errorf("failed to remove stale observation: %w", delErr)
				}
				continue
			}
		case term.State == model.TermStateDeleted:
			counters.SkippedTombstone++
			return outcomeSkipped, nil
		case term.Origin != model.TermOriginAuto || term.UserEditedAt != "":
			counters.SkippedManual++
			return outcomeSkipped, nil
		case term.State != model.TermStateActive && term.State != model.TermStateSuggested:
			counters.SkippedManual++ // unknown state: not automatically managed
			return outcomeSkipped, nil
		case ref.Tainted:
			counters.SkippedTainted++
			return outcomeSkipped, nil
		case term.State == model.TermStateSuggested:
			// The suggestion's aliases, sources, content and hit came from
			// a tainted run; only this untainted run's evidence survives.
			in := repository.DictMergeInput{
				PK: pk, TermKey: c.key, Observation: obs, SeenAt: now, ExpectedVersion: term.Version,
				HeardAs: obsHeard, SourceTypes: c.SourceTypes, Sources: []model.DictSourceRef{source},
				Member: scope.Member,
			}
			err = m.store.PromoteSuggestedTerm(ctx, in, repository.DictPromotion{
				DisplayAs: strings.TrimSpace(c.DisplayAs), Phrase: c.Phrase, Type: c.Type,
				Confidence: c.Confidence, TranscribeKo: c.TranscribeKo, TranscribeEn: c.TranscribeEn,
				FirstSourceMeetingID: meetingID,
			})
			if err == nil {
				counters.Promoted++
				m.pruneObservations(ctx, pk, c.key, counters, cleanup)
				return outcomeObserved, nil
			}
			if errors.Is(err, repository.ErrDictObservationExists) {
				counters.AlreadyCounted++
				return outcomeObserved, nil
			}
		default:
			in := repository.DictMergeInput{
				PK: pk, TermKey: c.key, Observation: obs, SeenAt: now, ExpectedVersion: term.Version,
				HeardAs:     cleanHeardAs(c.key, termKeys, append(append([]string(nil), term.HeardAs...), obsHeard...), new(int)),
				SourceTypes: unionStrings(term.SourceTypes, c.SourceTypes),
				Sources:     model.AppendDictSource(term.Sources, source),
				SkipHit:     acceptedIndex(ref.Accepted, c.key) >= 0,
				Member:      scope.Member,
			}
			err = m.store.MergeAutoObservation(ctx, in)
			if err == nil {
				if in.SkipHit {
					counters.AlreadyCounted++
				} else {
					counters.Merged++
				}
				m.pruneObservations(ctx, pk, c.key, counters, cleanup)
				return outcomeObserved, nil
			}
			if errors.Is(err, repository.ErrDictObservationExists) {
				counters.AlreadyCounted++
				return outcomeObserved, nil
			}
		}
		if errors.Is(err, repository.ErrDictTermConflict) || errors.Is(err, repository.ErrDictTransactionConflict) {
			continue // re-read and retry
		}
		if errors.Is(err, repository.ErrDictTransactionCancelled) || errors.Is(err, repository.ErrDictSourceMeetingGone) ||
			errors.Is(err, repository.ErrNotAccountMember) || errors.Is(err, repository.ErrInvalidInput) {
			// A cancelled or refused transaction applied nothing.
			return outcomeSkipped, fmt.Errorf("failed to merge dictionary term: %w", err)
		}
		// Any other failure may have committed (an ambiguous response), so
		// the key is still recorded for deletion cleanup.
		return outcomeObserved, fmt.Errorf("failed to merge dictionary term: %w", err)
	}
	counters.Conflicts++
	return outcomeSkipped, nil
}

func newAutoTerm(pk string, c mergeCandidate, heard []string, ref TermsSourceRef, source model.DictSourceRef, now string) *model.DictTerm {
	state := model.TermStateActive
	if ref.Tainted {
		state = model.TermStateSuggested
	}
	scope, _, _ := model.TermScopeOf(pk)
	firstMeeting := ""
	if ref.Kind == model.DictSourceMeeting {
		firstMeeting = ref.Ref
	}
	return &model.DictTerm{
		PK: pk, SK: model.DictTermSK(c.key), TermKey: c.key, Scope: scope, State: state,
		DisplayAs: strings.TrimSpace(c.DisplayAs), Phrase: c.Phrase, HeardAs: heard,
		AliasMode: model.TermAliasHint, Type: c.Type, Origin: model.TermOriginAuto,
		SourceTypes: c.SourceTypes, FirstSourceMeetingID: firstMeeting, Hits: 1, Sources: []model.DictSourceRef{source},
		Confidence: c.Confidence, TranscribeKo: c.TranscribeKo, TranscribeEn: c.TranscribeEn,
		AddedAt: now, UpdatedAt: now, LastSeenAt: now, CreatedBy: ref.OwnerID, UpdatedBy: ref.OwnerID,
		Version: 1, EntityType: model.EntityTypeDictTerm,
	}
}

// pruneObservations deletes the oldest observations beyond ten. hits is not
// decremented; the meeting's acceptedTerms entry keeps it from being counted
// again (SkipHit). Failures are surfaced, not fatal.
func (m *TermsMerger) pruneObservations(ctx context.Context, pk, key string, counters *model.TermsCounters, cleanup *[]error) {
	rows, err := m.store.ListDictObs(ctx, pk, key)
	if err != nil {
		*cleanup = append(*cleanup, fmt.Errorf("failed to list observations: %w", err))
		return
	}
	if len(rows) <= MaxObservationsPerTerm {
		return
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].CreatedAt != rows[j].CreatedAt {
			return rows[i].CreatedAt < rows[j].CreatedAt
		}
		return rows[i].SK < rows[j].SK
	})
	for _, row := range rows[:len(rows)-MaxObservationsPerTerm] {
		if err := m.store.DeleteDictObs(ctx, pk, row.SK); err != nil {
			*cleanup = append(*cleanup, err)
			continue
		}
		counters.ObsPruned++
	}
}

// evictable is an automatic term no user has touched.
func evictable(t model.DictTerm) bool {
	return t.Origin == model.TermOriginAuto && t.UserEditedAt == "" &&
		(t.State == model.TermStateActive || t.State == model.TermStateSuggested)
}

// termScore is hits*2 plus one when seen within 30 days.
func termScore(t model.DictTerm, now time.Time) int {
	score := t.Hits * 2
	if seen, err := time.Parse(model.DictTimeLayout, t.LastSeenAt); err == nil && now.Sub(seen) <= recentTermWindow {
		score++
	}
	return score
}

// enforceAutoCap evicts the lowest-scoring, then least recently seen,
// unedited automatic terms until at most 300 remain. When EvictAutoTerm's
// guard fails the row is re-read: a row now gone, edited or tombstoned no
// longer counts toward the cap (a concurrent merge may have evicted it), so
// it reduces the excess; one that is merely newer is skipped for the next.
func (m *TermsMerger) enforceAutoCap(ctx context.Context, pk string, result *TermsMergeResult, cleanup *[]error) error {
	rows, err := m.store.ListDictTerms(ctx, pk)
	if err != nil {
		return fmt.Errorf("failed to list dictionary terms for the cap: %w", err)
	}
	var auto []model.DictTerm
	for _, row := range rows {
		if evictable(row) {
			auto = append(auto, row)
		}
	}
	excess := len(auto) - MaxAutoTermsPerScope
	if excess <= 0 {
		return nil
	}
	now := m.now()
	sort.SliceStable(auto, func(i, j int) bool {
		si, sj := termScore(auto[i], now), termScore(auto[j], now)
		if si != sj {
			return si < sj
		}
		if auto[i].LastSeenAt != auto[j].LastSeenAt {
			return auto[i].LastSeenAt < auto[j].LastSeenAt
		}
		return auto[i].SK < auto[j].SK
	})
	for _, row := range auto {
		if excess == 0 {
			break
		}
		res, err := m.store.EvictAutoTerm(ctx, pk, row.TermKey, row.Version)
		if errors.Is(err, repository.ErrDictTermConflict) {
			result.Counters.Conflicts++
			fresh, getErr := m.store.GetDictTerm(ctx, pk, row.TermKey)
			if getErr != nil {
				return fmt.Errorf("failed to re-read dictionary term for the cap: %w", getErr)
			}
			if fresh == nil || !evictable(*fresh) {
				excess--
			}
			continue
		}
		if err != nil {
			return fmt.Errorf("failed to evict dictionary term: %w", err)
		}
		if res.CleanupErr != nil {
			*cleanup = append(*cleanup, res.CleanupErr)
		}
		result.Counters.Evicted++
		excess--
	}
	return nil
}
