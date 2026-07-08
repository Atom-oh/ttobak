package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// chunkRichText must split any text longer than Notion's 2000-char rich_text
// limit into multiple entries — otherwise a single unbroken line (e.g. a
// transcript paragraph with no newlines) trips Notion's validation_error
// ("text.content.length should be ≤ 2000") when sent as one block.
func TestChunkRichText(t *testing.T) {
	t.Run("short text stays a single entry", func(t *testing.T) {
		got := chunkRichText("hello")
		if len(got) != 1 || got[0].Text.Content != "hello" {
			t.Fatalf("expected single entry %q, got %+v", "hello", got)
		}
	})

	t.Run("empty text still produces one entry", func(t *testing.T) {
		got := chunkRichText("")
		if len(got) != 1 || got[0].Text.Content != "" {
			t.Fatalf("expected one empty entry, got %+v", got)
		}
	})

	t.Run("long text splits into ≤2000-char chunks and round-trips", func(t *testing.T) {
		long := strings.Repeat("가", 8525) // matches the reported 8525-char failure
		got := chunkRichText(long)

		if len(got) != 5 { // 8525 / 2000 = 4 full chunks + 1 remainder
			t.Fatalf("expected 5 chunks, got %d", len(got))
		}
		var rebuilt strings.Builder
		for _, rt := range got {
			if len([]rune(rt.Text.Content)) > notionRichTextMaxLen {
				t.Fatalf("chunk exceeds %d runes: %d", notionRichTextMaxLen, len([]rune(rt.Text.Content)))
			}
			rebuilt.WriteString(rt.Text.Content)
		}
		if rebuilt.String() != long {
			t.Fatal("chunks did not reconstruct the original text")
		}
	})
}

// blocksForText must split into multiple blocks of the same type when a
// single line's chunkRichText output exceeds Notion's 100-rich_text-per-block
// limit — otherwise a single very long unbroken line trips Notion's
// validation_error on block creation.
func TestBlocksForTextSplitsOversizedRichText(t *testing.T) {
	// notionRichTextMaxLen=2000 chars/entry; 100*2000+1 runes yields 101
	// chunkRichText entries, one more than fits in a single block.
	long := strings.Repeat("a", notionRichTextMaxLen*notionMaxRichTextPerBlock+1)

	blocks := blocksForText("paragraph", long, false)

	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(blocks))
	}

	var rebuilt strings.Builder
	for _, b := range blocks {
		if b.Type != "paragraph" || b.Paragraph == nil {
			t.Fatalf("expected paragraph block, got %+v", b)
		}
		if len(b.Paragraph.RichText) > notionMaxRichTextPerBlock {
			t.Fatalf("block has %d rich_text entries, exceeds limit of %d", len(b.Paragraph.RichText), notionMaxRichTextPerBlock)
		}
		for _, rt := range b.Paragraph.RichText {
			rebuilt.WriteString(rt.Text.Content)
		}
	}
	if rebuilt.String() != long {
		t.Fatal("blocks did not reconstruct the original text")
	}
}

// splitBlocks must batch a page's blocks into groups of at most max so
// CreatePage can send the first batch via pages.create and append the rest
// via blocks.children.append, honoring Notion's 100-children-per-request
// limit.
func TestSplitBlocks(t *testing.T) {
	tests := []struct {
		name        string
		numBlocks   int
		max         int
		wantBatches []int // expected size of each batch, in order
	}{
		{"fits in one batch", 5, 100, []int{5}},
		{"exactly at limit", 100, 100, []int{100}},
		{"one over limit", 101, 100, []int{100, 1}},
		{"several full batches plus remainder", 250, 100, []int{100, 100, 50}},
		{"empty input", 0, 100, []int{0}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			blocks := make([]NotionBlock, tt.numBlocks)
			batches := splitBlocks(blocks, tt.max)

			if len(batches) != len(tt.wantBatches) {
				t.Fatalf("expected %d batches, got %d", len(tt.wantBatches), len(batches))
			}
			total := 0
			for i, b := range batches {
				if len(b) != tt.wantBatches[i] {
					t.Fatalf("batch %d: expected size %d, got %d", i, tt.wantBatches[i], len(b))
				}
				if len(b) > tt.max {
					t.Fatalf("batch %d exceeds max %d: %d", i, tt.max, len(b))
				}
				total += len(b)
			}
			if total != tt.numBlocks {
				t.Fatalf("expected total %d blocks across batches, got %d", tt.numBlocks, total)
			}
		})
	}
}

// NormalizeMarkdownListItem must canonicalize the list forms that HTML→markdown
// converters produce for an edited summary — turndown's "-   " (3-space marker)
// and both converters' backslash-escaped task brackets ("\[ \]" / "\[ ]") — so
// the downstream "- [ ] " todo prefix still matches. Without this, edited action
// items export as plain bullets containing literal "[ ]" text instead of Notion
// to_do blocks.
func TestNormalizeMarkdownListItem(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"turndown unchecked task", `-   \[ \] 담당자에게 문서 전달`, "- [ ] 담당자에게 문서 전달"},
		{"turndown checked task", `-   \[x\] 완료된 작업`, "- [x] 완료된 작업"},
		{"html-to-markdown unchecked task", `- \[ ] 문서 전달`, "- [ ] 문서 전달"},
		{"plain bullet triple space", "-   일반 항목", "- 일반 항목"},
		{"already canonical", "- [ ] 이미 정상", "- [ ] 이미 정상"},
		{"asterisk bullet", "* 항목", "* 항목"},
		{"non-checkbox escaped brackets preserved", `- \[참고\] 항목`, `- \[참고\] 항목`},
		{"bold text is not a list", "**중요** 강조", "**중요** 강조"},
		{"heading untouched", "## 액션 아이템", "## 액션 아이템"},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NormalizeMarkdownListItem(tt.in); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

// After an edit round-trips a summary through the editor's HTML→markdown path,
// action items arrive as "-   \[ \] ..." — markdownToNotionBlocks must still
// emit to_do blocks (checked/unchecked) for them, not plain bulleted_list_item
// blocks with literal "[ ]" text.
func TestMarkdownToNotionBlocksEditedTasks(t *testing.T) {
	s := NewNotionService()
	md := "## 액션 아이템\n" +
		`-   \[ \] 담당자에게 문서 전달` + "\n" +
		`-   \[x\] 완료된 작업` + "\n" +
		"-   일반 불릿 항목"
	blocks := s.markdownToNotionBlocks(md)

	var todos, bullets int
	for _, b := range blocks {
		switch b.Type {
		case "to_do":
			todos++
			if len(b.ToDo.RichText) == 0 || strings.Contains(b.ToDo.RichText[0].Text.Content, "[") {
				t.Fatalf("to_do block still contains literal bracket text: %+v", b.ToDo.RichText)
			}
		case "bulleted_list_item":
			bullets++
		}
	}
	if todos != 2 {
		t.Fatalf("expected 2 to_do blocks (1 unchecked + 1 checked), got %d", todos)
	}
	if bullets != 1 {
		t.Fatalf("expected 1 bulleted_list_item block, got %d", bullets)
	}
}

// parseInlineMarkdown must turn inline markdown emitted by the summarizer
// (bold speaker names, ADR-013 "[00:30](transcript://seg-30000)" deep links)
// into styled Notion rich_text runs — otherwise the asterisks/brackets show
// up as literal characters in Notion instead of bold text or a real link.
func TestParseInlineMarkdown(t *testing.T) {
	t.Run("bold run gets a bold annotation, not literal asterisks", func(t *testing.T) {
		rt := parseInlineMarkdown("**오준석 SA**: 발언")
		if len(rt) != 2 {
			t.Fatalf("expected 2 runs, got %d: %+v", len(rt), rt)
		}
		if rt[0].Text.Content != "오준석 SA" || rt[0].Annotations == nil || !rt[0].Annotations.Bold {
			t.Fatalf("expected bold run %q, got %+v", "오준석 SA", rt[0])
		}
		if rt[1].Text.Content != ": 발언" || rt[1].Annotations != nil {
			t.Fatalf("expected plain trailing run, got %+v", rt[1])
		}
	})

	t.Run("markdown link becomes a real href, not literal brackets", func(t *testing.T) {
		rt := parseInlineMarkdown("합의했다. [00:30](transcript://seg-30000)")
		if len(rt) != 2 {
			t.Fatalf("expected 2 runs, got %d: %+v", len(rt), rt)
		}
		if rt[1].Text.Content != "00:30" || rt[1].Text.Link == nil || rt[1].Text.Link.URL != "transcript://seg-30000" {
			t.Fatalf("expected link run to transcript://seg-30000, got %+v", rt[1])
		}
		if strings.Contains(rt[0].Text.Content, "[") || strings.Contains(rt[1].Text.Content, "[") {
			t.Fatalf("literal bracket leaked into rich_text: %+v", rt)
		}
	})

	t.Run("inline code gets a code annotation", func(t *testing.T) {
		rt := parseInlineMarkdown("run `kubectl get pods`")
		if len(rt) != 2 || rt[1].Text.Content != "kubectl get pods" || rt[1].Annotations == nil || !rt[1].Annotations.Code {
			t.Fatalf("expected code run, got %+v", rt)
		}
	})

	t.Run("plain text with no markdown yields a single unstyled run", func(t *testing.T) {
		rt := parseInlineMarkdown("plain sentence")
		if len(rt) != 1 || rt[0].Text.Content != "plain sentence" || rt[0].Annotations != nil {
			t.Fatalf("expected single plain run, got %+v", rt)
		}
	})
}

// End-to-end through markdownToNotionBlocks: a bullet line combining a bold
// name and a deep link (the exact shape SummarizeTranscript emits) must
// produce a bulleted_list_item whose rich_text has a bold run and a link run,
// not one literal-text run.
func TestMarkdownToNotionBlocksInlineFormatting(t *testing.T) {
	s := NewNotionService()
	md := "- **오준석 SA**: VPC CNI 권장. [05:46](transcript://seg-346100)"
	blocks := s.markdownToNotionBlocks(md)

	if len(blocks) != 1 || blocks[0].Type != "bulleted_list_item" {
		t.Fatalf("expected 1 bulleted_list_item block, got %+v", blocks)
	}
	rt := blocks[0].BulletedListItem.RichText

	var sawBold, sawLink bool
	for _, r := range rt {
		if r.Annotations != nil && r.Annotations.Bold && r.Text.Content == "오준석 SA" {
			sawBold = true
		}
		if r.Text.Link != nil && r.Text.Link.URL == "transcript://seg-346100" && r.Text.Content == "05:46" {
			sawLink = true
		}
		if strings.ContainsAny(r.Text.Content, "*[]") {
			t.Fatalf("literal markdown syntax leaked into rich_text: %q", r.Text.Content)
		}
	}
	if !sawBold {
		t.Fatalf("expected a bold run for the speaker name, got %+v", rt)
	}
	if !sawLink {
		t.Fatalf("expected a link run for the timestamp, got %+v", rt)
	}
}

// UpsertPage must update an existing Notion page in place (title + replace
// children) instead of creating a new one, so repeated exports of the same
// meeting don't pile up duplicate pages. It must still create a fresh page
// when no existing ID is given, and fall back to creating one when the
// existing page is no longer reachable (deleted, unshared).
func TestUpsertPage(t *testing.T) {
	t.Run("no existing page ID creates a new page", func(t *testing.T) {
		var sawCreate bool
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == "POST" && r.URL.Path == "/v1/pages" {
				sawCreate = true
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"id":"new-id","url":"https://notion.so/new-id"}`))
				return
			}
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}))
		defer srv.Close()

		s := &NotionService{httpClient: srv.Client(), baseURL: srv.URL}
		pageID, pageURL, err := s.UpsertPage(context.Background(), "key", "page_id", "parent", "title", "Title", "# content", "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !sawCreate || pageID != "new-id" || pageURL != "https://notion.so/new-id" {
			t.Fatalf("expected a fresh page, got id=%q url=%q sawCreate=%v", pageID, pageURL, sawCreate)
		}
	})

	t.Run("existing page ID updates title and replaces children instead of creating", func(t *testing.T) {
		// clearPageChildren deletes concurrently, so the test server's handler
		// can be invoked from multiple goroutines at once — these flags must be
		// atomic, not plain bools.
		var sawCreate, sawDelete1, sawDelete2, sawAppend atomic.Bool
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.Method == "POST" && r.URL.Path == "/v1/pages":
				sawCreate.Store(true)
				w.WriteHeader(http.StatusOK)
			case r.Method == "PATCH" && r.URL.Path == "/v1/pages/existing-id":
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"id":"existing-id","url":"https://notion.so/existing-id"}`))
			case r.Method == "GET" && r.URL.Path == "/v1/blocks/existing-id/children":
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"results":[{"id":"child-1"},{"id":"child-2"}],"has_more":false}`))
			case r.Method == "DELETE" && r.URL.Path == "/v1/blocks/child-1":
				sawDelete1.Store(true)
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{}`))
			case r.Method == "DELETE" && r.URL.Path == "/v1/blocks/child-2":
				sawDelete2.Store(true)
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{}`))
			case r.Method == "PATCH" && r.URL.Path == "/v1/blocks/existing-id/children":
				sawAppend.Store(true)
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{}`))
			default:
				t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
			}
		}))
		defer srv.Close()

		s := &NotionService{httpClient: srv.Client(), baseURL: srv.URL}
		pageID, pageURL, err := s.UpsertPage(context.Background(), "key", "page_id", "parent", "title", "Title", "# content", "existing-id")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if sawCreate.Load() {
			t.Fatal("expected UpsertPage to update in place, but it created a new page")
		}
		if !sawDelete1.Load() || !sawDelete2.Load() {
			t.Fatalf("expected both existing children to be deleted (child-1=%v child-2=%v)", sawDelete1.Load(), sawDelete2.Load())
		}
		if !sawAppend.Load() {
			t.Fatal("expected new content to be appended to the existing page")
		}
		if pageID != "existing-id" || pageURL != "https://notion.so/existing-id" {
			t.Fatalf("expected the same page ID/URL to be returned, got id=%q url=%q", pageID, pageURL)
		}
	})

	t.Run("unreachable existing page falls back to creating a new one", func(t *testing.T) {
		var sawCreate bool
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.Method == "PATCH" && r.URL.Path == "/v1/pages/gone-id":
				w.WriteHeader(http.StatusNotFound)
			case r.Method == "POST" && r.URL.Path == "/v1/pages":
				sawCreate = true
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"id":"fresh-id","url":"https://notion.so/fresh-id"}`))
			default:
				t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
			}
		}))
		defer srv.Close()

		s := &NotionService{httpClient: srv.Client(), baseURL: srv.URL}
		pageID, _, err := s.UpsertPage(context.Background(), "key", "page_id", "parent", "title", "Title", "# content", "gone-id")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !sawCreate || pageID != "fresh-id" {
			t.Fatalf("expected fallback to create a new page, got id=%q sawCreate=%v", pageID, sawCreate)
		}
	})

	// Regression: a real meeting summary can have 50+ blocks. Deleting them
	// one at a time sequentially (each DELETE taking ~300-500ms against the
	// real Notion API) blew past the export Lambda's 30s timeout in
	// production. clearPageChildren must delete concurrently — this asserts
	// wall-clock time for 60 blocks stays well under what serial deletes
	// would take, using a fake per-request latency the test controls.
	t.Run("clears many children concurrently, not one at a time", func(t *testing.T) {
		const numChildren = 60
		const perRequestDelay = 50 * time.Millisecond

		results := make([]struct {
			ID string `json:"id"`
		}, numChildren)
		for i := range results {
			results[i].ID = fmt.Sprintf("child-%d", i)
		}
		childrenBody, err := json.Marshal(map[string]interface{}{"results": results, "has_more": false})
		if err != nil {
			t.Fatalf("failed to build fixture: %v", err)
		}

		var deletes atomic.Int64
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.Method == "PATCH" && r.URL.Path == "/v1/pages/existing-id":
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"id":"existing-id","url":"https://notion.so/existing-id"}`))
			case r.Method == "GET" && r.URL.Path == "/v1/blocks/existing-id/children":
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(childrenBody)
			case r.Method == "DELETE" && strings.HasPrefix(r.URL.Path, "/v1/blocks/child-"):
				time.Sleep(perRequestDelay)
				deletes.Add(1)
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{}`))
			case r.Method == "PATCH" && r.URL.Path == "/v1/blocks/existing-id/children":
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{}`))
			default:
				t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
			}
		}))
		defer srv.Close()

		s := &NotionService{httpClient: srv.Client(), baseURL: srv.URL}
		start := time.Now()
		_, _, err = s.UpsertPage(context.Background(), "key", "page_id", "parent", "title", "Title", "# content", "existing-id")
		elapsed := time.Since(start)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if deletes.Load() != numChildren {
			t.Fatalf("expected %d deletes, got %d", numChildren, deletes.Load())
		}

		serialLowerBound := time.Duration(numChildren) * perRequestDelay
		if elapsed >= serialLowerBound {
			t.Fatalf("deletes ran serially, not concurrently: %d children took %v (serial lower bound %v)", numChildren, elapsed, serialLowerBound)
		}
	})
}

// ParseNotionPageID must extract a Notion page/database ID from whatever a
// user pastes — a bare ID, a dashed UUID, or a full page URL — and normalize
// it to dashed UUID form, since that's what Notion's parent.page_id and
// parent.database_id expect. This is the fix for internal integrations being
// unable to create pages at the workspace root: the user now supplies a
// parent page/database ID at connect time.
func TestParseNotionPageID(t *testing.T) {
	const want = "1a2b3c4d-5e6f-7a8b-9c0d-1e2f3a4b5c6d"

	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{"bare 32-hex ID", "1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d", want, false},
		{"dashed UUID", "1a2b3c4d-5e6f-7a8b-9c0d-1e2f3a4b5c6d", want, false},
		{"plain URL", "https://www.notion.so/My-Page-1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d", want, false},
		{"URL with workspace path and query", "https://www.notion.so/my-workspace/My-Page-1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d?pvs=4", want, false},
		{"uppercase hex", "1A2B3C4D5E6F7A8B9C0D1E2F3A4B5C6D", want, false},
		{"whitespace-padded", "  1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d\n", want, false},
		{"too short", "1a2b3c4d", "", true},
		{"non-hex 32 chars", strings.Repeat("z", 32), "", true},
		{"empty string", "", "", true},
		{"URL with no ID", "https://www.notion.so/", "", true},
		// Regression: a 31-char (truncated-by-one) ID must not silently absorb
		// a trailing hex-looking character from the preceding title word
		// ("Page" ends in a non-hex 'g', so it can no longer donate a char).
		{"truncated ID must not borrow from title word", "https://www.notion.so/My-Page-1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6", "", true},
		// Regression: a trailing slash must not make the post-"/" segment
		// empty and reject an otherwise-valid ID.
		{"URL with trailing slash", "https://www.notion.so/My-Page-1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d/", want, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseNotionPageID(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

// notionTitlePropertyName must find the title-type property by its "type"
// field, not by assuming the key is literally "title" — a database's title
// property display name (the properties map key) can be anything (e.g.
// "Name"), which is exactly the case this PR's database-parent support
// depends on getting right.
func TestNotionTitlePropertyName(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "title property is not literally named title",
			body: `{"properties":{"Tags":{"type":"multi_select"},"Name":{"type":"title"}}}`,
			want: "Name",
		},
		{
			name: "no title property present",
			body: `{"properties":{"Tags":{"type":"multi_select"}}}`,
			want: "",
		},
		{
			name: "invalid json",
			body: `not json`,
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := notionTitlePropertyName([]byte(tt.body)); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

// VerifyParent's status-code branching (200/401/404/400/403/429/5xx across
// two probes) is the core logic this feature depends on: it decides whether
// a user gets a "bad key", "not shared", "check capabilities", or "try
// again later" message. Table-driven against an httptest.Server so it's
// verified without calling the real Notion API.
func TestVerifyParent(t *testing.T) {
	const id = "1a2b3c4d-5e6f-7a8b-9c0d-1e2f3a4b5c6d"

	tests := []struct {
		name           string
		pageStatus     int
		dbStatus       int
		dbBody         string
		wantParentType string
		wantTitleProp  string
		wantErr        error // nil + wantErrNonNil means "any non-nil error"
		wantErrNonNil  bool
	}{
		{
			name:           "page found",
			pageStatus:     http.StatusOK,
			wantParentType: "page_id",
			wantTitleProp:  "title",
		},
		{
			name:          "page probe unauthorized",
			pageStatus:    http.StatusUnauthorized,
			wantErr:       ErrNotionInvalidAPIKey,
			wantErrNonNil: true,
		},
		{
			name:           "database found with non-default title property name",
			pageStatus:     http.StatusNotFound,
			dbStatus:       http.StatusOK,
			dbBody:         `{"properties":{"Tags":{"type":"multi_select"},"Name":{"type":"title"}}}`,
			wantParentType: "database_id",
			wantTitleProp:  "Name",
		},
		{
			name:          "database probe unauthorized",
			pageStatus:    http.StatusNotFound,
			dbStatus:      http.StatusUnauthorized,
			wantErr:       ErrNotionInvalidAPIKey,
			wantErrNonNil: true,
		},
		{
			name:          "database found but has no title property",
			pageStatus:    http.StatusNotFound,
			dbStatus:      http.StatusOK,
			dbBody:        `{"properties":{"Tags":{"type":"multi_select"}}}`,
			wantErrNonNil: true,
		},
		{
			name:          "not found on both probes means not shared",
			pageStatus:    http.StatusNotFound,
			dbStatus:      http.StatusNotFound,
			wantErr:       errNotionParentNotFound,
			wantErrNonNil: true,
		},
		{
			name:          "permanent forbidden on both probes",
			pageStatus:    http.StatusForbidden,
			dbStatus:      http.StatusForbidden,
			wantErr:       ErrNotionParentInaccessible,
			wantErrNonNil: true,
		},
		{
			name:          "permanent bad request on one probe still classifies as inaccessible",
			pageStatus:    http.StatusBadRequest,
			dbStatus:      http.StatusNotFound,
			wantErr:       ErrNotionParentInaccessible,
			wantErrNonNil: true,
		},
		{
			name:          "transient rate limit on both probes is retryable",
			pageStatus:    http.StatusTooManyRequests,
			dbStatus:      http.StatusTooManyRequests,
			wantErr:       ErrNotionUnavailable,
			wantErrNonNil: true,
		},
		{
			name:          "transient server error on both probes is retryable",
			pageStatus:    http.StatusInternalServerError,
			dbStatus:      http.StatusInternalServerError,
			wantErr:       ErrNotionUnavailable,
			wantErrNonNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.HasPrefix(r.URL.Path, "/v1/pages/"):
					w.WriteHeader(tt.pageStatus)
				case strings.HasPrefix(r.URL.Path, "/v1/databases/"):
					w.WriteHeader(tt.dbStatus)
					if tt.dbBody != "" {
						_, _ = w.Write([]byte(tt.dbBody))
					}
				default:
					t.Fatalf("unexpected request path: %s", r.URL.Path)
				}
			}))
			defer srv.Close()

			s := &NotionService{httpClient: srv.Client(), baseURL: srv.URL}
			parentType, titleProperty, err := s.VerifyParent(context.Background(), "ntn_test", id)

			if !tt.wantErrNonNil {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if parentType != tt.wantParentType || titleProperty != tt.wantTitleProp {
					t.Fatalf("got (%q, %q), want (%q, %q)", parentType, titleProperty, tt.wantParentType, tt.wantTitleProp)
				}
				return
			}

			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Fatalf("got error %v, want it to match %v", err, tt.wantErr)
			}
		})
	}
}
