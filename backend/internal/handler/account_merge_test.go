package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/service"
)

type stubMergePreviewer struct {
	err    error
	called bool
	args   [3]string
}

func (s *stubMergePreviewer) Preview(_ context.Context, requester, from, into string) (*model.AccountMergePreview, error) {
	s.called, s.args = true, [3]string{requester, from, into}
	if s.err != nil {
		return nil, s.err
	}
	return &model.AccountMergePreview{FromAccountID: from, IntoAccountID: into, Counts: map[string]int{"members": 2},
		Unmoved: []model.MergeUnmovedCategory{{Category: "kb", Present: true}}, CLIOnly: []string{"specreqTasks"}}, nil
}

func TestMergePreviewHandler(t *testing.T) {
	for _, tc := range []struct {
		name, target string
		err          error
		status       int
		code         string
	}{
		{name: "ok", target: "/api/accounts/a/merge-preview?into=b", status: 200},
		{name: "missing into", target: "/api/accounts/a/merge-preview", status: 400, code: "BAD_REQUEST"},
		{name: "empty into", target: "/api/accounts/a/merge-preview?into=", status: 400, code: "BAD_REQUEST"},
		{name: "invalid", target: "/api/accounts/a/merge-preview?into=a", err: service.ErrInvalidInput, status: 400, code: "BAD_REQUEST"},
		{name: "forbidden", target: "/api/accounts/a/merge-preview?into=b", err: service.ErrForbidden, status: 403, code: "FORBIDDEN"},
		{name: "not found", target: "/api/accounts/a/merge-preview?into=b", err: service.ErrNotFound, status: 404, code: "NOT_FOUND"},
		{name: "ineligible", target: "/api/accounts/a/merge-preview?into=b",
			err: fmt.Errorf("%w: the target is a descendant of the source", service.ErrAccountMergeIneligible), status: 409, code: "MERGE_INELIGIBLE"},
		{name: "storage error", target: "/api/accounts/a/merge-preview?into=b", err: fmt.Errorf("dynamodb down"), status: 500, code: "INTERNAL_ERROR"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := &stubMergePreviewer{err: tc.err}
			r := withChiParam(withUserEmailCtx(httptest.NewRequest(http.MethodGet, tc.target, nil), "u1", ""), "accountId", "a")
			w := httptest.NewRecorder()
			NewAccountMergeHandler(stub).Preview(w, r)
			if w.Code != tc.status {
				t.Fatalf("status=%d body=%s want %d", w.Code, w.Body.String(), tc.status)
			}
			var body map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if tc.status == 200 {
				if stub.args != [3]string{"u1", "a", "b"} || body["fromAccountId"] != "a" || body["intoAccountId"] != "b" {
					t.Fatalf("args %v body %s", stub.args, w.Body.String())
				}
				return
			}
			detail, ok := body["error"].(map[string]any)
			if !ok || detail["code"] != tc.code {
				t.Fatalf("error contract: %s", w.Body.String())
			}
			if tc.name == "ineligible" && detail["message"] != "account merge ineligible: the target is a descendant of the source" {
				t.Fatalf("reason missing: %s", w.Body.String())
			}
			if tc.name == "missing into" || tc.name == "empty into" {
				if stub.called {
					t.Fatal("service called without into")
				}
			}
			if tc.name == "storage error" && detail["message"] == "dynamodb down" {
				t.Fatal("internal error leaked")
			}
		})
	}
}
