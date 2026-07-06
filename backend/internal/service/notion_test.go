package service

import (
	"strings"
	"testing"
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
