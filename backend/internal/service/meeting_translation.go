package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

// On-demand translation of one MeetingReadingService page of AI notes or the
// selected transcript (ADR-005 2026-10-18 amendment). Nothing is persisted.
const (
	MeetingTranslationBucket          = "meeting-translate"
	MeetingTranslationWindow          = 24 * time.Hour
	DefaultMeetingTranslateDailyBytes = 2_000_000
	MaxMeetingTranslationBytes        = 64 * 1024

	meetingTranslationChunkBytes  = 3000
	meetingTranslationMaxChunks   = 12
	meetingTranslationConcurrency = 4
	meetingTranslationBudget      = 20 * time.Second
	meetingTranslationPageSize    = "4000"
)

var (
	ErrMeetingTranslationInvalid     = errors.New("invalid meeting translation request")
	ErrMeetingTranslationLimited     = errors.New("meeting translation daily limit reached")
	ErrMeetingTranslationUnavailable = errors.New("meeting translation is unavailable")
	ErrMeetingTranslationTooLarge    = errors.New("translated page exceeds the response limit")

	translationPlaceholder   = regexp.MustCompile(`⟦L(\d+)⟧`)
	translationProtected     = regexp.MustCompile("`[^`\n]+`|!?\\[[^\\]\n]*\\]\\([^)\n]*\\)|<[^>\n]+>|https?://[^\\s<>()]+")
	translationMarkdownLead  = regexp.MustCompile(`^\s*(?:(?:#{1,6}|>|[-*+]|\d{1,9}[.)])(?:\s+|$)(?:\[[ xX]\]\s+)?)*`)
	translationTableRule     = regexp.MustCompile(`^\s*\|?\s*:?-{3,}:?\s*(?:\|\s*:?-{3,}:?\s*)*\|?\s*$`)
	translationSpeakerHeader = regexp.MustCompile(`^\s*\[([^\[\]\n]{1,128})\]\s*$`)
	translationSpeakerName   = regexp.MustCompile(`^[^\[\]\n]{1,128}$`)
)

// MeetingTranslationLimitError carries the end of the exhausted daily window.
type MeetingTranslationLimitError struct{ RetryAt time.Time }

func (e *MeetingTranslationLimitError) Error() string   { return ErrMeetingTranslationLimited.Error() }
func (e *MeetingTranslationLimitError) Is(t error) bool { return t == ErrMeetingTranslationLimited }

type meetingPageReader interface {
	Read(ctx context.Context, userID, meetingID string, q url.Values) ([]byte, error)
}

type meetingSegmentTranslator interface {
	TranslateSegment(context.Context, TranslateRequest) (TranslateResult, error)
}

type meetingTranslationLimiter interface {
	Exceeds(ctx context.Context, userID, bucket string, window time.Duration, n, limit int64) (bool, WindowUsage)
}

type MeetingTranslationService struct {
	reader     meetingPageReader
	translator meetingSegmentTranslator
	limiter    meetingTranslationLimiter
	dailyBytes int64
	budget     time.Duration
}

func NewMeetingTranslationService(reader meetingPageReader, translator meetingSegmentTranslator, limiter meetingTranslationLimiter, dailyBytes int64) *MeetingTranslationService {
	return &MeetingTranslationService{reader: reader, translator: translator, limiter: limiter, dailyBytes: dailyBytes, budget: meetingTranslationBudget}
}

type MeetingTranslationRequest struct {
	Part   string `json:"part"`
	Target string `json:"target"`
	Cursor string `json:"cursor,omitempty"`
}

type MeetingTranslationSegment struct {
	SegmentID string   `json:"segmentId,omitempty"`
	Speaker   string   `json:"speaker,omitempty"`
	StartTime *float64 `json:"startTime,omitempty"`
	EndTime   *float64 `json:"endTime,omitempty"`
	Partial   bool     `json:"partial,omitempty"`
	Text      string   `json:"text"`
	lines     []*translationLine
}

type meetingTranslationResponse struct {
	MeetingID         string                      `json:"meetingId"`
	Part              string                      `json:"part"`
	Target            string                      `json:"target"`
	Revision          string                      `json:"revision"`
	Mode              string                      `json:"mode,omitempty"`
	Notes             *string                     `json:"notes,omitempty"`
	Segments          []MeetingTranslationSegment `json:"segments,omitempty"`
	Engines           map[string]int              `json:"engines"`
	UntranslatedLines int                         `json:"untranslatedLines"`
	NextCursor        *string                     `json:"nextCursor"`
	Complete          bool                        `json:"complete"`
	Persisted         bool                        `json:"persisted"`
}

// translationLine is one source line. Only body is sent; prefix (Markdown
// markers) and suffix stay local. tokens restore ⟦Ln⟧ placeholders.
type translationLine struct {
	prefix, body, suffix string
	tokens               map[int]string
	pieces               []*translationPiece
	untranslated         bool
}

type translationPiece struct {
	line     *translationLine
	sep      string
	text     string
	ids      []int
	out      string
	done     bool
	language string
}

// render restores placeholders; unless every token appears exactly once with
// no stray brackets, the line keeps its source and is marked untranslated.
func (l *translationLine) render() string {
	source := l.prefix + l.body + l.suffix
	if l.pieces == nil || l.untranslated {
		return source // including a line with any failed piece
	}
	var b strings.Builder
	for _, p := range l.pieces {
		b.WriteString(p.sep)
		if p.done {
			b.WriteString(p.out)
		} else {
			b.WriteString(p.text)
		}
	}
	joined := b.String()
	seen := map[int]bool{}
	valid := !strings.ContainsAny(translationPlaceholder.ReplaceAllString(joined, ""), "⟦⟧")
	restored := translationPlaceholder.ReplaceAllStringFunc(joined, func(token string) string {
		id, _ := strconv.Atoi(translationPlaceholder.FindStringSubmatch(token)[1])
		original, known := l.tokens[id]
		if !known || seen[id] {
			valid = false
		}
		seen[id] = true
		return original
	})
	if !valid || len(seen) != len(l.tokens) {
		l.untranslated = true
		return source
	}
	return l.prefix + restored + l.suffix
}

// prepareTranslationLine protects links, code spans, HTML and URLs behind
// numbered placeholders and splits a long body into ≤ chunk-size pieces.
func prepareTranslationLine(prefix, body, suffix string, next *int) *translationLine {
	line := &translationLine{prefix: prefix, body: body, suffix: suffix}
	// A source that already contains placeholder brackets cannot round-trip.
	if strings.ContainsAny(body, "⟦⟧") {
		_, letters := hangulShare(body)
		line.untranslated = letters > 0
		return line
	}
	line.tokens = map[int]string{}
	protected := translationProtected.ReplaceAllStringFunc(body, func(token string) string {
		*next++
		line.tokens[*next] = token
		return fmt.Sprintf("⟦L%d⟧", *next)
	})
	hangul, letters := hangulShare(translationPlaceholder.ReplaceAllString(protected, " "))
	if letters == 0 {
		return line
	}
	var language string
	switch {
	case float64(hangul)/float64(letters) >= 0.3:
		language = "ko"
	case !isHangulLatinOnly(protected):
		line.untranslated = true // unsupported script: keep the source rather than guess
		return line
	case hangul > 0:
		// Code-mixed Korean/English ("Lambda timeout을 30초로 늘리죠"): the
		// source is whichever of the two languages is not the target.
		language = translationMixed
	default:
		language = "en"
	}
	pieces, seps := splitTranslationBody(protected, meetingTranslationChunkBytes)
	for i, text := range pieces {
		piece := &translationPiece{line: line, text: text, sep: seps[i], language: language}
		for _, m := range translationPlaceholder.FindAllStringSubmatch(text, -1) {
			id, _ := strconv.Atoi(m[1])
			piece.ids = append(piece.ids, id)
		}
		line.pieces = append(line.pieces, piece)
	}
	return line
}

// splitTranslationBody splits text into pieces of at most limit bytes at
// spaces; seps[i] restores what was removed before piece i. A byte cut moves
// before (or, if leading, after) a placeholder it would bisect.
func splitTranslationBody(text string, limit int) (pieces, seps []string) {
	sep := ""
	for len(text) > limit {
		cut := strings.LastIndexByte(text[:limit], ' ')
		if cut > 0 {
			pieces, seps = append(pieces, text[:cut]), append(seps, sep)
			text, sep = text[cut+1:], " "
			continue
		}
		cut = limit
		for cut > 0 && !utf8RuneStart(text[cut]) {
			cut--
		}
		for _, loc := range translationPlaceholder.FindAllStringIndex(text, -1) {
			if loc[0] >= cut {
				break
			}
			if cut < loc[1] {
				cut = loc[0]
				if cut == 0 {
					cut = loc[1]
				}
				break
			}
		}
		if cut <= 0 {
			cut = len(text)
		}
		pieces, seps = append(pieces, text[:cut]), append(seps, sep)
		text, sep = text[cut:], ""
	}
	return append(pieces, text), append(seps, sep)
}

func utf8RuneStart(b byte) bool { return b&0xC0 != 0x80 }

// translationMixed marks a mostly-Latin line that also contains Hangul.
const translationMixed = "mixed"

// isHangulLatinOnly reports whether every letter is Hangul or Latin script.
func isHangulLatinOnly(text string) bool {
	for _, r := range text {
		if unicode.IsLetter(r) && !unicode.Is(unicode.Latin, r) && !unicode.Is(unicode.Hangul, r) {
			return false
		}
	}
	return true
}

// translationSourceLanguage resolves a piece's source for target; a mixed
// line is sent as the language that is not the target.
func translationSourceLanguage(language, target string) string {
	if language != translationMixed {
		return language
	}
	if target == "en" {
		return "ko"
	}
	return "en"
}

// notesTranslationLines keeps fenced code, table rules and blank lines as
// source and sends only the text after a line's Markdown markers.
func notesTranslationLines(text string, fence markdownFence) []*translationLine {
	lines := []*translationLine{}
	next := 0
	for _, raw := range strings.Split(text, "\n") {
		body, suffix := strings.CutSuffix(raw, "\r")
		trimmed := strings.TrimSpace(body)
		if fence.step(body) {
			lines = append(lines, &translationLine{body: raw})
			continue
		}
		if fence != "" || trimmed == "" || translationTableRule.MatchString(body) {
			lines = append(lines, &translationLine{body: raw})
			continue
		}
		lead := translationMarkdownLead.FindString(body)
		content := body[len(lead):]
		tail := content[len(strings.TrimRight(content, " \t")):]
		content = content[:len(content)-len(tail)]
		r := ""
		if suffix {
			r = "\r"
		}
		lines = append(lines, prepareTranslationLine(lead, content, tail+r, &next))
	}
	return lines
}

type meetingReadingPage struct {
	Revision string `json:"revision"`
	Mode     string `json:"mode"`
	Content  string `json:"content"`
	Chunks   []struct {
		Text        string `json:"text"`
		StartOffset int    `json:"startOffset"`
		Segment     *struct {
			ID          string   `json:"id"`
			Speaker     string   `json:"speaker"`
			StartTime   *float64 `json:"startTime"`
			EndTime     *float64 `json:"endTime"`
			TimingScope string   `json:"timingScope"`
			Partial     bool     `json:"partial"`
		} `json:"segment"`
	} `json:"chunks"`
	Page struct {
		NextCursor  *string `json:"nextCursor"`
		Complete    bool    `json:"complete"`
		StartOffset int     `json:"startOffset"`
	} `json:"page"`
}

// alignTranslationPage trims a non-final page to a line or whole-segment
// boundary and re-issues the reader's cursor there; a page without one is kept
// whole. It returns the notes fence open at the new end.
func alignTranslationPage(page *meetingReadingPage, part string, state translationState) markdownFence {
	if page.Page.NextCursor == nil {
		return ""
	}
	reader, err := decodeReadingCursor(*page.Page.NextCursor)
	if err != nil {
		return ""
	}
	offset, fence := -1, markdownFence("")
	switch {
	case part == "notes":
		var cut int
		if cut, fence = notesPageCut(page.Content, state.fence); cut > 0 {
			page.Content = string([]rune(page.Content)[:cut])
			offset = page.Page.StartOffset + cut
		}
	case len(page.Chunks) > 1 && page.Chunks[len(page.Chunks)-1].Segment != nil:
		// Earlier chunks end their segment, so a partial last one starts it.
		if last := page.Chunks[len(page.Chunks)-1]; last.Segment.Partial {
			page.Chunks = page.Chunks[:len(page.Chunks)-1]
			offset = last.StartOffset
		}
	case len(page.Chunks) == 1 && page.Chunks[0].Segment == nil:
		if cut := transcriptTextPageCut(page.Chunks[0].Text, !state.mid); cut > 0 {
			page.Chunks[0].Text = string([]rune(page.Chunks[0].Text)[:cut])
			offset = page.Chunks[0].StartOffset + cut
		}
	}
	if offset >= 0 {
		raw, _ := json.Marshal(readingCursor{1, reader.Binding, offset})
		next := base64.RawURLEncoding.EncodeToString(raw)
		page.Page.NextCursor = &next
	}
	return fence
}

// notesPageCut returns the code-point offset of the last line start outside a
// fence, else inside one (0 when none), and the fence open there.
func notesPageCut(text string, fence markdownFence) (int, markdownFence) {
	cut, inside, insideFence, at := 0, 0, fence, 0
	for i, line := range strings.Split(text, "\n") {
		if i > 0 && fence == "" {
			cut = at
		} else if i > 0 {
			inside, insideFence = at, fence
		}
		fence.step(line)
		at += utf8.RuneCountInString(line) + 1
	}
	if cut > 0 {
		return cut, ""
	}
	if inside > 0 {
		return inside, insideFence
	}
	return 0, fence
}

// markdownFence is the opening run of an open fenced block ("" when none).
// As in CommonMark, only a bare run of the same character at least as long
// closes it, so a ```` block may contain ``` lines.
type markdownFence string

var translationFenceRun = regexp.MustCompile("^(?:`{3,}|~{3,})")

// step advances the fence over one line and reports a delimiter line.
func (f *markdownFence) step(line string) bool {
	trimmed := strings.TrimSpace(line)
	run := translationFenceRun.FindString(trimmed)
	switch {
	case run == "":
		return false
	case *f == "":
		*f = markdownFence(run)
	case run[0] == (*f)[0] && len(run) >= len(*f) && trimmed == run:
		*f = ""
	default:
		return false
	}
	return true
}

// translationCursor wraps a reader cursor with the open notes fence, or the
// text-transcript speaker and mid-run flag (first line is not a header). The
// speaker only labels the caller's own response; the reader binds the cursor.
type translationCursor struct {
	Version int    `json:"tv"`
	Reader  string `json:"reader"`
	Fence   string `json:"fence,omitempty"`
	Speaker string `json:"speaker,omitempty"`
	Mid     bool   `json:"mid,omitempty"`
}

type translationState struct {
	fence   markdownFence
	speaker string
	mid     bool
}

func encodeTranslationCursor(reader string, st translationState) string {
	raw, _ := json.Marshal(translationCursor{1, reader, string(st.fence), st.speaker, st.mid})
	return base64.RawURLEncoding.EncodeToString(raw)
}

// decodeTranslationCursor treats anything but a canonical wrapper for part as
// an opaque reader cursor.
func decodeTranslationCursor(cursor, part string) (string, translationState) {
	raw, err := base64.RawURLEncoding.Strict().DecodeString(cursor)
	if err != nil {
		return cursor, translationState{}
	}
	var c translationCursor
	if json.Unmarshal(raw, &c) != nil || c.Version != 1 || !readingCursorAlphabet.MatchString(c.Reader) {
		return cursor, translationState{}
	}
	valid := c.Fence != "" && translationFenceRun.FindString(c.Fence) == c.Fence && c.Speaker == "" && !c.Mid
	if part == "transcript" {
		valid = c.Fence == "" && (c.Speaker != "" || c.Mid) && (c.Speaker == "" || translationSpeakerName.MatchString(c.Speaker))
	}
	if canonical, _ := json.Marshal(c); !valid || string(canonical) != string(raw) {
		return cursor, translationState{}
	}
	return c.Reader, translationState{markdownFence(c.Fence), c.Speaker, c.Mid}
}

// transcriptTextPageCut prefers the last "[speaker]" header after a blank
// line, then the last line start not right after a header (0: none).
func transcriptTextPageCut(text string, afterBlank bool) int {
	header, line, at := 0, 0, 0
	prevHeader := false
	for i, raw := range strings.Split(text, "\n") {
		isHeader := afterBlank && translationSpeakerHeader.MatchString(raw)
		if i > 0 && isHeader {
			header = at
		}
		if i > 0 && !prevHeader {
			line = at
		}
		prevHeader, afterBlank = isHeader, strings.TrimSpace(raw) == ""
		at += utf8.RuneCountInString(raw) + 1
	}
	if header > 0 {
		return header
	}
	return line
}

// transcriptTranslationSegments sends text without speaker labels or times,
// reattached from the page. Text mode returns the state at the page end.
func transcriptTranslationSegments(page *meetingReadingPage, state translationState) ([]MeetingTranslationSegment, translationState) {
	segments := []MeetingTranslationSegment{}
	next := 0
	speaker, afterBlank, text := state.speaker, !state.mid, false
	for ci, chunk := range page.Chunks {
		if s := chunk.Segment; s != nil {
			parts := []string{}
			for _, line := range strings.Split(chunk.Text, "\n") {
				if trimmed := strings.TrimSpace(line); trimmed != "" && trimmed != "["+s.Speaker+"]" {
					parts = append(parts, trimmed)
				}
			}
			if len(parts) == 0 {
				continue
			}
			segment := MeetingTranslationSegment{SegmentID: s.ID, Speaker: s.Speaker, Partial: s.Partial}
			if s.TimingScope != "unavailable" {
				segment.StartTime, segment.EndTime = s.StartTime, s.EndTime
			}
			segment.lines = []*translationLine{prepareTranslationLine("", strings.Join(parts, " "), "", &next)}
			segments = append(segments, segment)
			continue
		}
		// Text mode: a bracketed line at the start of the text or after a blank
		// line is a speaker header (buildTranscriptText writes "\n\n[speaker]\n")
		// and is never sent; other bracketed lines ("[웃음]") are speech. A
		// non-final page's last line is a reader-cut fragment.
		var current *MeetingTranslationSegment
		text = true
		lines := strings.Split(chunk.Text, "\n")
		for i, raw := range lines {
			trimmed := strings.TrimSpace(raw)
			fragment := page.Page.NextCursor != nil && ci == len(page.Chunks)-1 && i == len(lines)-1
			if fragment && raw == "" {
				break
			}
			header := afterBlank && !fragment
			afterBlank = trimmed == "" && !fragment
			if m := translationSpeakerHeader.FindStringSubmatch(raw); header && m != nil {
				speaker, current = m[1], nil
				continue
			}
			if trimmed == "" {
				continue
			}
			if current == nil {
				segments = append(segments, MeetingTranslationSegment{Speaker: speaker})
				current = &segments[len(segments)-1]
			}
			current.lines = append(current.lines, prepareTranslationLine("", trimmed, "", &next))
		}
	}
	if !text {
		return segments, translationState{}
	}
	return segments, translationState{speaker: speaker, mid: !afterBlank}
}

type translationChunk struct {
	language string
	pieces   []*translationPiece
	bytes    int
}

func (c *translationChunk) text() string {
	parts := make([]string, len(c.pieces))
	for i, p := range c.pieces {
		parts[i] = p.text
	}
	return strings.Join(parts, "\n")
}

// planTranslationChunks groups consecutive same-language pieces into chunks
// of ≤ 3000 bytes. Pieces already in the target language stay as written.
func planTranslationChunks(lines []*translationLine, target string) []*translationChunk {
	chunks := []*translationChunk{}
	for _, line := range lines {
		for _, p := range line.pieces {
			language := translationSourceLanguage(p.language, target)
			if language == target {
				continue
			}
			last := (*translationChunk)(nil)
			if len(chunks) > 0 {
				last = chunks[len(chunks)-1]
			}
			if last == nil || last.language != language || last.bytes+1+len(p.text) > meetingTranslationChunkBytes {
				last = &translationChunk{language: language, bytes: -1}
				chunks = append(chunks, last)
			}
			last.pieces = append(last.pieces, p)
			last.bytes += 1 + len(p.text)
		}
	}
	return chunks
}

// acceptTranslatedChunk requires the same line count, each line's exact
// placeholder set and table pipe count. Nothing is applied on failure.
func acceptTranslatedChunk(c *translationChunk, output string) bool {
	lines := strings.Split(strings.Trim(strings.ReplaceAll(output, "\r\n", "\n"), "\n"), "\n")
	if len(lines) != len(c.pieces) {
		return false
	}
	for i, p := range c.pieces {
		out := strings.TrimSpace(lines[i])
		if out == "" || strings.Count(out, "|") != strings.Count(p.text, "|") {
			return false
		}
		found := translationPlaceholder.FindAllStringSubmatch(out, -1)
		if len(found) != len(p.ids) || strings.Count(out, "⟦") != len(found) || strings.Count(out, "⟧") != len(found) {
			return false
		}
		want := map[int]bool{}
		for _, id := range p.ids {
			want[id] = true
		}
		for _, m := range found {
			id, _ := strconv.Atoi(m[1])
			if !want[id] {
				return false
			}
			delete(want, id)
		}
	}
	for i, p := range c.pieces {
		p.out, p.done = strings.TrimSpace(lines[i]), true
	}
	return true
}

// translateChunk tries the interpreter, then Amazon Translate if the structure
// did not round-trip. It returns the engine, or "" when the source is kept.
func (s *MeetingTranslationService) translateChunk(ctx context.Context, c *translationChunk, target string) string {
	text := c.text()
	for _, quality := range []string{TranslateQualityHigh, TranslateQualityFast} {
		result, err := s.translator.TranslateSegment(ctx, TranslateRequest{
			Text: text, SourceLang: c.language, TargetLang: target, Quality: quality, PreserveMarkup: true,
		})
		if err != nil {
			// Never log meeting text; the error is the SDK/model failure only.
			log.Printf("meeting translation chunk failed: quality=%s err=%v", quality, err)
			return ""
		}
		if acceptTranslatedChunk(c, result.Text) {
			return result.Engine
		}
		if result.Engine != TranslateEngineLLM {
			return ""
		}
	}
	return ""
}

// validateMeetingTranslationRequest rejects part/target before storage reads;
// a malformed cursor is the reading service's own INVALID_CURSOR failure.
func validateMeetingTranslationRequest(req MeetingTranslationRequest) error {
	if req.Part != "notes" && req.Part != "transcript" {
		return ErrMeetingTranslationInvalid
	}
	if req.Target != "ko" && req.Target != "en" {
		return ErrMeetingTranslationInvalid
	}
	if len(req.Cursor) > 2048 || (req.Cursor != "" && !readingCursorAlphabet.MatchString(req.Cursor)) {
		return ErrInvalidReadingCursor
	}
	return nil
}

// Translate returns sized JSON for one translated reading page.
func (s *MeetingTranslationService) Translate(ctx context.Context, userID, meetingID string, req MeetingTranslationRequest) ([]byte, error) {
	if err := validateMeetingTranslationRequest(req); err != nil {
		return nil, err
	}
	q := url.Values{"kind": {"meeting"}, "section": {"summary"}, "pageSize": {meetingTranslationPageSize}}
	if req.Part == "transcript" {
		q = url.Values{"kind": {"transcript"}, "source": {"selected"}, "pageSize": {meetingTranslationPageSize}}
	}
	var state translationState
	if req.Cursor != "" {
		var cursor string
		cursor, state = decodeTranslationCursor(req.Cursor, req.Part)
		q.Set("cursor", cursor)
	}
	raw, err := s.reader.Read(ctx, userID, meetingID, q)
	if err != nil {
		return nil, err
	}
	var page meetingReadingPage
	if err := json.Unmarshal(raw, &page); err != nil {
		return nil, readingDataError(err)
	}
	carry := translationState{fence: alignTranslationPage(&page, req.Part, state)}

	response := meetingTranslationResponse{MeetingID: meetingID, Part: req.Part, Target: req.Target,
		Revision: page.Revision, Engines: map[string]int{}, NextCursor: page.Page.NextCursor, Complete: page.Page.Complete}
	var lines []*translationLine
	if req.Part == "notes" {
		lines = notesTranslationLines(page.Content, state.fence)
	} else {
		response.Mode = page.Mode
		response.Segments, carry = transcriptTranslationSegments(&page, state)
		for _, segment := range response.Segments {
			lines = append(lines, segment.lines...)
		}
	}
	if page.Page.NextCursor != nil && carry != (translationState{}) {
		next := encodeTranslationCursor(*page.Page.NextCursor, carry)
		response.NextCursor = &next
	}

	chunks := planTranslationChunks(lines, req.Target)
	if len(chunks) > meetingTranslationMaxChunks {
		for _, c := range chunks[meetingTranslationMaxChunks:] {
			for _, p := range c.pieces {
				p.line.untranslated = true
			}
		}
		chunks = chunks[:meetingTranslationMaxChunks]
	}
	var inBytes int64
	for _, c := range chunks {
		inBytes += int64(len(c.text()))
	}
	started, failed := time.Now(), 0
	meter := func(outcome string, outBytes int) {
		line, _ := json.Marshal(map[string]any{"evt": "meeting_translate", "outcome": outcome, "part": req.Part, "tgt": req.Target,
			"inBytes": inBytes, "outBytes": outBytes, "chunks": len(chunks), "failedChunks": failed,
			"untranslatedLines": response.UntranslatedLines, "ms": time.Since(started).Milliseconds()})
		log.Print(string(line))
	}
	// An abuse brake charged on attempt, so failures cannot spend past it.
	if inBytes > 0 && s.limiter != nil {
		if over, usage := s.limiter.Exceeds(ctx, userID, MeetingTranslationBucket, MeetingTranslationWindow, inBytes, s.dailyBytes); over {
			meter("limited", 0)
			return nil, &MeetingTranslationLimitError{RetryAt: usage.WindowEnd}
		}
	}

	budget, cancel := context.WithTimeout(ctx, s.budget)
	defer cancel()
	engines := make([]string, len(chunks))
	var wg sync.WaitGroup
	slots := make(chan struct{}, meetingTranslationConcurrency)
	for i, c := range chunks {
		wg.Add(1)
		go func(i int, c *translationChunk) {
			defer wg.Done()
			select {
			case slots <- struct{}{}:
			case <-budget.Done():
				return
			}
			defer func() { <-slots }()
			engines[i] = s.translateChunk(budget, c, req.Target)
		}(i, c)
	}
	wg.Wait()

	for i, engine := range engines {
		if engine == "" {
			failed++
			for _, p := range chunks[i].pieces {
				p.done = false
				p.line.untranslated = true
			}
			continue
		}
		response.Engines[engine]++
	}
	if len(chunks) > 0 && failed == len(chunks) {
		meter("failed", 0)
		return nil, ErrMeetingTranslationUnavailable
	}

	if req.Part == "notes" {
		rendered := make([]string, len(lines))
		for i, line := range lines {
			rendered[i] = line.render()
		}
		notes := strings.Join(rendered, "\n")
		response.Notes = &notes
	} else {
		for i := range response.Segments {
			rendered := []string{}
			for _, line := range response.Segments[i].lines {
				rendered = append(rendered, line.render())
			}
			response.Segments[i].Text = strings.Join(rendered, "\n")
		}
	}
	// Counted after rendering, which may fall a line back to its source.
	for _, line := range lines {
		if line.untranslated {
			response.UntranslatedLines++
		}
	}
	out, err := readingJSON(response)
	if err != nil {
		return nil, err
	}
	if len(out) > MaxMeetingTranslationBytes {
		meter("too_large", len(out))
		return nil, ErrMeetingTranslationTooLarge
	}
	meter("ok", len(out))
	return out, nil
}
