package repository

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// Exercise the real SDK wire boundary: a stale rename must not alter any
// object referenced before the conditional database write.
func TestConditionalTranscriptSpillNeverOverwritesReferencedObject(t *testing.T) {
	for _, outcome := range []string{"rejected", "committed", "ambiguous"} {
		t.Run(outcome, func(t *testing.T) {
			const oldKey = "transcripts/m1/transcriptA.txt"
			const oldText = "newer concurrent transcript"
			objects := map[string]string{oldKey: oldText}
			ref := "s3://synthetic/" + oldKey
			var uploaded string
			writes := 0
			wire := meetingListHTTPClientFunc(func(req *http.Request) (*http.Response, error) {
				status, body := 200, "{}"
				switch req.Header.Get("X-Amz-Target") {
				case "DynamoDB_20120810.GetItem":
					body = `{"Item":{"transcriptA":{"S":"` + ref + `"}}}`
				case "DynamoDB_20120810.UpdateItem":
					writes++
					var input struct {
						Values map[string]struct{ S string } `json:"ExpressionAttributeValues"`
					}
					if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
						t.Fatal(err)
					}
					if outcome != "rejected" {
						for _, value := range input.Values {
							if strings.HasPrefix(value.S, "s3://synthetic/") {
								ref = value.S
							}
						}
					}
					if outcome == "rejected" {
						status, body = 400, `{"__type":"com.amazonaws.dynamodb.v20120810#ConditionalCheckFailedException","message":"newer version"}`
					} else if outcome == "ambiguous" {
						// Simulate commit followed by a lost/error response.
						status, body = 500, `{"__type":"com.amazonaws.dynamodb.v20120810#InternalServerError","message":"response lost"}`
					}
				default:
					key := strings.TrimPrefix(req.URL.Path, "/")
					switch req.Method {
					case http.MethodPut:
						uploaded = key
						data, err := io.ReadAll(req.Body)
						if err != nil {
							t.Fatal(err)
						}
						objects[key] = string(data)
					case http.MethodDelete:
						delete(objects, key)
						status, body = 204, ""
					default:
						t.Fatalf("unexpected S3 request %s %s", req.Method, key)
					}
				}
				return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/x-amz-json-1.0"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
			})
			repo := NewDynamoDBRepositoryWithS3(
				dynamodb.New(dynamodb.Options{Region: "ap-northeast-2", Credentials: aws.AnonymousCredentials{}, HTTPClient: wire, RetryMaxAttempts: 2}),
				"test",
				s3.New(s3.Options{Region: "ap-northeast-2", Credentials: aws.AnonymousCredentials{}, HTTPClient: wire, RetryMaxAttempts: 1}),
				"synthetic",
			)
			text := strings.Repeat("stale snapshot ", 24000)
			fields := map[string]interface{}{"transcriptA": text}
			err := repo.UpdateMeetingFieldsIfMatch(context.Background(), "owner", "m1",
				map[string]interface{}{"updatedAt": "old"},
				fields)
			if fields["transcriptA"] != text {
				t.Fatal("caller retry input was replaced with an internal spill reference")
			}
			if objects[oldKey] != oldText {
				t.Fatal("conditional writer overwrote the object referenced by newer data")
			}
			if !regexp.MustCompile(`^transcripts/m1/transcriptA\.[0-9a-f]{32}\.txt$`).MatchString(uploaded) {
				t.Fatalf("spill did not get an immutable key: %s", uploaded)
			}
			_, retained := objects[uploaded]
			switch outcome {
			case "rejected":
				if !errors.Is(err, ErrConditionFailed) || retained || ref != "s3://synthetic/"+oldKey {
					t.Fatalf("rejected spill cleanup/reference: err=%v retained=%v ref=%s", err, retained, ref)
				}
			case "committed":
				if err != nil || !retained || ref != "s3://synthetic/"+uploaded {
					t.Fatalf("committed spill missing: err=%v retained=%v ref=%s", err, retained, ref)
				}
			case "ambiguous":
				if err == nil || !retained || writes != 1 {
					t.Fatalf("uncertain commit retried or its object deleted: err=%v retained=%v writes=%d", err, retained, writes)
				}
			}
		})
	}
}
