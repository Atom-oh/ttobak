package repository

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/smithy-go"
	"github.com/ttobak/backend/internal/model"
)

func privacyWriterWire(t *testing.T, rows map[string]interface{}, write func(string, map[string]any) (int, string)) *DynamoDBRepository {
	t.Helper()
	client := dynamodb.New(dynamodb.Options{Region: "ap-northeast-2", Credentials: aws.AnonymousCredentials{}, RetryMaxAttempts: 1,
		HTTPClient: meetingListHTTPClientFunc(func(req *http.Request) (*http.Response, error) {
			var body map[string]any
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			status, reply := 200, `{}`
			target := req.Header.Get("X-Amz-Target")
			if strings.HasSuffix(target, ".GetItem") {
				key := body["Key"].(map[string]any)
				name := key["PK"].(map[string]any)["S"].(string) + "/" + key["SK"].(map[string]any)["S"].(string)
				if row := rows[name]; row != nil {
					attrs, err := attributevalue.MarshalMap(row)
					if err != nil {
						t.Fatal(err)
					}
					data, err := attributevalue.MarshalMapJSON(attrs)
					if err != nil {
						t.Fatalf("marshal fixture: %v", err)
					}
					reply = `{"Item":` + string(data) + `}`
				}
			} else if strings.HasSuffix(target, ".Query") {
				reply = `{"Items":[]}`
			} else if write != nil {
				status, reply = write(target, body)
			}
			return summaryHTTPResponse(status, reply, nil), nil
		})})
	return NewDynamoDBRepository(client, "privacy-test")
}

func privacyWriterEnabled(t *testing.T) {
	t.Helper()
	t.Setenv("SUMMARY_PRIVACY_WRITER_ENABLED", "1")
}

func privacyWriterFences(t *testing.T, condition, format string, fields []string, args ...any) {
	t.Helper()
	for _, field := range fields {
		if !strings.Contains(condition, field) {
			t.Fatalf(format, append(args, field)...)
		}
	}
}

func privacyStamp(parent *model.Meeting, state, generation string) {
	parent.SummaryPrivacy = &model.SummaryPrivacy{Version: 1, State: state,
		GenerationID: generation, ContentSHA256: model.SummaryPrivacyHash(parent.Content)}
}

func privacyParentRows(parent *model.Meeting) map[string]interface{} {
	return map[string]interface{}{"USER#owner/MEETING#meeting": parent}
}

func privacyParent() *model.Meeting {
	return &model.Meeting{PK: "USER#owner", SK: "MEETING#meeting", UserID: "owner",
		MeetingID: "meeting", EntityType: "MEETING", Status: model.StatusDone}
}

func TestAllNewGrantPathsRejectUnknownAndPrivateDerivation(t *testing.T) {
	privacyWriterEnabled(t)
	for _, private := range []bool{false, true} {
		for _, path := range []string{"direct", "pending", "materialized", "account"} {
			t.Run(path+map[bool]string{false: "-unknown", true: "-private"}[private], func(t *testing.T) {
				parent := privacyParent()
				parent.Content = "PRIVATE_PREDECESSOR_DERIVATION"
				if private {
					privacyStamp(parent, model.SummaryPrivacyPrivate, "private")
				}
				rows := map[string]interface{}{"USER#owner/MEETING#meeting": parent,
					"ACCOUNT#account/MEMBER#owner": &model.AccountMember{AccountID: "account", UserID: "owner"}}
				repo := privacyWriterWire(t, rows, func(string, map[string]any) (int, string) {
					t.Fatal("unsafe grant reached a write")
					return 200, `{}`
				})
				var err error
				pending := &model.PendingShare{Kind: model.PendingShareKindMeeting, MeetingID: "meeting",
					InvitedByUserID: "owner", Email: "reader@example.test"}
				switch path {
				case "direct":
					_, err = repo.CreateShare(context.Background(), "meeting", "owner", "", "reader", "", "read", "")
				case "pending":
					err = repo.PutPendingShare(context.Background(), pending)
				case "materialized":
					done, e := repo.MaterializePendingMeetingGrant(context.Background(), pending, "reader", pending.Email)
					if done || e != nil {
						t.Fatalf("unsafe invitation must remain pending: %v %v", done, e)
					}
					return
				case "account":
					err = repo.UpdateMeetingFields(context.Background(), "owner", "meeting",
						map[string]interface{}{"accountId": "account", "sharedToAccount": true})
				}
				if !errors.Is(err, ErrSummarySharingUnsafe) {
					t.Fatalf("unsafe %s grant was not rejected: %v", path, err)
				}
			})
		}
	}
}

func TestEveryNewGrantFencesParentAndRetainsInviteOnParentRace(t *testing.T) {
	privacyWriterEnabled(t)
	for _, path := range []string{"direct", "pending", "materialized", "account-cache"} {
		t.Run(path, func(t *testing.T) {
			parent := privacyParent()
			parent.AccountID, parent.SharedToAccount = "account", path == "account-cache"
			writes := 0
			repo := privacyWriterWire(t, privacyParentRows(parent),
				func(target string, body map[string]any) (int, string) {
					writes++
					if !strings.HasSuffix(target, ".TransactWriteItems") {
						t.Fatal("grant published without a transaction")
					}
					ops := body["TransactItems"].([]any)
					index := 0
					switch path {
					case "direct":
						index = 2
					case "account-cache":
						index = 3
					}
					update := ops[index].(map[string]any)["Update"].(map[string]any)
					condition := analysisCondition(t, update)
					privacyWriterFences(t, condition, "%s grant lost parent fence %s", []string{"authorizationRevision", "summaryPrivacy", "content", "sharedToAccount", "linkedMeetingIds"}, path)
					if !strings.Contains(update["UpdateExpression"].(string), "ADD ") {
						t.Fatal("grant did not advance authorization revision")
					}
					return 400, projectCancellation(len(ops), index)
				})
			pending := &model.PendingShare{Kind: model.PendingShareKindMeeting, MeetingID: "meeting", SK: "PENDING_MEETING#meeting",
				Email: "reader@example.test", InvitedByUserID: "owner", CreatedAt: time.Now().UTC()}
			var err error
			switch path {
			case "direct":
				_, err = repo.CreateShare(context.Background(), "meeting", "owner", "", "reader", "", "read", "")
			case "pending":
				err = repo.PutPendingShare(context.Background(), pending)
			case "materialized":
				done, e := repo.MaterializePendingMeetingGrant(context.Background(), pending, "reader", pending.Email)
				if done || e != nil || writes != 1 {
					t.Fatalf("parent CAS race consumed invite: done=%v writes=%d err=%v", done, writes, e)
				}
				return
			case "account-cache":
				_, err = repo.CreateShareIfMember(context.Background(), "meeting", "owner", "", "account", "reader", "", "read")
			}
			if !errors.Is(err, ErrConditionFailed) || writes != 1 {
				t.Fatalf("%s parent race was classified as successful grant: %v writes=%d", path, err, writes)
			}
		})
	}
}

func TestAccountPublicationFencesCanonicalMembershipAndParent(t *testing.T) {
	privacyWriterEnabled(t)
	parent := privacyParent()
	rows := map[string]interface{}{"USER#owner/MEETING#meeting": parent,
		"ACCOUNT#account/MEMBER#owner": &model.AccountMember{AccountID: "account", UserID: "owner", AddedAt: time.Now().UTC(), SummaryPrivacyVersion: 1}}
	writes := 0
	repo := privacyWriterWire(t, rows, func(target string, body map[string]any) (int, string) {
		writes++
		if !strings.HasSuffix(target, ".TransactWriteItems") {
			t.Fatal("canonical publication must fence membership atomically")
		}
		ops := body["TransactItems"].([]any)
		member := ops[0].(map[string]any)["ConditionCheck"].(map[string]any)
		privacyWriterFences(t, analysisCondition(t, member), "missing canonical membership identity %s", []string{"addedAt", "summaryPrivacyVersion", "accountId", "userId"})
		meeting := ops[1].(map[string]any)["Update"].(map[string]any)
		privacyWriterFences(t, analysisCondition(t, meeting), "missing parent publication fence %s", []string{"authorizationRevision", "summaryPrivacy", "content", "linkedMeetingIds", "sharedToAccount"})
		return 400, projectCancellation(2, 0)
	})
	err := repo.UpdateMeetingFields(context.Background(), "owner", "meeting",
		map[string]interface{}{"accountId": "account", "sharedToAccount": true})
	if !errors.Is(err, ErrConditionFailed) || writes != 1 || parent.SharedToAccount {
		t.Fatalf("removed/re-added owner membership granted publication: %v writes=%d", err, writes)
	}
}

func TestBatchLinkedPublicationPinsPredecessorBytesAndAuthorizationRevision(t *testing.T) {
	privacyWriterEnabled(t)
	parent := privacyParent()
	parent.LinkedMeetingIDs, parent.TranscriptA = []string{"predecessor"}, "own raw transcript"
	source := &model.Meeting{PK: "USER#owner", SK: "MEETING#predecessor", UserID: "owner",
		MeetingID: "predecessor", Title: "before", Content: "PRIVATE_SOURCE", ActionItems: "[]"}
	rows := map[string]interface{}{"USER#owner/MEETING#meeting": parent, "USER#owner/MEETING#predecessor": source}
	repo := privacyWriterWire(t, rows, func(_ string, body map[string]any) (int, string) {
		ops := body["TransactItems"].([]any)
		if len(ops) != 2 {
			t.Fatal("linked predecessor is not part of publication transaction")
		}
		meeting := ops[0].(map[string]any)["Update"].(map[string]any)
		if !strings.Contains(analysisCondition(t, meeting), "authorizationRevision") {
			t.Fatal("a grant created during generation would not invalidate publication")
		}
		check := ops[1].(map[string]any)["ConditionCheck"].(map[string]any)
		privacyWriterFences(t, analysisCondition(t, check), "predecessor changed during generation was not pinned: %s", []string{"userId", "title", "before", "content", "PRIVATE_SOURCE", "actionItems", "summaryPrivacy"})
		return 400, projectCancellation(2, 1)
	})
	snapshot, err := repo.CaptureMeetingSummary(context.Background(), "owner", "meeting")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.BindLinkedSummarySources(context.Background(), snapshot); err != nil || len(snapshot.LinkedSources) != 1 {
		t.Fatalf("pre-model source binding failed: %+v %v", snapshot.LinkedSources, err)
	}
	source.Content = "edited while model was running"
	if err := repo.SaveMeetingSummary(context.Background(), snapshot, "late private summary", ""); !errors.Is(err, ErrConditionFailed) {
		t.Fatalf("late linked generation published: %v", err)
	}
	if parent.Content != "" {
		t.Fatal("conflict altered current parent content")
	}
}

func TestDerivativePublicationRejectsResetWithSameContent(t *testing.T) {
	privacyWriterEnabled(t)
	parent := privacyParent()
	parent.Content = "same summary"
	privacyStamp(parent, model.SummaryPrivacyPrivate, "before")
	repo := privacyWriterWire(t, privacyParentRows(parent), func(target string, body map[string]any) (int, string) {
		if !strings.HasSuffix(target, ".UpdateItem") {
			t.Fatal("unexpected derivative write")
		}
		condition := analysisCondition(t, body)
		if !strings.Contains(condition, "before") || !strings.Contains(condition, "summaryPrivacy") ||
			!strings.Contains(condition, "actionItems") || !strings.Contains(condition, "authorizationRevision") {
			t.Fatalf("derivative source snapshot was not retained: %s", condition)
		}
		return 400, `{"__type":"ConditionalCheckFailedException"}`
	})
	snapshot, err := repo.CaptureSummaryPrivacy(context.Background(), "owner", "meeting")
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"tags", "sentiment", "insights"} {
		if err := repo.PublishSummaryDerivatives(context.Background(), snapshot, map[string]interface{}{field: "late private output"}); !errors.Is(err, ErrConditionFailed) {
			t.Fatalf("late %s publication did not reject: %v", field, err)
		}
	}
}

func privacyWriterSetValue(t *testing.T, op map[string]any, field string) map[string]any {
	t.Helper()
	for alias, name := range op["ExpressionAttributeNames"].(map[string]any) {
		if name == field {
			match := regexp.MustCompile(regexp.QuoteMeta(alias) + `\s*=\s*(:\w+)`).FindStringSubmatch(op["UpdateExpression"].(string))
			if len(match) == 2 {
				return op["ExpressionAttributeValues"].(map[string]any)[match[1]].(map[string]any)
			}
		}
	}
	t.Fatalf("missing SET %s", field)
	return nil
}

func TestSharingSafeModeAndResetArePersistedAtomically(t *testing.T) {
	privacyWriterEnabled(t)
	parent := privacyParent()
	parent.Content, parent.Notes = "PRIVATE_OLD_SUMMARY", "saved human memo"
	parent.ActionItems = `[{"id":"keep-completed-id","text":"PRIVATE_OLD_TASK","completed":true}]`
	parent.LiveSummary, parent.Insights, parent.Sentiment, parent.Tags = "PRIVATE_DRAFT", "PRIVATE_INSIGHT", "private", []string{"private"}
	rows := privacyParentRows(parent)
	now := time.Now().UTC()
	state := &model.ResummaryState{RunID: "run", OwnerID: "owner", RequestedBy: "owner",
		Status: model.AnalysisQueued, SharingSafe: true, SourceHash: "hash", LeaseUntil: now.Add(time.Minute).UnixMilli()}
	phase := "queue"
	repo := privacyWriterWire(t, rows, func(_ string, body map[string]any) (int, string) {
		ops := body["TransactItems"].([]any)
		if phase == "queue" {
			run := ops[len(ops)-1].(map[string]any)["Update"].(map[string]any)
			if privacyWriterSetValue(t, run, "sharingSafe")["BOOL"] != true {
				t.Fatal("sharing-safe flag was lost before async delivery")
			}
			return 200, `{}`
		}
		if len(ops) != 4 {
			t.Fatalf("reset must atomically publish parent/run and delete action/sim state: %d", len(ops))
		}
		meeting := ops[0].(map[string]any)["Update"].(map[string]any)
		for _, field := range []string{"actionItems", "insights", "liveSummary", "tags", "sentiment", "insightsRevision", "actionItemContinuity"} {
			privacyWriterSetValue(t, meeting, field)
		}
		privacy := privacyWriterSetValue(t, meeting, "summaryPrivacy")["M"].(map[string]any)
		if privacy["state"].(map[string]any)["S"] != model.SummaryPrivacySafe {
			t.Fatal("reset did not publish safe provenance")
		}
		continuity, _ := json.Marshal(privacyWriterSetValue(t, meeting, "actionItemContinuity"))
		if strings.Contains(string(continuity), "PRIVATE_OLD_TASK") || !strings.Contains(string(continuity), "keep-completed-id") {
			t.Fatal("continuity either copied private prose or lost completion identity")
		}
		for i, sk := range []string{model.ActionAnalysisSK, model.PrefixSimRun} {
			deleted := ops[i+2].(map[string]any)["Delete"].(map[string]any)
			if deleted["Key"].(map[string]any)["SK"].(map[string]any)["S"] != sk {
				t.Fatal("reset left a stale derived worker publication row")
			}
		}
		if !strings.Contains(analysisCondition(t, ops[1].(map[string]any)["Update"].(map[string]any)), "sharingSafe") {
			t.Fatal("completion lost the owner-confirmed operation mode")
		}
		return 400, projectCancellation(4, 0)
	})
	snapshot, err := repo.CaptureResummary(context.Background(), "owner", "meeting", "owner")
	if err != nil {
		t.Fatal(err)
	}
	snapshot.SharingSafe = true
	if err := repo.QueueResummary(context.Background(), snapshot, nil, state); err != nil {
		t.Fatal(err)
	}
	state.Status = model.AnalysisRunning
	phase = "complete"
	err = repo.CompleteResummary(context.Background(), snapshot, state, "fresh own evidence", "", "hash", now)
	if !errors.Is(err, ErrConditionFailed) || parent.Content != "PRIVATE_OLD_SUMMARY" || parent.ActionItems == "[]" {
		t.Fatalf("failed reset altered old output: %v", err)
	}
}

func TestNewMembershipStampAndRoleUpdateDoNotGrandfatherReaddedMembers(t *testing.T) {
	privacyWriterEnabled(t)
	member := &model.AccountMember{PK: "ACCOUNT#account", SK: "MEMBER#reader", AccountID: "account", UserID: "reader",
		AddedAt: time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)}
	repo := privacyWriterWire(t, nil, func(target string, body map[string]any) (int, string) {
		if strings.HasSuffix(target, ".PutItem") {
			item := body["Item"].(map[string]any)
			if item["summaryPrivacyVersion"].(map[string]any)["N"] != "1" ||
				item["addedAt"].(map[string]any)["S"] == member.AddedAt.Format(time.RFC3339Nano) ||
				!strings.Contains(body["ConditionExpression"].(string), "attribute_not_exists") {
				t.Fatal("new/re-added membership retained old eligibility")
			}
		} else {
			names := body["ExpressionAttributeNames"].(map[string]any)
			for _, name := range names {
				if name == "addedAt" || name == "summaryPrivacyVersion" {
					t.Fatal("role update rewrote server-minted membership provenance")
				}
			}
		}
		return 200, `{}`
	})
	if err := repo.PutMember(context.Background(), member); err != nil {
		t.Fatal(err)
	}
	if member.SummaryPrivacyVersion != 0 || member.AddedAt.Year() != 2000 {
		t.Fatal("stamp mutated caller membership")
	}
	if err := repo.UpdateMemberRole(context.Background(), "account", "reader", "editor"); err != nil {
		t.Fatal(err)
	}
}

func TestAccountInsightCopyCannotRepublishOldGenerationOrRevision(t *testing.T) {
	privacyWriterEnabled(t)
	for _, race := range []string{"generation", "revision", "after-read"} {
		t.Run(race, func(t *testing.T) {
			parent := privacyParent()
			parent.AccountID, parent.SharedToAccount = "account", true
			parent.Date = time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
			parent.Content, parent.InsightsRevision = "summary", "current-insight"
			privacyStamp(parent, model.SummaryPrivacySafe, "current-generation")
			parent.Insights = `[{"type":"risk","text":"current insight"}]`
			insight := model.AccountInsight{PK: "ACCOUNT#account", SK: "INSIGHT#2026-10-13T12:00:00Z#meeting#0",
				AccountID: "account", SourceType: "meeting", SourceID: "meeting", SourceUserID: "owner",
				SummaryGeneration: "current-generation", InsightsRevision: "current-insight",
				OccurredAt: parent.Date, Type: "risk", Text: "current insight"}
			if race == "generation" {
				insight.SummaryGeneration = "old-generation"
			}
			if race == "revision" {
				insight.InsightsRevision = "old-insight"
			}
			writes := 0
			repo := privacyWriterWire(t, privacyParentRows(parent),
				func(target string, body map[string]any) (int, string) {
					writes++
					if race != "after-read" {
						t.Fatal("stale copy reached write instead of canonical validation")
					}
					if !strings.HasSuffix(target, ".TransactWriteItems") {
						t.Fatal("insight replacement contains an unfenced write")
					}
					ops := body["TransactItems"].([]any)
					if len(ops) != 2 {
						t.Fatal("insight replacement missing atomic canonical check")
					}
					check := ops[0].(map[string]any)["ConditionCheck"].(map[string]any)
					privacyWriterFences(t, analysisCondition(t, check), "copy source fence lost %s", []string{"summaryPrivacy", "current-generation", "insightsRevision", "current-insight", "sharedToAccount"})
					return 400, projectCancellation(2, 0)
				})
			err := repo.PutAccountInsights(context.Background(), []model.AccountInsight{insight})
			if !errors.Is(err, ErrConditionFailed) || (race == "after-read") != (writes == 1) {
				t.Fatalf("copy race was accepted: %s writes=%d err=%v", race, writes, err)
			}
		})
	}
}

type accountInsightWire struct {
	repo         *DynamoDBRepository
	parent       *model.Meeting
	rows         map[string]model.AccountInsight
	transactions int
	reject       bool
	failPage     bool
}

func newAccountInsightWire(t *testing.T, parent *model.Meeting) *accountInsightWire {
	t.Helper()
	wire := &accountInsightWire{parent: parent, rows: map[string]model.AccountInsight{}}
	wire.repo = summarySDKRepo(func(req *http.Request) (*http.Response, error) {
		var body map[string]any
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		target := req.Header.Get("X-Amz-Target")
		reply := map[string]any{}
		item := func(value any) map[string]any {
			attrs, err := attributevalue.MarshalMap(value)
			if err != nil {
				t.Fatal(err)
			}
			data, err := attributevalue.MarshalMapJSON(attrs)
			if err != nil {
				t.Fatal(err)
			}
			var result map[string]any
			if err := json.Unmarshal(data, &result); err != nil {
				t.Fatal(err)
			}
			return result
		}
		put := func(value any) {
			data, _ := json.Marshal(value)
			attrs, err := attributevalue.UnmarshalMapJSON(data)
			if err != nil {
				t.Fatal(err)
			}
			var insight model.AccountInsight
			if err := attributevalue.UnmarshalMap(attrs, &insight); err != nil {
				t.Fatal(err)
			}
			wire.rows[insight.SK] = insight
		}
		key := func(value any) string {
			return value.(map[string]any)["SK"].(map[string]any)["S"].(string)
		}
		switch {
		case strings.HasSuffix(target, ".GetItem"):
			reply["Item"] = item(wire.parent)
		case strings.HasSuffix(target, ".Query"):
			pk, prefix := "", ""
			for _, value := range body["ExpressionAttributeValues"].(map[string]any) {
				text, _ := value.(map[string]any)["S"].(string)
				if strings.HasPrefix(text, "ACCOUNT#") {
					pk = text
				} else if strings.HasPrefix(text, "INSIGHT#") {
					prefix = text
				}
			}
			var keys []string
			for sk, insight := range wire.rows {
				if insight.PK == pk && strings.HasPrefix(sk, prefix) {
					keys = append(keys, sk)
				}
			}
			sort.Strings(keys)
			start := 0
			if previous := body["ExclusiveStartKey"]; previous != nil {
				if wire.failPage {
					return summaryHTTPResponse(400, `{"__type":"ValidationException"}`, nil), nil
				}
				for i, sk := range keys {
					if sk == key(previous) {
						start = i + 1
						break
					}
				}
			}
			reply["Items"] = []any{}
			if start < len(keys) {
				sk := keys[start]
				row := item(wire.rows[sk])
				if body["ProjectionExpression"] != nil {
					row = map[string]any{"PK": row["PK"], "SK": row["SK"]}
				}
				reply["Items"] = []any{row}
				if start+1 < len(keys) {
					reply["LastEvaluatedKey"] = map[string]any{"PK": map[string]any{"S": pk}, "SK": map[string]any{"S": sk}}
				}
			}
		case strings.HasSuffix(target, ".PutItem"):
			put(body["Item"])
		case strings.HasSuffix(target, ".DeleteItem"):
			delete(wire.rows, key(body["Key"]))
		case strings.HasSuffix(target, ".TransactWriteItems"):
			wire.transactions++
			ops := body["TransactItems"].([]any)
			check := ops[0].(map[string]any)["ConditionCheck"].(map[string]any)
			privacyWriterFences(t, resolved(check, "ConditionExpression"), "replacement lost source fence %s",
				[]string{"insights", "insightsRevision", "summaryPrivacy", "sharedToAccount", "accountId", "date", "userId", "meetingId"})
			if wire.reject {
				return summaryHTTPResponse(400, projectCancellation(len(ops), 0), nil), nil
			}
			for _, value := range ops[1:] {
				op := value.(map[string]any)
				if deletion := op["Delete"]; deletion != nil {
					delete(wire.rows, key(deletion.(map[string]any)["Key"]))
				}
				if insertion := op["Put"]; insertion != nil {
					put(insertion.(map[string]any)["Item"])
				}
			}
		default:
			t.Fatalf("unexpected insight request %s", target)
		}
		data, _ := json.Marshal(reply)
		return summaryHTTPResponse(200, string(data), nil), nil
	})
	return wire
}

func TestAccountInsightValidEmptyReplacementRemovesPreviousRows(t *testing.T) {
	for _, enabled := range []string{"0", "1"} {
		t.Run(enabled, func(t *testing.T) {
			t.Setenv("SUMMARY_PRIVACY_WRITER_ENABLED", enabled)
			parent := privacyParent()
			parent.AccountID, parent.SharedToAccount = "account", true
			parent.Date = time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)
			parent.Insights = `[{"type":"risk","text":"prior insight"}]`
			if enabled == "1" {
				privacyStamp(parent, model.SummaryPrivacySafe, "generation")
				parent.InsightsRevision = "revision"
			}
			wire := newAccountInsightWire(t, parent)
			previous := model.AccountInsight{PK: "ACCOUNT#account", SK: "INSIGHT#2026-10-18T00:00:00Z#meeting#0",
				AccountID: "account", SourceType: "meeting", SourceID: "meeting", SourceUserID: "owner",
				SummaryGeneration: parent.SummaryGeneration(), InsightsRevision: parent.InsightsRevision,
				OccurredAt: parent.Date, Type: "risk", Text: "prior insight"}
			if err := wire.repo.ReplaceMeetingAccountInsights(context.Background(), parent, []model.AccountInsight{previous}); err != nil {
				t.Fatal(err)
			}
			before, err := wire.repo.ListInsightsForAccount(context.Background(), "account")
			if err != nil || len(before) != 1 || before[0].Text != "prior insight" {
				t.Fatalf("nonempty publication missing: %#v %v", before, err)
			}
			second := previous
			second.SK = "INSIGHT#2026-10-18T00:00:00Z#meeting#1"
			wire.rows[second.SK] = second
			unrelated := previous
			unrelated.SK, unrelated.SourceID = "INSIGHT#2026-10-18T00:00:00Z#other#0", "other"
			wire.rows[unrelated.SK] = unrelated
			parent.Insights = `[]`
			if err := wire.repo.ReplaceMeetingAccountInsights(context.Background(), parent, []model.AccountInsight{}); err != nil {
				t.Fatal(err)
			}
			after, err := wire.repo.ListInsightsForAccount(context.Background(), "account")
			if err != nil || len(after) != 1 || after[0].SourceID != "other" {
				t.Fatalf("empty replacement retained old rows or removed another meeting: %#v %v", after, err)
			}
		})
	}
}

func TestEmptyAccountInsightReplacementRejectsInvalidOrChangedSource(t *testing.T) {
	for _, tc := range []string{"absent", "null", "malformed", "nonempty", "unshared", "account", "date", "generation", "revision", "canonical", "condition", "later-page", "limit"} {
		t.Run(tc, func(t *testing.T) {
			t.Setenv("SUMMARY_PRIVACY_WRITER_ENABLED", "0")
			parent := privacyParent()
			parent.AccountID, parent.SharedToAccount, parent.Insights = "account", true, "[]"
			parent.Date = time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)
			observed := *parent
			wire := newAccountInsightWire(t, parent)
			old := model.AccountInsight{PK: "ACCOUNT#account", SK: "INSIGHT#2026-10-18T00:00:00Z#meeting#0",
				AccountID: "account", SourceID: "meeting", Text: "prior insight"}
			wire.rows[old.SK] = old
			expected := ErrConditionFailed
			switch tc {
			case "absent":
				observed.Insights = ""
			case "null":
				observed.Insights = "null"
			case "malformed":
				observed.Insights = "["
			case "nonempty":
				observed.Insights = `[{"text":"new"}]`
			case "unshared":
				parent.SharedToAccount = false
			case "account":
				parent.AccountID = "other"
			case "date":
				parent.Date = parent.Date.Add(time.Hour)
			case "generation":
				privacyStamp(parent, model.SummaryPrivacySafe, "new")
			case "revision":
				parent.InsightsRevision = "new"
			case "canonical":
				parent.Insights = `[{"text":"new"}]`
			case "condition":
				wire.reject = true
			case "later-page", "limit":
				count := 2
				if tc == "limit" {
					count, expected = 100, ErrSummaryLimit
				}
				for i := 1; i < count; i++ {
					row := old
					row.SK += strings.Repeat("x", i)
					wire.rows[row.SK] = row
				}
				wire.failPage = tc == "later-page"
			}
			before := len(wire.rows)
			err := wire.repo.ReplaceMeetingAccountInsights(context.Background(), &observed, nil)
			if tc == "later-page" {
				var apiErr smithy.APIError
				if !errors.As(err, &apiErr) || apiErr.ErrorCode() != "ValidationException" {
					t.Fatalf("pagination failure lost: %v", err)
				}
			} else if !errors.Is(err, expected) {
				t.Fatalf("invalid source accepted: %v", err)
			}
			if len(wire.rows) != before || wire.rows[old.SK].Text != "prior insight" ||
				(tc == "condition") != (wire.transactions == 1) {
				t.Fatalf("failed replacement changed copies: rows=%d tx=%d", len(wire.rows), wire.transactions)
			}
		})
	}
}

func TestMembershipTransactionSpillUsesDeferredCleanup(t *testing.T) {
	privacyWriterEnabled(t)
	for _, tc := range []string{"condition", "cleanup-failure", "ambiguous", "success"} {
		t.Run(tc, func(t *testing.T) {
			parent := privacyParent()
			parent.AccountID = "account"
			puts, transactions, deletes := 0, 0, 0
			key := ""
			repo := summarySDKRepo(func(req *http.Request) (*http.Response, error) {
				if req.Method == "PUT" {
					puts++
					key = req.URL.Path
					if !regexp.MustCompile(`^/bucket/transcripts/meeting/transcriptA\.[a-f0-9]{32}\.txt$`).MatchString(key) {
						t.Fatalf("spill was not immutable: %s", key)
					}
					return summaryHTTPResponse(200, "", nil), nil
				}
				if req.Method == "DELETE" {
					deletes++
					if req.URL.Path != key {
						t.Fatal("cleanup deleted a preexisting spill")
					}
					if tc == "cleanup-failure" {
						return summaryHTTPResponse(403, `<Error><Code>AccessDenied</Code></Error>`, nil), nil
					}
					return summaryHTTPResponse(204, "", nil), nil
				}
				var body map[string]any
				json.NewDecoder(req.Body).Decode(&body)
				switch {
				case strings.HasSuffix(req.Header.Get("X-Amz-Target"), ".GetItem"):
					row := any(parent)
					if body["Key"].(map[string]any)["SK"].(map[string]any)["S"] == "MEMBER#owner" {
						row = &model.AccountMember{AccountID: "account", UserID: "owner"}
					}
					attrs, _ := attributevalue.MarshalMap(row)
					data, _ := attributevalue.MarshalMapJSON(attrs)
					return summaryHTTPResponse(200, `{"Item":`+string(data)+`}`, nil), nil
				case strings.HasSuffix(req.Header.Get("X-Amz-Target"), ".TransactWriteItems"):
					transactions++
					ops := body["TransactItems"].([]any)
					update := ops[1].(map[string]any)["Update"].(map[string]any)
					values, _ := json.Marshal(update["ExpressionAttributeValues"])
					if !strings.Contains(string(values), "s3://bucket/"+strings.TrimPrefix(key, "/bucket/")) {
						t.Fatal("transaction did not publish the new spill")
					}
					if tc == "ambiguous" {
						return summaryHTTPResponse(500, `{"__type":"InternalServerError"}`, nil), nil
					}
					if tc != "success" {
						return summaryHTTPResponse(400, projectCancellation(len(ops), 1), nil), nil
					}
					return summaryHTTPResponse(200, `{}`, nil), nil
				}
				t.Fatalf("unexpected request %s", req.Header.Get("X-Amz-Target"))
				return nil, nil
			})
			fields := map[string]interface{}{"accountId": "account", "transcriptA": strings.Repeat("x", 310*1024)}
			err := repo.UpdateMeetingFieldsIfMatch(context.Background(), "owner", "meeting", map[string]interface{}{"status": model.StatusDone}, fields)
			conditional := tc == "condition" || tc == "cleanup-failure"
			if errors.Is(err, ErrConditionFailed) != conditional || (err == nil) != (tc == "success") {
				t.Fatalf("wrong write outcome: %v", err)
			}
			if tc == "cleanup-failure" {
				var apiErr smithy.APIError
				if !errors.As(err, &apiErr) || apiErr.ErrorCode() != "AccessDenied" {
					t.Fatalf("cleanup failure was hidden: %v", err)
				}
			}
			expectedDeletes := 0
			if conditional {
				expectedDeletes = 1
			}
			if puts != 1 || transactions != 1 || deletes != expectedDeletes ||
				fields["transcriptA"] != strings.Repeat("x", 310*1024) {
				t.Fatalf("unsafe spill outcome: put=%d tx=%d delete=%d", puts, transactions, deletes)
			}
		})
	}
}

func TestSimulatorClaimFencesCapturedGeneration(t *testing.T) {
	repo := privacyWriterWire(t, nil, func(_ string, body map[string]any) (int, string) {
		ops := body["TransactItems"].([]any)
		check := ops[0].(map[string]any)["ConditionCheck"].(map[string]any)
		if condition := analysisCondition(t, check); !strings.Contains(condition, "summaryPrivacy.generationId") ||
			!strings.Contains(condition, "before-reset") {
			t.Fatalf("simulator claim lost source generation: %s", condition)
		}
		return 400, projectCancellation(2, 0)
	})
	err := repo.PutSimRunIfNotRunning(context.Background(), &model.SimRun{
		MeetingID: "meeting", UserID: "owner", SimRunID: "run", Status: model.SimStatusQueued,
		SummaryGeneration: "before-reset",
	})
	if !errors.Is(err, ErrConditionFailed) {
		t.Fatalf("old-generation simulator claim survived reset: %v", err)
	}
}
