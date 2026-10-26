package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"

	"github.com/ttobak/backend/internal/model"
)

// A news merge records the system actor as updatedBy; other merges leave it.
func TestMergeUpdatedByIsWrittenOnlyWhenSet(t *testing.T) {
	key := "0123456789abcdef0123456789abcdef"
	obs := &model.DictObservation{PK: model.PrefixDictCommon, SK: model.DictObsSK(key, model.DictObsSourceID(model.DictSourceNews, "aaaa1111")), TermKey: key,
		SourceKind: model.DictSourceNews, SourceRef: "aaaa1111", OwnerID: model.DictNewsActor}
	repo, calls := dictWire(t, nil)
	merge := DictMergeInput{PK: model.PrefixDictCommon, TermKey: key, Observation: obs, SeenAt: "now", ExpectedVersion: 1, UpdatedBy: model.DictNewsActor}
	if err := repo.MergeAutoObservation(context.Background(), merge); err != nil {
		t.Fatal(err)
	}
	merge.UpdatedBy = ""
	if err := repo.MergeAutoObservation(context.Background(), merge); err != nil {
		t.Fatal(err)
	}
	withActor := resolved(txItems(t, (*calls)[0])[1]["Update"].(map[string]any), "UpdateExpression")
	requireAll(t, "news merge", withActor, "updatedBy")
	if plain := resolved(txItems(t, (*calls)[1])[1]["Update"].(map[string]any), "UpdateExpression"); strings.Contains(plain, "updatedBy") {
		t.Fatalf("a merge without an actor must not touch updatedBy: %s", plain)
	}
}

func TestTermsNewsRowsNeedTheirKeys(t *testing.T) {
	repo := &DynamoDBRepository{}
	if err := repo.PutTermsNewsHistory(context.Background(), &model.TermsNewsHistory{TermKey: "k", ExpiresAt: 1}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("history without days: %v", err)
	}
	if err := repo.PutTermsNewsRun(context.Background(), &model.TermsNewsRun{Day: "2026-10-18", ExpiresAt: 1}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("run without a hash: %v", err)
	}
}

// A Common create or eviction by the news job carries its audit row in the
// same transaction; a cancelled audit put cancels the whole write.
func TestCommonAutoWritesCarryTheirAudit(t *testing.T) {
	key := "0123456789abcdef0123456789abcdef"
	term := &model.DictTerm{PK: model.PrefixDictCommon, SK: model.DictTermSK(key), TermKey: key, State: model.TermStateActive, Origin: model.TermOriginAuto, DisplayAs: "AlphaGrid", Version: 1}
	obs := &model.DictObservation{PK: model.PrefixDictCommon, SK: model.DictObsSK(key, model.DictObsSourceID(model.DictSourceNews, "aaaa1111")), TermKey: key,
		SourceKind: model.DictSourceNews, SourceRef: "aaaa1111", OwnerID: model.DictNewsActor}
	entry := func(action string) *model.DictTermLog {
		return &model.DictTermLog{PK: model.PrefixDictCommon, SK: model.DictLogSK(key, "t1"), TermKey: key, Actor: model.DictNewsActor, Action: action, At: "t1"}
	}
	repo, calls := dictWire(t, nil)
	if err := repo.CreateCommonAutoTerm(context.Background(), term, obs, entry(model.DictLogActionCreate)); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.EvictCommonAutoTerm(context.Background(), key, 1, entry(model.DictLogActionEvict)); err != nil {
		t.Fatal(err)
	}
	create := txItems(t, (*calls)[0])
	if len(create) != 3 || !strings.Contains(fmt.Sprint(create[2]), model.PrefixDictLog) || !strings.Contains(fmt.Sprint(create[2]), "create") {
		t.Fatalf("create transaction %v", create)
	}
	evict := txItems(t, (*calls)[1])
	if len(evict) != 2 || evict[0]["Delete"] == nil || !strings.Contains(fmt.Sprint(evict[1]), "evict") {
		t.Fatalf("evict transaction %v", evict)
	}
	requireAll(t, "evict guard", resolved(evict[0]["Delete"].(map[string]any), "ConditionExpression"), "userEditedAt", "origin", "version")

	for _, bad := range []*model.DictTermLog{nil, {PK: model.PrefixDictCommon, SK: model.DictLogSK("other", "t1"), TermKey: key, Actor: model.DictNewsActor}, {PK: model.PrefixDictCommon, SK: model.DictLogSK(key, "t1"), TermKey: key}} {
		if err := repo.CreateCommonAutoTerm(context.Background(), term, obs, bad); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("create without a matching audit row: %v", err)
		}
		if _, err := repo.EvictCommonAutoTerm(context.Background(), key, 1, bad); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("evict without a matching audit row: %v", err)
		}
	}
	user := *term
	user.PK = "USER#u1"
	if err := repo.CreateCommonAutoTerm(context.Background(), &user, obs, entry(model.DictLogActionCreate)); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("only Common rows: %v", err)
	}

	conflict, _ := dictWire(t, func(int, string, map[string]any) (int, string) {
		return 400, cancelled("None", "None", "ConditionalCheckFailed")
	})
	if err := conflict.CreateCommonAutoTerm(context.Background(), term, obs, entry(model.DictLogActionCreate)); !errors.Is(err, ErrDictTermConflict) {
		t.Fatalf("an existing audit SK is a retryable conflict: %v", err)
	}
}

// Only an operator-marked source with no owner, subscribers or custom URLs is
// global; everything else, including a missing CONFIG, is not.
func TestIsGlobalNewsSource(t *testing.T) {
	bool_ := func(v bool) map[string]any { return map[string]any{"BOOL": v} }
	list := func(items ...string) map[string]any {
		l := []any{}
		for _, i := range items {
			l = append(l, map[string]string{"S": i})
		}
		return map[string]any{"L": l}
	}
	cases := []struct {
		name string
		item map[string]any
		want bool
	}{
		{"missing config", nil, false},
		{"no marker", map[string]any{"ownerId": map[string]string{"S": ""}}, false},
		{"marker false", map[string]any{GlobalNewsSourceAttribute: bool_(false)}, false},
		{"marker as a string", map[string]any{GlobalNewsSourceAttribute: map[string]string{"S": "true"}}, false},
		{"marked, user-owned", map[string]any{GlobalNewsSourceAttribute: bool_(true), "ownerId": map[string]string{"S": "u1"}}, false},
		{"marked, subscribed", map[string]any{GlobalNewsSourceAttribute: bool_(true), "subscribers": list("u1")}, false},
		{"marked, custom URLs", map[string]any{GlobalNewsSourceAttribute: bool_(true), "customUrls": list("https://a.example")}, false},
		{"marked operator source", map[string]any{GlobalNewsSourceAttribute: bool_(true), "subscribers": list(), "customUrls": list(), "newsQueries": list("AlphaGrid")}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := dynamodb.New(dynamodb.Options{
				Region: "ap-northeast-2", Credentials: aws.AnonymousCredentials{}, RetryMaxAttempts: 1,
				HTTPClient: meetingListHTTPClientFunc(func(req *http.Request) (*http.Response, error) {
					if req.Header.Get("X-Amz-Target") != "DynamoDB_20120810.GetItem" {
						t.Fatal("source lookup must be a GetItem")
					}
					body := map[string]any{}
					if tc.item != nil {
						body["Item"] = tc.item
					}
					data, _ := json.Marshal(body)
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/x-amz-json-1.0"}},
						Body: io.NopCloser(strings.NewReader(string(data)))}, nil
				}),
			})
			got, err := NewCrawlerRepository(client, "t").IsGlobalNewsSource(context.Background(), "src")
			if err != nil || got != tc.want {
				t.Fatalf("got %v err %v, want %v", got, err, tc.want)
			}
		})
	}
}
