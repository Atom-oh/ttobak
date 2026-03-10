package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// NotionService handles Notion API operations
type NotionService struct {
	httpClient *http.Client
}

// NewNotionService creates a new Notion service
func NewNotionService() *NotionService {
	return &NotionService{
		httpClient: &http.Client{},
	}
}

// NotionBlock represents a Notion block
type NotionBlock struct {
	Object    string                 `json:"object"`
	Type      string                 `json:"type"`
	Paragraph *NotionParagraphBlock  `json:"paragraph,omitempty"`
	Heading1  *NotionHeadingBlock    `json:"heading_1,omitempty"`
	Heading2  *NotionHeadingBlock    `json:"heading_2,omitempty"`
	Heading3  *NotionHeadingBlock    `json:"heading_3,omitempty"`
	BulletedListItem *NotionListItemBlock `json:"bulleted_list_item,omitempty"`
	ToDo      *NotionToDoBlock       `json:"to_do,omitempty"`
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
	Type string          `json:"type"`
	Text *NotionTextObj  `json:"text,omitempty"`
}

// NotionTextObj represents text content
type NotionTextObj struct {
	Content string `json:"content"`
}

// NotionPageParent represents the parent of a page
type NotionPageParent struct {
	Type      string `json:"type"`
	PageID    string `json:"page_id,omitempty"`
	Workspace bool   `json:"workspace,omitempty"`
}

// NotionCreatePageRequest represents a request to create a page
type NotionCreatePageRequest struct {
	Parent     map[string]interface{}   `json:"parent"`
	Properties map[string]interface{}   `json:"properties"`
	Children   []NotionBlock            `json:"children,omitempty"`
}

// NotionCreatePageResponse represents the response from creating a page
type NotionCreatePageResponse struct {
	ID  string `json:"id"`
	URL string `json:"url"`
}

// CreatePage creates a new page in Notion
func (s *NotionService) CreatePage(ctx context.Context, apiKey, title, content string) (string, string, error) {
	// Convert markdown content to Notion blocks
	blocks := s.markdownToNotionBlocks(content)

	// Create page request
	reqBody := NotionCreatePageRequest{
		Parent: map[string]interface{}{
			"type":      "workspace",
			"workspace": true,
		},
		Properties: map[string]interface{}{
			"title": map[string]interface{}{
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
		Children: blocks,
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return "", "", fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.notion.com/v1/pages", bytes.NewReader(jsonBody))
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

	return pageResp.ID, pageResp.URL, nil
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

		var block NotionBlock
		block.Object = "block"

		// Check for headings
		if strings.HasPrefix(line, "### ") {
			block.Type = "heading_3"
			block.Heading3 = &NotionHeadingBlock{
				RichText: []NotionRichText{
					{Type: "text", Text: &NotionTextObj{Content: strings.TrimPrefix(line, "### ")}},
				},
			}
		} else if strings.HasPrefix(line, "## ") {
			block.Type = "heading_2"
			block.Heading2 = &NotionHeadingBlock{
				RichText: []NotionRichText{
					{Type: "text", Text: &NotionTextObj{Content: strings.TrimPrefix(line, "## ")}},
				},
			}
		} else if strings.HasPrefix(line, "# ") {
			block.Type = "heading_1"
			block.Heading1 = &NotionHeadingBlock{
				RichText: []NotionRichText{
					{Type: "text", Text: &NotionTextObj{Content: strings.TrimPrefix(line, "# ")}},
				},
			}
		} else if strings.HasPrefix(line, "- [ ] ") {
			// Unchecked to-do
			block.Type = "to_do"
			block.ToDo = &NotionToDoBlock{
				RichText: []NotionRichText{
					{Type: "text", Text: &NotionTextObj{Content: strings.TrimPrefix(line, "- [ ] ")}},
				},
				Checked: false,
			}
		} else if strings.HasPrefix(line, "- [x] ") || strings.HasPrefix(line, "- [X] ") {
			// Checked to-do
			text := strings.TrimPrefix(line, "- [x] ")
			text = strings.TrimPrefix(text, "- [X] ")
			block.Type = "to_do"
			block.ToDo = &NotionToDoBlock{
				RichText: []NotionRichText{
					{Type: "text", Text: &NotionTextObj{Content: text}},
				},
				Checked: true,
			}
		} else if strings.HasPrefix(line, "- ") {
			// Bulleted list
			block.Type = "bulleted_list_item"
			block.BulletedListItem = &NotionListItemBlock{
				RichText: []NotionRichText{
					{Type: "text", Text: &NotionTextObj{Content: strings.TrimPrefix(line, "- ")}},
				},
			}
		} else if strings.HasPrefix(line, "* ") {
			// Bulleted list (asterisk)
			block.Type = "bulleted_list_item"
			block.BulletedListItem = &NotionListItemBlock{
				RichText: []NotionRichText{
					{Type: "text", Text: &NotionTextObj{Content: strings.TrimPrefix(line, "* ")}},
				},
			}
		} else {
			// Regular paragraph
			block.Type = "paragraph"
			block.Paragraph = &NotionParagraphBlock{
				RichText: []NotionRichText{
					{Type: "text", Text: &NotionTextObj{Content: line}},
				},
			}
		}

		blocks = append(blocks, block)
	}

	return blocks
}
