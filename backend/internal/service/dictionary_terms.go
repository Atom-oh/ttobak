package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

// Dictionary term CRUD outcomes; handlers map each to a 409 code.
var (
	ErrDictionaryVersionConflict = errors.New("dictionary term changed concurrently")
	ErrDictionaryFull            = errors.New("dictionary is full")
	ErrDictionaryTermExists      = errors.New("dictionary term already exists")
	ErrDictionaryTermNotManual   = errors.New("only manual dictionary terms can be removed this way")
)

// Per-scope caps; tombstones beyond the cap are pruned oldest first.
const (
	MaxManualTermsPerScope = 500
	MaxTombstonesPerScope  = 2000
	// MaxLegacyTerms bounds a legacy PUT body. GET lists auto rows too, so a
	// full round-trip can exceed the manual cap; only entries that become or
	// stay manual rows count toward MaxManualTermsPerScope.
	MaxLegacyTerms = 1000
)

const vocabularyRebuildWarning = "vocabulary rebuild failed; recognition keeps the previous vocabulary"

func (s *DictionaryService) stamp() string { return s.now().Format(model.DictTimeLayout) }

func termView(row model.DictTerm) model.DictTermView {
	heard := row.HeardAs
	if heard == nil {
		heard = []string{}
	}
	sources := sharedLayerSources(row)
	view := model.DictTermView{
		TermID: row.TermKey, DisplayAs: row.DisplayAs, Phrase: row.Phrase, HeardAs: heard,
		AliasMode: row.AliasMode, Type: row.Type, Origin: row.Origin, State: row.State,
		UserEdited: row.UserEditedAt != "", Version: row.Version,
		TranscribeKo: row.TranscribeKo, TranscribeEn: row.TranscribeEn, Hits: row.Hits,
		AddedAt: row.AddedAt, UpdatedAt: row.UpdatedAt,
		Layer: model.DictLayerOf(row.PK), Sources: sources,
	}
	if row.PK == model.PrefixDictCommon {
		view.UpdatedBy = row.UpdatedBy // L2: Common edits are attributed
	}
	return view
}

// sharedLayerSources returns the row's sources for a view. On Account and
// Common rows the refs name other users' content (a KB key carries its
// owner's ID and file name), and even a digest of one can be confirmed by
// guessing, so those views carry only each source's kind and time; only the
// owner's Personal rows keep refs.
func sharedLayerSources(row model.DictTerm) []model.DictSourceRef {
	if len(row.Sources) == 0 {
		return []model.DictSourceRef{}
	}
	if !strings.HasPrefix(row.PK, model.PrefixAccount) && row.PK != model.PrefixDictCommon {
		return row.Sources
	}
	out := make([]model.DictSourceRef, len(row.Sources))
	for i, src := range row.Sources {
		out[i] = model.DictSourceRef{Kind: src.Kind, At: src.At}
	}
	return out
}

func sortRowsByAdded(rows []model.DictTerm) {
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].AddedAt != rows[j].AddedAt {
			return rows[i].AddedAt < rows[j].AddedAt
		}
		return rows[i].SK < rows[j].SK
	})
}

// termViews lists non-tombstone rows; before migration pristine legacy copies
// are skipped because the blob is still authoritative over them.
func termViews(dict *model.UserDictionary, rows []model.DictTerm) []model.DictTermView {
	premigration := dict != nil && dict.MigratedAt == ""
	visible := make([]model.DictTerm, 0, len(rows))
	for _, row := range rows {
		if row.State == model.TermStateDeleted || (premigration && pristineLegacyRow(row)) {
			continue
		}
		visible = append(visible, row)
	}
	sortRowsByAdded(visible)
	views := make([]model.DictTermView, 0, len(visible))
	for _, row := range visible {
		views = append(views, termView(row))
	}
	return views
}

// GetDictionaryWithTerms is the API GET: a best-effort migration first, so
// terms have termIds, then EnsureVocabularies (poll, flip or start a build),
// then legacy fields plus rows. Shared readers keep the never-writing
// GetDictionary.
func (s *DictionaryService) GetDictionaryWithTerms(ctx context.Context, userID string) (*model.DictionaryResponse, error) {
	if _, err := s.MigrateLegacyDictionary(ctx, userID); err != nil && !errors.Is(err, ErrLegacyDictionaryConflict) {
		log.Printf("dictionary migration deferred for user %s: %v", userID, err)
	}
	views, warnings := s.EnsureVocabularies(ctx, userID, model.VocabContextPersonal, false)
	resp, dict, rows, err := s.readDictionary(ctx, userID)
	if err != nil {
		return nil, err
	}
	resp.Items = termViews(dict, rows)
	resp.Vocabularies, resp.Warnings = views, warnings
	resp.MigrationPending = dict != nil && dict.MigratedAt == "" && len(dict.Terms) > 0
	return resp, nil
}

// ensureMigrated makes term rows authoritative before a v2 write.
func (s *DictionaryService) ensureMigrated(ctx context.Context, userID string) error {
	_, err := s.MigrateLegacyDictionary(ctx, userID)
	return err
}

func versionConflict(err error) error {
	if errors.Is(err, repository.ErrConditionFailed) {
		return fmt.Errorf("%w: %w", ErrDictionaryVersionConflict, err)
	}
	return err
}

func countManual(rows []model.DictTerm) int {
	n := 0
	for _, row := range rows {
		if row.Origin == model.TermOriginManual && row.State == model.TermStateActive {
			n++
		}
	}
	return n
}

func newManualRow(userID string, v ValidatedTerm, phrase, aliasMode string, displayEmpty bool, stamp string) model.DictTerm {
	return model.DictTerm{
		PK: model.PrefixUser + userID, SK: model.DictTermSK(v.TermKey), TermKey: v.TermKey,
		Scope: model.TermScopeUser, State: model.TermStateActive,
		DisplayAs: v.DisplayAs, Phrase: phrase, HeardAs: v.HeardAs, AliasMode: aliasMode, Type: v.Type,
		Origin: model.TermOriginManual, TranscribeKo: phrase != "",
		AddedAt: stamp, UpdatedAt: stamp, CreatedBy: userID, UpdatedBy: userID,
		Version: 1, EntityType: model.EntityTypeDictTerm, LegacyDisplayEmpty: displayEmpty,
	}
}

// CreateTerm creates a manual term or restores a tombstone as manual.
func (s *DictionaryService) CreateTerm(ctx context.Context, userID string, in model.CreateDictTermRequest) (*model.DictTermMutationResponse, error) {
	return s.createTerm(ctx, userID, personalTarget(userID), in)
}

// createTerm is CreateTerm in a personal or Account partition.
func (s *DictionaryService) createTerm(ctx context.Context, userID string, t dictTarget, in model.CreateDictTermRequest) (*model.DictTermMutationResponse, error) {
	v, err := ValidateTermInput(in.DisplayAs, in.HeardAs, in.Type)
	if err != nil {
		return nil, err
	}
	phrase := v.DisplayAs
	if in.Phrase != nil {
		if phrase, err = validateTermPhrase(*in.Phrase); err != nil {
			return nil, err
		}
	}
	aliasMode, err := validateAliasMode(in.AliasMode)
	if err != nil {
		return nil, err
	}
	if err := s.ensureTargetMigrated(ctx, userID, t); err != nil {
		return nil, err
	}
	rows, err := s.repo.ListDictTerms(ctx, t.pk)
	if err != nil {
		return nil, err
	}
	var existing *model.DictTerm
	for i := range rows {
		if rows[i].TermKey == v.TermKey {
			existing = &rows[i]
		}
	}
	if existing != nil && existing.State != model.TermStateDeleted {
		return nil, ErrDictionaryTermExists
	}
	if countManual(rows) >= MaxManualTermsPerScope {
		return nil, ErrDictionaryFull
	}
	row := newManualRow(userID, v, phrase, aliasMode, false, s.stamp())
	row.PK, row.Scope = t.pk, t.scope
	if existing == nil {
		if err := s.repo.CreateDictTerm(ctx, &row, t.member); err != nil {
			if errors.Is(err, repository.ErrConditionFailed) {
				return nil, ErrDictionaryTermExists
			}
			return nil, err
		}
	} else {
		if err := s.repo.RestoreDictTerm(ctx, &row, existing.Version, t.member); err != nil {
			return nil, versionConflict(err)
		}
		row.Version = existing.Version + 1
	}
	return s.targetResult(ctx, userID, t, &row, phrase != ""), nil
}

// UpdateTerm applies a user edit under the caller's version. Every edit sets
// userEditedAt, which blocks all later automatic writes; aliasMode=always is
// the "keep" action, and state=active accepts a suggestion.
func (s *DictionaryService) UpdateTerm(ctx context.Context, userID, termKey string, in model.UpdateDictTermRequest) (*model.DictTermMutationResponse, error) {
	return s.updateTerm(ctx, userID, personalTarget(userID), termKey, in)
}

// updateTerm is UpdateTerm in a personal or Account partition.
func (s *DictionaryService) updateTerm(ctx context.Context, userID string, t dictTarget, termKey string, in model.UpdateDictTermRequest) (*model.DictTermMutationResponse, error) {
	if in.Version == nil {
		return nil, termInputError("version", "is required")
	}
	if in.DisplayAs == nil && in.Phrase == nil && in.HeardAs == nil && in.Type == nil && in.AliasMode == nil && in.State == nil {
		return nil, termInputError("body", "no changes")
	}
	if err := s.ensureTargetMigrated(ctx, userID, t); err != nil {
		return nil, err
	}
	pk := t.pk
	row, err := s.repo.GetDictTerm(ctx, pk, termKey)
	if err != nil {
		return nil, err
	}
	if row == nil || row.State == model.TermStateDeleted {
		return nil, ErrNotFound
	}
	if row.Version != *in.Version {
		return nil, ErrDictionaryVersionConflict
	}
	change, err := termChange(*row, in)
	if err != nil {
		return nil, err
	}
	if err := s.repo.UpdateDictTerm(ctx, pk, termKey, change, userID, s.stamp(), *in.Version, t.member); err != nil {
		return nil, versionConflict(err)
	}
	updated, err := s.repo.GetDictTerm(ctx, pk, termKey)
	if err != nil {
		return nil, fmt.Errorf("failed to reread dictionary term: %w", err)
	}
	if updated == nil || updated.State == model.TermStateDeleted {
		// Committed, then removed concurrently: report the current state.
		return nil, ErrNotFound
	}
	return s.targetResult(ctx, userID, t, updated, in.Phrase != nil || in.State != nil || in.DisplayAs != nil), nil
}

func termChange(row model.DictTerm, in model.UpdateDictTermRequest) (repository.DictTermChange, error) {
	var change repository.DictTermChange
	if in.DisplayAs != nil {
		display, err := validateTermText("displayAs", *in.DisplayAs)
		if err != nil {
			return change, err
		}
		if TermKey(display) != row.TermKey {
			return change, termInputError("displayAs", "a different term must be created instead of renamed")
		}
		noLegacyEmpty := false
		change.DisplayAs, change.LegacyDisplayEmpty = &display, &noLegacyEmpty
	}
	if in.HeardAs != nil {
		heard, err := validateHeardAs(*in.HeardAs, NormalizeTermKey(row.DisplayAs))
		if err != nil {
			return change, err
		}
		change.HeardAs = &heard
	}
	if in.Type != nil {
		typ, err := validateTermType(*in.Type)
		if err != nil {
			return change, err
		}
		change.Type = &typ
	}
	if in.Phrase != nil {
		phrase, err := validateTermPhrase(*in.Phrase)
		if err != nil {
			return change, err
		}
		ko := phrase != ""
		change.Phrase, change.TranscribeKo = &phrase, &ko
	}
	if in.AliasMode != nil {
		if *in.AliasMode == "" {
			return change, termInputError("aliasMode", "must be hint or always")
		}
		mode, err := validateAliasMode(*in.AliasMode)
		if err != nil {
			return change, err
		}
		change.AliasMode = &mode
	}
	if in.State != nil {
		if *in.State != model.TermStateActive || (row.State != model.TermStateSuggested && row.State != model.TermStateActive) {
			return change, termInputError("state", "only a suggestion can be accepted; use DELETE to remove a term")
		}
		active := model.TermStateActive
		change.State = &active
	}
	return change, nil
}

// TombstoneTerm deletes a term of any origin by leaving a tombstone, so it is
// never added automatically again. A nil version uses the current row.
func (s *DictionaryService) TombstoneTerm(ctx context.Context, userID, termKey string, version *int64) (*model.DictTermMutationResponse, error) {
	return s.tombstoneTerm(ctx, userID, personalTarget(userID), termKey, version)
}

// tombstoneTerm is TombstoneTerm in a personal or Account partition.
func (s *DictionaryService) tombstoneTerm(ctx context.Context, userID string, t dictTarget, termKey string, version *int64) (*model.DictTermMutationResponse, error) {
	if err := s.ensureTargetMigrated(ctx, userID, t); err != nil {
		return nil, err
	}
	pk := t.pk
	row, err := s.repo.GetDictTerm(ctx, pk, termKey)
	if err != nil {
		return nil, err
	}
	if row == nil || row.State == model.TermStateDeleted {
		return nil, ErrNotFound
	}
	if version != nil && *version != row.Version {
		return nil, ErrDictionaryVersionConflict
	}
	result, err := s.repo.TombstoneDictTerm(ctx, pk, termKey, userID, s.stamp(), row.Version, t.member)
	if err != nil {
		return nil, versionConflict(err)
	}
	resp := &model.DictTermMutationResponse{TermID: termKey, State: model.TermStateDeleted}
	if result.CleanupErr != nil {
		log.Printf("dictionary observation cleanup failed for user %s: %v", userID, result.CleanupErr)
		resp.Warnings = append(resp.Warnings, "term observation cleanup incomplete")
	}
	if err := s.pruneTombstones(ctx, pk, t.member); err != nil {
		log.Printf("dictionary tombstone pruning failed for user %s: %v", userID, err)
		resp.Warnings = append(resp.Warnings, "tombstone pruning incomplete")
	}
	if !t.personal() {
		return resp, nil // only personal writes rebuild the personal vocabulary here
	}
	views, warnings := s.ensureAfterWrite(ctx, userID, row.State == model.TermStateActive && row.TranscribeKo && row.Phrase != "")
	resp.Status, resp.Vocabularies, resp.Warnings = s.currentVocabularyStatus(ctx, userID), views, append(resp.Warnings, warnings...)
	return resp, nil
}

func (s *DictionaryService) pruneTombstones(ctx context.Context, pk string, member *repository.MembershipCheck) error {
	rows, err := s.repo.ListDictTerms(ctx, pk)
	if err != nil {
		return err
	}
	var tombs []model.DictTerm
	for _, row := range rows {
		if row.State == model.TermStateDeleted {
			tombs = append(tombs, row)
		}
	}
	if len(tombs) <= MaxTombstonesPerScope {
		return nil
	}
	sort.SliceStable(tombs, func(i, j int) bool {
		if tombs[i].DeletedAt != tombs[j].DeletedAt {
			return tombs[i].DeletedAt < tombs[j].DeletedAt
		}
		return tombs[i].SK < tombs[j].SK
	})
	var errs []error
	for _, tomb := range tombs[:len(tombs)-MaxTombstonesPerScope] {
		if err := s.repo.DeleteTombstone(ctx, pk, tomb.TermKey, tomb.Version, member); err != nil && !errors.Is(err, repository.ErrConditionFailed) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (s *DictionaryService) mutationResult(ctx context.Context, userID string, row *model.DictTerm, rebuild bool) *model.DictTermMutationResponse {
	view := termView(*row)
	resp := &model.DictTermMutationResponse{Item: &view, TermID: row.TermKey, State: row.State}
	resp.Vocabularies, resp.Warnings = s.ensureAfterWrite(ctx, userID, rebuild)
	resp.Status = s.currentVocabularyStatus(ctx, userID)
	return resp
}

// ensureAfterWrite runs EnsureVocabularies with the manual-write bypass when
// the write can change a vocabulary, and otherwise only reads the states.
func (s *DictionaryService) ensureAfterWrite(ctx context.Context, userID string, changed bool) (*model.VocabularyViews, []string) {
	if !changed {
		return s.vocabularyViews(ctx, userID), nil
	}
	return s.EnsureVocabularies(ctx, userID, model.VocabContextPersonal, true)
}

func (s *DictionaryService) currentVocabularyStatus(ctx context.Context, userID string) string {
	state, err := s.repo.GetVocabularyState(ctx, userID, model.VocabContextPersonal, model.VocabLangKo)
	if err != nil || state == nil {
		return ""
	}
	return legacyVocabularyStatus(state)
}

// legacyDesired is one validated legacy PUT term.
type legacyDesired struct {
	v            ValidatedTerm
	phrase       string
	legacy       model.DictionaryTerm // stored legacy form
	displayEmpty bool
	unchanged    bool // identical to the current list entry: not revalidated
}

const legacyIgnoredWarning = "some terms match automatic or suggested terms and were not changed; edit those terms individually"

// legacyPlan is a validated legacy PUT body: manual terms to reconcile, and
// whether any entry addressed a non-manual row in a way the PUT ignores.
type legacyPlan struct {
	desired []legacyDesired
	ignored bool
}

// prepareLegacyTerms splits a legacy PUT body. Entries whose key belongs to a
// live auto or suggested row pass through unvalidated (GET lists active auto
// rows, which may lack a phrase) and are never written; a changed one, or one
// naming a hidden suggestion, sets ignored. The rest are manual terms: new or
// changed ones are validated, a term identical to the current list is kept as
// stored. Exact duplicates collapse; distinct terms sharing a key are rejected.
func prepareLegacyTerms(terms []model.DictionaryTerm, current map[model.DictionaryTerm]bool, rows []model.DictTerm) (legacyPlan, error) {
	var plan legacyPlan
	if len(terms) > MaxLegacyTerms {
		return plan, termInputError("terms", fmt.Sprintf("at most %d terms", MaxLegacyTerms))
	}
	byKey := make(map[string]model.DictTerm, len(rows))
	for _, row := range rows {
		byKey[row.TermKey] = row
	}
	out := make([]legacyDesired, 0, len(terms))
	index := map[string]int{}
	for i, t := range terms {
		field := fmt.Sprintf("terms[%d]", i)
		if key := legacyTermKey(t); key != "" {
			if row, ok := byKey[key]; ok && row.State != model.TermStateDeleted && row.Origin != model.TermOriginManual {
				if row.State != model.TermStateActive || legacyTermFromRow(row) != t {
					plan.ignored = true
				}
				continue
			}
		}
		row, exists := byKey[legacyTermKey(t)]
		editsManual := exists && row.State == model.TermStateActive && row.Origin == model.TermOriginManual
		d, err := prepareLegacyTerm(t, current[t], editsManual)
		if err != nil {
			var input *TermInputError
			if errors.As(err, &input) {
				return plan, termInputError(field+"."+input.Field, input.Reason)
			}
			return plan, err
		}
		if j, ok := index[d.v.TermKey]; ok {
			if out[j].legacy != d.legacy {
				return plan, termInputError(field, fmt.Sprintf("is the same term as terms[%d]", j))
			}
			continue
		}
		index[d.v.TermKey] = len(out)
		out = append(out, d)
	}
	if len(out) > MaxManualTermsPerScope {
		return plan, termInputError("terms", fmt.Sprintf("at most %d manual terms", MaxManualTermsPerScope))
	}
	plan.desired = out
	return plan, nil
}

// prepareLegacyTerm validates one manual legacy term. A phrase is required
// to create or restore a term; an edit of an existing manual row may keep an
// empty phrase (the v2 API allows phrase-less terms).
func prepareLegacyTerm(t model.DictionaryTerm, unchanged, editsManual bool) (legacyDesired, error) {
	if unchanged {
		display := t.DisplayAs
		if display == "" {
			display = t.Phrase
		}
		key := TermKey(display)
		if key == "" {
			return legacyDesired{}, termInputError("phrase", "is required")
		}
		return legacyDesired{
			v:      ValidatedTerm{DisplayAs: display, HeardAs: splitLegacySoundsLike(t.SoundsLike), TermKey: key},
			phrase: t.Phrase, legacy: t, displayEmpty: t.DisplayAs == "", unchanged: true,
		}, nil
	}
	phrase, err := validateTermPhrase(t.Phrase)
	if err != nil {
		return legacyDesired{}, err
	}
	display, displayEmpty := t.DisplayAs, trimTermSpaces(t.DisplayAs) == ""
	if phrase == "" && (!editsManual || displayEmpty) {
		return legacyDesired{}, termInputError("phrase", "is required")
	}
	if displayEmpty {
		display = phrase
	}
	v, err := ValidateTermInput(display, splitLegacySoundsLike(t.SoundsLike), "")
	if err != nil {
		return legacyDesired{}, err
	}
	legacy := model.DictionaryTerm{Phrase: phrase, SoundsLike: strings.Join(v.HeardAs, ", "), DisplayAs: v.DisplayAs}
	if displayEmpty {
		legacy.DisplayAs = ""
	}
	return legacyDesired{v: v, phrase: phrase, legacy: legacy, displayEmpty: displayEmpty}, nil
}

// UpdateDictionary is the legacy whole-list PUT. It writes the manual terms
// to the blob for pre-v2 readers, migrates it, then makes manual rows match
// the body: missing terms are created (a tombstoned one is restored as
// manual), changed manual terms are edited, and manual rows absent from the
// body are hard-deleted. It never tombstones and never touches auto or
// suggested rows; ignored edits of them are reported as a warning. Auto rows
// are kept out of the blob so derived terms never gain a second copy there.
func (s *DictionaryService) UpdateDictionary(ctx context.Context, userID string, terms []model.DictionaryTerm) (*model.DictionaryResponse, error) {
	_, dict, rows, err := s.readDictionary(ctx, userID)
	if err != nil {
		return nil, err
	}
	current := map[model.DictionaryTerm]bool{}
	for _, t := range unionLegacyTerms(dict, rows) {
		current[t] = true
	}
	plan, err := prepareLegacyTerms(terms, current, rows)
	if err != nil {
		return nil, err
	}
	desired := plan.desired
	blobTerms := make([]model.DictionaryTerm, len(desired))
	for i, d := range desired {
		blobTerms[i] = d.legacy
	}
	vocabName := legacyVocabularyName(userID)
	if dict != nil && dict.VocabularyName != "" {
		vocabName = dict.VocabularyName
	}
	blob := &model.UserDictionary{
		PK: model.PrefixUser + userID, SK: model.PrefixDictionary, UserID: userID, Terms: blobTerms,
		VocabularyName: vocabName, VocabularyStatus: "PENDING",
		UpdatedAt: time.Now().UTC().Format(time.RFC3339), EntityType: "DICTIONARY",
	}
	if err := s.repo.SaveDictionary(ctx, blob); err != nil {
		return nil, fmt.Errorf("failed to save dictionary: %w", err)
	}
	if err := s.ensureMigrated(ctx, userID); err != nil {
		return nil, err
	}
	if err := s.reconcileManualRows(ctx, userID, desired); err != nil {
		return nil, err
	}
	views, warnings := s.EnsureVocabularies(ctx, userID, model.VocabContextPersonal, true)
	resp, _, _, err := s.readDictionary(ctx, userID)
	if err != nil {
		return nil, err
	}
	if plan.ignored {
		warnings = append([]string{legacyIgnoredWarning}, warnings...)
	}
	resp.Vocabularies, resp.Warnings = views, warnings
	return resp, nil
}

func (s *DictionaryService) reconcileManualRows(ctx context.Context, userID string, desired []legacyDesired) error {
	pk := model.PrefixUser + userID
	rows, err := s.repo.ListDictTerms(ctx, pk)
	if err != nil {
		return err
	}
	byKey := make(map[string]model.DictTerm, len(rows))
	for _, row := range rows {
		byKey[row.TermKey] = row
	}
	want := make(map[string]bool, len(desired))
	base := s.now()
	for i, d := range desired {
		want[d.v.TermKey] = true
		row, ok := byKey[d.v.TermKey]
		fresh := newManualRow(userID, d.v, d.phrase, model.TermAliasHint, d.displayEmpty,
			base.Add(time.Duration(i)*time.Microsecond).Format(model.DictTimeLayout))
		switch {
		case !ok:
			err = s.repo.CreateDictTerm(ctx, &fresh, nil)
		case row.State == model.TermStateDeleted:
			err = s.repo.RestoreDictTerm(ctx, &fresh, row.Version, nil)
		case row.Origin != model.TermOriginManual || d.unchanged || legacyTermFromRow(row) == d.legacy:
			continue // auto and suggested rows are never touched
		default:
			ko := d.phrase != ""
			heard := d.v.HeardAs
			err = s.repo.UpdateDictTerm(ctx, pk, d.v.TermKey, repository.DictTermChange{
				DisplayAs: &d.v.DisplayAs, LegacyDisplayEmpty: &d.displayEmpty,
				Phrase: &d.phrase, HeardAs: &heard, TranscribeKo: &ko,
			}, userID, s.stamp(), row.Version, nil)
		}
		if err != nil {
			return versionConflict(err)
		}
	}
	for _, row := range rows {
		if row.Origin == model.TermOriginManual && row.State == model.TermStateActive && !want[row.TermKey] {
			if err := s.repo.DeleteManualTerm(ctx, pk, row.TermKey, row.Version, nil); err != nil {
				return versionConflict(err)
			}
		}
	}
	return nil
}

// DeleteTerm is the legacy DELETE by phrase. It hard-deletes matching active
// manual rows only; auto or suggested matches return ErrDictionaryTermNotManual.
// A blob that cannot migrate keeps the pre-v2 blob behavior.
func (s *DictionaryService) DeleteTerm(ctx context.Context, userID, phrase string) error {
	err := s.ensureMigrated(ctx, userID)
	if errors.Is(err, ErrLegacyDictionaryConflict) {
		if err := s.deleteLegacyBlobTerm(ctx, userID, phrase); err != nil {
			return err
		}
		// The delete may have removed the conflict; retry migration.
		if err := s.ensureMigrated(ctx, userID); err != nil && !errors.Is(err, ErrLegacyDictionaryConflict) {
			log.Printf("dictionary migration deferred for user %s: %v", userID, err)
		}
		return nil
	}
	if err != nil {
		return err
	}
	pk := model.PrefixUser + userID
	rows, err := s.repo.ListDictTerms(ctx, pk)
	if err != nil {
		return err
	}
	matched, other := 0, false
	for _, row := range rows {
		if row.State == model.TermStateDeleted || row.Phrase != phrase {
			continue
		}
		if row.Origin != model.TermOriginManual || row.State != model.TermStateActive {
			other = true
			continue
		}
		if err := s.repo.DeleteManualTerm(ctx, pk, row.TermKey, row.Version, nil); err != nil {
			return versionConflict(err)
		}
		matched++
	}
	switch {
	case matched == 0 && other:
		return ErrDictionaryTermNotManual
	case matched == 0:
		return ErrNotFound
	}
	if _, warnings := s.EnsureVocabularies(ctx, userID, model.VocabContextPersonal, true); len(warnings) > 0 {
		log.Printf("vocabulary update after legacy delete for user %s: %d warnings", userID, len(warnings))
	}
	return nil
}
