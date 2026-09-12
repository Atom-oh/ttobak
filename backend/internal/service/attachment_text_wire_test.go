package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	dbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

func attachmentWireResponse(value interface{}) *http.Response {
	if envelope, ok := value.(map[string]interface{}); ok {
		if item, ok := envelope["Item"].(map[string]dbtypes.AttributeValue); ok {
			wire := map[string]interface{}{}
			for key, value := range item {
				switch v := value.(type) {
				case *dbtypes.AttributeValueMemberS:
					wire[key] = map[string]interface{}{"S": v.Value}
				case *dbtypes.AttributeValueMemberN:
					wire[key] = map[string]interface{}{"N": v.Value}
				case *dbtypes.AttributeValueMemberBOOL:
					wire[key] = map[string]interface{}{"BOOL": v.Value}
				case *dbtypes.AttributeValueMemberNULL:
					wire[key] = map[string]interface{}{"NULL": v.Value}
				}
			}
			envelope["Item"] = wire
		}
	}
	body, _ := json.Marshal(value)
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/x-amz-json-1.0"}}, Body: io.NopCloser(strings.NewReader(string(body)))}
}
func attachmentWireItem(t *testing.T, value interface{}) map[string]dbtypes.AttributeValue {
	t.Helper()
	item, err := attributevalue.MarshalMap(value)
	if err != nil {
		t.Fatal(err)
	}
	return item
}
func attachmentApplySet(t *testing.T, op map[string]interface{}, item map[string]dbtypes.AttributeValue) {
	t.Helper()
	names := op["ExpressionAttributeNames"].(map[string]interface{})
	for _, assignment := range strings.Split(strings.TrimPrefix(op["UpdateExpression"].(string), "SET "), ",") {
		pair := strings.SplitN(assignment, "=", 2)
		name := names[strings.TrimSpace(pair[0])].(string)
		value := op["ExpressionAttributeValues"].(map[string]interface{})[strings.TrimSpace(pair[1])].(map[string]interface{})
		switch {
		case value["S"] != nil:
			item[name] = &dbtypes.AttributeValueMemberS{Value: value["S"].(string)}
		case value["N"] != nil:
			item[name] = &dbtypes.AttributeValueMemberN{Value: value["N"].(string)}
		case value["BOOL"] != nil:
			item[name] = &dbtypes.AttributeValueMemberBOOL{Value: value["BOOL"].(bool)}
		default:
			t.Fatalf("unsupported wire value %v", value)
		}
	}
}
func TestCompleteDocumentUploadRealSDKQueuesAndPersistsEventFailure(t *testing.T) {
	for _, publishFails := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "delivery failure"}[publishFails], func(t *testing.T) {
			var attachment *model.Attachment
			state := map[string]dbtypes.AttributeValue{}
			events, claims, s3Calls := 0, 0, 0
			meeting := &model.Meeting{UserID: "owner", MeetingID: "m", TranscriptA: "s3://bucket/transcripts/m/transcriptA.txt"}
			transport := noteSourceHTTPClient(func(req *http.Request) (*http.Response, error) {
				var body map[string]interface{}
				if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				switch target := req.Header.Get("X-Amz-Target"); {
				case strings.HasSuffix(target, ".GetItem"):
					if body["ConsistentRead"] != true {
						t.Fatal("metadata auth/state lookup is not strong")
					}
					key := body["Key"].(map[string]interface{})
					pk := key["PK"].(map[string]interface{})["S"].(string)
					sk := key["SK"].(map[string]interface{})["S"].(string)
					if pk == "USER#owner" {
						return attachmentWireResponse(map[string]interface{}{"Item": attachmentWireItem(t, meeting)}), nil
					}
					if strings.HasPrefix(sk, "ATTEXT#") {
						return attachmentWireResponse(map[string]interface{}{"Item": state}), nil
					}
					if strings.HasPrefix(sk, "ATTACH#") && attachment != nil {
						return attachmentWireResponse(map[string]interface{}{"Item": attachmentWireItem(t, attachment)}), nil
					}
					t.Fatalf("unexpected lookup %s %s", pk, sk)
				case strings.HasSuffix(target, ".TransactWriteItems"):
					ops := body["TransactItems"].([]interface{})
					for _, raw := range ops {
						op := raw.(map[string]interface{})
						if put, ok := op["Put"]; ok {
							fields := put.(map[string]interface{})["Item"].(map[string]interface{})
							get := func(key string) string {
								v, ok := fields[key].(map[string]interface{})
								if !ok {
									return ""
								}
								value, _ := v["S"].(string)
								return value
							}
							attachment = &model.Attachment{AttachmentID: get("attachmentId"), MeetingID: get("meetingId"), UserID: get("userId"), OriginalKey: get("originalKey"), Type: get("type"), Status: get("status"), FileName: get("fileName")}
							if len(ops) != 2 || get("status") != "done" || get("fileName") != "노트.md" {
								t.Fatalf("non-atomic metadata upload %v", ops)
							}
						}
						if update, ok := op["Update"]; ok {
							claims++
							if len(ops) != 3 {
								t.Fatal("queue missing source checks")
							}
							attachmentApplySet(t, update.(map[string]interface{}), state)
						}
					}
					return attachmentWireResponse(map[string]interface{}{}), nil
				case strings.HasSuffix(target, ".UpdateItem"):
					attachmentApplySet(t, body, state)
					return attachmentWireResponse(map[string]interface{}{}), nil
				case strings.HasSuffix(target, ".PutEvents"):
					events++
					entries := body["Entries"].([]interface{})
					entry := entries[0].(map[string]interface{})
					var detail model.DocumentUploadCompleted
					json.Unmarshal([]byte(entry["Detail"].(string)), &detail)
					if entry["Source"] != "ttobak.upload" || entry["DetailType"] != "DocumentUploadCompleted" || detail.AttachmentID != attachment.AttachmentID || detail.UserID != "owner" || detail.OwnerID != "owner" || detail.Key != attachment.OriginalKey || detail.RunID == "" {
						t.Fatalf("bad event %+v", detail)
					}
					if publishFails {
						return attachmentWireResponse(map[string]interface{}{"FailedEntryCount": 1, "Entries": []interface{}{map[string]string{"ErrorCode": "InternalFailure"}}}), nil
					}
					return attachmentWireResponse(map[string]interface{}{"FailedEntryCount": 0, "Entries": []interface{}{map[string]string{"EventId": "event"}}}), nil
				default:
					s3Calls++
					t.Fatalf("unexpected AWS operation: %s", target)
				}
				return nil, errors.New("unexpected call")
			})
			cfg := aws.Config{Region: "ap-northeast-2", Credentials: aws.AnonymousCredentials{}, HTTPClient: transport}
			db := dynamodb.NewFromConfig(cfg)
			storage := s3.NewFromConfig(cfg)
			eventsClient := eventbridge.NewFromConfig(cfg)
			repo := repository.NewDynamoDBRepositoryWithS3(db, "table", storage, "bucket")
			upload := NewUploadService(storage, repo, "bucket", eventsClient)
			err := upload.CompleteUpload(context.Background(), "owner", &model.UploadCompleteRequest{MeetingID: "m", Key: "files/owner/m/노트.md", Category: "file", FileName: "노트.md", MimeType: "text/markdown"})
			if publishFails != errors.Is(err, ErrAttachmentPublish) || !publishFails && err != nil {
				t.Fatalf("error=%v", err)
			}
			var actual model.AttachmentTextState
			if err := attributevalue.UnmarshalMap(state, &actual); err != nil {
				t.Fatal(err)
			}
			if publishFails && (actual.Status != "failed" || actual.ErrorCode != "PUBLISH_FAILED") {
				t.Fatalf("%+v", actual)
			}
			if !publishFails && actual.Status != "queued" {
				t.Fatalf("%+v", actual)
			}
			if events != 1 || claims != 1 || s3Calls != 0 {
				t.Fatalf("events=%d claims=%d S3=%d", events, claims, s3Calls)
			}
		})
	}
}

func TestAttachmentEditorRetryUsesCanonicalUploaderAndSuppressesDuplicate(t *testing.T) {
	s, r := attachmentFixture(t)
	r.meetingsByID["m"] = r.meetings[meetingKey("owner", "m")]
	r.shares[shareKey("editor", "m")] = &model.Share{MeetingID: "m", OwnerID: "owner", SharedToID: "editor", Permission: model.PermissionEdit}
	published := 0
	s.publish = func(_ context.Context, event model.DocumentUploadCompleted) error {
		published++
		if event.OwnerID != "owner" || event.UserID != "owner" {
			t.Fatalf("requester substituted uploader: %+v", event)
		}
		return nil
	}
	for i := 0; i < 2; i++ {
		if _, err := s.Request(context.Background(), "editor", "m", "a"); err != nil {
			t.Fatal(err)
		}
	}
	if published != 1 || r.queues != 1 {
		t.Fatalf("duplicate work: %d %d", published, r.queues)
	}
}
