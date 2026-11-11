package repository

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
)

type operatorCall struct {
	op   string
	body map[string]any
}

// operatorWire answers GetItem with item (JSON, or "" for absent) and records
// every call; writeResponse answers the first write.
func operatorWire(t *testing.T, item, writeResponse string) (*CrawlerRepository, *[]operatorCall) {
	t.Helper()
	calls := &[]operatorCall{}
	client := dynamodb.New(dynamodb.Options{
		Region: "ap-northeast-2", Credentials: aws.AnonymousCredentials{}, RetryMaxAttempts: 1,
		HTTPClient: meetingListHTTPClientFunc(func(r *http.Request) (*http.Response, error) {
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			op := r.Header.Get("X-Amz-Target")
			op = op[strings.LastIndex(op, ".")+1:]
			*calls = append(*calls, operatorCall{op: op, body: body})
			status, resp := 200, `{}`
			switch {
			case op == "GetItem" && item != "":
				resp = `{"Item":` + item + `}`
			case op != "GetItem" && writeResponse != "":
				status, resp = 400, writeResponse
			}
			return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/x-amz-json-1.0"}},
				Body: io.NopCloser(strings.NewReader(resp))}, nil
		}),
	})
	return NewCrawlerRepository(client, "crawler-test"), calls
}

var seedSource = OperatorNewsSource{
	SourceID: "fss-kr", SourceName: "금융감독원", Aliases: []string{"Financial Supervisory Service"},
	NewsQueries: []string{"감독규정"}, SearchDomains: []string{"fss.or.kr", "yna.co.kr"},
}

const operatorItem = `{"PK":{"S":"CRAWLER#fss-kr"},"SK":{"S":"CONFIG"},"globalNewsSource":{"BOOL":true},
 "status":{"S":"disabled"},"sourceName":{"S":"금융감독원"},"aliases":{"L":[{"S":"Financial Supervisory Service"}]},
 "newsQueries":{"L":[{"S":"감독규정"}]},"searchDomains":{"L":[{"S":"fss.or.kr"},{"S":"yna.co.kr"}]},
 "subscribers":{"L":[]},"customUrls":{"NULL":true}}`

func TestOperatorSourceCreateIsConditionalGlobalAndActive(t *testing.T) {
	repo, calls := operatorWire(t, "", "")
	outcome, err := repo.UpsertOperatorNewsSource(context.Background(), seedSource, true)
	if err != nil || outcome != OperatorSourceCreated {
		t.Fatalf("outcome=%v err=%v", outcome, err)
	}
	if len(*calls) != 2 || (*calls)[0].op != "GetItem" || (*calls)[1].op != "PutItem" {
		t.Fatalf("calls: %+v", *calls)
	}
	if (*calls)[0].body["ConsistentRead"] != true {
		t.Fatal("the existing-row check must be strongly consistent")
	}
	put := (*calls)[1].body
	if cond := resolved(put, "ConditionExpression"); !strings.Contains(cond, "attribute_not_exists (PK)") {
		t.Fatalf("create must not overwrite: %s", cond)
	}
	item := put["Item"].(map[string]any)
	for name, want := range map[string]string{
		"globalNewsSource": `{"BOOL":true}`, "status": `{"S":"active"}`, "sourceId": `{"S":"fss-kr"}`,
		"PK": `{"S":"CRAWLER#fss-kr"}`, "SK": `{"S":"CONFIG"}`, "subscribers": `{"L":[]}`, "customUrls": `{"L":[]}`,
		"searchDomains": `{"L":[{"S":"fss.or.kr"},{"S":"yna.co.kr"}]}`,
	} {
		got, _ := json.Marshal(item[name])
		if string(got) != want {
			t.Errorf("%s = %s, want %s", name, got, want)
		}
	}
	for _, name := range []string{"ownerId", "userTiedAt", "accountId"} {
		if _, ok := item[name]; ok {
			t.Errorf("operator source must not carry %s", name)
		}
	}
}

func TestOperatorSourceDryRunOnlyReads(t *testing.T) {
	for _, item := range []string{"", strings.Replace(operatorItem, "감독규정", "검사", 1)} {
		repo, calls := operatorWire(t, item, "")
		if _, err := repo.UpsertOperatorNewsSource(context.Background(), seedSource, false); err != nil {
			t.Fatal(err)
		}
		if len(*calls) != 1 || (*calls)[0].op != "GetItem" {
			t.Fatalf("dry run wrote: %+v", *calls)
		}
	}
}

func TestOperatorSourceUnchangedAndUpdateKeepStatus(t *testing.T) {
	repo, calls := operatorWire(t, operatorItem, "")
	if outcome, err := repo.UpsertOperatorNewsSource(context.Background(), seedSource, true); err != nil || outcome != OperatorSourceUnchanged {
		t.Fatalf("outcome=%v err=%v", outcome, err)
	}
	if len(*calls) != 1 {
		t.Fatalf("unchanged source was written: %+v", *calls)
	}

	changed := seedSource
	changed.NewsQueries = []string{"감독규정", "제재"}
	changed.SearchDomains = nil
	repo, calls = operatorWire(t, operatorItem, "")
	if outcome, err := repo.UpsertOperatorNewsSource(context.Background(), changed, true); err != nil || outcome != OperatorSourceUpdated {
		t.Fatalf("outcome=%v err=%v", outcome, err)
	}
	update := (*calls)[1].body
	expr := resolved(update, "UpdateExpression")
	requireAll(t, "update", expr, "newsQueries", "aliases", "sourceName", "REMOVE searchDomains")
	if strings.Contains(expr, "status") || strings.Contains(expr, "globalNewsSource") {
		t.Fatalf("update must not change status or the global marker: %s", expr)
	}
	requireAll(t, "update condition", resolved(update, "ConditionExpression"),
		"globalNewsSource", "attribute_not_exists (userTiedAt)", "attribute_not_exists (accountId)", "sourceName")
}

func TestOperatorSourceRefusesUserOwnedRows(t *testing.T) {
	for name, item := range map[string]string{
		"unmarked":   strings.Replace(operatorItem, `"globalNewsSource":{"BOOL":true},`, "", 1),
		"user taint": strings.Replace(operatorItem, `"status"`, `"userTiedAt":{"S":"2026-11-10T00:00:00Z"},"status"`, 1),
		"owner":      strings.Replace(operatorItem, `"status"`, `"ownerId":{"S":"user-1"},"status"`, 1),
		"account":    strings.Replace(operatorItem, `"status"`, `"accountId":{"S":"acct"},"status"`, 1),
		"subscriber": strings.Replace(operatorItem, `"subscribers":{"L":[]}`, `"subscribers":{"L":[{"S":"u"}]}`, 1),
		"custom url": strings.Replace(operatorItem, `"customUrls":{"NULL":true}`, `"customUrls":{"L":[{"S":"https://x.example"}]}`, 1),
	} {
		repo, calls := operatorWire(t, item, "")
		if _, err := repo.UpsertOperatorNewsSource(context.Background(), seedSource, true); !errors.Is(err, ErrNotOperatorSource) {
			t.Errorf("%s: err=%v", name, err)
		}
		if len(*calls) != 1 {
			t.Errorf("%s: refused row was written", name)
		}
	}
}

func TestOperatorSourceConcurrentChangeIsConditionFailed(t *testing.T) {
	for _, item := range []string{"", strings.Replace(operatorItem, "감독규정", "검사", 1)} {
		repo, _ := operatorWire(t, item, conditionalFailed)
		if _, err := repo.UpsertOperatorNewsSource(context.Background(), seedSource, true); !errors.Is(err, ErrConditionFailed) {
			t.Fatalf("err=%v", err)
		}
	}
}
