package repository

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/ttobak/backend/internal/model"
)

// sweepTermJSON renders an ACCOUNT#a1 term row; refs are "kind:ref" sources.
func sweepTermJSON(key, origin, state string, version int, first string, refs ...string) string {
	var sources []string
	for _, r := range refs {
		kind, ref, _ := strings.Cut(r, ":")
		sources = append(sources, `{"M":{"kind":{"S":"`+kind+`"},"ref":{"S":"`+ref+`"},"at":{"S":"2026-11-01T00:00:00Z"}}}`)
	}
	extra := ""
	if len(sources) > 0 {
		extra += `,"sources":{"L":[` + strings.Join(sources, ",") + `]}`
	}
	if first != "" {
		extra += `,"firstSourceMeetingId":{"S":"` + first + `"}`
	}
	return `{"Item":{"PK":{"S":"ACCOUNT#a1"},"SK":{"S":"DICTTERM#` + key + `"},"termKey":{"S":"` + key + `"},"origin":{"S":"` + origin +
		`"},"state":{"S":"` + state + `"},"version":{"N":"` + strconv.Itoa(version) + `"}` + extra + `}}`
}

func sweepStateJSON(owner string, accountIDs []string, records int, accepted []string, accountKeys []string) string {
	var list []string
	for _, k := range accepted {
		list = append(list, `{"M":{"termKey":{"S":"`+k+`"}}}`)
	}
	quote := func(values []string) string {
		out := make([]string, len(values))
		for i, v := range values {
			out[i] = `"` + v + `"`
		}
		return strings.Join(out, ",")
	}
	item := `"PK":{"S":"MEETING#m1"},"SK":{"S":"ANALYSIS#terms"},"ownerId":{"S":"` + owner + `"},"runId":{"S":"r"},"status":{"S":"succeeded"}`
	if len(accountIDs) > 0 {
		item += `,"accountIds":{"SS":[` + quote(accountIDs) + `]}`
	}
	if len(accountKeys) > 0 {
		item += `,"accountAcceptedTerms":{"SS":[` + quote(accountKeys) + `]}`
	}
	if records > 0 {
		item += `,"accountRecords":{"N":"` + strconv.Itoa(records) + `"}`
	}
	if len(list) > 0 {
		item += `,"acceptedTerms":{"L":[` + strings.Join(list, ",") + `]}`
	}
	return `{"Item":{` + item + `}}`
}

const sweepMeetingLinkedA2 = `{"Item":{"PK":{"S":"USER#owner"},"accountId":{"S":"a2"}}}`

func callText(c dictCall) string {
	encoded, _ := json.Marshal(c.body)
	return string(encoded)
}

func callKey(c dictCall) (string, string) {
	key, _ := c.body["Key"].(map[string]any)
	if key == nil {
		return "", ""
	}
	return key["PK"].(map[string]any)["S"].(string), key["SK"].(map[string]any)["S"].(string)
}

func isWrite(op string) bool {
	return op == "DeleteItem" || op == "UpdateItem" || op == "PutItem" || op == "TransactWriteItems"
}

// A relinked meeting (recorded a1 and a2, now linked to a2) loses its
// evidence in a1 only: observations go, an orphaned auto term goes, its
// source entry and first-meeting marker leave a term that stays (auto or
// manual), tombstones and terms without its entry are untouched, and a1 then
// leaves accountIds under the record-count and relink guards.
func TestSweepMeetingAccountTermsCleansOnlyTheAccountsLeft(t *testing.T) {
	k1, k2, k3, k4, k5 := strings.Repeat("1", 32), strings.Repeat("2", 32), strings.Repeat("3", 32), strings.Repeat("4", 32), strings.Repeat("5", 32)
	state := sweepStateJSON("owner", []string{"a1", "a2"}, 2, []string{k1}, []string{k2, k3, k4, k5})
	dict, calls := dictWire(t, func(_ int, op string, body map[string]any) (int, string) {
		text := callText(dictCall{body: body})
		switch {
		case op == "GetItem" && strings.Contains(text, "ANALYSIS#terms"):
			return 200, state
		case op == "GetItem" && strings.Contains(text, "MEETING#m1"):
			return 200, sweepMeetingLinkedA2
		case op == "Query" && strings.Contains(text, k1):
			return 200, `{"Count":0}`
		case op == "Query":
			return 200, `{"Count":1}`
		case op == "GetItem" && strings.Contains(text, k1):
			return 200, sweepTermJSON(k1, model.TermOriginAuto, model.TermStateActive, 3, "m1", "meeting:m1")
		case op == "GetItem" && strings.Contains(text, k2):
			return 200, sweepTermJSON(k2, model.TermOriginAuto, model.TermStateActive, 5, "m1", "meeting:m1", "meeting:m9")
		case op == "GetItem" && strings.Contains(text, k3):
			return 200, sweepTermJSON(k3, model.TermOriginManual, model.TermStateActive, 2, "", "meeting:m1")
		case op == "GetItem" && strings.Contains(text, k4):
			return 200, sweepTermJSON(k4, model.TermOriginAuto, model.TermStateDeleted, 8, "")
		case op == "GetItem" && strings.Contains(text, k5):
			return 200, sweepTermJSON(k5, model.TermOriginAuto, model.TermStateActive, 1, "m9", "kb:doc")
		}
		return 200, `{}`
	})
	got, err := NewDynamoDBRepository(dict.client, "dict-test").SweepMeetingAccountTerms(context.Background(), "owner", "m1")
	if err != nil || got != (TermsAccountSweep{Stale: 1, Dropped: 1}) {
		t.Fatalf("sweep: %+v err=%v", got, err)
	}
	var deleted []string
	updates := map[string]string{}
	var drop *dictCall
	for i, c := range *calls {
		if strings.Contains(callText(c), "ACCOUNT#a2") {
			t.Fatal("the current Account must not be touched")
		}
		if !isWrite(c.op) {
			continue
		}
		pk, sk := callKey(c)
		switch c.op {
		case "DeleteItem":
			if pk != "ACCOUNT#a1" {
				t.Fatalf("only the left Account is cleaned: %s", pk)
			}
			deleted = append(deleted, sk)
			if sk == model.DictTermSK(k1) {
				requireAll(t, "orphan delete", resolved(c.body, "ConditionExpression"), `version = {"N":"3"}`, `origin = {"S":"auto"}`, "attribute_not_exists (userEditedAt)")
			}
		case "UpdateItem":
			updates[sk] = resolved(c.body, "UpdateExpression") + " IF " + resolved(c.body, "ConditionExpression")
		case "TransactWriteItems":
			drop = &(*calls)[i]
		}
	}
	want := []string{model.DictObsSK(k1, "m1"), model.DictTermSK(k1), model.DictObsSK(k2, "m1"), model.DictObsSK(k3, "m1"), model.DictObsSK(k4, "m1"), model.DictObsSK(k5, "m1")}
	if strings.Join(deleted, ",") != strings.Join(want, ",") {
		t.Fatalf("deletes:\n got %v\nwant %v", deleted, want)
	}
	if len(updates) != 2 {
		t.Fatalf("only k2 and k3 name the meeting: %v", updates)
	}
	u2 := updates[model.DictTermSK(k2)]
	requireAll(t, "k2", u2, `"m9"`, "REMOVE", "firstSourceMeetingId", `version = {"N":"5"}`, "version + {\"N\":\"1\"}")
	if strings.Contains(u2, `"m1"`) {
		t.Fatalf("the meeting's source entry must leave the term: %s", u2)
	}
	requireAll(t, "k3", updates[model.DictTermSK(k3)], "REMOVE sources", `version = {"N":"2"}`)
	if drop == nil || drop != &(*calls)[len(*calls)-1] {
		t.Fatal("accountIds changes last, after the partition is clean")
	}
	items := txItems(t, *drop)
	if len(items) != 2 {
		t.Fatalf("relink check + state update expected: %v", items)
	}
	check := items[0]["ConditionCheck"].(map[string]any)
	if pk, sk := callKey(dictCall{body: check}); pk != "USER#owner" || sk != "MEETING#m1" {
		t.Fatalf("the drop must check the owner's meeting: %s %s", pk, sk)
	}
	requireAll(t, "relink guard", resolved(check, "ConditionExpression"), "attribute_not_exists (accountId)", `accountId <> {"S":"a1"}`)
	update := items[1]["Update"].(map[string]any)
	requireAll(t, "drop", resolved(update, "UpdateExpression"), `DELETE accountIds {"SS":["a1"]}`)
	requireAll(t, "drop guard", resolved(update, "ConditionExpression"), `ownerId = {"S":"owner"}`, "contains (accountIds", `accountRecords = {"N":"2"}`)
	if strings.Contains(resolved(update, "UpdateExpression"), "accountAcceptedTerms") {
		t.Fatal("the shared Account key list is kept")
	}
}

func TestSweepMeetingAccountTermsNoOps(t *testing.T) {
	k := strings.Repeat("a", 32)
	for name, tc := range map[string]struct{ state, meeting string }{
		"no run state":             {`{}`, sweepMeetingLinkedA2},
		"another owner's state":    {sweepStateJSON("intruder", []string{"a1"}, 1, []string{k}, nil), sweepMeetingLinkedA2},
		"nothing recorded":         {sweepStateJSON("owner", nil, 0, []string{k}, nil), sweepMeetingLinkedA2},
		"only the current Account": {sweepStateJSON("owner", []string{"a2"}, 1, []string{k}, nil), sweepMeetingLinkedA2},
		"deleted meeting":          {sweepStateJSON("owner", []string{"a1"}, 1, []string{k}, nil), `{}`},
	} {
		t.Run(name, func(t *testing.T) {
			dict, calls := dictWire(t, func(_ int, op string, body map[string]any) (int, string) {
				if strings.Contains(callText(dictCall{body: body}), "ANALYSIS#terms") {
					return 200, tc.state
				}
				return 200, tc.meeting
			})
			got, err := NewDynamoDBRepository(dict.client, "dict-test").SweepMeetingAccountTerms(context.Background(), "owner", "m1")
			if err != nil || got != (TermsAccountSweep{}) {
				t.Fatalf("got %+v err=%v", got, err)
			}
			for _, c := range *calls {
				if isWrite(c.op) {
					t.Fatalf("%s must write nothing: %s", name, c.op)
				}
			}
		})
	}
	if _, err := NewDynamoDBRepository(nil, "t").SweepMeetingAccountTerms(context.Background(), "", "m1"); err == nil {
		t.Fatal("an owner is required")
	}
}

// A failed cleanup step keeps the Account recorded for the retry; a lost
// drop guard (a concurrent record or a relink back) keeps it as Retained.
func TestSweepMeetingAccountTermsKeepsTheEntryUntilClean(t *testing.T) {
	k := strings.Repeat("b", 32)
	for _, tc := range []struct {
		name      string
		records   int
		failObs   bool
		dropReply string
		want      TermsAccountSweep
		wantErr   bool
		wantGuard string
	}{
		{"observation delete fails", 1, true, "", TermsAccountSweep{Stale: 1}, true, ""},
		{"record or relink during the sweep", 4, false, cancelled("ConditionalCheckFailed", "None"), TermsAccountSweep{Stale: 1, Retained: 1}, false, `accountRecords = {"N":"4"}`},
		{"legacy state without a record count", 0, false, "", TermsAccountSweep{Stale: 1, Dropped: 1}, false, "attribute_not_exists (accountRecords)"},
		{"drop throttled", 1, false, cancelled("None", "ThrottlingError"), TermsAccountSweep{Stale: 1}, true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := sweepStateJSON("owner", []string{"a1"}, tc.records, []string{k}, nil)
			dict, calls := dictWire(t, func(_ int, op string, body map[string]any) (int, string) {
				text := callText(dictCall{body: body})
				switch {
				case op == "GetItem" && strings.Contains(text, "ANALYSIS#terms"):
					return 200, state
				case op == "GetItem" && strings.Contains(text, "MEETING#m1"):
					return 200, `{"Item":{"PK":{"S":"USER#owner"}}}` // unlinked
				case op == "DeleteItem" && tc.failObs:
					return 500, `{"__type":"InternalServerError","message":"down"}`
				case op == "TransactWriteItems" && tc.dropReply != "":
					return 400, tc.dropReply
				}
				return 200, `{}` // the term is already gone
			})
			got, err := NewDynamoDBRepository(dict.client, "dict-test").SweepMeetingAccountTerms(context.Background(), "owner", "m1")
			if (err != nil) != tc.wantErr || got != tc.want {
				t.Fatalf("got %+v err=%v", got, err)
			}
			var drops int
			for _, c := range *calls {
				if c.op != "TransactWriteItems" {
					continue
				}
				drops++
				if tc.wantGuard != "" {
					requireAll(t, "drop guard", resolved(txItems(t, c)[1]["Update"].(map[string]any), "ConditionExpression"), tc.wantGuard)
				}
				requireAll(t, "unlinked meeting", resolved(txItems(t, c)[0]["ConditionCheck"].(map[string]any), "ConditionExpression"), "attribute_not_exists (accountId)")
			}
			if tc.failObs && drops != 0 {
				t.Fatal("an Account with a failed step must stay recorded")
			}
		})
	}
}

// A merge for another source that lands between the sweep's read and write
// rewrites sources from a copy that still names this meeting; the guarded
// write fails, the term is re-read and the entry is removed from the new
// version. An orphan delete that loses to a new observation does the same.
func TestPruneUnlinkedAccountTermRereadsAfterConcurrentMerge(t *testing.T) {
	key := strings.Repeat("c", 32)
	t.Run("sources update", func(t *testing.T) {
		reads := 0
		dict, calls := dictWire(t, func(_ int, op string, body map[string]any) (int, string) {
			switch op {
			case "GetItem":
				reads++
				if reads == 1 {
					return 200, sweepTermJSON(key, model.TermOriginAuto, model.TermStateActive, 5, "", "meeting:m1", "meeting:m9")
				}
				return 200, sweepTermJSON(key, model.TermOriginAuto, model.TermStateActive, 6, "", "meeting:m7", "meeting:m1", "meeting:m9")
			case "Query":
				return 200, `{"Count":2}`
			case "UpdateItem":
				if strings.Contains(resolved(body, "ConditionExpression"), `{"N":"5"}`) {
					return 400, conditionalFailed
				}
			}
			return 200, `{}`
		})
		if err := NewDynamoDBRepository(dict.client, "dict-test").pruneUnlinkedAccountTerm(context.Background(), "ACCOUNT#a1", key, "m1"); err != nil {
			t.Fatal(err)
		}
		last := (*calls)[len(*calls)-1]
		update := resolved(last.body, "UpdateExpression")
		if reads != 2 || last.op != "UpdateItem" || !strings.Contains(resolved(last.body, "ConditionExpression"), `version = {"N":"6"}`) ||
			!strings.Contains(update, `"m7"`) || strings.Contains(update, `"m1"`) {
			t.Fatalf("retry must rewrite the new version without the meeting: reads=%d %s", reads, update)
		}
	})
	t.Run("orphan delete loses to an observation", func(t *testing.T) {
		count, version := 0, 3
		var ops []string
		dict, _ := dictWire(t, func(_ int, op string, body map[string]any) (int, string) {
			ops = append(ops, op)
			switch op {
			case "GetItem":
				return 200, sweepTermJSON(key, model.TermOriginAuto, model.TermStateActive, version, "", "meeting:m1")
			case "Query":
				c := count
				count, version = 1, 4 // another meeting observes right after the count
				return 200, `{"Count":` + strconv.Itoa(c) + `}`
			case "DeleteItem":
				return 400, conditionalFailed
			}
			return 200, `{}`
		})
		if err := NewDynamoDBRepository(dict.client, "dict-test").pruneUnlinkedAccountTerm(context.Background(), "ACCOUNT#a1", key, "m1"); err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(ops, ","); got != "GetItem,Query,DeleteItem,GetItem,Query,UpdateItem" {
			t.Fatalf("a lost orphan delete must re-read and drop the entry: %s", got)
		}
	})
	t.Run("persistent conflict surfaces", func(t *testing.T) {
		dict, calls := dictWire(t, func(_ int, op string, _ map[string]any) (int, string) {
			switch op {
			case "GetItem":
				return 200, sweepTermJSON(key, model.TermOriginManual, model.TermStateActive, 2, "", "meeting:m1")
			case "UpdateItem":
				return 400, conditionalFailed
			}
			return 200, `{}`
		})
		err := NewDynamoDBRepository(dict.client, "dict-test").pruneUnlinkedAccountTerm(context.Background(), "ACCOUNT#a1", key, "m1")
		if !errors.Is(err, ErrConditionFailed) || len(*calls) != 2*termsAccountPruneAttempts {
			t.Fatalf("want a retryable failure after %d attempts: err=%v calls=%d", termsAccountPruneAttempts, err, len(*calls))
		}
	})
}

// Race guard: an Account observation written for a meeting also requires the
// meeting to still be linked to that Account, so a run that read the link
// before a relink cannot write into the Account the meeting left. Personal
// writes and source-less (KB/research) observations are unchanged.
func TestAccountObservationRequiresTheMeetingLink(t *testing.T) {
	member := &MembershipCheck{AccountID: "a1", UserID: "u1"}
	for _, w := range acctWrites()[:3] {
		t.Run(w.name, func(t *testing.T) {
			for pk, want := range map[string]string{"ACCOUNT#a1": `accountId = {"S":"a1"}`, "USER#u1": ""} {
				repo, calls := dictWire(t, nil)
				if err := w.run(repo, pk, member); err != nil {
					t.Fatal(err)
				}
				var meetingCheck string
				for _, item := range txItems(t, (*calls)[0]) {
					check, ok := item["ConditionCheck"].(map[string]any)
					if !ok {
						continue
					}
					if _, sk := callKey(dictCall{body: check}); sk == "MEETING#m1" {
						meetingCheck = resolved(check, "ConditionExpression")
					}
				}
				if !strings.Contains(meetingCheck, "attribute_exists (PK)") {
					t.Fatalf("%s: the source meeting must exist: %q", pk, meetingCheck)
				}
				if want == "" && strings.Contains(meetingCheck, "accountId") || want != "" && !strings.Contains(meetingCheck, want) {
					t.Fatalf("%s: meeting check %q", pk, meetingCheck)
				}
			}
		})
	}
	repo, calls := dictWire(t, func(int, string, map[string]any) (int, string) {
		return 400, cancelled("None", "None", "ConditionalCheckFailed", "None")
	})
	term, obs := acctTerm("ACCOUNT#a1")
	if err := repo.CreateAutoTermChecked(context.Background(), term, obs, member); !errors.Is(err, ErrDictSourceMeetingGone) || len(*calls) != 1 {
		t.Fatalf("a relinked meeting must map to ErrDictSourceMeetingGone: %v", err)
	}
	kbTerm, kbObs := acctTerm("ACCOUNT#a1")
	kbObs.MeetingID, kbObs.SourceKind, kbObs.SourceRef = "", model.DictSourceKB, "kb/u1/doc.md"
	kbObs.SK = kbObs.ObservationSK()
	repo, calls = dictWire(t, nil)
	if err := repo.CreateAutoTermChecked(context.Background(), kbTerm, kbObs, member); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(callText((*calls)[0]), "MEETING#") {
		t.Fatal("a KB observation has no source meeting check")
	}
}
