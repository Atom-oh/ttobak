package repository

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"testing"

	"github.com/ttobak/backend/internal/model"
)

const mergeMeetingRefSK = "MEETINGREF#2026-11-01T00:00:00Z#m1"

func mergeMeetingRef() *model.MeetingRef {
	return &model.MeetingRef{PK: "ACCOUNT#src", SK: mergeMeetingRefSK, AccountID: "src", MeetingID: "m1",
		OwnerUserID: "author", Title: "Weekly", EntityType: model.EntityTypeMeetingRef}
}

// mergeMeetingRow is a stored meeting row in wire JSON; extra holds further
// attribute entries ("name":{...}).
func mergeMeetingRow(account string, extra ...string) string {
	fields := []string{`"PK":{"S":"USER#author"}`, `"SK":{"S":"MEETING#m1"}`, `"userId":{"S":"author"}`,
		`"meetingId":{"S":"m1"}`, `"status":{"S":"done"}`, `"content":{"S":"notes"}`, `"authorizationRevision":{"N":"4"}`}
	if account != "" {
		fields = append(fields, `"accountId":{"S":"`+account+`"}`)
	}
	return `{"Item":{` + strings.Join(append(fields, extra...), ",") + `}}`
}

func mergeApproval(account string) string {
	return `"summaryAccountApproval":{"M":{"version":{"N":"1"},"ownerUserId":{"S":"author"},"accountId":{"S":"` + account +
		`"},"summaryGeneration":{"S":"g1"},"contentSha256":{"S":"abc"},"approvedAt":{"S":"2026-11-01T00:00:00Z"}}}`
}

// resolvedExpression replaces name and scalar value aliases in an expression.
func resolvedExpression(op map[string]any, field string) string {
	text, _ := op[field].(string)
	replace := map[string]string{}
	names, _ := op["ExpressionAttributeNames"].(map[string]any)
	for alias, name := range names {
		replace[alias] = name.(string)
	}
	values, _ := op["ExpressionAttributeValues"].(map[string]any)
	for alias, v := range values {
		m := v.(map[string]any)
		for _, kind := range []string{"S", "N", "BOOL"} {
			if val, ok := m[kind]; ok {
				if kind == "N" {
					replace[alias] = val.(string)
				} else {
					b, _ := json.Marshal(val)
					replace[alias] = string(b)
				}
			}
		}
	}
	// Longest alias first so #1 never rewrites part of #10.
	aliases := make([]string, 0, len(replace))
	for alias := range replace {
		aliases = append(aliases, alias)
	}
	sort.Slice(aliases, func(i, j int) bool { return len(aliases[i]) > len(aliases[j]) })
	for _, alias := range aliases {
		text = strings.ReplaceAll(text, alias, replace[alias])
	}
	return text
}

func meetingMoveRepo(t *testing.T, calls *[]fenceRequest, row string, failAt int, marker bool) *DynamoDBRepository {
	return fenceRepo(t, calls, func(call fenceRequest) (int, string) {
		switch call.target {
		case "GetItem":
			return 200, row
		case "TransactWriteItems":
			if failAt >= 0 {
				return 400, cancelledAt(len(call.body["TransactItems"].([]any)), failAt, marker)
			}
		}
		return 200, `{}`
	})
}

func TestMoveMeetingForAccountMergeTransaction(t *testing.T) {
	cases := []struct {
		name     string
		approval string
		rebind   bool
	}{
		{name: "approval bound to source is rebound", approval: mergeApproval("src"), rebind: true},
		{name: "approval bound elsewhere is unchanged", approval: mergeApproval("other")},
		{name: "no approval"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			extra := []string{`"sharedToAccount":{"BOOL":true}`}
			if tc.approval != "" {
				extra = append(extra, tc.approval)
			}
			var calls []fenceRequest
			r := meetingMoveRepo(t, &calls, mergeMeetingRow("src", extra...), -1, false)
			if err := r.MoveMeetingForAccountMerge(context.Background(), mergeRowsJob(), "author", "m1", mergeMeetingRef()); err != nil {
				t.Fatal(err)
			}
			last := calls[len(calls)-1]
			if last.target != "TransactWriteItems" {
				t.Fatalf("last call %s", last.target)
			}
			items := fenceItems(t, last)
			if len(items) > 9 || len(items) != 1+mergeFenceCheckCount+1 {
				t.Fatalf("got %d items", len(items))
			}
			if itemKey(items[0]) != "USER#author|MEETING#m1" {
				t.Fatalf("meeting key %s", itemKey(items[0]))
			}
			update := resolvedExpression(items[0], "UpdateExpression")
			for _, frag := range []string{`accountId = "tgt"`, `ADD authorizationRevision 1`} {
				if !strings.Contains(update, frag) {
					t.Errorf("update %q missing %q", update, frag)
				}
			}
			if strings.Contains(update, "sharedToAccount") {
				t.Errorf("update must never write sharedToAccount: %q", update)
			}
			if got := strings.Contains(update, `summaryAccountApproval.accountId = "tgt"`); got != tc.rebind {
				t.Errorf("approval rebind = %v, want %v (%q)", got, tc.rebind, update)
			}
			cond := resolvedExpression(items[0], "ConditionExpression")
			for _, frag := range []string{`accountId = "src"`, `sharedToAccount = true`, `authorizationRevision = 4`, `content = "notes"`} {
				if !strings.Contains(cond, frag) {
					t.Errorf("condition %q missing %q", cond, frag)
				}
			}
			if tc.approval != "" && !strings.Contains(cond, "summaryAccountApproval =") {
				t.Errorf("condition must pin the observed approval: %q", cond)
			}
			ref := items[len(items)-1]
			if itemKey(ref) != "ACCOUNT#tgt|"+mergeMeetingRefSK {
				t.Fatalf("target ref %s", itemKey(ref))
			}
			row := ref["Item"].(map[string]any)
			if attrS(t, row, "accountId") != "tgt" || attrS(t, row, "meetingId") != "m1" || attrS(t, row, "ownerUserId") != "author" {
				t.Errorf("target ref row %v", row)
			}
			for _, it := range items {
				if itemKey(it) == "ACCOUNT#src|"+mergeMeetingRefSK {
					t.Errorf("source ref must not be deleted before shares and insights finish")
				}
			}
		})
	}
}

// Review Focus 4: an unsafe summary on a link-only meeting moves without the
// sharing-safe gate and stays unpublished.
func TestMoveMeetingKeepsLinkOnlyUnpublished(t *testing.T) {
	private := `"summaryPrivacy":{"M":{"version":{"N":"1"},"state":{"S":"private"},"generationId":{"S":"g1"},"contentSha256":{"S":"abc"}}}`
	for name, extra := range map[string][]string{
		"absent flag":    {private},
		"explicit false": {private, `"sharedToAccount":{"BOOL":false}`},
	} {
		t.Run(name, func(t *testing.T) {
			var calls []fenceRequest
			r := meetingMoveRepo(t, &calls, mergeMeetingRow("src", extra...), -1, false)
			if err := r.MoveMeetingForAccountMerge(context.Background(), mergeRowsJob(), "author", "m1", mergeMeetingRef()); err != nil {
				t.Fatalf("unsafe link-only meeting must move: %v", err)
			}
			items := fenceItems(t, calls[len(calls)-1])
			update := resolvedExpression(items[0], "UpdateExpression")
			if strings.Contains(update, "sharedToAccount") || strings.Contains(update, "summaryAccountApproval") {
				t.Fatalf("link-only move must not publish or approve: %q", update)
			}
			cond := resolvedExpression(items[0], "ConditionExpression")
			want := "attribute_not_exists (sharedToAccount)"
			if len(extra) == 2 {
				want = "sharedToAccount = false"
			}
			if !strings.Contains(cond, want) {
				t.Errorf("condition %q must pin the observed unpublished state %q", cond, want)
			}
		})
	}
}

func TestMoveMeetingForAccountMergeOutcomes(t *testing.T) {
	published := `"sharedToAccount":{"BOOL":true}`
	cases := []struct {
		name   string
		row    string
		failAt int
		marker bool
		want   error
		writes bool
	}{
		{name: "meeting now on another account", row: mergeMeetingRow("other", published), failAt: -1, want: ErrConditionFailed},
		{name: "meeting gone", row: `{}`, failAt: -1, want: ErrConditionFailed},
		{name: "snapshot changed", row: mergeMeetingRow("src", published), failAt: 0, want: ErrConditionFailed, writes: true},
		{name: "lease lost", row: mergeMeetingRow("src", published), failAt: 1, want: ErrMergeFenceLost, writes: true},
		{name: "requester lost ownership", row: mergeMeetingRow("src", published), failAt: 3, want: ErrMergeFenceLost, writes: true},
		{name: "target started merging", row: mergeMeetingRow("src", published), failAt: 5, marker: true, want: ErrAccountMerging, writes: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls []fenceRequest
			r := meetingMoveRepo(t, &calls, tc.row, tc.failAt, tc.marker)
			err := r.MoveMeetingForAccountMerge(context.Background(), mergeRowsJob(), "author", "m1", mergeMeetingRef())
			if !errors.Is(err, tc.want) {
				t.Fatalf("err=%v, want %v", err, tc.want)
			}
			wrote := false
			for _, c := range calls {
				wrote = wrote || c.target == "TransactWriteItems"
			}
			if wrote != tc.writes {
				t.Errorf("transaction sent = %v, want %v", wrote, tc.writes)
			}
		})
	}
	t.Run("invalid ref", func(t *testing.T) {
		var calls []fenceRequest
		r := meetingMoveRepo(t, &calls, mergeMeetingRow("src"), -1, false)
		for _, ref := range []*model.MeetingRef{nil, {PK: "ACCOUNT#tgt", SK: mergeMeetingRefSK, MeetingID: "m1"},
			{PK: "ACCOUNT#src", SK: "MEETINGREF#x#m2", MeetingID: "m1"}} {
			if err := r.MoveMeetingForAccountMerge(context.Background(), mergeRowsJob(), "author", "m1", ref); err == nil {
				t.Errorf("ref %+v must be rejected", ref)
			}
		}
		if len(calls) != 0 {
			t.Errorf("invalid input reached DynamoDB %d times", len(calls))
		}
	})
}

func TestDeleteMeetingRefForMerge(t *testing.T) {
	var calls []fenceRequest
	r := fenceRepo(t, &calls, func(fenceRequest) (int, string) { return 200, `{}` })
	if err := r.DeleteMeetingRefForMerge(context.Background(), mergeRowsJob(), mergeMeetingRef()); err != nil {
		t.Fatal(err)
	}
	items := fenceItems(t, calls[0])
	if len(items) != 1+mergeFenceCheckCount || itemKey(items[0]) != "ACCOUNT#src|"+mergeMeetingRefSK {
		t.Fatalf("items %d, first %s", len(items), itemKey(items[0]))
	}
	lost := fenceRepo(t, new([]fenceRequest), func(fenceRequest) (int, string) { return 400, cancelledAt(1+mergeFenceCheckCount, 2, false) })
	if err := lost.DeleteMeetingRefForMerge(context.Background(), mergeRowsJob(), mergeMeetingRef()); !errors.Is(err, ErrMergeFenceLost) {
		t.Fatalf("err=%v, want ErrMergeFenceLost", err)
	}
}

func TestRetagAccountShareForMerge(t *testing.T) {
	var calls []fenceRequest
	r := fenceRepo(t, &calls, func(fenceRequest) (int, string) { return 200, `{}` })
	if err := r.RetagAccountShareForMerge(context.Background(), mergeRowsJob(), "m1", "u1"); err != nil {
		t.Fatal(err)
	}
	items := fenceItems(t, calls[0])
	if len(items) != 2+mergeFenceCheckCount {
		t.Fatalf("got %d items", len(items))
	}
	for _, op := range []map[string]any{items[0], items[len(items)-1]} {
		if c := resolvedExpression(op, "ConditionExpression"); !strings.Contains(c, `origin = "account"`) || !strings.Contains(c, `accountId = "src"`) {
			t.Errorf("share condition %q", c)
		}
		if u := resolvedExpression(op, "UpdateExpression"); !strings.Contains(u, `accountId = "tgt"`) {
			t.Errorf("share update %q", u)
		}
	}
	if itemKey(items[0]) != "USER#u1|SHARED#m1" || itemKey(items[len(items)-1]) != "MEETING#m1|SHARE_TO#u1" {
		t.Errorf("share keys %s %s", itemKey(items[0]), itemKey(items[len(items)-1]))
	}
	for name, tc := range map[string]struct {
		idx  int
		want error
	}{
		"direct share untouched":  {idx: 0},
		"meeting row not account": {idx: 1 + mergeFenceCheckCount},
		"fence lost":              {idx: 4, want: ErrMergeFenceLost},
	} {
		t.Run(name, func(t *testing.T) {
			r := fenceRepo(t, new([]fenceRequest), func(fenceRequest) (int, string) { return 400, cancelledAt(2+mergeFenceCheckCount, tc.idx, false) })
			err := r.RetagAccountShareForMerge(context.Background(), mergeRowsJob(), "m1", "u1")
			if tc.want == nil && err != nil || tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("err=%v, want %v", err, tc.want)
			}
		})
	}
}
