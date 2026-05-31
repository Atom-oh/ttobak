package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

type mockVaultRepo struct {
	owned    []model.Meeting
	full     map[string]*model.Meeting
	accounts map[string]*model.Account
}

func (m *mockVaultRepo) ListMeetings(_ context.Context, _ repository.ListMeetingsParams) (*repository.ListMeetingsResult, error) {
	return &repository.ListMeetingsResult{Meetings: m.owned, NextCursor: nil}, nil
}
func (m *mockVaultRepo) GetMeeting(_ context.Context, _ string, meetingID string) (*model.Meeting, error) {
	return m.full[meetingID], nil
}
func (m *mockVaultRepo) ListAttachments(_ context.Context, _ string) ([]model.Attachment, error) {
	return nil, nil
}
func (m *mockVaultRepo) GetAccount(_ context.Context, accountID string) (*model.Account, error) {
	return m.accounts[accountID], nil
}

func TestExportVault_PlacesSharedAndPrivate(t *testing.T) {
	d := time.Date(2026, 5, 12, 9, 0, 0, 0, time.UTC)
	shared := model.Meeting{MeetingID: "m-1", UserID: "u1", Title: "ROSA 리뷰", Date: d, Status: model.StatusDone, AccountID: "acc-1", SharedToAccount: true, Insights: `[{"type":"risk","text":"x"}]`}
	priv := model.Meeting{MeetingID: "m-2", UserID: "u1", Title: "개인 메모", Date: d, Status: model.StatusDone}
	repo := &mockVaultRepo{
		owned: []model.Meeting{shared, priv},
		full:  map[string]*model.Meeting{"m-1": &shared, "m-2": &priv},
		accounts: map[string]*model.Account{"acc-1": {AccountID: "acc-1", Name: "하나은행"}},
	}
	svc := newVaultServiceWithRepo(repo)

	files, err := svc.ExportVault(context.Background(), "u1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("expected 2 files, got %d", len(files))
	}
	byPath := map[string]string{}
	for _, f := range files {
		byPath[f.Path] = f.Markdown
	}
	sharedMd, ok := byPath["Accounts/하나은행/2026-05-12 ROSA 리뷰.md"]
	if !ok {
		t.Fatalf("shared meeting not placed under account folder; paths=%v", byPath)
	}
	if !strings.Contains(sharedMd, `account: "[[하나은행]]"`) || !strings.Contains(sharedMd, "ttobak_id: m-1") || !strings.Contains(sharedMd, "insights: {risk: 1}") {
		t.Errorf("frontmatter missing fields:\n%s", sharedMd)
	}
	if !strings.Contains(sharedMd, "# ROSA 리뷰") {
		t.Error("expected GenerateMeetingDocument body (H1 title)")
	}
	if _, ok := byPath["_Private/Meetings/2026-05-12 개인 메모.md"]; !ok {
		t.Fatalf("private meeting not under _Private; paths=%v", byPath)
	}
}
