package service

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"testing"

	transcribeTypes "github.com/aws/aws-sdk-go-v2/service/transcribe/types"

	"github.com/ttobak/backend/internal/model"
)

// TestTermKeySharedVectors pins NormalizeTermKey/TermKey to the vectors the
// frontend's normalizeTermKey test reads (frontend/scripts/dictionary.test.mjs).
func TestTermKeySharedVectors(t *testing.T) {
	raw, err := os.ReadFile("testdata/term-key/vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Vectors []struct{ Input, Normalized, TermKey string }
	}
	if err := json.Unmarshal(raw, &file); err != nil || len(file.Vectors) == 0 {
		t.Fatalf("bad vectors file: %v", err)
	}
	for _, v := range file.Vectors {
		if got := NormalizeTermKey(v.Input); got != v.Normalized {
			t.Errorf("NormalizeTermKey(%q) = %q, want %q", v.Input, got, v.Normalized)
		}
		if got := TermKey(v.Input); got != v.TermKey {
			t.Errorf("TermKey(%q) = %q, want %q", v.Input, got, v.TermKey)
		}
	}
}

func TestLiveDictionaryConfigListsOnlyReadyNames(t *testing.T) {
	koSK := model.VocabSK(model.VocabContextPersonal, model.VocabLangKo)
	enSK := model.VocabSK(model.VocabContextPersonal, model.VocabLangEn)
	a, b, c := "ttobak-vocab-aaaaaaaaaaaaaaaa", "ttobak-vocab-bbbbbbbbbbbbbbbb", "ttobak-vocab-cccccccccccccccc"
	ready, pending := transcribeTypes.VocabularyStateReady, transcribeTypes.VocabularyStatePending
	st := func(sk, active, inactive, status string) *model.VocabularyState {
		return &model.VocabularyState{SK: sk, ActiveName: active, ActiveReady: active != "", InactiveName: inactive, Status: status}
	}
	cases := []struct {
		name                         string
		blob                         *model.UserDictionary
		ko, en                       *model.VocabularyState
		provider                     map[string]transcribeTypes.VocabularyState
		wantKo, wantEn, wantEnStatus string
		wantWarning                  bool
	}{
		{"both ready", nil, st(koSK, a, "", "READY"), st(enSK, b, "", "READY"),
			map[string]transcribeTypes.VocabularyState{a: ready, b: ready}, a, b, "READY", false},
		{"en first build in progress", nil, st(koSK, a, "", "READY"), st(enSK, "", b, "BUILDING"),
			map[string]transcribeTypes.VocabularyState{a: ready, b: pending}, a, "", "BUILDING", false},
		{"rebuild keeps serving the active slot", nil, nil, st(enSK, b, c, "BUILDING"),
			map[string]transcribeTypes.VocabularyState{b: ready}, "", b, "BUILDING", false},
		{"provider not ready", nil, nil, st(enSK, b, "", "READY"),
			map[string]transcribeTypes.VocabularyState{b: pending}, "", "", "READY", false},
		{"unmigrated legacy ko", legacyBlob("READY"), nil, nil,
			map[string]transcribeTypes.VocabularyState{"ttobak-vocab-0123456789abcdef": ready}, "ttobak-vocab-0123456789abcdef", "", "NONE", false},
		// The fake provider fails GetVocabulary for unknown names.
		{"provider lookup fails", nil, st(koSK, a, "", "READY"), st(enSK, b, "", "READY"),
			map[string]transcribeTypes.VocabularyState{}, "", "", "READY", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeDictStore()
			store.blob = tc.blob
			for sk, v := range map[string]*model.VocabularyState{koSK: tc.ko, enSK: tc.en} {
				if v != nil {
					store.vocab[sk] = *v
				}
			}
			cfg := newTestDictService(store, &fakeVocabClient{states: tc.provider}).LiveDictionaryConfig(context.Background(), "u1")
			got := cfg.Vocabularies
			if hasWarning := slices.Contains(cfg.Warnings, liveVocabularyWarning); hasWarning != tc.wantWarning {
				t.Fatalf("warnings %v, want live warning %v", cfg.Warnings, tc.wantWarning)
			}
			if got.Ko.Name != tc.wantKo || got.En.Name != tc.wantEn || got.En.Status != tc.wantEnStatus {
				t.Fatalf("got %+v, want ko=%q en=%q en status %q", got, tc.wantKo, tc.wantEn, tc.wantEnStatus)
			}
			if store.saveCalls != 0 || len(store.legacyCalls) != 0 {
				t.Fatal("live config must not write the legacy item")
			}
		})
	}
}
