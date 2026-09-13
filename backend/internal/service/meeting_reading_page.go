package service

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

const MaxMeetingReadingBytes = 14000

var (
	ErrInvalidReadingOptions = errors.New("invalid reading options")
	ErrInvalidReadingCursor  = errors.New("invalid reading cursor")
	ErrStaleReadingCursor    = errors.New("reading source changed; restart without cursor")
	ErrNoReadingTranscript   = errors.New("requested transcript is unavailable")
	ErrReadingTimeRange      = errors.New("time ranges require verified transcript segments")
	ErrReadingData           = errors.New("meeting reading data is invalid")
	readingID                = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
	readingCursorAlphabet    = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
)

type ReadingOptions struct {
	Kind, Section, Source, Cursor string
	PageSize                      int
	StartTime, EndTime            *float64
}

func ParseReadingOptions(q url.Values) (ReadingOptions, error) {
	o := ReadingOptions{Kind: "meeting", Section: "notes", Source: "selected", PageSize: 4000}
	for key, values := range q {
		if len(values) != 1 {
			return o, ErrInvalidReadingOptions
		}
		value := values[0]
		switch key {
		case "kind":
			o.Kind = value
		case "section":
			o.Section = value
		case "source":
			o.Source = value
		case "cursor":
			if len(value) > 2048 || !readingCursorAlphabet.MatchString(value) {
				return o, ErrInvalidReadingCursor
			}
			if _, err := decodeReadingCursor(value); err != nil {
				return o, err
			}
			o.Cursor = value
		case "pageSize":
			n, err := strconv.Atoi(value)
			if err != nil || n < 1 || n > 8000 {
				return o, ErrInvalidReadingOptions
			}
			o.PageSize = n
		case "startTime", "endTime":
			n, err := strconv.ParseFloat(value, 64)
			if err != nil || math.IsInf(n, 0) || math.IsNaN(n) {
				return o, ErrInvalidReadingOptions
			}
			if key == "startTime" {
				o.StartTime = &n
			} else {
				o.EndTime = &n
			}
		default:
			return o, ErrInvalidReadingOptions
		}
	}
	if o.Kind != "meeting" && o.Kind != "transcript" {
		return o, ErrInvalidReadingOptions
	}
	if o.Kind == "meeting" {
		if q.Has("source") || q.Has("startTime") || q.Has("endTime") {
			return o, ErrInvalidReadingOptions
		}
		if o.Section != "notes" && o.Section != "summary" && o.Section != "actionItems" {
			return o, ErrInvalidReadingOptions
		}
	} else {
		if q.Has("section") || (o.Source != "selected" && o.Source != "A" && o.Source != "B") {
			return o, ErrInvalidReadingOptions
		}
		if (o.StartTime == nil) != (o.EndTime == nil) {
			return o, ErrInvalidReadingOptions
		}
		if o.StartTime != nil && (*o.StartTime < 0 || *o.EndTime <= *o.StartTime) {
			return o, ErrInvalidReadingOptions
		}
	}
	return o, nil
}

type readingObject = map[string]interface{}
type readingWindow struct {
	start, end, index int
	segment           *speakerSegment
}
type readingChunk struct {
	Text    string        `json:"text"`
	Start   int           `json:"startOffset"`
	End     int           `json:"endOffset"`
	Segment readingObject `json:"segment,omitempty"`
}
type readingCursor struct {
	Version int    `json:"v"`
	Binding string `json:"binding"`
	Offset  int    `json:"offset"`
}

func readingHash(value interface{}) string {
	data, _ := json.Marshal(value)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
func readingLimit(value string, size int, field string, truncated *[]string) string {
	points := []rune(value)
	if len(points) > size {
		*truncated = append(*truncated, field)
		points = points[:size]
	}
	return string(points)
}
func readingJSON(value interface{}) ([]byte, error) {
	data, err := json.Marshal(value)
	return append(data, '\n'), err
}
func decodeReadingCursor(cursor string) (readingCursor, error) {
	var c readingCursor
	raw, err := base64.RawURLEncoding.Strict().DecodeString(cursor)
	if err != nil {
		return c, ErrInvalidReadingCursor
	}
	if json.Unmarshal(raw, &c) != nil || c.Version != 1 || c.Offset < 0 {
		return c, ErrInvalidReadingCursor
	}
	canonical, _ := json.Marshal(c)
	if !bytes.Equal(canonical, raw) {
		return c, ErrInvalidReadingCursor
	}
	if _, err := hex.DecodeString(c.Binding); err != nil || len(c.Binding) != 64 {
		return c, ErrInvalidReadingCursor
	}
	return c, nil
}
func readingOffset(cursor, binding string, windows []readingWindow) (int, error) {
	if cursor == "" {
		if len(windows) > 0 {
			return windows[0].start, nil
		}
		return 0, nil
	}
	c, err := decodeReadingCursor(cursor)
	if err != nil {
		return 0, err
	}
	if c.Binding != binding {
		return 0, ErrStaleReadingCursor
	}
	for _, w := range windows {
		if c.Offset >= w.start && c.Offset < w.end {
			return c.Offset, nil
		}
	}
	return 0, ErrInvalidReadingCursor
}
func readingChunkFor(points []rune, w readingWindow, start, count int) readingChunk {
	c := readingChunk{Text: string(points[start : start+count]), Start: start, End: start + count}
	if s := w.segment; s != nil {
		truncated := []string{}
		c.Segment = readingObject{
			"index": w.index, "id": readingLimit(s.ID, 128, "id", &truncated),
			"speaker":   readingLimit(s.Speaker, 128, "speaker", &truncated),
			"startTime": s.StartTime, "endTime": s.EndTime, "timingScope": "whole_segment",
			"partial": start != w.start || start+count != w.end, "metadataTruncated": truncated,
		}
	}
	return c
}
func paginateReading(text string, windows []readingWindow, o ReadingOptions, binding string, render func([]readingChunk, readingObject) readingObject) ([]byte, error) {
	if !utf8.ValidString(text) {
		return nil, ErrReadingData
	}
	points := []rune(text)
	offset, err := readingOffset(o.Cursor, binding, windows)
	if err != nil {
		return nil, err
	}
	matching := 0
	for _, w := range windows {
		matching += w.end - w.start
	}
	result := func(chunks []readingChunk) ([]byte, error) {
		start, end := offset, offset
		if len(chunks) > 0 {
			start = chunks[0].Start
			end = chunks[len(chunks)-1].End
		}
		var next interface{}
		for _, w := range windows {
			if w.end > end {
				raw, _ := json.Marshal(readingCursor{1, binding, max(end, w.start)})
				next = base64.RawURLEncoding.EncodeToString(raw)
				break
			}
		}
		return readingJSON(render(chunks, readingObject{"unit": "unicode_code_points",
			"startOffset": start, "endOffset": end, "totalCodePoints": len(points), "matchingCodePoints": matching,
			"complete": next == nil, "nextCursor": next}))
	}
	chunks := []readingChunk{}
	remaining := o.PageSize
	for _, w := range windows {
		if w.end <= offset {
			continue
		}
		start := max(offset, w.start)
		count := min(remaining, w.end-start)
		candidate := func(n int) []readingChunk {
			return append(append([]readingChunk{}, chunks...), readingChunkFor(points, w, start, n))
		}
		data, err := result(candidate(count))
		if err != nil {
			return nil, err
		}
		if len(data) > MaxMeetingReadingBytes {
			low, high := 0, count
			for low < high {
				mid := (low + high + 1) / 2
				data, err = result(candidate(mid))
				if err != nil {
					return nil, err
				}
				if len(data) <= MaxMeetingReadingBytes {
					low = mid
				} else {
					high = mid - 1
				}
			}
			if low > 0 {
				chunks = candidate(low)
			}
			break
		}
		chunks = candidate(count)
		remaining -= count
		if remaining == 0 || len(chunks) == 50 {
			break
		}
	}
	data, err := result(chunks)
	if err != nil {
		return nil, err
	}
	if (len(windows) > 0 && len(chunks) == 0) || len(data) > MaxMeetingReadingBytes {
		return nil, ErrReadingData
	}
	return data, nil
}

// Require the repository's complete segment policy AND exact source windows.
// Legacy punctuation repair is accepted only when its resulting text aligns.
func readingWindows(text, raw string) []readingWindow {
	var times []struct {
		Start *float64 `json:"startTime"`
		End   *float64 `json:"endTime"`
	}
	if json.Unmarshal([]byte(raw), &times) != nil || len(times) == 0 {
		return nil
	}
	prior := -1.0
	for _, t := range times {
		if t.Start == nil || t.End == nil || math.IsNaN(*t.Start) || math.IsInf(*t.Start, 0) ||
			math.IsNaN(*t.End) || math.IsInf(*t.End, 0) || *t.Start < 0 || *t.Start < prior || *t.End < *t.Start {
			return nil
		}
		prior = *t.Start
	}
	segments := transcriptSegmentsForText(text, raw)
	if len(segments) != len(times) {
		return nil
	}
	align := func(grouped bool) []int {
		ends := []int{}
		offset := 0
		for i, s := range segments {
			same := i > 0 && s.Speaker == segments[i-1].Speaker
			start, ok := offset, true
			if grouped && !same {
				start, ok = consumeTranscriptSpeakerHeader(text, offset, s.Speaker)
			}
			if !ok {
				return nil
			}
			end, ok := consumeExactTranscriptText(text, start, s.Text)
			if !ok && grouped && same {
				start, ok = consumeTranscriptSpeakerHeader(text, offset, s.Speaker)
				if ok {
					end, ok = consumeExactTranscriptText(text, start, s.Text)
				}
			}
			if !ok {
				return nil
			}
			ends = append(ends, end)
			offset = end
		}
		if strings.TrimSpace(text[offset:]) != "" {
			return nil
		}
		return ends
	}
	ends := align(false)
	if ends == nil {
		ends = align(true)
	}
	if ends == nil {
		return nil
	}
	windows := make([]readingWindow, 0, len(ends))
	position, points := 0, 0
	for i, end := range ends {
		start := points
		points += utf8.RuneCountInString(text[position:end])
		position = end
		if i == len(ends)-1 {
			points += utf8.RuneCountInString(text[end:])
		}
		windows = append(windows, readingWindow{start, points, i, &segments[i]})
	}
	return windows
}

func readingDataError(err error) error { return fmt.Errorf("%w: %v", ErrReadingData, err) }
