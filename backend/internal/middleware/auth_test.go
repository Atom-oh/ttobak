package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIsAdmin(t *testing.T) {
	tests := []struct {
		name   string
		groups interface{} // stored under UserGroupsKey; interface{} to cover the "unset" case
		want   bool
	}{
		{name: "admins group present", groups: []string{"admins"}, want: true},
		{name: "admins among several groups", groups: []string{"users", "admins", "beta"}, want: true},
		{name: "non-admin groups only", groups: []string{"users", "beta"}, want: false},
		{name: "empty group slice", groups: []string{}, want: false},
		{name: "nil groups", groups: []string(nil), want: false},
		{name: "no groups key set", groups: nil, want: false},
		{name: "case-sensitive — Admins is not admins", groups: []string{"Admins"}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			if tt.groups != nil {
				ctx = context.WithValue(ctx, UserGroupsKey, tt.groups)
			}
			if got := IsAdmin(ctx); got != tt.want {
				t.Errorf("IsAdmin() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRequireAdmin(t *testing.T) {
	nextCalled := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
		w.WriteHeader(http.StatusOK)
	})

	tests := []struct {
		name       string
		groups     []string
		wantStatus int
		wantNext   bool
	}{
		{name: "admin passes through", groups: []string{"admins"}, wantStatus: http.StatusOK, wantNext: true},
		{name: "non-admin rejected", groups: []string{"users"}, wantStatus: http.StatusForbidden, wantNext: false},
		{name: "no groups rejected", groups: nil, wantStatus: http.StatusForbidden, wantNext: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nextCalled = false
			req := httptest.NewRequest(http.MethodPost, "/api/settings/invite-user", nil)
			ctx := context.WithValue(req.Context(), UserGroupsKey, tt.groups)
			req = req.WithContext(ctx)
			rec := httptest.NewRecorder()

			RequireAdmin(next).ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if nextCalled != tt.wantNext {
				t.Errorf("next called = %v, want %v", nextCalled, tt.wantNext)
			}
		})
	}
}

func TestGetUserGroups(t *testing.T) {
	// Populated case
	ctx := context.WithValue(context.Background(), UserGroupsKey, []string{"admins", "users"})
	got := GetUserGroups(ctx)
	if len(got) != 2 || got[0] != "admins" || got[1] != "users" {
		t.Errorf("GetUserGroups() = %v, want [admins users]", got)
	}

	// Unset case returns nil (not a panic)
	if got := GetUserGroups(context.Background()); got != nil {
		t.Errorf("GetUserGroups() on empty ctx = %v, want nil", got)
	}
}
