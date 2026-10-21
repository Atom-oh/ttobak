package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ttobak/backend/internal/model"
)

func TestCrawlerRequestBodyLimitsAndUnicode(t *testing.T) {
	for name, body := range map[string]string{
		"oversized":     `{"sourceName":"` + strings.Repeat("a", 64*1024) + `"}`,
		"invalid UTF8":  "{\"sourceName\":\"\xff\"}",
		"trailing JSON": `{"sourceName":"source"} {"aliases":[]}`,
	} {
		t.Run(name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodPost, "/api/crawler/sources", strings.NewReader(body))
			var target model.AddCrawlerSourceRequest
			if decodeCrawlerRequest(w, r, &target) || w.Code != http.StatusBadRequest {
				t.Fatalf("invalid request accepted: %d", w.Code)
			}
		})
	}
	w := httptest.NewRecorder()
	var target model.AddCrawlerSourceRequest
	r := httptest.NewRequest(http.MethodPost, "/api/crawler/sources", strings.NewReader(`{"sourceName":"우리은행","aliases":["Woori Bank","𠮷"]}`))
	if !decodeCrawlerRequest(w, r, &target) || target.SourceName != "우리은행" || len(target.Aliases) != 2 {
		t.Fatalf("valid Unicode request rejected: %+v", target)
	}
}
