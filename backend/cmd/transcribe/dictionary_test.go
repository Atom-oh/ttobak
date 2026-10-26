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

// layerStore serves dictionary rows per partition.
type layerStore struct {
	service.DictionaryStore
	blob *model.UserDictionary
	rows map[string][]model.DictTerm
}

func (s *layerStore) GetDictionary(context.Context, string) (*model.UserDictionary, error) {
	return s.blob, nil
}
func (s *layerStore) ListDictTerms(_ context.Context, pk string) ([]model.DictTerm, error) {
	return s.rows[pk], nil
}

func promptRow(pk, display, origin, state string) model.DictTerm {
	key := service.TermKey(display)
	return model.DictTerm{PK: pk, SK: "DICTTERM#" + key, TermKey: key, DisplayAs: display, Phrase: display, Origin: origin, State: state, AddedAt: "2026-10-18T00:00:00.000001Z"}
}

func layerPromptFixture(members map[string]bool, memberErr error) *service.DictionaryService {
	store := &layerStore{rows: map[string][]model.DictTerm{
		"USER#u1": {
			promptRow("USER#u1", "PersonalAuto", model.TermOriginAuto, model.TermStateActive),
			promptRow("USER#u1", "PersonalManual", model.TermOriginManual, model.TermStateActive),
			promptRow("USER#u1", "PersonalSuggested", model.TermOriginAuto, model.TermStateSuggested),
		},
		"ACCOUNT#a1": {
			promptRow("ACCOUNT#a1", "AlphaGrid", model.TermOriginAuto, model.TermStateActive),
			promptRow("ACCOUNT#a1", "AccountSuggested", model.TermOriginAuto, model.TermStateSuggested),
			promptRow("ACCOUNT#a1", "AccountGone", model.TermOriginManual, model.TermStateDeleted),
		},
		"DICT#COMMON": {
			promptRow("DICT#COMMON", "K-culture", model.TermOriginManual, model.TermStateActive),
			promptRow("DICT#COMMON", "CommonGone", model.TermOriginManual, model.TermStateDeleted),
		},
	}}
	svc := service.NewDictionaryServiceWithStore(store, nil)
	svc.SetMembershipReader(func(_ context.Context, accountID, userID string) (bool, error) {
		return members[accountID+"/"+userID], memberErr
	})
	return svc
}

func TestWhisperPromptLayersForMeetingAccount(t *testing.T) {
	ctx := context.Background()
	svc := layerPromptFixture(map[string]bool{"a1/u1": true}, nil)
	got, err := svc.WhisperPromptTerms(ctx, "u1", "a1")
	if err != nil {
		t.Fatal(err)
	}
	if want := "PersonalManual, AlphaGrid, K-culture, PersonalAuto"; joinWhisperPrompt(got) != want {
		t.Fatalf("member prompt = %q, want %q", joinWhisperPrompt(got), want)
	}

	// A non-member, a meeting without an Account and a failed membership read
	// all drop the Account layer and keep the rest.
	rest := "PersonalManual, K-culture, PersonalAuto"
	for name, tc := range map[string]struct {
		svc       *service.DictionaryService
		accountID string
	}{
		"non-member": {layerPromptFixture(nil, nil), "a1"},
		"no account": {svc, ""},
		"read error": {layerPromptFixture(map[string]bool{"a1/u1": true}, fmt.Errorf("throttled")), "a1"},
	} {
		got, err := tc.svc.WhisperPromptTerms(ctx, "u1", tc.accountID)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if joinWhisperPrompt(got) != rest {
			t.Fatalf("%s prompt = %q, want %q", name, joinWhisperPrompt(got), rest)
		}
	}
}

// failingCommonStore fails only the Common read.
type failingCommonStore struct{ *layerStore }

func (s failingCommonStore) ListDictTerms(ctx context.Context, pk string) ([]model.DictTerm, error) {
	if pk == model.PrefixDictCommon {
		return nil, fmt.Errorf("throttled")
	}
	return s.layerStore.ListDictTerms(ctx, pk)
}

// A Common read error skips that layer like an Account error: the prompt
// keeps the Personal (and Account) terms instead of failing.
func TestWhisperPromptSkipsCommonOnReadError(t *testing.T) {
	store := failingCommonStore{&layerStore{rows: map[string][]model.DictTerm{
		"USER#u1": {
			promptRow("USER#u1", "PersonalAuto", model.TermOriginAuto, model.TermStateActive),
			promptRow("USER#u1", "PersonalManual", model.TermOriginManual, model.TermStateActive),
		},
		"ACCOUNT#a1": {promptRow("ACCOUNT#a1", "AlphaGrid", model.TermOriginAuto, model.TermStateActive)},
	}}}
	svc := service.NewDictionaryServiceWithStore(store, nil)
	svc.SetMembershipReader(func(context.Context, string, string) (bool, error) { return true, nil })
	for accountID, want := range map[string]string{
		"":   "PersonalManual, PersonalAuto",
		"a1": "PersonalManual, AlphaGrid, PersonalAuto",
	} {
		got, err := svc.WhisperPromptTerms(context.Background(), "u1", accountID)
		if err != nil {
			t.Fatalf("account %q: a Common read error must not fail the prompt: %v", accountID, err)
		}
		if joinWhisperPrompt(got) != want {
			t.Fatalf("account %q: prompt = %q, want %q", accountID, joinWhisperPrompt(got), want)
		}
	}
}

func TestWhisperPromptLegacyBlobCountsAsManual(t *testing.T) {
	store := &layerStore{
		blob: &model.UserDictionary{UserID: "u1", Terms: []model.DictionaryTerm{{Phrase: "LegacyTerm", DisplayAs: "LegacyTerm"}}},
		rows: map[string][]model.DictTerm{
			"USER#u1":     {promptRow("USER#u1", "PersonalAuto", model.TermOriginAuto, model.TermStateActive)},
			"DICT#COMMON": {promptRow("DICT#COMMON", "K-culture", model.TermOriginManual, model.TermStateActive)},
		},
	}
	got, err := service.NewDictionaryServiceWithStore(store, nil).WhisperPromptTerms(context.Background(), "u1", "")
	if err != nil {
		t.Fatal(err)
	}
	if want := "LegacyTerm, K-culture, PersonalAuto"; joinWhisperPrompt(got) != want {
		t.Fatalf("prompt = %q, want %q", joinWhisperPrompt(got), want)
	}
}

func TestWhisperInputsPassesMeetingAccount(t *testing.T) {
	original := loadDictionaryTerms
	t.Cleanup(func() { loadDictionaryTerms = original })
	var gotUser, gotAccount string
	loadDictionaryTerms = func(_ context.Context, userID, accountID string) ([]string, error) {
		gotUser, gotAccount = userID, accountID
		return []string{"AlphaGrid"}, nil
	}
	prompt, _ := whisperInputs(context.Background(), &model.Meeting{AccountID: "a1"}, "u1")
	if prompt != "AlphaGrid" || gotUser != "u1" || gotAccount != "a1" {
		t.Fatalf("prompt=%q user=%q account=%q", prompt, gotUser, gotAccount)
	}
}
