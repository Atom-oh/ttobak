package service

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/transcribe"
	transcribeTypes "github.com/aws/aws-sdk-go-v2/service/transcribe/types"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

// DictionaryService handles custom dictionary business logic
type DictionaryService struct {
	repo             *repository.DictionaryRepository
	transcribeClient *transcribe.Client
}

// NewDictionaryService creates a new dictionary service
func NewDictionaryService(repo *repository.DictionaryRepository, transcribeClient *transcribe.Client) *DictionaryService {
	return &DictionaryService{
		repo:             repo,
		transcribeClient: transcribeClient,
	}
}

// GetDictionary retrieves a user's custom dictionary
func (s *DictionaryService) GetDictionary(ctx context.Context, userID string) (*model.DictionaryResponse, error) {
	dict, err := s.repo.GetDictionary(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to get dictionary: %w", err)
	}

	if dict == nil {
		return &model.DictionaryResponse{
			Terms:  []model.DictionaryTerm{},
			Status: "",
		}, nil
	}

	terms := dict.Terms
	if terms == nil {
		terms = []model.DictionaryTerm{}
	}

	return &model.DictionaryResponse{
		Terms:  terms,
		Status: dict.VocabularyStatus,
	}, nil
}

// UpdateDictionary updates a user's custom dictionary and triggers vocabulary build
func (s *DictionaryService) UpdateDictionary(ctx context.Context, userID string, terms []model.DictionaryTerm) (*model.DictionaryResponse, error) {
	vocabName := fmt.Sprintf("ttobak-vocab-%s", truncateID(userID, 8))

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
			s.repo.SaveDictionary(ctx, dict)
			return &model.DictionaryResponse{
				Terms:  terms,
				Status: "FAILED",
			}, nil
		}
	} else {
		// No terms — delete vocabulary if it exists
		s.deleteTranscribeVocabulary(ctx, vocabName)
		dict.VocabularyStatus = ""
		s.repo.SaveDictionary(ctx, dict)
	}

	return &model.DictionaryResponse{
		Terms:  terms,
		Status: dict.VocabularyStatus,
	}, nil
}

// DeleteTerm removes a single term from the user's dictionary
func (s *DictionaryService) DeleteTerm(ctx context.Context, userID, phrase string) error {
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
	_, err = s.UpdateDictionary(ctx, userID, remaining)
	return err
}

// GetVocabularyForTranscription returns the vocabulary name if the user has a READY vocabulary
func (s *DictionaryService) GetVocabularyForTranscription(ctx context.Context, userID string) (string, error) {
	dict, err := s.repo.GetDictionary(ctx, userID)
	if err != nil {
		return "", err
	}

	if dict == nil || dict.VocabularyName == "" {
		return "", nil
	}

	// Check actual vocabulary status from AWS Transcribe
	if s.transcribeClient != nil {
		result, err := s.transcribeClient.GetVocabulary(ctx, &transcribe.GetVocabularyInput{
			VocabularyName: aws.String(dict.VocabularyName),
		})
		if err != nil {
			log.Printf("failed to get vocabulary status for %s: %v", dict.VocabularyName, err)
			return "", nil
		}
		if result.VocabularyState == transcribeTypes.VocabularyStateReady {
			return dict.VocabularyName, nil
		}
		return "", nil
	}

	// Fallback: trust DynamoDB status
	if dict.VocabularyStatus == "READY" {
		return dict.VocabularyName, nil
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
		if isVocabularyNotFoundError(err) {
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

// isVocabularyNotFoundError checks if the error is a "vocabulary not found" error
func isVocabularyNotFoundError(err error) bool {
	return strings.Contains(err.Error(), "NotFoundException") ||
		strings.Contains(err.Error(), "BadRequestException") ||
		strings.Contains(err.Error(), "not found")
}

// truncateID returns the first n characters of an ID
func truncateID(id string, n int) string {
	if len(id) <= n {
		return id
	}
	return id[:n]
}
