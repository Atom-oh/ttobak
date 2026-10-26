package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	transcribeTypes "github.com/aws/aws-sdk-go-v2/service/transcribe/types"

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
func (f *fakeDictStore) CreateDictTerm(_ context.Context, term *model.DictTerm, _ *repository.MembershipCheck) error {
	if _, ok := f.terms[term.SK]; ok {
		return cond()
	}
	f.terms[term.SK] = *term
	return nil
}
func (f *fakeDictStore) UpdateDictTerm(_ context.Context, _ string, key string, ch repository.DictTermChange, actor, now string, v int64, _ *repository.MembershipCheck) error {
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
func (f *fakeDictStore) TombstoneDictTerm(_ context.Context, _ string, key, actor, now string, v int64, _ *repository.MembershipCheck) (repository.DictWriteResult, error) {
	row, ok := f.terms[model.DictTermSK(key)]
	if !ok || row.Version != v || row.State == model.TermStateDeleted {
		return repository.DictWriteResult{}, cond()
	}
	f.terms[row.SK] = model.DictTerm{PK: row.PK, SK: row.SK, TermKey: key, Scope: row.Scope, Origin: row.Origin,
		State: model.TermStateDeleted, DeletedAt: now, UpdatedBy: actor, Version: v + 1, AddedAt: row.AddedAt}
	return repository.DictWriteResult{CleanupErr: f.cleanupErr}, nil
}
func (f *fakeDictStore) RestoreDictTerm(_ context.Context, term *model.DictTerm, v int64, _ *repository.MembershipCheck) error {
	row, ok := f.terms[term.SK]
	if !ok || row.State != model.TermStateDeleted || row.Version != v {
		return cond()
	}
	next := *term
	next.Version = v + 1
	f.terms[term.SK] = next
	return nil
}
func (f *fakeDictStore) DeleteManualTerm(_ context.Context, _ string, key string, v int64, _ *repository.MembershipCheck) error {
	row, ok := f.terms[model.DictTermSK(key)]
	if !ok || row.Version != v || row.Origin != model.TermOriginManual || row.State != model.TermStateActive {
		return cond()
	}
	delete(f.terms, row.SK)
	return nil
}
func (f *fakeDictStore) DeleteTombstone(_ context.Context, _ string, key string, v int64, _ *repository.MembershipCheck) error {
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
	fx := newBuilderFixture(t, "a")
	svc := fx.svc
	resp, err := svc.CreateTerm(ctx, "u1", model.CreateDictTermRequest{DisplayAs: "Alpha Grid", HeardAs: []string{"알파 그리드"}})
	if err != nil || resp.Item.Origin != model.TermOriginManual || resp.Item.Version != 1 || resp.Item.Phrase != "Alpha Grid" || resp.Status != "PENDING" {
		t.Fatalf("create = %+v %v", resp, err)
	}
	if fx.tr.count("create:") != 1 || fx.tr.count("update:") != 0 || resp.Vocabularies.Ko.Status != model.VocabStatusBuilding {
		t.Fatalf("manual create must start an A/B build: %v %+v", fx.tr.calls, resp.Vocabularies)
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

func TestLegacyPutNeverTombstonesOrTouchesAutoRows(t *testing.T) {
	ctx := context.Background()
	store := migratedStore()
	keep := store.seedRow("Alpha Grid", "Alpha-Grid", model.TermOriginManual, model.TermStateActive, 1)
	gone := store.seedRow("Beta Node", "Beta-Node", model.TermOriginManual, model.TermStateActive, 1)
	auto := store.seedRow("Gamma Cloud", "Gamma-Cloud", model.TermOriginAuto, model.TermStateActive, 3)
	hidden := store.seedRow("Delta Mesh", "Delta-Mesh", model.TermOriginAuto, model.TermStateSuggested, 5)
	absentAuto := store.seedRow("Zeta Port", "Zeta-Port", model.TermOriginAuto, model.TermStateActive, 2)
	tomb := store.seedRow("Epsilon", "", model.TermOriginAuto, model.TermStateDeleted, 7)
	fx := newBuilderFixture(t, "a")
	fx.store = store
	resp, err := fx.newService("a").UpdateDictionary(ctx, "u1", []model.DictionaryTerm{
		{Phrase: "Alpha-Grid", SoundsLike: "알파 그리드, 알파그릿", DisplayAs: "Alpha Grid"}, // edited
		{Phrase: "Gamma-Cloud-X", DisplayAs: "Gamma Cloud"},                         // auto row listed by GET
		{Phrase: "Omega", DisplayAs: "Omega"},                                       // new
		{Phrase: "Epsilon", DisplayAs: "Epsilon"},                                   // explicit re-add
	})
	if err != nil {
		t.Fatal(err)
	}
	if row := store.terms[model.DictTermSK(keep)]; len(row.HeardAs) != 2 || row.Version != 2 {
		t.Fatalf("changed manual term must be edited: %+v", row)
	}
	if _, ok := store.terms[model.DictTermSK(gone)]; ok {
		t.Fatal("a manual term absent from the body must be hard-deleted")
	}
	for key, version := range map[string]int64{auto: 3, hidden: 5, absentAuto: 2} {
		if row := store.terms[model.DictTermSK(key)]; row.Version != version || row.State == model.TermStateDeleted {
			t.Fatalf("legacy PUT touched an auto or suggested row: %+v", row)
		}
	}
	if row := store.terms[model.DictTermSK(tomb)]; row.State != model.TermStateActive || row.Origin != model.TermOriginManual {
		t.Fatalf("an explicit re-add restores the tombstone as manual: %+v", row)
	}
	deleted := 0
	for _, row := range store.terms {
		if row.State == model.TermStateDeleted {
			deleted++
		}
	}
	if deleted != 0 || store.terms[model.DictTermSK(TermKey("Omega"))].Origin != model.TermOriginManual {
		t.Fatalf("legacy PUT must create manual rows and never tombstone: %d tombstones", deleted)
	}
	ko := fx.tr.vocabs["ttobak-vocab-a000000000000001"]
	table := fx.objects.puts[strings.TrimPrefix(ko.uri, "s3://assets/")]
	// Five Latin terms would qualify en-US, but en-US builds are deferred.
	if ko == nil || ko.lang != transcribeTypes.LanguageCodeKoKr || fx.tr.count("create:") != 1 || !strings.Contains(table, "Omega\t\t\tOmega\n") || resp.Status != "PENDING" {
		t.Fatalf("legacy PUT must start an A/B build: %v %q %q", fx.tr.calls, table, resp.Status)
	}
	if len(resp.Terms) != 5 {
		t.Fatalf("legacy response lists the active rows: %+v", resp.Terms)
	}
	if len(resp.Warnings) != 1 || resp.Warnings[0] != legacyIgnoredWarning {
		t.Fatalf("an ignored edit of an auto row must be reported: %v", resp.Warnings)
	}
	for _, blobTerm := range store.blob.Terms {
		if blobTerm.DisplayAs == "Gamma Cloud" || blobTerm.DisplayAs == "Zeta Port" {
			t.Fatalf("auto terms must not be copied into the legacy blob: %+v", store.blob.Terms)
		}
	}
}

func TestLegacyPutRoundTripsGetOutput(t *testing.T) {
	ctx := context.Background()
	store := migratedStore()
	store.seedRow("Auto Term", "", model.TermOriginAuto, model.TermStateActive, 1)
	for i := 0; i < 450; i++ {
		store.seedRow(fmt.Sprintf("Manual %03d", i), fmt.Sprintf("Manual-%03d", i), model.TermOriginManual, model.TermStateActive, 1)
	}
	for i := 0; i < 100; i++ {
		store.seedRow(fmt.Sprintf("Auto %03d", i), fmt.Sprintf("Auto-%03d", i), model.TermOriginAuto, model.TermStateActive, 1)
	}
	svc := newTestDictService(store, nil)
	empty := ""
	if _, err := svc.CreateTerm(ctx, "u1", model.CreateDictTermRequest{DisplayAs: "No Phrase", Phrase: &empty}); err != nil {
		t.Fatal(err)
	}
	got, err := svc.GetDictionaryWithTerms(ctx, "u1")
	if err != nil || len(got.Terms) != 552 {
		t.Fatalf("GET = %d terms %v", len(got.Terms), err)
	}
	body := append(append([]model.DictionaryTerm{}, got.Terms...), model.DictionaryTerm{Phrase: "Omega", DisplayAs: "Omega"})
	resp, err := svc.UpdateDictionary(ctx, "u1", body)
	if err != nil || len(resp.Warnings) != 0 || len(resp.Terms) != 553 {
		t.Fatalf("GET-then-PUT with phrase-less and >500 union terms must succeed: %v %v %d", err, resp, len(got.Terms))
	}
	if row := store.terms[model.DictTermSK(TermKey("No Phrase"))]; row.Version != 1 || row.Origin != model.TermOriginManual {
		t.Fatalf("unchanged phrase-less manual term must be kept: %+v", row)
	}
	if len(store.blob.Terms) != 452 {
		t.Fatalf("blob holds manual terms only: %d", len(store.blob.Terms))
	}

	tooMany := append([]model.DictionaryTerm{}, body...)
	for i := 0; i < 50; i++ {
		tooMany = append(tooMany, model.DictionaryTerm{Phrase: fmt.Sprintf("Extra-%02d", i)})
	}
	var input *TermInputError
	if _, err := svc.UpdateDictionary(ctx, "u1", tooMany); !errors.As(err, &input) || input.Field != "terms" {
		t.Fatalf("more than %d manual terms must be rejected: %v", MaxManualTermsPerScope, err)
	}
}

// A term created through the legacy PUT with an empty displayAs, then given a
// new phrase through v2, must survive an unchanged GET-to-PUT round trip with
// its termId, version and v2 settings.
func TestLegacyPutRoundTripKeepsDisplayEmptyRowAfterPhraseEdit(t *testing.T) {
	ctx := context.Background()
	store := migratedStore()
	svc := newTestDictService(store, nil)
	if _, err := svc.UpdateDictionary(ctx, "u1", []model.DictionaryTerm{{Phrase: "Kappa-One"}}); err != nil {
		t.Fatal(err)
	}
	key := TermKey("Kappa-One")
	row := store.terms[model.DictTermSK(key)]
	if !row.LegacyDisplayEmpty {
		t.Fatalf("legacy term without displayAs must record it: %+v", row)
	}
	phrase, typ := "Kappa-Two", "product"
	if _, err := svc.UpdateTerm(ctx, "u1", key, model.UpdateDictTermRequest{Version: ver(row.Version), Phrase: &phrase, Type: &typ}); err != nil {
		t.Fatal(err)
	}
	edited := store.terms[model.DictTermSK(key)]
	got, err := svc.GetDictionaryWithTerms(ctx, "u1")
	if err != nil || len(got.Terms) != 1 {
		t.Fatalf("GET = %+v %v", got, err)
	}
	if got.Terms[0].Phrase != "Kappa-Two" || legacyTermKey(got.Terms[0]) != key {
		t.Fatalf("legacy GET must keep naming the stored term after a phrase edit: %+v", got.Terms[0])
	}
	resp, err := svc.UpdateDictionary(ctx, "u1", got.Terms)
	if err != nil || len(resp.Terms) != 1 || resp.Terms[0] != got.Terms[0] {
		t.Fatalf("unchanged round trip = %+v %v", resp, err)
	}
	if len(store.terms) != 1 {
		t.Fatalf("round trip must not create a second row: %d rows", len(store.terms))
	}
	after := store.terms[model.DictTermSK(key)]
	if after.Version != edited.Version || after.Type != typ || after.Phrase != "Kappa-Two" || after.UserEditedAt != edited.UserEditedAt {
		t.Fatalf("unchanged round trip must keep the stored row: before %+v after %+v", edited, after)
	}
}

func TestLegacyPutReportsHiddenSuggestion(t *testing.T) {
	store := migratedStore()
	key := store.seedRow("Delta Mesh", "Delta-Mesh", model.TermOriginAuto, model.TermStateSuggested, 5)
	resp, err := newTestDictService(store, nil).UpdateDictionary(context.Background(), "u1", []model.DictionaryTerm{{Phrase: "Delta-Mesh", DisplayAs: "Delta Mesh"}})
	if err != nil || len(resp.Warnings) == 0 || resp.Warnings[0] != legacyIgnoredWarning {
		t.Fatalf("a body term naming a suggestion must warn: %+v %v", resp, err)
	}
	if row := store.terms[model.DictTermSK(key)]; row.Version != 5 || row.State != model.TermStateSuggested {
		t.Fatalf("the suggestion must stay untouched: %+v", row)
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

func TestLegacyPutValidation(t *testing.T) {
	ctx := context.Background()
	store := migratedStore()
	store.seedRow("X1", "X", model.TermOriginManual, model.TermStateActive, 1)
	// A stored term that predates the validator (one-rune phrase as display).
	key := TermKey("Q")
	store.terms[model.DictTermSK(key)] = model.DictTerm{SK: model.DictTermSK(key), TermKey: key, DisplayAs: "Q", Phrase: "Q",
		Origin: model.TermOriginManual, State: model.TermStateActive, Version: 1, LegacyDisplayEmpty: true}
	svc := newTestDictService(store, nil)
	cases := []struct {
		name  string
		terms []model.DictionaryTerm
		field string
	}{
		{"tab in a new term", []model.DictionaryTerm{{Phrase: "Q"}, {Phrase: "New", DisplayAs: "New\tTerm"}}, "terms[1].displayAs"},
		{"newline in an alias", []model.DictionaryTerm{{Phrase: "New", SoundsLike: "뉴\n텀"}}, "terms[0].heardAs"},
		{"distinct terms sharing a key", []model.DictionaryTerm{{Phrase: "A.I"}, {Phrase: "AI"}}, "terms[1]"},
		{"too many terms", make([]model.DictionaryTerm, MaxLegacyTerms+1), "terms"},
		{"new term without phrase", []model.DictionaryTerm{{DisplayAs: "Fresh Term"}}, "terms[0].phrase"},
		{"unchanged legacy term is kept", []model.DictionaryTerm{{Phrase: "Q"}, {Phrase: "Delta", DisplayAs: "Delta"}, {Phrase: "Delta", DisplayAs: "Delta"}}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := store.saveCalls
			_, err := svc.UpdateDictionary(ctx, "u1", tc.terms)
			var input *TermInputError
			if tc.field == "" {
				if err != nil || store.terms[model.DictTermSK(key)].Version != 1 {
					t.Fatalf("got %v", err)
				}
				return
			}
			if !errors.As(err, &input) || input.Field != tc.field || store.saveCalls != before {
				t.Fatalf("want %s rejected before any write, got %v", tc.field, err)
			}
		})
	}
}

func TestLegacyWritesMigrateTheBlobFirst(t *testing.T) {
	ctx := context.Background()
	store := newFakeDictStore()
	store.blob = legacyBlob("READY")
	fx := newBuilderFixture(t, "a")
	fx.store = store
	svc := fx.newService("a")
	fx.tr.vocabs["ttobak-vocab-0123456789abcdef"] = &fakeVocabulary{state: transcribeTypes.VocabularyStateReady}
	resp, err := svc.UpdateDictionary(ctx, "u1", []model.DictionaryTerm{store.blob.Terms[0], {Phrase: "Omega", DisplayAs: "Omega"}})
	if err != nil || store.blob.MigratedAt == "" || len(store.terms) != 2 || len(resp.Terms) != 2 {
		t.Fatalf("legacy PUT must migrate and replace the list: %v %+v rows=%d", err, store.blob, len(store.terms))
	}
	state := store.vocab[model.VocabSK(model.VocabContextPersonal, model.VocabLangKo)]
	if state.Status != model.VocabStatusBuilding || state.ActiveName != "ttobak-vocab-0123456789abcdef" || !state.ActiveReady || state.TermCount != 2 || state.InactiveName == state.ActiveName {
		t.Fatalf("the build goes to a new slot while the adopted name stays active: %+v", state)
	}
	if err := svc.DeleteTerm(ctx, "u1", "Omega"); err != nil || len(store.terms) != 1 {
		t.Fatalf("legacy DELETE hard-deletes the manual row: %v", err)
	}
	if err := svc.DeleteTerm(ctx, "u1", "Ttobak-Sync"); err != nil || fx.tr.count("delete:") != 0 {
		t.Fatalf("emptying the dictionary never deletes a vocabulary at once: %v %v", err, fx.tr.calls)
	}
	store.seedRow("Auto Term", "Auto-Term", model.TermOriginAuto, model.TermStateActive, 1)
	if err := svc.DeleteTerm(ctx, "u1", "Auto-Term"); !errors.Is(err, ErrDictionaryTermNotManual) {
		t.Fatalf("legacy DELETE must not remove auto rows: %v", err)
	}
	if err := svc.DeleteTerm(ctx, "u1", "Missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v", err)
	}
}

func TestConflictingBlobKeepsLegacyPathUntilResolved(t *testing.T) {
	ctx := context.Background()
	store := newFakeDictStore()
	store.blob = &model.UserDictionary{UserID: "u1", Terms: []model.DictionaryTerm{{Phrase: "A.I"}, {Phrase: "AI"}, {Phrase: "Delta"}}}
	svc := newTestDictService(store, nil)
	resp, err := svc.GetDictionaryWithTerms(ctx, "u1")
	if err != nil || !resp.MigrationPending || len(resp.Terms) != 3 || len(resp.Items) != 0 {
		t.Fatalf("a refused blob stays readable and flagged: %+v %v", resp, err)
	}
	if _, err := svc.CreateTerm(ctx, "u1", model.CreateDictTermRequest{DisplayAs: "Omega"}); !errors.Is(err, ErrLegacyDictionaryConflict) {
		t.Fatalf("v2 writes wait for the conflict to be resolved: %v", err)
	}
	if err := svc.DeleteTerm(ctx, "u1", "AI"); err != nil || len(store.blob.Terms) != 2 {
		t.Fatalf("legacy DELETE keeps the blob path: %v %+v", err, store.blob.Terms)
	}
	if store.blob.MigratedAt == "" || len(store.terms) != 2 {
		t.Fatalf("migration retries once the conflict is gone: %+v rows=%d", store.blob, len(store.terms))
	}
	resp, err = svc.GetDictionaryWithTerms(ctx, "u1")
	if err != nil || resp.MigrationPending || len(resp.Items) != 2 || resp.Items[0].TermID != TermKey("A.I") {
		t.Fatalf("migrated items carry termIds: %+v %v", resp, err)
	}
}

func TestListLayerPersonalMigratesLegacyBlob(t *testing.T) {
	ctx := context.Background()
	store := newFakeDictStore()
	store.blob = legacyBlob("READY")
	svc := newTestDictService(store, nil)
	items, err := svc.ListLayer(ctx, "u1", model.DictLayerPersonal)
	if err != nil || store.blob.MigratedAt == "" || len(items) != len(store.blob.Terms) {
		t.Fatalf("personal list must migrate the legacy blob first: %v migrated=%q items=%+v", err, store.blob.MigratedAt, items)
	}
}

func TestTermViewOmitsSharedLayerSourceRefs(t *testing.T) {
	kb := model.DictSourceRef{Kind: model.DictSourceKB, Ref: "kb/user-1/plan.md", At: "2026-10-26T00:00:00Z"}
	for _, pk := range []string{model.PrefixAccount + "acc-1", model.PrefixDictCommon} {
		view := termView(model.DictTerm{PK: pk, Sources: []model.DictSourceRef{kb}})
		if len(view.Sources) != 1 || view.Sources[0].Kind != kb.Kind || view.Sources[0].At != kb.At {
			t.Fatalf("%s: sources = %+v", pk, view.Sources)
		}
		if ref := view.Sources[0].Ref; ref != "" {
			t.Fatalf("%s: shared view must not carry a source ref: %q", pk, ref)
		}
	}
	personal := termView(model.DictTerm{PK: model.PrefixUser + "user-1", Sources: []model.DictSourceRef{kb}})
	if personal.Sources[0].Ref != kb.Ref {
		t.Fatalf("personal ref = %q, want raw", personal.Sources[0].Ref)
	}
}
