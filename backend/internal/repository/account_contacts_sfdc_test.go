package repository

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/ttobak/backend/internal/model"
)

const (
	sfdcTestContactID = "01234567-89ab-4cde-8123-0123456789ab"
	sfdcTestID        = "003AB00000abcdEFGH"
	sfdcTestAccountID = "001AB00000abcdEFGH"
)

func sfdcTestContact() *model.AccountContact {
	return &model.AccountContact{ContactID: sfdcTestContactID, Revision: "r1", Name: "A",
		Source: model.ContactSourceSfdc, SfdcContactID: sfdcTestID, SfdcAccountID: sfdcTestAccountID}
}

// wireItems decodes TransactItems from a raw request body into op maps.
func wireItems(body map[string]interface{}) []map[string]any {
	raw, _ := body["TransactItems"].([]interface{})
	out := make([]map[string]any, 0, len(raw))
	for _, entry := range raw {
		for kind, op := range entry.(map[string]interface{}) {
			m := op.(map[string]interface{})
			m["_kind"] = kind
			out = append(out, m)
		}
	}
	return out
}

func sfdcCancel(codes ...string) string {
	parts := make([]string, len(codes))
	for i, c := range codes {
		parts[i] = `{"Code":"` + c + `"}`
	}
	return `{"__type":"TransactionCanceledException","CancellationReasons":[` + strings.Join(parts, ",") + `]}`
}

func TestSfdcContactLinks(t *testing.T) {
	t.Run("create_with_sfdc_id_writes_index_row", func(t *testing.T) {
		var items []map[string]any
		repo := contactWire(t, func(target string, body map[string]interface{}) string {
			if !strings.HasSuffix(target, "TransactWriteItems") {
				t.Fatalf("unexpected call %s", target)
			}
			items = wireItems(body)
			return `{}`
		})
		if err := repo.WriteAccountContact(context.Background(), "account", "user", sfdcTestContact(), "create"); err != nil {
			t.Fatal(err)
		}
		if len(items) != 5 {
			t.Fatalf("want contact + index + suppression check, got %d items", len(items))
		}
		index := items[3]
		if index["_kind"] != "Put" || itemKey(index) != "ACCOUNT#account|SFDCCONTACT#"+sfdcTestID ||
			!strings.Contains(resolvedCondition(index), "attribute_not_exists (PK)") {
			t.Fatalf("index put %v %q", index["_kind"], resolvedCondition(index))
		}
		row := index["Item"].(map[string]any)
		if attrS(t, row, "contactId") != sfdcTestContactID || attrS(t, row, "sfdcAccountId") != sfdcTestAccountID {
			t.Fatalf("index row %v", row)
		}
		for k := range row {
			if k != "PK" && k != "SK" && k != "contactId" && k != "sfdcAccountId" && k != "sfdcContactId" {
				t.Fatalf("index row must hold only IDs, has %s", k)
			}
		}
		check := items[4]
		if check["_kind"] != "ConditionCheck" || itemKey(check) != "ACCOUNT#account|SFDCSUPPRESS#"+sfdcTestID ||
			!strings.Contains(resolvedCondition(check), "attribute_not_exists (PK)") {
			t.Fatalf("suppression check %v %q", check["_kind"], resolvedCondition(check))
		}
	})

	t.Run("create_without_sfdc_id_is_unchanged", func(t *testing.T) {
		var items []map[string]any
		repo := contactWire(t, func(_ string, body map[string]interface{}) string { items = wireItems(body); return `{}` })
		if err := repo.WriteAccountContact(context.Background(), "account", "user", &model.AccountContact{ContactID: sfdcTestContactID, Name: "A"}, "create"); err != nil {
			t.Fatal(err)
		}
		if len(items) != 3 {
			t.Fatalf("unlinked create must stay at 3 items, got %d", len(items))
		}
	})

	t.Run("invalid_sfdc_ids_are_rejected_before_any_write", func(t *testing.T) {
		calls := 0
		repo := contactWire(t, func(string, map[string]interface{}) string { calls++; return `{}` })
		for _, c := range []model.AccountContact{
			{ContactID: sfdcTestContactID, Name: "A", SfdcContactID: "003#SFDCSUPPRESS#x"},
			{ContactID: sfdcTestContactID, Name: "A", SfdcContactID: "short"},
			{ContactID: sfdcTestContactID, Name: "A", SfdcContactID: sfdcTestID, SfdcAccountID: "bad id"},
		} {
			for _, op := range []string{"create", "update"} {
				c := c
				if err := repo.WriteAccountContact(context.Background(), "account", "user", &c, op); !errors.Is(err, ErrInvalidSfdcLink) {
					t.Fatalf("%s %q: %v", op, c.SfdcContactID, err)
				}
			}
		}
		if calls != 0 {
			t.Fatalf("invalid links must not reach DynamoDB: %d", calls)
		}
	})

	t.Run("update_that_sets_a_link_reserves_the_index", func(t *testing.T) {
		var items []map[string]any
		repo := contactWire(t, func(_ string, body map[string]interface{}) string { items = wireItems(body); return `{}` })
		if err := repo.WriteAccountContact(context.Background(), "account", "user", sfdcTestContact(), "update"); err != nil {
			t.Fatal(err)
		}
		if len(items) != 5 || items[2]["_kind"] != "Update" {
			t.Fatalf("got %d items", len(items))
		}
		// The index may already be this contact's (a kept link), never another's.
		cond := resolvedCondition(items[3])
		if items[3]["_kind"] != "Put" || itemKey(items[3]) != "ACCOUNT#account|SFDCCONTACT#"+sfdcTestID ||
			!strings.Contains(cond, "attribute_not_exists (PK)") || !strings.Contains(cond, "contactId = ") {
			t.Fatalf("index put %q", cond)
		}
		if items[4]["_kind"] != "ConditionCheck" || itemKey(items[4]) != "ACCOUNT#account|SFDCSUPPRESS#"+sfdcTestID {
			t.Fatalf("suppression check %v", itemKey(items[4]))
		}
	})

	t.Run("second_create_for_same_sfdc_id_reports_already_linked", func(t *testing.T) {
		for _, op := range []string{"create", "update"} {
			repo := summarySDKRepo(func(req *http.Request) (*http.Response, error) {
				return summaryHTTPResponse(400, sfdcCancel("None", "None", "None", "ConditionalCheckFailed", "None"), nil), nil
			})
			repo.SetRecordSealer(testSealer{})
			err := repo.WriteAccountContact(context.Background(), "account", "user", sfdcTestContact(), op)
			if !errors.Is(err, ErrSfdcAlreadyLinked) {
				t.Fatalf("%s: %v", op, err)
			}
		}
	})

	t.Run("create_of_suppressed_sfdc_id_is_refused", func(t *testing.T) {
		repo := summarySDKRepo(func(req *http.Request) (*http.Response, error) {
			return summaryHTTPResponse(400, sfdcCancel("None", "None", "None", "None", "ConditionalCheckFailed"), nil), nil
		})
		repo.SetRecordSealer(testSealer{})
		if err := repo.WriteAccountContact(context.Background(), "account", "user", sfdcTestContact(), "create"); !errors.Is(err, ErrSfdcSuppressed) {
			t.Fatalf("suppressed create: %v", err)
		}
	})

	t.Run("access_failure_wins_over_link_failures", func(t *testing.T) {
		repo := summarySDKRepo(func(req *http.Request) (*http.Response, error) {
			return summaryHTTPResponse(400, sfdcCancel("None", "ConditionalCheckFailed", "None", "ConditionalCheckFailed", "None"), nil), nil
		})
		repo.SetRecordSealer(testSealer{})
		if err := repo.WriteAccountContact(context.Background(), "account", "user", sfdcTestContact(), "create"); !errors.Is(err, ErrContactAccess) {
			t.Fatalf("access first: %v", err)
		}
	})

	t.Run("deleted_linked_contact_is_suppressed", func(t *testing.T) {
		var items []map[string]any
		var query map[string]interface{}
		repo := contactWire(t, func(target string, body map[string]interface{}) string {
			if strings.HasSuffix(target, ".Query") {
				query = body
				return `{"Items":[{"PK":{"S":"ACCOUNT#account"},"SK":{"S":"SFDCCONTACT#` + sfdcTestID + `"},"contactId":{"S":"` + sfdcTestContactID + `"},"sfdcAccountId":{"S":"` + sfdcTestAccountID + `"}}]}`
			}
			items = wireItems(body)
			return `{}`
		})
		// A delete carries only identity and revision; the link comes from the index.
		if err := repo.WriteAccountContact(context.Background(), "account", "user", &model.AccountContact{ContactID: sfdcTestContactID, Revision: "r1"}, "delete"); err != nil {
			t.Fatal(err)
		}
		if query == nil || query["ConsistentRead"] != true || !strings.Contains(query["KeyConditionExpression"].(string), "begins_with") {
			t.Fatalf("index lookup must be a consistent partition query: %v", query)
		}
		if len(items) != 5 || items[2]["_kind"] != "Delete" || itemKey(items[2]) != "ACCOUNT#account|CONTACT#"+sfdcTestContactID {
			t.Fatalf("got %d items", len(items))
		}
		del := items[3]
		if del["_kind"] != "Delete" || itemKey(del) != "ACCOUNT#account|SFDCCONTACT#"+sfdcTestID || !strings.Contains(resolvedCondition(del), "contactId = ") {
			t.Fatalf("index delete %v %q", del["_kind"], resolvedCondition(del))
		}
		put := items[4]
		if put["_kind"] != "Put" || itemKey(put) != "ACCOUNT#account|SFDCSUPPRESS#"+sfdcTestID {
			t.Fatalf("suppression put %v", put["_kind"])
		}
		row := put["Item"].(map[string]any)
		if attrS(t, row, "sfdcContactId") != sfdcTestID {
			t.Fatalf("suppression row %v", row)
		}
		for k := range row {
			if k != "PK" && k != "SK" && k != "sfdcContactId" {
				t.Fatalf("suppression row must hold only the SFDC ID, has %s", k)
			}
		}
		if _, expires := row["pendingShareExpiresAt"]; expires {
			t.Fatal("suppression row must not expire")
		}
	})

	t.Run("deleted_unlinked_contact_is_unchanged", func(t *testing.T) {
		var items []map[string]any
		repo := contactWire(t, func(target string, body map[string]interface{}) string {
			if strings.HasSuffix(target, ".Query") {
				return `{"Items":[]}`
			}
			items = wireItems(body)
			return `{}`
		})
		if err := repo.WriteAccountContact(context.Background(), "account", "user", &model.AccountContact{ContactID: sfdcTestContactID, Revision: "r1"}, "delete"); err != nil {
			t.Fatal(err)
		}
		if len(items) != 3 {
			t.Fatalf("unlinked delete must stay at 3 items, got %d", len(items))
		}
	})

	t.Run("links_and_suppressions_list_all_pages", func(t *testing.T) {
		var starts []string
		repo := contactWire(t, func(target string, body map[string]interface{}) string {
			if !strings.HasSuffix(target, ".Query") || body["ConsistentRead"] != true {
				t.Fatalf("listing must be consistent queries: %s", target)
			}
			values, _ := json.Marshal(body["ExpressionAttributeValues"])
			start, _ := json.Marshal(body["ExclusiveStartKey"])
			starts = append(starts, string(start))
			prefix := "SFDCCONTACT#"
			if strings.Contains(string(values), "SFDCSUPPRESS#") {
				prefix = "SFDCSUPPRESS#"
			}
			row := func(id string) string {
				return `{"PK":{"S":"ACCOUNT#account"},"SK":{"S":"` + prefix + id + `"},"sfdcContactId":{"S":"` + id + `"},"contactId":{"S":"c-` + id + `"},"sfdcAccountId":{"S":"` + sfdcTestAccountID + `"}}`
			}
			if body["ExclusiveStartKey"] == nil {
				return `{"Items":[` + row("003AB00000abcdEFG1") + `],"LastEvaluatedKey":{"PK":{"S":"ACCOUNT#account"},"SK":{"S":"` + prefix + `003AB00000abcdEFG1"}}}`
			}
			return `{"Items":[` + row("003AB00000abcdEFG2") + `]}`
		})
		links, err := repo.ListSfdcContactLinks(context.Background(), "account")
		if err != nil || len(links) != 2 || links[1] != (SfdcContactLink{SfdcContactID: "003AB00000abcdEFG2", SfdcAccountID: sfdcTestAccountID, ContactID: "c-003AB00000abcdEFG2"}) {
			t.Fatalf("links %+v %v", links, err)
		}
		suppressed, err := repo.ListSfdcSuppressions(context.Background(), "account")
		if err != nil || len(suppressed) != 2 || !suppressed["003AB00000abcdEFG1"] || !suppressed["003AB00000abcdEFG2"] {
			t.Fatalf("suppressions %v %v", suppressed, err)
		}
		if len(starts) != 4 {
			t.Fatalf("each listing must follow its continuation: %v", starts)
		}
	})

	t.Run("sfdc_fields_are_sealed_not_plain", func(t *testing.T) {
		var items []map[string]any
		repo := contactWire(t, func(_ string, body map[string]interface{}) string { items = wireItems(body); return `{}` })
		c := sfdcTestContact()
		c.SfdcLeftAt = "2026-11-11T00:00:00Z"
		if err := repo.WriteAccountContact(context.Background(), "account", "user", c, "create"); err != nil {
			t.Fatal(err)
		}
		row := items[2]["Item"].(map[string]any)
		for _, k := range []string{"source", "sfdcContactId", "sfdcAccountId", "sfdcLeftAt"} {
			if _, ok := row[k]; ok {
				t.Fatalf("%s stored in plaintext on the contact row", k)
			}
		}
		plain, err := testSealer{}.Open(context.Background(), attrS(t, row, "sealed"), contactBinding("account", sfdcTestContactID))
		if err != nil {
			t.Fatal(err)
		}
		var got model.AccountContact
		if err := json.Unmarshal(plain, &got); err != nil || got.Source != "sfdc" || got.SfdcContactID != sfdcTestID ||
			got.SfdcAccountID != sfdcTestAccountID || got.SfdcLeftAt != "2026-11-11T00:00:00Z" {
			t.Fatalf("sealed SFDC fields %+v %v", got, err)
		}
	})
}

func TestValidSfdcID(t *testing.T) {
	for id, want := range map[string]bool{
		"003AB00000abcdE": true, "003AB00000abcdEFGH": true, "003AB00000abcd": false,
		"003AB00000abcdEFGHI": false, "003AB00000abcd#FG": false, "": false, "003AB00000abcdé": false,
	} {
		if model.ValidSfdcID(id) != want {
			t.Errorf("%q: want %v", id, want)
		}
	}
}

// sfdcMergeRespond answers GetItem for the target's SFDC rows from present
// (SK -> item JSON) and succeeds everything else.
func sfdcMergeRespond(present map[string]string) func(fenceRequest) (int, string) {
	return func(call fenceRequest) (int, string) {
		if call.target == "GetItem" {
			key, _ := json.Marshal(call.body["Key"])
			for sk, item := range present {
				if strings.Contains(string(key), `"`+sk+`"`) {
					return 200, `{"Item":` + item + `}`
				}
			}
			return 200, `{}`
		}
		return 200, `{}`
	}
}

func transactCall(t *testing.T, calls []fenceRequest) fenceRequest {
	t.Helper()
	for _, c := range calls {
		if c.target == "TransactWriteItems" {
			return c
		}
	}
	t.Fatal("no transaction")
	return fenceRequest{}
}

func findItem(items []map[string]any, key string) map[string]any {
	for _, it := range items {
		if itemKey(it) == key {
			return it
		}
	}
	return nil
}

func linkedMergeFrom() model.AccountContact {
	c := mergeFromContact()
	c.Source, c.SfdcContactID, c.SfdcAccountID = model.ContactSourceSfdc, sfdcTestID, sfdcTestAccountID
	return c
}

func TestSfdcMergeRows(t *testing.T) {
	t.Run("move_carries_index_row_to_target", func(t *testing.T) {
		var calls []fenceRequest
		r, _ := sealedMergeRepo(t, &calls, sfdcMergeRespond(nil))
		if err := r.MoveContactForMerge(context.Background(), mergeRowsJob(), linkedMergeFrom()); err != nil {
			t.Fatal(err)
		}
		items := fenceItems(t, transactCall(t, calls))
		if len(items) != 1+mergeFenceCheckCount+2+3 {
			t.Fatalf("got %d items", len(items))
		}
		src := findItem(items, "ACCOUNT#src|SFDCCONTACT#"+sfdcTestID)
		if src == nil || src["Item"] != nil || !strings.Contains(resolvedCondition(src), "contactId = ") {
			t.Fatalf("source index delete %v", src)
		}
		dst := findItem(items, "ACCOUNT#tgt|SFDCCONTACT#"+sfdcTestID)
		if dst == nil || attrS(t, dst["Item"].(map[string]any), "contactId") != "c1" || !strings.Contains(resolvedCondition(dst), "attribute_not_exists (PK)") {
			t.Fatalf("target index put %v", dst)
		}
		if chk := findItem(items, "ACCOUNT#tgt|SFDCSUPPRESS#"+sfdcTestID); chk == nil || !strings.Contains(resolvedCondition(chk), "attribute_not_exists (PK)") {
			t.Fatalf("target suppression check %v", chk)
		}
		plain, _ := testSealer{}.Open(context.Background(), attrS(t, items[0]["Item"].(map[string]any), "sealed"), contactBinding("tgt", "c1"))
		if !strings.Contains(string(plain), sfdcTestID) {
			t.Fatalf("moved contact must keep its link: %s", plain)
		}
	})

	t.Run("move_drops_link_when_target_holds_or_suppresses_it", func(t *testing.T) {
		for name, present := range map[string]map[string]string{
			"held":       {"SFDCCONTACT#" + sfdcTestID: `{"PK":{"S":"ACCOUNT#tgt"},"SK":{"S":"SFDCCONTACT#` + sfdcTestID + `"},"contactId":{"S":"other"}}`},
			"suppressed": {"SFDCSUPPRESS#" + sfdcTestID: `{"PK":{"S":"ACCOUNT#tgt"},"SK":{"S":"SFDCSUPPRESS#` + sfdcTestID + `"}}`},
		} {
			var calls []fenceRequest
			r, _ := sealedMergeRepo(t, &calls, sfdcMergeRespond(present))
			if err := r.MoveContactForMerge(context.Background(), mergeRowsJob(), linkedMergeFrom()); err != nil {
				t.Fatal(err)
			}
			items := fenceItems(t, transactCall(t, calls))
			if len(items) != 1+mergeFenceCheckCount+2+1 || findItem(items, "ACCOUNT#src|SFDCCONTACT#"+sfdcTestID) == nil {
				t.Fatalf("%s: only the source index delete is added, got %d", name, len(items))
			}
			plain, _ := testSealer{}.Open(context.Background(), attrS(t, items[0]["Item"].(map[string]any), "sealed"), contactBinding("tgt", "c1"))
			if strings.Contains(string(plain), sfdcTestID) || strings.Contains(string(plain), `"source"`) {
				t.Fatalf("%s: moved contact must drop the link: %s", name, plain)
			}
		}
	})

	t.Run("merge_into_same_link_keeps_target_index", func(t *testing.T) {
		var calls []fenceRequest
		held := map[string]string{"SFDCCONTACT#" + sfdcTestID: `{"PK":{"S":"ACCOUNT#tgt"},"SK":{"S":"SFDCCONTACT#` + sfdcTestID + `"},"contactId":{"S":"b9"}}`}
		r, _ := sealedMergeRepo(t, &calls, sfdcMergeRespond(held))
		into := model.AccountContact{ContactID: "b9", Revision: "rev-b", Name: "Alice", Source: "sfdc", SfdcContactID: sfdcTestID, SfdcAccountID: sfdcTestAccountID}
		if err := r.MergeContactIntoExisting(context.Background(), mergeRowsJob(), linkedMergeFrom(), into); err != nil {
			t.Fatal(err)
		}
		items := fenceItems(t, transactCall(t, calls))
		dst := findItem(items, "ACCOUNT#tgt|SFDCCONTACT#"+sfdcTestID)
		if findItem(items, "ACCOUNT#src|SFDCCONTACT#"+sfdcTestID) == nil || dst == nil || attrS(t, dst["Item"].(map[string]any), "contactId") != "b9" {
			t.Fatalf("source index must be deleted and target index stay on b9: %d items", len(items))
		}
		if !strings.Contains(resolvedCondition(dst), "contactId = ") {
			t.Fatalf("target index may only be rewritten for b9: %q", resolvedCondition(dst))
		}
	})

	t.Run("merge_into_other_link_suppresses_duplicate", func(t *testing.T) {
		var calls []fenceRequest
		r, _ := sealedMergeRepo(t, &calls, sfdcMergeRespond(nil))
		into := model.AccountContact{ContactID: "b9", Revision: "rev-b", Name: "Alice", Source: "sfdc", SfdcContactID: "003AB00000abcdOTHR", SfdcAccountID: sfdcTestAccountID}
		if err := r.MergeContactIntoExisting(context.Background(), mergeRowsJob(), linkedMergeFrom(), into); err != nil {
			t.Fatal(err)
		}
		items := fenceItems(t, transactCall(t, calls))
		if len(items) != 1+mergeFenceCheckCount+2+3 {
			t.Fatalf("got %d items", len(items))
		}
		if findItem(items, "ACCOUNT#src|SFDCCONTACT#"+sfdcTestID) == nil {
			t.Fatal("source index delete missing")
		}
		sup := findItem(items, "ACCOUNT#tgt|SFDCSUPPRESS#"+sfdcTestID)
		if sup == nil || sup["Item"] == nil {
			t.Fatal("duplicate link must be suppressed on the target")
		}
		if chk := findItem(items, "ACCOUNT#tgt|SFDCCONTACT#"+sfdcTestID); chk == nil || !strings.Contains(resolvedCondition(chk), "attribute_not_exists (PK)") {
			t.Fatal("suppression requires the target index to stay absent")
		}
	})

	t.Run("suppressions_move_to_target", func(t *testing.T) {
		var calls []fenceRequest
		r, _ := sealedMergeRepo(t, &calls, func(call fenceRequest) (int, string) {
			switch call.target {
			case "Query":
				return 200, `{"Items":[{"PK":{"S":"ACCOUNT#src"},"SK":{"S":"SFDCSUPPRESS#` + sfdcTestID + `"},"sfdcContactId":{"S":"` + sfdcTestID + `"}}]}`
			case "GetItem":
				return 200, `{}`
			}
			return 200, `{}`
		})
		moved, done, err := r.MoveSfdcSuppressionsForMerge(context.Background(), mergeRowsJob(), 50)
		if err != nil || moved != 1 || !done {
			t.Fatalf("move: %d %v %v", moved, done, err)
		}
		items := fenceItems(t, transactCall(t, calls))
		if len(items) != mergeFenceCheckCount+3 {
			t.Fatalf("got %d items", len(items))
		}
		if del := findItem(items, "ACCOUNT#src|SFDCSUPPRESS#"+sfdcTestID); del == nil || del["Item"] != nil {
			t.Fatal("source suppression delete missing")
		}
		if put := findItem(items, "ACCOUNT#tgt|SFDCSUPPRESS#"+sfdcTestID); put == nil || attrS(t, put["Item"].(map[string]any), "sfdcContactId") != sfdcTestID {
			t.Fatal("target suppression put missing")
		}
		if chk := findItem(items, "ACCOUNT#tgt|SFDCCONTACT#"+sfdcTestID); chk == nil || !strings.Contains(resolvedCondition(chk), "attribute_not_exists (PK)") {
			t.Fatal("target suppression must not shadow a target link")
		}
	})
}
