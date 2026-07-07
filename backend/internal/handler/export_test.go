package handler

import (
	"strings"
	"testing"
)

// contentAsMarkdown must leave real markdown untouched and convert TipTap HTML
// back to markdown — meetings whose summary was edited in the editor before the
// frontend started saving markdown have HTML stored in content, and exporting
// that verbatim renders literal <h1>/<p> tags in Notion.
func TestContentAsMarkdown(t *testing.T) {
	t.Run("plain markdown passes through unchanged", func(t *testing.T) {
		md := "# 회의록\n\n## 참석자\n\n- **오준석 SA**: 기술 자문\n"
		if got := contentAsMarkdown(md); got != md {
			t.Fatalf("markdown was altered:\ngot:  %q\nwant: %q", got, md)
		}
	})

	t.Run("empty string passes through", func(t *testing.T) {
		if got := contentAsMarkdown(""); got != "" {
			t.Fatalf("expected empty string, got %q", got)
		}
	})

	t.Run("markdown autolink is not mistaken for HTML", func(t *testing.T) {
		// Starts with "<" but has no closing tag — a bare autolink, not HTML.
		md := "<https://example.com> 참고 링크입니다"
		if got := contentAsMarkdown(md); got != md {
			t.Fatalf("autolink markdown was altered:\ngot:  %q\nwant: %q", got, md)
		}
	})

	t.Run("tiptap HTML converts to markdown", func(t *testing.T) {
		html := `<h1>회의록</h1><p></p><h2>참석자</h2><ul><li><p><strong>오준석 SA</strong>: AWS 솔루션즈 아키텍트</p></li><li><p><br></p></li><li><p><strong>강광일 TAM</strong>: AWS 서포트</p></li></ul>`
		got := contentAsMarkdown(html)

		// No raw HTML tags should survive.
		for _, tag := range []string{"<h1>", "<h2>", "<p>", "<ul>", "<li>", "<br>", "<strong>"} {
			if strings.Contains(got, tag) {
				t.Fatalf("converted output still contains %q:\n%s", tag, got)
			}
		}
		// Markdown structure the exporter expects.
		for _, want := range []string{"# 회의록", "## 참석자", "오준석 SA", "강광일 TAM"} {
			if !strings.Contains(got, want) {
				t.Fatalf("converted output missing %q:\n%s", want, got)
			}
		}
	})
}
