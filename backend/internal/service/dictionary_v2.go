package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	"golang.org/x/text/unicode/norm"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

// NormalizeTermKey is the comparison form of a term: NFKC, lowercase, with
// whitespace and the separators - . _ · removed.
func NormalizeTermKey(s string) string {
	folded := strings.ToLower(norm.NFKC.String(s))
	var b strings.Builder
	b.Grow(len(folded))
	for _, r := range folded {
		if unicode.IsSpace(r) {
			continue
		}
		switch r {
		case '-', '.', '_', '·':
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// TermKey is the 32-lowercase-hex identity of a term; "" when the input has
// no comparable characters.
func TermKey(displayAs string) string {
	normalized := NormalizeTermKey(displayAs)
	if normalized == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(sum[:])[:32]
}

// legacyTermKey identifies a blob term by its display form, falling back to
// the phrase as the legacy UI does.
func legacyTermKey(t model.DictionaryTerm) string {
	display := t.DisplayAs
	if display == "" {
		display = t.Phrase
	}
	return TermKey(display)
}

func legacyTermFromRow(row model.DictTerm) model.DictionaryTerm {
	display := row.DisplayAs
	if row.LegacyDisplayEmpty {
		display = ""
	}
	return model.DictionaryTerm{
		Phrase:     row.Phrase,
		SoundsLike: strings.Join(row.HeardAs, ", "),
		DisplayAs:  display,
	}
}

// pristineLegacyRow is an active, unedited copy of a blob term. Until
// migratedAt the blob is authoritative over such rows.
func pristineLegacyRow(row model.DictTerm) bool {
	return row.LegacyMigrated && row.State == model.TermStateActive &&
		row.Origin == model.TermOriginManual && row.UserEditedAt == ""
}

// unionLegacyTerms lists active rows plus, before migration, blob terms whose
// key has no row (any other row, including a tombstone, hides the blob term).
// Before migration pristine legacy copies are ignored, so a partial migration
// never keeps a term the blob has since dropped or changed.
func unionLegacyTerms(dict *model.UserDictionary, rows []model.DictTerm) []model.DictionaryTerm {
	premigration := dict != nil && dict.MigratedAt == ""
	if premigration {
		kept := rows[:0:0]
		for _, row := range rows {
			if !pristineLegacyRow(row) {
				kept = append(kept, row)
			}
		}
		rows = kept
	}
	byKey := make(map[string]model.DictTerm, len(rows))
	for _, row := range rows {
		byKey[row.TermKey] = row
	}
	terms := []model.DictionaryTerm{}
	used := make(map[string]bool, len(rows))
	if premigration {
		for _, t := range dict.Terms {
			key := legacyTermKey(t)
			row, ok := byKey[key]
			if key == "" || !ok {
				terms = append(terms, t)
				continue
			}
			if !used[key] && row.State == model.TermStateActive {
				terms = append(terms, legacyTermFromRow(row))
			}
			used[key] = true
		}
	}
	extra := make([]model.DictTerm, 0, len(rows))
	for _, row := range rows {
		if !used[row.TermKey] && row.State == model.TermStateActive {
			extra = append(extra, row)
		}
	}
	sort.SliceStable(extra, func(i, j int) bool {
		if extra[i].AddedAt != extra[j].AddedAt {
			return extra[i].AddedAt < extra[j].AddedAt
		}
		return extra[i].SK < extra[j].SK
	})
	for _, row := range extra {
		terms = append(terms, legacyTermFromRow(row))
	}
	return terms
}

// LegacyMigrationResult reports what one migration run did.
type LegacyMigrationResult struct {
	AlreadyMigrated bool
	NoDictionary    bool
	TermsCreated    int
}

// legacyVocabStatus maps a blob status to a VOCAB# build status.
func legacyVocabStatus(status string) string {
	switch status {
	case "READY":
		return model.VocabStatusReady
	case "PENDING":
		return model.VocabStatusBuilding
	case "FAILED":
		return model.VocabStatusFailed
	}
	return model.VocabStatusNone
}

// ErrLegacyDictionaryConflict means the blob holds terms that one row per
// termKey cannot represent without loss (distinct terms sharing a key, or a
// term with no comparable characters). Migration then writes nothing.
var ErrLegacyDictionaryConflict = errors.New("legacy dictionary terms cannot be migrated without loss")

// legacyMigrationAttempts bounds re-runs when the blob changes mid-migration.
const legacyMigrationAttempts = 3

// legacyBlobRows converts blob terms into pristine legacy rows. Exact
// duplicates collapse; any other shared or empty key is counted as a
// conflict. AddedAt steps by a microsecond so rows keep blob order.
func legacyBlobRows(userID string, dict *model.UserDictionary, base time.Time) ([]model.DictTerm, int) {
	pk := model.PrefixUser + userID
	seen := map[string]model.DictionaryTerm{}
	var rows []model.DictTerm
	conflicts := 0
	for _, t := range dict.Terms {
		key := legacyTermKey(t)
		if key == "" {
			conflicts++
			continue
		}
		if prior, ok := seen[key]; ok {
			if prior != t {
				conflicts++
			}
			continue
		}
		seen[key] = t
		display := t.DisplayAs
		if display == "" {
			display = t.Phrase
		}
		var heard []string
		if strings.TrimSpace(t.SoundsLike) != "" {
			heard = []string{t.SoundsLike}
		}
		stamp := base.Add(time.Duration(len(rows)) * time.Microsecond).Format(model.DictTimeLayout)
		rows = append(rows, model.DictTerm{
			PK: pk, SK: model.DictTermSK(key), TermKey: key,
			Scope: model.TermScopeUser, State: model.TermStateActive,
			DisplayAs: display, Phrase: t.Phrase, HeardAs: heard,
			AliasMode: model.TermAliasHint, Origin: model.TermOriginManual,
			TranscribeKo: t.Phrase != "",
			AddedAt:      stamp, UpdatedAt: stamp,
			CreatedBy: userID, UpdatedBy: userID,
			Version: 1, EntityType: model.EntityTypeDictTerm,
			LegacyMigrated: true, LegacyDisplayEmpty: t.DisplayAs == "",
		})
	}
	return rows, conflicts
}

func sameLegacyContent(a, b model.DictTerm) bool {
	return a.Phrase == b.Phrase && a.DisplayAs == b.DisplayAs &&
		a.LegacyDisplayEmpty == b.LegacyDisplayEmpty && a.TranscribeKo == b.TranscribeKo &&
		strings.Join(a.HeardAs, "\x00") == strings.Join(b.HeardAs, "\x00") && len(a.HeardAs) == len(b.HeardAs)
}

// MigrateLegacyDictionary copies blob terms into pristine origin=manual rows
// (other existing rows and tombstones win), reconciles copies left by an
// earlier partial run, adopts the blob's vocabulary name in
// VOCAB#personal#ko-KR, and sets migratedAt last, pinned to the blob revision
// it copied. A blob change before the mark re-runs the copy. It never removes
// blob fields and is safe to re-run.
func (s *DictionaryService) MigrateLegacyDictionary(ctx context.Context, userID string) (LegacyMigrationResult, error) {
	var result LegacyMigrationResult
	for attempt := 0; attempt < legacyMigrationAttempts; attempt++ {
		dict, err := s.repo.GetDictionary(ctx, userID)
		if err != nil {
			return result, fmt.Errorf("failed to read legacy dictionary: %w", err)
		}
		if dict == nil {
			result.NoDictionary = true
			return result, nil
		}
		if dict.MigratedAt != "" {
			result.AlreadyMigrated = true
			return result, nil
		}
		done, err := s.migrateLegacyOnce(ctx, userID, dict, &result)
		if err != nil || done {
			return result, err
		}
	}
	return result, fmt.Errorf("legacy dictionary kept changing during migration: %w", repository.ErrConditionFailed)
}

// migrateLegacyOnce copies one observed blob; done is false when the blob
// changed before the mark and the copy must be redone.
func (s *DictionaryService) migrateLegacyOnce(ctx context.Context, userID string, dict *model.UserDictionary, result *LegacyMigrationResult) (bool, error) {
	pk := model.PrefixUser + userID
	now := s.now()
	rows, conflicts := legacyBlobRows(userID, dict, now)
	if conflicts > 0 {
		return false, fmt.Errorf("%w: %d terms", ErrLegacyDictionaryConflict, conflicts)
	}
	existing, err := s.repo.ListDictTerms(ctx, pk)
	if err != nil {
		return false, fmt.Errorf("failed to read dictionary terms: %w", err)
	}
	current := make(map[string]model.DictTerm, len(existing))
	for _, row := range existing {
		current[row.TermKey] = row
	}
	wanted := make(map[string]bool, len(rows))
	var missing []model.DictTerm
	for _, row := range rows {
		wanted[row.TermKey] = true
		prior, ok := current[row.TermKey]
		if !ok {
			missing = append(missing, row)
			continue
		}
		if pristineLegacyRow(prior) && !sameLegacyContent(prior, row) {
			next := row
			next.AddedAt = prior.AddedAt
			if err := s.repo.ReplaceLegacyMigratedTerm(ctx, pk, row.TermKey, prior.Version, &next); err != nil {
				return false, fmt.Errorf("failed to refresh legacy term: %w", err)
			}
		}
	}
	created, err := s.repo.PutDictTermsIfAbsent(ctx, missing)
	result.TermsCreated += created
	if err != nil {
		return false, fmt.Errorf("failed to copy legacy terms: %w", err)
	}
	for _, prior := range existing {
		if pristineLegacyRow(prior) && !wanted[prior.TermKey] {
			if err := s.repo.ReplaceLegacyMigratedTerm(ctx, pk, prior.TermKey, prior.Version, nil); err != nil {
				return false, fmt.Errorf("failed to remove stale legacy term: %w", err)
			}
		}
	}
	stamp := now.Format(model.DictTimeLayout)
	status := legacyVocabStatus(dict.VocabularyStatus)
	if dict.VocabularyName == "" {
		status = model.VocabStatusNone
	}
	phrases := 0
	for _, row := range rows {
		if row.Phrase != "" {
			phrases++
		}
	}
	state := &model.VocabularyState{
		PK: pk, SK: model.VocabSK(model.VocabContextPersonal, model.VocabLangKo),
		Context: model.VocabContextPersonal, Language: model.VocabLangKo,
		ActiveName: dict.VocabularyName, Status: status, ActiveReady: status == model.VocabStatusReady,
		TermCount: phrases, UpdatedAt: stamp, Version: 1, EntityType: model.EntityTypeVocabularyState,
	}
	if status == model.VocabStatusFailed {
		state.FailureCode = "UNKNOWN"
	}
	if err := s.repo.CreateVocabularyStateIfAbsent(ctx, state); err != nil && !errors.Is(err, repository.ErrConditionFailed) {
		return false, fmt.Errorf("failed to write vocabulary state: %w", err)
	}
	if err := s.repo.MarkDictionaryMigrated(ctx, userID, stamp, dict.BlobRevision, dict.UpdatedAt); err != nil {
		if errors.Is(err, repository.ErrConditionFailed) {
			return false, nil // blob changed or another run marked it: re-read
		}
		return false, fmt.Errorf("failed to mark dictionary migrated: %w", err)
	}
	return true, nil
}
