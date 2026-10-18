package repository

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
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
