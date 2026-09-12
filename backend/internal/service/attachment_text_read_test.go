package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/ttobak/backend/internal/model"
)

type attachmentS3Mock struct {
	body                []byte
	etag                string
	heads, gets, closed int
	onGet               func()
	length              *int64
}

func (m *attachmentS3Mock) HeadObject(context.Context, *s3.HeadObjectInput, ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	m.heads++
	return &s3.HeadObjectOutput{ETag: aws.String(m.etag), ContentLength: aws.Int64(10)}, nil
}

type attachmentBody struct {
	io.Reader
	close func()
}

func (b attachmentBody) Close() error { b.close(); return nil }
func (m *attachmentS3Mock) GetObject(_ context.Context, in *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	m.gets++
	if m.onGet != nil {
		m.onGet()
	}
	if aws.ToString(in.Key) != "files/owner/m/text/a/result.json" {
		return nil, errors.New("unexpected object key")
	}
	length := aws.Int64(int64(len(m.body)))
	if m.length != nil {
		length = m.length
	}
	return &s3.GetObjectOutput{Body: attachmentBody{strings.NewReader(string(m.body)), func() { m.closed++ }}, ContentLength: length}, nil
}
func readingFixture(t *testing.T) (*AttachmentTextService, *attachmentTestRepo, *attachmentS3Mock, *model.AttachmentTextResult) {
	t.Helper()
	s, r := attachmentFixture(t)
	r.meetingsByID["m"] = r.meetings[meetingKey("owner", "m")]
	r.state = &model.AttachmentTextState{RunID: "result", Status: "succeeded", OwnerID: "owner", UploaderID: "owner", SourceKey: r.attachment.OriginalKey, ResultKey: "files/owner/m/text/a/result.json", SourceETag: `"etag"`, UnitCount: 1, Complete: true}
	result := &model.AttachmentTextResult{SchemaVersion: 1, Format: "md", Status: "succeeded", Complete: true, Scope: "markdown_source",
		Source: model.AttachmentTextSource{Bucket: "bucket", Key: r.attachment.OriginalKey, ETag: `"etag"`, MeetingID: "m", OwnerID: "owner", UploaderID: "owner", AttachmentID: "a", RunID: "result"},
		Units:  []model.AttachmentTextUnit{{Text: "한국어 문서", Location: map[string]interface{}{"kind": "paragraph", "paragraph": 1, "startLine": 1, "endLine": 1}}}}
	result.Metrics.Units = 1
	result.Metrics.TextBytes = len(result.Units[0].Text)
	body, _ := json.Marshal(result)
	storage := &attachmentS3Mock{body: body, etag: `"etag"`}
	s.s3 = storage
	return s, r, storage, result
}
func TestAttachmentReadingUnicodePagesAndStaleCursor(t *testing.T) {
	s, r, storage, result := readingFixture(t)
	want := strings.Repeat("한국어𐐀<&>\n", 4000)
	result.Units[0].Text = want
	result.Metrics.TextBytes = len(want)
	storage.body, _ = json.Marshal(result)
	cursor := ""
	var got strings.Builder
	offset := 0
	pages := 0
	for {
		page, err := s.Read(context.Background(), "owner", "m", "a", cursor, 6000)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := json.Marshal(page)
		if len(body)+1 > AttachmentTextPageLimit || len(page.Units) > 50 {
			t.Fatalf("page bound %d %d", len(body), len(page.Units))
		}
		for _, chunk := range page.Units {
			if !utf8.ValidString(chunk.Text) || chunk.StartOffset != offset || chunk.EndOffset-chunk.StartOffset != utf8.RuneCountInString(chunk.Text) {
				t.Fatalf("offsets %+v", chunk)
			}
			offset = chunk.EndOffset
			got.WriteString(chunk.Text)
		}
		pages++
		if page.NextCursor == "" {
			if !page.PageComplete {
				t.Fatal("completion missing")
			}
			break
		}
		cursor = page.NextCursor
	}
	if pages < 2 || got.String() != want || storage.closed != pages {
		t.Fatalf("pages=%d closed=%d reconstructed=%t", pages, storage.closed, got.String() == want)
	}
	r.state.RunID = "retry"
	r.state.Status = "failed"
	if _, err := s.Read(context.Background(), "owner", "m", "a", cursor, 6000); !errors.Is(err, ErrAttachmentCursor) {
		t.Fatalf("stale cursor %v", err)
	}
}
func TestAttachmentReadReauthRevocationAndRetainedFailure(t *testing.T) {
	s, r, storage, _ := readingFixture(t)
	r.shares[shareKey("reader", "m")] = &model.Share{MeetingID: "m", OwnerID: "owner", SharedToID: "reader", Permission: model.PermissionRead}
	if _, err := s.Request(context.Background(), "reader", "m", "a"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("reader retry %v", err)
	}
	if storage.heads != 0 {
		t.Fatal("unauthorized operation touched S3")
	}
	storage.onGet = func() { delete(r.shares, shareKey("reader", "m")) }
	if _, err := s.Read(context.Background(), "reader", "m", "a", "", 100); err == nil {
		t.Fatal("revoked reader received document")
	}
	storage.onGet = nil
	r.state.RunID = "new"
	r.state.Status = "failed"
	r.state.ErrorCode = "TIMEOUT"
	page, err := s.Read(context.Background(), "owner", "m", "a", "", 100)
	if err != nil || page.Current || page.Analysis.Complete || !page.Complete || page.Analysis.Status != "failed" || page.Source.RunID != "result" {
		t.Fatalf("%+v %v", page, err)
	}
}
func TestAttachmentReadRejectsProvenanceAndOversizedObject(t *testing.T) {
	for _, field := range []string{"bucket", "key", "eTag", "meetingId", "ownerId", "uploaderId", "attachmentId", "runId"} {
		t.Run(field, func(t *testing.T) {
			s, _, storage, _ := readingFixture(t)
			var body map[string]interface{}
			json.Unmarshal(storage.body, &body)
			body["source"].(map[string]interface{})[field] = "other"
			storage.body, _ = json.Marshal(body)
			if _, err := s.Read(context.Background(), "owner", "m", "a", "", 100); !errors.Is(err, ErrAttachmentSourceChanged) {
				t.Fatalf("%v", err)
			}
			if storage.closed != 1 {
				t.Fatal("body leaked")
			}
		})
	}
	s, _, storage, _ := readingFixture(t)
	storage.etag = `"overwritten"`
	if _, err := s.Read(context.Background(), "owner", "m", "a", "", 100); !errors.Is(err, ErrAttachmentSourceChanged) || storage.gets != 0 {
		t.Fatalf("%v gets=%d", err, storage.gets)
	}
	s, _, storage, _ = readingFixture(t)
	storage.length = aws.Int64(AttachmentTextResultLimit + 1)
	if _, err := s.Read(context.Background(), "owner", "m", "a", "", 100); !errors.Is(err, ErrAttachmentUnavailable) || storage.closed != 1 {
		t.Fatalf("%v closed=%d", err, storage.closed)
	}
	s, _, storage, _ = readingFixture(t)
	storage.length = aws.Int64(1)
	storage.body = []byte(strings.Repeat("x", AttachmentTextResultLimit+1))
	if _, err := s.Read(context.Background(), "owner", "m", "a", "", 100); !errors.Is(err, ErrAttachmentUnavailable) {
		t.Fatalf("%v", err)
	}
	s, _, storage, _ = readingFixture(t)
	storage.onGet = func() { storage.etag = `"replaced-during-read"` }
	if _, err := s.Read(context.Background(), "owner", "m", "a", "", 100); !errors.Is(err, ErrAttachmentSourceChanged) {
		t.Fatalf("concurrent replacement: %v", err)
	}
}
func TestAttachmentReadingAcceptsPythonPPTXLocationWithoutInventedTimes(t *testing.T) {
	s, r, storage, result := readingFixture(t)
	r.attachment.OriginalKey = "files/owner/m/deck.pptx"
	r.state.SourceKey = r.attachment.OriginalKey
	result.Source.Key = r.attachment.OriginalKey
	result.Format = "pptx"
	result.Scope = "slide_body"
	result.Units[0].Location = map[string]interface{}{"kind": "slide", "slide": 1, "paragraph": 1, "part": "ppt/slides/slide10.xml", "hidden": false}
	storage.body, _ = json.Marshal(result)
	page, err := s.Read(context.Background(), "owner", "m", "a", "", 100)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := page.Units[0].Location["startTime"]; ok {
		t.Fatal("invented audio location")
	}
}
func TestAttachmentPageChunkLimitAndInvalidCursor(t *testing.T) {
	_, r, _, result := readingFixture(t)
	result.Units = nil
	for i := 0; i < 80; i++ {
		result.Units = append(result.Units, model.AttachmentTextUnit{Text: "글", Location: map[string]interface{}{"kind": "paragraph", "paragraph": i + 1, "startLine": i + 1, "endLine": i + 1}})
	}
	page, err := renderAttachmentPage(&model.AttachmentTextStatus{Status: "succeeded"}, r.state, result, "revision", "", 6000)
	if err != nil || len(page.Units) != 50 || page.NextCursor == "" {
		t.Fatalf("%+v %v", page, err)
	}
	for _, cursor := range []string{"bad", encodeAttachmentCursor(attachmentCursor{1, "revision", 1, -1}), encodeAttachmentCursor(attachmentCursor{1, "revision", 99, 0}), encodeAttachmentCursor(attachmentCursor{1, "other", 1, 0})} {
		if _, err := renderAttachmentPage(page.Analysis, r.state, result, "revision", cursor, 100); !errors.Is(err, ErrAttachmentCursor) {
			t.Fatal(err)
		}
	}
}
