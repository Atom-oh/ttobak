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
	return model.DictionaryTerm{
		Phrase:     row.Phrase,
		SoundsLike: strings.Join(row.HeardAs, ", "),
		DisplayAs:  row.DisplayAs,
	}
}

// unionLegacyTerms lists active rows plus, before migration, blob terms whose
// key has no row (any row, including a tombstone, hides the blob term).
func unionLegacyTerms(dict *model.UserDictionary, rows []model.DictTerm) []model.DictionaryTerm {
	byKey := make(map[string]model.DictTerm, len(rows))
	for _, row := range rows {
		byKey[row.TermKey] = row
	}
	terms := []model.DictionaryTerm{}
	used := make(map[string]bool, len(rows))
	if dict != nil && dict.MigratedAt == "" {
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

// legacyBlobRows converts blob terms into manual rows (first per key); AddedAt
// steps by a microsecond so rows keep blob order.
func legacyBlobRows(userID string, dict *model.UserDictionary, base time.Time) []model.DictTerm {
	pk := model.PrefixUser + userID
	seen := map[string]bool{}
	var rows []model.DictTerm
	for _, t := range dict.Terms {
		key := legacyTermKey(t)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		display := t.DisplayAs
		if display == "" {
			display = t.Phrase
		}
		var heard []string
		if soundsLike := strings.TrimSpace(t.SoundsLike); soundsLike != "" {
			heard = []string{soundsLike}
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
		})
	}
	return rows
}

// MigrateLegacyDictionary copies blob terms into origin=manual rows (existing
// rows and tombstones win), adopts the blob's vocabulary name in
// VOCAB#personal#ko-KR, and sets migratedAt last. It never removes blob fields
// and is safe to re-run.
func (s *DictionaryService) MigrateLegacyDictionary(ctx context.Context, userID string) (LegacyMigrationResult, error) {
	dict, err := s.repo.GetDictionary(ctx, userID)
	if err != nil {
		return LegacyMigrationResult{}, fmt.Errorf("failed to read legacy dictionary: %w", err)
	}
	if dict == nil {
		return LegacyMigrationResult{NoDictionary: true}, nil
	}
	if dict.MigratedAt != "" {
		return LegacyMigrationResult{AlreadyMigrated: true}, nil
	}
	now := s.now()
	rows := legacyBlobRows(userID, dict, now)
	created, err := s.repo.PutDictTermsIfAbsent(ctx, rows)
	if err != nil {
		return LegacyMigrationResult{TermsCreated: created}, fmt.Errorf("failed to copy legacy terms: %w", err)
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
		PK: model.PrefixUser + userID, SK: model.VocabSK(model.VocabContextPersonal, model.VocabLangKo),
		Context: model.VocabContextPersonal, Language: model.VocabLangKo,
		ActiveName: dict.VocabularyName, Status: status, ActiveReady: status == model.VocabStatusReady,
		TermCount: phrases, UpdatedAt: stamp, Version: 1, EntityType: model.EntityTypeVocabularyState,
	}
	if status == model.VocabStatusFailed {
		state.FailureCode = "UNKNOWN"
	}
	if err := s.repo.CreateVocabularyStateIfAbsent(ctx, state); err != nil && !errors.Is(err, repository.ErrConditionFailed) {
		return LegacyMigrationResult{TermsCreated: created}, fmt.Errorf("failed to write vocabulary state: %w", err)
	}
	if err := s.repo.MarkDictionaryMigrated(ctx, userID, stamp); err != nil {
		if errors.Is(err, repository.ErrConditionFailed) {
			return LegacyMigrationResult{AlreadyMigrated: true, TermsCreated: created}, nil
		}
		return LegacyMigrationResult{TermsCreated: created}, fmt.Errorf("failed to mark dictionary migrated: %w", err)
	}
	return LegacyMigrationResult{TermsCreated: created}, nil
}
