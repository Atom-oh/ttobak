package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	chiadapter "github.com/awslabs/aws-lambda-go-api-proxy/chi"
	"github.com/go-chi/chi/v5"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
	"github.com/ttobak/backend/internal/service"
)

type attachmentHTTP func(*http.Request) (*http.Response, error)

func (f attachmentHTTP) Do(r *http.Request) (*http.Response, error) { return f(r) }

func TestAttachmentAPIRealServiceGatewayV1BoundAndMetadataAuth(t *testing.T) {
	text := strings.Repeat("장문 한국어𐐀<&>\n", 5000)
	result := model.AttachmentTextResult{SchemaVersion: 1, Format: "md", Status: "succeeded", Complete: true, Scope: "markdown_source",
		Source: model.AttachmentTextSource{Bucket: "bucket", Key: "files/owner/m/file.md", ETag: `"tag"`, MeetingID: "m", OwnerID: "owner", UploaderID: "owner", AttachmentID: "a", RunID: "run"},
		Units:  []model.AttachmentTextUnit{{Text: text, Location: map[string]interface{}{"kind": "paragraph", "paragraph": 1, "startLine": 1, "endLine": 1}}}}
	result.Metrics.Units = 1
	result.Metrics.TextBytes = len(text)
	data, _ := json.Marshal(result)
	s3Calls, reads := 0, 0
	response := func(body string, headers http.Header) *http.Response {
		return &http.Response{StatusCode: 200, Header: headers, Body: io.NopCloser(strings.NewReader(body)), ContentLength: int64(len(body))}
	}
	transport := attachmentHTTP(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.Header.Get("X-Amz-Target"), ".GetItem") {
			reads++
			var input map[string]interface{}
			json.NewDecoder(r.Body).Decode(&input)
			if input["ConsistentRead"] != true {
				t.Fatal("authorization read is not current")
			}
			key := input["Key"].(map[string]interface{})["SK"].(map[string]interface{})["S"].(string)
			item := map[string]interface{}{}
			put := func(key, value string) { item[key] = map[string]string{"S": value} }
			switch key {
			case "MEETING#m":
				put("meetingId", "m")
				put("userId", "owner")
				put("content", "previous summary")
				put("transcriptA", "s3://bucket/transcripts/m/transcriptA.txt")
			case "ATTACH#a":
				put("attachmentId", "a")
				put("meetingId", "m")
				put("userId", "owner")
				put("originalKey", "files/owner/m/file.md")
				put("type", "document")
				put("status", "done")
			case "ATTEXT#a":
				put("runId", "run")
				put("status", "succeeded")
				put("sourceKey", "files/owner/m/file.md")
				put("ownerId", "owner")
				put("uploaderId", "owner")
				put("sourceETag", `"tag"`)
				put("resultKey", "files/owner/m/text/a/run.json")
				item["unitCount"] = map[string]string{"N": "1"}
				item["complete"] = map[string]bool{"BOOL": true}
			default:
				t.Fatalf("unexpected key %s", key)
			}
			body, _ := json.Marshal(map[string]interface{}{"Item": item})
			return response(string(body), http.Header{"Content-Type": {"application/x-amz-json-1.0"}}), nil
		}
		s3Calls++
		if strings.Contains(r.URL.Path, "transcripts/") {
			t.Fatal("metadata route hydrated a transcript")
		}
		if r.Method == "HEAD" && strings.HasSuffix(r.URL.Path, "files/owner/m/file.md") {
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Length": {"10"}, "Etag": {`"tag"`}}, Body: io.NopCloser(strings.NewReader(""))}, nil
		}
		if r.Method == "GET" && strings.HasSuffix(r.URL.Path, "files/owner/m/text/a/run.json") {
			return response(string(data), http.Header{"Content-Length": {strconv.Itoa(len(data))}, "Content-Type": {"application/json"}}), nil
		}
		t.Fatalf("unexpected S3 request %s %s", r.Method, r.URL.Path)
		return nil, errors.New("unexpected")
	})
	cfg := aws.Config{Region: "ap-northeast-2", Credentials: aws.AnonymousCredentials{}, HTTPClient: transport}
	db := dynamodb.NewFromConfig(cfg)
	storage := s3.NewFromConfig(cfg)
	repo := repository.NewDynamoDBRepositoryWithS3(db, "table", storage, "bucket")
	meta := repo.MetadataView()
	s := service.NewAttachmentTextService(meta, service.NewMeetingService(meta), storage, "bucket", nil)
	h := NewAttachmentTextHandler(s)
	router := chi.NewRouter()
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { next.ServeHTTP(w, withUserCtx(r, "owner")) })
	})
	router.Get("/api/meetings/{meetingId}/attachments/{attachmentId}/text", h.Read)
	router.Get("/api/meetings/{meetingId}/attachments/{attachmentId}/text/status", h.Status)
	proxy := chiadapter.New(router)
	base := "/api/meetings/m/attachments/a/text"
	status, err := proxy.ProxyWithContext(context.Background(), events.APIGatewayProxyRequest{HTTPMethod: "GET", Path: base + "/status"})
	if err != nil || status.StatusCode != 200 || s3Calls != 0 {
		t.Fatalf("status=%+v error=%v S3=%d", status, err, s3Calls)
	}
	var reconstructed strings.Builder
	cursor := ""
	pages := 0
	for {
		out, err := proxy.ProxyWithContext(context.Background(), events.APIGatewayProxyRequest{HTTPMethod: "GET", Path: base, QueryStringParameters: map[string]string{"pageSize": "6000", "cursor": cursor}})
		if err != nil || out.StatusCode != 200 || len(out.Body) > service.AttachmentTextPageLimit {
			t.Fatalf("status=%d bytes=%d error=%v body=%.150s", out.StatusCode, len(out.Body), err, out.Body)
		}
		var page service.AttachmentTextPage
		if err := json.Unmarshal([]byte(out.Body), &page); err != nil {
			t.Fatal(err)
		}
		if !page.Current || len(page.Units) > 50 {
			t.Fatal("invalid current/page state")
		}
		for _, chunk := range page.Units {
			reconstructed.WriteString(chunk.Text)
		}
		pages++
		cursor = page.NextCursor
		if cursor == "" {
			break
		}
	}
	if reconstructed.String() != text || pages < 2 || reads < pages*3 {
		t.Fatalf("reconstruction=%t pages=%d reads=%d", reconstructed.String() == text, pages, reads)
	}
}

type attachmentHandlerStub struct {
	calls int
	err   error
}

func (s *attachmentHandlerStub) GetStatus(context.Context, string, string, string) (*model.AttachmentTextStatus, error) {
	s.calls++
	return &model.AttachmentTextStatus{Status: "unknown"}, s.err
}
func (s *attachmentHandlerStub) Request(ctx context.Context, u, m, a string) (*model.AttachmentTextStatus, error) {
	return s.GetStatus(ctx, u, m, a)
}
func (s *attachmentHandlerStub) Read(context.Context, string, string, string, string, int) (*service.AttachmentTextPage, error) {
	s.calls++
	return &service.AttachmentTextPage{}, s.err
}
func TestAttachmentHTTPRejectsUnknownDuplicateAndInvalidQueries(t *testing.T) {
	for _, query := range []string{"startTime=1", "pageSize=0", "pageSize=-1", "pageSize=6001", "pageSize=x", "cursor=a&cursor=b", "pageSize=1&pageSize=2", "cursor=%zz"} {
		stub := &attachmentHandlerStub{}
		h := &AttachmentTextHandler{service: stub}
		r := httptest.NewRequest("GET", "/text?"+query, nil)
		w := httptest.NewRecorder()
		h.Read(w, r)
		if w.Code != 400 || stub.calls != 0 {
			t.Fatalf("%s: %d calls=%d", query, w.Code, stub.calls)
		}
	}
	for _, test := range []struct {
		err    error
		status int
	}{{service.ErrForbidden, 403}, {service.ErrNotFound, 404}, {service.ErrUnsupportedAttachment, 422}, {service.ErrAttachmentCursor, 409}, {service.ErrAttachmentPublish, 500}, {errors.New("secret document text"), 500}} {
		h := &AttachmentTextHandler{service: &attachmentHandlerStub{err: test.err}}
		w := httptest.NewRecorder()
		h.Retry(w, httptest.NewRequest("POST", "/retry", nil))
		if w.Code != test.status || strings.Contains(w.Body.String(), "secret document") {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
	}
}
