package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

// dictTarget is the partition a layered read or write acts on. member is set
// for Account partitions only, where the repository transaction re-checks it.
type dictTarget struct {
	pk     string
	scope  string
	member *repository.MembershipCheck
}

func personalTarget(userID string) dictTarget {
	return dictTarget{pk: model.PrefixUser + userID, scope: model.TermScopeUser}
}

func (t dictTarget) personal() bool { return t.scope == model.TermScopeUser }

// ensureTargetMigrated migrates the legacy blob before a personal write;
// other partitions have no legacy form.
func (s *DictionaryService) ensureTargetMigrated(ctx context.Context, userID string, t dictTarget) error {
	if !t.personal() {
		return nil
	}
	return s.ensureMigrated(ctx, userID)
}

// targetResult answers a write; only personal writes touch the personal
// vocabulary here.
func (s *DictionaryService) targetResult(ctx context.Context, userID string, t dictTarget, row *model.DictTerm, rebuild bool) *model.DictTermMutationResponse {
	if t.personal() {
		return s.mutationResult(ctx, userID, row, rebuild)
	}
	view := termView(*row)
	return &model.DictTermMutationResponse{Item: &view, TermID: row.TermKey, State: row.State}
}

// SetMembershipReader sets the Account membership lookup used by Account
// layers (wired to the account repository's GetMember). Unset, every Account
// layer is ErrForbidden.
func (s *DictionaryService) SetMembershipReader(fn func(ctx context.Context, accountID, userID string) (bool, error)) {
	s.isMember = fn
}

// requireMember is the early Account check; the write transaction's
// ConditionCheck stays the authority.
func (s *DictionaryService) requireMember(ctx context.Context, accountID, userID string) error {
	if s.isMember == nil || userID == "" {
		return ErrForbidden
	}
	ok, err := s.isMember(ctx, accountID, userID)
	if err != nil {
		return fmt.Errorf("failed to check account membership: %w", err)
	}
	if !ok {
		return ErrForbidden
	}
	return nil
}

// resolveLayer maps personal, common or account:{id} to its partition,
// checking membership for an Account.
func (s *DictionaryService) resolveLayer(ctx context.Context, userID, layer string) (dictTarget, error) {
	switch {
	case layer == model.DictLayerPersonal:
		return personalTarget(userID), nil
	case layer == model.DictLayerCommon:
		return dictTarget{pk: model.PrefixDictCommon, scope: model.TermScopeCommon}, nil
	case strings.HasPrefix(layer, model.DictLayerAccountPrefix):
		accountID := strings.TrimPrefix(layer, model.DictLayerAccountPrefix)
		if accountID == "" || strings.ContainsAny(accountID, "#/") {
			break
		}
		if err := s.requireMember(ctx, accountID, userID); err != nil {
			return dictTarget{}, err
		}
		return dictTarget{
			pk: model.PrefixAccount + accountID, scope: model.TermScopeAccount,
			member: &repository.MembershipCheck{AccountID: accountID, UserID: userID},
		}, nil
	}
	return dictTarget{}, termInputError("layer", "must be personal, common or account:{id}")
}

// ListLayer lists one layer's non-tombstone terms, oldest first. Like GET,
// the personal layer first migrates a legacy blob (best effort) and hides
// pristine legacy copies while the blob stays unmigrated. A migration that
// fails while the blob still holds unmigrated terms is returned as its error
// (ErrLegacyDictionaryConflict for a blob that cannot migrate) rather than a
// silently empty or partial list; GET still serves the legacy terms.
func (s *DictionaryService) ListLayer(ctx context.Context, userID, layer string) ([]model.DictTermView, error) {
	t, err := s.resolveLayer(ctx, userID, layer)
	if err != nil {
		return nil, err
	}
	var dict *model.UserDictionary
	if t.personal() {
		_, migrateErr := s.MigrateLegacyDictionary(ctx, userID)
		if dict, err = s.repo.GetDictionary(ctx, userID); err != nil {
			return nil, fmt.Errorf("failed to get dictionary: %w", err)
		}
		if migrateErr != nil && dict != nil && dict.MigratedAt == "" && len(dict.Terms) > 0 {
			if errors.Is(migrateErr, ErrLegacyDictionaryConflict) {
				return nil, migrateErr
			}
			return nil, fmt.Errorf("dictionary migration pending: %w", migrateErr)
		}
	}
	rows, err := s.repo.ListDictTerms(ctx, t.pk)
	if err != nil {
		return nil, fmt.Errorf("failed to get dictionary terms: %w", err)
	}
	return termViews(dict, rows), nil
}

// ListDeletedCommon lists Common tombstones so a deleted term can be found
// and restored. Each carries the display fields of the term as it was before
// its latest audited change (not the cleared tombstone) plus the deletion's
// time and actor. A tombstone with no audited earlier state cannot be
// restored and is left out. Newest deletion first.
func (s *DictionaryService) ListDeletedCommon(ctx context.Context) ([]model.DictDeletedTermView, error) {
	rows, err := s.repo.ListDictTerms(ctx, model.PrefixDictCommon)
	if err != nil {
		return nil, fmt.Errorf("failed to get dictionary terms: %w", err)
	}
	out := []model.DictDeletedTermView{}
	for _, row := range rows {
		if row.State != model.TermStateDeleted {
			continue
		}
		logs, err := s.repo.ListDictTermLog(ctx, model.PrefixDictCommon, row.TermKey)
		if err != nil {
			return nil, err
		}
		// Rows are newest first; the latest one with a live earlier state wins.
		for _, entry := range logs {
			prev := snapshotView(entry.Before)
			if prev == nil || prev.State == model.TermStateDeleted {
				continue
			}
			out = append(out, model.DictDeletedTermView{
				TermID: row.TermKey, DisplayAs: prev.DisplayAs, Phrase: prev.Phrase, Type: prev.Type,
				DeletedAt: row.DeletedAt, DeletedBy: row.UpdatedBy,
			})
			break
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DeletedAt > out[j].DeletedAt })
	return out, nil
}

// CreateLayerTerm creates a manual term in the personal or an Account layer.
// Common terms come from automatic sources only.
func (s *DictionaryService) CreateLayerTerm(ctx context.Context, userID, layer string, in model.CreateDictTermRequest) (*model.DictTermMutationResponse, error) {
	t, err := s.resolveLayer(ctx, userID, layer)
	if err != nil {
		return nil, err
	}
	if t.scope == model.TermScopeCommon {
		return nil, termInputError("layer", "common terms cannot be created directly")
	}
	return s.createTerm(ctx, userID, t, in)
}

// UpdateLayerTerm applies a PATCH body in any layer; Common edits are audited.
func (s *DictionaryService) UpdateLayerTerm(ctx context.Context, userID, layer, termKey string, in model.UpdateDictTermRequest) (*model.DictTermMutationResponse, error) {
	t, err := s.resolveLayer(ctx, userID, layer)
	if err != nil {
		return nil, err
	}
	if t.scope != model.TermScopeCommon {
		return s.updateTerm(ctx, userID, t, termKey, in)
	}
	if in.Version == nil {
		return nil, termInputError("version", "is required")
	}
	if in.DisplayAs == nil && in.Phrase == nil && in.HeardAs == nil && in.Type == nil && in.AliasMode == nil && in.State == nil {
		return nil, termInputError("body", "no changes")
	}
	row, err := s.liveCommonTerm(ctx, termKey)
	if err != nil {
		return nil, err
	}
	change, err := termChange(*row, in)
	if err != nil {
		return nil, err
	}
	if err := s.UpdateCommonTerm(ctx, userID, termKey, change, *in.Version); err != nil {
		return nil, err
	}
	return s.commonResult(ctx, termKey)
}

// DeleteLayerTerm tombstones a term in any layer; Common deletes are audited.
func (s *DictionaryService) DeleteLayerTerm(ctx context.Context, userID, layer, termKey string, version *int64) (*model.DictTermMutationResponse, error) {
	t, err := s.resolveLayer(ctx, userID, layer)
	if err != nil {
		return nil, err
	}
	if t.scope != model.TermScopeCommon {
		return s.tombstoneTerm(ctx, userID, t, termKey, version)
	}
	warnings, err := s.deleteCommon(ctx, userID, termKey, version)
	if err != nil {
		return nil, err
	}
	return &model.DictTermMutationResponse{TermID: termKey, State: model.TermStateDeleted, Warnings: warnings}, nil
}

// RestoreLayerTerm restores a Common term from one of its audit rows; logID
// is the row's timestamp. Other layers keep no audit and are ErrNotFound.
func (s *DictionaryService) RestoreLayerTerm(ctx context.Context, userID, layer, termKey, logID string) (*model.DictTermMutationResponse, error) {
	if layer != model.DictLayerCommon {
		return nil, ErrNotFound
	}
	if err := s.RestoreCommonTerm(ctx, userID, termKey, model.DictLogSK(termKey, logID)); err != nil {
		return nil, err
	}
	return s.commonResult(ctx, termKey)
}

func (s *DictionaryService) liveCommonTerm(ctx context.Context, termKey string) (*model.DictTerm, error) {
	row, err := s.repo.GetDictTerm(ctx, model.PrefixDictCommon, termKey)
	if err != nil {
		return nil, err
	}
	if row == nil || row.State == model.TermStateDeleted {
		return nil, ErrNotFound
	}
	return row, nil
}

func (s *DictionaryService) commonResult(ctx context.Context, termKey string) (*model.DictTermMutationResponse, error) {
	row, err := s.liveCommonTerm(ctx, termKey)
	if err != nil {
		return nil, err // committed, then removed concurrently
	}
	view := termView(*row)
	return &model.DictTermMutationResponse{Item: &view, TermID: termKey, State: row.State}, nil
}

// UpdateCommonTerm applies an edit to a Common term at expectedVersion (L2:
// any signed-in user). The new row and its audit row are written in one
// transaction, then the term's audit list is trimmed to the newest 20.
func (s *DictionaryService) UpdateCommonTerm(ctx context.Context, userID, termID string, change repository.DictTermChange, expectedVersion int64) error {
	if change.State != nil && *change.State == model.TermStateDeleted {
		return termInputError("state", "use DELETE to remove a term")
	}
	row, err := s.liveCommonTerm(ctx, termID)
	if err != nil {
		return err
	}
	if row.Version != expectedVersion {
		return ErrDictionaryVersionConflict
	}
	now := s.stamp()
	next := applyTermChange(*row, change, userID, now)
	_, err = s.writeCommon(ctx, userID, model.DictLogActionUpdate, row, &next, now)
	return err
}

// DeleteCommonTerm tombstones a Common term (a nil version uses the current
// row) with an audit row; observation cleanup and pruning are best effort.
func (s *DictionaryService) DeleteCommonTerm(ctx context.Context, userID, termID string, version *int64) error {
	_, err := s.deleteCommon(ctx, userID, termID, version)
	return err
}

// deleteCommon is DeleteCommonTerm plus the warnings of its best-effort
// steps, which the layer DELETE response carries like a personal delete.
func (s *DictionaryService) deleteCommon(ctx context.Context, userID, termID string, version *int64) ([]string, error) {
	row, err := s.liveCommonTerm(ctx, termID)
	if err != nil {
		return nil, err
	}
	if version != nil && *version != row.Version {
		return nil, ErrDictionaryVersionConflict
	}
	now := s.stamp()
	next := tombstoneOf(*row, userID, now)
	result, err := s.writeCommon(ctx, userID, model.DictLogActionDelete, row, &next, now)
	if err != nil {
		return nil, err
	}
	var warnings []string
	if result.CleanupErr != nil {
		log.Printf("common dictionary observation cleanup failed for term %s: %v", termID, result.CleanupErr)
		warnings = append(warnings, "term observation cleanup incomplete")
	}
	if err := s.pruneTombstones(ctx, model.PrefixDictCommon, nil); err != nil {
		log.Printf("common dictionary tombstone pruning failed: %v", err)
		warnings = append(warnings, "tombstone pruning incomplete")
	}
	return warnings, nil
}

// RestoreCommonTerm puts back the term as it was before the change recorded
// at logSK, whether the term is now live or a tombstone, and audits that.
func (s *DictionaryService) RestoreCommonTerm(ctx context.Context, userID, termID string, logSK string) error {
	rows, err := s.repo.ListDictTermLog(ctx, model.PrefixDictCommon, termID)
	if err != nil {
		return err
	}
	var entry *model.DictTermLog
	for i := range rows {
		if rows[i].SK == logSK {
			entry = &rows[i]
		}
	}
	if entry == nil {
		return ErrNotFound
	}
	if entry.Before == "" {
		return termInputError("logId", "has no earlier state to restore")
	}
	var prev model.DictTerm
	if err := json.Unmarshal([]byte(entry.Before), &prev); err != nil || prev.TermKey != termID || prev.State == model.TermStateDeleted {
		return termInputError("logId", "has no earlier state to restore")
	}
	current, err := s.repo.GetDictTerm(ctx, model.PrefixDictCommon, termID)
	if err != nil {
		return err
	}
	now := s.stamp()
	next := prev
	next.PK, next.SK, next.Scope = model.PrefixDictCommon, model.DictTermSK(termID), model.TermScopeCommon
	next.DeletedAt, next.UpdatedAt, next.UpdatedBy, next.UserEditedAt = "", now, userID, now
	next.Version = 1
	if current != nil {
		next.Version = current.Version + 1
	}
	_, err = s.writeCommon(ctx, userID, model.DictLogActionRestore, current, &next, now)
	return err
}

// ListTermLog lists a Common term's audit rows, newest first. Only the
// Common layer is audited; other layers are ErrNotFound.
func (s *DictionaryService) ListTermLog(ctx context.Context, layer, termID string) ([]model.DictTermLog, error) {
	if layer != model.DictLayerCommon {
		return nil, ErrNotFound
	}
	rows, err := s.repo.ListDictTermLog(ctx, model.PrefixDictCommon, termID)
	if err != nil {
		return nil, err
	}
	if rows == nil {
		rows = []model.DictTermLog{}
	}
	return rows, nil
}

// writeCommon writes next (version guarded on before, absent when nil) and
// its audit row in one transaction, then trims the audit list best effort:
// a failed trim leaves extra rows that the next write trims.
func (s *DictionaryService) writeCommon(ctx context.Context, userID, action string, before, next *model.DictTerm, now string) (repository.DictWriteResult, error) {
	entry := &model.DictTermLog{
		PK: model.PrefixDictCommon, SK: model.DictLogSK(next.TermKey, now), TermKey: next.TermKey,
		Actor: userID, Action: action, At: now,
	}
	var expected int64
	if before != nil {
		expected = before.Version
		raw, err := json.Marshal(before)
		if err != nil {
			return repository.DictWriteResult{}, err
		}
		entry.Before = string(raw)
	}
	raw, err := json.Marshal(next)
	if err != nil {
		return repository.DictWriteResult{}, err
	}
	entry.After = string(raw)
	result, err := s.repo.PutCommonDictTerm(ctx, next, expected, entry)
	if err != nil {
		return result, versionConflict(err)
	}
	if err := s.repo.TrimDictTermLog(ctx, model.PrefixDictCommon, next.TermKey, model.MaxDictLogsPerTerm); err != nil {
		log.Printf("common dictionary audit trim failed for term %s: %v", next.TermKey, err)
	}
	return result, nil
}

// applyTermChange is the row UpdateDictTerm stores: the set fields, the
// editor, userEditedAt and the next version.
func applyTermChange(row model.DictTerm, change repository.DictTermChange, actor, now string) model.DictTerm {
	setS := func(dst *string, src *string) {
		if src != nil {
			*dst = *src
		}
	}
	setB := func(dst *bool, src *bool) {
		if src != nil {
			*dst = *src
		}
	}
	setS(&row.DisplayAs, change.DisplayAs)
	setS(&row.Phrase, change.Phrase)
	setS(&row.AliasMode, change.AliasMode)
	setS(&row.Type, change.Type)
	setS(&row.State, change.State)
	setB(&row.TranscribeKo, change.TranscribeKo)
	setB(&row.TranscribeEn, change.TranscribeEn)
	setB(&row.LegacyDisplayEmpty, change.LegacyDisplayEmpty)
	if change.HeardAs != nil {
		row.HeardAs = append([]string(nil), (*change.HeardAs)...)
	}
	row.UserEditedAt, row.UpdatedBy, row.UpdatedAt = now, actor, now
	row.Version++
	return row
}

// tombstoneOf is the row TombstoneDictTerm stores: content, counters and
// recognition flags cleared; key, scope, origin, sources and times kept.
func tombstoneOf(row model.DictTerm, actor, now string) model.DictTerm {
	row.State, row.DeletedAt, row.UpdatedAt, row.UpdatedBy = model.TermStateDeleted, now, now, actor
	row.DisplayAs, row.Phrase, row.HeardAs, row.SourceTypes = "", "", nil, nil
	row.FirstSourceMeetingID, row.Confidence, row.Type, row.AliasMode = "", 0, "", ""
	row.LastSeenAt, row.LegacyDisplayEmpty = "", false
	row.Hits, row.TranscribeKo, row.TranscribeEn = 0, false, false
	row.Version++
	return row
}

// TermLogView is the API form of an audit row; LogID is its timestamp.
func TermLogView(row model.DictTermLog) model.DictTermLogView {
	view := model.DictTermLogView{
		LogID: strings.TrimPrefix(row.SK, model.PrefixDictLog+row.TermKey+"#"), TermID: row.TermKey,
		Actor: row.Actor, Action: row.Action, At: row.At,
	}
	view.Before, view.After = snapshotView(row.Before), snapshotView(row.After)
	return view
}

func snapshotView(raw string) *model.DictTermView {
	if raw == "" {
		return nil
	}
	var term model.DictTerm
	if err := json.Unmarshal([]byte(raw), &term); err != nil {
		return nil
	}
	view := termView(term)
	return &view
}
