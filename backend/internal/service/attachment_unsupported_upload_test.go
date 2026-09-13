package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	dbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

func TestUnsupportedDocumentUploadRemainsDownloadableWithoutQueueing(t *testing.T) {
	meeting := &model.Meeting{UserID: "owner", MeetingID: "m"}
	var attachment *model.Attachment
	state := map[string]dbtypes.AttributeValue{}
	transport := noteSourceHTTPClient(func(req *http.Request) (*http.Response, error) {
		var body map[string]interface{}
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		switch target := req.Header.Get("X-Amz-Target"); {
		case strings.HasSuffix(target, ".GetItem"):
			key := body["Key"].(map[string]interface{})
			if key["PK"].(map[string]interface{})["S"] == "USER#owner" {
				return attachmentWireResponse(map[string]interface{}{"Item": attachmentWireItem(t, meeting)}), nil
			}
			if attachment != nil && key["SK"].(map[string]interface{})["S"] == "ATTACH#"+attachment.AttachmentID {
				return attachmentWireResponse(map[string]interface{}{"Item": attachmentWireItem(t, attachment)}), nil
			}
			if strings.HasPrefix(key["SK"].(map[string]interface{})["S"].(string), "ATTEXT#") {
				return attachmentWireResponse(map[string]interface{}{"Item": state}), nil
			}
		case strings.HasSuffix(target, ".TransactWriteItems"):
			ops := body["TransactItems"].([]interface{})
			for _, raw := range ops {
				if put, ok := raw.(map[string]interface{})["Put"]; ok {
					fields := put.(map[string]interface{})["Item"].(map[string]interface{})
					text := func(key string) string { return fields[key].(map[string]interface{})["S"].(string) }
					attachment = &model.Attachment{AttachmentID: text("attachmentId"), MeetingID: text("meetingId"),
						UserID: text("userId"), OriginalKey: text("originalKey"), FileName: text("fileName"),
						Type: text("type"), Status: text("status")}
				}
				if update, ok := raw.(map[string]interface{})["Update"]; ok {
					attachmentApplySet(t, update.(map[string]interface{}), state)
					if state["status"].(*dbtypes.AttributeValueMemberS).Value != model.AttachmentTextFailed ||
						state["errorCode"].(*dbtypes.AttributeValueMemberS).Value != "UNSUPPORTED_FORMAT" {
						t.Fatal("unsupported document must be recorded as failed, never queued")
					}
				}
			}
			return attachmentWireResponse(map[string]interface{}{}), nil
		}
		t.Fatalf("unsupported document reached unexpected operation %s", req.Header.Get("X-Amz-Target"))
		return nil, errors.New("unexpected request")
	})
	cfg := aws.Config{Region: "ap-northeast-2", Credentials: aws.AnonymousCredentials{}, HTTPClient: transport}
	storage := s3.NewFromConfig(cfg)
	repo := repository.NewDynamoDBRepositoryWithS3(dynamodb.NewFromConfig(cfg), "table", storage, "bucket")
	upload := NewUploadService(storage, repo, "bucket", eventbridge.NewFromConfig(cfg))
	if err := upload.CompleteUpload(context.Background(), "owner", &model.UploadCompleteRequest{
		MeetingID: "m", Key: "files/owner/m/발표.ppt", Category: "file",
		FileName: "발표.ppt", MimeType: "application/vnd.ms-powerpoint",
	}); err != nil {
		t.Fatalf("original upload was rejected: %v", err)
	}
	if attachment == nil || attachment.Status != model.AttachStatusDone {
		t.Fatal("uploaded attachment metadata missing")
	}
	status, err := upload.attachmentText.GetStatus(context.Background(), "owner", "m", attachment.AttachmentID)
	if err != nil || status.Status != model.AttachmentTextFailed || status.ErrorCode != "UNSUPPORTED_FORMAT" || status.HasResult {
		t.Fatalf("unsupported extraction must remain explicit: %+v %v", status, err)
	}
}
