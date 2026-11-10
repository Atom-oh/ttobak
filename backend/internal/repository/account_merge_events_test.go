package repository

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/ttobak/backend/internal/model"
)

const mergeEventSK = "EVENT#2026-11-20T01:00:00Z#e1"

func mergeFromEvent() model.AccountEvent {
	return model.AccountEvent{EventID: "e1", Revision: "rev-a", Title: "Kickoff", StartAt: "2026-11-20T01:00:00Z",
		Attendees: []model.EventAttendee{
			{ContactID: "c1", Name: "Alice", Email: "alice@example.com"},
			{ContactID: "c3", Name: "Cy", Email: "cy@example.com"},
			{Name: "Dan"},
		}}
}

func TestMoveEventForMergeTransaction(t *testing.T) {
	var calls []fenceRequest
	r, sealer := sealedMergeRepo(t, &calls, func(fenceRequest) (int, string) { return 200, `{}` })
	if err := r.MoveEventForMerge(context.Background(), mergeRowsJob(), mergeEventSK, mergeFromEvent(), map[string]string{"c1": "b9"}); err != nil {
		t.Fatal(err)
	}
	items := fenceItems(t, calls[0])
	if len(items) != 1+mergeFenceCheckCount+3 {
		t.Fatalf("got %d items", len(items))
	}
	put := items[0]["Item"].(map[string]any)
	if itemKey(items[0]) != "ACCOUNT#tgt|"+mergeEventSK || !strings.Contains(resolvedCondition(items[0]), "attribute_not_exists (PK)") {
		t.Fatalf("target event put %s %q", itemKey(items[0]), resolvedCondition(items[0]))
	}
	if rev := attrS(t, put, "revision"); rev == "" || rev == "rev-a" {
		t.Errorf("target revision %q must be new", rev)
	}
	if attrS(t, put, "startAt") != "2026-11-20T01:00:00Z" || attrS(t, put, "eventId") != "e1" {
		t.Errorf("target plaintext fields %v", put)
	}
	sealed := attrS(t, put, "sealed")
	plain, err := testSealer{}.Open(context.Background(), sealed, eventBinding("tgt", "e1"))
	if err != nil {
		t.Fatalf("target event must open under the target binding: %v", err)
	}
	if _, err := (testSealer{}).Open(context.Background(), sealed, eventBinding("src", "e1")); err == nil {
		t.Error("target event must not open under the source binding")
	}
	var got model.AccountEvent
	if err := json.Unmarshal(plain, &got); err != nil || got.Title != "Kickoff" {
		t.Fatalf("sealed event %+v %v", got, err)
	}
	want := []model.EventAttendee{{ContactID: "b9", Name: "Alice", Email: "alice@example.com"}, {Name: "Cy", Email: "cy@example.com"}, {Name: "Dan"}}
	if len(got.Attendees) != len(want) {
		t.Fatalf("attendees %+v", got.Attendees)
	}
	for i := range want {
		if got.Attendees[i] != want[i] {
			t.Errorf("attendee %d = %+v, want %+v", i, got.Attendees[i], want[i])
		}
	}
	if len(sealer.seals) != 1 || sealer.seals[0]["purpose"] != "ttobak-account-event" || sealer.seals[0]["accountId"] != "tgt" || sealer.seals[0]["eventId"] != "e1" {
		t.Errorf("seal bindings %v", sealer.seals)
	}
	ref := items[6]["Item"].(map[string]any)
	if itemKey(items[6]) != "ACCOUNT#tgt|EVENTREF#e1" || attrS(t, ref, "eventSK") != mergeEventSK || attrS(t, ref, "eventId") != "e1" ||
		!strings.Contains(resolvedCondition(items[6]), "attribute_not_exists (PK)") {
		t.Errorf("target ref %s %v", itemKey(items[6]), ref)
	}
	if itemKey(items[7]) != "ACCOUNT#src|"+mergeEventSK || !strings.Contains(resolvedCondition(items[7]), "revision = ") {
		t.Errorf("source event delete %s %q", itemKey(items[7]), resolvedCondition(items[7]))
	}
	if itemKey(items[8]) != "ACCOUNT#src|EVENTREF#e1" || !strings.Contains(resolvedCondition(items[8]), "eventSK = ") ||
		!strings.Contains(resolvedCondition(items[8]), "attribute_not_exists (PK)") {
		t.Errorf("source ref delete %s %q", itemKey(items[8]), resolvedCondition(items[8]))
	}
	if from := mergeFromEvent(); from.Attendees[0].ContactID != "c1" {
		t.Error("caller's attendees must not be modified")
	}
}

func TestEventMergeOutcomes(t *testing.T) {
	n := 1 + mergeFenceCheckCount + 3
	for name, tc := range map[string]struct {
		idx    int
		marker bool
		want   error
	}{
		"target event exists":    {idx: 0, want: ErrConditionFailed},
		"lease lost":             {idx: 1, want: ErrMergeFenceLost},
		"requester lost owner":   {idx: 3, want: ErrMergeFenceLost},
		"target started merging": {idx: 5, marker: true, want: ErrAccountMerging},
		"target ref exists":      {idx: 6, want: ErrConditionFailed},
		"source event changed":   {idx: 7, want: ErrConditionFailed},
		"source ref elsewhere":   {idx: 8, want: ErrConditionFailed},
	} {
		t.Run(name, func(t *testing.T) {
			var calls []fenceRequest
			r, _ := sealedMergeRepo(t, &calls, func(fenceRequest) (int, string) { return 400, cancelledAt(n, tc.idx, tc.marker) })
			if err := r.MoveEventForMerge(context.Background(), mergeRowsJob(), mergeEventSK, mergeFromEvent(), nil); !errors.Is(err, tc.want) {
				t.Errorf("got %v, want %v", err, tc.want)
			}
		})
	}
}

func TestEventMergeFailsClosedAndRejectsInvalidInput(t *testing.T) {
	var calls []fenceRequest
	keyless := fenceRepo(t, &calls, func(fenceRequest) (int, string) { return 200, `{}` })
	if err := keyless.MoveEventForMerge(context.Background(), mergeRowsJob(), mergeEventSK, mergeFromEvent(), nil); !errors.Is(err, ErrEventEncryptionUnavailable) {
		t.Errorf("move without a key: %v", err)
	}
	if _, _, _, err := keyless.ListAccountEventsForMerge(context.Background(), "src", 10, ""); !errors.Is(err, ErrEventEncryptionUnavailable) {
		t.Errorf("list without a key: %v", err)
	}
	r, _ := sealedMergeRepo(t, &calls, func(fenceRequest) (int, string) { return 200, `{}` })
	same := mergeRowsJob()
	same.TargetAccountID = "src"
	noRev := mergeFromEvent()
	noRev.Revision = ""
	for name, call := range map[string]func() error{
		"no revision": func() error {
			return r.MoveEventForMerge(context.Background(), mergeRowsJob(), mergeEventSK, noRev, nil)
		},
		"sk of other": func() error {
			return r.MoveEventForMerge(context.Background(), mergeRowsJob(), "EVENT#2026-11-20T01:00:00Z#e2", mergeFromEvent(), nil)
		},
		"not an event": func() error {
			return r.MoveEventForMerge(context.Background(), mergeRowsJob(), "CONTACT#e1", mergeFromEvent(), nil)
		},
		"same account": func() error {
			return r.MoveEventForMerge(context.Background(), same, mergeEventSK, mergeFromEvent(), nil)
		},
		"bad map": func() error {
			return r.MoveEventForMerge(context.Background(), mergeRowsJob(), mergeEventSK, mergeFromEvent(), map[string]string{"c1": "a#b"})
		},
		"no lease": func() error {
			j := mergeRowsJob()
			j.LeaseOwner = ""
			return r.MoveEventForMerge(context.Background(), j, mergeEventSK, mergeFromEvent(), nil)
		},
		"bad cursor": func() error {
			_, _, _, err := r.ListAccountEventsForMerge(context.Background(), "src", 10, "CONTACT#x")
			return err
		},
	} {
		if err := call(); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
	if len(calls) != 0 {
		t.Errorf("rejected input reached DynamoDB: %d calls", len(calls))
	}
}

func TestListAccountEventsForMergeReportsUnreadable(t *testing.T) {
	good, _ := testSealer{}.Seal(context.Background(), []byte(`{"eventId":"e1","title":"Kickoff","attendees":[{"contactId":"c1","name":"Alice"}]}`), eventBinding("src", "e1"))
	foreign, _ := testSealer{}.Seal(context.Background(), []byte(`{"eventId":"e2","title":"Other"}`), eventBinding("other", "e2"))
	var calls []fenceRequest
	r, _ := sealedMergeRepo(t, &calls, func(fenceRequest) (int, string) {
		return 200, `{"Items":[` +
			`{"PK":{"S":"ACCOUNT#src"},"SK":{"S":"EVENT#2026-01-01T00:00:00Z#e1"},"eventId":{"S":"e1"},"revision":{"S":"r1"},"startAt":{"S":"2026-01-01T00:00:00Z"},"sealed":{"S":"` + good + `"}},` +
			`{"PK":{"S":"ACCOUNT#src"},"SK":{"S":"EVENT#2026-01-02T00:00:00Z#e2"},"eventId":{"S":"e2"},"revision":{"S":"r2"},"sealed":{"S":"` + foreign + `"}},` +
			`{"PK":{"S":"ACCOUNT#src"},"SK":{"S":"EVENT#2026-01-03T00:00:00Z#e3"},"eventId":{"S":"e3"},"title":{"S":"plain"}}` +
			`],"LastEvaluatedKey":{"PK":{"S":"ACCOUNT#src"},"SK":{"S":"EVENT#2026-01-03T00:00:00Z#e3"}}}`
	})
	rows, unreadable, next, err := r.ListAccountEventsForMerge(context.Background(), "src", 3, "EVENT#2025")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].SK != "EVENT#2026-01-01T00:00:00Z#e1" || rows[0].Event.Revision != "r1" || rows[0].Event.Title != "Kickoff" ||
		len(rows[0].Event.Attendees) != 1 || rows[0].Event.Attendees[0].ContactID != "c1" {
		t.Errorf("rows %+v", rows)
	}
	if strings.Join(unreadable, ",") != "EVENT#2026-01-02T00:00:00Z#e2,EVENT#2026-01-03T00:00:00Z#e3" || next != "EVENT#2026-01-03T00:00:00Z#e3" {
		t.Errorf("unreadable %v next %q", unreadable, next)
	}
	body := calls[0].body
	if body["ConsistentRead"] != true {
		t.Error("merge listing must read strongly consistently")
	}
	if !strings.Contains(body["KeyConditionExpression"].(string), "begins_with") {
		t.Errorf("key condition %v", body["KeyConditionExpression"])
	}
	start, _ := body["ExclusiveStartKey"].(map[string]any)
	if sk, _ := start["SK"].(map[string]any); sk["S"] != "EVENT#2025" {
		t.Errorf("start key %v", start)
	}
}
