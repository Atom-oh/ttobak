package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestListMeetings_ForwardsAccountFilterWithinCallerScope(t *testing.T) {
	for _, query := range []struct {
		url       string
		tab       string
		accountID string
		cursor    string
	}{
		{"/api/meetings", "all", "", ""},
		{"/api/meetings?accountId=acc-a", "all", "acc-a", ""},
		{"/api/meetings?tab=shared&accountId=acc-a&cursor=next-page", "shared", "acc-a", "next-page"},
	} {
		t.Run(query.url, func(t *testing.T) {
			h, repo := newStubMeetingHandler()
			req := withUserCtx(httptest.NewRequest(http.MethodGet, query.url, nil), "viewer")
			w := httptest.NewRecorder()
			h.ListMeetings(w, req)
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
			}
			if len(repo.listMeetingsParams) != 1 {
				t.Fatalf("expected one query, got %d", len(repo.listMeetingsParams))
			}
			got := repo.listMeetingsParams[0]
			if got.UserID != "viewer" || got.AccountID != query.accountID ||
				got.Tab != query.tab || got.Cursor != query.cursor || got.Limit != 20 {
				t.Fatalf("unexpected list parameters: %+v", got)
			}
		})
	}
}

func TestListMeetings_RejectsMalformedTeamCursor(t *testing.T) {
	h, _ := newStubMeetingHandler()
	req := withUserCtx(httptest.NewRequest(http.MethodGet, "/api/meetings?cursor=team%3Ainvalid", nil), "viewer")
	w := httptest.NewRecorder()
	h.ListMeetings(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected invalid cursor to return 400, got %d: %s", w.Code, w.Body.String())
	}
}
