package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/transcribe"
	transcribeTypes "github.com/aws/aws-sdk-go-v2/service/transcribe/types"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

// DictionaryStore is the persistence used by DictionaryService.
// *repository.DictionaryRepository implements it.
type DictionaryStore interface {
	GetDictionary(ctx context.Context, userID string) (*model.UserDictionary, error)
	SaveDictionary(ctx context.Context, dict *model.UserDictionary) error
	UpdateLegacyVocabularyStatus(ctx context.Context, userID, vocabName, from, to string) error
	MarkDictionaryMigrated(ctx context.Context, userID, now string, observedRevision int64, observedUpdatedAt string) error
	ListDictTerms(ctx context.Context, pk string) ([]model.DictTerm, error)
	PutDictTermsIfAbsent(ctx context.Context, guard repository.LegacyBlobGuard, terms []model.DictTerm) (int, error)
	ReplaceLegacyMigratedTerm(ctx context.Context, guard repository.LegacyBlobGuard, pk, termKey string, expectedVersion int64, next *model.DictTerm) error
	GetVocabularyState(ctx context.Context, userID, vocabContext, lang string) (*model.VocabularyState, error)
	CreateLegacyVocabularyState(ctx context.Context, guard repository.LegacyBlobGuard, state *model.VocabularyState) error
	ReconcileLegacyVocabularyState(ctx context.Context, guard repository.LegacyBlobGuard, vocabContext, lang string, expectedVersion int64, change repository.VocabularyStateChange, now string) error
	CreateMigratedVocabularyState(ctx context.Context, userID string, state *model.VocabularyState) error
	UpdateVocabularyState(ctx context.Context, userID, vocabContext, lang string, expectedVersion int64, change repository.VocabularyStateChange, now string) error
	GetDictTerm(ctx context.Context, pk, termKey string) (*model.DictTerm, error)
	CreateDictTerm(ctx context.Context, term *model.DictTerm) error
	UpdateDictTerm(ctx context.Context, pk, termKey string, change repository.DictTermChange, actor, now string, expectedVersion int64) error
	TombstoneDictTerm(ctx context.Context, pk, termKey, actor, now string, expectedVersion int64) (repository.DictWriteResult, error)
	RestoreDictTerm(ctx context.Context, term *model.DictTerm, expectedVersion int64) error
	DeleteManualTerm(ctx context.Context, pk, termKey string, expectedVersion int64) error
	DeleteTombstone(ctx context.Context, pk, termKey string, expectedVersion int64) error
}

// TranscribeVocabularyAPI is the subset of the Transcribe client used here.
type TranscribeVocabularyAPI interface {
	GetVocabulary(ctx context.Context, in *transcribe.GetVocabularyInput, optFns ...func(*transcribe.Options)) (*transcribe.GetVocabularyOutput, error)
	CreateVocabulary(ctx context.Context, in *transcribe.CreateVocabularyInput, optFns ...func(*transcribe.Options)) (*transcribe.CreateVocabularyOutput, error)
	UpdateVocabulary(ctx context.Context, in *transcribe.UpdateVocabularyInput, optFns ...func(*transcribe.Options)) (*transcribe.UpdateVocabularyOutput, error)
	DeleteVocabulary(ctx context.Context, in *transcribe.DeleteVocabularyInput, optFns ...func(*transcribe.Options)) (*transcribe.DeleteVocabularyOutput, error)
}

// DictionaryService handles custom dictionary business logic
type DictionaryService struct {
	repo             DictionaryStore
	transcribeClient TranscribeVocabularyAPI
	builder          *vocabularyBuilder // API Lambda only; nil never builds
	now              func() time.Time
}

// NewDictionaryService creates a new dictionary service
func NewDictionaryService(repo *repository.DictionaryRepository, transcribeClient *transcribe.Client) *DictionaryService {
	if transcribeClient == nil {
		return NewDictionaryServiceWithStore(repo, nil)
	}
	return NewDictionaryServiceWithStore(repo, transcribeClient)
}

// NewDictionaryServiceWithStore builds the service over any store; a nil
// client disables Transcribe status checks.
func NewDictionaryServiceWithStore(store DictionaryStore, client TranscribeVocabularyAPI) *DictionaryService {
	return &DictionaryService{repo: store, transcribeClient: client, now: func() time.Time { return time.Now().UTC() }}
}

// GetDictionary returns the legacy-shaped dictionary: the union of v2 term
// rows and the legacy blob (rows win) until the blob is migrated, then rows
// only. It never rewrites the blob; vocabulary status sync is a conditional
// update of the VOCAB# row (or of the blob status field before migration).
func (s *DictionaryService) GetDictionary(ctx context.Context, userID string) (*model.DictionaryResponse, error) {
	resp, _, _, err := s.readDictionary(ctx, userID)
	return resp, err
}

func (s *DictionaryService) readDictionary(ctx context.Context, userID string) (*model.DictionaryResponse, *model.UserDictionary, []model.DictTerm, error) {
	dict, err := s.repo.GetDictionary(ctx, userID)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to get dictionary: %w", err)
	}
	rows, err := s.repo.ListDictTerms(ctx, model.PrefixUser+userID)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to get dictionary terms: %w", err)
	}
	return &model.DictionaryResponse{
		Terms:  unionLegacyTerms(dict, rows),
		Status: s.personalVocabularyStatus(ctx, userID, dict),
	}, dict, rows, nil
}

// personalVocabularyStatus reports the legacy status string for the personal
// ko-KR vocabulary, preferring the VOCAB# row over the blob. The response
// carries no vocabulary name: the live client would send a ko-KR-only
// vocabulary on its ko-KR/en-US stream (ADR-008 acceptance item 5).
func (s *DictionaryService) personalVocabularyStatus(ctx context.Context, userID string, dict *model.UserDictionary) string {
	state, err := s.repo.GetVocabularyState(ctx, userID, model.VocabContextPersonal, model.VocabLangKo)
	if err != nil {
		log.Printf("failed to read vocabulary state for user %s: %v", userID, err)
	} else if state != nil {
		return legacyVocabularyStatus(s.syncVocabularyState(ctx, userID, state))
	}
	if dict == nil {
		return ""
	}
	return s.legacyBlobStatus(ctx, userID, dict)
}

// legacyBlobStatus reports and refreshes an unmigrated blob's status.
func (s *DictionaryService) legacyBlobStatus(ctx context.Context, userID string, dict *model.UserDictionary) string {
	status := dict.VocabularyStatus
	if status != "PENDING" || dict.VocabularyName == "" || s.transcribeClient == nil {
		return status
	}
	next, ok := s.providerStatus(ctx, dict.VocabularyName)
	if !ok || next == status {
		return status
	}
	if err := s.repo.UpdateLegacyVocabularyStatus(ctx, userID, dict.VocabularyName, status, next); err != nil {
		if !errors.Is(err, repository.ErrConditionFailed) {
			log.Printf("failed to sync vocabulary status for user %s: %v", userID, err)
			return next
		}
		// A concurrent writer changed the blob; report its newer state.
		current, readErr := s.repo.GetDictionary(ctx, userID)
		if readErr != nil || current == nil {
			return status
		}
		return current.VocabularyStatus
	}
	return next
}

// providerStatus maps Transcribe's state for a vocabulary to READY, FAILED or
// PENDING. ok is false when the state could not be determined.
func (s *DictionaryService) providerStatus(ctx context.Context, name string) (string, bool) {
	result, err := s.transcribeClient.GetVocabulary(ctx, &transcribe.GetVocabularyInput{VocabularyName: aws.String(name)})
	if err != nil {
		if vocabularyMissing(err) {
			return "FAILED", true
		}
		log.Printf("failed to get vocabulary status for %s: %v", name, err)
		return "", false
	}
	switch result.VocabularyState {
	case transcribeTypes.VocabularyStateReady:
		return "READY", true
	case transcribeTypes.VocabularyStateFailed:
		return "FAILED", true
	}
	return "PENDING", true
}

// syncVocabularyState refreshes a single-slot (legacy, in-place) build from
// Transcribe under a version guard and returns the state to report.
func (s *DictionaryService) syncVocabularyState(ctx context.Context, userID string, state *model.VocabularyState) *model.VocabularyState {
	if state.Status != model.VocabStatusBuilding || state.ActiveName == "" || state.InactiveName != "" || state.BuildingFingerprint != "" || s.transcribeClient == nil {
		return state
	}
	provider, ok := s.providerStatus(ctx, state.ActiveName)
	if !ok || provider == "PENDING" {
		return state
	}
	next := *state
	next.ActiveReady = provider == "READY"
	next.Status = model.VocabStatusReady
	next.FailureCode = ""
	if provider == "FAILED" {
		next.Status = model.VocabStatusFailed
		next.FailureCode = "PROVIDER"
	}
	change := repository.VocabularyStateChange{Status: &next.Status, ActiveReady: &next.ActiveReady, FailureCode: &next.FailureCode}
	err := s.repo.UpdateVocabularyState(ctx, userID, state.Context, state.Language, state.Version, change, s.now().Format(model.DictTimeLayout))
	if errors.Is(err, repository.ErrConditionFailed) {
		// A concurrent writer moved the row; report its state, not ours.
		current, readErr := s.repo.GetVocabularyState(ctx, userID, state.Context, state.Language)
		if readErr != nil || current == nil {
			return state
		}
		return current
	}
	if err != nil {
		log.Printf("failed to sync vocabulary state for user %s: %v", userID, err)
	}
	return &next
}

// legacyVocabularyStatus maps a VOCAB# row to the legacy API value. A READY
// active slot reports READY even while a newer build runs or failed, so the
// legacy client keeps using it; per-build status is in vocabularies.
func legacyVocabularyStatus(state *model.VocabularyState) string {
	if state.ActiveReady && state.ActiveName != "" {
		return "READY"
	}
	switch state.Status {
	case model.VocabStatusReady:
		return "READY"
	case model.VocabStatusBuilding:
		return "PENDING"
	case model.VocabStatusFailed, model.VocabStatusQuotaExceeded:
		return "FAILED"
	}
	return ""
}

// saveLegacyBlob is the pre-v2 blob write and in-place rebuild, kept only for
// a blob that migration refuses (ErrLegacyDictionaryConflict).
func (s *DictionaryService) saveLegacyBlob(ctx context.Context, userID string, terms []model.DictionaryTerm) (*model.DictionaryResponse, error) {
	vocabName := legacyVocabularyName(userID)

	dict := &model.UserDictionary{
		PK:               model.PrefixUser + userID,
		SK:               model.PrefixDictionary,
		UserID:           userID,
		Terms:            terms,
		VocabularyName:   vocabName,
		VocabularyStatus: "PENDING",
		UpdatedAt:        time.Now().UTC().Format(time.RFC3339),
		EntityType:       "DICTIONARY",
	}

	if err := s.repo.SaveDictionary(ctx, dict); err != nil {
		return nil, fmt.Errorf("failed to save dictionary: %w", err)
	}

	// Build Transcribe vocabulary (async — AWS builds it in background)
	if len(terms) > 0 {
		if err := s.buildTranscribeVocabulary(ctx, userID, vocabName, terms); err != nil {
			log.Printf("failed to build vocabulary for user %s: %v", userID, err)
			// Update status to FAILED
			dict.VocabularyStatus = "FAILED"
			if saveErr := s.repo.SaveDictionary(ctx, dict); saveErr != nil {
				log.Printf("failed to persist FAILED status for user %s: %v", userID, saveErr)
			}
			return &model.DictionaryResponse{
				Terms:  terms,
				Status: "FAILED",
			}, nil
		}
	} else {
		// No terms — delete vocabulary if it exists
		s.deleteTranscribeVocabulary(ctx, vocabName)
		dict.VocabularyStatus = ""
		if err := s.repo.SaveDictionary(ctx, dict); err != nil {
			log.Printf("failed to clear vocabulary status for user %s: %v", userID, err)
		}
	}

	return &model.DictionaryResponse{
		Terms:  terms,
		Status: dict.VocabularyStatus,
	}, nil
}

// deleteLegacyBlobTerm is the pre-v2 delete for a blob that migration refuses.
func (s *DictionaryService) deleteLegacyBlobTerm(ctx context.Context, userID, phrase string) error {
	dict, err := s.repo.GetDictionary(ctx, userID)
	if err != nil {
		return fmt.Errorf("failed to get dictionary: %w", err)
	}

	if dict == nil {
		return ErrNotFound
	}

	// Filter out the term
	var remaining []model.DictionaryTerm
	found := false
	for _, t := range dict.Terms {
		if t.Phrase == phrase {
			found = true
			continue
		}
		remaining = append(remaining, t)
	}

	if !found {
		return ErrNotFound
	}

	// Update with remaining terms
	_, err = s.saveLegacyBlob(ctx, userID, remaining)
	return err
}

// GetVocabularyForTranscription returns the personal ko-KR ActiveName when
// it is READY, never a building slot. The VOCAB# row is authoritative when
// present; the legacy blob is the fallback for users without one. It never
// writes. A Transcribe lookup failure is logged and yields no name.
func (s *DictionaryService) GetVocabularyForTranscription(ctx context.Context, userID string) (string, error) {
	name, providerErr, err := s.koVocabularyForTranscription(ctx, userID)
	if providerErr != nil {
		log.Printf("failed to confirm ko-KR vocabulary for user %s: %v", userID, providerErr)
	}
	return name, err
}

// koVocabularyForTranscription separates stored-state errors (err) from a
// Transcribe confirmation failure (providerErr), which both yield no name.
func (s *DictionaryService) koVocabularyForTranscription(ctx context.Context, userID string) (name string, providerErr, err error) {
	state, err := s.repo.GetVocabularyState(ctx, userID, model.VocabContextPersonal, model.VocabLangKo)
	if err != nil {
		return "", nil, err
	}
	if state != nil {
		if state.ActiveReady {
			name, providerErr = s.confirmReady(ctx, state.ActiveName, true)
			return name, providerErr, nil
		}
		// A legacy in-place build is only marked ready by an API-side sync, so
		// ask Transcribe. An A/B row or an emptied language is never used unready.
		if state.Status == model.VocabStatusBuilding && state.BuildingFingerprint == "" && state.InactiveName == "" && state.EmptySince == "" {
			name, providerErr = s.confirmReady(ctx, state.ActiveName, false)
			return name, providerErr, nil
		}
		return "", nil, nil
	}

	dict, err := s.repo.GetDictionary(ctx, userID)
	if err != nil {
		return "", nil, err
	}
	if dict == nil {
		return "", nil, nil
	}
	name, providerErr = s.confirmReady(ctx, dict.VocabularyName, dict.VocabularyStatus == "READY")
	return name, providerErr, nil
}

// confirmReady returns name only when it is READY; a GetVocabulary failure
// returns "" and the error.
func (s *DictionaryService) confirmReady(ctx context.Context, name string, storedReady bool) (string, error) {
	if name == "" {
		return "", nil
	}
	if s.transcribeClient == nil {
		if storedReady {
			return name, nil
		}
		return "", nil
	}
	result, err := s.transcribeClient.GetVocabulary(ctx, &transcribe.GetVocabularyInput{VocabularyName: aws.String(name)})
	if err != nil {
		return "", err
	}
	if result.VocabularyState == transcribeTypes.VocabularyStateReady {
		return name, nil
	}
	return "", nil
}

// buildTranscribeVocabulary creates or updates an AWS Transcribe custom vocabulary
func (s *DictionaryService) buildTranscribeVocabulary(ctx context.Context, userID, vocabName string, terms []model.DictionaryTerm) error {
	if s.transcribeClient == nil {
		return nil
	}

	// Build phrases list — AWS Transcribe custom vocabularies use plain phrase strings
	var phrases []string
	for _, t := range terms {
		if t.Phrase != "" {
			phrases = append(phrases, t.Phrase)
		}
	}

	if len(phrases) == 0 {
		return nil
	}

	// Try update first (vocabulary already exists)
	_, err := s.transcribeClient.UpdateVocabulary(ctx, &transcribe.UpdateVocabularyInput{
		VocabularyName: aws.String(vocabName),
		LanguageCode:   transcribeTypes.LanguageCodeKoKr,
		Phrases:        phrases,
	})
	if err != nil {
		// If update fails (vocabulary doesn't exist), create new
		if vocabularyMissing(err) {
			_, err = s.transcribeClient.CreateVocabulary(ctx, &transcribe.CreateVocabularyInput{
				VocabularyName: aws.String(vocabName),
				LanguageCode:   transcribeTypes.LanguageCodeKoKr,
				Phrases:        phrases,
			})
			if err != nil {
				return fmt.Errorf("failed to create vocabulary: %w", err)
			}
		} else {
			return fmt.Errorf("failed to update vocabulary: %w", err)
		}
	}

	return nil
}

// deleteTranscribeVocabulary deletes an AWS Transcribe custom vocabulary
func (s *DictionaryService) deleteTranscribeVocabulary(ctx context.Context, vocabName string) {
	if s.transcribeClient == nil {
		return
	}
	_, err := s.transcribeClient.DeleteVocabulary(ctx, &transcribe.DeleteVocabularyInput{
		VocabularyName: aws.String(vocabName),
	})
	if err != nil {
		log.Printf("failed to delete vocabulary %s: %v", vocabName, err)
	}
}

// legacyVocabularyName is the derived single-slot vocabulary name.
func legacyVocabularyName(userID string) string {
	return fmt.Sprintf("ttobak-vocab-%s", vocabSuffix(userID))
}

// vocabSuffix returns a collision-resistant suffix for vocabulary naming.
// Uses SHA-256 truncated to 16 hex chars (64 bits) — birthday collision at ~4 billion users.
func vocabSuffix(userID string) string {
	h := sha256.Sum256([]byte(userID))
	return hex.EncodeToString(h[:8])
}
