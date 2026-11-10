package repository

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ttobak/backend/internal/model"
)

var performanceTestClock = time.Date(2026, time.November, 10, 0, 0, 0, 0, time.UTC)

// pinPerformanceNow fixes the repository clock for one test.
func pinPerformanceNow(test *testing.T) {
	test.Helper()
	previous := performanceNow
	performanceNow = func() time.Time { return performanceTestClock }
	test.Cleanup(func() { performanceNow = previous })
}

const performanceTestBatch = "90ec2d25-a32e-43a5-96a9-e203859b7d19"

var performanceTestDigest = strings.Repeat("a", 64)

func performanceTarget(request *http.Request) string {
	target := request.Header.Get("X-Amz-Target")
	return target[strings.LastIndex(target, ".")+1:]
}

func performanceBody(test *testing.T, request *http.Request) map[string]any {
	test.Helper()
	var body map[string]any
	if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
		test.Fatal(err)
	}
	return body
}

func performanceTaskRequest(expected int64) model.PerformanceSyncRequest {
	return model.PerformanceSyncRequest{
		BatchID: performanceTestBatch, ExpectedRevision: expected, Complete: true,
		Mutations: []model.PerformanceMutation{
			{Op: "upsert", Task: &model.SFDCTask{TaskID: "T1", SFDCAccountID: "A1", ActivityDate: "2026-03-04", Type: "Call", Subject: "Sync", Status: "Completed", SpecReqID: "SR-1"}},
			{Op: "remove", ExternalID: "T2"},
		},
	}
}

const performanceCancelled = `{"__type":"com.amazonaws.dynamodb.v20120810#TransactionCanceledException","Message":"x","CancellationReasons":[{"Code":"ConditionalCheckFailed"},{"Code":"None"},{"Code":"None"},{"Code":"None"}]}`

func TestApplyPerformanceBatchRevisionCAS(test *testing.T) {
	pinPerformanceNow(test)
	for _, expected := range []int64{0, 4} {
		var source map[string]any
		repo := summarySDKRepo(func(request *http.Request) (*http.Response, error) {
			body := performanceBody(test, request)
			switch performanceTarget(request) {
			case "GetItem":
				return summaryHTTPResponse(200, `{}`, nil), nil
			case "TransactWriteItems":
				operations := body["TransactItems"].([]any)
				if len(operations) != 4 {
					test.Fatalf("source, task, removal and receipt must share one transaction: %d", len(operations))
				}
				source = operations[0].(map[string]any)["Update"].(map[string]any)
				return summaryHTTPResponse(400, performanceCancelled, nil), nil
			}
			test.Fatalf("unexpected call %s", performanceTarget(request))
			return nil, nil
		})
		_, err := repo.ApplyPerformanceBatch(test.Context(), "owner", model.PerformanceSourceSFDCTasks, performanceTaskRequest(expected), performanceTestDigest)
		if !errors.Is(err, ErrConditionFailed) {
			test.Fatalf("a stale revision must map to ErrConditionFailed: %v", err)
		}
		condition := analysisCondition(test, source)
		if expected == 0 && !strings.Contains(condition, "attribute_not_exists") {
			test.Fatalf("first batch must require an absent source row: %s", condition)
		}
		if expected == 4 && (!strings.Contains(condition, "revision") || !strings.Contains(condition, `"N":"4"`) || !strings.Contains(condition, "owner")) {
			test.Fatalf("source update must be conditioned on the expected revision: %s", condition)
		}
	}
}

func TestApplyPerformanceBatchReceiptReplay(test *testing.T) {
	pinPerformanceNow(test)
	var receiptPut map[string]any
	var stored map[string]any
	repo := summarySDKRepo(func(request *http.Request) (*http.Response, error) {
		body := performanceBody(test, request)
		switch performanceTarget(request) {
		case "GetItem":
			if stored != nil {
				return summaryHTTPResponse(200, `{"Item":`+mustJSON(test, stored)+`}`, nil), nil
			}
			return summaryHTTPResponse(200, `{}`, nil), nil
		case "TransactWriteItems":
			operations := body["TransactItems"].([]any)
			receiptPut = operations[len(operations)-1].(map[string]any)["Put"].(map[string]any)
			stored = receiptPut["Item"].(map[string]any)
			return summaryHTTPResponse(200, `{}`, nil), nil
		}
		test.Fatalf("unexpected call %s", performanceTarget(request))
		return nil, nil
	})
	first, err := repo.ApplyPerformanceBatch(test.Context(), "owner", model.PerformanceSourceSFDCTasks, performanceTaskRequest(0), performanceTestDigest)
	if err != nil || first.Revision != 1 || !first.Completed || first.Upserted != 1 || first.Removed != 1 {
		test.Fatalf("unexpected receipt: %+v %v", first, err)
	}
	if !strings.Contains(analysisCondition(test, receiptPut), "attribute_not_exists") {
		test.Fatal("receipt put must use attribute_not_exists(PK)")
	}
	ttl, _ := stored["pendingShareExpiresAt"].(map[string]any)["N"].(string)
	if ttl == "" {
		test.Fatal("receipt must expire")
	}
	again, err := repo.ApplyPerformanceBatch(test.Context(), "owner", model.PerformanceSourceSFDCTasks, performanceTaskRequest(0), performanceTestDigest)
	if err != nil || *again != *first {
		test.Fatalf("identical retry must return the saved receipt: %+v %v", again, err)
	}
	_, err = repo.ApplyPerformanceBatch(test.Context(), "owner", model.PerformanceSourceSFDCTasks, performanceTaskRequest(0), strings.Repeat("b", 64))
	if !errors.Is(err, ErrConditionFailed) {
		test.Fatalf("a reused batch ID with another digest must conflict: %v", err)
	}
}

func mustJSON(test *testing.T, value any) string {
	test.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		test.Fatal(err)
	}
	return string(encoded)
}

func TestPerformanceTaskTTL(test *testing.T) {
	pinPerformanceNow(test)
	var task, removal map[string]any
	repo := summarySDKRepo(func(request *http.Request) (*http.Response, error) {
		body := performanceBody(test, request)
		switch performanceTarget(request) {
		case "GetItem":
			return summaryHTTPResponse(200, `{}`, nil), nil
		case "TransactWriteItems":
			operations := body["TransactItems"].([]any)
			task = operations[1].(map[string]any)["Put"].(map[string]any)["Item"].(map[string]any)
			removal = operations[2].(map[string]any)["Delete"].(map[string]any)["Key"].(map[string]any)
			return summaryHTTPResponse(200, `{}`, nil), nil
		}
		return nil, errors.New("unexpected call")
	})
	if _, err := repo.ApplyPerformanceBatch(test.Context(), "owner", model.PerformanceSourceSFDCTasks, performanceTaskRequest(0), performanceTestDigest); err != nil {
		test.Fatal(err)
	}
	want := time.Date(2028, 1, 1, 0, 0, 0, 0, time.UTC).Unix()
	if got := task["pendingShareExpiresAt"].(map[string]any)["N"]; got != mustJSONNumber(want) {
		test.Fatalf("task TTL = %v, want %d", got, want)
	}
	if task["SK"].(map[string]any)["S"] != "PERFTASK#T1" || task["PK"].(map[string]any)["S"] != "USER#owner" {
		test.Fatalf("task keys: %v", task)
	}
	if removal["SK"].(map[string]any)["S"] != "PERFTASK#T2" {
		test.Fatalf("remove must delete by sort key: %v", removal)
	}
}

func mustJSONNumber(n int64) string { return mustJSONInt(n) }

func mustJSONInt(n int64) string {
	encoded, _ := json.Marshal(n)
	return string(encoded)
}

func TestPerformanceAssignmentRowsHaveNoTTL(test *testing.T) {
	pinPerformanceNow(test)
	var assignment map[string]any
	repo := summarySDKRepo(func(request *http.Request) (*http.Response, error) {
		body := performanceBody(test, request)
		if performanceTarget(request) == "GetItem" {
			return summaryHTTPResponse(200, `{}`, nil), nil
		}
		operations := body["TransactItems"].([]any)
		assignment = operations[1].(map[string]any)["Put"].(map[string]any)["Item"].(map[string]any)
		return summaryHTTPResponse(200, `{}`, nil), nil
	})
	req := model.PerformanceSyncRequest{BatchID: performanceTestBatch, Mutations: []model.PerformanceMutation{
		{Op: "upsert", Assignment: &model.SFDCAssignment{SFDCAccountID: "A1", Name: "Acme", Role: "activity"}},
	}}
	if _, err := repo.ApplyPerformanceBatch(test.Context(), "owner", model.PerformanceSourceSFDCAssignments, req, performanceTestDigest); err != nil {
		test.Fatal(err)
	}
	if _, found := assignment["pendingShareExpiresAt"]; found || assignment["SK"].(map[string]any)["S"] != "PERFASSIGN#A1" ||
		assignment["role"].(map[string]any)["S"] != "activity" {
		test.Fatalf("assignment row: %v", assignment)
	}
}

func TestApplyPerformanceBatchRejectsInvalid(test *testing.T) {
	pinPerformanceNow(test)
	repo := summarySDKRepo(func(request *http.Request) (*http.Response, error) {
		test.Fatalf("invalid batch reached DynamoDB: %s", performanceTarget(request))
		return nil, nil
	})
	mutate := func(edit func(*model.PerformanceSyncRequest)) model.PerformanceSyncRequest {
		req := performanceTaskRequest(0)
		edit(&req)
		return req
	}
	cases := map[string]struct {
		source string
		req    model.PerformanceSyncRequest
		digest string
	}{
		"unknown source": {"calendar", performanceTaskRequest(0), performanceTestDigest},
		"bad batch id":   {model.PerformanceSourceSFDCTasks, mutate(func(r *model.PerformanceSyncRequest) { r.BatchID = "x" }), performanceTestDigest},
		"bad digest":     {model.PerformanceSourceSFDCTasks, performanceTaskRequest(0), "abc"},
		"negative rev":   {model.PerformanceSourceSFDCTasks, performanceTaskRequest(-1), performanceTestDigest},
		"too many":       {model.PerformanceSourceSFDCTasks, mutate(func(r *model.PerformanceSyncRequest) { r.Mutations = make([]model.PerformanceMutation, 21) }), performanceTestDigest},
		"bad date":       {model.PerformanceSourceSFDCTasks, mutate(func(r *model.PerformanceSyncRequest) { r.Mutations[0].Task.ActivityDate = "2026-13-40" }), performanceTestDigest},
		"duplicate":      {model.PerformanceSourceSFDCTasks, mutate(func(r *model.PerformanceSyncRequest) { r.Mutations[1].ExternalID = "T1" }), performanceTestDigest},
		"wrong row kind": {model.PerformanceSourceSFDCAssignments, performanceTaskRequest(0), performanceTestDigest},
		"unknown op":     {model.PerformanceSourceSFDCTasks, mutate(func(r *model.PerformanceSyncRequest) { r.Mutations[1].Op = "merge" }), performanceTestDigest},
		"remove no id":   {model.PerformanceSourceSFDCTasks, mutate(func(r *model.PerformanceSyncRequest) { r.Mutations[1].ExternalID = "" }), performanceTestDigest},
	}
	for name, tc := range cases {
		if _, err := repo.ApplyPerformanceBatch(test.Context(), "owner", tc.source, tc.req, tc.digest); !errors.Is(err, ErrInvalidPerformanceBatch) {
			test.Errorf("%s: want ErrInvalidPerformanceBatch, got %v", name, err)
		}
	}
}

func TestPutAccountMappingKeepsManual(test *testing.T) {
	pinPerformanceNow(test)
	var put map[string]any
	reject := false
	repo := summarySDKRepo(func(request *http.Request) (*http.Response, error) {
		put = performanceBody(test, request)
		if reject {
			return summaryHTTPResponse(400, `{"__type":"com.amazonaws.dynamodb.v20120810#ConditionalCheckFailedException","message":"x"}`, nil), nil
		}
		return summaryHTTPResponse(200, `{}`, nil), nil
	})
	m := model.AccountMapping{SFDCAccountID: "A1", AccountID: "acct-1"}
	if err := repo.PutAccountMapping(test.Context(), "owner", m, true); err != nil {
		test.Fatal(err)
	}
	condition := analysisCondition(test, put)
	if !strings.Contains(condition, "attribute_not_exists") || !strings.Contains(condition, "OR") ||
		!strings.Contains(condition, "manual") || !strings.Contains(condition, `{"BOOL":false}`) {
		test.Fatalf("automatic put must be conditioned on not-manual: %s", condition)
	}
	item := put["Item"].(map[string]any)
	if item["SK"].(map[string]any)["S"] != "PERFMAP#A1" || item["manual"].(map[string]any)["BOOL"] != false {
		test.Fatalf("mapping item: %v", item)
	}
	if _, found := item["pendingShareExpiresAt"]; found {
		test.Fatal("mapping rows have no TTL")
	}
	m.Manual = true
	if err := repo.PutAccountMapping(test.Context(), "owner", m, false); err != nil {
		test.Fatal(err)
	}
	if _, found := put["ConditionExpression"]; found {
		test.Fatal("an explicit manual put must be unconditional")
	}
	reject = true
	if err := repo.PutAccountMapping(test.Context(), "owner", m, true); !errors.Is(err, ErrConditionFailed) {
		test.Fatalf("a kept manual mapping must report ErrConditionFailed: %v", err)
	}
}

func TestPerformanceListsPaginateAndFilter(test *testing.T) {
	pinPerformanceNow(test)
	pages := 0
	repo := summarySDKRepo(func(request *http.Request) (*http.Response, error) {
		body := performanceBody(test, request)
		if performanceTarget(request) != "Query" {
			test.Fatalf("unexpected call %s", performanceTarget(request))
		}
		task := func(id, date string, ttl int64) string {
			return `{"PK":{"S":"USER#owner"},"SK":{"S":"PERFTASK#` + id + `"},"userId":{"S":"owner"},"taskId":{"S":"` + id +
				`"},"sfdcAccountId":{"S":"A1"},"activityDate":{"S":"` + date + `"},"type":{"S":"Call"},"subject":{"S":"s"},"status":{"S":"Done"},"pendingShareExpiresAt":{"N":"` + mustJSONInt(ttl) + `"}}`
		}
		future := performanceNow().AddDate(1, 0, 0).Unix()
		pages++
		if _, again := body["ExclusiveStartKey"]; !again {
			return summaryHTTPResponse(200, `{"Items":[`+task("T1", "2026-03-04", future)+`,`+task("T0", "2025-12-31", future)+`],"LastEvaluatedKey":{"PK":{"S":"USER#owner"},"SK":{"S":"PERFTASK#T1"}}}`, nil), nil
		}
		return summaryHTTPResponse(200, `{"Items":[`+task("T3", "2026-06-01", future)+`,`+task("T4", "2026-06-02", 1)+`]}`, nil), nil
	})
	tasks, err := repo.ListPerformanceTasks(test.Context(), "owner", "2026-01-01", "2026-12-31")
	if err != nil || pages != 2 || len(tasks) != 2 || tasks[0].TaskID != "T1" || tasks[1].TaskID != "T3" {
		test.Fatalf("tasks must paginate, filter by date and drop expired rows: %+v pages=%d err=%v", tasks, pages, err)
	}
	if _, err := repo.ListPerformanceTasks(test.Context(), "owner", "bad", ""); !errors.Is(err, ErrInvalidPerformanceInput) {
		test.Fatalf("bad bound: %v", err)
	}
}

func TestPerformanceGoalsRoundTrip(test *testing.T) {
	pinPerformanceNow(test)
	var put map[string]any
	repo := summarySDKRepo(func(request *http.Request) (*http.Response, error) {
		body := performanceBody(test, request)
		switch performanceTarget(request) {
		case "PutItem":
			put = body["Item"].(map[string]any)
			return summaryHTTPResponse(200, `{}`, nil), nil
		case "GetItem":
			if put == nil {
				return summaryHTTPResponse(200, `{}`, nil), nil
			}
			return summaryHTTPResponse(200, `{"Item":`+mustJSON(test, put)+`}`, nil), nil
		}
		return nil, errors.New("unexpected call")
	})
	if goals, err := repo.GetPerformanceGoals(test.Context(), "owner", 2026); goals != nil || err != nil {
		test.Fatalf("missing goals: %v %v", goals, err)
	}
	if err := repo.PutPerformanceGoals(test.Context(), "owner", model.PerformanceGoals{Year: 2026, Text: "목표"}); err != nil {
		test.Fatal(err)
	}
	if put["SK"].(map[string]any)["S"] != "PERFGOAL#2026" ||
		put["pendingShareExpiresAt"].(map[string]any)["N"] != mustJSONInt(time.Date(2028, 1, 1, 0, 0, 0, 0, time.UTC).Unix()) {
		test.Fatalf("goals row: %v", put)
	}
	goals, err := repo.GetPerformanceGoals(test.Context(), "owner", 2026)
	if err != nil || goals.Text != "목표" || goals.UpdatedAt == "" {
		test.Fatalf("goals: %+v %v", goals, err)
	}
	tooLong := model.PerformanceGoals{Year: 2026, Text: strings.Repeat("가", 2667)} // 8,001 bytes
	if err := repo.PutPerformanceGoals(test.Context(), "owner", tooLong); !errors.Is(err, ErrInvalidPerformanceInput) {
		test.Fatalf("oversized goals: %v", err)
	}
}

func TestApplyPerformanceBatchAcceptsAccountlessTask(test *testing.T) {
	pinPerformanceNow(test)
	var stored map[string]any
	repo := summarySDKRepo(func(request *http.Request) (*http.Response, error) {
		body := performanceBody(test, request)
		switch performanceTarget(request) {
		case "GetItem":
			return summaryHTTPResponse(200, `{}`, nil), nil
		case "TransactWriteItems":
			stored = body["TransactItems"].([]any)[1].(map[string]any)["Put"].(map[string]any)["Item"].(map[string]any)
			return summaryHTTPResponse(200, `{}`, nil), nil
		case "Query":
			return summaryHTTPResponse(200, `{"Items":[`+mustJSON(test, stored)+`]}`, nil), nil
		}
		return nil, errors.New("unexpected call")
	})
	req := model.PerformanceSyncRequest{BatchID: performanceTestBatch, Mutations: []model.PerformanceMutation{
		{Op: "upsert", Task: &model.SFDCTask{TaskID: "T9", ActivityDate: "2026-05-05", Type: "Call", Subject: "s", Status: "Open"}},
	}}
	if _, err := repo.ApplyPerformanceBatch(test.Context(), "owner", model.PerformanceSourceSFDCTasks, req, performanceTestDigest); err != nil {
		test.Fatalf("accountless task must be accepted: %v", err)
	}
	if _, found := stored["sfdcAccountId"]; found {
		test.Fatalf("empty account must be stored without the attribute: %v", stored)
	}
	tasks, err := repo.ListPerformanceTasks(test.Context(), "owner", "", "")
	if err != nil || len(tasks) != 1 || tasks[0].TaskID != "T9" || tasks[0].SFDCAccountID != "" {
		test.Fatalf("list: %+v %v", tasks, err)
	}
}

func TestPerformanceRejectsAlreadyExpiredWrites(test *testing.T) {
	pinPerformanceNow(test)
	repo := summarySDKRepo(func(request *http.Request) (*http.Response, error) {
		test.Fatalf("expired write must be rejected before any call: %s", performanceTarget(request))
		return nil, errors.New("unexpected call")
	})
	if err := repo.PutPerformanceGoals(test.Context(), "owner", model.PerformanceGoals{Year: 2019, Text: "old"}); !errors.Is(err, ErrInvalidPerformanceInput) {
		test.Fatalf("expired goals: %v", err)
	}
	req := model.PerformanceSyncRequest{BatchID: performanceTestBatch, Mutations: []model.PerformanceMutation{
		{Op: "upsert", Task: &model.SFDCTask{TaskID: "T1", ActivityDate: "2019-12-31", Type: "Call", Subject: "s", Status: "Open"}},
	}}
	if _, err := repo.ApplyPerformanceBatch(test.Context(), "owner", model.PerformanceSourceSFDCTasks, req, performanceTestDigest); !errors.Is(err, ErrInvalidPerformanceBatch) {
		test.Fatalf("expired task: %v", err)
	}
}

func TestGetPerformanceGoalsHidesExpired(test *testing.T) {
	pinPerformanceNow(test)
	row := func(ttl int64) string {
		return `{"Item":{"PK":{"S":"USER#owner"},"SK":{"S":"PERFGOAL#2024"},"userId":{"S":"owner"},"year":{"N":"2024"},"text":{"S":"g"},"pendingShareExpiresAt":{"N":"` + mustJSONInt(ttl) + `"}}}`
	}
	ttl := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC).Unix()
	repo := summarySDKRepo(func(request *http.Request) (*http.Response, error) {
		return summaryHTTPResponse(200, row(ttl), nil), nil
	})
	if goals, err := repo.GetPerformanceGoals(test.Context(), "owner", 2024); goals != nil || err != nil {
		test.Fatalf("expired goals must read as not found: %+v %v", goals, err)
	}
	ttl = time.Date(2027, time.January, 1, 0, 0, 0, 0, time.UTC).Unix()
	if goals, err := repo.GetPerformanceGoals(test.Context(), "owner", 2024); err != nil || goals == nil {
		test.Fatalf("live goals must be returned: %+v %v", goals, err)
	}
}
