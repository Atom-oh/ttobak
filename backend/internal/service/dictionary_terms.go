package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

// Dictionary term CRUD outcomes; handlers map each to a 409 code.
var (
	ErrDictionaryVersionConflict = errors.New("dictionary term changed concurrently")
	ErrDictionaryFull            = errors.New("dictionary is full")
	ErrDictionaryTermExists      = errors.New("dictionary term already exists")
)

// Per-scope caps; tombstones beyond the cap are pruned oldest first.
const (
	MaxManualTermsPerScope = 500
	MaxTombstonesPerScope  = 2000
)

const vocabularyRebuildWarning = "vocabulary rebuild failed; recognition keeps the previous vocabulary"

func (s *DictionaryService) stamp() string { return s.now().Format(model.DictTimeLayout) }

func termView(row model.DictTerm) model.DictTermView {
	heard := row.HeardAs
	if heard == nil {
		heard = []string{}
	}
	return model.DictTermView{
		TermID: row.TermKey, DisplayAs: row.DisplayAs, Phrase: row.Phrase, HeardAs: heard,
		AliasMode: row.AliasMode, Type: row.Type, Origin: row.Origin, State: row.State,
		UserEdited: row.UserEditedAt != "", Version: row.Version,
		TranscribeKo: row.TranscribeKo, TranscribeEn: row.TranscribeEn, Hits: row.Hits,
		AddedAt: row.AddedAt, UpdatedAt: row.UpdatedAt,
	}
}

func sortRowsByAdded(rows []model.DictTerm) {
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].AddedAt != rows[j].AddedAt {
			return rows[i].AddedAt < rows[j].AddedAt
		}
		return rows[i].SK < rows[j].SK
	})
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
	if err := s.ensureMigrated(ctx, userID); err != nil {
		return nil, err
	}
	pk := model.PrefixUser + userID
	rows, err := s.repo.ListDictTerms(ctx, pk)
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
	if existing == nil {
		if err := s.repo.CreateDictTerm(ctx, &row); err != nil {
			if errors.Is(err, repository.ErrConditionFailed) {
				return nil, ErrDictionaryTermExists
			}
			return nil, err
		}
	} else {
		if err := s.repo.RestoreDictTerm(ctx, &row, existing.Version); err != nil {
			return nil, versionConflict(err)
		}
		row.Version = existing.Version + 1
	}
	return s.mutationResult(ctx, userID, &row, phrase != ""), nil
}

// UpdateTerm applies a user edit under the caller's version. Every edit sets
// userEditedAt, which blocks all later automatic writes; aliasMode=always is
// the "keep" action, and state=active accepts a suggestion.
func (s *DictionaryService) UpdateTerm(ctx context.Context, userID, termKey string, in model.UpdateDictTermRequest) (*model.DictTermMutationResponse, error) {
	if in.Version == nil {
		return nil, termInputError("version", "is required")
	}
	if in.DisplayAs == nil && in.Phrase == nil && in.HeardAs == nil && in.Type == nil && in.AliasMode == nil && in.State == nil {
		return nil, termInputError("body", "no changes")
	}
	if err := s.ensureMigrated(ctx, userID); err != nil {
		return nil, err
	}
	pk := model.PrefixUser + userID
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
	if err := s.repo.UpdateDictTerm(ctx, pk, termKey, change, userID, s.stamp(), *in.Version); err != nil {
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
	return s.mutationResult(ctx, userID, updated, in.Phrase != nil || in.State != nil || in.DisplayAs != nil), nil
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
	if err := s.ensureMigrated(ctx, userID); err != nil {
		return nil, err
	}
	pk := model.PrefixUser + userID
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
	result, err := s.repo.TombstoneDictTerm(ctx, pk, termKey, userID, s.stamp(), row.Version)
	if err != nil {
		return nil, versionConflict(err)
	}
	resp := &model.DictTermMutationResponse{TermID: termKey, State: model.TermStateDeleted}
	if result.CleanupErr != nil {
		log.Printf("dictionary observation cleanup failed for user %s: %v", userID, result.CleanupErr)
		resp.Warnings = append(resp.Warnings, "term observation cleanup incomplete")
	}
	if err := s.pruneTombstones(ctx, pk); err != nil {
		log.Printf("dictionary tombstone pruning failed for user %s: %v", userID, err)
		resp.Warnings = append(resp.Warnings, "tombstone pruning incomplete")
	}
	if row.State == model.TermStateActive && row.TranscribeKo && row.Phrase != "" {
		status, warnings := s.rebuildPersonalVocabulary(ctx, userID)
		resp.Status, resp.Warnings = status, append(resp.Warnings, warnings...)
	} else {
		resp.Status = s.currentVocabularyStatus(ctx, userID)
	}
	return resp, nil
}

func (s *DictionaryService) pruneTombstones(ctx context.Context, pk string) error {
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
		if err := s.repo.DeleteTombstone(ctx, pk, tomb.TermKey, tomb.Version); err != nil && !errors.Is(err, repository.ErrConditionFailed) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (s *DictionaryService) mutationResult(ctx context.Context, userID string, row *model.DictTerm, rebuild bool) *model.DictTermMutationResponse {
	view := termView(*row)
	resp := &model.DictTermMutationResponse{Item: &view, TermID: row.TermKey, State: row.State}
	if rebuild {
		resp.Status, resp.Warnings = s.rebuildPersonalVocabulary(ctx, userID)
	} else {
		resp.Status = s.currentVocabularyStatus(ctx, userID)
	}
	return resp
}

func (s *DictionaryService) currentVocabularyStatus(ctx context.Context, userID string) string {
	state, err := s.repo.GetVocabularyState(ctx, userID, model.VocabContextPersonal, model.VocabLangKo)
	if err != nil || state == nil {
		return ""
	}
	return legacyVocabularyStatus(state)
}

// vocabularyPhrases lists the in-place vocabulary's phrases: active rows with
// a Transcribe phrase, oldest first, exact duplicates once.
func vocabularyPhrases(rows []model.DictTerm) []model.DictionaryTerm {
	active := make([]model.DictTerm, 0, len(rows))
	for _, row := range rows {
		if row.State == model.TermStateActive && row.TranscribeKo && row.Phrase != "" {
			active = append(active, row)
		}
	}
	sortRowsByAdded(active)
	seen := map[string]bool{}
	var phrases []model.DictionaryTerm
	for _, row := range active {
		if !seen[row.Phrase] {
			seen[row.Phrase] = true
			phrases = append(phrases, model.DictionaryTerm{Phrase: row.Phrase})
		}
	}
	return phrases
}

// rebuildPersonalVocabulary keeps the pre-v2 in-place rebuild for manual
// changes and records it in VOCAB#personal#ko-KR under its version guard.
func (s *DictionaryService) rebuildPersonalVocabulary(ctx context.Context, userID string) (string, []string) {
	pk := model.PrefixUser + userID
	rows, err := s.repo.ListDictTerms(ctx, pk)
	if err != nil {
		log.Printf("vocabulary rebuild skipped for user %s: %v", userID, err)
		return s.currentVocabularyStatus(ctx, userID), []string{vocabularyRebuildWarning}
	}
	state, err := s.personalVocabularyState(ctx, userID)
	if err != nil {
		log.Printf("vocabulary rebuild skipped for user %s: %v", userID, err)
		return "", []string{vocabularyRebuildWarning}
	}
	if state.InactiveName != "" { // an A/B build owns this row
		return legacyVocabularyStatus(state), nil
	}
	phrases := vocabularyPhrases(rows)
	name := state.ActiveName
	if name == "" {
		name = legacyVocabularyName(userID)
	}
	status, failure, warnings := model.VocabStatusBuilding, "", []string(nil)
	switch {
	case len(phrases) == 0:
		s.deleteTranscribeVocabulary(ctx, name)
		status, name = model.VocabStatusNone, ""
	default:
		if err := s.buildTranscribeVocabulary(ctx, userID, name, phrases); err != nil {
			log.Printf("failed to build vocabulary for user %s: %v", userID, err)
			status, failure, warnings = model.VocabStatusFailed, "PROVIDER", []string{vocabularyRebuildWarning}
		}
	}
	notReady, count := false, len(phrases)
	change := repository.VocabularyStateChange{ActiveName: &name, Status: &status, ActiveReady: &notReady, FailureCode: &failure, TermCount: &count}
	for attempt := 0; attempt < 3; attempt++ {
		err = s.repo.UpdateVocabularyState(ctx, userID, model.VocabContextPersonal, model.VocabLangKo, state.Version, change, s.stamp())
		if !errors.Is(err, repository.ErrConditionFailed) {
			break
		}
		if state, err = s.personalVocabularyState(ctx, userID); err != nil {
			break
		}
	}
	if err != nil {
		log.Printf("failed to record vocabulary build for user %s: %v", userID, err)
		warnings = append(warnings, vocabularyRebuildWarning)
	}
	return legacyVocabularyStatus(&model.VocabularyState{Status: status}), warnings
}

func (s *DictionaryService) personalVocabularyState(ctx context.Context, userID string) (*model.VocabularyState, error) {
	for attempt := 0; attempt < 2; attempt++ {
		state, err := s.repo.GetVocabularyState(ctx, userID, model.VocabContextPersonal, model.VocabLangKo)
		if err != nil || state != nil {
			return state, err
		}
		err = s.repo.CreateMigratedVocabularyState(ctx, userID, &model.VocabularyState{
			PK: model.PrefixUser + userID, SK: model.VocabSK(model.VocabContextPersonal, model.VocabLangKo),
			Context: model.VocabContextPersonal, Language: model.VocabLangKo, Status: model.VocabStatusNone,
			UpdatedAt: s.stamp(), Version: 1, EntityType: model.EntityTypeVocabularyState,
		})
		if err != nil && !errors.Is(err, repository.ErrConditionFailed) {
			return nil, err
		}
	}
	return nil, fmt.Errorf("vocabulary state unavailable: %w", repository.ErrConditionFailed)
}
