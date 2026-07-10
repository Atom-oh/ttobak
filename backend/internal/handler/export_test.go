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

// resolveTranscriptLinksForExport must rewrite ADR-013 "transcript://{id}"
// deep links into absolute ttobak URLs before content reaches Notion —
// Notion's pages/blocks API rejects non-http(s) link schemes outright with
// "Invalid URL for link", which failed every Notion export for any meeting
// whose summary contains a deep link (i.e. every meeting) until this fix.
func TestResolveTranscriptLinksForExport(t *testing.T) {
	const baseURL = "https://ttobak.atomai.click"

	t.Run("rewrites a transcript link to an absolute app URL with the same anchor", func(t *testing.T) {
		md := "합의했다. [05:46](transcript://seg-346100) 추가로."
		got := resolveTranscriptLinksForExport(md, "a7964eee-84a8-42d9-a3ca-ca304990241c", baseURL)
		want := "합의했다. [05:46](https://ttobak.atomai.click/meeting/a7964eee-84a8-42d9-a3ca-ca304990241c#ts-seg-346100) 추가로."
		if got != want {
			t.Fatalf("got  %q\nwant %q", got, want)
		}
	})

	t.Run("rewrites multiple links in the same document", func(t *testing.T) {
		md := "[00:30](transcript://seg-30000) 그리고 [05:46](transcript://seg-346100)"
		got := resolveTranscriptLinksForExport(md, "meeting-1", baseURL)
		for _, want := range []string{
			"https://ttobak.atomai.click/meeting/meeting-1#ts-seg-30000",
			"https://ttobak.atomai.click/meeting/meeting-1#ts-seg-346100",
		} {
			if !strings.Contains(got, want) {
				t.Fatalf("expected output to contain %q, got %q", want, got)
			}
		}
		if strings.Contains(got, "transcript://") {
			t.Fatalf("transcript:// scheme leaked through: %q", got)
		}
	})

	t.Run("content without deep links passes through unchanged", func(t *testing.T) {
		md := "일반 텍스트, 링크 없음."
		if got := resolveTranscriptLinksForExport(md, "meeting-1", baseURL); got != md {
			t.Fatalf("got %q, want unchanged %q", got, md)
		}
	})

	t.Run("segment id and meeting id are path-escaped", func(t *testing.T) {
		md := "[00:30](transcript://seg 300#00)"
		got := resolveTranscriptLinksForExport(md, "meeting one", baseURL)
		want := "https://ttobak.atomai.click/meeting/meeting%20one#ts-seg%20300%2300"
		if !strings.Contains(got, want) {
			t.Fatalf("expected output to contain escaped URL %q, got %q", want, got)
		}
	})
}
