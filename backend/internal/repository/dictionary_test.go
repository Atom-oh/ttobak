package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/ttobak/backend/internal/model"
)

type dictCall struct {
	op   string
	body map[string]any
}

// dictWire records every DynamoDB call; respond returns (status, body) per call.
func dictWire(t *testing.T, respond func(i int, op string, body map[string]any) (int, string)) (*DictionaryRepository, *[]dictCall) {
	t.Helper()
	calls := &[]dictCall{}
	client := dynamodb.New(dynamodb.Options{
		Region: "ap-northeast-2", Credentials: aws.AnonymousCredentials{}, RetryMaxAttempts: 1,
		HTTPClient: meetingListHTTPClientFunc(func(r *http.Request) (*http.Response, error) {
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			op := r.Header.Get("X-Amz-Target")
			op = op[strings.LastIndex(op, ".")+1:]
			*calls = append(*calls, dictCall{op: op, body: body})
			status, resp := 200, `{}`
			if respond != nil {
				status, resp = respond(len(*calls)-1, op, body)
			}
			return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/x-amz-json-1.0"}}, Body: io.NopCloser(strings.NewReader(resp))}, nil
		}),
	})
	return NewDictionaryRepository(client, "dict-test"), calls
}

func cancelled(codes ...string) string {
	reasons := make([]string, len(codes))
	for i, c := range codes {
		reasons[i] = `{"Code":"` + c + `"}`
	}
	return `{"__type":"com.amazonaws.dynamodb.v20120810#TransactionCanceledException","CancellationReasons":[` + strings.Join(reasons, ",") + `]}`
}

const conditionalFailed = `{"__type":"com.amazonaws.dynamodb.v20120810#ConditionalCheckFailedException","message":"failed"}`

// resolved returns the expression with names and values substituted in one
// pass, so a substituted value (such as a timestamp containing ":0") is never
// rewritten again and #1 never matches part of #10.
func resolved(op map[string]any, field string) string {
	expr, _ := op[field].(string)
	subst := map[string]string{}
	if names, ok := op["ExpressionAttributeNames"].(map[string]any); ok {
		for alias, name := range names {
			subst[alias] = name.(string)
		}
	}
	if values, ok := op["ExpressionAttributeValues"].(map[string]any); ok {
		for alias, value := range values {
			encoded, _ := json.Marshal(value)
			subst[alias] = string(encoded)
		}
	}
	return expressionAlias.ReplaceAllStringFunc(expr, func(alias string) string {
		if v, ok := subst[alias]; ok {
			return v
		}
		return alias
	})
}

var expressionAlias = regexp.MustCompile(`[#:][A-Za-z0-9_]+`)

func requireAll(t *testing.T, label, expr string, parts ...string) {
	t.Helper()
	for _, p := range parts {
		if !strings.Contains(expr, p) {
			t.Errorf("%s missing %q: %s", label, p, expr)
		}
	}
}

func txItems(t *testing.T, call dictCall) []map[string]any {
	t.Helper()
	if call.op != "TransactWriteItems" {
		t.Fatalf("expected a transaction, got %s", call.op)
	}
	var out []map[string]any
	for _, item := range call.body["TransactItems"].([]any) {
		out = append(out, item.(map[string]any))
	}
	return out
}

func sampleTerm() (*model.DictTerm, *model.DictObservation) {
	key := "0123456789abcdef0123456789abcdef"
	term := &model.DictTerm{PK: "USER#u1", SK: model.DictTermSK(key), TermKey: key, State: model.TermStateActive, Origin: model.TermOriginAuto, DisplayAs: "AlphaGrid", Hits: 1, Version: 1}
	obs := &model.DictObservation{PK: "USER#u1", SK: model.DictObsSK(key, "m1"), TermKey: key, MeetingID: "m1", OwnerID: "u1"}
	return term, obs
}

func TestSaveDictionaryIsFieldScopedUpsert(t *testing.T) {
	repo, calls := dictWire(t, nil)
	err := repo.SaveDictionary(context.Background(), &model.UserDictionary{UserID: "u1", Terms: []model.DictionaryTerm{{Phrase: "A"}}, VocabularyStatus: "PENDING"})
	if err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 || (*calls)[0].op != "UpdateItem" {
		t.Fatalf("legacy blob write must not be a whole-item PutItem: %+v", *calls)
	}
	update := resolved((*calls)[0].body, "UpdateExpression")
	if strings.Contains(update, "migratedAt") || strings.Contains(update, "REMOVE") {
		t.Fatalf("legacy write must preserve migratedAt: %s", update)
	}
	requireAll(t, "legacy write", update, `ADD blobRevision {"N":"1"}`)
}

// guardCase runs one write against a response and checks the mapped error
// plus the resolved condition of call 0.
type guardCase struct {
	name     string
	resp     string
	run      func(*DictionaryRepository) error
	want     error
	field    string // condition source: "" = top level, else Put/Update index
	item     int
	contains []string
	forbid   []string // must not appear in the UpdateExpression
}

func TestDictionaryWriteGuards(t *testing.T) {
	term, obs := sampleTerm()
	key := term.TermKey
	merge := DictMergeInput{PK: "USER#u1", TermKey: key, Observation: obs, HeardAs: []string{"알파"}, SeenAt: "now", ExpectedVersion: 3}
	phrase, ready := "Alpha-Grid", model.VocabStatusReady
	legacyGuard := []string{`legacyMigrated = {"BOOL":true}`, `version = {"N":"1"}`, `state = {"S":"active"}`, `origin = {"S":"manual"}`, "attribute_not_exists (userEditedAt)"}
	cases := []guardCase{
		{"legacy status sync pins observed name and status", conditionalFailed,
			func(r *DictionaryRepository) error {
				return r.UpdateLegacyVocabularyStatus(context.Background(), "u1", "ttobak-vocab-x", "PENDING", "READY")
			},
			ErrDictTermConflict, "", 0, []string{`vocabularyName = {"S":"ttobak-vocab-x"}`, `vocabularyStatus = {"S":"PENDING"}`}, []string{"terms"}},
		{"migration mark is set once on the copied blob and removes nothing", conditionalFailed,
			func(r *DictionaryRepository) error {
				return r.MarkDictionaryMigrated(context.Background(), "u1", "now", 7, "2026-10-18T09:00:00Z")
			},
			ErrDictTermConflict, "", 0, []string{"attribute_not_exists (migratedAt)", `blobRevision = {"N":"7"}`, `updatedAt = {"S":"2026-10-18T09:00:00Z"}`}, []string{"REMOVE", "terms", "vocabulary"}},
		{"pre-v2 blob mark requires no revision", conditionalFailed,
			func(r *DictionaryRepository) error {
				return r.MarkDictionaryMigrated(context.Background(), "u1", "now", 0, "")
			},
			ErrDictTermConflict, "", 0, []string{"attribute_not_exists (blobRevision)", "attribute_not_exists (updatedAt)"}, nil},
		{"legacy copy refresh only touches pristine copies", conditionalFailed,
			func(r *DictionaryRepository) error {
				next := *term
				next.Origin, next.LegacyMigrated = model.TermOriginManual, true
				return r.ReplaceLegacyMigratedTerm(context.Background(), "USER#u1", key, 1, &next)
			},
			ErrDictTermConflict, "", 0, legacyGuard, nil},
		{"legacy copy removal only touches pristine copies", conditionalFailed,
			func(r *DictionaryRepository) error {
				return r.ReplaceLegacyMigratedTerm(context.Background(), "USER#u1", key, 1, nil)
			},
			ErrDictTermConflict, "", 0, legacyGuard, nil},
		{"vocabulary state update is version guarded", conditionalFailed,
			func(r *DictionaryRepository) error {
				return r.UpdateVocabularyState(context.Background(), "u1", model.VocabContextPersonal, model.VocabLangKo, 3, VocabularyStateChange{Status: &ready}, "now")
			},
			ErrDictTermConflict, "", 0, []string{`version = {"N":"3"}`}, nil},
		{"vocabulary state create is create-only", conditionalFailed,
			func(r *DictionaryRepository) error {
				return r.CreateVocabularyStateIfAbsent(context.Background(), &model.VocabularyState{PK: "USER#u1", SK: "VOCAB#personal#ko-KR"})
			},
			ErrDictTermConflict, "", 0, []string{"attribute_not_exists (PK)"}, nil},
		{"tombstone or existing row blocks create", cancelled("ConditionalCheckFailed", "None"),
			func(r *DictionaryRepository) error { return r.CreateAutoTerm(context.Background(), term, obs) },
			ErrDictTermConflict, "Put", 0, []string{"attribute_not_exists (PK)"}, nil},
		{"create observation is create-only", cancelled("None", "ConditionalCheckFailed"),
			func(r *DictionaryRepository) error { return r.CreateAutoTerm(context.Background(), term, obs) },
			ErrDictObservationExists, "Put", 1, []string{"attribute_not_exists (PK)"}, nil},
		{"merge: meeting already counted", cancelled("ConditionalCheckFailed", "None"),
			func(r *DictionaryRepository) error { return r.MergeAutoObservation(context.Background(), merge) },
			ErrDictObservationExists, "Put", 0, []string{"attribute_not_exists (PK)"}, nil},
		{"merge: version, state, origin or user edit", cancelled("None", "ConditionalCheckFailed"),
			func(r *DictionaryRepository) error { return r.MergeAutoObservation(context.Background(), merge) },
			ErrDictTermConflict, "Update", 1, []string{`version = {"N":"3"}`, `state = {"S":"active"}`, `origin = {"S":"auto"}`, "attribute_not_exists (userEditedAt)"}, nil},
		{"evict blocked by user edit", conditionalFailed,
			func(r *DictionaryRepository) error {
				_, err := r.EvictAutoTerm(context.Background(), "USER#u1", key, 5)
				return err
			},
			ErrDictTermConflict, "", 0, []string{`version = {"N":"5"}`, `origin = {"S":"auto"}`, "attribute_not_exists (userEditedAt)", `state = {"S":"active"}`}, nil},
		{"user edit is version guarded", conditionalFailed,
			func(r *DictionaryRepository) error {
				return r.UpdateDictTerm(context.Background(), "USER#u1", key, DictTermChange{Phrase: &phrase}, "u1", "now", 9)
			},
			ErrDictTermConflict, "", 0, []string{`version = {"N":"9"}`, `<> {"S":"deleted"}`}, nil},
		{"tombstone is version guarded", conditionalFailed,
			func(r *DictionaryRepository) error {
				_, err := r.TombstoneDictTerm(context.Background(), "USER#u1", key, "u1", "now", 2)
				return err
			},
			ErrDictTermConflict, "", 0, []string{`version = {"N":"2"}`}, nil},
		{"restore requires the tombstone", conditionalFailed,
			func(r *DictionaryRepository) error { return r.RestoreDictTerm(context.Background(), term, 4) },
			ErrDictTermConflict, "", 0, []string{`state = {"S":"deleted"}`, `version = {"N":"4"}`}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo, calls := dictWire(t, func(int, string, map[string]any) (int, string) { return 400, tc.resp })
			if err := tc.run(repo); !errors.Is(err, tc.want) || !errors.Is(err, ErrConditionFailed) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
			if len(*calls) != 1 {
				t.Fatalf("a rejected write must not continue to cleanup: %d calls", len(*calls))
			}
			op := (*calls)[0].body
			if tc.field != "" {
				op = txItems(t, (*calls)[0])[tc.item][tc.field].(map[string]any)
			}
			requireAll(t, tc.name, resolved(op, "ConditionExpression"), tc.contains...)
			for _, bad := range tc.forbid {
				if strings.Contains(resolved(op, "UpdateExpression"), bad) {
					t.Errorf("update must not touch %q", bad)
				}
			}
		})
	}
}

func TestDictionaryWriteEffects(t *testing.T) {
	term, obs := sampleTerm()
	key := term.TermKey
	obsQuery := func(fail bool) func(int, string, map[string]any) (int, string) {
		return func(_ int, op string, _ map[string]any) (int, string) {
			if op == "Query" && fail {
				return 500, `{"__type":"com.amazonaws.dynamodb.v20120810#InternalServerError","message":"x"}`
			}
			if op == "Query" {
				return 200, `{"Items":[{"PK":{"S":"USER#u1"},"SK":{"S":"DICTOBS#` + key + `#m1"},"termKey":{"S":"` + key + `"}}]}`
			}
			return 200, `{}`
		}
	}
	repo, calls := dictWire(t, obsQuery(false))
	if res, err := repo.EvictAutoTerm(context.Background(), "USER#u1", key, 5); err != nil || res.CleanupErr != nil {
		t.Fatal(err, res.CleanupErr)
	}
	var ops []string
	for _, c := range *calls {
		ops = append(ops, c.op)
	}
	if strings.Join(ops, ",") != "DeleteItem,Query,DeleteItem" {
		t.Fatalf("eviction must delete the term, then its observations: %v", ops)
	}
	requireAll(t, "obs query", resolved((*calls)[1].body, "KeyConditionExpression"), "begins_with", "DICTOBS#"+key+"#")

	repo, calls = dictWire(t, obsQuery(true))
	res, err := repo.TombstoneDictTerm(context.Background(), "USER#u1", key, "u1", "now", 2)
	if err != nil || res.CleanupErr == nil {
		t.Fatalf("tombstone must commit and surface the cleanup failure: %v %v", err, res.CleanupErr)
	}
	requireAll(t, "tombstone", resolved((*calls)[0].body, "UpdateExpression"), "REMOVE", "displayAs", "phrase", "heardAs", "type", "aliasMode", "lastSeenAt", `hits = {"N":"0"}`, `"deleted"`)

	repo, calls = dictWire(t, nil)
	if err := repo.MergeAutoObservation(context.Background(), DictMergeInput{PK: "USER#u1", TermKey: key, Observation: obs, ExpectedVersion: 1}); err != nil {
		t.Fatal(err)
	}
	requireAll(t, "merge", resolved(txItems(t, (*calls)[0])[1]["Update"].(map[string]any), "UpdateExpression"), "ADD hits", "heardAs", "lastSeenAt", "version")
	if err := repo.RestoreDictTerm(context.Background(), term, 4); err != nil {
		t.Fatal(err)
	}
	if (*calls)[1].body["Item"].(map[string]any)["version"].(map[string]any)["N"] != "5" {
		t.Fatal("restore must bump the version")
	}
	phrase := "Alpha-Grid"
	if err := repo.UpdateDictTerm(context.Background(), "USER#u1", key, DictTermChange{Phrase: &phrase}, "u1", "now", 9); err != nil {
		t.Fatal(err)
	}
	requireAll(t, "edit", resolved((*calls)[2].body, "UpdateExpression"), "userEditedAt", "updatedBy", "phrase")
}

func TestPutDictTermsIfAbsentChunksAndSkipsExisting(t *testing.T) {
	terms := make([]model.DictTerm, 150)
	for i := range terms {
		terms[i] = model.DictTerm{PK: "USER#u1", SK: fmt.Sprintf("DICTTERM#%032d", i), Version: 1}
	}
	repo, calls := dictWire(t, func(i int, _ string, body map[string]any) (int, string) {
		if i == 0 {
			codes := make([]string, len(body["TransactItems"].([]any)))
			for j := range codes {
				codes[j] = "None"
			}
			codes[0] = "ConditionalCheckFailed" // already migrated or tombstoned
			return 400, cancelled(codes...)
		}
		return 200, `{}`
	})
	created, err := repo.PutDictTermsIfAbsent(context.Background(), terms)
	if err != nil {
		t.Fatal(err)
	}
	var sizes []int
	for _, c := range *calls {
		items := txItems(t, c)
		sizes = append(sizes, len(items))
		for _, item := range items {
			requireAll(t, "migration put", resolved(item["Put"].(map[string]any), "ConditionExpression"), "attribute_not_exists (PK)")
		}
	}
	if fmt.Sprint(sizes) != "[99 98 51]" || created != 149 {
		t.Fatalf("sizes=%v created=%d", sizes, created)
	}
}
