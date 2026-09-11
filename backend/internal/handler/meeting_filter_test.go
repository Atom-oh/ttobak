package handler

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
)

func TestListMeetings_MultiAccountRequest(t *testing.T) {
	hundred := make([]string, 100)
	for i := range hundred {
		hundred[i] = fmt.Sprintf("acc-%03d", i)
	}
	for _, tt := range []struct {
		name, query string
		want        []string
		status      int
	}{
		{"union normalized", "accountIds=" + url.QueryEscape(" acc-b,acc-a,acc-b "), []string{"acc-a", "acc-b"}, 200},
		{"empty", "accountIds=", []string{}, 200},
		{"whitespace empty", "accountIds=%20", []string{}, 200},
		{"100 distinct plus duplicates", "accountIds=" + strings.Join(append(slices.Clone(hundred), hundred...), ","), hundred, 200},
		{"101 distinct", "accountIds=" + strings.Join(hundred, ",") + ",extra", nil, 400},
		{"ambiguous", "accountId=acc-a&accountIds=acc-b", nil, 400},
		{"ambiguous empty", "accountId=&accountIds=", nil, 400},
		{"repeated multi", "accountIds=acc-a&accountIds=acc-b", nil, 400},
		{"repeated legacy", "accountId=acc-a&accountId=acc-b", nil, 400},
		{"traversal", "accountIds=acc-a,..%2Fsecret", nil, 400},
		{"escaped traversal", "accountIds=%252e%252e", nil, 400},
		{"backslash", "accountIds=acc-a%5Cacc-b", nil, 400},
		{"empty element", "accountIds=acc-a,,acc-b", nil, 400},
		{"trailing comma", "accountIds=acc-a,", nil, 400},
		{"delimiter", "accountIds=ACCOUNT%23acc-a", nil, 400},
		{"control", "accountIds=acc%00a", nil, 400},
		{"long id", "accountIds=" + strings.Repeat("a", 129), nil, 400},
		{"legacy traversal", "accountId=..", nil, 400},
		{"invalid URL escape", "accountIds=%ZZ", nil, 400},
		{"invalid query separator", "accountIds=acc-a;acc-b", nil, 400},
		{"bad cursor", "accountIds=acc-a&cursor=not-a-cursor", nil, 400},
		{"legacy cursor cannot enter multi stream", "accountIds=acc-a&cursor=team%3Ainvalid", nil, 400},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h, repo := newStubMeetingHandler()
			r := withUserCtx(httptest.NewRequest(http.MethodGet, "/api/meetings?"+tt.query, nil), "viewer")
			w := httptest.NewRecorder()
			h.ListMeetings(w, r)
			if w.Code != tt.status {
				t.Fatalf("status = %d, want %d: %s", w.Code, tt.status, w.Body.String())
			}
			if tt.status == 400 {
				if len(repo.listMeetingsParams) != 0 {
					t.Fatal("invalid input reached the repository")
				}
				return
			}
			if len(repo.listMeetingsParams) == 0 {
				t.Fatal("request did not query meetings")
			}
			got := repo.listMeetingsParams[0]
			if got.UserID != "viewer" || got.Tab != "all" || got.AccountID != "" ||
				got.AccountIDs == nil || !slices.Equal(got.AccountIDs, tt.want) {
				t.Fatalf("wrong query scope: %+v", got)
			}
		})
	}
}
