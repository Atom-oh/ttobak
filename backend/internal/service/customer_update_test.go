package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

type mockUpdateRepo struct {
	sources       []model.UpdateSource
	items         map[string]bool // urlHash -> exists
	drafts        map[string]*model.UpdateDraft
	listed        []model.UpdateDraft
	ensureCalls   int
	ensureErr     error
	putCalls      int
	putFailAt     int // 1-based PutItemsIfAbsent call that fails; 0 = never
	putErr        error
	markCalls     int
	putSourceErr  error
	updateSrcErr  error
	updateDrftErr error
	updated       *model.UpdateDraft
	updatedExpect int
	opt           map[string]bool
}

func newMockUpdateRepo() *mockUpdateRepo {
	return &mockUpdateRepo{items: map[string]bool{}, drafts: map[string]*model.UpdateDraft{}, opt: map[string]bool{}}
}

func (m *mockUpdateRepo) PutSourceIfAbsent(_ context.Context, _ string, s *model.UpdateSource) error {
	if m.putSourceErr != nil {
		return m.putSourceErr
	}
	m.sources = append(m.sources, *s)
	return nil
}
func (m *mockUpdateRepo) ListSources(_ context.Context, _ string, _ int) ([]model.UpdateSource, error) {
	return m.sources, nil
}
func (m *mockUpdateRepo) UpdateSource(_ context.Context, _, _ string, _ *string, _ *string) error {
	return m.updateSrcErr
}
func (m *mockUpdateRepo) DeleteSource(context.Context, string, string) error { return nil }
func (m *mockUpdateRepo) PutItemsIfAbsent(_ context.Context, _, _ string, items []model.UpdateItem) (int, error) {
	m.putCalls++
	if m.putFailAt != 0 && m.putCalls == m.putFailAt {
		return 0, m.putErr
	}
	n := 0
	for _, it := range items {
		if !m.items[it.URLHash] {
			m.items[it.URLHash] = true
			n++
		}
	}
	return n, nil
}
func (m *mockUpdateRepo) EnsureURLPending(context.Context, string, string) error {
	m.ensureCalls++
	return m.ensureErr
}
func (m *mockUpdateRepo) MarkPending(context.Context, string) error { m.markCalls++; return nil }
func (m *mockUpdateRepo) GetDraft(_ context.Context, _, accountID, urlHash string) (*model.UpdateDraft, error) {
	d := m.drafts[accountID+"#"+urlHash]
	if d == nil {
		return nil, nil
	}
	c := *d
	return &c, nil
}
func (m *mockUpdateRepo) ListDrafts(context.Context, string, string, int, string) ([]model.UpdateDraft, string, error) {
	return m.listed, "next", nil
}
func (m *mockUpdateRepo) UpdateDraftIfRevision(_ context.Context, _ string, d *model.UpdateDraft, expected int) error {
	if m.updateDrftErr != nil {
		return m.updateDrftErr
	}
	c := *d
	m.updated = &c
	m.updatedExpect = expected
	return nil
}
func (m *mockUpdateRepo) GetAccountOpt(_ context.Context, _, accountID string) (bool, error) {
	v, ok := m.opt[accountID]
	return !ok || v, nil
}
func (m *mockUpdateRepo) PutAccountOpt(_ context.Context, _, accountID string, enabled bool) error {
	m.opt[accountID] = enabled
	return nil
}

type mockMembers struct {
	denied map[string]error
	calls  map[string]int
}

func (m *mockMembers) RequireDirectMember(_ context.Context, _, accountID string) error {
	if m.calls == nil {
		m.calls = map[string]int{}
	}
	m.calls[accountID]++
	return m.denied[accountID]
}

var updateTestNow = time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)

func newUpdateSvc(repo *mockUpdateRepo, members *mockMembers) *CustomerUpdateService {
	return NewCustomerUpdateService(repo, members, func() time.Time { return updateTestNow })
}

func TestCustomerUpdateAddSource(t *testing.T) {
	long := strings.Repeat("가", 101)
	tests := []struct {
		name    string
		req     model.AddUpdateSourceRequest
		existed int
		wantErr error
	}{
		{"rss ok", model.AddUpdateSourceRequest{Kind: "rss", Label: "Blog", URL: "https://example.com/feed"}, 0, nil},
		{"slack ok", model.AddUpdateSourceRequest{Kind: "slack", Label: "Chan", LocalSourceID: "local-1"}, 0, nil},
		{"rss http", model.AddUpdateSourceRequest{Kind: "rss", Label: "Blog", URL: "http://example.com/feed"}, 0, ErrInvalidInput},
		{"site internal", model.AddUpdateSourceRequest{Kind: "site", Label: "Wiki", URL: "https://w.amazon.com/x"}, 0, ErrInvalidInput},
		{"youtube internal slack host", model.AddUpdateSourceRequest{Kind: "youtube", Label: "Y", URL: "https://foo.slack.com/x"}, 0, ErrInvalidInput},
		{"slack with url", model.AddUpdateSourceRequest{Kind: "slack", Label: "Chan", LocalSourceID: "l", URL: "https://example.com"}, 0, ErrInvalidInput},
		{"slack without local id", model.AddUpdateSourceRequest{Kind: "slack", Label: "Chan"}, 0, ErrInvalidInput},
		{"rss with local id", model.AddUpdateSourceRequest{Kind: "rss", Label: "B", URL: "https://example.com/f", LocalSourceID: "l"}, 0, ErrInvalidInput},
		{"unknown kind", model.AddUpdateSourceRequest{Kind: "ftp", Label: "B", URL: "https://example.com/f"}, 0, ErrInvalidInput},
		{"empty label", model.AddUpdateSourceRequest{Kind: "rss", Label: "  ", URL: "https://example.com/f"}, 0, ErrInvalidInput},
		{"label 100 ok", model.AddUpdateSourceRequest{Kind: "rss", Label: strings.Repeat("가", 100), URL: "https://example.com/f"}, 0, nil},
		{"label 101", model.AddUpdateSourceRequest{Kind: "rss", Label: long, URL: "https://example.com/f"}, 0, ErrInvalidInput},
		{"cap reached", model.AddUpdateSourceRequest{Kind: "rss", Label: "B", URL: "https://example.com/f"}, model.MaxUpdateSources, ErrConflict},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newMockUpdateRepo()
			for i := 0; i < tt.existed; i++ {
				repo.sources = append(repo.sources, model.UpdateSource{SourceID: fmt.Sprintf("s%d", i)})
			}
			before := len(repo.sources)
			got, err := newUpdateSvc(repo, &mockMembers{}).AddSource(context.Background(), "u1", &tt.req)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if tt.wantErr != nil {
				if len(repo.sources) != before {
					t.Fatalf("source written despite error")
				}
				return
			}
			if got.SourceID == "" || got.Status != model.UpdateSourceActive || !got.CreatedAt.Equal(updateTestNow) {
				t.Fatalf("unexpected source %+v", got)
			}
		})
	}
}

func TestCustomerUpdateAddSourceConditionFailedIsConflict(t *testing.T) {
	repo := newMockUpdateRepo()
	repo.putSourceErr = fmt.Errorf("wrap: %w", repository.ErrConditionFailed)
	_, err := newUpdateSvc(repo, &mockMembers{}).AddSource(context.Background(), "u1",
		&model.AddUpdateSourceRequest{Kind: "rss", Label: "B", URL: "https://example.com/f"})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
}

func TestCustomerUpdateUpdateSource(t *testing.T) {
	bad := "paused"
	good := model.UpdateSourceDisabled
	empty := ""
	tests := []struct {
		name    string
		req     *model.PatchUpdateSourceRequest
		repoErr error
		wantErr error
	}{
		{"ok", &model.PatchUpdateSourceRequest{Status: &good}, nil, nil},
		{"nothing", &model.PatchUpdateSourceRequest{}, nil, ErrInvalidInput},
		{"bad status", &model.PatchUpdateSourceRequest{Status: &bad}, nil, ErrInvalidInput},
		{"empty label", &model.PatchUpdateSourceRequest{Label: &empty}, nil, ErrInvalidInput},
		{"missing source", &model.PatchUpdateSourceRequest{Status: &good}, repository.ErrConditionFailed, ErrNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newMockUpdateRepo()
			repo.updateSrcErr = tt.repoErr
			err := newUpdateSvc(repo, &mockMembers{}).UpdateSource(context.Background(), "u1", "src1", tt.req)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestCustomerUpdateUploadLinks(t *testing.T) {
	slack := model.UpdateSource{SourceID: "slack1", Kind: model.UpdateSourceKindSlack}
	rss := model.UpdateSource{SourceID: "rss1", Kind: model.UpdateSourceKindRSS}

	t.Run("201 links rejected before any write", func(t *testing.T) {
		repo := newMockUpdateRepo()
		repo.sources = []model.UpdateSource{slack}
		links := make([]model.UpdateUploadLink, 201)
		for i := range links {
			links[i].URL = fmt.Sprintf("https://example.com/%d", i)
		}
		_, err := newUpdateSvc(repo, &mockMembers{}).UploadLinks(context.Background(), "u1",
			&model.UploadUpdateLinksRequest{SourceID: "slack1", Links: links})
		if !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("err = %v, want ErrInvalidInput", err)
		}
		if len(repo.items) != 0 || repo.ensureCalls != 0 || repo.markCalls != 0 {
			t.Fatalf("writes happened: items=%d ensure=%d mark=%d", len(repo.items), repo.ensureCalls, repo.markCalls)
		}
	})

	for _, tt := range []struct {
		name     string
		sourceID string
	}{
		{"not the callers source", "other"},
		{"not a slack source", "rss1"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			repo := newMockUpdateRepo()
			repo.sources = []model.UpdateSource{slack, rss}
			_, err := newUpdateSvc(repo, &mockMembers{}).UploadLinks(context.Background(), "u1",
				&model.UploadUpdateLinksRequest{SourceID: tt.sourceID, Links: []model.UpdateUploadLink{{URL: "https://example.com/a"}}})
			if !errors.Is(err, ErrNotFound) {
				t.Fatalf("err = %v, want ErrNotFound", err)
			}
			if repo.markCalls != 0 || len(repo.items) != 0 {
				t.Fatalf("writes happened")
			}
		})
	}

	t.Run("mixed outcomes", func(t *testing.T) {
		repo := newMockUpdateRepo()
		repo.sources = []model.UpdateSource{slack}
		_, dupHash, _ := model.NormalizeUpdateURL("https://example.com/dup")
		repo.items[dupHash] = true
		resp, err := newUpdateSvc(repo, &mockMembers{}).UploadLinks(context.Background(), "u1",
			&model.UploadUpdateLinksRequest{SourceID: "slack1", Links: []model.UpdateUploadLink{
				{URL: "https://example.com/a?utm_source=x", SeenAt: "2026-11-01T00:00:00Z"},
				{URL: "https://example.com/b"},
				{URL: "https://example.com/dup"},
				{URL: "https://team.slack.com/archives/C1"},
			}})
		if err != nil {
			t.Fatal(err)
		}
		if resp.Accepted != 2 || resp.Duplicate != 1 || resp.Rejected != 1 {
			t.Fatalf("resp = %+v", resp)
		}
		if repo.ensureCalls != 3 || repo.markCalls != 1 {
			t.Fatalf("ensure=%d mark=%d, want 3 and 1", repo.ensureCalls, repo.markCalls)
		}
	})

	t.Run("ensure failure writes no item and does not mark pending", func(t *testing.T) {
		repo := newMockUpdateRepo()
		repo.sources = []model.UpdateSource{slack}
		repo.ensureErr = errors.New("ensure failed")
		_, err := newUpdateSvc(repo, &mockMembers{}).UploadLinks(context.Background(), "u1",
			&model.UploadUpdateLinksRequest{SourceID: "slack1", Links: []model.UpdateUploadLink{
				{URL: "https://example.com/a"}, {URL: "https://example.com/b"},
			}})
		if !errors.Is(err, repo.ensureErr) {
			t.Fatalf("err = %v, want ensure error", err)
		}
		if len(repo.items) != 0 || repo.markCalls != 0 || repo.ensureCalls != 1 {
			t.Fatalf("items=%d mark=%d ensure=%d", len(repo.items), repo.markCalls, repo.ensureCalls)
		}
	})

	t.Run("same link twice in one upload counts once", func(t *testing.T) {
		repo := newMockUpdateRepo()
		repo.sources = []model.UpdateSource{slack}
		resp, err := newUpdateSvc(repo, &mockMembers{}).UploadLinks(context.Background(), "u1",
			&model.UploadUpdateLinksRequest{SourceID: "slack1", Links: []model.UpdateUploadLink{
				{URL: "https://example.com/a"}, {URL: "https://example.com/a#frag"},
			}})
		if err != nil {
			t.Fatal(err)
		}
		if resp.Accepted != 1 || resp.Duplicate != 1 || repo.ensureCalls != 1 {
			t.Fatalf("resp = %+v ensure=%d", resp, repo.ensureCalls)
		}
	})

	t.Run("item write failure after partial progress still marks pending", func(t *testing.T) {
		repo := newMockUpdateRepo()
		repo.sources = []model.UpdateSource{slack}
		repo.putFailAt = 3
		repo.putErr = errors.New("put failed")
		_, err := newUpdateSvc(repo, &mockMembers{}).UploadLinks(context.Background(), "u1",
			&model.UploadUpdateLinksRequest{SourceID: "slack1", Links: []model.UpdateUploadLink{
				{URL: "https://example.com/1"}, {URL: "https://example.com/2"},
				{URL: "https://example.com/3"}, {URL: "https://example.com/4"},
			}})
		if !errors.Is(err, repo.putErr) {
			t.Fatalf("err = %v, want put error", err)
		}
		if repo.markCalls != 1 || len(repo.items) != 2 {
			t.Fatalf("mark=%d items=%d, want 1 and 2", repo.markCalls, len(repo.items))
		}
	})

	t.Run("retry of all-duplicate links heals the pending marker", func(t *testing.T) {
		repo := newMockUpdateRepo()
		repo.sources = []model.UpdateSource{slack}
		for _, u := range []string{"https://example.com/1", "https://example.com/2"} {
			_, h, _ := model.NormalizeUpdateURL(u)
			repo.items[h] = true
		}
		resp, err := newUpdateSvc(repo, &mockMembers{}).UploadLinks(context.Background(), "u1",
			&model.UploadUpdateLinksRequest{SourceID: "slack1", Links: []model.UpdateUploadLink{
				{URL: "https://example.com/1"}, {URL: "https://example.com/2"},
			}})
		if err != nil {
			t.Fatal(err)
		}
		if resp.Accepted != 0 || resp.Duplicate != 2 || repo.markCalls != 1 {
			t.Fatalf("resp = %+v mark=%d", resp, repo.markCalls)
		}
	})

	t.Run("all rejected does not mark pending", func(t *testing.T) {
		repo := newMockUpdateRepo()
		repo.sources = []model.UpdateSource{slack}
		resp, err := newUpdateSvc(repo, &mockMembers{}).UploadLinks(context.Background(), "u1",
			&model.UploadUpdateLinksRequest{SourceID: "slack1", Links: []model.UpdateUploadLink{
				{URL: "http://example.com/a"}, {URL: "https://x.slack.com/y"},
			}})
		if err != nil {
			t.Fatal(err)
		}
		if resp.Rejected != 2 || resp.Accepted != 0 || repo.markCalls != 0 {
			t.Fatalf("resp = %+v mark=%d", resp, repo.markCalls)
		}
	})
}

func TestCustomerUpdateListDrafts(t *testing.T) {
	past := updateTestNow.Add(-time.Hour).Unix()
	future := updateTestNow.Add(time.Hour).Unix()

	t.Run("single account non-member forbidden", func(t *testing.T) {
		members := &mockMembers{denied: map[string]error{"a1": ErrForbidden}}
		_, err := newUpdateSvc(newMockUpdateRepo(), members).ListDrafts(context.Background(), "u1", "a1", 10, "")
		if !errors.Is(err, ErrForbidden) {
			t.Fatalf("err = %v, want ErrForbidden", err)
		}
	})

	t.Run("across accounts drops non-member accounts and checks once per account", func(t *testing.T) {
		repo := newMockUpdateRepo()
		repo.listed = []model.UpdateDraft{
			{AccountID: "a1", URLHash: "h1", ExpiresAt: future},
			{AccountID: "a2", URLHash: "h2", ExpiresAt: future},
			{AccountID: "a1", URLHash: "h3"},
			{AccountID: "a2", URLHash: "h4", ExpiresAt: future},
		}
		members := &mockMembers{denied: map[string]error{"a2": ErrForbidden}}
		page, err := newUpdateSvc(repo, members).ListDrafts(context.Background(), "u1", "", 10, "")
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Drafts) != 2 || page.Drafts[0].URLHash != "h1" || page.Drafts[1].URLHash != "h3" {
			t.Fatalf("drafts = %+v", page.Drafts)
		}
		if members.calls["a1"] != 1 || members.calls["a2"] != 1 {
			t.Fatalf("calls = %v", members.calls)
		}
		if page.NextCursor != "next" {
			t.Fatalf("cursor = %q", page.NextCursor)
		}
	})

	t.Run("expired drafts are dropped synchronously", func(t *testing.T) {
		repo := newMockUpdateRepo()
		repo.listed = []model.UpdateDraft{
			{AccountID: "a1", URLHash: "old", ExpiresAt: past},
			{AccountID: "a1", URLHash: "live", ExpiresAt: future},
		}
		page, err := newUpdateSvc(repo, &mockMembers{}).ListDrafts(context.Background(), "u1", "a1", 10, "")
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Drafts) != 1 || page.Drafts[0].URLHash != "live" {
			t.Fatalf("drafts = %+v", page.Drafts)
		}
	})

	t.Run("membership lookup failure is returned", func(t *testing.T) {
		repo := newMockUpdateRepo()
		repo.listed = []model.UpdateDraft{{AccountID: "a1", URLHash: "h"}}
		boom := errors.New("boom")
		_, err := newUpdateSvc(repo, &mockMembers{denied: map[string]error{"a1": boom}}).ListDrafts(context.Background(), "u1", "", 10, "")
		if !errors.Is(err, boom) {
			t.Fatalf("err = %v, want boom", err)
		}
	})
}

func TestCustomerUpdatePatchDraft(t *testing.T) {
	hash := strings.Repeat("a", 64)
	future := updateTestNow.Add(24 * time.Hour).Unix()
	past := updateTestNow.Add(-time.Hour).Unix()
	base := func() *model.UpdateDraft {
		return &model.UpdateDraft{AccountID: "a1", URLHash: hash, Body: "model text", Status: model.UpdateDraftNew,
			Revision: 3, ExpiresAt: future}
	}
	tests := []struct {
		name     string
		draft    *model.UpdateDraft
		req      model.PatchUpdateDraftRequest
		repoErr  error
		wantErr  error
		wantBody func(t *testing.T, got *model.UpdateDraft)
	}{
		{name: "edit too long", draft: base(),
			req:     model.PatchUpdateDraftRequest{Action: "edit", Body: strings.Repeat("가", 2001), Revision: 3},
			wantErr: ErrInvalidInput},
		{name: "edit exactly 2000", draft: base(),
			req: model.PatchUpdateDraftRequest{Action: "edit", Body: strings.Repeat("가", 2000), Revision: 3},
			wantBody: func(t *testing.T, got *model.UpdateDraft) {
				if got.Body != "model text" || got.EditedBody != strings.Repeat("가", 2000) {
					t.Fatalf("body/edited wrong: %q", got.Body)
				}
			}},
		{name: "dismiss bad reason", draft: base(),
			req:     model.PatchUpdateDraftRequest{Action: "dismiss", Reason: "boring", Revision: 3},
			wantErr: ErrInvalidInput},
		{name: "unknown action", draft: base(),
			req: model.PatchUpdateDraftRequest{Action: "delete", Revision: 3}, wantErr: ErrInvalidInput},
		{name: "stale revision precheck", draft: base(),
			req: model.PatchUpdateDraftRequest{Action: "adopt", Revision: 2}, wantErr: ErrConflict},
		{name: "condition failed maps to conflict", draft: base(),
			req:     model.PatchUpdateDraftRequest{Action: "adopt", Revision: 3},
			repoErr: fmt.Errorf("x: %w", repository.ErrConditionFailed), wantErr: ErrConflict},
		{name: "missing draft", draft: nil,
			req: model.PatchUpdateDraftRequest{Action: "adopt", Revision: 0}, wantErr: ErrNotFound},
		{name: "expired draft", draft: func() *model.UpdateDraft { d := base(); d.ExpiresAt = past; return d }(),
			req: model.PatchUpdateDraftRequest{Action: "adopt", Revision: 3}, wantErr: ErrNotFound},
		{name: "adopt", draft: base(),
			req: model.PatchUpdateDraftRequest{Action: "adopt", Revision: 3},
			wantBody: func(t *testing.T, got *model.UpdateDraft) {
				if got.Status != model.UpdateDraftAdopted || got.Revision != 4 || got.ExpiresAt != future {
					t.Fatalf("got %+v", got)
				}
			}},
		{name: "dismiss", draft: base(),
			req: model.PatchUpdateDraftRequest{Action: "dismiss", Reason: "not_now", Revision: 3},
			wantBody: func(t *testing.T, got *model.UpdateDraft) {
				if got.Status != model.UpdateDraftDismissed || got.DismissReason != "not_now" {
					t.Fatalf("got %+v", got)
				}
			}},
		{name: "reopen dismissed", draft: func() *model.UpdateDraft {
			d := base()
			d.Status = model.UpdateDraftDismissed
			d.DismissReason = "not_now"
			return d
		}(),
			req: model.PatchUpdateDraftRequest{Action: "reopen", Revision: 3},
			wantBody: func(t *testing.T, got *model.UpdateDraft) {
				if got.Status != model.UpdateDraftNew || got.DismissReason != "" || got.Revision != 4 {
					t.Fatalf("got %+v", got)
				}
			}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newMockUpdateRepo()
			if tt.draft != nil {
				repo.drafts["a1#"+hash] = tt.draft
			}
			repo.updateDrftErr = tt.repoErr
			req := tt.req
			got, err := newUpdateSvc(repo, &mockMembers{}).PatchDraft(context.Background(), "u1", "a1", hash, &req)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if tt.wantErr != nil {
				if repo.updated != nil {
					t.Fatalf("draft written despite error")
				}
				return
			}
			if repo.updatedExpect != tt.req.Revision {
				t.Fatalf("expected revision passed = %d", repo.updatedExpect)
			}
			if tt.wantBody != nil {
				tt.wantBody(t, got)
			}
		})
	}

	t.Run("non-member forbidden before read", func(t *testing.T) {
		members := &mockMembers{denied: map[string]error{"a1": ErrForbidden}}
		_, err := newUpdateSvc(newMockUpdateRepo(), members).PatchDraft(context.Background(), "u1", "a1", hash,
			&model.PatchUpdateDraftRequest{Action: "adopt"})
		if !errors.Is(err, ErrForbidden) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("bad url hash", func(t *testing.T) {
		_, err := newUpdateSvc(newMockUpdateRepo(), &mockMembers{}).PatchDraft(context.Background(), "u1", "a1", "xyz",
			&model.PatchUpdateDraftRequest{Action: "adopt"})
		if !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestCustomerUpdateAccountSetting(t *testing.T) {
	repo := newMockUpdateRepo()
	svc := newUpdateSvc(repo, &mockMembers{denied: map[string]error{"nope": ErrForbidden}})
	ctx := context.Background()
	if on, err := svc.GetAccountSetting(ctx, "u1", "a1"); err != nil || !on {
		t.Fatalf("default = %v, %v", on, err)
	}
	if err := svc.PutAccountSetting(ctx, "u1", "a1", false); err != nil {
		t.Fatal(err)
	}
	if on, _ := svc.GetAccountSetting(ctx, "u1", "a1"); on {
		t.Fatal("expected disabled")
	}
	if err := svc.PutAccountSetting(ctx, "u1", "nope", true); !errors.Is(err, ErrForbidden) {
		t.Fatalf("err = %v", err)
	}
	if _, err := svc.GetAccountSetting(ctx, "u1", "nope"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("err = %v", err)
	}
}

type mockDirectMemberRepo struct {
	account *model.Account
	member  *model.AccountMember
	err     error
}

func (m *mockDirectMemberRepo) GetAccount(context.Context, string) (*model.Account, error) {
	return m.account, m.err
}
func (m *mockDirectMemberRepo) GetMember(context.Context, string, string) (*model.AccountMember, error) {
	return m.member, m.err
}

func TestDirectMemberChecker(t *testing.T) {
	acct := &model.Account{}
	tests := []struct {
		name    string
		repo    *mockDirectMemberRepo
		user    string
		account string
		wantErr error
	}{
		{"member", &mockDirectMemberRepo{account: acct, member: &model.AccountMember{UserID: "u1"}}, "u1", "a1", nil},
		{"no member row", &mockDirectMemberRepo{account: acct}, "u1", "a1", ErrForbidden},
		{"other user row", &mockDirectMemberRepo{account: acct, member: &model.AccountMember{UserID: "u2"}}, "u1", "a1", ErrForbidden},
		{"missing account", &mockDirectMemberRepo{}, "u1", "a1", ErrNotFound},
		{"bad account id", &mockDirectMemberRepo{account: acct}, "u1", "a/1", ErrInvalidInput},
		{"empty account id", &mockDirectMemberRepo{account: acct}, "u1", "", ErrInvalidInput},
		{"bad user id", &mockDirectMemberRepo{account: acct}, "", "a1", ErrInvalidInput},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := NewDirectMemberChecker(tt.repo).RequireDirectMember(context.Background(), tt.user, tt.account)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
		})
	}
	boom := errors.New("boom")
	if err := NewDirectMemberChecker(&mockDirectMemberRepo{err: boom}).RequireDirectMember(context.Background(), "u1", "a1"); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
}
