package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/transcribe"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

func (f *fakeDictStore) GetDictTerm(_ context.Context, _ string, key string) (*model.DictTerm, error) {
	row, ok := f.terms[model.DictTermSK(key)]
	if !ok {
		return nil, nil
	}
	return &row, nil
}
func (f *fakeDictStore) CreateDictTerm(_ context.Context, term *model.DictTerm) error {
	if _, ok := f.terms[term.SK]; ok {
		return cond()
	}
	f.terms[term.SK] = *term
	return nil
}
func (f *fakeDictStore) UpdateDictTerm(_ context.Context, _ string, key string, ch repository.DictTermChange, actor, now string, v int64) error {
	row, ok := f.terms[model.DictTermSK(key)]
	if !ok || row.Version != v || row.State == model.TermStateDeleted {
		return cond()
	}
	set := func(dst *string, src *string) {
		if src != nil {
			*dst = *src
		}
	}
	set(&row.DisplayAs, ch.DisplayAs)
	set(&row.Phrase, ch.Phrase)
	set(&row.AliasMode, ch.AliasMode)
	set(&row.Type, ch.Type)
	set(&row.State, ch.State)
	if ch.HeardAs != nil {
		row.HeardAs = *ch.HeardAs
	}
	if ch.TranscribeKo != nil {
		row.TranscribeKo = *ch.TranscribeKo
	}
	if ch.LegacyDisplayEmpty != nil {
		row.LegacyDisplayEmpty = *ch.LegacyDisplayEmpty
	}
	row.UserEditedAt, row.UpdatedBy, row.Version = now, actor, v+1
	f.terms[row.SK] = row
	if f.onUpdate != nil {
		f.onUpdate()
	}
	return nil
}
func (f *fakeDictStore) TombstoneDictTerm(_ context.Context, _ string, key, actor, now string, v int64) (repository.DictWriteResult, error) {
	row, ok := f.terms[model.DictTermSK(key)]
	if !ok || row.Version != v || row.State == model.TermStateDeleted {
		return repository.DictWriteResult{}, cond()
	}
	f.terms[row.SK] = model.DictTerm{PK: row.PK, SK: row.SK, TermKey: key, Scope: row.Scope, Origin: row.Origin,
		State: model.TermStateDeleted, DeletedAt: now, UpdatedBy: actor, Version: v + 1, AddedAt: row.AddedAt}
	return repository.DictWriteResult{CleanupErr: f.cleanupErr}, nil
}
func (f *fakeDictStore) RestoreDictTerm(_ context.Context, term *model.DictTerm, v int64) error {
	row, ok := f.terms[term.SK]
	if !ok || row.State != model.TermStateDeleted || row.Version != v {
		return cond()
	}
	next := *term
	next.Version = v + 1
	f.terms[term.SK] = next
	return nil
}
func (f *fakeDictStore) DeleteManualTerm(_ context.Context, _ string, key string, v int64) error {
	row, ok := f.terms[model.DictTermSK(key)]
	if !ok || row.Version != v || row.Origin != model.TermOriginManual || row.State != model.TermStateActive {
		return cond()
	}
	delete(f.terms, row.SK)
	return nil
}
func (f *fakeDictStore) DeleteTombstone(_ context.Context, _ string, key string, v int64) error {
	row, ok := f.terms[model.DictTermSK(key)]
	if !ok || row.Version != v || row.State != model.TermStateDeleted {
		return cond()
	}
	delete(f.terms, row.SK)
	return nil
}

// CreateMigratedVocabularyState mirrors the repository: the legacy blob must
// be absent or migrated, and the row must not exist.
func (f *fakeDictStore) CreateMigratedVocabularyState(_ context.Context, _ string, s *model.VocabularyState) error {
	if _, ok := f.vocab[s.SK]; ok {
		return conflict()
	}
	if f.blob != nil && f.blob.MigratedAt == "" {
		return fmt.Errorf("%w: %w", repository.ErrConditionFailed, repository.ErrLegacyBlobChanged)
	}
	f.vocab[s.SK] = *s
	return nil
}

// fakeVocabWriter records in-place vocabulary writes.
type fakeVocabWriter struct {
	fakeVocabClient
	updates [][]string
	deletes int
}

func (c *fakeVocabWriter) UpdateVocabulary(_ context.Context, in *transcribe.UpdateVocabularyInput, _ ...func(*transcribe.Options)) (*transcribe.UpdateVocabularyOutput, error) {
	c.updates = append(c.updates, in.Phrases)
	return &transcribe.UpdateVocabularyOutput{}, nil
}
func (c *fakeVocabWriter) DeleteVocabulary(context.Context, *transcribe.DeleteVocabularyInput, ...func(*transcribe.Options)) (*transcribe.DeleteVocabularyOutput, error) {
	c.deletes++
	return &transcribe.DeleteVocabularyOutput{}, nil
}

// seedRow stores a synthetic row and returns its key.
func (f *fakeDictStore) seedRow(display, phrase, origin, state string, version int64) string {
	key := TermKey(display)
	f.terms[model.DictTermSK(key)] = model.DictTerm{
		PK: "USER#u1", SK: model.DictTermSK(key), TermKey: key, DisplayAs: display, Phrase: phrase,
		Origin: origin, State: state, Version: version, TranscribeKo: phrase != "", AddedAt: "2026-10-01T00:00:00.000000Z:" + display,
	}
	return key
}

func ver(n int64) *int64 { return &n }

func migratedStore() *fakeDictStore {
	store := newFakeDictStore()
	store.blob = &model.UserDictionary{UserID: "u1", MigratedAt: "2026-10-17T00:00:00.000000Z"}
	return store
}

func TestValidateTermInput(t *testing.T) {
	cases := []struct {
		name, display string
		heard         []string
		typ           string
		wantField     string // "" = valid
		wantDisplay   string
		wantHeard     int
	}{
		{"valid and trimmed", "  Alpha Grid ", []string{"알파 그리드"}, "product", "", "Alpha Grid", 1},
		{"NFKC full width", "Ａｌｐｈａ", nil, "", "", "Alpha", 0},
		{"tab rejected", "Alpha\tGrid", nil, "", "displayAs", "", 0},
		{"newline rejected", "Alpha\nGrid", nil, "", "displayAs", "", 0},
		{"leading tab rejected", "\tAlpha Grid", nil, "", "displayAs", "", 0},
		{"trailing newline rejected", "Alpha Grid\n", nil, "", "displayAs", "", 0},
		{"edge tab in alias rejected", "Alpha Grid", []string{"알파\t"}, "", "heardAs", "", 0},
		{"ideographic space trimmed", "\u3000Alpha Grid ", nil, "", "", "Alpha Grid", 0},
		{"format char rejected", "Alpha​Grid", nil, "", "displayAs", "", 0},
		{"line separator rejected", "Alpha Grid", nil, "", "displayAs", "", 0},
		{"one rune too short", "A", nil, "", "displayAs", "", 0},
		{"41 runes too long", strings.Repeat("가", 41), nil, "", "displayAs", "", 0},
		{"40 Hangul fits 120 bytes", strings.Repeat("가", 40), nil, "", "", strings.Repeat("가", 40), 0},
		{"byte cap", strings.Repeat("😀", 31), nil, "", "displayAs", "", 0},
		{"separators only", "-- ..", nil, "", "displayAs", "", 0},
		{"six aliases", "Alpha Grid", []string{"a1", "a2", "a3", "a4", "a5", "a6"}, "", "heardAs", "", 0},
		{"duplicate aliases collapse", "Alpha Grid", []string{"알파", " 알파 ", "", "a1", "a2", "a3", "a4"}, "", "", "Alpha Grid", 5},
		{"self-key alias", "Alpha Grid", []string{"alpha-grid"}, "", "heardAs", "", 0},
		{"blocklisted alias", "Alpha Grid", []string{"그러니까"}, "", "heardAs", "", 0},
		{"bad type", "Alpha Grid", nil, "Product Name", "type", "", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ValidateTermInput(tc.display, tc.heard, tc.typ)
			var input *TermInputError
			if tc.wantField != "" {
				if !errors.As(err, &input) || input.Field != tc.wantField || !errors.Is(err, ErrInvalidDictionaryTerm) {
					t.Fatalf("want %s error, got %v", tc.wantField, err)
				}
				return
			}
			if err != nil || got.DisplayAs != tc.wantDisplay || len(got.HeardAs) != tc.wantHeard || got.TermKey != TermKey(tc.wantDisplay) {
				t.Fatalf("got %+v %v", got, err)
			}
		})
	}
}

func TestCreateTermCreatesRestoresAndCaps(t *testing.T) {
	ctx := context.Background()
	store := migratedStore()
	client := &fakeVocabWriter{}
	svc := newTestDictService(store, client)
	resp, err := svc.CreateTerm(ctx, "u1", model.CreateDictTermRequest{DisplayAs: "Alpha Grid", HeardAs: []string{"알파 그리드"}})
	if err != nil || resp.Item.Origin != model.TermOriginManual || resp.Item.Version != 1 || resp.Item.Phrase != "Alpha Grid" || resp.Status != "PENDING" {
		t.Fatalf("create = %+v %v", resp, err)
	}
	if fmt.Sprint(client.updates) != "[[Alpha Grid]]" {
		t.Fatalf("manual create must rebuild the legacy vocabulary: %v", client.updates)
	}
	if _, err := svc.CreateTerm(ctx, "u1", model.CreateDictTermRequest{DisplayAs: "alpha-grid"}); !errors.Is(err, ErrDictionaryTermExists) {
		t.Fatalf("same key must be TERM_EXISTS, got %v", err)
	}
	key := TermKey("Alpha Grid")
	if _, err := svc.TombstoneTerm(ctx, "u1", key, nil); err != nil {
		t.Fatal(err)
	}
	empty := ""
	restored, err := svc.CreateTerm(ctx, "u1", model.CreateDictTermRequest{DisplayAs: "Alpha Grid", Phrase: &empty})
	if err != nil || restored.Item.Version != 3 || restored.Item.State != model.TermStateActive || restored.Item.TranscribeKo {
		t.Fatalf("POST must restore a tombstone as manual: %+v %v", restored, err)
	}

	full := migratedStore()
	for i := 0; i < MaxManualTermsPerScope; i++ {
		full.seedRow(fmt.Sprintf("Term %03d", i), "", model.TermOriginManual, model.TermStateActive, 1)
	}
	full.seedRow("Auto Term", "", model.TermOriginAuto, model.TermStateActive, 1)
	if _, err := newTestDictService(full, nil).CreateTerm(ctx, "u1", model.CreateDictTermRequest{DisplayAs: "One More"}); !errors.Is(err, ErrDictionaryFull) {
		t.Fatalf("501st manual term must be DICTIONARY_FULL, got %v", err)
	}
	delete(full.terms, model.DictTermSK(TermKey("Term 000")))
	if _, err := newTestDictService(full, nil).CreateTerm(ctx, "u1", model.CreateDictTermRequest{DisplayAs: "One More"}); err != nil {
		t.Fatalf("auto rows must not count toward the manual cap: %v", err)
	}
	if _, err := newTestDictService(migratedStore(), nil).CreateTerm(ctx, "u1", model.CreateDictTermRequest{DisplayAs: "Tab\tTerm"}); !errors.Is(err, ErrInvalidDictionaryTerm) {
		t.Fatalf("validator must run on create: %v", err)
	}
}

func TestUpdateTermVersionAndEditRules(t *testing.T) {
	ctx := context.Background()
	str := func(s string) *string { return &s }
	cases := []struct {
		name   string
		origin string
		state  string
		in     model.UpdateDictTermRequest
		want   error
		check  func(model.DictTerm) bool
	}{
		{"stale version", model.TermOriginAuto, model.TermStateActive, model.UpdateDictTermRequest{Version: ver(1), AliasMode: str("always")}, ErrDictionaryVersionConflict, nil},
		{"keep sets always and userEditedAt", model.TermOriginAuto, model.TermStateActive, model.UpdateDictTermRequest{Version: ver(2), AliasMode: str("always")}, nil,
			func(r model.DictTerm) bool {
				return r.AliasMode == "always" && r.UserEditedAt != "" && r.Origin == model.TermOriginAuto && r.Version == 3
			}},
		{"accept suggestion", model.TermOriginAuto, model.TermStateSuggested, model.UpdateDictTermRequest{Version: ver(2), State: str("active")}, nil,
			func(r model.DictTerm) bool { return r.State == model.TermStateActive && r.UserEditedAt != "" }},
		{"delete through PATCH refused", model.TermOriginAuto, model.TermStateActive, model.UpdateDictTermRequest{Version: ver(2), State: str("deleted")}, ErrInvalidDictionaryTerm, nil},
		{"rename refused", model.TermOriginManual, model.TermStateActive, model.UpdateDictTermRequest{Version: ver(2), DisplayAs: str("Beta Grid")}, ErrInvalidDictionaryTerm, nil},
		{"casing edit allowed", model.TermOriginManual, model.TermStateActive, model.UpdateDictTermRequest{Version: ver(2), DisplayAs: str("ALPHA GRID")}, nil,
			func(r model.DictTerm) bool { return r.DisplayAs == "ALPHA GRID" }},
		{"heardAs validated", model.TermOriginManual, model.TermStateActive, model.UpdateDictTermRequest{Version: ver(2), HeardAs: &[]string{"alpha grid"}}, ErrInvalidDictionaryTerm, nil},
		{"tombstone is not found", model.TermOriginAuto, model.TermStateDeleted, model.UpdateDictTermRequest{Version: ver(2), AliasMode: str("always")}, ErrNotFound, nil},
		{"empty patch", model.TermOriginAuto, model.TermStateActive, model.UpdateDictTermRequest{Version: ver(2)}, ErrInvalidDictionaryTerm, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := migratedStore()
			key := store.seedRow("Alpha Grid", "Alpha-Grid", tc.origin, tc.state, 2)
			resp, err := newTestDictService(store, nil).UpdateTerm(ctx, "u1", key, tc.in)
			if tc.want != nil {
				if !errors.Is(err, tc.want) || store.terms[model.DictTermSK(key)].Version != 2 {
					t.Fatalf("want %v without a write, got %v", tc.want, err)
				}
				return
			}
			if err != nil || !tc.check(store.terms[model.DictTermSK(key)]) || !resp.Item.UserEdited {
				t.Fatalf("got %+v %v", store.terms[model.DictTermSK(key)], err)
			}
		})
	}
}

func TestTombstoneTermSurfacesCleanupAndPrunes(t *testing.T) {
	ctx := context.Background()
	store := migratedStore()
	for i := 0; i < MaxTombstonesPerScope; i++ {
		key := TermKey(fmt.Sprintf("Old %04d", i))
		store.terms[model.DictTermSK(key)] = model.DictTerm{SK: model.DictTermSK(key), TermKey: key, State: model.TermStateDeleted,
			DeletedAt: fmt.Sprintf("2026-01-01T00:00:00.%06dZ", i), Version: 2}
	}
	key := store.seedRow("Alpha Grid", "", model.TermOriginAuto, model.TermStateActive, 4)
	store.cleanupErr = errors.New("synthetic cleanup failure")
	resp, err := newTestDictService(store, nil).TombstoneTerm(ctx, "u1", key, nil)
	if err != nil || resp.State != model.TermStateDeleted || len(resp.Warnings) != 1 {
		t.Fatalf("tombstone must commit and surface the cleanup warning: %+v %v", resp, err)
	}
	if row := store.terms[model.DictTermSK(key)]; row.State != model.TermStateDeleted || row.DisplayAs != "" {
		t.Fatalf("auto term must become a content-free tombstone: %+v", row)
	}
	if _, ok := store.terms[model.DictTermSK(TermKey("Old 0000"))]; ok || len(store.terms) != MaxTombstonesPerScope {
		t.Fatalf("the oldest tombstone beyond the cap must be pruned: %d rows", len(store.terms))
	}
	stale := int64(1)
	if _, err := newTestDictService(store, nil).TombstoneTerm(ctx, "u1", TermKey("Old 0001"), &stale); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleting a tombstone again must be not found: %v", err)
	}
}

func TestUpdateTermConcurrentDeleteIsNotFound(t *testing.T) {
	store := migratedStore()
	key := store.seedRow("Alpha Grid", "Alpha-Grid", model.TermOriginManual, model.TermStateActive, 1)
	store.onUpdate = func() { delete(store.terms, model.DictTermSK(key)) }
	mode := "always"
	_, err := newTestDictService(store, nil).UpdateTerm(context.Background(), "u1", key, model.UpdateDictTermRequest{Version: ver(1), AliasMode: &mode})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("a row removed after the update must be not found, got %v", err)
	}
}
