package repository

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/google/uuid"
	"github.com/ttobak/backend/internal/model"
)

type notesRevisionWire struct {
	sync.Mutex
	notes, revision       string
	hasNotes, hasRevision bool
	delayB, releaseB      chan struct{}
}

type notesRevisionRequest struct {
	Key                       map[string]map[string]any
	Item                      map[string]map[string]any
	ConditionExpression       string
	UpdateExpression          string
	ExpressionAttributeNames  map[string]string
	ExpressionAttributeValues map[string]map[string]any
	ConsistentRead            bool
}

func notesWireValue(input notesRevisionRequest, expr, field string) (string, bool) {
	for alias, name := range input.ExpressionAttributeNames {
		if name != field {
			continue
		}
		match := regexp.MustCompile(regexp.QuoteMeta(alias) + `\s*=\s*(:\w+)`).FindStringSubmatch(expr)
		if match != nil {
			value, _ := input.ExpressionAttributeValues[match[1]]["S"].(string)
			return value, true
		}
	}
	return "", false
}

func (w *notesRevisionWire) repository(t *testing.T) *DynamoDBRepository {
	t.Helper()
	client := dynamodb.New(dynamodb.Options{
		Region: "ap-northeast-2", Credentials: aws.AnonymousCredentials{}, RetryMaxAttempts: 1,
		HTTPClient: meetingListHTTPClientFunc(func(req *http.Request) (*http.Response, error) {
			var input notesRevisionRequest
			if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
				t.Error(err)
				return nil, err
			}
			op := req.Header.Get("X-Amz-Target")
			next, writesNotes := notesWireValue(input, input.UpdateExpression, "notes")
			if op == "DynamoDB_20120810.UpdateItem" && writesNotes && next == "B" && w.delayB != nil {
				close(w.delayB)
				<-w.releaseB
			}
			w.Lock()
			defer w.Unlock()
			status, body := 200, map[string]any{}
			switch op {
			case "DynamoDB_20120810.GetItem":
				if !input.ConsistentRead {
					t.Error("notes metadata must use a strong read")
				}
				item := map[string]any{"PK": map[string]string{"S": "USER#owner"}, "SK": map[string]string{"S": "MEETING#m"},
					"userId": map[string]string{"S": "owner"}, "meetingId": map[string]string{"S": "m"}}
				if w.hasNotes {
					item["notes"] = map[string]string{"S": w.notes}
				}
				if w.hasRevision {
					item["notesRevision"] = map[string]string{"S": w.revision}
				}
				body["Item"] = item
			case "DynamoDB_20120810.UpdateItem":
				if input.Key["PK"]["S"] != "USER#owner" || input.Key["SK"]["S"] != "MEETING#m" {
					err := errors.New("notes write escaped its exact primary key")
					t.Error(err)
					return nil, err
				}
				_, checksText := notesWireValue(input, input.ConditionExpression, "notes")
				_, checksVersion := notesWireValue(input, input.ConditionExpression, "notesRevision")
				if checksText && checksVersion && strings.Count(input.ConditionExpression, " AND ") != 2 {
					err := errors.New("existence, text and version must be conjoined in the same condition")
					t.Error(err)
					return nil, err
				}
				for _, field := range []struct {
					name, current string
					present       bool
				}{{"notes", w.notes, w.hasNotes}, {"notesRevision", w.revision, w.hasRevision}} {
					expected, compared := notesWireValue(input, input.ConditionExpression, field.name)
					if !compared {
						continue
					}
					matched := field.present && expected == field.current
					if !field.present && expected == "" {
						for alias, name := range input.ExpressionAttributeNames {
							if name == field.name && strings.Contains(input.ConditionExpression, "attribute_not_exists ("+alias+")") {
								matched = true
							}
						}
					}
					if !matched {
						status = 400
						body = map[string]any{"__type": "ConditionalCheckFailedException"}
						break
					}
				}
				if status == 200 {
					if writesNotes {
						w.notes, w.hasNotes = next, true
					}
					if version, ok := notesWireValue(input, input.UpdateExpression, "notesRevision"); ok {
						w.revision, w.hasRevision = version, true
					}
				}
			case "DynamoDB_20120810.PutItem":
				w.notes, _ = input.Item["notes"]["S"].(string)
				w.revision, _ = input.Item["notesRevision"]["S"].(string)
				w.hasNotes, w.hasRevision = input.Item["notes"] != nil, input.Item["notesRevision"] != nil
			default:
				err := errors.New("unexpected storage operation: " + op)
				t.Error(err)
				return nil, err
			}
			raw, _ := json.Marshal(body)
			return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/x-amz-json-1.0"}},
				Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
		}),
	})
	return NewDynamoDBRepository(client, "notes-revision-test")
}

func TestNotesRevisionFenceRejectsDelayedSameTextCAS(t *testing.T) {
	for _, initial := range []string{"", "00000000-0000-4000-8000-000000000001"} {
		t.Run("initial="+initial, func(t *testing.T) {
			wire := &notesRevisionWire{notes: "A", hasNotes: true, revision: initial, hasRevision: initial != "",
				delayB: make(chan struct{}), releaseB: make(chan struct{})}
			repo := wire.repository(t)
			var release sync.Once
			defer release.Do(func() { close(wire.releaseB) })
			finished := make(chan error, 1)
			go func() {
				_, err := repo.UpdateMeetingNotesWithRevision(context.Background(), "owner", "m", "A", "B", &initial)
				finished <- err
			}()
			select {
			case <-wire.delayB:
			case err := <-finished:
				t.Fatalf("old write did not reach delayed storage: %v", err)
			case <-time.After(5 * time.Second):
				t.Fatal("old write did not arrive")
			}
			current, err := repo.MetadataView().GetMeeting(context.Background(), "owner", "m")
			if err != nil || current.Notes != "A" || current.NotesRevision != initial {
				t.Fatalf("readback before delayed commit: meeting=%+v err=%v", current, err)
			}
			revision, err := repo.UpdateMeetingNotesWithRevision(context.Background(), "owner", "m", "A", "A", &initial)
			if _, parseErr := uuid.Parse(revision); err != nil || parseErr != nil || revision == initial {
				t.Fatalf("same-text fence did not persist a fresh UUID: revision=%q err=%v", revision, err)
			}
			release.Do(func() { close(wire.releaseB) })
			if err := <-finished; !errors.Is(err, ErrConditionFailed) {
				t.Fatalf("delayed B survived A->A fence: %v", err)
			}
			if wire.notes != "A" || wire.revision != revision {
				t.Fatal("delayed write overwrote the fenced notes")
			}
		})
	}
}

func TestEveryRepositoryNotesMutationAdvancesRevision(t *testing.T) {
	wire := &notesRevisionWire{notes: "A", hasNotes: true}
	repo := wire.repository(t)
	ctx := context.Background()
	for _, tc := range []struct {
		name  string
		write func() error
	}{
		{"legacy partial", func() error { return repo.UpdateMeetingFields(ctx, "owner", "m", map[string]any{"notes": "A"}) }},
		{"generic conditional", func() error {
			return repo.UpdateMeetingFieldsIfMatch(ctx, "owner", "m", map[string]any{"notes": "A"}, map[string]any{"notes": "A"})
		}},
		{"text-only CAS", func() error { return repo.UpdateMeetingNotesIfMatch(ctx, "owner", "m", "A", "A") }},
		{"full legacy replacement", func() error {
			return repo.UpdateMeeting(ctx, &model.Meeting{UserID: "owner", MeetingID: "m", Notes: "A", NotesRevision: "caller-supplied"})
		}},
		{"revision-returning partial", func() error {
			version, err := repo.UpdateMeetingFieldsWithNotesRevision(ctx, "owner", "m", map[string]any{"notes": "A"})
			if err == nil && version != wire.revision {
				t.Error("response did not return the persisted revision")
			}
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := wire.revision
			if err := tc.write(); err != nil {
				t.Fatal(err)
			}
			if _, err := uuid.Parse(wire.revision); err != nil || wire.revision == before {
				t.Fatalf("notes write did not advance revision: %q", wire.revision)
			}
		})
	}
	before := wire.revision
	if err := repo.UpdateMeetingFields(ctx, "owner", "m", map[string]any{"title": "new title"}); err != nil || wire.revision != before {
		t.Fatal("unrelated update changed notes revision")
	}
	if err := repo.UpdateMeetingFields(ctx, "owner", "m", map[string]any{"notesRevision": "chosen"}); err == nil || wire.revision != before {
		t.Fatal("standalone revision assignment was accepted")
	}
}

func TestNotesRevisionMatchesAbsentLegacyNotes(t *testing.T) {
	wire := &notesRevisionWire{}
	repo := wire.repository(t)
	empty := ""
	version, err := repo.UpdateMeetingNotesWithRevision(context.Background(), "owner", "m", "", "", &empty)
	if err != nil || version == "" || wire.revision != version {
		t.Fatalf("absent legacy notes/revision fence failed: version=%q err=%v", version, err)
	}
	if _, err := repo.UpdateMeetingNotesWithRevision(context.Background(), "owner", "m", "", "changed", &empty); !errors.Is(err, ErrConditionFailed) {
		t.Fatalf("empty revision matched a now-versioned legacy row: %v", err)
	}
}
