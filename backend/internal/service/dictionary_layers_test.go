package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

// The single-partition fakeDictStore never sees Common writes.
func (f *fakeDictStore) PutCommonDictTerm(context.Context, *model.DictTerm, int64, *model.DictTermLog) (repository.DictWriteResult, error) {
	return repository.DictWriteResult{}, errors.New("fakeDictStore: no common partition")
}
func (f *fakeDictStore) ListDictTermLog(context.Context, string, string) ([]model.DictTermLog, error) {
	return nil, nil
}
func (f *fakeDictStore) TrimDictTermLog(context.Context, string, string, int) error { return nil }

// layerStore is a multi-partition store mirroring the repository's version,
// membership and audit conditions. Unused DictionaryStore methods panic.
type layerStore struct {
	DictionaryStore
	terms   map[string]map[string]model.DictTerm // PK -> SK -> row
	logs    map[string]map[string]model.DictTermLog
	members map[string]bool // accountID/userID
}

func newLayerStore() *layerStore {
	return &layerStore{terms: map[string]map[string]model.DictTerm{}, logs: map[string]map[string]model.DictTermLog{}, members: map[string]bool{}}
}

func (l *layerStore) member(pk string, m *repository.MembershipCheck) error {
	scope, accountID, _ := model.TermScopeOf(pk)
	if scope != model.TermScopeAccount {
		return nil
	}
	if m == nil || m.AccountID != accountID {
		return repository.ErrInvalidInput
	}
	if !l.members[accountID+"/"+m.UserID] {
		return repository.ErrNotAccountMember
	}
	return nil
}

func (l *layerStore) put(t model.DictTerm) {
	if l.terms[t.PK] == nil {
		l.terms[t.PK] = map[string]model.DictTerm{}
	}
	l.terms[t.PK][t.SK] = t
}

func (l *layerStore) seed(pk, display string) model.DictTerm {
	key := TermKey(display)
	scope, _, _ := model.TermScopeOf(pk)
	t := model.DictTerm{
		PK: pk, SK: model.DictTermSK(key), TermKey: key, Scope: scope, State: model.TermStateActive,
		DisplayAs: display, Phrase: display, HeardAs: []string{}, Origin: model.TermOriginAuto, TranscribeKo: true,
		Sources: []model.DictSourceRef{{Kind: model.DictSourceKB, Ref: "doc-1", At: "2026-10-18T00:00:00.000000Z"}},
		AddedAt: "2026-10-18T00:00:00.000000Z", UpdatedAt: "2026-10-18T00:00:00.000000Z", Version: 1, EntityType: model.EntityTypeDictTerm,
	}
	l.put(t)
	return t
}

func (l *layerStore) row(pk, key string) (model.DictTerm, bool) {
	t, ok := l.terms[pk][model.DictTermSK(key)]
	return t, ok
}

func (l *layerStore) GetDictionary(context.Context, string) (*model.UserDictionary, error) {
	return nil, nil
}
func (l *layerStore) ListDictTerms(_ context.Context, pk string) ([]model.DictTerm, error) {
	var out []model.DictTerm
	for _, t := range l.terms[pk] {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SK < out[j].SK })
	return out, nil
}
func (l *layerStore) GetDictTerm(_ context.Context, pk, key string) (*model.DictTerm, error) {
	if t, ok := l.row(pk, key); ok {
		return &t, nil
	}
	return nil, nil
}
func (l *layerStore) CreateDictTerm(_ context.Context, t *model.DictTerm, m *repository.MembershipCheck) error {
	if err := l.member(t.PK, m); err != nil {
		return err
	}
	if _, ok := l.terms[t.PK][t.SK]; ok {
		return conflict()
	}
	l.put(*t)
	return nil
}
func (l *layerStore) UpdateDictTerm(_ context.Context, pk, key string, ch repository.DictTermChange, actor, now string, v int64, m *repository.MembershipCheck) error {
	if err := l.member(pk, m); err != nil {
		return err
	}
	t, ok := l.row(pk, key)
	if !ok || t.Version != v || t.State == model.TermStateDeleted {
		return conflict()
	}
	l.put(applyTermChange(t, ch, actor, now))
	return nil
}
func (l *layerStore) TombstoneDictTerm(_ context.Context, pk, key, actor, now string, v int64, m *repository.MembershipCheck) (repository.DictWriteResult, error) {
	if err := l.member(pk, m); err != nil {
		return repository.DictWriteResult{}, err
	}
	t, ok := l.row(pk, key)
	if !ok || t.Version != v || t.State == model.TermStateDeleted {
		return repository.DictWriteResult{}, conflict()
	}
	l.put(tombstoneOf(t, actor, now))
	return repository.DictWriteResult{}, nil
}
func (l *layerStore) RestoreDictTerm(_ context.Context, t *model.DictTerm, v int64, m *repository.MembershipCheck) error {
	if err := l.member(t.PK, m); err != nil {
		return err
	}
	cur, ok := l.row(t.PK, t.TermKey)
	if !ok || cur.Version != v || cur.State != model.TermStateDeleted {
		return conflict()
	}
	next := *t
	next.Version = v + 1
	l.put(next)
	return nil
}
func (l *layerStore) DeleteTombstone(_ context.Context, pk, key string, v int64, m *repository.MembershipCheck) error {
	if err := l.member(pk, m); err != nil {
		return err
	}
	delete(l.terms[pk], model.DictTermSK(key))
	return nil
}
func (l *layerStore) GetVocabularyState(context.Context, string, string, string) (*model.VocabularyState, error) {
	return nil, nil
}
func (l *layerStore) PutCommonDictTerm(_ context.Context, t *model.DictTerm, v int64, entry *model.DictTermLog) (repository.DictWriteResult, error) {
	if t.PK != model.PrefixDictCommon || entry == nil || entry.PK != model.PrefixDictCommon || entry.TermKey != t.TermKey {
		return repository.DictWriteResult{}, repository.ErrInvalidInput
	}
	cur, ok := l.row(t.PK, t.TermKey)
	if (v == 0 && ok) || (v != 0 && (!ok || cur.Version != v)) {
		return repository.DictWriteResult{}, conflict()
	}
	if _, dup := l.logs[entry.PK][entry.SK]; dup {
		return repository.DictWriteResult{}, conflict()
	}
	l.put(*t)
	if l.logs[entry.PK] == nil {
		l.logs[entry.PK] = map[string]model.DictTermLog{}
	}
	l.logs[entry.PK][entry.SK] = *entry
	return repository.DictWriteResult{}, nil
}
func (l *layerStore) ListDictTermLog(_ context.Context, pk, key string) ([]model.DictTermLog, error) {
	var out []model.DictTermLog
	for sk, e := range l.logs[pk] {
		if strings.HasPrefix(sk, model.PrefixDictLog+key+"#") {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SK > out[j].SK })
	return out, nil
}
func (l *layerStore) TrimDictTermLog(ctx context.Context, pk, key string, keep int) error {
	rows, _ := l.ListDictTermLog(ctx, pk, key)
	for i := keep; i < len(rows); i++ {
		delete(l.logs[pk], rows[i].SK)
	}
	return nil
}

func newLayerService(store *layerStore) *DictionaryService {
	svc := NewDictionaryServiceWithStore(store, nil)
	tick := 0
	svc.now = func() time.Time {
		tick++
		return time.Date(2026, 10, 26, 9, 0, 0, 0, time.UTC).Add(time.Duration(tick) * time.Millisecond)
	}
	svc.SetMembershipReader(func(_ context.Context, accountID, userID string) (bool, error) {
		return store.members[accountID+"/"+userID], nil
	})
	return svc
}

func TestListLayerAccountRequiresMember(t *testing.T) {
	store := newLayerStore()
	store.members["acc1/u1"] = true
	store.seed(model.PrefixAccount+"acc1", "AlphaGrid")
	store.seed(model.PrefixDictCommon, "K-culture")
	store.seed(model.PrefixUser+"u1", "BetaNode")
	svc := newLayerService(store)
	ctx := context.Background()

	if _, err := svc.ListLayer(ctx, "u2", "account:acc1"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("non-member list: %v, want ErrForbidden", err)
	}
	items, err := svc.ListLayer(ctx, "u1", "account:acc1")
	if err != nil || len(items) != 1 || items[0].DisplayAs != "AlphaGrid" || items[0].Layer != "account:acc1" || len(items[0].Sources) != 1 {
		t.Fatalf("member list: %+v %v", items, err)
	}
	common, err := svc.ListLayer(ctx, "u2", "common")
	if err != nil || len(common) != 1 || common[0].Layer != model.DictLayerCommon {
		t.Fatalf("common list: %+v %v", common, err)
	}
	personal, err := svc.ListLayer(ctx, "u1", "personal")
	if err != nil || len(personal) != 1 || personal[0].Layer != model.DictLayerPersonal {
		t.Fatalf("personal list: %+v %v", personal, err)
	}
	for _, bad := range []string{"", "account:", "team:acc1", "ACCOUNT#acc1"} {
		var input *TermInputError
		if _, err := svc.ListLayer(ctx, "u1", bad); !errors.As(err, &input) {
			t.Fatalf("layer %q: %v, want TermInputError", bad, err)
		}
	}

	// Account writes: early 403 for a non-member; a member's write carries
	// the membership check, and a membership lost before the write is 403.
	if _, err := svc.CreateLayerTerm(ctx, "u2", "account:acc1", model.CreateDictTermRequest{DisplayAs: "GammaRay"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("non-member create: %v", err)
	}
	created, err := svc.CreateLayerTerm(ctx, "u1", "account:acc1", model.CreateDictTermRequest{DisplayAs: "GammaRay"})
	if err != nil || created.Item.Layer != "account:acc1" {
		t.Fatalf("member create: %+v %v", created, err)
	}
	row, ok := store.row(model.PrefixAccount+"acc1", created.TermID)
	if !ok || row.Scope != model.TermScopeAccount || row.Origin != model.TermOriginManual || row.CreatedBy != "u1" {
		t.Fatalf("account row: %+v", row)
	}
	v := row.Version
	phrase := "감마레이"
	if _, err := svc.UpdateLayerTerm(ctx, "u1", "account:acc1", created.TermID, model.UpdateDictTermRequest{Version: &v, Phrase: &phrase}); err != nil {
		t.Fatal(err)
	}
	if row, _ = store.row(model.PrefixAccount+"acc1", created.TermID); row.Phrase != phrase || row.UpdatedBy != "u1" {
		t.Fatalf("account update: %+v", row)
	}
	store.members["acc1/u1"] = false
	svc.SetMembershipReader(func(context.Context, string, string) (bool, error) { return true, nil }) // stale reader
	if _, err := svc.DeleteLayerTerm(ctx, "u1", "account:acc1", created.TermID, nil); !errors.Is(err, repository.ErrNotAccountMember) {
		t.Fatalf("transaction membership: %v", err)
	}
	store.members["acc1/u1"] = true
	if _, err := svc.DeleteLayerTerm(ctx, "u1", "account:acc1", created.TermID, nil); err != nil {
		t.Fatal(err)
	}
	if row, _ = store.row(model.PrefixAccount+"acc1", created.TermID); row.State != model.TermStateDeleted {
		t.Fatalf("account delete: %+v", row)
	}
	if len(store.logs) != 0 {
		t.Fatalf("account writes must not audit: %+v", store.logs)
	}
	if _, err := svc.CreateLayerTerm(ctx, "u1", "common", model.CreateDictTermRequest{DisplayAs: "DeltaWave"}); err == nil {
		t.Fatal("common create must be refused")
	}

	unwired := NewDictionaryServiceWithStore(store, nil)
	if _, err := unwired.ListLayer(ctx, "u1", "account:acc1"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("no membership reader: %v, want ErrForbidden", err)
	}
}

func decodeSnapshot(t *testing.T, raw string) model.DictTerm {
	t.Helper()
	var term model.DictTerm
	if err := json.Unmarshal([]byte(raw), &term); err != nil {
		t.Fatalf("snapshot %q: %v", raw, err)
	}
	return term
}

func TestCommonEditKeepsAuditAndRestores(t *testing.T) {
	store := newLayerStore()
	seeded := store.seed(model.PrefixDictCommon, "K-culture")
	key := seeded.TermKey
	svc := newLayerService(store)
	ctx := context.Background()

	phrase := "케이컬처"
	if err := svc.UpdateCommonTerm(ctx, "u1", key, repository.DictTermChange{Phrase: &phrase}, 1); err != nil {
		t.Fatal(err)
	}
	logs, err := svc.ListTermLog(ctx, "common", key)
	if err != nil || len(logs) != 1 {
		t.Fatalf("logs after edit: %+v %v", logs, err)
	}
	edit := logs[0]
	if edit.Actor != "u1" || edit.Action != model.DictLogActionUpdate || edit.SK != model.DictLogSK(key, edit.At) ||
		decodeSnapshot(t, edit.Before).Phrase != "K-culture" || decodeSnapshot(t, edit.After).Phrase != phrase {
		t.Fatalf("edit log: %+v", edit)
	}
	row, _ := store.row(model.PrefixDictCommon, key)
	if row.Phrase != phrase || row.UpdatedBy != "u1" || row.UpdatedAt != edit.At || row.UserEditedAt == "" || row.Version != 2 {
		t.Fatalf("edited row: %+v", row)
	}
	if after := decodeSnapshot(t, edit.After); after.Version != row.Version || after.UpdatedAt != row.UpdatedAt {
		t.Fatalf("After must equal the stored row: %+v vs %+v", after, row)
	}
	if err := svc.UpdateCommonTerm(ctx, "u1", key, repository.DictTermChange{Phrase: &phrase}, 1); !errors.Is(err, ErrDictionaryVersionConflict) {
		t.Fatalf("stale edit: %v", err)
	}

	if err := svc.DeleteCommonTerm(ctx, "u2", key, nil); err != nil {
		t.Fatal(err)
	}
	row, _ = store.row(model.PrefixDictCommon, key)
	if row.State != model.TermStateDeleted || row.DisplayAs != "" || row.UpdatedBy != "u2" {
		t.Fatalf("tombstone: %+v", row)
	}
	logs, _ = svc.ListTermLog(ctx, "common", key)
	if len(logs) != 2 || logs[0].Action != model.DictLogActionDelete || logs[0].Actor != "u2" || decodeSnapshot(t, logs[0].Before).DisplayAs != "K-culture" {
		t.Fatalf("delete log: %+v", logs)
	}
	if _, err := svc.ListLayer(ctx, "u1", "common"); err != nil {
		t.Fatal(err)
	}

	if view := TermLogView(logs[0]); view.LogID != logs[0].At || view.Before == nil || view.Before.DisplayAs != "K-culture" || view.After.State != model.TermStateDeleted {
		t.Fatalf("log view: %+v", view)
	}
	if err := svc.RestoreCommonTerm(ctx, "u3", key, logs[0].SK); err != nil {
		t.Fatal(err)
	}
	row, _ = store.row(model.PrefixDictCommon, key)
	if row.State != model.TermStateActive || row.DisplayAs != "K-culture" || row.Phrase != phrase || row.UpdatedBy != "u3" || row.Version != 4 || row.DeletedAt != "" {
		t.Fatalf("restored: %+v", row)
	}
	logs, _ = svc.ListTermLog(ctx, "common", key)
	if len(logs) != 3 || logs[0].Action != model.DictLogActionRestore || logs[0].Actor != "u3" || decodeSnapshot(t, logs[0].Before).State != model.TermStateDeleted {
		t.Fatalf("restore log: %+v", logs)
	}
	if err := svc.RestoreCommonTerm(ctx, "u3", key, model.DictLogSK(key, "2020-01-01T00:00:00.000000Z")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown log row: %v", err)
	}

	for i := 0; i < 25; i++ {
		p := fmt.Sprintf("K-culture %d", i)
		if err := svc.UpdateCommonTerm(ctx, "u1", key, repository.DictTermChange{Phrase: &p}, row.Version); err != nil {
			t.Fatal(err)
		}
		row, _ = store.row(model.PrefixDictCommon, key)
	}
	logs, _ = svc.ListTermLog(ctx, "common", key)
	if len(logs) != model.MaxDictLogsPerTerm || decodeSnapshot(t, logs[0].After).Phrase != "K-culture 24" {
		t.Fatalf("kept %d log rows, newest %+v", len(logs), logs[0])
	}

	// The request-shaped route validates, writes and lists the editor.
	v := row.Version
	display := "k-culture"
	resp, err := svc.UpdateLayerTerm(ctx, "u4", "common", key, model.UpdateDictTermRequest{Version: &v, DisplayAs: &display})
	if err != nil || resp.Item.DisplayAs != display || resp.Item.UpdatedBy != "u4" || resp.Item.Layer != model.DictLayerCommon {
		t.Fatalf("layer update: %+v %v", resp, err)
	}
	if _, err := svc.ListTermLog(ctx, "personal", key); !errors.Is(err, ErrNotFound) {
		t.Fatalf("personal log: %v, want ErrNotFound", err)
	}
}

func TestListCommonDeletedIncludesLastSnapshot(t *testing.T) {
	store := newLayerStore()
	gone := store.seed(model.PrefixDictCommon, "K-culture")
	store.seed(model.PrefixDictCommon, "AlphaGrid") // stays live
	svc := newLayerService(store)
	ctx := context.Background()

	phrase := "케이컬처"
	if err := svc.UpdateCommonTerm(ctx, "u1", gone.TermKey, repository.DictTermChange{Phrase: &phrase}, 1); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteCommonTerm(ctx, "u2", gone.TermKey, nil); err != nil {
		t.Fatal(err)
	}
	got, err := svc.ListDeletedCommon(ctx)
	if err != nil || len(got) != 1 {
		t.Fatalf("deleted list: %+v %v", got, err)
	}
	row, _ := store.row(model.PrefixDictCommon, gone.TermKey)
	d := got[0]
	// The snapshot is the term before the delete (with the edited phrase), not the cleared tombstone.
	if d.TermID != gone.TermKey || d.DisplayAs != "K-culture" || d.Phrase != phrase || d.DeletedBy != "u2" || d.DeletedAt != row.DeletedAt || d.DeletedAt == "" {
		t.Fatalf("deleted item: %+v (tombstone %+v)", d, row)
	}

	// Restoring brings it back to the live list and empties the deleted list.
	logs, _ := svc.ListTermLog(ctx, "common", gone.TermKey)
	if err := svc.RestoreCommonTerm(ctx, "u3", gone.TermKey, logs[0].SK); err != nil {
		t.Fatal(err)
	}
	if got, err = svc.ListDeletedCommon(ctx); err != nil || len(got) != 0 {
		t.Fatalf("after restore: %+v %v", got, err)
	}
}
