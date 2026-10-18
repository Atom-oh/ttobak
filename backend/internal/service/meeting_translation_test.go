package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

type pageReaderFunc func(url.Values) ([]byte, error)

func (f pageReaderFunc) Read(_ context.Context, _, _ string, q url.Values) ([]byte, error) {
	return f(q)
}

// lineTranslator answers line by line so placeholders and line structure
// survive unless edit changes them; it records every request.
type lineTranslator struct {
	mu       sync.Mutex
	requests []TranslateRequest
	edit     func(TranslateRequest, string) string
	engine   func(TranslateRequest) string
	block    bool
	inFlight int
	maxSeen  int
}

func (f *lineTranslator) TranslateSegment(ctx context.Context, req TranslateRequest) (TranslateResult, error) {
	f.mu.Lock()
	f.requests = append(f.requests, req)
	f.inFlight++
	f.maxSeen = max(f.maxSeen, f.inFlight)
	f.mu.Unlock()
	defer func() { f.mu.Lock(); f.inFlight--; f.mu.Unlock() }()
	if f.block {
		<-ctx.Done()
		return TranslateResult{}, ctx.Err()
	}
	time.Sleep(time.Millisecond)
	lines := strings.Split(req.Text, "\n")
	for i, line := range lines {
		lines[i] = "EN<" + line + ">"
		if f.edit != nil {
			lines[i] = f.edit(req, lines[i])
		}
	}
	engine := TranslateEngineLLM
	if req.Quality == TranslateQualityFast {
		engine = TranslateEngineTranslate
	}
	if f.engine != nil {
		engine = f.engine(req)
	}
	return TranslateResult{Text: strings.Join(lines, "\n"), Engine: engine}, nil
}

type recordingLimiter struct {
	bucket string
	window time.Duration
	n      int64
	limit  int64
	over   bool
}

func (l *recordingLimiter) Exceeds(_ context.Context, _, bucket string, window time.Duration, n, limit int64) (bool, WindowUsage) {
	l.bucket, l.window, l.n, l.limit = bucket, window, n, limit
	return l.over, WindowUsage{Count: n, WindowEnd: time.Now().Add(time.Hour)}
}

func notesPage(content string, next *string) pageReaderFunc {
	return func(q url.Values) ([]byte, error) {
		if q.Get("kind") != "meeting" || q.Get("section") != "summary" {
			return nil, errors.New("notes must read the AI notes section")
		}
		return json.Marshal(map[string]any{"revision": "rev", "content": content,
			"page": map[string]any{"nextCursor": next, "complete": next == nil}})
	}
}

type translationOutput struct {
	Revision          string           `json:"revision"`
	Mode              string           `json:"mode"`
	Notes             *string          `json:"notes"`
	Segments          []map[string]any `json:"segments"`
	Engines           map[string]int   `json:"engines"`
	UntranslatedLines int              `json:"untranslatedLines"`
	NextCursor        *string          `json:"nextCursor"`
	Complete          bool             `json:"complete"`
	Persisted         bool             `json:"persisted"`
}

func runTranslation(t *testing.T, s *MeetingTranslationService, req MeetingTranslationRequest) translationOutput {
	t.Helper()
	raw, err := s.Translate(context.Background(), "owner", "m1", req)
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	if len(raw) > MaxMeetingTranslationBytes {
		t.Fatalf("response %d bytes", len(raw))
	}
	var out translationOutput
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestMeetingTranslationNotesRoundTripPlaceholdersAndMarkdown(t *testing.T) {
	content := strings.Join([]string{
		"# 회의록",
		"",
		"- [ ] 예산을 [12:34](transcript://seg-1) 기준으로 확정합니다",
		"1. `kubectl get pods` 명령은 https://example.com/docs 문서를 참고합니다",
		"> 인용 ![도식](attachment://0f0e-aa) 확인",
		"| 항목 | 담당 |",
		"|---|---|",
		"| 예산 | 김 |",
		"```mermaid",
		"graph TD; 시작-->끝",
		"```",
		"Already English text",
		"- 3/4",
	}, "\n")
	tr := &lineTranslator{}
	s := NewMeetingTranslationService(notesPage(content, nil), tr, nil, 0)
	out := runTranslation(t, s, MeetingTranslationRequest{Part: "notes", Target: "en"})
	if out.Notes == nil || out.Persisted || !out.Complete || out.Revision != "rev" {
		t.Fatalf("unexpected envelope: %+v", out)
	}
	got := strings.Split(*out.Notes, "\n")
	want := map[int]string{
		0:  "# EN<회의록>",
		1:  "",
		2:  "- [ ] EN<예산을 [12:34](transcript://seg-1) 기준으로 확정합니다>",
		3:  "1. EN<`kubectl get pods` 명령은 https://example.com/docs 문서를 참고합니다>",
		4:  "> EN<인용 ![도식](attachment://0f0e-aa) 확인>",
		5:  "EN<| 항목 | 담당 |>",
		6:  "|---|---|",
		8:  "```mermaid",
		9:  "graph TD; 시작-->끝",
		11: "Already English text",
		12: "- 3/4",
	}
	for i, line := range want {
		if got[i] != line {
			t.Errorf("line %d = %q, want %q", i, got[i], line)
		}
	}
	for _, req := range tr.requests {
		for _, leaked := range []string{"transcript://", "attachment://", "https://", "kubectl", "# ", "- [ ]", "graph TD", "Already English"} {
			if strings.Contains(req.Text, leaked) {
				t.Errorf("protected text %q was sent: %q", leaked, req.Text)
			}
		}
		if !req.PreserveMarkup || req.SourceLang != "ko" || req.TargetLang != "en" || req.Quality != TranslateQualityHigh {
			t.Errorf("request shape = %+v", req)
		}
	}
	if out.Engines[TranslateEngineLLM] != 1 || out.UntranslatedLines != 0 {
		t.Fatalf("engines=%v untranslated=%d", out.Engines, out.UntranslatedLines)
	}
}

func TestMeetingTranslationRejectsBrokenPlaceholders(t *testing.T) {
	content := "예산은 [링크](https://example.com) 참고\n두 번째 줄입니다"
	cases := []struct {
		name        string
		edit        func(TranslateRequest, string) string
		wantEngine  string
		wantSource  bool
		wantRetries int
	}{
		{"interpreter drops placeholder, Translate keeps it", func(r TranslateRequest, line string) string {
			if r.Quality == TranslateQualityHigh {
				return translationPlaceholder.ReplaceAllString(line, "")
			}
			return line
		}, TranslateEngineTranslate, false, 2},
		{"duplicated placeholder", func(_ TranslateRequest, line string) string {
			return line + translationPlaceholder.FindString(line)
		}, "", true, 2},
		{"invented placeholder", func(_ TranslateRequest, line string) string { return line + "⟦L99⟧" }, "", true, 2},
		{"merged lines", func(_ TranslateRequest, line string) string { return strings.ReplaceAll(line, "\n", " ") + "\n" }, "", true, 2},
		{"stray bracket", func(_ TranslateRequest, line string) string { return line + "⟧" }, "", true, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tr := &lineTranslator{edit: tc.edit}
			s := NewMeetingTranslationService(notesPage(content, nil), tr, nil, 0)
			raw, err := s.Translate(context.Background(), "owner", "m1", MeetingTranslationRequest{Part: "notes", Target: "en"})
			if tc.wantSource {
				if !errors.Is(err, ErrMeetingTranslationUnavailable) {
					t.Fatalf("broken output accepted: %v %s", err, raw)
				}
			} else {
				var out translationOutput
				if err != nil || json.Unmarshal(raw, &out) != nil {
					t.Fatal(err)
				}
				if out.Engines[tc.wantEngine] != 1 || !strings.Contains(*out.Notes, "[링크](https://example.com)") {
					t.Fatalf("out = %+v", out)
				}
			}
			if len(tr.requests) != tc.wantRetries {
				t.Fatalf("requests = %d", len(tr.requests))
			}
		})
	}
}

func TestMeetingTranslationKeepsSourceForFailedChunksOnly(t *testing.T) {
	// Three ~1.6 KB Korean lines: the 3000-byte limit gives one chunk each,
	// and only the middle chunk is broken by both engines.
	first, second, third := strings.Repeat("첫째 ", 400), strings.Repeat("둘째 ", 400), strings.Repeat("셋째 ", 400)
	first, second, third = strings.TrimSpace(first), strings.TrimSpace(second), strings.TrimSpace(third)
	tr := &lineTranslator{edit: func(r TranslateRequest, line string) string {
		if strings.Contains(r.Text, "둘째") {
			return ""
		}
		return line
	}}
	s := NewMeetingTranslationService(notesPage(first+"\n"+second+"\n"+third, nil), tr, nil, 0)
	out := runTranslation(t, s, MeetingTranslationRequest{Part: "notes", Target: "en"})
	if want := "EN<" + first + ">\n" + second + "\nEN<" + third + ">"; *out.Notes != want || out.UntranslatedLines != 1 {
		t.Fatalf("untranslated=%d notes match=%v", out.UntranslatedLines, *out.Notes == want)
	}
	if len(tr.requests) != 4 || out.Engines[TranslateEngineLLM] != 2 {
		t.Fatalf("requests=%d engines=%v", len(tr.requests), out.Engines)
	}

	tr2 := &lineTranslator{edit: func(r TranslateRequest, line string) string {
		if strings.Contains(r.Text, "둘째") {
			return ""
		}
		return line
	}}
	long := strings.Repeat("첫째 문장입니다. ", 200) + "\n" + strings.Repeat("둘째 문장입니다. ", 200)
	s = NewMeetingTranslationService(notesPage(long, nil), tr2, nil, 0)
	out = runTranslation(t, s, MeetingTranslationRequest{Part: "notes", Target: "en"})
	if out.UntranslatedLines != 1 || !strings.Contains(*out.Notes, "\n"+strings.Repeat("둘째 문장입니다. ", 200)) {
		t.Fatalf("failed chunk not kept as source: untranslated=%d", out.UntranslatedLines)
	}
}

func TestMeetingTranslationCodeMixedLines(t *testing.T) {
	mixed := "Lambda function timeout을 30초로 늘리죠"
	for _, tc := range []struct {
		target, source string
	}{{"en", "ko"}, {"ko", "en"}} {
		tr := &lineTranslator{}
		out := runTranslation(t, NewMeetingTranslationService(notesPage(mixed, nil), tr, nil, 0),
			MeetingTranslationRequest{Part: "notes", Target: tc.target})
		if *out.Notes != "EN<"+mixed+">" || out.UntranslatedLines != 0 || len(tr.requests) != 1 || tr.requests[0].SourceLang != tc.source {
			t.Fatalf("target=%s notes=%q untranslated=%d requests=%+v", tc.target, *out.Notes, out.UntranslatedLines, tr.requests)
		}
	}
	// Scripts other than Hangul and Latin are still kept as source.
	tr := &lineTranslator{}
	out := runTranslation(t, NewMeetingTranslationService(notesPage("Это тест 회의", nil), tr, nil, 0),
		MeetingTranslationRequest{Part: "notes", Target: "en"})
	if *out.Notes != "Это тест 회의" || out.UntranslatedLines != 1 || len(tr.requests) != 0 {
		t.Fatalf("other script: notes=%q requests=%d", *out.Notes, len(tr.requests))
	}
}

// pagedReader serves reading pages through the real paginateReading, so
// cursors, offsets and page cuts match MeetingReadingService.
func pagedReader(t *testing.T, text, mode string) pageReaderFunc {
	binding := strings.Repeat("ab", 32)
	return func(q url.Values) ([]byte, error) {
		o := ReadingOptions{PageSize: 4000, Cursor: q.Get("cursor")}
		windows := []readingWindow{{start: 0, end: len([]rune(text))}}
		return paginateReading(text, windows, o, binding, func(chunks []readingChunk, page readingObject) readingObject {
			if mode == "" {
				var body strings.Builder
				for _, c := range chunks {
					body.WriteString(c.Text)
				}
				return readingObject{"revision": "rev", "content": body.String(), "page": page}
			}
			return readingObject{"revision": "rev", "mode": mode, "chunks": chunks, "page": page}
		})
	}
}

func translateAllPages(t *testing.T, s *MeetingTranslationService, part string) []translationOutput {
	t.Helper()
	pages := []translationOutput{}
	cursor := ""
	for i := 0; i < 50; i++ {
		out := runTranslation(t, s, MeetingTranslationRequest{Part: part, Target: "ko", Cursor: cursor})
		pages = append(pages, out)
		if out.NextCursor == nil {
			if !out.Complete {
				t.Fatal("last page not complete")
			}
			return pages
		}
		cursor = *out.NextCursor
	}
	t.Fatal("pagination did not finish")
	return nil
}

func TestMeetingTranslationPagesEndAtLineAndFenceBoundaries(t *testing.T) {
	// The reader's raw cuts fall inside the fenced block (offset 4000) and
	// inside a link on the next page; aligned pages must avoid both.
	source, want := []string{}, []string{}
	fenced := false
	prose := func(i int) {
		line := fmt.Sprintf("Line %03d says hello there", i)
		if fenced && i%3 == 0 {
			line = fmt.Sprintf("See [source %d](transcript://s%d) for details", i, i)
		}
		source, want = append(source, line), append(want, "EN<"+line+">")
	}
	i := 0
	for ; len(strings.Join(source, "\n")) < 3880; i++ {
		prose(i)
	}
	block := []string{"```"}
	for k := 0; k < 10; k++ {
		block = append(block, fmt.Sprintf("code line %02d stays as code", k))
	}
	block = append(block, "```")
	source, want, fenced = append(source, block...), append(want, block...), true
	for ; len(strings.Join(source, "\n")) < 13000; i++ {
		prose(i)
	}
	s := NewMeetingTranslationService(pagedReader(t, strings.Join(source, "\n"), ""), &lineTranslator{}, nil, 0)
	pages := translateAllPages(t, s, "notes")
	var joined strings.Builder
	for _, page := range pages {
		joined.WriteString(*page.Notes)
	}
	if len(pages) < 4 || joined.String() != strings.Join(want, "\n") {
		t.Fatalf("pages=%d: joined translation differs from the line-by-line translation", len(pages))
	}
}

func TestMeetingTranslationCarriesFenceAcrossOversizedCodeBlock(t *testing.T) {
	// A fenced block longer than two reading pages that starts a page: those
	// pages are kept whole, and the code lines on the following pages must
	// stay code while the prose after the closing fence is translated.
	for _, leading := range []int{0, 3} {
		source, want := []string{}, []string{}
		for i := 0; i < leading; i++ {
			line := fmt.Sprintf("Intro %d says hello", i)
			source, want = append(source, line), append(want, "EN<"+line+">")
		}
		block := []string{"```go"}
		for k := 0; len(strings.Join(block, "\n")) < 9000; k++ {
			block = append(block, fmt.Sprintf("// comment %04d should stay code", k))
		}
		block = append(block, "```")
		source, want = append(source, block...), append(want, block...)
		for i := 0; i < 30; i++ {
			line := fmt.Sprintf("After %02d says hello there", i)
			source, want = append(source, line), append(want, "EN<"+line+">")
		}
		tr := &lineTranslator{}
		s := NewMeetingTranslationService(pagedReader(t, strings.Join(source, "\n"), ""), tr, nil, 0)
		pages := translateAllPages(t, s, "notes")
		var joined strings.Builder
		untranslated := 0
		for _, page := range pages {
			joined.WriteString(*page.Notes)
			untranslated += page.UntranslatedLines
		}
		if len(pages) < 3 || untranslated != 0 || joined.String() != strings.Join(want, "\n") {
			t.Fatalf("leading=%d pages=%d untranslated=%d: joined translation differs", leading, len(pages), untranslated)
		}
		for _, req := range tr.requests {
			if strings.Contains(req.Text, "comment") {
				t.Fatalf("leading=%d: code sent for translation", leading)
			}
		}
	}
	// A forged or plain cursor is passed through unchanged.
	if c, fenced := decodeTranslationCursor("abc"); c != "abc" || fenced {
		t.Fatalf("plain cursor=%q fenced=%v", c, fenced)
	}
	if c, fenced := decodeTranslationCursor(encodeTranslationCursor("abc")); c != "abc" || !fenced {
		t.Fatalf("wrapped cursor=%q fenced=%v", c, fenced)
	}
}

func TestMeetingTranslationTextTranscriptPagesKeepSpeakers(t *testing.T) {
	runs, speakers := []string{}, []string{}
	for i := 0; i < 40; i++ {
		speaker := fmt.Sprintf("화자%d", i%3)
		speakers = append(speakers, speaker)
		runs = append(runs, "["+speaker+"]\n"+strings.TrimSpace(strings.Repeat(fmt.Sprintf("run %d words ", i), 25)))
	}
	s := NewMeetingTranslationService(pagedReader(t, strings.Join(runs, "\n\n"), "text"), &lineTranslator{}, nil, 0)
	got := []map[string]any{}
	pages := translateAllPages(t, s, "transcript")
	for _, page := range pages {
		got = append(got, page.Segments...)
	}
	if len(pages) < 3 || len(got) != len(runs) {
		t.Fatalf("pages=%d segments=%d", len(pages), len(got))
	}
	for i, seg := range got {
		body := strings.SplitN(runs[i], "\n", 2)[1]
		if seg["speaker"] != speakers[i] || seg["text"] != "EN<"+body+">" {
			t.Fatalf("segment %d speaker=%v", i, seg["speaker"])
		}
	}
}

func TestMeetingTranslationDropsTrailingPartialSegment(t *testing.T) {
	binding := strings.Repeat("cd", 32)
	raw, _ := json.Marshal(readingCursor{1, binding, 120})
	reader := pageReaderFunc(func(url.Values) ([]byte, error) {
		return json.Marshal(map[string]any{"revision": "r", "mode": "segments", "chunks": []any{
			map[string]any{"text": "First segment", "startOffset": 0, "segment": map[string]any{"id": "s1", "speaker": "A"}},
			map[string]any{"text": "Second cut", "startOffset": 50, "segment": map[string]any{"id": "s2", "speaker": "B", "partial": true}},
		}, "page": map[string]any{"nextCursor": base64.RawURLEncoding.EncodeToString(raw), "complete": false}})
	})
	out := runTranslation(t, NewMeetingTranslationService(reader, &lineTranslator{}, nil, 0), MeetingTranslationRequest{Part: "transcript", Target: "ko"})
	if len(out.Segments) != 1 || out.NextCursor == nil {
		t.Fatalf("segments=%+v", out.Segments)
	}
	c, err := decodeReadingCursor(*out.NextCursor)
	if err != nil || c.Binding != binding || c.Offset != 50 {
		t.Fatalf("cursor=%+v err=%v", c, err)
	}
}

func TestMeetingTranslationBracketAnnotationIsSpeech(t *testing.T) {
	page := map[string]any{"revision": "r", "mode": "text", "chunks": []any{
		map[string]any{"text": "[화자A]\n첫 번째 말입니다\n[웃음]\n두 번째 말입니다"},
	}, "page": map[string]any{"nextCursor": nil, "complete": true}}
	reader := pageReaderFunc(func(url.Values) ([]byte, error) { return json.Marshal(page) })
	out := runTranslation(t, NewMeetingTranslationService(reader, &lineTranslator{}, nil, 0), MeetingTranslationRequest{Part: "transcript", Target: "en"})
	if len(out.Segments) != 1 || out.Segments[0]["speaker"] != "화자A" ||
		out.Segments[0]["text"] != "EN<첫 번째 말입니다>\nEN<[웃음]>\nEN<두 번째 말입니다>" {
		t.Fatalf("segments=%+v", out.Segments)
	}
}

func TestMeetingTranslationTranscriptOmitsSpeakersAndTimes(t *testing.T) {
	start, end := 12.5, 14.0
	segmentsPage := map[string]any{"revision": "r1", "mode": "segments", "chunks": []any{
		map[string]any{"text": "[김 팀장]\n예산을 확정합니다.\n", "segment": map[string]any{
			"id": "s1", "speaker": "김 팀장", "startTime": start, "endTime": end, "timingScope": "whole_segment"}},
		map[string]any{"text": "\n[Speaker 2]\n일정은 다음 주입니다.", "segment": map[string]any{
			"id": "s2", "speaker": "Speaker 2", "timingScope": "unavailable", "partial": true}},
	}, "page": map[string]any{"nextCursor": "abc", "complete": false}}
	textPage := map[string]any{"revision": "r2", "mode": "text", "chunks": []any{
		map[string]any{"text": "[화자A]\n안녕하세요 반갑습니다\n오늘 안건입니다\n\n[화자B]\n네 좋습니다\n"},
	}, "page": map[string]any{"nextCursor": nil, "complete": true}}
	for _, tc := range []struct {
		page     map[string]any
		speakers []string
		texts    []string
	}{
		{segmentsPage, []string{"김 팀장", "Speaker 2"}, []string{"EN<예산을 확정합니다.>", "EN<일정은 다음 주입니다.>"}},
		{textPage, []string{"화자A", "화자B"}, []string{"EN<안녕하세요 반갑습니다>\nEN<오늘 안건입니다>", "EN<네 좋습니다>"}},
	} {
		tr := &lineTranslator{}
		reader := pageReaderFunc(func(q url.Values) ([]byte, error) {
			if q.Get("kind") != "transcript" || q.Get("source") != "selected" {
				return nil, errors.New("transcript must read the selected source")
			}
			return json.Marshal(tc.page)
		})
		out := runTranslation(t, NewMeetingTranslationService(reader, tr, nil, 0), MeetingTranslationRequest{Part: "transcript", Target: "en"})
		if out.Mode != tc.page["mode"] || len(out.Segments) != len(tc.speakers) {
			t.Fatalf("segments = %+v", out.Segments)
		}
		for i, seg := range out.Segments {
			if seg["speaker"] != tc.speakers[i] || seg["text"] != tc.texts[i] {
				t.Errorf("segment %d = %+v", i, seg)
			}
		}
		for _, req := range tr.requests {
			for _, speaker := range tc.speakers {
				if strings.Contains(req.Text, speaker) || strings.Contains(req.Text, "12.5") || strings.Contains(req.Text, "[") {
					t.Fatalf("speaker label or time sent: %q", req.Text)
				}
			}
		}
		if tc.page["mode"] == "segments" {
			if out.Segments[0]["startTime"] != 12.5 || out.Segments[1]["startTime"] != nil || out.Segments[1]["partial"] != true {
				t.Fatalf("timing not reattached: %+v", out.Segments)
			}
			if out.NextCursor == nil || *out.NextCursor != "abc" || out.Complete {
				t.Fatal("continuation not forwarded from the reading page")
			}
		}
	}
}

func TestMeetingTranslationBoundsChunksConcurrencyAndBudget(t *testing.T) {
	// 40 lines of ~1 KB alternating language: more than 12 chunks.
	lines := []string{}
	for i := 0; i < 40; i++ {
		if i%2 == 0 {
			lines = append(lines, strings.Repeat("가", 340))
		} else {
			lines = append(lines, strings.Repeat("word ", 200))
		}
	}
	// A single over-long line is split at spaces and rejoined.
	lines = append(lines, strings.Repeat("긴문장 ", 1500))
	tr := &lineTranslator{edit: func(_ TranslateRequest, line string) string {
		return strings.TrimSuffix(strings.TrimPrefix(line, "EN<"), ">")
	}}
	limiter := &recordingLimiter{}
	s := NewMeetingTranslationService(notesPage(strings.Join(lines, "\n"), nil), tr, limiter, 777)
	out := runTranslation(t, s, MeetingTranslationRequest{Part: "notes", Target: "en"})
	if len(tr.requests) != meetingTranslationMaxChunks {
		t.Fatalf("requests = %d", len(tr.requests))
	}
	var sent int64
	for _, req := range tr.requests {
		if len(req.Text) > meetingTranslationChunkBytes {
			t.Fatalf("chunk of %d bytes", len(req.Text))
		}
		sent += int64(len(req.Text))
	}
	if tr.maxSeen > meetingTranslationConcurrency {
		t.Fatalf("concurrency %d", tr.maxSeen)
	}
	if out.UntranslatedLines == 0 {
		t.Fatal("chunks beyond the cap must be reported as untranslated")
	}
	if limiter.bucket != MeetingTranslationBucket || limiter.window != 24*time.Hour || limiter.n != sent || limiter.limit != 777 {
		t.Fatalf("limiter = %+v sent=%d", limiter, sent)
	}

	long := strings.TrimSuffix(strings.Repeat("긴문장 ", 1500), " ")
	tr = &lineTranslator{edit: func(_ TranslateRequest, line string) string {
		return strings.TrimSuffix(strings.TrimPrefix(line, "EN<"), ">")
	}}
	out = runTranslation(t, NewMeetingTranslationService(notesPage(long, nil), tr, nil, 0), MeetingTranslationRequest{Part: "notes", Target: "en"})
	if *out.Notes != long || len(tr.requests) < 2 {
		t.Fatalf("split line not rejoined exactly: requests=%d", len(tr.requests))
	}

	// Bytes are charged on attempt, even when every chunk then fails.
	blocked := &lineTranslator{block: true}
	charged := &recordingLimiter{}
	s = NewMeetingTranslationService(notesPage("번역할 문장", nil), blocked, charged, 777)
	s.budget = 20 * time.Millisecond
	if _, err := s.Translate(context.Background(), "owner", "m1", MeetingTranslationRequest{Part: "notes", Target: "en"}); !errors.Is(err, ErrMeetingTranslationUnavailable) {
		t.Fatalf("budget exhaustion = %v", err)
	}
	if charged.n != int64(len("번역할 문장")) {
		t.Fatalf("failed attempt charged %d bytes", charged.n)
	}
}

func TestMeetingTranslationLimitAndValidation(t *testing.T) {
	reads := 0
	reader := pageReaderFunc(func(q url.Values) ([]byte, error) {
		reads++
		return notesPage("번역할 문장", nil)(q)
	})
	limiter := &recordingLimiter{over: true}
	tr := &lineTranslator{}
	s := NewMeetingTranslationService(reader, tr, limiter, DefaultMeetingTranslateDailyBytes)
	_, err := s.Translate(context.Background(), "owner", "m1", MeetingTranslationRequest{Part: "notes", Target: "en"})
	var limited *MeetingTranslationLimitError
	if !errors.Is(err, ErrMeetingTranslationLimited) || !errors.As(err, &limited) || limited.RetryAt.IsZero() || len(tr.requests) != 0 {
		t.Fatalf("limit = %v requests=%d", err, len(tr.requests))
	}
	reads = 0
	for _, req := range []MeetingTranslationRequest{
		{Part: "summary", Target: "en"}, {Part: "notes", Target: "ja"}, {Part: "notes", Target: ""},
		{Part: "transcript", Target: "ko", Cursor: "bad cursor!"}, {Part: "notes", Target: "en", Cursor: strings.Repeat("a", 2049)},
	} {
		if _, err := s.Translate(context.Background(), "owner", "m1", req); !errors.Is(err, ErrMeetingTranslationInvalid) {
			t.Fatalf("%+v accepted: %v", req, err)
		}
	}
	if reads != 0 {
		t.Fatal("invalid request reached the reader")
	}
	stale := pageReaderFunc(func(url.Values) ([]byte, error) { return nil, ErrStaleReadingCursor })
	if _, err := NewMeetingTranslationService(stale, tr, nil, 0).Translate(context.Background(), "owner", "m1",
		MeetingTranslationRequest{Part: "notes", Target: "en", Cursor: "abc"}); !errors.Is(err, ErrStaleReadingCursor) {
		t.Fatalf("reader error not propagated: %v", err)
	}
}

func TestInterpreterMarkupRuleOnlyForDocuments(t *testing.T) {
	for _, preserve := range []bool{false, true} {
		body, err := buildInterpreterRequest(TranslateRequest{Text: "x", SourceLang: "ko", TargetLang: "en", PreserveMarkup: preserve})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body), "⟦L1⟧") != preserve {
			t.Fatalf("preserve=%v body=%s", preserve, body)
		}
	}
}

func TestMeetingTranslationUsesInterpreterValidationAndFallback(t *testing.T) {
	mt := &fakeTextTranslator{out: "Budget ⟦L1⟧ confirmed"}
	inv := &fakeModelInvoker{body: chatResponse("Translation: Budget ⟦L1⟧ confirmed", "stop")}
	svc := newTestTranslateService(mt, inv, lunaID)
	s := NewMeetingTranslationService(notesPage("- 예산 [근거](transcript://s1) 확정", nil), svc, nil, 0)
	out := runTranslation(t, s, MeetingTranslationRequest{Part: "notes", Target: "en"})
	if *out.Notes != "- Budget [근거](transcript://s1) confirmed" || out.Engines[TranslateEngineTranslate] != 1 || inv.calls != 1 || mt.calls != 1 {
		t.Fatalf("notes=%q engines=%v calls=%d/%d", *out.Notes, out.Engines, inv.calls, mt.calls)
	}
}
