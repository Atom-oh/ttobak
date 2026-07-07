package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
)

// notionAPIBaseURL is Notion's production API host. Tests override this via
// the unexported baseURL field (same package) to point at an httptest.Server,
// since VerifyParent's status-code branching is the core logic this feature
// depends on and must be verifiable without calling the real Notion API.
const notionAPIBaseURL = "https://api.notion.com"

// NotionService handles Notion API operations
type NotionService struct {
	httpClient *http.Client
	baseURL    string
}

// NewNotionService creates a new Notion service
func NewNotionService() *NotionService {
	return &NotionService{
		httpClient: &http.Client{},
		baseURL:    notionAPIBaseURL,
	}
}

// NotionBlock represents a Notion block
type NotionBlock struct {
	Object           string                `json:"object"`
	Type             string                `json:"type"`
	Paragraph        *NotionParagraphBlock `json:"paragraph,omitempty"`
	Heading1         *NotionHeadingBlock   `json:"heading_1,omitempty"`
	Heading2         *NotionHeadingBlock   `json:"heading_2,omitempty"`
	Heading3         *NotionHeadingBlock   `json:"heading_3,omitempty"`
	BulletedListItem *NotionListItemBlock  `json:"bulleted_list_item,omitempty"`
	ToDo             *NotionToDoBlock      `json:"to_do,omitempty"`
}

// NotionParagraphBlock represents a paragraph block
type NotionParagraphBlock struct {
	RichText []NotionRichText `json:"rich_text"`
}

// NotionHeadingBlock represents a heading block
type NotionHeadingBlock struct {
	RichText []NotionRichText `json:"rich_text"`
}

// NotionListItemBlock represents a list item block
type NotionListItemBlock struct {
	RichText []NotionRichText `json:"rich_text"`
}

// NotionToDoBlock represents a to-do block
type NotionToDoBlock struct {
	RichText []NotionRichText `json:"rich_text"`
	Checked  bool             `json:"checked"`
}

// NotionRichText represents rich text in Notion
type NotionRichText struct {
	Type string         `json:"type"`
	Text *NotionTextObj `json:"text,omitempty"`
}

// NotionTextObj represents text content
type NotionTextObj struct {
	Content string `json:"content"`
}

// NotionCreatePageRequest represents a request to create a page
type NotionCreatePageRequest struct {
	Parent     map[string]interface{} `json:"parent"`
	Properties map[string]interface{} `json:"properties"`
	Children   []NotionBlock          `json:"children,omitempty"`
}

// NotionCreatePageResponse represents the response from creating a page
type NotionCreatePageResponse struct {
	ID  string `json:"id"`
	URL string `json:"url"`
}

// CreatePage creates a new page in Notion as a child of the given parent
// (parentType is "page_id" or "database_id" — internal integrations cannot
// create pages at the workspace root, only under a page/database the user
// has explicitly shared with the integration). titleProperty is the
// properties-object key to set the title under: "title" for a page parent;
// for a database parent it must be that database's actual title-property
// name (from VerifyParent), since Notion does not accept a generic alias.
func (s *NotionService) CreatePage(ctx context.Context, apiKey, parentType, parentID, titleProperty, title, content string) (string, string, error) {
	// Convert markdown content to Notion blocks
	blocks := s.markdownToNotionBlocks(content)

	// Notion's pages.create only accepts up to notionMaxChildrenPerRequest
	// children; the rest must be appended afterwards.
	batches := splitBlocks(blocks, notionMaxChildrenPerRequest)

	// Create page request
	reqBody := NotionCreatePageRequest{
		Parent: map[string]interface{}{
			"type":     parentType,
			parentType: parentID,
		},
		Properties: map[string]interface{}{
			titleProperty: map[string]interface{}{
				"title": []map[string]interface{}{
					{
						"type": "text",
						"text": map[string]string{
							"content": title,
						},
					},
				},
			},
		},
		Children: batches[0],
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return "", "", fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", s.baseURL+"/v1/pages", bytes.NewReader(jsonBody))
	if err != nil {
		return "", "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Notion-Version", "2022-06-28")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("notion API error: %s", string(body))
	}

	var pageResp NotionCreatePageResponse
	if err := json.Unmarshal(body, &pageResp); err != nil {
		return "", "", fmt.Errorf("failed to unmarshal response: %w", err)
	}

	// Append any remaining blocks that didn't fit in the create request. The
	// page already exists at this point, so on failure we still return its
	// ID/URL alongside the error instead of "", "" — otherwise the caller has
	// no way to find (or clean up) the now-orphaned partial page, and a retry
	// would create a duplicate rather than resuming it.
	for _, batch := range batches[1:] {
		if err := s.appendBlocks(ctx, apiKey, pageResp.ID, batch); err != nil {
			log.Printf("notion CreatePage: page %s created but appendBlocks failed, page left partial: %v", pageResp.ID, err)
			return pageResp.ID, pageResp.URL, fmt.Errorf("page created but not all content was added: %w", err)
		}
	}

	return pageResp.ID, pageResp.URL, nil
}

// appendBlocks sends additional children blocks to an already-created page
// via PATCH /v1/blocks/{page_id}/children, used when a page has more blocks
// than Notion's per-request create limit.
func (s *NotionService) appendBlocks(ctx context.Context, apiKey, pageID string, blocks []NotionBlock) error {
	jsonBody, err := json.Marshal(map[string]interface{}{"children": blocks})
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "PATCH", s.baseURL+"/v1/blocks/"+pageID+"/children", bytes.NewReader(jsonBody))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Notion-Version", "2022-06-28")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("notion API error: %s", string(body))
	}

	return nil
}

// isHexString reports whether s is non-empty and consists only of lowercase hex digits.
func isHexString(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// ParseNotionPageID extracts a Notion page/database ID from a pasted URL or
// bare ID and normalizes it to dashed UUID form (8-4-4-4-12, lowercase), the
// form the Notion API expects in parent.page_id / parent.database_id.
func ParseNotionPageID(input string) (string, error) {
	s := strings.TrimSpace(input)
	if i := strings.IndexAny(s, "?#"); i != -1 {
		s = s[:i]
	}
	s = strings.TrimRight(s, "/")
	if i := strings.LastIndex(s, "/"); i != -1 {
		s = s[i+1:]
	}

	// Walk dash-separated tokens from the end, merging only tokens that are
	// themselves entirely hex digits — this stops a title word (e.g. "Page")
	// from donating a stray hex-looking character to a truncated or malformed
	// ID; merging requires the whole preceding token to be hex, not just its
	// last character.
	tokens := strings.Split(s, "-")
	hex := ""
	for i := len(tokens) - 1; i >= 0; i-- {
		t := strings.ToLower(tokens[i])
		if !isHexString(t) {
			break
		}
		hex = t + hex
	}
	if len(hex) < 32 {
		return "", fmt.Errorf("no Notion page ID found in %q", input)
	}
	id := hex[len(hex)-32:]
	return fmt.Sprintf("%s-%s-%s-%s-%s", id[0:8], id[8:12], id[12:16], id[16:20], id[20:32]), nil
}

// ErrNotionInvalidAPIKey is returned by VerifyParent when Notion rejects the
// API key itself (401), as opposed to the parent page/database being
// missing or unshared (404) — the two are distinguishable, so the caller can
// give the user the right guidance instead of always blaming the page.
var ErrNotionInvalidAPIKey = errors.New("notion api key is invalid or has been revoked")

// ErrNotionUnavailable is returned by VerifyParent when Notion couldn't be
// reached, or returned something other than a definitive 200/401/404 (e.g.
// 429 rate-limited, 5xx outage) — this must not be reported to the user as
// "page not shared", since the page/key may be perfectly fine.
var ErrNotionUnavailable = errors.New("notion is temporarily unavailable, try again later")

// errNotionParentNotFound is Notion 404 on both probes: a nonexistent ID and
// an existing-but-unshared one are indistinguishable, so this also means
// "not shared with the integration" — the caller should tell the user to
// share the page.
var errNotionParentNotFound = errors.New("notion page not found or not shared with the integration")

// ErrNotionParentInaccessible is returned when Notion rejects both probes
// with a permanent client error (400/403) rather than a definitive 404 —
// unlike ErrNotionUnavailable, retrying will not help; the user's Notion
// setup (integration capabilities, malformed ID) needs to change.
var ErrNotionParentInaccessible = errors.New("notion rejected access to this page or database")

// isPermanentNotionErrorStatus reports whether status is a definitive client
// error (bad request / forbidden) that will not resolve on retry, as opposed
// to a transient one (429 rate-limited, 5xx outage).
func isPermanentNotionErrorStatus(status int) bool {
	return status == http.StatusBadRequest || status == http.StatusForbidden
}

// VerifyParent checks that id is a page or database the integration can
// access, returning the parentType ("page_id" or "database_id") and the
// properties key to use for the title when creating a page under it —
// "title" for a page parent, or the target database's actual title-property
// name for a database parent (Notion requires the properties object to use
// the database's own property name; a database's title property is not
// always literally called "title").
func (s *NotionService) VerifyParent(ctx context.Context, apiKey, id string) (parentType, titleProperty string, err error) {
	pageStatus, _, err := s.notionGet(ctx, apiKey, s.baseURL+"/v1/pages/"+id)
	if err != nil {
		log.Printf("notion VerifyParent: GET pages/%s failed: %v", id, err)
		return "", "", fmt.Errorf("%w: %v", ErrNotionUnavailable, err)
	}
	if pageStatus == http.StatusOK {
		return "page_id", "title", nil
	}
	if pageStatus == http.StatusUnauthorized {
		return "", "", ErrNotionInvalidAPIKey
	}

	dbStatus, body, err := s.notionGet(ctx, apiKey, s.baseURL+"/v1/databases/"+id)
	if err != nil {
		log.Printf("notion VerifyParent: GET databases/%s failed: %v", id, err)
		return "", "", fmt.Errorf("%w: %v", ErrNotionUnavailable, err)
	}
	if dbStatus == http.StatusOK {
		dbTitleProperty := notionTitlePropertyName(body)
		if dbTitleProperty == "" {
			return "", "", fmt.Errorf("could not find a title property on the Notion database")
		}
		return "database_id", dbTitleProperty, nil
	}
	if dbStatus == http.StatusUnauthorized {
		return "", "", ErrNotionInvalidAPIKey
	}

	if pageStatus == http.StatusNotFound && dbStatus == http.StatusNotFound {
		return "", "", errNotionParentNotFound
	}

	log.Printf("notion VerifyParent: unexpected status for %s (pages=%d, databases=%d)", id, pageStatus, dbStatus)

	// A permanent 400/403 on either probe means retrying won't help — it's a
	// user-actionable config problem (capabilities, malformed ID), distinct
	// from a transient 429/5xx that's genuinely worth retrying.
	if isPermanentNotionErrorStatus(pageStatus) || isPermanentNotionErrorStatus(dbStatus) {
		return "", "", ErrNotionParentInaccessible
	}

	return "", "", ErrNotionUnavailable
}

// notionTitlePropertyName returns the property name (the properties map key,
// e.g. "Name") of the title-type property in a Notion database's GET
// response body, or "" if not found.
func notionTitlePropertyName(body []byte) string {
	var db struct {
		Properties map[string]struct {
			Type string `json:"type"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(body, &db); err != nil {
		return ""
	}
	for name, prop := range db.Properties {
		if prop.Type == "title" {
			return name
		}
	}
	return ""
}

// notionGet performs a GET to url with apiKey, returning the HTTP status
// code and response body. err is non-nil only for a transport-level failure
// (request construction, network, or body read) — the caller must not treat
// that the same as a normal Notion response.
func (s *NotionService) notionGet(ctx context.Context, apiKey, url string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return 0, nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Notion-Version", "2022-06-28")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, fmt.Errorf("failed to read response: %w", err)
	}
	return resp.StatusCode, body, nil
}

// notionMaxChildrenPerRequest is Notion's limit on children blocks accepted
// per pages.create or blocks.children.append request.
const notionMaxChildrenPerRequest = 100

// splitBlocks groups blocks into batches of at most max, so a page with more
// blocks than Notion's per-request limit can be sent as an initial create
// plus follow-up append requests.
func splitBlocks(blocks []NotionBlock, max int) [][]NotionBlock {
	if len(blocks) <= max {
		return [][]NotionBlock{blocks}
	}
	batches := make([][]NotionBlock, 0, (len(blocks)/max)+1)
	for i := 0; i < len(blocks); i += max {
		end := i + max
		if end > len(blocks) {
			end = len(blocks)
		}
		batches = append(batches, blocks[i:end])
	}
	return batches
}

// notionRichTextMaxLen is the Notion API limit for a single rich_text.text.content value.
const notionRichTextMaxLen = 2000

// chunkRichText splits text into one or more Notion rich_text entries, each capped at
// notionRichTextMaxLen runes, so a single long line (e.g. an unbroken transcript
// paragraph) doesn't trip Notion's per-rich-text length validation. A block's rich_text
// array can hold multiple entries that render as one continuous span, so splitting here
// doesn't change how the text looks on the page.
func chunkRichText(text string) []NotionRichText {
	runes := []rune(text)
	if len(runes) == 0 {
		return []NotionRichText{{Type: "text", Text: &NotionTextObj{Content: ""}}}
	}
	chunks := make([]NotionRichText, 0, (len(runes)/notionRichTextMaxLen)+1)
	for i := 0; i < len(runes); i += notionRichTextMaxLen {
		end := i + notionRichTextMaxLen
		if end > len(runes) {
			end = len(runes)
		}
		chunks = append(chunks, NotionRichText{Type: "text", Text: &NotionTextObj{Content: string(runes[i:end])}})
	}
	return chunks
}

// notionMaxRichTextPerBlock is Notion's limit on rich_text entries per block.
const notionMaxRichTextPerBlock = 100

// blocksForText builds one or more Notion blocks of blockType from text. A
// single line's chunkRichText output can exceed notionMaxRichTextPerBlock
// (e.g. a >200,000-rune line), so the rich_text entries are split across
// multiple consecutive blocks of the same type instead of one oversized block.
func blocksForText(blockType, text string, checked bool) []NotionBlock {
	rt := chunkRichText(text)
	blocks := make([]NotionBlock, 0, (len(rt)/notionMaxRichTextPerBlock)+1)
	for i := 0; i < len(rt); i += notionMaxRichTextPerBlock {
		end := i + notionMaxRichTextPerBlock
		if end > len(rt) {
			end = len(rt)
		}
		part := rt[i:end]

		block := NotionBlock{Object: "block", Type: blockType}
		switch blockType {
		case "heading_1":
			block.Heading1 = &NotionHeadingBlock{RichText: part}
		case "heading_2":
			block.Heading2 = &NotionHeadingBlock{RichText: part}
		case "heading_3":
			block.Heading3 = &NotionHeadingBlock{RichText: part}
		case "to_do":
			block.ToDo = &NotionToDoBlock{RichText: part, Checked: checked}
		case "bulleted_list_item":
			block.BulletedListItem = &NotionListItemBlock{RichText: part}
		default: // paragraph
			block.Paragraph = &NotionParagraphBlock{RichText: part}
		}
		blocks = append(blocks, block)
	}
	return blocks
}

// NormalizeMarkdownListItem rewrites a list line to the canonical
// "- "/"* " + unescaped-content form the markdown parsers expect. HTML→markdown
// converters used on edited summaries diverge from that form: turndown emits
// "-   " (marker + three spaces) and both turndown and html-to-markdown/v2
// backslash-escape task brackets ("\[ \]" / "\[ ]"), so an edited "- [ ] task"
// round-trips as "-   \[ \] task" and would otherwise export as a plain bullet
// with literal brackets instead of a Notion to_do. Returns line unchanged when
// it is not a list item (marker not followed by whitespace).
func NormalizeMarkdownListItem(line string) string {
	if line == "" {
		return line
	}
	marker := line[0]
	if marker != '-' && marker != '*' {
		return line
	}
	rest := line[1:]
	trimmed := strings.TrimLeft(rest, " ")
	if trimmed == rest {
		return line // no space after marker (e.g. "**bold**", "-word")
	}
	trimmed = strings.ReplaceAll(trimmed, "\\[", "[")
	trimmed = strings.ReplaceAll(trimmed, "\\]", "]")
	return string(marker) + " " + trimmed
}

// markdownToNotionBlocks converts markdown text to Notion blocks
func (s *NotionService) markdownToNotionBlocks(content string) []NotionBlock {
	var blocks []NotionBlock
	lines := strings.Split(content, "\n")

	for _, line := range lines {
		line = strings.TrimRight(line, "\r")

		if line == "" {
			continue
		}

		// Normalize list markers so summaries edited in the TipTap editor
		// (HTML→markdown converted) still match the "- [ ] " todo / "- " bullet
		// prefixes below. No-op for headings and paragraphs.
		line = NormalizeMarkdownListItem(line)

		// Check for headings
		if strings.HasPrefix(line, "### ") {
			blocks = append(blocks, blocksForText("heading_3", strings.TrimPrefix(line, "### "), false)...)
		} else if strings.HasPrefix(line, "## ") {
			blocks = append(blocks, blocksForText("heading_2", strings.TrimPrefix(line, "## "), false)...)
		} else if strings.HasPrefix(line, "# ") {
			blocks = append(blocks, blocksForText("heading_1", strings.TrimPrefix(line, "# "), false)...)
		} else if strings.HasPrefix(line, "- [ ] ") {
			// Unchecked to-do
			blocks = append(blocks, blocksForText("to_do", strings.TrimPrefix(line, "- [ ] "), false)...)
		} else if strings.HasPrefix(line, "- [x] ") || strings.HasPrefix(line, "- [X] ") {
			// Checked to-do
			text := strings.TrimPrefix(line, "- [x] ")
			text = strings.TrimPrefix(text, "- [X] ")
			blocks = append(blocks, blocksForText("to_do", text, true)...)
		} else if strings.HasPrefix(line, "- ") {
			// Bulleted list
			blocks = append(blocks, blocksForText("bulleted_list_item", strings.TrimPrefix(line, "- "), false)...)
		} else if strings.HasPrefix(line, "* ") {
			// Bulleted list (asterisk)
			blocks = append(blocks, blocksForText("bulleted_list_item", strings.TrimPrefix(line, "* "), false)...)
		} else {
			// Regular paragraph
			blocks = append(blocks, blocksForText("paragraph", line, false)...)
		}
	}

	return blocks
}
