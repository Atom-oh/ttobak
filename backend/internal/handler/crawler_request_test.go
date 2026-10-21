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

func TestCrawlerAliasRemovalRequiresOwner(t *testing.T) {
	for _, owner := range []string{testUserID, "another-user"} {
		repo := newMockCrawlerRepo()
		repo.sources["source"] = &model.CrawlerSource{
			SourceID: "source", SourceName: "Source", OwnerID: owner,
			Subscribers: []string{testUserID}, Aliases: []string{"Saved"}, Status: "idle",
		}
		repo.subscriptions[subKey(testUserID, "source")] = &model.CrawlerSubscription{SourceID: "source"}
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPut, "/api/crawler/sources/source",
			strings.NewReader(`{"removeAliases":["saved"]}`))
		setupCrawlerRouter(repo).ServeHTTP(w, r)
		expected := http.StatusForbidden
		if owner == testUserID {
			expected = http.StatusOK
		}
		if w.Code != expected {
			t.Fatalf("owner %s: status %d, want %d: %s", owner, w.Code, expected, w.Body.String())
		}
		if owner != testUserID && len(repo.sources["source"].Aliases) != 1 {
			t.Fatal("forbidden removal changed saved aliases")
		}
	}
}
