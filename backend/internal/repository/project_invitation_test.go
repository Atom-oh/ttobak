package repository

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/ttobak/backend/internal/model"
)

func projectInviteWire(t *testing.T, fn func(string, map[string]any) (int, string), control ...string) *DynamoDBRepository {
	t.Helper()
	client := dynamodb.New(dynamodb.Options{Region: "ap-northeast-2", Credentials: aws.AnonymousCredentials{}, RetryMaxAttempts: 1, HTTPClient: meetingListHTTPClientFunc(func(req *http.Request) (*http.Response, error) {
		var body map[string]any
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		var status int
		var text string
		key, _ := body["Key"].(map[string]any)
		pk, _ := key["PK"].(map[string]any)
		if strings.HasSuffix(req.Header.Get("X-Amz-Target"), ".GetItem") && pk["S"] == projectInvitationControlPK {
			status = 200
			text = "{}"
			if len(control) > 0 {
				text = control[0]
			}
		} else {
			status, text = fn(req.Header.Get("X-Amz-Target"), body)
		}
		return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/x-amz-json-1.0"}}, Body: io.NopCloser(strings.NewReader(text))}, nil
	})})
	return NewDynamoDBRepository(client, "test")
}
func inviteFixture() *model.PendingShare {
	return &model.PendingShare{PK: "PROJECT_INVITES#user@example.com", SK: "PENDING_PROJECT#project", Kind: model.PendingShareKindProject, ProjectID: "project", Email: "user@example.com", InvitedByUserID: "owner", InvitedCognitoSub: "recipient", CreatedAt: time.Now().UTC(), TTL: time.Now().Add(time.Hour).Unix()}
}

func projectCancellation(count int, failed ...int) string {
	reasons := make([]map[string]string, count)
	for index := range reasons {
		reasons[index] = map[string]string{"Code": "None"}
	}
	for _, index := range failed {
		reasons[index]["Code"] = "ConditionalCheckFailed"
	}
	payload, _ := json.Marshal(map[string]any{"__type": "TransactionCanceledException", "CancellationReasons": reasons})
	return string(payload)
}
func TestProjectInvitationQueuesBothRowsWithOwnerAndExistingMemberGuards(t *testing.T) {
	calls := 0
	r := projectInviteWire(t, func(target string, body map[string]any) (int, string) {
		calls++
		if !strings.HasSuffix(target, ".TransactWriteItems") {
			t.Fatal(target)
		}
		items := body["TransactItems"].([]any)
		if len(items) != 6 {
			t.Fatalf("expected owner, canonical, reverse, absent-member checks: %d", len(items))
		}
		owner := items[0].(map[string]any)["ConditionCheck"].(map[string]any)
		if condition := analysisCondition(t, owner); !strings.Contains(condition, "ownerUserId") || !strings.Contains(condition, "owner") || !strings.Contains(condition, "attribute_exists") {
			t.Fatal(condition)
		}
		canonical := items[1].(map[string]any)["Put"].(map[string]any)["Item"].(map[string]any)
		reverse := items[2].(map[string]any)["Put"].(map[string]any)["Item"].(map[string]any)
		if canonical["PK"].(map[string]any)["S"] != "PROJECT_INVITES#user@example.com" || reverse["PK"].(map[string]any)["S"] != "PROJECT#project" {
			t.Fatal("wrong partitions")
		}
		for _, field := range []string{"createdAt", "pendingShareExpiresAt", "invitedCognitoSub"} {
			a, _ := json.Marshal(canonical[field])
			b, _ := json.Marshal(reverse[field])
			if string(a) != string(b) {
				t.Fatalf("mismatched %s", field)
			}
		}
		member := items[3].(map[string]any)["ConditionCheck"].(map[string]any)
		if !strings.Contains(member["ConditionExpression"].(string), "attribute_not_exists") || member["Key"].(map[string]any)["SK"].(map[string]any)["S"] != "MEMBER#recipient" {
			t.Fatal("pending grant could resurrect an existing member")
		}
		return 200, "{}"
	})
	p := inviteFixture()
	p.Email = " User@Example.com "
	if err := r.PutPendingShare(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal(calls)
	}
}
func TestProjectInvitationMaterializationBindsIdentityExpiryAndVersion(t *testing.T) {
	r := projectInviteWire(t, func(_ string, body map[string]any) (int, string) {
		items := body["TransactItems"].([]any)
		if len(items) != 6 {
			t.Fatal("grant is not atomic with both queue rows")
		}
		canonical := items[2].(map[string]any)["Delete"].(map[string]any)
		condition := analysisCondition(t, canonical)
		for _, field := range []string{"createdAt", "invitedCognitoSub", "recipient", "pendingShareExpiresAt", "invitedByUserId", "owner", "projectId", "project"} {
			if !strings.Contains(condition, field) {
				t.Fatalf("missing %s: %s", field, condition)
			}
		}
		reverse := items[3].(map[string]any)["Delete"].(map[string]any)
		if c := analysisCondition(t, reverse); !strings.Contains(c, "createdAt") || !strings.Contains(c, "invitedCognitoSub") {
			t.Fatal(c)
		}
		return 200, "{}"
	})
	if ok, err := r.MaterializePendingProjectGrant(context.Background(), inviteFixture(), "recipient", "user@example.com"); err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
}
func TestProjectInvitationInvalidIdentityNeverWrites(t *testing.T) {
	r := projectInviteWire(t, func(string, map[string]any) (int, string) {
		t.Fatal("invalid invitation reached database")
		return 200, "{}"
	})
	for _, test := range []struct {
		name, id, email string
		expired         bool
	}{{"different sub", "recreated", "user@example.com", false}, {"different email", "recipient", "other@example.com", false}, {"expired", "recipient", "user@example.com", true}} {
		t.Run(test.name, func(t *testing.T) {
			p := inviteFixture()
			if test.expired {
				p.TTL = time.Now().Add(-time.Second).Unix()
			}
			if ok, err := r.MaterializePendingProjectGrant(context.Background(), p, test.id, test.email); err != nil || ok {
				t.Fatalf("ok=%v err=%v", ok, err)
			}
		})
	}
}
func TestProjectInvitationNewVersionRaceNeverCleansFreshGrant(t *testing.T) {
	calls := 0
	r := projectInviteWire(t, func(string, map[string]any) (int, string) {
		calls++
		return 400, projectCancellation(6, 2)
	})
	ok, err := r.MaterializePendingProjectGrant(context.Background(), inviteFixture(), "recipient", "user@example.com")
	if err != nil || ok || calls != 1 {
		t.Fatalf("must leave refreshed grant, ok=%v err=%v calls=%d", ok, err, calls)
	}
}
func TestProjectInvitationDeletedProjectOrExistingMemberRetiresOnlyObservedVersion(t *testing.T) {
	for _, failed := range []int{0, 1} {
		t.Run(string(rune('0'+failed)), func(t *testing.T) {
			calls := 0
			p := inviteFixture()
			r := projectInviteWire(t, func(_ string, body map[string]any) (int, string) {
				calls++
				if calls == 1 {
					return 400, projectCancellation(6, failed)
				}
				items := body["TransactItems"].([]any)
				if len(items) != 2 {
					t.Fatal("cleanup must remove both rows")
				}
				for _, item := range items {
					deletion := item.(map[string]any)["Delete"].(map[string]any)
					timestamp, identity := false, false
					for _, value := range deletion["ExpressionAttributeValues"].(map[string]any) {
						text, _ := value.(map[string]any)["S"].(string)
						timestamp = timestamp || text == p.CreatedAt.Format(time.RFC3339Nano)
						identity = identity || text == "recipient"
					}
					if !timestamp || !identity || deletion["ConditionExpression"] == "" {
						t.Fatal("unversioned cleanup", deletion)
					}
				}
				return 200, "{}"
			})
			if ok, err := r.MaterializePendingProjectGrant(context.Background(), p, "recipient", "user@example.com"); err != nil || !ok || calls != 2 {
				t.Fatalf("ok=%v err=%v calls=%d", ok, err, calls)
			}
		})
	}
}
func TestProjectInvitationRevokeChecksOwnerAtomically(t *testing.T) {
	for _, failed := range []int{0, 3} {
		store := projectInviteWire(t, func(_ string, body map[string]any) (int, string) {
			items := body["TransactItems"].([]any)
			if len(items) != 4 {
				t.Fatalf("revocation lacks an atomic membership guard: %d", len(items))
			}
			owner := analysisCondition(t, items[0].(map[string]any)["ConditionCheck"].(map[string]any))
			member := items[3].(map[string]any)["ConditionCheck"].(map[string]any)
			if !strings.Contains(owner, "ownerUserId") || !strings.Contains(owner, "owner") ||
				!strings.Contains(member["ConditionExpression"].(string), "attribute_not_exists") ||
				member["Key"].(map[string]any)["SK"].(map[string]any)["S"] != "MEMBER#recipient" {
				t.Fatal("revocation could hide an existing grant")
			}
			return 400, projectCancellation(4, failed)
		})
		if err := store.RevokePendingProjectShare(context.Background(), inviteFixture(), "owner"); !errors.Is(err, ErrConditionFailed) {
			t.Fatalf("err=%v", err)
		}
	}
}
func TestProjectPendingListRevalidatesCanonicalVersionAndKeepsCursor(t *testing.T) {
	for _, fresh := range []bool{false, true} {
		t.Run(map[bool]string{false: "stale reverse", true: "current"}[fresh], func(t *testing.T) {
			p := inviteFixture()
			reverse := *p
			reverse.PK = "PROJECT#project"
			reverse.SK = "PENDING_MEMBER#user@example.com"
			r := projectInviteWire(t, func(target string, body map[string]any) (int, string) {
				if strings.HasSuffix(target, ".Query") {
					if body["Limit"] != float64(25) || body["ConsistentRead"] != true {
						t.Fatal("unbounded or stale page")
					}
					item, _ := attributevalue.MarshalMap(reverse)
					out, _ := json.Marshal(map[string]any{"Items": []any{avMapForProjectTest(t, item)}, "LastEvaluatedKey": map[string]any{"PK": map[string]string{"S": "PROJECT#project"}, "SK": map[string]string{"S": "PENDING_MEMBER#user@example.com"}}})
					return 200, string(out)
				}
				if !fresh {
					p.CreatedAt = p.CreatedAt.Add(time.Second)
				}
				item, _ := attributevalue.MarshalMap(p)
				out, _ := json.Marshal(map[string]any{"Item": avMapForProjectTest(t, item)})
				return 200, string(out)
			})
			members, next, err := r.ListPendingProjectShares(context.Background(), "project", "")
			if err != nil {
				t.Fatal(err)
			}
			want := 0
			if fresh {
				want = 1
			}
			if len(members) != want || next == "" {
				t.Fatalf("members=%d next=%q", len(members), next)
			}
		})
	}
}

func avMapForProjectTest(t *testing.T, values map[string]types.AttributeValue) map[string]any {
	t.Helper()
	out := map[string]any{}
	for key, v := range values {
		switch a := v.(type) {
		case *types.AttributeValueMemberS:
			out[key] = map[string]string{"S": a.Value}
		case *types.AttributeValueMemberN:
			out[key] = map[string]string{"N": a.Value}
		default:
			t.Fatalf("unexpected fixture type %T", v)
		}
	}
	return out
}
func TestProjectPendingInvalidCursorDoesNotReadOtherPartitions(t *testing.T) {
	r := projectInviteWire(t, func(string, map[string]any) (int, string) {
		t.Fatal("invalid cursor queried database")
		return 200, "{}"
	})
	for _, cursor := range []string{"!", base64.RawURLEncoding.EncodeToString([]byte("MEMBER#private")), strings.Repeat("x", 513)} {
		if _, _, err := r.ListPendingProjectShares(context.Background(), "project", cursor); !errors.Is(err, ErrInvalidCursor) {
			t.Fatalf("err=%v", err)
		}
	}
}

func TestProjectUserQueueIsBoundedAndCursorCannotChooseAnotherUser(t *testing.T) {
	cursor := base64.RawURLEncoding.EncodeToString([]byte("PENDING_PROJECT#earlier"))
	r := projectInviteWire(t, func(target string, body map[string]any) (int, string) {
		if !strings.HasSuffix(target, ".Query") || body["Limit"] != float64(25) || body["ConsistentRead"] != true {
			t.Fatal("unbounded canonical query")
		}
		key := body["ExclusiveStartKey"].(map[string]any)
		if key["PK"].(map[string]any)["S"] != "PROJECT_INVITES#user@example.com" || key["SK"].(map[string]any)["S"] != "PENDING_PROJECT#earlier" {
			t.Fatal(key)
		}
		return 200, `{"Items":[],"LastEvaluatedKey":{"PK":{"S":"PROJECT_INVITES#user@example.com"},"SK":{"S":"PENDING_PROJECT#later"}}}`
	})
	rows, next, err := r.ListPendingProjectSharesForUser(context.Background(), "user@example.com", cursor)
	if err != nil || len(rows) != 0 || next == "" {
		t.Fatalf("rows=%v next=%q err=%v", rows, next, err)
	}
	if _, _, err := r.ListPendingProjectSharesForUser(context.Background(), "user@example.com", base64.RawURLEncoding.EncodeToString([]byte("OTHER#user"))); !errors.Is(err, ErrInvalidCursor) {
		t.Fatal(err)
	}
}
func TestLegacyProjectAddCannotCoexistWithPendingGrant(t *testing.T) {
	r := projectInviteWire(t, func(_ string, body map[string]any) (int, string) {
		items := body["TransactItems"].([]any)
		if len(items) != 4 {
			t.Fatal(len(items))
		}
		pending := items[2].(map[string]any)["ConditionCheck"].(map[string]any)
		if !strings.Contains(pending["ConditionExpression"].(string), "attribute_not_exists") || pending["Key"].(map[string]any)["PK"].(map[string]any)["S"] != "PROJECT_INVITES#user@example.com" {
			t.Fatal(pending)
		}
		return 400, projectCancellation(4, 2)
	})
	if err := r.PutRegisteredProjectMember(context.Background(), "owner", "project", "recipient", "user@example.com"); !errors.Is(err, ErrConditionFailed) {
		t.Fatal(err)
	}
}

func TestProjectFenceStopsQueueAndPinsEpochAcrossResume(t *testing.T) {
	paused := projectInviteWire(t, func(string, map[string]any) (int, string) { t.Fatal("paused queue reached a write"); return 200, "{}" }, `{"Item":{"enabled":{"BOOL":false},"revision":{"S":"paused"}}}`)
	if err := paused.PutPendingShare(context.Background(), inviteFixture()); !errors.Is(err, ErrProjectInvitationsPaused) {
		t.Fatal(err)
	}
	active := projectInviteWire(t, func(_ string, body map[string]any) (int, string) {
		items := body["TransactItems"].([]any)
		check := items[5].(map[string]any)["ConditionCheck"].(map[string]any)
		condition := analysisCondition(t, check)
		if !strings.Contains(condition, "old-epoch") || !strings.Contains(condition, "revision") {
			t.Fatal("late queue is not generation-bound", condition)
		}
		return 400, `{"__type":"TransactionCanceledException","CancellationReasons":[{"Code":"None"},{"Code":"None"},{"Code":"None"},{"Code":"None"},{"Code":"None"},{"Code":"ConditionalCheckFailed"}]}`
	}, `{"Item":{"enabled":{"BOOL":true},"revision":{"S":"old-epoch"}}}`)
	if err := active.PutPendingShare(context.Background(), inviteFixture()); !errors.Is(err, ErrProjectInvitationsPaused) {
		t.Fatal(err)
	}
}
func TestProjectFenceAlsoBlocksDeferredGrantPublication(t *testing.T) {
	r := projectInviteWire(t, func(_ string, body map[string]any) (int, string) {
		items := body["TransactItems"].([]any)
		condition := analysisCondition(t, items[5].(map[string]any)["ConditionCheck"].(map[string]any))
		if !strings.Contains(condition, "enabled") || !strings.Contains(condition, "revision") {
			t.Fatal(condition)
		}
		return 400, `{"__type":"TransactionCanceledException","CancellationReasons":[{"Code":"None"},{"Code":"None"},{"Code":"None"},{"Code":"None"},{"Code":"None"},{"Code":"ConditionalCheckFailed"}]}`
	})
	if ok, err := r.MaterializePendingProjectGrant(context.Background(), inviteFixture(), "recipient", "user@example.com"); ok || !errors.Is(err, ErrProjectInvitationsPaused) {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
}
func TestProjectControlUpdateIsRevisionConditional(t *testing.T) {
	r := projectInviteWire(t, func(target string, body map[string]any) (int, string) {
		if !strings.HasSuffix(target, ".UpdateItem") || !strings.Contains(analysisCondition(t, body), "old-control") {
			t.Fatal(body)
		}
		return 400, `{"__type":"ConditionalCheckFailedException"}`
	})
	if _, err := r.SetProjectInvitationControl(context.Background(), "old-control", false); !errors.Is(err, ErrConditionFailed) {
		t.Fatal(err)
	}
}

func TestProjectMemberRemovalInvalidatesAllEmailInvitations(t *testing.T) {
	r := projectInviteWire(t, func(_ string, body map[string]any) (int, string) {
		items := body["TransactItems"].([]any)
		if len(items) != 2 {
			t.Fatal(items)
		}
		guard := items[1].(map[string]any)["Delete"].(map[string]any)["Key"].(map[string]any)
		if guard["SK"].(map[string]any)["S"] != "INVITE_SUB#recipient" {
			t.Fatal(guard)
		}
		return 200, "{}"
	})
	if err := r.DeleteProjectMember(context.Background(), "project", "recipient"); err != nil {
		t.Fatal(err)
	}
}
func TestExpiredProjectCleanupReportsConcurrentRefresh(t *testing.T) {
	r := projectInviteWire(t, func(string, map[string]any) (int, string) {
		return 400, projectCancellation(2, 0)
	})
	if err := r.DeletePendingProjectShareIfMatch(context.Background(), inviteFixture()); !errors.Is(err, ErrConditionFailed) {
		t.Fatal(err)
	}
}

func TestProjectInvitationSubjectIntegration(t *testing.T) {
	table := os.Getenv("TTOBAK_PROJECT_INVITATION_TEST_TABLE")
	if table == "" {
		t.Skip("disposable integration table not requested")
	}
	if !regexp.MustCompile(`^ttobak-onboarding-regression-[a-f0-9]{12}$`).MatchString(table) {
		t.Fatal("unsafe test table")
	}
	account := os.Getenv("TTOBAK_PROJECT_INVITATION_TEST_ACCOUNT")
	if account == "" {
		t.Fatal("expected account is required")
	}
	ctx := context.Background()
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion("ap-northeast-2"))
	if err != nil {
		t.Fatal(err)
	}
	who, err := sts.NewFromConfig(cfg).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil || aws.ToString(who.Account) != account {
		t.Fatal("unexpected integration identity")
	}
	client := dynamodb.NewFromConfig(cfg)
	repo := NewDynamoDBRepository(client, table)
	_, err = client.PutItem(ctx, &dynamodb.PutItemInput{TableName: aws.String(table), Item: map[string]types.AttributeValue{"PK": &types.AttributeValueMemberS{Value: "PROJECT#integration"}, "SK": &types.AttributeValueMemberS{Value: "CONFIG"}, "ownerUserId": &types.AttributeValueMemberS{Value: "owner"}}})
	if err != nil {
		t.Fatal(err)
	}
	invite := func(email string) *model.PendingShare {
		p := &model.PendingShare{Kind: model.PendingShareKindProject, ProjectID: "integration", Email: email, InvitedByUserID: "owner", InvitedCognitoSub: "same-sub"}
		if err := repo.PutPendingShare(ctx, p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	old := invite("old@example.test")
	newer := invite("new@example.test")
	if ok, err := repo.MaterializePendingProjectGrant(ctx, newer, "same-sub", "new@example.test"); err != nil || !ok {
		t.Fatalf("new grant failed: %v", err)
	}
	if err := repo.RevokePendingProjectShare(ctx, old, "owner"); !errors.Is(err, ErrConditionFailed) {
		t.Fatalf("stale email revocation hid an existing member: %v", err)
	}
	if err := repo.DeleteProjectMember(ctx, "integration", "same-sub"); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.MaterializePendingProjectGrant(ctx, old, "same-sub", "old@example.test"); err != nil {
		t.Fatal(err)
	}
	member, err := repo.GetProjectMember(ctx, "integration", "same-sub")
	if err != nil || member != nil {
		t.Fatalf("old email restored removed access: %+v %v", member, err)
	}
	before := invite("refresh@example.test")
	after := invite("refresh@example.test")
	if err := repo.DeletePendingProjectShareIfMatch(ctx, before); !errors.Is(err, ErrConditionFailed) {
		t.Fatalf("refresh conflict hidden: %v", err)
	}
	current, err := repo.GetPendingShare(ctx, after.Email, after.SK)
	if err != nil || current == nil || !current.CreatedAt.Equal(after.CreatedAt) {
		t.Fatal("refreshed invitation was removed")
	}
}
