package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	tt "github.com/aws/aws-sdk-go-v2/service/transcribe/types"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

// Synthetic fixtures only.

func layerRow(pk, display, origin string, hits int) model.DictTerm {
	key := TermKey(display)
	return model.DictTerm{PK: pk, SK: model.DictTermSK(key), TermKey: key, DisplayAs: display, Phrase: display,
		Origin: origin, State: model.TermStateActive, TranscribeKo: true, Hits: hits, AddedAt: "2026-10-01T00:00:00.000000Z"}
}

// letters spells n in base 26 so every generated phrase is a valid phrase.
func letters(n int) string {
	s := ""
	for i := 0; i < 4; i++ {
		s = string(rune('a'+n%26)) + s
		n /= 26
	}
	return s
}

func tsvPhrases(body string) []string {
	var out []string
	for _, line := range strings.Split(strings.TrimSuffix(body, "\n"), "\n")[1:] {
		out = append(out, strings.SplitN(line, "\t", 2)[0])
	}
	return out
}

func TestLayeredPlanOrderAndDedupe(t *testing.T) {
	m, a := model.TermOriginManual, model.TermOriginAuto
	layers := []vocabLayer{
		{name: "personal-manual", rows: []model.DictTerm{layerRow("USER#u1", "Kilo", m, 0)}},
		{name: "account:a2", rows: []model.DictTerm{layerRow("ACCOUNT#a2", "Bravo", a, 9), layerRow("ACCOUNT#a2", "Alpha", m, 0)}},
		{name: "account:a1", rows: []model.DictTerm{layerRow("ACCOUNT#a1", "Charlie", a, 1), layerRow("ACCOUNT#a1", "kilo", m, 0)}},
		{name: "personal-auto", rows: []model.DictTerm{layerRow("USER#u1", "Delta", a, 5)}},
		{name: "common", rows: []model.DictTerm{layerRow(model.PrefixDictCommon, "Echo", a, 7), layerRow(model.PrefixDictCommon, "Bravo", a, 1)}},
	}
	// The a1 copy of kilo shares Kilo's termKey but not its phrase: the
	// personal row wins by termKey, so the a1 phrase is never written.
	layers[2].rows[1].Phrase = "Kilo-Alt"
	p := planVocabularyLayers(layers, model.VocabLangKo)
	want := VocabTSVHeader + "Kilo\t\t\tKilo\nAlpha\t\t\tAlpha\nBravo\t\t\tBravo\nCharlie\t\t\tCharlie\nDelta\t\t\tDelta\nEcho\t\t\tEcho\n"
	if p.body != want || p.count != 6 || p.empty {
		t.Fatalf("layer order, manual first within a layer, first layer wins:\n%q\nwant\n%q", p.body, want)
	}

	// One personal layer pair reproduces the single-list plan exactly.
	personal := []model.DictTerm{layerRow("USER#u1", "Delta", a, 5), layerRow("USER#u1", "Kilo", m, 0), layerRow("USER#u1", "Alpha", a, 9)}
	split := planVocabularyLayers([]vocabLayer{
		{name: "personal-manual", rows: personal[1:2]}, {name: "personal-auto", rows: []model.DictTerm{personal[0], personal[2]}},
	}, model.VocabLangKo)
	if old := planVocabulary(personal, model.VocabLangKo); split != old {
		t.Fatalf("a personal-only plan keeps its fingerprint: %+v vs %+v", split, old)
	}

	// The service reads the layers in order with the Account order applied.
	store := newLayerStore()
	store.put(layerRow("USER#u1", "Kilo", m, 0))
	store.put(layerRow("USER#u1", "Delta", a, 5))
	store.put(layerRow("ACCOUNT#a1", "Charlie", a, 1))
	store.put(layerRow("ACCOUNT#a2", "Bravo", a, 9))
	store.put(layerRow(model.PrefixDictCommon, "Echo", a, 7))
	svc := NewDictionaryServiceWithStore(store, nil)
	svc.SetMembershipReader(func(context.Context, string, string) (bool, error) { return true, nil })
	svc.SetAccountOrder(func(context.Context, string) ([]string, error) { return []string{"a2", "a1", "a2"}, nil })
	got, err := svc.vocabularyLayers(context.Background(), "u1")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, l := range got {
		names = append(names, fmt.Sprintf("%s=%d", l.name, len(l.rows)))
	}
	if strings.Join(names, ",") != "personal-manual=1,account:a2=1,account:a1=1,personal-auto=1,common=1" {
		t.Fatalf("layers: %v", names)
	}
	if p := planVocabularyLayers(got, model.VocabLangKo); strings.Join(tsvPhrases(p.body), ",") != "Kilo,Bravo,Charlie,Delta,Echo" {
		t.Fatalf("merged table: %q", p.body)
	}
}

func TestLayeredPlanCutsAtLimits(t *testing.T) {
	m, a := model.TermOriginManual, model.TermOriginAuto
	var manual, common []model.DictTerm
	for i := 0; i < 100; i++ {
		manual = append(manual, layerRow("USER#u1", "Man-"+letters(i), m, 0))
	}
	for i := 0; i < 1500; i++ {
		common = append(common, layerRow(model.PrefixDictCommon, "Com-"+letters(i), a, 1))
	}
	layers := []vocabLayer{{name: "personal-manual", rows: manual}, {name: "common", rows: common}}
	p := planVocabularyLayers(layers, model.VocabLangKo)
	phrases := tsvPhrases(p.body)
	if p.count != VocabMaxRows || len(phrases) != VocabMaxRows || len(p.body) > VocabMaxBytes {
		t.Fatalf("1,600 rows cut to %d (%d bytes)", p.count, len(p.body))
	}
	kept := map[string]bool{}
	for _, ph := range phrases[:100] {
		kept[ph] = strings.HasPrefix(ph, "Man-")
	}
	for i := 0; i < 100; i++ {
		if !kept["Man-"+letters(i)] {
			t.Fatalf("every personal manual row is kept first: Man-%s missing", letters(i))
		}
	}

	// The byte cap cuts at a whole row too.
	long := strings.Repeat("가", 60)
	for i := range common {
		common[i].DisplayAs = long + letters(i)
	}
	p = planVocabularyLayers(layers, model.VocabLangKo)
	if len(p.body) > VocabMaxBytes || p.count >= VocabMaxRows || !strings.HasSuffix(p.body, "\n") || len(tsvPhrases(p.body)) != p.count {
		t.Fatalf("byte cap: %d bytes %d rows", len(p.body), p.count)
	}
	if strings.Join(tsvPhrases(p.body)[:100], ",") != strings.Join(phrases[:100], ",") {
		t.Fatal("the byte cut drops Common rows before Personal manual ones")
	}
}

func TestLayeredPlanSkipsAccountsUserLeft(t *testing.T) {
	store := newLayerStore()
	store.put(layerRow("USER#u1", "Kilo", model.TermOriginManual, 0))
	store.put(layerRow("ACCOUNT#a1", "Charlie", model.TermOriginAuto, 1))
	store.put(layerRow("ACCOUNT#a2", "Bravo", model.TermOriginAuto, 9))
	ctx := context.Background()
	phrases := func(svc *DictionaryService) string {
		t.Helper()
		layers, err := svc.vocabularyLayers(ctx, "u1")
		if err != nil {
			t.Fatal(err)
		}
		return strings.Join(tsvPhrases(planVocabularyLayers(layers, model.VocabLangKo).body), ",")
	}
	svc := NewDictionaryServiceWithStore(store, nil)
	if got := phrases(svc); got != "Kilo" {
		t.Fatalf("no Account order: personal and common only, got %s", got)
	}
	// The order is stale (a2 left): the plan-time membership check drops it.
	svc.SetAccountOrder(func(context.Context, string) ([]string, error) { return []string{"a2", "a1", "", "x#y"}, nil })
	svc.SetMembershipReader(func(_ context.Context, accountID, userID string) (bool, error) {
		return accountID == "a1" && userID == "u1", nil
	})
	if got := phrases(svc); got != "Kilo,Charlie" {
		t.Fatalf("an Account the user left is absent, got %s", got)
	}
	// Without a membership reader no Account layer is trusted.
	svc.SetMembershipReader(nil)
	if got := phrases(svc); got != "Kilo" {
		t.Fatalf("no membership reader: no Account layer, got %s", got)
	}
	// A failing order func skips the Account layer for the step.
	svc.SetMembershipReader(func(context.Context, string, string) (bool, error) { return true, nil })
	svc.SetAccountOrder(func(context.Context, string) ([]string, error) { return nil, errors.New("synthetic") })
	if got := phrases(svc); got != "Kilo" {
		t.Fatalf("failing order: Account layer skipped, got %s", got)
	}
	// A failing membership read fails the listing (no table missing a
	// member's terms is built from a transient error).
	svc.SetAccountOrder(func(context.Context, string) ([]string, error) { return []string{"a1"}, nil })
	svc.SetMembershipReader(func(context.Context, string, string) (bool, error) { return false, errors.New("synthetic") })
	if _, err := svc.vocabularyLayers(ctx, "u1"); err == nil {
		t.Fatal("a membership read error is an error")
	}
}

// The order is read once per ensure; a failing order still builds the
// personal table.
func TestEnsureVocabulariesLayeredOrderFailureStillBuilds(t *testing.T) {
	fx := newBuilderFixture(t, "a")
	fx.add("Alpha Grid", "Alpha-Grid")
	calls := 0
	fx.svc.SetMembershipReader(func(context.Context, string, string) (bool, error) { return true, nil })
	fx.svc.SetAccountOrder(func(context.Context, string) ([]string, error) { calls++; return nil, errors.New("synthetic") })
	if _, warnings := fx.ensure(true); len(warnings) != 0 || calls != 1 {
		t.Fatalf("warnings %v, order calls %d", warnings, calls)
	}
	fx.tr.settle(tt.VocabularyStateReady)
	fx.ensure(false)
	if ko := fx.ko(); ko.ActiveName != name("a", 1) || ko.TermCount != 1 {
		t.Fatalf("the personal table builds without the Account layer: %+v", ko)
	}
}

type fakeRecencySource struct {
	meetings []model.Meeting
	accounts []model.AccountMember
	params   repository.ListMeetingsParams
	err      error
}

func (f *fakeRecencySource) ListMeetings(_ context.Context, p repository.ListMeetingsParams) (*repository.ListMeetingsResult, error) {
	f.params = p
	return &repository.ListMeetingsResult{Meetings: f.meetings}, f.err
}

func (f *fakeRecencySource) ListAccountsForUser(context.Context, string) ([]model.AccountMember, error) {
	return f.accounts, nil
}

func TestAccountRecencyOrder(t *testing.T) {
	at := func(id string) model.Meeting { return model.Meeting{AccountID: id} }
	src := &fakeRecencySource{
		meetings: []model.Meeting{at("a2"), at(""), at("gone"), at("a1"), at("a2")},
		accounts: []model.AccountMember{{AccountID: "a1"}, {AccountID: "a4"}, {AccountID: "a2"}},
	}
	order := AccountRecencyOrder(src)
	got, err := order(context.Background(), "u1")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "a2,a1,a4" {
		t.Fatalf("recent meetings first, then remaining memberships, never a left Account: %v", got)
	}
	if src.params.UserID != "u1" || src.params.Limit != accountOrderMeetingWindow || src.params.AccountIDs != nil || src.params.Cursor != "" {
		t.Fatalf("newest meetings of the user: %+v", src.params)
	}
	src.err = errors.New("synthetic")
	if _, err := order(context.Background(), "u1"); err == nil {
		t.Fatal("a meeting list error is returned")
	}
}
