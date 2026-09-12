package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/ttobak/backend/internal/model"
)

const (
	AttachmentTextResultLimit = 1024 * 1024
	AttachmentTextPageLimit   = 14000 // Includes the HTTP encoder's newline.
	attachmentTextLimit       = 256 * 1024
)

type AttachmentTextChunk struct {
	UnitIndex   int                    `json:"unitIndex"`
	StartOffset int                    `json:"startOffset"`
	EndOffset   int                    `json:"endOffset"`
	Text        string                 `json:"text"`
	Location    map[string]interface{} `json:"location"`
}
type AttachmentTextPage struct {
	Analysis     *model.AttachmentTextStatus `json:"analysis"`
	Current      bool                        `json:"current"`
	Source       model.AttachmentTextSource  `json:"source"`
	Format       string                      `json:"format"`
	Scope        string                      `json:"scope"`
	Complete     bool                        `json:"complete"` // Completeness of the extracted result, not page coverage.
	WarningCount int                         `json:"warningCount"`
	Units        []AttachmentTextChunk       `json:"units"`
	NextCursor   string                      `json:"nextCursor,omitempty"`
	PageComplete bool                        `json:"pageComplete"`
}
type attachmentCursor struct {
	Version  int    `json:"v"`
	Revision string `json:"r"`
	Unit     int    `json:"u"`
	Offset   int    `json:"o"`
}

func resultRun(att *model.Attachment, key string) string {
	prefix := "files/" + att.UserID + "/" + att.MeetingID + "/text/" + att.AttachmentID + "/"
	if !strings.HasPrefix(key, prefix) || !strings.HasSuffix(key, ".json") {
		return ""
	}
	run := strings.TrimSuffix(strings.TrimPrefix(key, prefix), ".json")
	if !attachmentIdentifier.MatchString(run) {
		return ""
	}
	return run
}
func validDocumentLocation(format string, location map[string]interface{}) bool {
	required := []string{"paragraph"}
	kind := "paragraph"
	switch format {
	case "pdf":
		kind = "page"
		required = []string{"page"}
	case "pptx":
		kind = "slide"
		required = []string{"slide", "paragraph"}
	case "md":
		required = []string{"paragraph", "startLine", "endLine"}
	}
	if location["kind"] != kind {
		return false
	}
	for _, key := range required {
		value, ok := location[key].(float64)
		if !ok || value < 1 || value > 1e7 || math.Trunc(value) != value {
			return false
		}
	}
	for key := range location {
		allowed := map[string]string{
			"pdf": "|kind|page|", "md": "|kind|paragraph|startLine|endLine|",
			"docx": "|kind|paragraph|part|table|row|cell|",
			"pptx": "|kind|slide|paragraph|part|table|row|cell|hidden|",
		}
		if !strings.Contains(allowed[format], "|"+key+"|") {
			return false
		}
		switch key {
		case "kind":
		case "part":
			if value, ok := location[key].(string); !ok || value == "" || len(value) > 1024 {
				return false
			}
		case "hidden":
			if _, ok := location[key].(bool); !ok {
				return false
			}
		default:
			value, ok := location[key].(float64)
			if !ok || value < 1 || value > 1e7 || math.Trunc(value) != value {
				return false
			}
		}
	}
	if format == "md" && location["endLine"].(float64) < location["startLine"].(float64) {
		return false
	}
	body, err := json.Marshal(location)
	return err == nil && len(body) <= 2048
}
func validateAttachmentResult(body []byte, bucket string, meeting *model.Meeting, att *model.Attachment, state *model.AttachmentTextState) (*model.AttachmentTextResult, error) {
	if len(body) > AttachmentTextResultLimit || !utf8.Valid(body) {
		return nil, ErrAttachmentUnavailable
	}
	var result model.AttachmentTextResult
	decoder := json.NewDecoder(bytes.NewReader(body))
	if decoder.Decode(&result) != nil || decoder.Decode(new(interface{})) != io.EOF {
		return nil, ErrAttachmentUnavailable
	}
	var envelope struct {
		Complete *bool           `json:"complete"`
		Error    json.RawMessage `json:"error"`
	}
	if json.Unmarshal(body, &envelope) != nil || envelope.Complete == nil ||
		(len(envelope.Error) > 0 && !bytes.Equal(bytes.TrimSpace(envelope.Error), []byte("null"))) {
		return nil, ErrAttachmentUnavailable
	}
	format := attachmentFormat(att.OriginalKey)
	scope := map[string]string{"pdf": "embedded_pdf_text", "pptx": "slide_body", "docx": "document_body", "md": "markdown_source"}
	source := model.AttachmentTextSource{Bucket: bucket, Key: att.OriginalKey, ETag: state.SourceETag, MeetingID: att.MeetingID, OwnerID: meeting.UserID, UploaderID: att.UserID, AttachmentID: att.AttachmentID, RunID: resultRun(att, state.ResultKey)}
	if source.RunID == "" || source.ETag == "" || result.Source != source {
		return nil, ErrAttachmentSourceChanged
	}
	if result.SchemaVersion != 1 || format == "" || result.Format != format || result.Scope != scope[format] || len(result.Units) == 0 || len(result.Units) > 4000 || len(result.Warnings) > 400 {
		return nil, ErrAttachmentUnavailable
	}
	if (result.Status != model.AttachmentTextSucceeded && result.Status != model.AttachmentTextPartial) || result.Complete != (result.Status == model.AttachmentTextSucceeded) || (len(result.Warnings) > 0) != (result.Status == model.AttachmentTextPartial) {
		return nil, ErrAttachmentUnavailable
	}
	textBytes := 0
	for _, unit := range result.Units {
		if strings.TrimSpace(unit.Text) == "" || !validDocumentLocation(format, unit.Location) {
			return nil, ErrAttachmentUnavailable
		}
		textBytes += len(unit.Text)
		if textBytes > attachmentTextLimit {
			return nil, ErrAttachmentUnavailable
		}
	}
	if result.Metrics.Units != len(result.Units) || result.Metrics.TextBytes != textBytes {
		return nil, ErrAttachmentUnavailable
	}
	if source.RunID == state.RunID && (state.Status == model.AttachmentTextSucceeded || state.Status == model.AttachmentTextPartial) {
		if result.Status != state.Status || result.Complete != state.Complete || len(result.Units) != state.UnitCount {
			return nil, ErrAttachmentSourceChanged
		}
	}
	return &result, nil
}

// Caller must authorize current canonical metadata before any object request.
// The key and provenance are never taken from cursor/query input.
func (s *AttachmentTextService) readResult(ctx context.Context, meeting *model.Meeting, att *model.Attachment, state *model.AttachmentTextState) (*model.AttachmentTextResult, string, error) {
	if !stateMatchesAttachment(state, meeting, att) || resultRun(att, state.ResultKey) == "" || state.SourceETag == "" {
		return nil, "", ErrAttachmentUnavailable
	}
	if s.s3 == nil {
		return nil, "", ErrAttachmentUnavailable
	}
	head, err := s.s3.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(att.OriginalKey)})
	if err != nil || head == nil {
		return nil, "", ErrAttachmentUnavailable
	}
	if aws.ToString(head.ETag) != state.SourceETag {
		return nil, "", ErrAttachmentSourceChanged
	}
	if head.ContentLength == nil || *head.ContentLength < 0 || *head.ContentLength > 20*1024*1024 {
		return nil, "", ErrAttachmentUnavailable
	}
	out, err := s.s3.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(state.ResultKey)})
	if err != nil || out == nil || out.Body == nil {
		return nil, "", ErrAttachmentUnavailable
	}
	defer out.Body.Close()
	if out.ContentLength != nil && (*out.ContentLength < 0 || *out.ContentLength > AttachmentTextResultLimit) {
		return nil, "", ErrAttachmentUnavailable
	}
	body, err := io.ReadAll(io.LimitReader(out.Body, AttachmentTextResultLimit+1))
	if err != nil || len(body) > AttachmentTextResultLimit || (out.ContentLength != nil && int64(len(body)) != *out.ContentLength) {
		return nil, "", ErrAttachmentUnavailable
	}
	result, err := validateAttachmentResult(body, s.bucket, meeting, att, state)
	if err != nil {
		return nil, "", err
	}
	// A source can change while the immutable result object is being read.
	head, err = s.s3.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(att.OriginalKey)})
	if err != nil || head == nil {
		return nil, "", ErrAttachmentUnavailable
	}
	if aws.ToString(head.ETag) != state.SourceETag {
		return nil, "", ErrAttachmentSourceChanged
	}
	return result, actionSourceHash(attachmentRevision(state) + string(body)), nil
}
func encodeAttachmentCursor(cursor attachmentCursor) string {
	body, _ := json.Marshal(cursor)
	return base64.RawURLEncoding.EncodeToString(body)
}
func renderAttachmentPage(status *model.AttachmentTextStatus, state *model.AttachmentTextState, result *model.AttachmentTextResult, revision, cursor string, pageSize int) (*AttachmentTextPage, error) {
	if pageSize == 0 {
		pageSize = 3000
	}
	if pageSize < 1 || pageSize > 6000 {
		return nil, ErrInvalidInput
	}
	position := attachmentCursor{Version: 1, Revision: revision}
	if cursor != "" {
		if len(cursor) > 1024 {
			return nil, ErrAttachmentCursor
		}
		body, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil {
			return nil, ErrAttachmentCursor
		}
		decoder := json.NewDecoder(bytes.NewReader(body))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&position) != nil || decoder.Decode(new(interface{})) != io.EOF || position.Version != 1 || position.Revision != revision || position.Unit < 0 || position.Unit >= len(result.Units) || position.Offset < 0 || position.Offset >= utf8.RuneCountInString(result.Units[position.Unit].Text) {
			return nil, ErrAttachmentCursor
		}
	}
	page := &AttachmentTextPage{Analysis: status, Source: result.Source, Current: result.Source.RunID == state.RunID && (state.Status == model.AttachmentTextSucceeded || state.Status == model.AttachmentTextPartial),
		Format: result.Format, Scope: result.Scope, Complete: result.Complete, WarningCount: len(result.Warnings), Units: []AttachmentTextChunk{}}
	setNext := func(pos attachmentCursor) {
		page.PageComplete = pos.Unit == len(result.Units)
		page.NextCursor = ""
		if !page.PageComplete {
			page.NextCursor = encodeAttachmentCursor(pos)
		}
	}
	for position.Unit < len(result.Units) && len(page.Units) < 50 && pageSize > 0 {
		unit := result.Units[position.Unit]
		runes := []rune(unit.Text)
		limit := min(pageSize, len(runes)-position.Offset)
		lo, hi := 0, limit
		nextPosition := func(n int) attachmentCursor {
			next := position
			next.Offset += n
			if next.Offset == len(runes) {
				next.Unit++
				next.Offset = 0
			}
			return next
		}
		for lo < hi {
			n := (lo + hi + 1) / 2
			page.Units = append(page.Units, AttachmentTextChunk{UnitIndex: position.Unit, StartOffset: position.Offset, EndOffset: position.Offset + n, Text: string(runes[position.Offset : position.Offset+n]), Location: unit.Location})
			setNext(nextPosition(n))
			body, err := json.Marshal(page)
			page.Units = page.Units[:len(page.Units)-1]
			if err == nil && len(body)+1 <= AttachmentTextPageLimit {
				lo = n
			} else {
				hi = n - 1
			}
		}
		if lo == 0 {
			break
		}
		page.Units = append(page.Units, AttachmentTextChunk{UnitIndex: position.Unit, StartOffset: position.Offset, EndOffset: position.Offset + lo, Text: string(runes[position.Offset : position.Offset+lo]), Location: unit.Location})
		position = nextPosition(lo)
		pageSize -= lo
	}
	setNext(position)
	if len(page.Units) == 0 {
		return nil, ErrAttachmentUnavailable
	}
	return page, nil
}
func (s *AttachmentTextService) Read(ctx context.Context, userID, meetingID, attachmentID, cursor string, pageSize int) (*AttachmentTextPage, error) {
	if pageSize < 0 || pageSize > 6000 || len(cursor) > 1024 {
		return nil, ErrInvalidInput
	}
	meeting, att, err := s.authorized(ctx, userID, meetingID, attachmentID, false)
	if err != nil {
		return nil, err
	}
	if attachmentFormat(att.OriginalKey) == "" {
		return nil, ErrUnsupportedAttachment
	}
	state, err := s.describe(ctx, meeting, att)
	if err != nil {
		return nil, err
	}
	result, revision, err := s.readResult(ctx, meeting, att, state)
	if err != nil {
		return nil, err
	}
	// No cache: revocation/deletion and a replaced run are checked again after IO.
	fresh, current, err := s.authorized(ctx, userID, meetingID, attachmentID, false)
	if err != nil {
		return nil, err
	}
	latest, err := s.describe(ctx, fresh, current)
	if err != nil {
		return nil, err
	}
	if current.OriginalKey != att.OriginalKey || current.UserID != att.UserID || attachmentRevision(latest) != attachmentRevision(state) {
		return nil, ErrAttachmentSourceChanged
	}
	return renderAttachmentPage(attachmentStatus(fresh, current, latest), state, result, revision, cursor, pageSize)
}
