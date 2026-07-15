package service

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

type mockVaultRepo struct {
	owned       []model.Meeting
	full        map[string]*model.Meeting
	accounts    map[string]*model.Account
	accountErr  error
	memberships []model.AccountMember
	documents   map[string][]model.AccountDocument // PK -> docs
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
	if m.accountErr != nil {
		return nil, m.accountErr
	}
	return m.accounts[accountID], nil
}
func (m *mockVaultRepo) ListAccountsForUser(_ context.Context, _ string) ([]model.AccountMember, error) {
	return m.memberships, nil
}
func (m *mockVaultRepo) ListAccountDocuments(_ context.Context, pk string) ([]model.AccountDocument, error) {
	return m.documents[pk], nil
}
func (m *mockVaultRepo) GetAccountDocument(_ context.Context, pk, docID string) (*model.AccountDocument, error) {
	for _, d := range m.documents[pk] {
		if d.DocID == docID {
			cp := d
			return &cp, nil
		}
	}
	return nil, nil
}

func TestSanitizeFilename_PathTraversal(t *testing.T) {
	cases := map[string]string{
		"..":            "_",   // bare traversal segment neutralized
		"../etc":        "_-etc", // "/" → "-", ".." → "_"
		"a/b":           "a-b", // path separator stripped
		"하나은행":      "하나은행", // normal name preserved
		"v1..2":         "v1_2", // embedded ".." neutralized (acceptable)
	}
	for in, want := range cases {
		if got := sanitizeFilename(in); got != want {
			t.Errorf("sanitizeFilename(%q) = %q, want %q", in, got, want)
		}
	}
	// No output may contain a ".." traversal segment.
	for _, in := range []string{"..", "../../x", "a/../../b", "....//.."} {
		if strings.Contains(sanitizeFilename(in), "..") {
			t.Errorf("sanitizeFilename(%q) still contains '..': %q", in, sanitizeFilename(in))
		}
	}
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

func TestExportVault_IncludesDocuments(t *testing.T) {
	personalNote := model.AccountDocument{
		PK: model.PrefixUser + "u1", DocID: "doc-1", Title: "개인 노트",
		Content: "본문", EntityType: model.EntityTypeUserDoc,
	}
	accountNote := model.AccountDocument{
		PK: model.PrefixAccount + "acc-1", DocID: "doc-2", Title: "회의 준비",
		DocType: "note", Content: "준비 내용", Links: []string{"하나은행"},
		EntityType: model.EntityTypeAccountDoc,
	}
	slide := model.AccountDocument{
		PK: model.PrefixAccount + "acc-1", DocID: "doc-3", Title: "발표자료",
		DocType: "slide", FileName: "deck.pdf", EntityType: model.EntityTypeAccountDoc,
	}
	repo := &mockVaultRepo{
		accounts:    map[string]*model.Account{"acc-1": {AccountID: "acc-1", Name: "하나은행"}},
		memberships: []model.AccountMember{{AccountID: "acc-1", UserID: "u1"}},
		documents: map[string][]model.AccountDocument{
			model.PrefixUser + "u1":    {personalNote},
			model.PrefixAccount + "acc-1": {accountNote, slide},
		},
	}
	svc := newVaultServiceWithRepo(repo)

	files, err := svc.ExportVault(context.Background(), "u1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	byPath := map[string]string{}
	for _, f := range files {
		byPath[f.Path] = f.Markdown
	}

	personalMd, ok := byPath["_Private/Docs/개인 노트.md"]
	if !ok {
		t.Fatalf("personal doc not under _Private/Docs; paths=%v", byPath)
	}
	if !strings.Contains(personalMd, "ttobak_id: doc-1") || !strings.Contains(personalMd, "본문") {
		t.Errorf("personal doc missing frontmatter/content:\n%s", personalMd)
	}

	accountMd, ok := byPath["Accounts/하나은행/Docs/회의 준비.md"]
	if !ok {
		t.Fatalf("account doc not under Accounts/.../Docs; paths=%v", byPath)
	}
	if !strings.Contains(accountMd, "doc_type: note") || !strings.Contains(accountMd, `links: ["[[하나은행]]"]`) {
		t.Errorf("account doc missing frontmatter:\n%s", accountMd)
	}

	for path := range byPath {
		if strings.Contains(path, "발표자료") {
			t.Errorf("slide doc should not be exported as markdown, got path %q", path)
		}
	}
}

func TestExportVault_SkipsAccountScopeWhenAccountDeleted(t *testing.T) {
	// A membership row can outlive the account it points at (GetAccount
	// returns nil, no error). exportDocuments must skip that account's
	// documents rather than falling through with accountName="" -- which
	// docVaultPath reads as "personal" and would mis-file a shared
	// account's documents under _Private/Docs/. A transient GetAccount
	// *error* is a different case -- see
	// TestExportVault_FailsLoudlyOnTransientGetAccountError below.
	sharedNote := model.AccountDocument{
		PK: model.PrefixAccount + "acc-deleted", DocID: "doc-9", Title: "고아 문서",
		Content: "본문", EntityType: model.EntityTypeAccountDoc,
	}
	repo := &mockVaultRepo{
		accounts:    map[string]*model.Account{}, // acc-deleted intentionally absent -> GetAccount returns nil
		memberships: []model.AccountMember{{AccountID: "acc-deleted", UserID: "u1"}},
		documents: map[string][]model.AccountDocument{
			model.PrefixAccount + "acc-deleted": {sharedNote},
		},
	}
	svc := newVaultServiceWithRepo(repo)

	files, err := svc.ExportVault(context.Background(), "u1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, f := range files {
		if strings.Contains(f.Path, "_Private") {
			t.Errorf("account doc must not be exported under _Private when its account is gone, got %q", f.Path)
		}
	}
}

func TestExportVault_FailsLoudlyOnTransientGetAccountError(t *testing.T) {
	// A transient GetAccount error must fail the export, not be treated the
	// same as "account was deleted" (nil, no error) -- silently continuing
	// would produce an export that looks complete but is missing that
	// account's documents entirely (AGENTS.md: no silent failures).
	repo := &mockVaultRepo{
		accounts:    map[string]*model.Account{},
		accountErr:  fmt.Errorf("dynamodb: transient read error"),
		memberships: []model.AccountMember{{AccountID: "acc-1", UserID: "u1"}},
	}
	svc := newVaultServiceWithRepo(repo)

	_, err := svc.ExportVault(context.Background(), "u1")
	if err == nil {
		t.Fatal("expected export to fail on a transient GetAccount error, got nil")
	}
}

func TestExportVault_CapsLargeCorpus(t *testing.T) {
	orig := maxVaultMeetings
	maxVaultMeetings = 2
	defer func() { maxVaultMeetings = orig }()

	d := time.Date(2026, 5, 12, 9, 0, 0, 0, time.UTC)
	owned := []model.Meeting{}
	full := map[string]*model.Meeting{}
	for i := 0; i < 5; i++ {
		id := fmt.Sprintf("m-%d", i)
		m := model.Meeting{MeetingID: id, UserID: "u1", Title: "T" + id, Date: d, Status: model.StatusDone}
		owned = append(owned, m)
		mm := m
		full[id] = &mm
	}
	repo := &mockVaultRepo{owned: owned, full: full, accounts: map[string]*model.Account{}}
	svc := newVaultServiceWithRepo(repo)

	files, err := svc.ExportVault(context.Background(), "u1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// 2 capped meeting files + 1 truncation note
	if len(files) != 3 {
		t.Fatalf("expected 2 capped + 1 truncation note = 3 files, got %d", len(files))
	}
	if files[len(files)-1].Path != "_export-truncated.md" {
		t.Errorf("expected truncation note last, got %q", files[len(files)-1].Path)
	}
}
