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
