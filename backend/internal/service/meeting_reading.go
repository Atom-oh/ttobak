package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

// MeetingReadingService keeps authorization/notes on metadata reads. Only the
// explicitly requested transcript is hydrated, after authorization.
type MeetingReadingService struct {
	repo    *repository.DynamoDBRepository
	access  *MeetingService
	actions *ActionItemsAnalysisService
}

func NewMeetingReadingService(repo *repository.DynamoDBRepository, actions *ActionItemsAnalysisService) *MeetingReadingService {
	view := repo.MetadataView()
	access := NewMeetingService(view)
	if actions == nil {
		actions = NewActionItemsAnalysisService(view, access, nil, nil)
	}
	return &MeetingReadingService{view, access, actions}
}

// Read returns already-sized JSON including its trailing newline. The handler
// must write these bytes directly rather than encode them a second time.
func (s *MeetingReadingService) Read(ctx context.Context, userID, meetingID string, q url.Values) ([]byte, error) {
	o, err := ParseReadingOptions(q)
	if err != nil {
		return nil, err
	}
	if !readingID.MatchString(meetingID) {
		return nil, ErrInvalidReadingOptions
	}
	if userID == "" {
		return nil, ErrForbidden
	}
	meeting, permission, err := s.access.checkAccess(ctx, userID, meetingID)
	if err != nil {
		return nil, err
	}
	if meeting == nil {
		return nil, ErrNotFound
	}
	if o.Kind == "transcript" {
		return s.transcript(ctx, meeting, meetingID, o)
	}

	state, err := s.actions.Describe(ctx, meeting)
	if err != nil {
		log.Printf("Reading action status unavailable: %v", err)
		state = &model.ActionItemsAnalysis{Status: model.AnalysisUnknown, ErrorCode: "STATUS_UNAVAILABLE"}
	}
	// As in the action-items endpoint, read items AFTER status. Repeat access
	// checks too: neither an older item snapshot nor a revoked grant is cached.
	meeting, permission, err = s.access.checkAccess(ctx, userID, meetingID)
	if err != nil {
		return nil, err
	}
	if meeting == nil {
		return nil, ErrNotFound
	}
	if state == nil {
		state = &model.ActionItemsAnalysis{Status: model.AnalysisUnknown}
	}
	copyState := *state
	if copyState.Status == model.AnalysisSucceeded && copyState.SourceHash != actionSourceHash(meeting.Content) {
		copyState.Status, copyState.ErrorCode = model.AnalysisFailed, model.AnalysisSourceChanged
	}
	return renderMeetingReading(meeting, permission, &copyState, o)
}

func (s *MeetingReadingService) transcript(ctx context.Context, m *model.Meeting, meetingID string, o ReadingOptions) ([]byte, error) {
	_, selected := selectMeetingTranscript(m)
	source := o.Source
	if source == "selected" {
		source = selected
	}
	if source == "" {
		return nil, ErrNoReadingTranscript
	}
	field := func(variant string) *string {
		if variant == "A" {
			return &m.TranscriptA
		}
		return &m.TranscriptB
	}
	hydrate := func(variant string) (string, error) {
		value, err := s.repo.ReadTranscriptField(ctx, meetingID, "transcript"+variant, *field(variant))
		if err != nil {
			return "", readingDataError(err)
		}
		*field(variant) = value
		return value, nil
	}
	text, err := hydrate(source)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(text) == "" && o.Source == "selected" {
		_, fallback := selectMeetingTranscript(m)
		if fallback != "" && fallback != source {
			source = fallback
			text, err = hydrate(source)
			if err != nil {
				return nil, err
			}
		}
	}
	if strings.TrimSpace(text) == "" {
		return nil, ErrNoReadingTranscript
	}
	if !utf8.ValidString(text) {
		return nil, ErrReadingData
	}
	_, selected = selectMeetingTranscript(m)
	var verified []readingWindow
	if source == selected {
		raw, err := s.repo.ReadTranscriptField(ctx, meetingID, "transcriptSegments", m.TranscriptSegments)
		if err != nil {
			return nil, readingDataError(err)
		}
		verified = readingWindows(text, raw)
	}
	if o.StartTime != nil && len(verified) == 0 {
		return nil, ErrReadingTimeRange
	}
	windows := verified
	if len(windows) == 0 {
		windows = []readingWindow{{start: 0, end: utf8.RuneCountInString(text)}}
	}
	if o.StartTime != nil {
		filtered := []readingWindow{}
		for _, w := range windows {
			if w.segment.StartTime < *o.EndTime && w.segment.EndTime > *o.StartTime {
				filtered = append(filtered, w)
			}
		}
		windows = filtered
	}
	evidence, mode := "unavailable", "text"
	var segments interface{}
	if len(verified) > 0 {
		evidence, mode = "full_text_match", "segments"
		rows := []interface{}{}
		for _, w := range verified {
			v := w.segment
			rows = append(rows, []interface{}{v.ID, v.Speaker, v.Text, v.StartTime, v.EndTime})
		}
		segments = rows
	}
	var timeRange interface{}
	scope := "source"
	if o.StartTime != nil {
		timeRange = readingObject{"startTime": *o.StartTime, "endTime": *o.EndTime}
		scope = "requested_time_range"
	}
	revision := readingHash([]interface{}{source, text, m.SttProvider, segments})
	binding := readingHash([]interface{}{1, "transcript", meetingID, o.Source, selected, source, timeRange, revision})
	truncated := []string{}
	base := readingObject{
		"meetingId": meetingID, "title": readingLimit(m.Title, 256, "title", &truncated),
		"source": source, "selectedSource": selected, "requestedSource": o.Source, "revision": revision,
		"provenance": readingObject{"field": "transcript" + source, "segmentEvidence": evidence,
			"sttProvider": readingLimit(m.SttProvider, 64, "sttProvider", &truncated), "providerScope": "meeting"},
		"metadataTruncated": truncated, "mode": mode, "timeRange": timeRange, "completenessScope": scope,
	}
	return paginateReading(text, windows, o, binding, func(chunks []readingChunk, page readingObject) readingObject {
		base["chunks"], base["page"] = chunks, page
		return base
	})
}

func renderMeetingReading(m *model.Meeting, permission string, state *model.ActionItemsAnalysis, o ReadingOptions) ([]byte, error) {
	items, err := readingActionObjects(m.ActionItems)
	if err != nil {
		return nil, readingDataError(err)
	}
	actions, err := readingActionPreview(m.ActionItems, items, state)
	if err != nil {
		return nil, err
	}
	text, field := m.Notes, "notes"
	if o.Section == "summary" {
		text, field = m.Content, "content"
	}
	if o.Section == "actionItems" {
		raw, err := json.Marshal(items)
		if err != nil {
			return nil, err
		}
		text, field = string(raw), "actionItemsJson"
	}
	revision := readingHash([]string{field, text})
	binding := readingHash([]interface{}{1, "meeting", m.MeetingID, o.Section, revision})
	truncated := []string{}
	base := readingObject{"meetingId": m.MeetingID, "source": o.Section, "revision": revision,
		"title":               readingLimit(m.Title, 256, "title", &truncated),
		"date":                m.Date.Format(time.RFC3339Nano),
		"status":              readingLimit(m.Status, 64, "status", &truncated),
		"permission":          readingLimit(permission, 16, "permission", &truncated),
		"updatedAt":           m.UpdatedAt.Format(time.RFC3339Nano),
		"availableCodePoints": readingObject{"notes": utf8.RuneCountInString(m.Notes), "summary": utf8.RuneCountInString(m.Content)},
		"readingHints": readingObject{"summary": "ttobak_get_meeting(section=summary)",
			"actionItems": "ttobak_get_meeting(section=actionItems)", "transcript": "ttobak_read_transcript"},
	}
	for _, list := range []struct {
		name   string
		values []string
	}{{"participants", m.Participants}, {"tags", m.Tags}} {
		if list.values == nil {
			continue
		}
		values := []string{}
		for i, value := range list.values {
			if i == 5 {
				truncated = append(truncated, list.name)
				break
			}
			values = append(values, readingLimit(value, 32, list.name, &truncated))
		}
		base[list.name] = values
	}
	unique := []string{}
	for _, field := range truncated {
		found := false
		for _, prior := range unique {
			if prior == field {
				found = true
				break
			}
		}
		if !found {
			unique = append(unique, field)
		}
	}
	base["metadataTruncated"] = unique
	for key, value := range actions {
		base[key] = value
	}
	windows := []readingWindow{}
	if text != "" {
		windows = append(windows, readingWindow{start: 0, end: utf8.RuneCountInString(text)})
	}
	return paginateReading(text, windows, o, binding, func(chunks []readingChunk, page readingObject) readingObject {
		var body strings.Builder
		for _, c := range chunks {
			body.WriteString(c.Text)
		}
		base[field], base["page"] = body.String(), page
		return base
	})
}

// Preserve every stored JSON extension. Only the established legacy identity
// and completion normalization is applied; a typed DTO is not the full source.
func readingActionObjects(raw string) ([]map[string]json.RawMessage, error) {
	normalized, err := storedActionItems(raw)
	if err != nil {
		return nil, err
	}
	items := []map[string]json.RawMessage{}
	if strings.TrimSpace(raw) != "" {
		if err := json.Unmarshal([]byte(raw), &items); err != nil {
			return nil, err
		}
	}
	if items == nil {
		items = []map[string]json.RawMessage{}
	}
	if len(items) != len(normalized) {
		return nil, ErrReadingData
	}
	for i, item := range items {
		if item == nil {
			return nil, ErrReadingData
		}
		item["id"], _ = json.Marshal(normalized[i].ID)
		item["completed"], _ = json.Marshal(normalized[i].Completed)
	}
	return items, nil
}

func readingActionPreview(raw string, items []map[string]json.RawMessage, state *model.ActionItemsAnalysis) (readingObject, error) {
	preview := []readingObject{}
	truncated := []string{}
	fields := []struct {
		name  string
		limit int
	}{{"id", 128}, {"text", 256}, {"assignee", 64}, {"dueDate", 32}, {"priority", 16}}
	for i, item := range items {
		if i == 5 {
			break
		}
		projected := readingObject{}
		for _, field := range fields {
			value, exists := item[field.name]
			if !exists {
				continue
			}
			var decoded interface{}
			if err := json.Unmarshal(value, &decoded); err != nil {
				return nil, readingDataError(err)
			}
			if text, ok := decoded.(string); ok {
				projected[field.name] = readingLimit(text, field.limit, fmt.Sprintf("actionItems[%d].%s", i, field.name), &truncated)
			} else if decoded == nil {
				projected[field.name] = nil
			} else {
				return nil, ErrReadingData
			}
		}
		var completed bool
		if err := json.Unmarshal(item["completed"], &completed); err != nil {
			return nil, readingDataError(err)
		}
		projected["completed"] = completed
		for key := range item {
			if _, included := projected[key]; !included {
				truncated = append(truncated, fmt.Sprintf("actionItems[%d].otherFields", i))
				break
			}
		}
		data, err := json.Marshal(append(append([]readingObject{}, preview...), projected))
		if err != nil {
			return nil, err
		}
		// Leave room for status/metadata and at least one body point at the
		// stricter API budget. The full collection has its own paged section.
		if len(data) > 2500 {
			break
		}
		preview = append(preview, projected)
	}
	analysisTruncated := []string{}
	status := state.Status
	switch status {
	case "unknown", "queued", "running", "succeeded", "failed":
	default:
		status = "unknown"
		analysisTruncated = append(analysisTruncated, "status")
	}
	analysis := readingObject{"status": status}
	if state.ErrorCode != "" {
		analysis["errorCode"] = readingLimit(state.ErrorCode, 128, "errorCode", &analysisTruncated)
	}
	if state.RunID != "" {
		analysis["runId"] = readingLimit(state.RunID, 128, "runId", &analysisTruncated)
	}
	if state.LeaseUntil != 0 {
		analysis["leaseUntil"] = state.LeaseUntil
	}
	available := strings.TrimSpace(raw) != "" && strings.TrimSpace(raw) != "null"
	return readingObject{"actionItems": preview, "actionItemsAnalysis": analysis,
		"actionItemsAnalysisTruncated": analysisTruncated,
		"actionItemsPreview": readingObject{"available": available, "totalItems": len(items),
			"complete":          available && len(items) == len(preview) && len(truncated) == 0,
			"metadataTruncated": truncated, "readWithSection": "actionItems"}}, nil
}
