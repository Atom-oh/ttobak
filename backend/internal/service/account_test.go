package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ttobak/backend/internal/model"
)

// mockAccountRepo implements accountRepo with in-memory maps.
type mockAccountRepo struct {
	accounts map[string]*model.Account       // accountID
	members  map[string]*model.AccountMember // accountID|userID
	users    map[string]*model.User          // email
	meetingRefs map[string][]model.MeetingRef // accountID -> refs
	insightsByAccount map[string][]model.AccountInsight
	documents map[string][]model.AccountDocument // accountID -> docs
}

func newMockAccountRepo() *mockAccountRepo {
	return &mockAccountRepo{
		accounts: make(map[string]*model.Account),
		members:  make(map[string]*model.AccountMember),
		users:    make(map[string]*model.User),
		meetingRefs: make(map[string][]model.MeetingRef),
		insightsByAccount: make(map[string][]model.AccountInsight),
		documents: make(map[string][]model.AccountDocument),
	}
}

func memberKey(accountID, userID string) string { return accountID + "|" + userID }

func (m *mockAccountRepo) CreateAccount(_ context.Context, account *model.Account, ownerMember *model.AccountMember) error {
	cp := *account
	m.accounts[account.AccountID] = &cp
	mc := *ownerMember
	m.members[memberKey(ownerMember.AccountID, ownerMember.UserID)] = &mc
	return nil
}

func (m *mockAccountRepo) GetAccount(_ context.Context, accountID string) (*model.Account, error) {
	a, ok := m.accounts[accountID]
	if !ok {
		return nil, nil
	}
	cp := *a
	return &cp, nil
}

func (m *mockAccountRepo) GetMember(_ context.Context, accountID, userID string) (*model.AccountMember, error) {
	mem, ok := m.members[memberKey(accountID, userID)]
	if !ok {
		return nil, nil
	}
	cp := *mem
	return &cp, nil
}

func (m *mockAccountRepo) PutMember(_ context.Context, member *model.AccountMember) error {
	cp := *member
	m.members[memberKey(member.AccountID, member.UserID)] = &cp
	return nil
}

func (m *mockAccountRepo) ListAccountMembers(_ context.Context, accountID string) ([]model.AccountMember, error) {
	out := []model.AccountMember{}
	for _, mem := range m.members {
		if mem.AccountID == accountID {
			out = append(out, *mem)
		}
	}
	return out, nil
}

func (m *mockAccountRepo) ListAccountsForUser(_ context.Context, userID string) ([]model.AccountMember, error) {
	out := []model.AccountMember{}
	for _, mem := range m.members {
		if mem.UserID == userID {
			out = append(out, *mem)
		}
	}
	return out, nil
}

func (m *mockAccountRepo) GetUserByEmail(_ context.Context, email string) (*model.User, error) {
	u, ok := m.users[email]
	if !ok {
		return nil, nil
	}
	cp := *u
	return &cp, nil
}

func (m *mockAccountRepo) ListMeetingRefsForAccount(_ context.Context, accountID string) ([]model.MeetingRef, error) {
	return append([]model.MeetingRef(nil), m.meetingRefs[accountID]...), nil
}

func (m *mockAccountRepo) ListInsightsForAccount(_ context.Context, accountID string) ([]model.AccountInsight, error) {
	return append([]model.AccountInsight(nil), m.insightsByAccount[accountID]...), nil
}

func (m *mockAccountRepo) PutAccountDocument(_ context.Context, doc *model.AccountDocument) error {
	m.documents[doc.AccountID] = append(m.documents[doc.AccountID], *doc)
	return nil
}
func (m *mockAccountRepo) ListAccountDocuments(_ context.Context, accountID string) ([]model.AccountDocument, error) {
	return append([]model.AccountDocument(nil), m.documents[accountID]...), nil
}
func (m *mockAccountRepo) GetAccountDocument(_ context.Context, accountID, docID string) (*model.AccountDocument, error) {
	for _, d := range m.documents[accountID] {
		if d.DocID == docID {
			cp := d
			return &cp, nil
		}
	}
	return nil, nil
}

func TestHasTtobakOriginMarker(t *testing.T) {
	ttobakDoc := "---\naccount: \"[[하나은행]]\"\nttobak_id: m-123\n---\n\n# 회의록"
	if !hasTtobakOriginMarker(ttobakDoc) {
		t.Error("expected true for doc with ttobak_id frontmatter")
	}
	localDoc := "---\ntitle: Email notes\ntags: [prep]\n---\n\n# Prep"
	if hasTtobakOriginMarker(localDoc) {
		t.Error("expected false for local doc without ttobak_id")
	}
	noFront := "# Just markdown, no frontmatter"
	if hasTtobakOriginMarker(noFront) {
		t.Error("expected false when no frontmatter")
	}
}

func TestCreateAccount_SetsOwnerMember(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)

	acc, err := svc.CreateAccount(context.Background(), "user-1", "u1@example.com",
		&model.CreateAccountRequest{Name: "하나은행", Aliases: []string{"Hana Bank"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if acc.AccountID == "" {
		t.Fatal("expected generated accountId")
	}
	if acc.OwnerUserID != "user-1" {
		t.Errorf("expected owner user-1, got %s", acc.OwnerUserID)
	}
	if acc.EntityType != model.EntityTypeAccount {
		t.Errorf("expected entityType ACCOUNT, got %s", acc.EntityType)
	}
	// owner must exist as a member with role owner
	owner, _ := repo.GetMember(context.Background(), acc.AccountID, "user-1")
	if owner == nil || owner.Role != model.RoleOwner {
		t.Errorf("expected owner member with role owner, got %+v", owner)
	}
	// GSI1 keys for reverse lookup
	if owner.GSI1PK != model.PrefixUser+"user-1" || owner.GSI1SK != model.PrefixAccount+acc.AccountID {
		t.Errorf("owner GSI1 keys wrong: %s / %s", owner.GSI1PK, owner.GSI1SK)
	}
}

func TestCreateAccount_EmptyNameRejected(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	_, err := svc.CreateAccount(context.Background(), "user-1", "u1@example.com",
		&model.CreateAccountRequest{Name: "   "})
	if !errors.Is(err, ErrInvalidInput) {
		t.Errorf("expected ErrInvalidInput, got %v", err)
	}
}

func TestGetAccount_MemberSees(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com",
		&model.CreateAccountRequest{Name: "하나은행"})

	resp, err := svc.GetAccount(context.Background(), "owner-1", acc.AccountID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Name != "하나은행" {
		t.Errorf("expected name 하나은행, got %s", resp.Name)
	}
	if len(resp.Members) != 1 || resp.Members[0].Role != model.RoleOwner {
		t.Errorf("expected 1 owner member, got %+v", resp.Members)
	}
}

func TestGetAccount_NonMemberForbidden(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com",
		&model.CreateAccountRequest{Name: "하나은행"})

	_, err := svc.GetAccount(context.Background(), "stranger-9", acc.AccountID)
	if !errors.Is(err, ErrForbidden) {
		t.Errorf("expected ErrForbidden, got %v", err)
	}
}

func TestGetAccount_MissingNotFound(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	_, err := svc.GetAccount(context.Background(), "user-1", "does-not-exist")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestListAccounts_OnlyMine(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	a1, _ := svc.CreateAccount(context.Background(), "user-1", "u1@x.com", &model.CreateAccountRequest{Name: "하나은행"})
	_, _ = svc.CreateAccount(context.Background(), "user-2", "u2@x.com", &model.CreateAccountRequest{Name: "삼성전자"})

	list, err := svc.ListAccounts(context.Background(), "user-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 account for user-1, got %d", len(list))
	}
	if list[0].AccountID != a1.AccountID || list[0].Role != model.RoleOwner {
		t.Errorf("unexpected summary: %+v", list[0])
	}
}

func seedUser(repo *mockAccountRepo, userID, email string) {
	repo.users[email] = &model.User{UserID: userID, Email: email}
}

func TestAddMember_OwnerAddsTAM(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})
	seedUser(repo, "tam-1", "tam@x.com")

	dto, err := svc.AddMember(context.Background(), "owner-1", acc.AccountID, &model.AddMemberRequest{Email: "tam@x.com", Role: model.RoleTAM})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dto.UserID != "tam-1" || dto.Role != model.RoleTAM {
		t.Errorf("unexpected dto: %+v", dto)
	}
	mem, _ := repo.GetMember(context.Background(), acc.AccountID, "tam-1")
	if mem == nil || mem.GSI1PK != model.PrefixUser+"tam-1" {
		t.Errorf("member not persisted with GSI1 keys: %+v", mem)
	}
}

func TestAddMember_NonOwnerForbidden(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})
	seedUser(repo, "tam-1", "tam@x.com")
	// make tam-1 a non-owner member first
	_, _ = svc.AddMember(context.Background(), "owner-1", acc.AccountID, &model.AddMemberRequest{Email: "tam@x.com", Role: model.RoleTAM})
	seedUser(repo, "ssa-1", "ssa@x.com")

	_, err := svc.AddMember(context.Background(), "tam-1", acc.AccountID, &model.AddMemberRequest{Email: "ssa@x.com", Role: model.RoleSSA})
	if !errors.Is(err, ErrForbidden) {
		t.Errorf("expected ErrForbidden, got %v", err)
	}
}

func TestAddMember_UnknownEmail(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})
	_, err := svc.AddMember(context.Background(), "owner-1", acc.AccountID, &model.AddMemberRequest{Email: "ghost@x.com", Role: model.RoleSSA})
	if !errors.Is(err, ErrUserNotFound) {
		t.Errorf("expected ErrUserNotFound, got %v", err)
	}
}

func TestAddMember_DuplicateRejected(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})
	seedUser(repo, "tam-1", "tam@x.com")
	_, _ = svc.AddMember(context.Background(), "owner-1", acc.AccountID, &model.AddMemberRequest{Email: "tam@x.com", Role: model.RoleTAM})
	_, err := svc.AddMember(context.Background(), "owner-1", acc.AccountID, &model.AddMemberRequest{Email: "tam@x.com", Role: model.RoleSSA})
	if !errors.Is(err, ErrMemberExists) {
		t.Errorf("expected ErrMemberExists, got %v", err)
	}
}

func TestListAccountMeetings_MemberOnly(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})
	repo.meetingRefs[acc.AccountID] = []model.MeetingRef{
		{AccountID: acc.AccountID, MeetingID: "m-1", OwnerUserID: "owner-1", Title: "ROSA"},
	}

	list, err := svc.ListAccountMeetings(context.Background(), "owner-1", acc.AccountID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(list) != 1 || list[0].MeetingID != "m-1" {
		t.Errorf("unexpected list: %+v", list)
	}
}

func TestListAccountMeetings_NonMemberForbidden(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})
	_, err := svc.ListAccountMeetings(context.Background(), "stranger-9", acc.AccountID)
	if !errors.Is(err, ErrForbidden) {
		t.Errorf("expected ErrForbidden, got %v", err)
	}
}

func TestResolveAccountByAlias_Unique(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	acc, _ := svc.CreateAccount(context.Background(), "u1", "u1@x.com",
		&model.CreateAccountRequest{Name: "하나은행", Aliases: []string{"하나은행", "Hana Bank"}})

	got, err := svc.ResolveAccountByAlias(context.Background(), "u1", "Hana Bank")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil || got.AccountID != acc.AccountID {
		t.Errorf("expected to resolve to %s, got %+v", acc.AccountID, got)
	}
}

func TestResolveAccountByAlias_NotFound(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	_, _ = svc.CreateAccount(context.Background(), "u1", "u1@x.com",
		&model.CreateAccountRequest{Name: "하나은행", Aliases: []string{"하나은행"}})
	_, err := svc.ResolveAccountByAlias(context.Background(), "u1", "없는태그")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestResolveAccountByAlias_Ambiguous(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	_, _ = svc.CreateAccount(context.Background(), "u1", "u1@x.com",
		&model.CreateAccountRequest{Name: "A", Aliases: []string{"공통"}})
	_, _ = svc.CreateAccount(context.Background(), "u1", "u1@x.com",
		&model.CreateAccountRequest{Name: "B", Aliases: []string{"공통"}})
	_, err := svc.ResolveAccountByAlias(context.Background(), "u1", "공통")
	if !errors.Is(err, ErrAmbiguousAlias) {
		t.Errorf("expected ErrAmbiguousAlias, got %v", err)
	}
}

func TestAddMember_InvalidRole(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})
	seedUser(repo, "x-1", "x@x.com")
	_, err := svc.AddMember(context.Background(), "owner-1", acc.AccountID, &model.AddMemberRequest{Email: "x@x.com", Role: "owner"})
	if !errors.Is(err, ErrInvalidInput) {
		t.Errorf("expected ErrInvalidInput (owner not assignable), got %v", err)
	}
}

func TestListAccountInsights_FilterByType(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})
	d := time.Date(2026, 5, 12, 9, 0, 0, 0, time.UTC)
	repo.insightsByAccount[acc.AccountID] = []model.AccountInsight{
		{AccountID: acc.AccountID, Type: model.InsightRisk, Text: "지연", OccurredAt: d, SourceID: "m-1", SourceType: "meeting"},
		{AccountID: acc.AccountID, Type: model.InsightTech, Text: "EKS", OccurredAt: d, SourceID: "m-1", SourceType: "meeting"},
	}
	got, err := svc.ListAccountInsights(context.Background(), "owner-1", acc.AccountID, time.Time{}, time.Time{}, []string{model.InsightRisk})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].Type != model.InsightRisk {
		t.Errorf("expected only risk, got %+v", got)
	}
}

func TestListAccountInsights_FilterByPeriod(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})
	repo.insightsByAccount[acc.AccountID] = []model.AccountInsight{
		{AccountID: acc.AccountID, Type: model.InsightRisk, Text: "4월", OccurredAt: time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC)},
		{AccountID: acc.AccountID, Type: model.InsightRisk, Text: "5월", OccurredAt: time.Date(2026, 5, 10, 0, 0, 0, 0, time.UTC)},
	}
	from := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 5, 31, 23, 59, 59, 0, time.UTC)
	got, err := svc.ListAccountInsights(context.Background(), "owner-1", acc.AccountID, from, to, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].Text != "5월" {
		t.Errorf("expected only May insight, got %+v", got)
	}
}

func TestListAccountInsights_NonMemberForbidden(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})
	_, err := svc.ListAccountInsights(context.Background(), "stranger-9", acc.AccountID, time.Time{}, time.Time{}, nil)
	if !errors.Is(err, ErrForbidden) {
		t.Errorf("expected ErrForbidden, got %v", err)
	}
}

func TestGetAccountBrief_Bundles(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})
	d := time.Date(2026, 5, 12, 9, 0, 0, 0, time.UTC)
	repo.insightsByAccount[acc.AccountID] = []model.AccountInsight{
		{AccountID: acc.AccountID, Type: model.InsightRisk, Text: "지연", OccurredAt: d},
		{AccountID: acc.AccountID, Type: model.InsightRisk, Text: "지연2", OccurredAt: d},
		{AccountID: acc.AccountID, Type: model.InsightOpportunity, Text: "확대", OccurredAt: d},
	}
	repo.meetingRefs[acc.AccountID] = []model.MeetingRef{{AccountID: acc.AccountID, MeetingID: "m-1", Title: "ROSA"}}

	brief, err := svc.GetAccountBrief(context.Background(), "owner-1", acc.AccountID, time.Time{}, time.Time{}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if brief.Account == nil || brief.Account.Name != "하나은행" {
		t.Errorf("missing account meta: %+v", brief.Account)
	}
	if len(brief.InsightsByType[model.InsightRisk]) != 2 || len(brief.InsightsByType[model.InsightOpportunity]) != 1 {
		t.Errorf("insights not grouped: %+v", brief.InsightsByType)
	}
	if len(brief.Meetings) != 1 || brief.Meetings[0].MeetingID != "m-1" {
		t.Errorf("missing meetings: %+v", brief.Meetings)
	}
}

func TestGetAccountBrief_NonMemberForbidden(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})
	_, err := svc.GetAccountBrief(context.Background(), "stranger-9", acc.AccountID, time.Time{}, time.Time{}, nil)
	if !errors.Is(err, ErrForbidden) {
		t.Errorf("expected ErrForbidden, got %v", err)
	}
}

func TestPutDocument_MemberStores(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})
	dto, err := svc.PutDocument(context.Background(), "owner-1", acc.AccountID, &model.PutDocumentRequest{Title: "Email notes", DocType: "prep", Markdown: "# Prep\ncontent"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dto.DocID == "" || dto.Title != "Email notes" {
		t.Errorf("unexpected dto: %+v", dto)
	}
	docs, _ := repo.ListAccountDocuments(context.Background(), acc.AccountID)
	if len(docs) != 1 || docs[0].Content != "# Prep\ncontent" || docs[0].TtobakOrigin {
		t.Errorf("doc not stored correctly: %+v", docs)
	}
}

func TestPutDocument_RejectsTtobakOrigin(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})
	_, err := svc.PutDocument(context.Background(), "owner-1", acc.AccountID, &model.PutDocumentRequest{Title: "echo", Markdown: "---\nttobak_id: m-1\n---\n# loop"})
	if !errors.Is(err, ErrLoopGuard) {
		t.Errorf("expected ErrLoopGuard, got %v", err)
	}
}

func TestPutDocument_NonMemberForbidden(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})
	_, err := svc.PutDocument(context.Background(), "stranger-9", acc.AccountID, &model.PutDocumentRequest{Title: "t", Markdown: "x"})
	if !errors.Is(err, ErrForbidden) {
		t.Errorf("expected ErrForbidden, got %v", err)
	}
}

func TestGetAccountDocument_ReturnsContent(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})
	dto, _ := svc.PutDocument(context.Background(), "owner-1", acc.AccountID, &model.PutDocumentRequest{Title: "T", Markdown: "body"})
	detail, err := svc.GetAccountDocument(context.Background(), "owner-1", acc.AccountID, dto.DocID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if detail.Content != "body" {
		t.Errorf("expected content body, got %q", detail.Content)
	}
}
