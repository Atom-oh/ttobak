package repository

import (
	"context"
	"errors"
	"testing"

	"github.com/ttobak/backend/internal/model"
)

func commonTermAndLog() (*model.DictTerm, *model.DictTermLog) {
	term := &model.DictTerm{PK: model.PrefixDictCommon, SK: model.DictTermSK(acctKey), TermKey: acctKey, State: model.TermStateActive, Origin: model.TermOriginAuto, DisplayAs: "K-culture", Version: 3}
	at := "2026-10-26T09:00:00.000000Z"
	entry := &model.DictTermLog{PK: model.PrefixDictCommon, SK: model.DictLogSK(acctKey, at), TermKey: acctKey, Actor: "u1", Action: model.DictLogActionUpdate, At: at}
	return term, entry
}

func TestPutCommonDictTermWritesAuditInSameTransaction(t *testing.T) {
	ctx := context.Background()
	repo, calls := dictWire(t, nil)
	term, entry := commonTermAndLog()
	if _, err := repo.PutCommonDictTerm(ctx, term, 2, entry); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 {
		t.Fatalf("a live write is one transaction: %+v", *calls)
	}
	items := txItems(t, (*calls)[0])
	if len(items) != 2 {
		t.Fatalf("term put plus audit put: %+v", items)
	}
	termPut, logPut := items[0]["Put"].(map[string]any), items[1]["Put"].(map[string]any)
	requireAll(t, "term put", resolved(termPut, "ConditionExpression"), `version = {"N":"2"}`)
	requireAll(t, "audit put", resolved(logPut, "ConditionExpression"), "attribute_not_exists")
	if sk := logPut["Item"].(map[string]any)["SK"].(map[string]any)["S"]; sk != entry.SK {
		t.Fatalf("audit SK %v", sk)
	}

	// Absent term: attribute_not_exists; a tombstone cleans observations.
	repo, calls = dictWire(t, nil)
	term.State = model.TermStateDeleted
	if _, err := repo.PutCommonDictTerm(ctx, term, 0, entry); err != nil {
		t.Fatal(err)
	}
	requireAll(t, "absent put", resolved(txItems(t, (*calls)[0])[0]["Put"].(map[string]any), "ConditionExpression"), "attribute_not_exists")
	if len(*calls) != 2 || (*calls)[1].op != "Query" {
		t.Fatalf("tombstone must query observations for cleanup: %+v", *calls)
	}

	// A failed version guard is a term conflict.
	repo, _ = dictWire(t, func(int, string, map[string]any) (int, string) {
		return 400, cancelled("ConditionalCheckFailed", "None")
	})
	if _, err := repo.PutCommonDictTerm(ctx, term, 2, entry); !errors.Is(err, ErrConditionFailed) || !errors.Is(err, ErrDictTermConflict) {
		t.Fatalf("version guard: %v", err)
	}

	// Only matching Common rows are accepted, before any call.
	repo, calls = dictWire(t, nil)
	for name, mutate := range map[string]func(*model.DictTerm, *model.DictTermLog){
		"personal term":      func(tm *model.DictTerm, _ *model.DictTermLog) { tm.PK = "USER#u1" },
		"account term":       func(tm *model.DictTerm, _ *model.DictTermLog) { tm.PK = "ACCOUNT#a1" },
		"other term log":     func(_ *model.DictTerm, e *model.DictTermLog) { e.SK = model.DictLogSK("ffff", e.At) },
		"log outside common": func(_ *model.DictTerm, e *model.DictTermLog) { e.PK = "USER#u1" },
	} {
		tm, e := commonTermAndLog()
		mutate(tm, e)
		if _, err := repo.PutCommonDictTerm(ctx, tm, 2, e); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if len(*calls) != 0 {
		t.Fatalf("refused writes must not call DynamoDB: %+v", *calls)
	}
}

// The oldest row (the term's create) is pinned; the trim takes the next
// oldest so at most keep rows remain.
func TestTrimDictTermLogKeepsNewestAndPinsFirst(t *testing.T) {
	items := `{"Items":[` +
		`{"PK":{"S":"DICT#COMMON"},"SK":{"S":"DICTLOG#k#2026-10-26T09:00:01.000000Z"},"termKey":{"S":"k"}},` +
		`{"PK":{"S":"DICT#COMMON"},"SK":{"S":"DICTLOG#k#2026-10-26T09:00:03.000000Z"},"termKey":{"S":"k"}},` +
		`{"PK":{"S":"DICT#COMMON"},"SK":{"S":"DICTLOG#k#2026-10-26T09:00:02.000000Z"},"termKey":{"S":"k"}}]}`
	repo, calls := dictWire(t, func(_ int, op string, _ map[string]any) (int, string) {
		if op == "Query" {
			return 200, items
		}
		return 200, `{}`
	})
	rows, err := repo.ListDictTermLog(context.Background(), model.PrefixDictCommon, "k")
	if err != nil || len(rows) != 3 || rows[0].SK != "DICTLOG#k#2026-10-26T09:00:03.000000Z" || rows[2].SK != "DICTLOG#k#2026-10-26T09:00:01.000000Z" {
		t.Fatalf("newest first: %+v %v", rows, err)
	}
	requireAll(t, "log query", resolved((*calls)[0].body, "KeyConditionExpression"), `begins_with (SK, {"S":"DICTLOG#k#"})`)
	*calls = nil
	if err := repo.TrimDictTermLog(context.Background(), model.PrefixDictCommon, "k", 2); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 2 || (*calls)[1].op != "DeleteItem" {
		t.Fatalf("one delete beyond keep: %+v", *calls)
	}
	if sk := (*calls)[1].body["Key"].(map[string]any)["SK"].(map[string]any)["S"]; sk != "DICTLOG#k#2026-10-26T09:00:02.000000Z" {
		t.Fatalf("trimmed %v, want the oldest unpinned row", sk)
	}
}
