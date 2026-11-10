package repository

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/ttobak/backend/internal/model"
)

// recordingSealer is testSealer that also records every binding it seals.
type recordingSealer struct {
	testSealer
	mu    sync.Mutex
	seals []map[string]string
}

func (s *recordingSealer) Seal(ctx context.Context, p []byte, b map[string]string) (string, error) {
	s.mu.Lock()
	s.seals = append(s.seals, b)
	s.mu.Unlock()
	return s.testSealer.Seal(ctx, p, b)
}

func sealedMergeRepo(t *testing.T, calls *[]fenceRequest, respond func(fenceRequest) (int, string)) (*DynamoDBRepository, *recordingSealer) {
	t.Helper()
	r := fenceRepo(t, calls, respond)
	s := &recordingSealer{}
	r.SetRecordSealer(s)
	return r, s
}

func mergeFromContact() model.AccountContact {
	return model.AccountContact{ContactID: "c1", Revision: "rev-a", Name: "Alice", Email: "alice@example.com"}
}

func attrS(t *testing.T, op map[string]any, field string) string {
	t.Helper()
	m, _ := op[field].(map[string]any)
	s, _ := m["S"].(string)
	return s
}

func TestMoveContactForMergeTransaction(t *testing.T) {
	var calls []fenceRequest
	r, _ := sealedMergeRepo(t, &calls, func(fenceRequest) (int, string) { return 200, `{}` })
	if err := r.MoveContactForMerge(context.Background(), mergeRowsJob(), mergeFromContact()); err != nil {
		t.Fatal(err)
	}
	items := fenceItems(t, calls[0])
	if len(items) != 1+mergeFenceCheckCount+2 {
		t.Fatalf("got %d items", len(items))
	}
	put := items[0]["Item"].(map[string]any)
	if itemKey(items[0]) != "ACCOUNT#tgt|CONTACT#c1" || !strings.Contains(resolvedCondition(items[0]), "attribute_not_exists (PK)") {
		t.Fatalf("target put %s %q", itemKey(items[0]), resolvedCondition(items[0]))
	}
	if rev := attrS(t, put, "revision"); rev == "" || rev == "rev-a" {
		t.Errorf("target revision %q must be new", rev)
	}
	sealed := attrS(t, put, "sealed")
	plain, err := testSealer{}.Open(context.Background(), sealed, contactBinding("tgt", "c1"))
	if err != nil {
		t.Fatalf("target row must open under the target binding: %v", err)
	}
	var got model.AccountContact
	if err := json.Unmarshal(plain, &got); err != nil || got.Name != "Alice" || got.Email != "alice@example.com" {
		t.Fatalf("sealed fields %+v %v", got, err)
	}
	if _, err := (testSealer{}).Open(context.Background(), sealed, contactBinding("src", "c1")); err == nil {
		t.Error("target row must not open under the source binding")
	}
	if itemKey(items[6]) != "ACCOUNT#src|CONTACT#c1" || items[6]["ConditionExpression"] == nil {
		t.Fatalf("source delete %s", itemKey(items[6]))
	}
	if c := resolvedCondition(items[6]); !strings.Contains(c, "revision = ") {
		t.Errorf("source delete must pin the read revision: %q", c)
	}
	mapRow := items[7]["Item"].(map[string]any)
	if itemKey(items[7]) != "ACCOUNT#src|MERGE#contact#c1" || attrS(t, mapRow, "targetContactId") != "c1" ||
		attrS(t, mapRow, "sourceContactId") != "c1" || !strings.Contains(resolvedCondition(items[7]), "attribute_not_exists (PK)") {
		t.Errorf("map row %v", mapRow)
	}
	if strings.Contains(resolvedCondition(items[2]), "attribute_not_exists (mergingInto)") {
		t.Error("source META carries the open fence")
	}
}

func TestMergeContactIntoExistingTransaction(t *testing.T) {
	var calls []fenceRequest
	r, _ := sealedMergeRepo(t, &calls, func(fenceRequest) (int, string) { return 200, `{}` })
	into := model.AccountContact{ContactID: "b9", Revision: "rev-b", Name: "Alice Kim", Email: "alice@example.com"}
	if err := r.MergeContactIntoExisting(context.Background(), mergeRowsJob(), mergeFromContact(), into); err != nil {
		t.Fatal(err)
	}
	items := fenceItems(t, calls[0])
	if len(items) != 1+mergeFenceCheckCount+2 {
		t.Fatalf("got %d items", len(items))
	}
	if itemKey(items[0]) != "ACCOUNT#tgt|CONTACT#b9" || items[0]["UpdateExpression"] == nil {
		t.Fatalf("target update %s", itemKey(items[0]))
	}
	c := resolvedCondition(items[0])
	values, _ := items[0]["ExpressionAttributeValues"].(map[string]any)
	pinned := false
	for _, v := range values {
		if v.(map[string]any)["S"] == "rev-b" {
			pinned = true
		}
	}
	if !strings.Contains(c, "attribute_exists (PK)") || !strings.Contains(c, "revision = ") || !pinned {
		t.Errorf("target update must pin the observed revision: %q %v", c, values)
	}
	if itemKey(items[6]) != "ACCOUNT#src|CONTACT#c1" {
		t.Errorf("source delete %s", itemKey(items[6]))
	}
	if row := items[7]["Item"].(map[string]any); attrS(t, row, "targetContactId") != "b9" || attrS(t, row, "sourceContactId") != "c1" {
		t.Errorf("map row %v", row)
	}
}

func TestMoveContactsBindingIsB(t *testing.T) {
	var calls []fenceRequest
	r, sealer := sealedMergeRepo(t, &calls, func(fenceRequest) (int, string) { return 200, `{}` })
	if err := r.MoveContactForMerge(context.Background(), mergeRowsJob(), mergeFromContact()); err != nil {
		t.Fatal(err)
	}
	into := model.AccountContact{ContactID: "b9", Revision: "rev-b"}
	if err := r.MergeContactIntoExisting(context.Background(), mergeRowsJob(), mergeFromContact(), into); err != nil {
		t.Fatal(err)
	}
	want := []string{"tgt/c1", "tgt/b9"}
	if len(sealer.seals) != len(want) {
		t.Fatalf("seals %v", sealer.seals)
	}
	for i, b := range sealer.seals {
		if b["purpose"] != "ttobak-account-contact" || b["accountId"]+"/"+b["contactId"] != want[i] {
			t.Errorf("seal %d binding %v, want accountId/contactId %s", i, b, want[i])
		}
	}
}

func TestContactMergeOutcomes(t *testing.T) {
	n := 1 + mergeFenceCheckCount + 2
	into := model.AccountContact{ContactID: "b9", Revision: "rev-b"}
	for name, tc := range map[string]struct {
		idx    int
		marker bool
		want   error
	}{
		"target conflict":        {idx: 0, want: ErrConditionFailed},
		"lease lost":             {idx: 1, want: ErrMergeFenceLost},
		"requester lost owner":   {idx: 3, want: ErrMergeFenceLost},
		"target started merging": {idx: 5, marker: true, want: ErrAccountMerging},
		"source row changed":     {idx: 6, want: ErrConditionFailed},
		"mapping exists":         {idx: 7, want: ErrConditionFailed},
	} {
		t.Run(name, func(t *testing.T) {
			var calls []fenceRequest
			r, _ := sealedMergeRepo(t, &calls, func(fenceRequest) (int, string) { return 400, cancelledAt(n, tc.idx, tc.marker) })
			if err := r.MoveContactForMerge(context.Background(), mergeRowsJob(), mergeFromContact()); !errors.Is(err, tc.want) {
				t.Errorf("move: got %v, want %v", err, tc.want)
			}
			if err := r.MergeContactIntoExisting(context.Background(), mergeRowsJob(), mergeFromContact(), into); !errors.Is(err, tc.want) {
				t.Errorf("merge: got %v, want %v", err, tc.want)
			}
		})
	}
}

func TestContactMergeFailsClosedAndRejectsInvalidInput(t *testing.T) {
	var calls []fenceRequest
	keyless := fenceRepo(t, &calls, func(fenceRequest) (int, string) { return 200, `{}` })
	if err := keyless.MoveContactForMerge(context.Background(), mergeRowsJob(), mergeFromContact()); !errors.Is(err, ErrContactEncryptionUnavailable) {
		t.Errorf("move without a key: %v", err)
	}
	if err := keyless.MergeContactIntoExisting(context.Background(), mergeRowsJob(), mergeFromContact(), model.AccountContact{ContactID: "b9", Revision: "r"}); !errors.Is(err, ErrContactEncryptionUnavailable) {
		t.Errorf("merge without a key: %v", err)
	}
	if _, _, _, err := keyless.ListAccountContactsForMerge(context.Background(), "src", 10, ""); !errors.Is(err, ErrContactEncryptionUnavailable) {
		t.Errorf("list without a key: %v", err)
	}
	r, _ := sealedMergeRepo(t, &calls, func(fenceRequest) (int, string) { return 200, `{}` })
	same := mergeRowsJob()
	same.TargetAccountID = "src"
	for name, call := range map[string]func() error{
		"no revision": func() error {
			return r.MoveContactForMerge(context.Background(), mergeRowsJob(), model.AccountContact{ContactID: "c1"})
		},
		"key in id": func() error {
			return r.MoveContactForMerge(context.Background(), mergeRowsJob(), model.AccountContact{ContactID: "c#1", Revision: "r"})
		},
		"same account": func() error { return r.MoveContactForMerge(context.Background(), same, mergeFromContact()) },
		"no lease": func() error {
			j := mergeRowsJob()
			j.LeaseOwner = ""
			return r.MoveContactForMerge(context.Background(), j, mergeFromContact())
		},
		"target no rev": func() error {
			return r.MergeContactIntoExisting(context.Background(), mergeRowsJob(), mergeFromContact(), model.AccountContact{ContactID: "b9"})
		},
		"map key in id": func() error { _, err := r.GetMergeContactMap(context.Background(), "src", []string{"a#b"}); return err },
	} {
		if err := call(); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
	if len(calls) != 0 {
		t.Errorf("rejected input reached DynamoDB: %d calls", len(calls))
	}
}

func TestListAccountContactsForMergeReportsUnreadable(t *testing.T) {
	good, _ := testSealer{}.Seal(context.Background(), []byte(`{"contactId":"c1","name":"Alice"}`), contactBinding("src", "c1"))
	foreign, _ := testSealer{}.Seal(context.Background(), []byte(`{"contactId":"c2","name":"Bob"}`), contactBinding("other", "c2"))
	var calls []fenceRequest
	r, _ := sealedMergeRepo(t, &calls, func(fenceRequest) (int, string) {
		return 200, `{"Items":[` +
			`{"PK":{"S":"ACCOUNT#src"},"SK":{"S":"CONTACT#c1"},"contactId":{"S":"c1"},"revision":{"S":"r1"},"sealed":{"S":"` + good + `"}},` +
			`{"PK":{"S":"ACCOUNT#src"},"SK":{"S":"CONTACT#c2"},"contactId":{"S":"c2"},"revision":{"S":"r2"},"sealed":{"S":"` + foreign + `"}},` +
			`{"PK":{"S":"ACCOUNT#src"},"SK":{"S":"CONTACT#c3"},"contactId":{"S":"c3"},"name":{"S":"plain"}}` +
			`],"LastEvaluatedKey":{"PK":{"S":"ACCOUNT#src"},"SK":{"S":"CONTACT#c3"}}}`
	})
	contacts, unreadable, next, err := r.ListAccountContactsForMerge(context.Background(), "src", 3, "c0")
	if err != nil {
		t.Fatal(err)
	}
	if len(contacts) != 1 || contacts[0].ContactID != "c1" || contacts[0].Revision != "r1" || contacts[0].Name != "Alice" {
		t.Errorf("contacts %+v", contacts)
	}
	if strings.Join(unreadable, ",") != "c2,c3" || next != "c3" {
		t.Errorf("unreadable %v next %q", unreadable, next)
	}
	if calls[0].body["ConsistentRead"] != true {
		t.Error("merge listing must read strongly consistently")
	}
	start, _ := calls[0].body["ExclusiveStartKey"].(map[string]any)
	if sk, _ := start["SK"].(map[string]any); sk["S"] != "CONTACT#c0" {
		t.Errorf("start key %v", start)
	}
}

func TestGetMergeContactMap(t *testing.T) {
	var calls []fenceRequest
	r := fenceRepo(t, &calls, func(fenceRequest) (int, string) {
		return 200, `{"Responses":{"` + fenceTable + `":[` +
			`{"PK":{"S":"ACCOUNT#src"},"SK":{"S":"MERGE#contact#c1"},"sourceContactId":{"S":"c1"},"targetContactId":{"S":"b9"}}` +
			`]}}`
	})
	got, err := r.GetMergeContactMap(context.Background(), "src", []string{"c1", "c2"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got["c1"] != "b9" {
		t.Errorf("map %v", got)
	}
	none, err := r.GetMergeContactMap(context.Background(), "src", nil)
	if err != nil || len(none) != 0 || len(calls) != 1 {
		t.Errorf("empty selection: %v %v calls=%d", none, err, len(calls))
	}
}
