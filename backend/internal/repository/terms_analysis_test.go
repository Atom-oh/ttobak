package repository

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/ttobak/backend/internal/model"
)

// termsWire records each call and answers every call with (status, body).
func termsWire(t *testing.T, status int, body string) (*DynamoDBRepository, *[]dictCall) {
	t.Helper()
	calls := &[]dictCall{}
	client := dynamodb.New(dynamodb.Options{
		Region: "ap-northeast-2", Credentials: aws.AnonymousCredentials{}, RetryMaxAttempts: 1,
		HTTPClient: meetingListHTTPClientFunc(func(r *http.Request) (*http.Response, error) {
			var decoded map[string]any
			if err := json.NewDecoder(r.Body).Decode(&decoded); err != nil {
				t.Fatal(err)
			}
			op := r.Header.Get("X-Amz-Target")
			*calls = append(*calls, dictCall{op: op[strings.LastIndex(op, ".")+1:], body: decoded})
			return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/x-amz-json-1.0"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
		}),
	})
	return NewDynamoDBRepository(client, "terms-test"), calls
}

const acceptedKey = "0123456789abcdef0123456789abcdef"

func TestTermsAnalysisTransitions(t *testing.T) {
	now := time.UnixMilli(1_800_000_000_000).UTC()
	prior := &model.TermsAnalysis{RunID: "old-run", Status: model.AnalysisRunning, LeaseUntil: 111}
	next := &model.TermsAnalysis{RunID: "new-run", SourceHash: "hash-2", SummaryGeneration: 4, StartedAt: now, UpdatedAt: now, LeaseUntil: 999}
	accepted := []model.TermsAcceptedTerm{{TermKey: acceptedKey, SourceTypes: []string{model.TermSourceTranscript}}}
	meetingDone := []string{"attribute_exists (PK)", `status = {"S":"done"}`}
	cases := []struct {
		name        string
		run         func(*DynamoDBRepository) error
		meeting     []string // nil: no transaction / no meeting check
		condition   []string
		update      []string
		forbidState []string
	}{
		{"queue replaces an expired pending run for a new source",
			func(r *DynamoDBRepository) error {
				return r.QueueTermsAnalysis(context.Background(), "owner", "m1", prior, next)
			},
			meetingDone,
			[]string{`runId = {"S":"old-run"}`, `status = {"S":"running"}`, `leaseUntil = {"N":"111"}`, `leaseUntil <= {"N":"1800000000000"}`,
				"attribute_not_exists (succeededSourceHash)", `succeededSourceHash <> {"S":"hash-2"}`},
			[]string{`status = {"S":"queued"}`, `sourceHash = {"S":"hash-2"}`, `summaryGeneration = {"N":"4"}`, `errorCode = {"S":""}`, "counters"},
			[]string{"acceptedTerms"}},
		{"first queue is create-only",
			func(r *DynamoDBRepository) error {
				return r.QueueTermsAnalysis(context.Background(), "owner", "m1", nil, next)
			},
			meetingDone,
			[]string{"attribute_not_exists (PK)", `succeededSourceHash <> {"S":"hash-2"}`},
			[]string{`runId = {"S":"new-run"}`}, nil},
		{"start needs the queued run, its lease and a done meeting",
			func(r *DynamoDBRepository) error {
				return r.StartTermsAnalysis(context.Background(), "owner", "m1", "run-1", now, 2_000)
			},
			meetingDone,
			[]string{`runId = {"S":"run-1"}`, `status = {"S":"queued"}`, `leaseUntil > {"N":"1800000000000"}`},
			[]string{`status = {"S":"running"}`, `leaseUntil = {"N":"2000"}`}, nil},
		{"renew needs the running run and the observed live lease",
			func(r *DynamoDBRepository) error {
				return r.RenewTermsAnalysisLease(context.Background(), "owner", "m1", "run-1", 1_800_000_100_000, now, 1_800_000_400_000)
			},
			meetingDone,
			[]string{`runId = {"S":"run-1"}`, `status = {"S":"running"}`, `leaseUntil = {"N":"1800000100000"}`, `leaseUntil > {"N":"1800000000000"}`},
			[]string{`leaseUntil = {"N":"1800000400000"}`}, []string{"status ="}},
		{"reserve renews the live lease and lists keys before observations",
			func(r *DynamoDBRepository) error {
				return r.ReserveTermsAccepted(context.Background(), "owner", "m1", "run-1", 1_800_000_100_000, now, 1_800_000_400_000, accepted)
			},
			meetingDone,
			[]string{`runId = {"S":"run-1"}`, `status = {"S":"running"}`, `leaseUntil = {"N":"1800000100000"}`, `leaseUntil > {"N":"1800000000000"}`},
			[]string{`leaseUntil = {"N":"1800000400000"}`, "acceptedTerms", acceptedKey}, []string{"status ="}},
		{"complete matches run and lease and records the succeeded source",
			func(r *DynamoDBRepository) error {
				return r.CompleteTermsAnalysis(context.Background(), "owner", "m1", "run-1", 5_000, TermsCompletion{
					Counters: model.TermsCounters{Created: 2}, AcceptedTerms: accepted, ErrorCode: model.TermsErrorTaintedSuggestOnly,
				}, now)
			},
			[]string{"attribute_exists (PK)"},
			[]string{`runId = {"S":"run-1"}`, `status = {"S":"running"}`, `leaseUntil = {"N":"5000"}`, `leaseUntil > {"N":"1800000000000"}`},
			[]string{`status = {"S":"succeeded"}`, "succeededSourceHash = sourceHash", `"TAINTED_SUGGEST_ONLY"`, "acceptedTerms", acceptedKey, `leaseUntil = {"N":"0"}`}, nil},
		{"fail matches the observed run and keeps accepted terms",
			func(r *DynamoDBRepository) error {
				return r.FailTermsAnalysis(context.Background(), "m1", prior, model.AnalysisPersistenceFail, accepted, now)
			},
			nil,
			[]string{`runId = {"S":"old-run"}`, `status = {"S":"running"}`, `leaseUntil = {"N":"111"}`, "attribute_exists (PK)"},
			[]string{`status = {"S":"failed"}`, `"PERSISTENCE_FAILED"`, "acceptedTerms", `leaseUntil = {"N":"0"}`}, []string{"succeededSourceHash"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo, calls := termsWire(t, 200, `{}`)
			if err := tc.run(repo); err != nil {
				t.Fatal(err)
			}
			if len(*calls) != 1 {
				t.Fatalf("one write expected, got %d", len(*calls))
			}
			op := (*calls)[0].body
			if tc.meeting != nil {
				items := txItems(t, (*calls)[0])
				if len(items) != 2 {
					t.Fatalf("meeting check + state update expected, got %d", len(items))
				}
				check := items[0]["ConditionCheck"].(map[string]any)
				key := check["Key"].(map[string]any)
				if key["PK"].(map[string]any)["S"] != "USER#owner" || key["SK"].(map[string]any)["S"] != "MEETING#m1" {
					t.Fatalf("meeting check must target the owner's meeting: %v", key)
				}
				requireAll(t, "meeting check", resolved(check, "ConditionExpression"), tc.meeting...)
				op = items[1]["Update"].(map[string]any)
			} else if (*calls)[0].op != "UpdateItem" {
				t.Fatalf("fail must be one conditional update, got %s", (*calls)[0].op)
			}
			if op["Key"].(map[string]any)["SK"].(map[string]any)["S"] != model.TermsAnalysisSK {
				t.Fatal("run state must use ANALYSIS#terms")
			}
			requireAll(t, "condition", resolved(op, "ConditionExpression"), tc.condition...)
			update := resolved(op, "UpdateExpression")
			requireAll(t, "update", update, tc.update...)
			for _, bad := range tc.forbidState {
				if strings.Contains(update, bad) {
					t.Errorf("update must not touch %q: %s", bad, update)
				}
			}
		})
	}
}

func TestTermsAnalysisRejectsInvalidStateWithoutWriting(t *testing.T) {
	now := time.Now().UTC()
	tooMany := make([]model.TermsAcceptedTerm, model.MaxTermsAccepted+1)
	for i := range tooMany {
		tooMany[i] = model.TermsAcceptedTerm{TermKey: acceptedKey}
	}
	expected := &model.TermsAnalysis{RunID: "r", Status: model.AnalysisRunning}
	for name, run := range map[string]func(*DynamoDBRepository) error{
		"unknown fail code": func(r *DynamoDBRepository) error {
			return r.FailTermsAnalysis(context.Background(), "m1", expected, "SOMETHING", nil, now)
		},
		"tainted is not a failure": func(r *DynamoDBRepository) error {
			return r.FailTermsAnalysis(context.Background(), "m1", expected, model.TermsErrorTaintedSuggestOnly, nil, now)
		},
		"text instead of a key": func(r *DynamoDBRepository) error {
			return r.FailTermsAnalysis(context.Background(), "m1", expected, model.AnalysisInterrupted, []model.TermsAcceptedTerm{{TermKey: "AlphaGrid"}}, now)
		},
		"more than 40 accepted": func(r *DynamoDBRepository) error {
			return r.CompleteTermsAnalysis(context.Background(), "o", "m1", "r", 1, TermsCompletion{AcceptedTerms: tooMany}, now)
		},
		"unknown source type": func(r *DynamoDBRepository) error {
			return r.CompleteTermsAnalysis(context.Background(), "o", "m1", "r", 1, TermsCompletion{AcceptedTerms: []model.TermsAcceptedTerm{{TermKey: acceptedKey, SourceTypes: []string{"live"}}}}, now)
		},
		"failure code on success": func(r *DynamoDBRepository) error {
			return r.CompleteTermsAnalysis(context.Background(), "o", "m1", "r", 1, TermsCompletion{ErrorCode: model.AnalysisProviderFailed}, now)
		},
		"queue without a source hash": func(r *DynamoDBRepository) error {
			return r.QueueTermsAnalysis(context.Background(), "o", "m1", nil, &model.TermsAnalysis{RunID: "r"})
		},
		"queue without an owner": func(r *DynamoDBRepository) error {
			return r.QueueTermsAnalysis(context.Background(), "", "m1", nil, &model.TermsAnalysis{RunID: "r", SourceHash: "h"})
		},
	} {
		t.Run(name, func(t *testing.T) {
			repo, calls := termsWire(t, 200, `{}`)
			if err := run(repo); err == nil {
				t.Fatal("expected a validation error")
			}
			if len(*calls) != 0 {
				t.Fatal("invalid state must not reach DynamoDB")
			}
		})
	}
}

func TestTermsAnalysisMapsConditionFailures(t *testing.T) {
	now := time.Now().UTC()
	for _, tc := range []struct {
		status int
		body   string
		cond   bool
	}{
		{400, cancelled("ConditionalCheckFailed", "None"), true}, // meeting gone or not done
		{400, cancelled("None", "ConditionalCheckFailed"), true}, // run/lease/source CAS
		{400, cancelled("TransactionConflict", "None"), false},
	} {
		repo, _ := termsWire(t, tc.status, tc.body)
		err := repo.StartTermsAnalysis(context.Background(), "o", "m1", "r", now, 1)
		if errors.Is(err, ErrConditionFailed) != tc.cond || err == nil {
			t.Fatalf("%s mapped incorrectly: %v", tc.body, err)
		}
	}
	repo, _ := termsWire(t, 400, conditionalFailed)
	err := repo.FailTermsAnalysis(context.Background(), "m1", &model.TermsAnalysis{RunID: "r", Status: model.AnalysisQueued}, model.AnalysisInterrupted, nil, now)
	if !errors.Is(err, ErrConditionFailed) {
		t.Fatalf("fail lost to a replacement run must map to ErrConditionFailed: %v", err)
	}
}

func TestPromoteSuggestedTermActivatesAndRestartsHits(t *testing.T) {
	_, obs := sampleTerm()
	repo, calls := dictWire(t, nil)
	in := DictMergeInput{PK: "USER#u1", TermKey: obs.TermKey, Observation: obs, HeardAs: []string{"알파"},
		SourceTypes: []string{model.TermSourceTranscript}, SeenAt: "now", ExpectedVersion: 2}
	if err := repo.PromoteSuggestedTerm(context.Background(), in, DictPromotion{DisplayAs: "AlphaGrid", FirstSourceMeetingID: "m1"}); err != nil {
		t.Fatal(err)
	}
	items := txItems(t, (*calls)[0])
	if _, ok := items[0]["Put"]; !ok || len(items) != 3 {
		t.Fatal("promotion must create the observation and update the term atomically")
	}
	check := items[2]["ConditionCheck"].(map[string]any)
	if key := check["Key"].(map[string]any); key["SK"].(map[string]any)["S"] != "MEETING#m1" || resolved(check, "ConditionExpression") != "attribute_exists (PK)" {
		t.Fatalf("observation writes must require the source meeting: %v", check)
	}
	update := resolved(items[1]["Update"].(map[string]any), "UpdateExpression")
	requireAll(t, "promote", update, `state = {"S":"active"}`, `hits = {"N":"1"}`, "sourceTypes", "heardAs", "lastSeenAt", "version")
	if strings.Contains(update, "ADD") {
		t.Fatalf("the tainted suggestion's hit must not count: %s", update)
	}

	bad := in
	other := *obs
	other.SK = model.DictObsSK(obs.TermKey, "m2")
	bad.Observation = &other
	if err := repo.PromoteSuggestedTerm(context.Background(), bad, DictPromotion{DisplayAs: "AlphaGrid"}); err == nil {
		t.Fatal("an observation key for another meeting must be rejected")
	}
}

func TestSweepMeetingTermsDeletesObservationsOrphansAndState(t *testing.T) {
	k1, k2, k3 := strings.Repeat("a", 32), strings.Repeat("b", 32), strings.Repeat("c", 32)
	state := `{"Item":{"PK":{"S":"MEETING#m1"},"SK":{"S":"ANALYSIS#terms"},"ownerId":{"S":"owner"},"runId":{"S":"r"},"status":{"S":"succeeded"},"acceptedTerms":{"L":[` +
		`{"M":{"termKey":{"S":"` + k1 + `"}}},{"M":{"termKey":{"S":"` + k2 + `"}}},{"M":{"termKey":{"S":"` + k3 + `"}}}]}}}`
	term := func(key, origin string) string {
		return `{"Item":{"PK":{"S":"USER#owner"},"SK":{"S":"DICTTERM#` + key + `"},"termKey":{"S":"` + key + `"},"origin":{"S":"` + origin + `"},"state":{"S":"active"},"version":{"N":"4"}}}`
	}
	other, otherCalls := dictWire(t, func(_ int, op string, body map[string]any) (int, string) {
		if op == "GetItem" {
			return 200, state
		}
		return 200, `{}`
	})
	if err := NewDynamoDBRepository(other.client, "dict-test").SweepMeetingTerms(context.Background(), "intruder", "m1"); err != nil || len(*otherCalls) != 1 {
		t.Fatalf("another user's sweep must touch nothing: err=%v calls=%d", err, len(*otherCalls))
	}
	for _, failObs := range []bool{false, true} {
		dict, calls := dictWire(t, func(_ int, op string, body map[string]any) (int, string) {
			encoded, _ := json.Marshal(body)
			text := string(encoded)
			switch {
			case op == "GetItem" && strings.Contains(text, "ANALYSIS#terms"):
				return 200, state
			case op == "DeleteItem" && strings.Contains(text, "DICTOBS#") && failObs:
				return 500, `{"__type":"InternalServerError","message":"down"}`
			case op == "Query" && strings.Contains(text, k2):
				return 200, `{"Count":1}`
			case op == "Query":
				return 200, `{"Count":0}`
			case op == "GetItem" && strings.Contains(text, k1):
				return 200, term(k1, model.TermOriginAuto)
			case op == "GetItem" && strings.Contains(text, k3):
				return 200, term(k3, model.TermOriginManual)
			}
			return 200, `{}`
		})
		repo := NewDynamoDBRepository(dict.client, "dict-test")
		err := repo.SweepMeetingTerms(context.Background(), "owner", "m1")
		var deleted []string
		for _, c := range *calls {
			if c.op != "DeleteItem" {
				continue
			}
			sk := c.body["Key"].(map[string]any)["SK"].(map[string]any)["S"].(string)
			deleted = append(deleted, sk)
			if strings.HasPrefix(sk, "DICTTERM#") {
				requireAll(t, "orphan delete", resolved(c.body, "ConditionExpression"), `version = {"N":"4"}`, `origin = {"S":"auto"}`, "attribute_not_exists (userEditedAt)")
			}
			if strings.HasPrefix(sk, "DICTOBS#") && c.body["Key"].(map[string]any)["PK"].(map[string]any)["S"] != "USER#owner" {
				t.Fatal("observations are deleted in the owner's personal scope")
			}
		}
		got := strings.Join(deleted, ",")
		if failObs {
			if err == nil || strings.Contains(got, "ANALYSIS#terms") || strings.Contains(got, "DICTTERM#") {
				t.Fatalf("a failed step must surface and keep the run state: err=%v deleted=%s", err, got)
			}
			continue
		}
		want := "DICTOBS#" + k1 + "#m1,DICTTERM#" + k1 + ",DICTOBS#" + k2 + "#m1,DICTOBS#" + k3 + "#m1,ANALYSIS#terms"
		if err != nil || got != want {
			t.Fatalf("sweep: err=%v\n got %s\nwant %s", err, got, want)
		}
	}
}

// Interleaving (review of PR #374): the sweep reads the term version before it
// counts observations. A merge for another live meeting that commits DICTOBS#
// plus version+1 after the count must make the version-guarded delete fail,
// so the active auto term survives with its new observation.
func TestDeleteOrphanAutoTermLosesToConcurrentObservation(t *testing.T) {
	key := strings.Repeat("d", 32)
	version, observations := 4, 0
	var order []string
	dict, calls := dictWire(t, func(_ int, op string, body map[string]any) (int, string) {
		encoded, _ := json.Marshal(body)
		text := string(encoded)
		switch {
		case op == "GetItem" && strings.Contains(text, "DICTTERM#"):
			order = append(order, "read-term")
			return 200, `{"Item":{"PK":{"S":"USER#owner"},"SK":{"S":"DICTTERM#` + key + `"},"termKey":{"S":"` + key +
				`"},"origin":{"S":"auto"},"state":{"S":"active"},"version":{"N":"` + strconv.Itoa(version) + `"}}}`
		case op == "Query":
			order = append(order, "count")
			count := observations
			// observeAutoTerm for meeting m2 commits right after the count:
			// DICTOBS#key#m2 and version+1 in one transaction.
			observations++
			version++
			return 200, `{"Count":` + strconv.Itoa(count) + `}`
		case op == "DeleteItem" && strings.Contains(text, "DICTTERM#"):
			order = append(order, "delete-term")
			if !strings.Contains(resolved(body, "ConditionExpression"), `version = {"N":"`+strconv.Itoa(version)+`"}`) {
				return 400, `{"__type":"ConditionalCheckFailedException","message":"stale"}`
			}
			return 200, `{}`
		}
		return 200, `{}`
	})
	repo := NewDynamoDBRepository(dict.client, "dict-test")
	if err := repo.deleteOrphanAutoTerm(context.Background(), "USER#owner", key); err != nil {
		t.Fatalf("a lost race is not an error: %v", err)
	}
	if got := strings.Join(order, ","); got != "read-term,count,delete-term" {
		t.Fatalf("the term version must be read before the observation count: %s", got)
	}
	last := (*calls)[len(*calls)-1]
	if cond := resolved(last.body, "ConditionExpression"); !strings.Contains(cond, `version = {"N":"4"}`) {
		t.Fatalf("delete must bind the version read before the count: %s", cond)
	}
}
