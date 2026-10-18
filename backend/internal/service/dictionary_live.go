package service

import (
	"context"
	"log"

	"github.com/ttobak/backend/internal/model"
)

const liveVocabularyWarning = "Custom vocabulary is unavailable for live captions; recognition continues without it"

// LiveDictionaryConfig lists the personal ko-KR/en-US vocabulary names that
// Transcribe confirms READY after one EnsureVocabularies step (which may flip a
// finished build). ko-KR keeps the batch reader's legacy fallback. A stored-state
// or Transcribe lookup failure omits the name and adds liveVocabularyWarning.
func (s *DictionaryService) LiveDictionaryConfig(ctx context.Context, userID string) *model.LiveDictionaryConfig {
	views, warnings := s.EnsureVocabularies(ctx, userID, model.VocabContextPersonal, false)
	out := &model.LiveDictionaryConfig{
		Vocabularies: model.LiveVocabularyViews{
			Ko: model.LiveVocabularyView{Status: views.Ko.Status},
			En: model.LiveVocabularyView{Status: views.En.Status},
		},
		Warnings: warnings,
	}
	ko, providerErr, err := s.koVocabularyForTranscription(ctx, userID)
	if err == nil {
		err = providerErr
	}
	if err != nil {
		log.Printf("live ko-KR vocabulary lookup failed for user %s: %v", userID, err)
		out.Warnings = append(out.Warnings, liveVocabularyWarning)
	}
	out.Vocabularies.Ko.Name = ko
	en, err := s.repo.GetVocabularyState(ctx, userID, model.VocabContextPersonal, model.VocabLangEn)
	if err == nil && en != nil && en.ActiveReady && en.ActiveName != "" {
		out.Vocabularies.En.Name, err = s.confirmReady(ctx, en.ActiveName, true)
	}
	if err != nil {
		log.Printf("live en-US vocabulary lookup failed for user %s: %v", userID, err)
		out.Warnings = append(out.Warnings, liveVocabularyWarning)
	}
	out.Warnings = dedupeWarnings(out.Warnings)
	return out
}
