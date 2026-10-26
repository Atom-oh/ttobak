package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/aws/aws-sdk-go-v2/service/transcribe"
	transcribeTypes "github.com/aws/aws-sdk-go-v2/service/transcribe/types"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/service"
)

// migratedStore is a read-only dictionary store for a migrated user. Any
// write panics through the nil embedded interface.
type migratedStore struct {
	service.DictionaryStore
	blob  *model.UserDictionary
	rows  []model.DictTerm
	vocab *model.VocabularyState
}

func (s *migratedStore) GetDictionary(context.Context, string) (*model.UserDictionary, error) {
	return s.blob, nil
}
func (s *migratedStore) ListDictTerms(context.Context, string) ([]model.DictTerm, error) {
	return s.rows, nil
}
func (s *migratedStore) GetVocabularyState(context.Context, string, string, string) (*model.VocabularyState, error) {
	return s.vocab, nil
}

type readyVocabularies struct {
	service.TranscribeVocabularyAPI
}

func (readyVocabularies) GetVocabulary(_ context.Context, in *transcribe.GetVocabularyInput, _ ...func(*transcribe.Options)) (*transcribe.GetVocabularyOutput, error) {
	return &transcribe.GetVocabularyOutput{VocabularyName: in.VocabularyName, VocabularyState: transcribeTypes.VocabularyStateReady}, nil
}

func TestMigratedUserStillGetsPromptAndVocabulary(t *testing.T) {
	// Synthetic fixture. The blob is stale on purpose: after migration only
	// the rows are authoritative, but the blob is still present.
	store := &migratedStore{
		blob: &model.UserDictionary{
			UserID: "u1", MigratedAt: "2026-10-18T00:00:00.000000Z",
			Terms:          []model.DictionaryTerm{{Phrase: "Stale", DisplayAs: "Stale"}},
			VocabularyName: "ttobak-vocab-0123456789abcdef", VocabularyStatus: "READY",
		},
		rows: []model.DictTerm{
			{SK: "DICTTERM#1", TermKey: "1", State: model.TermStateActive, DisplayAs: "AlphaGrid", Phrase: "Alpha-Grid", AddedAt: "2026-10-18T00:00:00.000001Z"},
			{SK: "DICTTERM#2", TermKey: "2", State: model.TermStateActive, Phrase: "Beta-Node", AddedAt: "2026-10-18T00:00:00.000002Z"},
			{SK: "DICTTERM#3", TermKey: "3", State: model.TermStateDeleted, AddedAt: "2026-10-18T00:00:00.000003Z"},
		},
		vocab: &model.VocabularyState{
			Context: model.VocabContextPersonal, Language: model.VocabLangKo,
			ActiveName: "ttobak-vocab-0123456789abcdef", Status: model.VocabStatusReady, ActiveReady: true, Version: 1,
		},
	}
	svc := service.NewDictionaryServiceWithStore(store, readyVocabularies{})

	if got := transcriptionVocabulary(context.Background(), svc, "u1"); got != "ttobak-vocab-0123456789abcdef" {
		t.Fatalf("vocabulary = %q", got)
	}
	if got := whisperInitialPrompt(context.Background(), svc, "u1"); got != "AlphaGrid, Beta-Node" {
		t.Fatalf("prompt = %q", got)
	}
	if transcriptionVocabulary(context.Background(), svc, "") != "" || whisperInitialPrompt(context.Background(), svc, "") != "" {
		t.Fatal("an unknown owner must not get dictionary inputs")
	}
}

// A large dictionary is cut at whole terms within whisperPromptMaxRunes, so
// INITIAL_PROMPT never pushes the RunTask override past the ECS limit.
func TestJoinWhisperPromptCapsAtWholeTerms(t *testing.T) {
	var phrases []string
	for i := 0; i < 400; i++ {
		phrases = append(phrases, fmt.Sprintf("케이컬처용어%03d", i))
	}
	phrases = append([]string{"K-culture", " ", "K-culture"}, phrases...)
	got := joinWhisperPrompt(phrases)
	if n := utf8.RuneCountInString(got); n == 0 || n > whisperPromptMaxRunes {
		t.Fatalf("prompt length %d runes", n)
	}
	if !strings.HasPrefix(got, "K-culture, 케이컬처용어000") || strings.Count(got, "K-culture") != 1 {
		t.Fatalf("order or dedupe broken: %q", got[:40])
	}
	if strings.HasSuffix(got, ", ") || strings.Contains(got, ", ,") {
		t.Fatalf("a term was cut: %q", got[len(got)-20:])
	}
	if joinWhisperPrompt([]string{"AlphaGrid", "Beta-Node"}) != "AlphaGrid, Beta-Node" {
		t.Fatal("a small dictionary must pass through unchanged")
	}
}
