package repository

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/expression"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/ttobak/backend/internal/model"
)

func TestSummaryCheckPreservesStoredSetOperandTypes(t *testing.T) {
	for _, test := range []struct {
		name  string
		value types.AttributeValue
	}{
		{"string set", &types.AttributeValueMemberSS{Value: []string{"project-b", "project-a"}}},
		{"number set", &types.AttributeValueMemberNS{Value: []string{"2", "1"}}},
		{"list control", &types.AttributeValueMemberL{Value: []types.AttributeValue{
			&types.AttributeValueMemberS{Value: "project"},
		}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			check, err := summaryCheck("USER#owner", "MEETING#meeting",
				map[string]types.AttributeValue{"storedSet": test.value}, []string{"storedSet"})
			if err != nil {
				t.Fatal(err)
			}
			expr, err := expression.NewBuilder().WithCondition(summaryCondition(check)).Build()
			if err != nil {
				t.Fatal(err)
			}
			for alias, field := range expr.Names() {
				if field != "storedSet" {
					continue
				}
				match := regexp.MustCompile(regexp.QuoteMeta(alias) + `\s*=\s*(:\w+)`).FindStringSubmatch(*expr.Condition())
				if len(match) != 2 {
					t.Fatal("stored set lost its equality fence")
				}
				actual, err := attributevalue.MarshalJSON(expr.Values()[match[1]])
				if err != nil {
					t.Fatal(err)
				}
				expected, err := attributevalue.MarshalJSON(test.value)
				if err != nil || !bytes.Equal(actual, expected) {
					t.Fatalf("stored SDK operand changed type or values: got=%s want=%s err=%v", actual, expected, err)
				}
				return
			}
			t.Fatal("stored set condition missing")
		})
	}
}

func TestWholeMeetingWriteUsesCurrentProjectStringSetAndAuthorizationFence(t *testing.T) {
	t.Setenv("SUMMARY_PRIVACY_WRITER_ENABLED", "0")
	for _, concurrent := range []bool{false, true} {
		t.Run(strconv.FormatBool(concurrent), func(t *testing.T) {
			wire := newClearanceWire()
			repo := wire.repository(t)
			options := repo.client.Options()
			baseHTTP := options.HTTPClient
			attempts := 0
			options.HTTPClient = summaryHTTP(func(req *http.Request) (*http.Response, error) {
				if !strings.HasSuffix(req.Header.Get("X-Amz-Target"), ".PutItem") {
					return baseHTTP.Do(req)
				}
				body, err := io.ReadAll(req.Body)
				if err != nil {
					t.Fatal(err)
				}
				req.Body = io.NopCloser(bytes.NewReader(body))
				var input notesRevisionRequest
				if err := json.Unmarshal(body, &input); err != nil {
					t.Fatal(err)
				}
				attempts++
				if !strings.Contains(clearanceExpression(input, input.ConditionExpression), "attribute_exists (PK)") {
					t.Fatal("whole write lost its existing-row fence")
				}
				// Enforce the actual wire types and bytes of both metadata
				// operands. A list cannot equal the stored DynamoDB String Set.
				for _, field := range []string{"projectIds", "authorizationRevision"} {
					found := false
					for alias, name := range input.ExpressionAttributeNames {
						if name != field {
							continue
						}
						match := regexp.MustCompile(regexp.QuoteMeta(alias) + `\s*=\s*(:\w+)`).FindStringSubmatch(input.ConditionExpression)
						if len(match) != 2 {
							t.Fatalf("whole write lost %s equality fence", field)
						}
						actual, _ := json.Marshal(input.ExpressionAttributeValues[match[1]])
						expected, _ := json.Marshal(wire.row[field])
						found = true
						if !bytes.Equal(actual, expected) {
							return summaryHTTPResponse(400, `{"__type":"ConditionalCheckFailedException"}`, nil), nil
						}
					}
					if !found {
						t.Fatalf("whole write omitted %s", field)
					}
				}
				if concurrent && attempts == 1 {
					wire.row["projectIds"] = map[string]any{"SS": []any{"project", "linked-concurrently"}}
					wire.row["authorizationRevision"] = map[string]any{"N": "8"}
					return summaryHTTPResponse(400, `{"__type":"ConditionalCheckFailedException"}`, nil), nil
				}
				return baseHTTP.Do(req)
			})
			repo.client = dynamodb.New(options)
			caller := wire.meeting(t)
			caller.ProjectIDs = []string{"stale-project"}
			caller.Content = "fresh summary"
			if err := repo.UpdateMeeting(context.Background(), caller); err != nil {
				t.Fatalf("linked meeting whole write failed: %v (attempts=%d)", err, attempts)
			}
			expectedAttempts, expectedRevision, expectedProjects := 1, int64(8), 1
			if concurrent {
				expectedAttempts, expectedRevision, expectedProjects = 2, 9, 2
			}
			stored := wire.meeting(t)
			if attempts != expectedAttempts || len(wire.writes) != 1 ||
				stored.AuthorizationRevision != expectedRevision || len(stored.ProjectIDs) != expectedProjects ||
				stored.ProjectIDs[0] != "project" || stored.Content != "fresh summary" ||
				stored.SummaryPrivacy != nil || stored.InsightsRevision != "" {
				t.Fatalf("whole write lost relation/CAS/clearance guarantees: %+v attempts=%d committed=%d", stored, attempts, len(wire.writes))
			}
			if len(caller.ProjectIDs) != 1 || caller.ProjectIDs[0] != "stale-project" ||
				caller.SummaryPrivacy == nil || caller.InsightsRevision != "insights-old" {
				t.Fatal("whole write mutated caller relation or clearance fields")
			}
		})
	}
}

// Exercise actual SDK requests against a stored, stamped-era publication. The
// small wire model applies the emitted SET/REMOVE/ADD atomically, rather than
// teaching a service mock to invalidate clearance on our behalf.
type clearanceWire struct {
	row          map[string]map[string]any
	writes       []notesRevisionRequest
	rejected     bool
	ambiguous    bool
	conflictOnce bool
}

func newClearanceWire() *clearanceWire {
	s := func(value string) map[string]any { return map[string]any{"S": value} }
	return &clearanceWire{row: map[string]map[string]any{
		"PK": s("USER#owner"), "SK": s("MEETING#meeting"),
		"userId": s("owner"), "meetingId": s("meeting"), "content": s("old summary"),
		"notes": s("raw authorized notes"), "accountId": s("account"),
		"sharedToAccount": {"BOOL": true}, "authorizationRevision": {"N": "7"},
		"projectIds": {"SS": []any{"project"}}, "insightsRevision": s("insights-old"),
		"summaryPrivacy": {"M": map[string]any{
			"version": map[string]any{"N": "1"}, "state": s(model.SummaryPrivacySafe),
			"generationId": s("generation-old"), "contentSha256": s(model.SummaryPrivacyHash("old summary")),
		}},
	}}
}

func clearanceAttributes(t *testing.T, row map[string]map[string]any) map[string]types.AttributeValue {
	t.Helper()
	result := make(map[string]types.AttributeValue, len(row))
	for field, value := range row {
		switch {
		case value["S"] != nil:
			result[field] = &types.AttributeValueMemberS{Value: value["S"].(string)}
		case value["N"] != nil:
			result[field] = &types.AttributeValueMemberN{Value: value["N"].(string)}
		case value["BOOL"] != nil:
			result[field] = &types.AttributeValueMemberBOOL{Value: value["BOOL"].(bool)}
		case value["SS"] != nil:
			var values []string
			for _, item := range value["SS"].([]any) {
				values = append(values, item.(string))
			}
			result[field] = &types.AttributeValueMemberSS{Value: values}
		case value["M"] != nil:
			nested := map[string]map[string]any{}
			for key, item := range value["M"].(map[string]any) {
				nested[key] = item.(map[string]any)
			}
			result[field] = &types.AttributeValueMemberM{Value: clearanceAttributes(t, nested)}
		default:
			t.Fatalf("unsupported fixture attribute %s: %v", field, value)
		}
	}
	return result
}

func (w *clearanceWire) meeting(t *testing.T) *model.Meeting {
	t.Helper()
	var meeting model.Meeting
	if err := attributevalue.UnmarshalMap(clearanceAttributes(t, w.row), &meeting); err != nil {
		t.Fatal(err)
	}
	return &meeting
}

func (w *clearanceWire) snapshot(t *testing.T) *model.SummarySnapshot {
	t.Helper()
	attrs := clearanceAttributes(t, w.row)
	check, err := summaryCheck("USER#owner", "MEETING#meeting", attrs, withSummaryPrivacyFields(batchSummaryFields))
	if err != nil {
		t.Fatal(err)
	}
	stored := map[string]interface{}{}
	if err := attributevalue.UnmarshalMap(attrs, &stored); err != nil {
		t.Fatal(err)
	}
	return &model.SummarySnapshot{Meeting: w.meeting(t), Stored: stored, Checks: []model.SummaryCheck{check}}
}

func clearanceExpression(input notesRevisionRequest, expression string) string {
	return regexp.MustCompile(`#\w+`).ReplaceAllStringFunc(expression, func(alias string) string {
		return input.ExpressionAttributeNames[alias]
	})
}

func (w *clearanceWire) apply(t *testing.T, input notesRevisionRequest) {
	t.Helper()
	if input.Item != nil {
		w.row = input.Item
		return
	}
	sections := regexp.MustCompile(`\b(SET|REMOVE|ADD|DELETE)\b`).FindAllStringIndex(input.UpdateExpression, -1)
	for i, section := range sections {
		end := len(input.UpdateExpression)
		if i+1 < len(sections) {
			end = sections[i+1][0]
		}
		action, body := input.UpdateExpression[section[0]:section[1]], input.UpdateExpression[section[1]:end]
		for _, part := range strings.Split(body, ",") {
			terms := strings.Fields(strings.TrimSpace(part))
			switch action {
			case "SET":
				if len(terms) != 3 || terms[1] != "=" {
					t.Fatalf("unsupported SET fixture: %s", part)
				}
				w.row[input.ExpressionAttributeNames[terms[0]]] = input.ExpressionAttributeValues[terms[2]]
			case "REMOVE":
				if len(terms) != 1 {
					t.Fatalf("unsupported REMOVE fixture: %s", part)
				}
				delete(w.row, input.ExpressionAttributeNames[terms[0]])
			case "ADD":
				field := input.ExpressionAttributeNames[terms[0]]
				prior, _ := strconv.ParseInt(stringValue(w.row[field]["N"]), 10, 64)
				delta, err := strconv.ParseInt(input.ExpressionAttributeValues[terms[1]]["N"].(string), 10, 64)
				if err != nil {
					t.Fatal(err)
				}
				w.row[field] = map[string]any{"N": strconv.FormatInt(prior+delta, 10)}
			default:
				t.Fatalf("unsupported fixture action %s", action)
			}
		}
	}
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func (w *clearanceWire) repository(t *testing.T) *DynamoDBRepository {
	t.Helper()
	client := dynamodb.New(dynamodb.Options{
		Region: "ap-northeast-2", Credentials: aws.AnonymousCredentials{},
		Retryer: retry.NewStandard(func(o *retry.StandardOptions) {
			o.MaxAttempts = 2
			o.Backoff = retry.BackoffDelayerFunc(func(int, error) (time.Duration, error) { return 0, nil })
		}),
		HTTPClient: summaryHTTP(func(req *http.Request) (*http.Response, error) {
			var input struct {
				notesRevisionRequest
				ProjectionExpression string
				TransactItems        []map[string]notesRevisionRequest
			}
			if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
				t.Fatal(err)
			}
			switch {
			case strings.HasSuffix(req.Header.Get("X-Amz-Target"), ".GetItem"):
				if !input.ConsistentRead {
					t.Fatal("write metadata must be strongly consistent")
				}
				row := w.row
				if input.ProjectionExpression != "" {
					row = map[string]map[string]any{}
					for _, alias := range strings.Split(input.ProjectionExpression, ",") {
						field := input.ExpressionAttributeNames[strings.TrimSpace(alias)]
						if field != "PK" && field != "projectIds" && field != "authorizationRevision" &&
							field != "accountId" && field != "sharedToAccount" {
							t.Fatalf("whole-write metadata projected body field %s", field)
						}
						if value, ok := w.row[field]; ok {
							row[field] = value
						}
					}
				}
				body, _ := json.Marshal(map[string]any{"Item": row})
				return summaryHTTPResponse(200, string(body), nil), nil
			case strings.HasSuffix(req.Header.Get("X-Amz-Target"), ".Query"):
				return summaryHTTPResponse(200, `{"Items":[]}`, nil), nil
			}
			op := input.notesRevisionRequest
			transaction := len(input.TransactItems) > 0
			if transaction {
				op = input.TransactItems[0]["Update"]
			}
			w.writes = append(w.writes, op)
			conflict := w.conflictOnce
			if conflict {
				w.conflictOnce = false
				w.row["authorizationRevision"] = map[string]any{"N": "8"}
			}
			if w.rejected || conflict {
				reply := `{"__type":"ConditionalCheckFailedException"}`
				if transaction {
					reply = `{"__type":"TransactionCanceledException","CancellationReasons":[{"Code":"ConditionalCheckFailed"}]}`
				}
				return summaryHTTPResponse(400, reply, nil), nil
			}
			w.apply(t, op)
			if w.ambiguous {
				return summaryHTTPResponse(500, `{"__type":"InternalServerError","message":"synthetic lost committed reply"}`, nil), nil
			}
			return summaryHTTPResponse(200, `{}`, nil), nil
		}),
	})
	return NewDynamoDBRepository(client, "clearance-fixture")
}

func TestContentWritesInvalidateStoredClearanceAcrossWriterRollback(t *testing.T) {
	for _, flag := range []string{"", "0", "1"} {
		for _, path := range []string{"partial content", "conditional content", "live summary", "whole item", "batch summary", "resummary"} {
			t.Run(flag+"/"+path, func(t *testing.T) {
				t.Setenv("SUMMARY_PRIVACY_WRITER_ENABLED", flag)
				wire := newClearanceWire()
				if !wire.meeting(t).SummaryPrivacy.SafeMetadata() {
					t.Fatal("positive stored publication control is invalid")
				}
				repo, ctx := wire.repository(t), context.Background()
				snapshot := wire.snapshot(t)
				fields := map[string]interface{}{"content": "private edited summary"}
				caller := wire.meeting(t)
				var err error
				switch path {
				case "partial content":
					err = repo.UpdateMeetingFields(ctx, "owner", "meeting", fields)
				case "conditional content":
					err = repo.UpdateMeetingFieldsIfMatch(ctx, "owner", "meeting", map[string]interface{}{"content": "old summary"}, fields)
				case "live summary":
					fields = map[string]interface{}{"liveSummary": "private live summary"}
					err = repo.UpdateMeetingFields(ctx, "owner", "meeting", fields)
				case "whole item":
					caller.Content, caller.AuthorizationRevision = "private edited summary", 1
					err = repo.UpdateMeeting(ctx, caller)
				case "batch summary":
					err = repo.SaveMeetingSummary(ctx, snapshot, "private edited summary", "coverage")
				case "resummary":
					now := time.Now().UTC()
					state := &model.ResummaryState{OwnerID: "owner", RunID: "run", Status: model.AnalysisRunning, LeaseUntil: now.Add(time.Minute).UnixMilli()}
					err = repo.CompleteResummary(ctx, snapshot, state, "private edited summary", "coverage", "result", now)
				}
				if err != nil {
					t.Fatal(err)
				}
				metadata := wire.meeting(t)
				// Metadata-only readers must reject old derived rows without
				// fetching content or permitting the old generation to replay.
				if metadata.SummaryPrivacy.SafeMetadata() || metadata.SummaryGeneration() == "generation-old" ||
					metadata.InsightsRevision != "" || metadata.AuthorizationRevision != 8 {
					t.Fatalf("stale clearance survived %s: privacy=%+v insight=%q revision=%d",
						path, metadata.SummaryPrivacy, metadata.InsightsRevision, metadata.AuthorizationRevision)
				}
				if caller.SummaryPrivacy == nil || caller.InsightsRevision != "insights-old" {
					t.Fatal("write mutated caller provenance rather than the storage copy")
				}
				if len(fields) != 1 || strings.HasPrefix(stringValue(fields["content"]), "s3://") {
					t.Fatal("content invalidation mutated the caller's fields map")
				}
				op := wire.writes[0]
				if path == "conditional content" && !strings.Contains(clearanceExpression(op, op.ConditionExpression), "content") {
					t.Fatal("content invalidation dropped caller CAS")
				}
				if path == "whole item" && !strings.Contains(clearanceExpression(op, op.ConditionExpression), "authorizationRevision") {
					t.Fatal("whole replacement did not pin current authorization revision")
				}
			})
		}
	}
}

func TestClearancePersistsForNonContentAndFencedDerivativeWrites(t *testing.T) {
	for _, path := range []string{"title", "notes", "status", "fenced insights"} {
		t.Run(path, func(t *testing.T) {
			t.Setenv("SUMMARY_PRIVACY_WRITER_ENABLED", "0")
			wire := newClearanceWire()
			repo, snapshot := wire.repository(t), wire.snapshot(t)
			var err error
			if path == "fenced insights" {
				err = repo.PublishSummaryDerivatives(context.Background(), snapshot, map[string]interface{}{"insights": "fresh insights"})
			} else {
				err = repo.UpdateMeetingFields(context.Background(), "owner", "meeting", map[string]interface{}{path: "updated"})
			}
			if err != nil {
				t.Fatal(err)
			}
			meeting := wire.meeting(t)
			if !meeting.SummaryPrivacy.SafeMetadata() || meeting.SummaryGeneration() != "generation-old" || meeting.AuthorizationRevision != 7 {
				t.Fatal("non-content/fenced publication discarded valid clearance")
			}
			if path != "fenced insights" && meeting.InsightsRevision != "insights-old" {
				t.Fatal("non-content update discarded insight revision")
			}
			if path == "fenced insights" {
				op := wire.writes[0]
				for _, field := range []string{"content", "summaryPrivacy", "authorizationRevision"} {
					if !strings.Contains(clearanceExpression(op, op.ConditionExpression), field) {
						t.Fatalf("fenced derivative publication lost %s condition", field)
					}
				}
				if meeting.InsightsRevision == "" || meeting.InsightsRevision == "insights-old" {
					t.Fatal("fenced current derivative did not install its own insight revision")
				}
			}
		})
	}
}

func TestClearanceInvalidationRejectedOrAmbiguousWriteOutcomes(t *testing.T) {
	for _, path := range []string{"conditional", "whole item", "batch", "resummary"} {
		for _, outcome := range []string{"rejected", "ambiguous"} {
			t.Run(path+"/"+outcome, func(t *testing.T) {
				wire := newClearanceWire()
				wire.rejected, wire.ambiguous = outcome == "rejected", outcome == "ambiguous"
				repo, snapshot := wire.repository(t), wire.snapshot(t)
				ctx, now := context.Background(), time.Now().UTC()
				var err error
				switch path {
				case "conditional":
					err = repo.UpdateMeetingFieldsIfMatch(ctx, "owner", "meeting", map[string]interface{}{"content": "old summary"}, map[string]interface{}{"content": "new summary"})
				case "whole item":
					caller := wire.meeting(t)
					caller.Content = "new summary"
					err = repo.UpdateMeeting(ctx, caller)
				case "batch":
					err = repo.SaveMeetingSummary(ctx, snapshot, "new summary", "")
				case "resummary":
					state := &model.ResummaryState{OwnerID: "owner", RunID: "run", Status: model.AnalysisRunning, LeaseUntil: now.Add(time.Minute).UnixMilli()}
					err = repo.CompleteResummary(ctx, snapshot, state, "new summary", "", "result", now)
				}
				if err == nil || outcome == "ambiguous" && errors.Is(err, ErrConditionFailed) {
					t.Fatalf("write outcome incorrectly reported: %v", err)
				}
				meeting := wire.meeting(t)
				if outcome == "rejected" && (!meeting.SummaryPrivacy.SafeMetadata() || meeting.AuthorizationRevision != 7 || meeting.Content != "old summary") {
					t.Fatal("rejected write changed stored clearance or content")
				}
				if outcome == "ambiguous" && (meeting.SummaryPrivacy.SafeMetadata() || meeting.AuthorizationRevision != 8) {
					t.Fatal("committed ambiguous response did not invalidate atomically")
				}
				wantAttempts := 1
				if path == "whole item" && outcome == "rejected" {
					wantAttempts = 3
				}
				if len(wire.writes) != wantAttempts {
					t.Fatalf("unexpected replay count: %d, want %d", len(wire.writes), wantAttempts)
				}
				if outcome == "rejected" && path != "whole item" && !errors.Is(err, ErrConditionFailed) {
					t.Fatalf("lost conditional rejection sentinel: %v", err)
				}
			})
		}
	}
}

func TestOrdinaryFieldsCannotInstallClearance(t *testing.T) {
	for _, field := range []string{"summaryPrivacy", "insightsRevision", "authorizationRevision"} {
		wire := newClearanceWire()
		if err := wire.repository(t).UpdateMeetingFields(context.Background(), "owner", "meeting", map[string]interface{}{field: "untrusted"}); err == nil || len(wire.writes) != 0 {
			t.Fatalf("ordinary writer installed server-managed %s", field)
		}
	}
}

func TestWholeWriteRepinsConcurrentAuthorizationRevision(t *testing.T) {
	wire := newClearanceWire()
	wire.conflictOnce = true
	caller := wire.meeting(t)
	caller.Content, caller.AuthorizationRevision = "edited", 1
	if err := wire.repository(t).UpdateMeeting(context.Background(), caller); err != nil {
		t.Fatal(err)
	}
	if len(wire.writes) != 2 || wire.meeting(t).AuthorizationRevision != 9 {
		t.Fatal("whole-item retry rolled back concurrent authorization fence")
	}
	for i, expected := range []string{"7", "8"} {
		input := wire.writes[i]
		condition := clearanceExpression(input, input.ConditionExpression)
		if !strings.Contains(condition, "projectIds") || !strings.Contains(condition, "authorizationRevision") {
			t.Fatal("retry did not preserve both metadata fences")
		}
		found := false
		for alias, name := range input.ExpressionAttributeNames {
			if name == "authorizationRevision" {
				match := regexp.MustCompile(regexp.QuoteMeta(alias) + `\s*=\s*(:\w+)`).FindStringSubmatch(input.ConditionExpression)
				found = len(match) == 2 && input.ExpressionAttributeValues[match[1]]["N"] == expected
			}
		}
		if !found {
			t.Fatalf("attempt %d did not pin exact current revision %s", i, expected)
		}
	}
	if wire.meeting(t).SummaryPrivacy.SafeMetadata() || caller.SummaryPrivacy == nil {
		t.Fatal("whole-item retry lost invalidation or mutated caller clearance")
	}
}

func TestWholeWritePinsExistingLegacyRowWithoutMetadata(t *testing.T) {
	wire := newClearanceWire()
	delete(wire.row, "authorizationRevision")
	delete(wire.row, "projectIds")
	caller := wire.meeting(t)
	if err := wire.repository(t).UpdateMeeting(context.Background(), caller); err != nil {
		t.Fatal(err)
	}
	input := wire.writes[0]
	condition := clearanceExpression(input, input.ConditionExpression)
	if !strings.Contains(condition, "attribute_exists (PK)") || !strings.Contains(condition, "attribute_not_exists (authorizationRevision)") {
		t.Fatalf("metadata projection mistook an existing legacy item for an absent item: %s", condition)
	}
	if wire.meeting(t).AuthorizationRevision != 1 {
		t.Fatal("legacy invalidation did not initialize the authorization revision")
	}
}

func TestSummaryCaptureAlwaysPinsStoredPrivacy(t *testing.T) {
	for _, flag := range []string{"", "0", "1"} {
		for _, capture := range []string{"batch", "resummary"} {
			t.Run(flag+"/"+capture, func(t *testing.T) {
				t.Setenv("SUMMARY_PRIVACY_WRITER_ENABLED", flag)
				wire := newClearanceWire()
				repo := wire.repository(t)
				var snapshot *model.SummarySnapshot
				var err error
				if capture == "batch" {
					snapshot, err = repo.CaptureMeetingSummary(context.Background(), "owner", "meeting")
				} else {
					snapshot, err = repo.CaptureResummary(context.Background(), "owner", "meeting", "owner")
				}
				if err != nil {
					t.Fatal(err)
				}
				for _, field := range []string{"summaryPrivacy", "insightsRevision", "authorizationRevision"} {
					if !snapshot.Checks[0].Fields[field].Present {
						t.Fatalf("capture omitted current %s while writer flag=%q", field, flag)
					}
				}
				if capture == "batch" {
					for _, field := range []string{"title", "updatedAt", "actionItems"} {
						if _, pinned := snapshot.Checks[0].Fields[field]; pinned {
							t.Fatalf("legacy completion fenced unrelated metadata %s", field)
						}
					}
				}
			})
		}
	}
}

type wholeMeetingAccessRequest struct {
	Item                      map[string]map[string]any
	ProjectionExpression      string
	ExpressionAttributeNames  map[string]string
	ExpressionAttributeValues map[string]map[string]any
	ConditionExpression       string
	ConsistentRead            bool
}

type wholeMeetingAccessWire struct {
	row       map[string]map[string]any
	afterRead func()
	projected []string
	puts      []wholeMeetingAccessRequest
	gets      int
}

func wholeMeetingAccessResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/x-amz-json-1.0"}},
		Body: io.NopCloser(strings.NewReader(body))}
}

func (w *wholeMeetingAccessWire) Do(req *http.Request) (*http.Response, error) {
	var input wholeMeetingAccessRequest
	if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
		return nil, err
	}
	switch {
	case strings.HasSuffix(req.Header.Get("X-Amz-Target"), ".GetItem"):
		if !input.ConsistentRead {
			return nil, fmt.Errorf("metadata read must be strongly consistent")
		}
		w.gets++
		projected := map[string]map[string]any{}
		for _, alias := range strings.Split(input.ProjectionExpression, ",") {
			name := input.ExpressionAttributeNames[strings.TrimSpace(alias)]
			w.projected = append(w.projected, name)
			if value, exists := w.row[name]; exists {
				projected[name] = value
			}
		}
		body, err := json.Marshal(map[string]any{"Item": projected})
		if err != nil {
			return nil, err
		}
		if w.afterRead != nil {
			mutate := w.afterRead
			w.afterRead = nil
			mutate()
		}
		return wholeMeetingAccessResponse(200, string(body)), nil
	case strings.HasSuffix(req.Header.Get("X-Amz-Target"), ".PutItem"):
		w.puts = append(w.puts, input)
		// Evaluate the exact equality/existence subset emitted by these
		// positive-project-set cases. No live DynamoDB or permissive mock is used.
		for _, match := range regexp.MustCompile(`(#\w+)\s*=\s*(:\w+)`).FindAllStringSubmatch(input.ConditionExpression, -1) {
			name := input.ExpressionAttributeNames[match[1]]
			value, exists := w.row[name]
			if !exists || !reflect.DeepEqual(value, input.ExpressionAttributeValues[match[2]]) {
				return wholeMeetingAccessResponse(400, `{"__type":"ConditionalCheckFailedException"}`), nil
			}
		}
		for _, match := range regexp.MustCompile(`attribute_(not_)?exists\s*\(\s*(#\w+)\s*\)`).FindAllStringSubmatch(input.ConditionExpression, -1) {
			_, exists := w.row[input.ExpressionAttributeNames[match[2]]]
			if exists == (match[1] == "not_") {
				return wholeMeetingAccessResponse(400, `{"__type":"ConditionalCheckFailedException"}`), nil
			}
		}
		w.row = input.Item
		return wholeMeetingAccessResponse(200, `{}`), nil
	default:
		return nil, fmt.Errorf("unexpected operation; fixture never forwards network requests")
	}
}

func wholeMeetingAccessRow(account string, shared bool, revision bool) map[string]map[string]any {
	row := map[string]map[string]any{
		"PK": {"S": "USER#owner"}, "SK": {"S": "MEETING#meeting"},
		"projectIds": {"SS": []any{"project"}},
		"accountId":  {"S": account}, "sharedToAccount": {"BOOL": shared},
	}
	if revision {
		row["authorizationRevision"] = map[string]any{"N": "8"}
	}
	return row
}

func TestWholeMeetingWritePreservesCurrentAccountAuthorization(t *testing.T) {
	cases := []struct {
		name     string
		account  string
		shared   bool
		revision bool
		after    bool
		absent   bool
	}{
		{"unshare_before_metadata_read", "current-account", false, true, false, false},
		{"new_grant_before_metadata_read", "current-account", true, true, false, false},
		{"legacy_no_revision_unshare_before_read", "current-account", false, false, false, false},
		{"account_change_after_read_same_revision", "stale-account", true, true, true, false},
		{"account_change_after_read_absent_revision", "stale-account", true, false, true, false},
		{"absent_current_account_fields", "", false, true, false, true},
		{"explicit_empty_account_and_false_sharing", "", false, true, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := &wholeMeetingAccessWire{row: wholeMeetingAccessRow(tc.account, tc.shared, tc.revision)}
			if tc.absent {
				delete(w.row, "accountId")
				delete(w.row, "sharedToAccount")
			}
			caller := &model.Meeting{PK: "USER#owner", SK: "MEETING#meeting", UserID: "owner", MeetingID: "meeting",
				AccountID: "stale-account", SharedToAccount: true, AuthorizationRevision: 7, ProjectIDs: []string{"project"}}
			if tc.name == "new_grant_before_metadata_read" {
				caller.AccountID, caller.SharedToAccount = "", false
			}
			if tc.after {
				w.afterRead = func() {
					w.row["accountId"] = map[string]any{"S": "current-account"}
					w.row["sharedToAccount"] = map[string]any{"BOOL": false}
				}
			}
			wantAccount, wantShared := w.row["accountId"], w.row["sharedToAccount"]
			if tc.after {
				wantAccount, wantShared = map[string]any{"S": "current-account"}, map[string]any{"BOOL": false}
			}
			repo := NewDynamoDBRepository(dynamodb.New(dynamodb.Options{
				Region: "ap-northeast-2", Credentials: aws.AnonymousCredentials{}, HTTPClient: w,
			}), "synthetic-table")
			if err := repo.UpdateMeeting(context.Background(), caller); err != nil {
				t.Fatalf("whole save failed: %v", err)
			}
			t.Logf("reads=%d puts=%d projected=%v stampedRevision=%v", w.gets, len(w.puts), w.projected, w.row["authorizationRevision"])
			if !reflect.DeepEqual(w.row["accountId"], wantAccount) || !reflect.DeepEqual(w.row["sharedToAccount"], wantShared) {
				t.Fatalf("stale authorization payload accepted: account=%v shared=%v; want current account=%v shared=%v",
					w.row["accountId"], w.row["sharedToAccount"], wantAccount, wantShared)
			}
			if caller.AuthorizationRevision != 7 {
				t.Fatal("storage metadata refresh mutated caller")
			}
		})
	}
}

func TestWholeMeetingWriteMissingRowRetainsCallerClassification(t *testing.T) {
	w := &wholeMeetingAccessWire{row: map[string]map[string]any{}}
	caller := &model.Meeting{PK: "USER#owner", SK: "MEETING#meeting", UserID: "owner", MeetingID: "meeting",
		AccountID: "prepared-account", SharedToAccount: false}
	repo := NewDynamoDBRepository(dynamodb.New(dynamodb.Options{
		Region: "ap-northeast-2", Credentials: aws.AnonymousCredentials{}, HTTPClient: w,
	}), "synthetic-table")
	if err := repo.UpdateMeeting(context.Background(), caller); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(w.row["accountId"], map[string]any{"S": "prepared-account"}) {
		t.Fatal("missing-row create dropped caller's classification")
	}
	t.Log("missing-row classification retained; existing-row preservation must not replace it with empty metadata")
}
