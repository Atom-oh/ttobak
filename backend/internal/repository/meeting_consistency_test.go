package repository

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/ttobak/backend/internal/model"
)

func TestMeetingReadsUseStrongCanonicalNotesRevision(t *testing.T) {
	for _, mode := range []string{"detail-owner", "detail-by-id", "metadata-owner", "metadata-by-id"} {
		t.Run(mode, func(t *testing.T) {
			primaryReads := 0
			client := dynamodb.New(dynamodb.Options{
				Region: "ap-northeast-2", Credentials: aws.AnonymousCredentials{}, RetryMaxAttempts: 1,
				HTTPClient: meetingListHTTPClientFunc(func(req *http.Request) (*http.Response, error) {
					body := `{"Items":[{"meetingId":{"S":"m"},"userId":{"S":"owner"},"notes":{"S":"old"},"notesRevision":{"S":"old-version"}}]}`
					if req.Header.Get("X-Amz-Target") == "DynamoDB_20120810.GetItem" {
						primaryReads++
						var input struct {
							Key            map[string]map[string]string
							ConsistentRead bool
						}
						if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
							t.Fatal(err)
						}
						if !input.ConsistentRead || input.Key["PK"]["S"] != "USER#owner" || input.Key["SK"]["S"] != "MEETING#m" {
							t.Fatalf("snapshot did not read the exact strong primary key: %+v", input)
						}
						body = `{"Item":{"meetingId":{"S":"m"},"userId":{"S":"owner"},"notes":{"S":"acknowledged"},"notesRevision":{"S":"current-version"}}}`
					} else if req.Header.Get("X-Amz-Target") != "DynamoDB_20120810.Query" {
						t.Fatalf("unexpected operation %s", req.Header.Get("X-Amz-Target"))
					}
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/x-amz-json-1.0"}},
						Body: io.NopCloser(strings.NewReader(body))}, nil
				}),
			})
			repo := NewDynamoDBRepository(client, "meeting-consistency")
			if strings.HasPrefix(mode, "metadata") {
				repo = repo.MetadataView()
			}
			var meeting *model.Meeting
			var err error
			if strings.HasSuffix(mode, "by-id") {
				meeting, err = repo.GetMeetingByID(context.Background(), "m")
			} else {
				meeting, err = repo.GetMeeting(context.Background(), "owner", "m")
			}
			if err != nil || primaryReads != 1 || meeting.Notes != "acknowledged" || meeting.NotesRevision != "current-version" {
				t.Fatalf("stale source replaced canonical notes snapshot: meeting=%+v primaryReads=%d err=%v", meeting, primaryReads, err)
			}
		})
	}
}
