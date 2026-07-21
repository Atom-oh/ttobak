package service

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

// mockAccountRepo implements accountRepo with in-memory maps.
type mockAccountRepo struct {
	accounts map[string]*model.Account       // accountID
	members  map[string]*model.AccountMember // accountID|userID
	users    map[string]*model.User          // email
	meetingRefs map[string][]model.MeetingRef // accountID -> refs
	insightsByAccount map[string][]model.AccountInsight
	documents map[string][]model.AccountDocument // PK -> docs
	publicShares map[string]*model.PublicShare   // token -> share
}

func newMockAccountRepo() *mockAccountRepo {
	return &mockAccountRepo{
		accounts: make(map[string]*model.Account),
		members:  make(map[string]*model.AccountMember),
		users:    make(map[string]*model.User),
		meetingRefs: make(map[string][]model.MeetingRef),
		insightsByAccount: make(map[string][]model.AccountInsight),
		documents: make(map[string][]model.AccountDocument),
		publicShares: make(map[string]*model.PublicShare),
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
	docs := m.documents[doc.PK]
	for i, d := range docs {
		if d.DocID == doc.DocID {
			docs[i] = *doc
			m.documents[doc.PK] = docs
			return nil
		}
	}
	m.documents[doc.PK] = append(docs, *doc)
	return nil
}
func (m *mockAccountRepo) ListAccountDocuments(_ context.Context, pk string) ([]model.AccountDocument, error) {
	return append([]model.AccountDocument(nil), m.documents[pk]...), nil
}
func (m *mockAccountRepo) GetAccountDocument(_ context.Context, pk, docID string) (*model.AccountDocument, error) {
	for _, d := range m.documents[pk] {
		if d.DocID == docID {
			cp := d
			return &cp, nil
		}
	}
	return nil, nil
}
func (m *mockAccountRepo) DeleteAccountDocument(_ context.Context, pk, docID string) error {
	docs := m.documents[pk]
	for i, d := range docs {
		if d.DocID == docID {
			m.documents[pk] = append(docs[:i], docs[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("%w: doc %s not found", repository.ErrConditionFailed, docID)
}

func (m *mockAccountRepo) PutPublicShare(_ context.Context, share *model.PublicShare) error {
	cp := *share
	m.publicShares[share.Token] = &cp
	return nil
}
func (m *mockAccountRepo) GetPublicShare(_ context.Context, token string) (*model.PublicShare, error) {
	s, ok := m.publicShares[token]
	if !ok {
		return nil, nil
	}
	cp := *s
	return &cp, nil
}
func (m *mockAccountRepo) DeletePublicShare(_ context.Context, token string) error {
	delete(m.publicShares, token)
	return nil
}

// mdPtr always returns a non-nil pointer, unlike strPtr (meeting.go), which
// collapses "" to nil — Markdown needs "explicit empty" distinguishable from "omitted".
func mdPtr(s string) *string { return &s }

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
	// A UTF-8 BOM prefix must not bypass the guard (Kiro adversarial finding #2).
	bomTtobak := "\ufeff---\nttobak_id: m-123\n---\n\n# 회의록"
	if !hasTtobakOriginMarker(bomTtobak) {
		t.Error("expected true for BOM-prefixed ttobak_id doc (loop-guard bypass)")
	}
	// Whitespace before the colon ("ttobak_id :") is valid YAML and must not
	// bypass the guard (panel finding #7).
	spacedKey := "---\nttobak_id : m-123\n---\n\n# 회의록"
	if !hasTtobakOriginMarker(spacedKey) {
		t.Error("expected true for 'ttobak_id :' (space before colon) — loop-guard bypass")
	}
	// A different key that merely starts with ttobak_id must NOT match.
	lookalike := "---\nttobak_id_extra: nope\n---\n\n# x"
	if hasTtobakOriginMarker(lookalike) {
		t.Error("expected false for lookalike key 'ttobak_id_extra'")
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
	dto, err := svc.PutDocument(context.Background(), "owner-1", acc.AccountID, &model.PutDocumentRequest{Title: "Email notes", DocType: "prep", Markdown: mdPtr("# Prep\ncontent")})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dto.DocID == "" || dto.Title != "Email notes" {
		t.Errorf("unexpected dto: %+v", dto)
	}
	docs, _ := repo.ListAccountDocuments(context.Background(), model.PrefixAccount+acc.AccountID)
	if len(docs) != 1 || docs[0].Content != "# Prep\ncontent" || docs[0].TtobakOrigin {
		t.Errorf("doc not stored correctly: %+v", docs)
	}
}

func TestPutDocument_RejectsHybridMarkdownAndFileKey(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})
	_, err := svc.PutDocument(context.Background(), "owner-1", acc.AccountID, &model.PutDocumentRequest{
		Title: "Both", Markdown: mdPtr("body"), FileKey: "docs/owner-1/deck.pdf",
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Errorf("expected ErrInvalidInput for markdown+fileKey both set, got %v", err)
	}
}

func TestPutDocument_SlideWithWhitespaceMarkdownStoresEmptyContent(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})
	created, err := svc.PutDocument(context.Background(), "owner-1", acc.AccountID, &model.PutDocumentRequest{
		Title: "Slide", FileKey: "docs/owner-1/deck.pdf", FileName: "deck.pdf", Markdown: mdPtr("   \n  "),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	doc, _ := repo.GetAccountDocument(context.Background(), model.PrefixAccount+acc.AccountID, created.DocID)
	if doc.Content != "" {
		t.Errorf("expected empty Content for slide with whitespace-only markdown, got %q", doc.Content)
	}
}

func TestGetAccountDocument_UpdatedAtFallsBackToCreatedAtWhenZero(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})
	created, _ := svc.PutDocument(context.Background(), "owner-1", acc.AccountID, &model.PutDocumentRequest{Title: "Note", Markdown: mdPtr("body")})

	// Simulate a pre-existing document written before UpdatedAt existed.
	docs := repo.documents[model.PrefixAccount+acc.AccountID]
	for i := range docs {
		if docs[i].DocID == created.DocID {
			docs[i].UpdatedAt = time.Time{}
		}
	}

	detail, err := svc.GetAccountDocument(context.Background(), "owner-1", acc.AccountID, created.DocID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if detail.UpdatedAt.IsZero() || !detail.UpdatedAt.Equal(detail.CreatedAt) {
		t.Errorf("expected UpdatedAt to fall back to CreatedAt, got updatedAt=%v createdAt=%v", detail.UpdatedAt, detail.CreatedAt)
	}
}

func TestPutDocument_RejectsTtobakOrigin(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})
	_, err := svc.PutDocument(context.Background(), "owner-1", acc.AccountID, &model.PutDocumentRequest{Title: "echo", Markdown: mdPtr("---\nttobak_id: m-1\n---\n# loop")})
	if !errors.Is(err, ErrLoopGuard) {
		t.Errorf("expected ErrLoopGuard, got %v", err)
	}
}

func TestPutDocument_NonMemberForbidden(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})
	_, err := svc.PutDocument(context.Background(), "stranger-9", acc.AccountID, &model.PutDocumentRequest{Title: "t", Markdown: mdPtr("x")})
	if !errors.Is(err, ErrForbidden) {
		t.Errorf("expected ErrForbidden, got %v", err)
	}
}

func TestGetAccountDocument_ReturnsContent(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})
	dto, _ := svc.PutDocument(context.Background(), "owner-1", acc.AccountID, &model.PutDocumentRequest{Title: "T", Markdown: mdPtr("body")})
	detail, err := svc.GetAccountDocument(context.Background(), "owner-1", acc.AccountID, dto.DocID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if detail.Content != "body" {
		t.Errorf("expected content body, got %q", detail.Content)
	}
}

func TestParseWikilinks(t *testing.T) {
	cases := []struct {
		name string
		md   string
		want []string
	}{
		{"none", "plain text, no links", nil},
		{"simple", "see [[하나은행]] for details", []string{"하나은행"}},
		{"alias", "see [[하나은행|은행]] for details", []string{"하나은행"}},
		{"heading", "see [[하나은행#개요]] for details", []string{"하나은행"}},
		{"dedupe", "[[하나은행]] and again [[하나은행]]", []string{"하나은행"}},
		{"multiple", "[[하나은행]] met with [[토스]]", []string{"하나은행", "토스"}},
		{"unclosed", "typing [[하나은행", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := parseWikilinks(c.md)
			if len(got) != len(c.want) {
				t.Fatalf("parseWikilinks(%q) = %v, want %v", c.md, got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Errorf("parseWikilinks(%q)[%d] = %q, want %q", c.md, i, got[i], c.want[i])
				}
			}
		})
	}
}

func TestPutDocument_ParsesWikilinksIntoLinks(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})
	dto, err := svc.PutDocument(context.Background(), "owner-1", acc.AccountID, &model.PutDocumentRequest{Title: "Note", Markdown: mdPtr("meeting with [[토스]]")})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(dto.Links) != 1 || dto.Links[0] != "토스" {
		t.Errorf("expected links [토스], got %v", dto.Links)
	}
}

func TestPutUserDocument_StoresUnderUserPKNoMembershipCheck(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	dto, err := svc.PutUserDocument(context.Background(), "user-1", &model.PutDocumentRequest{Title: "My Note", Markdown: mdPtr("personal note")})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	docs, _ := repo.ListAccountDocuments(context.Background(), model.PrefixUser+"user-1")
	if len(docs) != 1 || docs[0].DocID != dto.DocID {
		t.Errorf("doc not stored under USER# pk: %+v", docs)
	}
	if docs[0].AccountID != "" || docs[0].EntityType != model.EntityTypeUserDoc {
		t.Errorf("expected personal doc metadata, got %+v", docs[0])
	}
}

func TestPutDocument_SlideRejectsForeignFileKey(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})
	_, err := svc.PutDocument(context.Background(), "owner-1", acc.AccountID, &model.PutDocumentRequest{
		Title: "Slide", FileKey: "docs/someone-else/deck.pdf", FileName: "deck.pdf",
	})
	if !errors.Is(err, ErrForbidden) {
		t.Errorf("expected ErrForbidden for foreign fileKey, got %v", err)
	}
}

func TestPutDocument_SlideAllowsEmptyMarkdown(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})
	dto, err := svc.PutDocument(context.Background(), "owner-1", acc.AccountID, &model.PutDocumentRequest{
		Title: "Slide", DocType: "slide", FileKey: "docs/owner-1/deck.pdf", FileName: "deck.pdf", MimeType: "application/pdf",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dto.FileName != "deck.pdf" {
		t.Errorf("expected fileName deck.pdf, got %+v", dto)
	}
}

func TestUpdateAccountDocument_NonUploaderMemberCanEditSlide(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})
	repo.PutMember(context.Background(), &model.AccountMember{AccountID: acc.AccountID, UserID: "member-2", Role: model.RoleSSA})

	created, err := svc.PutDocument(context.Background(), "owner-1", acc.AccountID, &model.PutDocumentRequest{
		Title: "Slide", DocType: "slide", FileKey: "docs/owner-1/deck.pdf", FileName: "deck.pdf",
	})
	if err != nil {
		t.Fatalf("unexpected error creating slide: %v", err)
	}

	// A different member re-saving the SAME (unchanged) fileKey must not be
	// 403'd just because the key isn't under their own docs/{userID}/ --
	// only a fileKey actually changing to something new re-triggers the
	// ownership check.
	updated, err := svc.UpdateAccountDocument(context.Background(), "member-2", acc.AccountID, created.DocID, &model.PutDocumentRequest{
		Title: "Slide (renamed)", DocType: "slide", FileKey: "docs/owner-1/deck.pdf", FileName: "deck.pdf",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if updated.Title != "Slide (renamed)" {
		t.Errorf("expected title updated, got %+v", updated)
	}
}

func TestUpdateAccountDocument_PreservesIdentityAndReparsesLinks(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})
	created, _ := svc.PutDocument(context.Background(), "owner-1", acc.AccountID, &model.PutDocumentRequest{Title: "Note", Markdown: mdPtr("[[토스]]")})
	createdAt := created.CreatedAt

	updated, err := svc.UpdateAccountDocument(context.Background(), "owner-1", acc.AccountID, created.DocID, &model.PutDocumentRequest{Title: "Note v2", Markdown: mdPtr("now mentions [[하나은행]] instead")})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if updated.DocID != created.DocID {
		t.Errorf("DocID changed on update: %s -> %s", created.DocID, updated.DocID)
	}
	if !updated.CreatedAt.Equal(createdAt) {
		t.Errorf("CreatedAt changed on update: %v -> %v", createdAt, updated.CreatedAt)
	}
	if updated.Title != "Note v2" {
		t.Errorf("title not updated: %+v", updated)
	}
	if len(updated.Links) != 1 || updated.Links[0] != "하나은행" {
		t.Errorf("links not reparsed on update: %v", updated.Links)
	}

	doc, _ := repo.GetAccountDocument(context.Background(), model.PrefixAccount+acc.AccountID, created.DocID)
	if doc.SourceUserID != "owner-1" {
		t.Errorf("SourceUserID changed on update: %+v", doc)
	}
}

func TestUpdateAccountDocument_RejectsTtobakOrigin(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})
	created, _ := svc.PutDocument(context.Background(), "owner-1", acc.AccountID, &model.PutDocumentRequest{Title: "Note", Markdown: mdPtr("body")})
	_, err := svc.UpdateAccountDocument(context.Background(), "owner-1", acc.AccountID, created.DocID, &model.PutDocumentRequest{Title: "Note", Markdown: mdPtr("---\nttobak_id: m-1\n---\n# loop")})
	if !errors.Is(err, ErrLoopGuard) {
		t.Errorf("expected ErrLoopGuard, got %v", err)
	}
}

func TestUpdateAccountDocument_RejectsForeignFileKey(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})
	created, _ := svc.PutDocument(context.Background(), "owner-1", acc.AccountID, &model.PutDocumentRequest{Title: "Note", Markdown: mdPtr("body")})

	// Regression: update must apply the same fileKey-ownership check as
	// create -- otherwise a caller can point their own doc's fileKey at
	// another user's S3 object and later fetch a presigned download URL
	// for it via GetAccountDocument.
	_, err := svc.UpdateAccountDocument(context.Background(), "owner-1", acc.AccountID, created.DocID, &model.PutDocumentRequest{
		Title: "Note", FileKey: "docs/someone-else/deck.pdf", FileName: "deck.pdf",
	})
	if !errors.Is(err, ErrForbidden) {
		t.Errorf("expected ErrForbidden for foreign fileKey on update, got %v", err)
	}

	// The doc must still carry no fileKey after the rejected update --
	// GetAccountDocument's presigned download URL must not leak.
	detail, err := svc.GetAccountDocument(context.Background(), "owner-1", acc.AccountID, created.DocID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if detail.FileKey != "" {
		t.Errorf("fileKey should not have been persisted, got %q", detail.FileKey)
	}
}

func TestUpdateAccountDocument_OmittingBothPreservesContent(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})
	created, _ := svc.PutDocument(context.Background(), "owner-1", acc.AccountID, &model.PutDocumentRequest{Title: "Note", Markdown: mdPtr("body")})

	// A PUT with neither markdown nor fileKey is a title/docType/path-only
	// edit -- it must succeed and leave the existing content untouched, not
	// be rejected. This is required (not just lenient): FileKey is never
	// returned to a client (json:"-"), so a title-only update of a *slide*
	// has no fileKey to resend at all.
	updated, err := svc.UpdateAccountDocument(context.Background(), "owner-1", acc.AccountID, created.DocID, &model.PutDocumentRequest{Title: "Note renamed"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if updated.Title != "Note renamed" {
		t.Errorf("expected title updated, got %+v", updated)
	}

	detail, _ := svc.GetAccountDocument(context.Background(), "owner-1", acc.AccountID, created.DocID)
	if detail.Content != "body" {
		t.Errorf("content should be unchanged when both markdown and fileKey are omitted, got %q", detail.Content)
	}
}

func TestUpdateAccountDocument_ExplicitEmptyMarkdownClearsContent(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})
	created, _ := svc.PutDocument(context.Background(), "owner-1", acc.AccountID, &model.PutDocumentRequest{Title: "Note", Markdown: mdPtr("body")})

	// A non-nil pointer to "" (the user select-all-deleted in the editor)
	// must actually clear the content -- distinct from the omitted-field
	// (nil) "preserve" case tested above.
	_, err := svc.UpdateAccountDocument(context.Background(), "owner-1", acc.AccountID, created.DocID, &model.PutDocumentRequest{
		Title: "Note", Markdown: mdPtr(""),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	detail, _ := svc.GetAccountDocument(context.Background(), "owner-1", acc.AccountID, created.DocID)
	if detail.Content != "" {
		t.Errorf("expected content cleared to empty, got %q", detail.Content)
	}
	if len(detail.Links) != 0 {
		t.Errorf("expected links cleared alongside content, got %v", detail.Links)
	}
}

func TestUpdateAccountDocument_SlideExplicitEmptyMarkdownWithoutFileKeyRejected(t *testing.T) {
	repo := newMockAccountRepo()
	mockS3 := &mockS3Deleter{}
	svc := &AccountService{repo: repo, s3: mockS3, bucketName: "test-bucket"}
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})
	created, _ := svc.PutDocument(context.Background(), "owner-1", acc.AccountID, &model.PutDocumentRequest{
		Title: "Slide", FileKey: "docs/owner-1/deck.pdf", FileName: "deck.pdf",
	})

	// Explicit empty markdown with no fileKey would otherwise silently
	// convert the slide into a content-less, file-less document AND
	// irreversibly delete the S3 object -- must be rejected instead.
	_, err := svc.UpdateAccountDocument(context.Background(), "owner-1", acc.AccountID, created.DocID, &model.PutDocumentRequest{
		Title: "Slide", Markdown: mdPtr(""),
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Errorf("expected ErrInvalidInput, got %v", err)
	}
	if len(mockS3.deletedKeys) != 0 {
		t.Errorf("expected no S3 delete on a rejected update, got %v", mockS3.deletedKeys)
	}
	detail, _ := svc.GetAccountDocument(context.Background(), "owner-1", acc.AccountID, created.DocID)
	if detail.FileKey != "docs/owner-1/deck.pdf" {
		t.Errorf("expected slide fileKey untouched after rejected update, got %q", detail.FileKey)
	}
}

func TestUpdateAccountDocument_TitleOnlyEditPreservesDocTypeAndPath(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})
	created, _ := svc.PutDocument(context.Background(), "owner-1", acc.AccountID, &model.PutDocumentRequest{
		Title: "Note", DocType: "note", Path: "Accounts/하나은행/note.md", Markdown: mdPtr("body"),
	})

	// Omitting docType/path on an otherwise title-only update must preserve
	// them, symmetric with how omitting markdown/fileKey preserves the
	// body -- an unconditional overwrite would silently blank a slide's
	// docType on every title-only save.
	updated, err := svc.UpdateAccountDocument(context.Background(), "owner-1", acc.AccountID, created.DocID, &model.PutDocumentRequest{Title: "Note renamed"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if updated.DocType != "note" || updated.Path != "Accounts/하나은행/note.md" {
		t.Errorf("expected docType/path preserved, got %+v", updated)
	}
}

func TestUpdateAccountDocument_SlideTitleOnlyEditWithoutKnowingFileKey(t *testing.T) {
	// Reproduces the real client flow: GET a slide (FileKey never comes back,
	// json:"-"), then PUT back only the fields the client actually has.
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})
	created, err := svc.PutDocument(context.Background(), "owner-1", acc.AccountID, &model.PutDocumentRequest{
		Title: "Slide", DocType: "slide", FileKey: "docs/owner-1/deck.pdf", FileName: "deck.pdf",
	})
	if err != nil {
		t.Fatalf("unexpected error creating slide: %v", err)
	}

	detail, err := svc.GetAccountDocument(context.Background(), "owner-1", acc.AccountID, created.DocID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// FileKey has json:"-" -- a real HTTP client deserializing this
	// response would never see it. Simulate that by building the update
	// request from only the DTO fields a client actually has, never
	// touching detail.FileKey (which IS populated on the Go struct here,
	// since json:"-" only affects marshaling, not this in-process call).
	updated, err := svc.UpdateAccountDocument(context.Background(), "owner-1", acc.AccountID, created.DocID, &model.PutDocumentRequest{
		Title: "Slide renamed", DocType: detail.DocType, Path: detail.Path,
	})
	if err != nil {
		t.Fatalf("unexpected error updating title-only: %v", err)
	}
	if updated.Title != "Slide renamed" || updated.FileName != "deck.pdf" {
		t.Errorf("expected title updated and file fields preserved, got %+v", updated)
	}
}

func TestUpdateAccountDocument_SlideToNoteClearsFileFields(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})
	created, _ := svc.PutDocument(context.Background(), "owner-1", acc.AccountID, &model.PutDocumentRequest{
		Title: "Slide", DocType: "slide", FileKey: "docs/owner-1/deck.pdf", FileName: "deck.pdf",
	})

	// Full-replace PUT: switching to markdown-only must clear the old file
	// fields, not leave a stale fileKey alongside new content.
	updated, err := svc.UpdateAccountDocument(context.Background(), "owner-1", acc.AccountID, created.DocID, &model.PutDocumentRequest{
		Title: "Now a note", DocType: "note", Markdown: mdPtr("converted to markdown"),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if updated.FileName != "" {
		t.Errorf("expected fileName cleared on slide->note conversion, got %q", updated.FileName)
	}

	detail, _ := svc.GetAccountDocument(context.Background(), "owner-1", acc.AccountID, created.DocID)
	if detail.FileKey != "" || detail.Content != "converted to markdown" {
		t.Errorf("expected file fields cleared and content set, got fileKey=%q content=%q", detail.FileKey, detail.Content)
	}
}

func TestDeleteAccountDocument_RemovesDoc(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})
	created, _ := svc.PutDocument(context.Background(), "owner-1", acc.AccountID, &model.PutDocumentRequest{Title: "Note", Markdown: mdPtr("body")})

	if err := svc.DeleteAccountDocument(context.Background(), "owner-1", acc.AccountID, created.DocID); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_, err := svc.GetAccountDocument(context.Background(), "owner-1", acc.AccountID, created.DocID)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound after delete, got %v", err)
	}
}

// mockS3Deleter records DeleteObject/CopyObject calls for assertion.
type mockS3Deleter struct {
	deletedKeys []string
	copied      []s3.CopyObjectInput
	err         error
}

func (m *mockS3Deleter) DeleteObject(_ context.Context, params *s3.DeleteObjectInput, _ ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	if m.err != nil {
		return nil, m.err
	}
	m.deletedKeys = append(m.deletedKeys, *params.Key)
	return &s3.DeleteObjectOutput{}, nil
}

func (m *mockS3Deleter) CopyObject(_ context.Context, params *s3.CopyObjectInput, _ ...func(*s3.Options)) (*s3.CopyObjectOutput, error) {
	if m.err != nil {
		return nil, m.err
	}
	m.copied = append(m.copied, *params)
	return &s3.CopyObjectOutput{}, nil
}

func TestDeleteAccountDocument_RemovesSlideS3Object(t *testing.T) {
	repo := newMockAccountRepo()
	mockS3 := &mockS3Deleter{}
	svc := &AccountService{repo: repo, s3: mockS3, bucketName: "test-bucket"}
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})
	created, _ := svc.PutDocument(context.Background(), "owner-1", acc.AccountID, &model.PutDocumentRequest{
		Title: "Slide", FileKey: "docs/owner-1/deck.pdf", FileName: "deck.pdf",
	})

	if err := svc.DeleteAccountDocument(context.Background(), "owner-1", acc.AccountID, created.DocID); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(mockS3.deletedKeys) != 1 || mockS3.deletedKeys[0] != "docs/owner-1/deck.pdf" {
		t.Errorf("expected S3 object docs/owner-1/deck.pdf deleted, got %v", mockS3.deletedKeys)
	}
}

func TestDeleteAccountDocument_NoteDeleteSkipsS3(t *testing.T) {
	repo := newMockAccountRepo()
	mockS3 := &mockS3Deleter{}
	svc := &AccountService{repo: repo, s3: mockS3, bucketName: "test-bucket"}
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})
	created, _ := svc.PutDocument(context.Background(), "owner-1", acc.AccountID, &model.PutDocumentRequest{Title: "Note", Markdown: mdPtr("body")})

	if err := svc.DeleteAccountDocument(context.Background(), "owner-1", acc.AccountID, created.DocID); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(mockS3.deletedKeys) != 0 {
		t.Errorf("expected no S3 delete for a note (no FileKey), got %v", mockS3.deletedKeys)
	}
}

func TestDeleteAccountDocument_S3FailureDoesNotBlockDelete(t *testing.T) {
	repo := newMockAccountRepo()
	mockS3 := &mockS3Deleter{err: errors.New("s3 transient error")}
	svc := &AccountService{repo: repo, s3: mockS3, bucketName: "test-bucket"}
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})
	created, _ := svc.PutDocument(context.Background(), "owner-1", acc.AccountID, &model.PutDocumentRequest{
		Title: "Slide", FileKey: "docs/owner-1/deck.pdf", FileName: "deck.pdf",
	})

	// S3 cleanup is best-effort: a failure there must not prevent (or
	// error out) the delete the caller already committed to.
	if err := svc.DeleteAccountDocument(context.Background(), "owner-1", acc.AccountID, created.DocID); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_, err := svc.GetAccountDocument(context.Background(), "owner-1", acc.AccountID, created.DocID)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected doc deleted from DynamoDB despite S3 failure, got %v", err)
	}
}

func TestUpdateAccountDocument_ReplacingSlideFileKeyCleansUpOldS3Object(t *testing.T) {
	repo := newMockAccountRepo()
	mockS3 := &mockS3Deleter{}
	svc := &AccountService{repo: repo, s3: mockS3, bucketName: "test-bucket"}
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})
	created, _ := svc.PutDocument(context.Background(), "owner-1", acc.AccountID, &model.PutDocumentRequest{
		Title: "Slide", FileKey: "docs/owner-1/old.pdf", FileName: "old.pdf",
	})

	_, err := svc.UpdateAccountDocument(context.Background(), "owner-1", acc.AccountID, created.DocID, &model.PutDocumentRequest{
		Title: "Slide v2", FileKey: "docs/owner-1/new.pdf", FileName: "new.pdf",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(mockS3.deletedKeys) != 1 || mockS3.deletedKeys[0] != "docs/owner-1/old.pdf" {
		t.Errorf("expected old S3 object docs/owner-1/old.pdf cleaned up, got %v", mockS3.deletedKeys)
	}
}

func TestUpdateAccountDocument_SlideToNoteCleansUpOldS3Object(t *testing.T) {
	repo := newMockAccountRepo()
	mockS3 := &mockS3Deleter{}
	svc := &AccountService{repo: repo, s3: mockS3, bucketName: "test-bucket"}
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})
	created, _ := svc.PutDocument(context.Background(), "owner-1", acc.AccountID, &model.PutDocumentRequest{
		Title: "Slide", FileKey: "docs/owner-1/old.pdf", FileName: "old.pdf",
	})

	_, err := svc.UpdateAccountDocument(context.Background(), "owner-1", acc.AccountID, created.DocID, &model.PutDocumentRequest{
		Title: "Now a note", Markdown: mdPtr("body"),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(mockS3.deletedKeys) != 1 || mockS3.deletedKeys[0] != "docs/owner-1/old.pdf" {
		t.Errorf("expected old S3 object docs/owner-1/old.pdf cleaned up, got %v", mockS3.deletedKeys)
	}
}

func TestUpdateAccountDocument_KeepingSameFileKeyDoesNotDeleteS3Object(t *testing.T) {
	repo := newMockAccountRepo()
	mockS3 := &mockS3Deleter{}
	svc := &AccountService{repo: repo, s3: mockS3, bucketName: "test-bucket"}
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})
	created, _ := svc.PutDocument(context.Background(), "owner-1", acc.AccountID, &model.PutDocumentRequest{
		Title: "Slide", FileKey: "docs/owner-1/deck.pdf", FileName: "deck.pdf",
	})

	_, err := svc.UpdateAccountDocument(context.Background(), "owner-1", acc.AccountID, created.DocID, &model.PutDocumentRequest{
		Title: "Slide v2", FileKey: "docs/owner-1/deck.pdf", FileName: "deck.pdf",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(mockS3.deletedKeys) != 0 {
		t.Errorf("expected no S3 delete when fileKey is unchanged, got %v", mockS3.deletedKeys)
	}
}

func TestUpdateAccountDocument_TitleOnlyEditOfSlideDoesNotDeleteS3Object(t *testing.T) {
	repo := newMockAccountRepo()
	mockS3 := &mockS3Deleter{}
	svc := &AccountService{repo: repo, s3: mockS3, bucketName: "test-bucket"}
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})
	created, _ := svc.PutDocument(context.Background(), "owner-1", acc.AccountID, &model.PutDocumentRequest{
		Title: "Slide", FileKey: "docs/owner-1/deck.pdf", FileName: "deck.pdf",
	})

	_, err := svc.UpdateAccountDocument(context.Background(), "owner-1", acc.AccountID, created.DocID, &model.PutDocumentRequest{
		Title: "Slide renamed",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(mockS3.deletedKeys) != 0 {
		t.Errorf("expected no S3 delete for a title-only edit, got %v", mockS3.deletedKeys)
	}
}

func TestUpdateAccountDocument_NonUploaderReplacingFileKeyDoesNotDeleteOldS3Object(t *testing.T) {
	repo := newMockAccountRepo()
	mockS3 := &mockS3Deleter{}
	svc := &AccountService{repo: repo, s3: mockS3, bucketName: "test-bucket"}
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})
	repo.PutMember(context.Background(), &model.AccountMember{AccountID: acc.AccountID, UserID: "member-2", Role: model.RoleSSA})
	created, _ := svc.PutDocument(context.Background(), "owner-1", acc.AccountID, &model.PutDocumentRequest{
		Title: "Slide", FileKey: "docs/owner-1/old.pdf", FileName: "old.pdf",
	})

	// member-2 replaces the slide with their own upload (a new fileKey must
	// be under the acting user's own docs/{userID}/ prefix -- see
	// validateFileKeyOwnership). The superseded key, docs/owner-1/old.pdf,
	// is scoped to owner-1, not member-2 or the account: owner-1 may
	// reference that same key elsewhere (e.g. a personal document), so a
	// non-uploader member's edit must not delete it. Best-effort cleanup
	// only fires for the doc's own uploader.
	_, err := svc.UpdateAccountDocument(context.Background(), "member-2", acc.AccountID, created.DocID, &model.PutDocumentRequest{
		Title: "Slide v2", FileKey: "docs/member-2/new.pdf", FileName: "new.pdf",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(mockS3.deletedKeys) != 0 {
		t.Errorf("expected no S3 delete when a non-uploader replaces the fileKey, got %v", mockS3.deletedKeys)
	}
}

func TestDeleteAccountDocument_NonUploaderDeleteDoesNotDeleteS3Object(t *testing.T) {
	repo := newMockAccountRepo()
	mockS3 := &mockS3Deleter{}
	svc := &AccountService{repo: repo, s3: mockS3, bucketName: "test-bucket"}
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})
	repo.PutMember(context.Background(), &model.AccountMember{AccountID: acc.AccountID, UserID: "member-2", Role: model.RoleSSA})
	created, _ := svc.PutDocument(context.Background(), "owner-1", acc.AccountID, &model.PutDocumentRequest{
		Title: "Slide", FileKey: "docs/owner-1/deck.pdf", FileName: "deck.pdf",
	})

	if err := svc.DeleteAccountDocument(context.Background(), "member-2", acc.AccountID, created.DocID); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(mockS3.deletedKeys) != 0 {
		t.Errorf("expected no S3 delete when a non-uploader member deletes the doc, got %v", mockS3.deletedKeys)
	}
}

func TestDeleteAccountDocument_OriginalUploaderCannotDeleteAnotherMembersReplacementFile(t *testing.T) {
	repo := newMockAccountRepo()
	mockS3 := &mockS3Deleter{}
	svc := &AccountService{repo: repo, s3: mockS3, bucketName: "test-bucket"}
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})
	repo.PutMember(context.Background(), &model.AccountMember{AccountID: acc.AccountID, UserID: "member-2", Role: model.RoleSSA})
	created, _ := svc.PutDocument(context.Background(), "owner-1", acc.AccountID, &model.PutDocumentRequest{
		Title: "Slide", FileKey: "docs/owner-1/old.pdf", FileName: "old.pdf",
	})
	// member-2 replaces the file -- the doc's SourceUserID stays "owner-1"
	// (fixed at creation) but its current FileKey now belongs to member-2.
	_, err := svc.UpdateAccountDocument(context.Background(), "member-2", acc.AccountID, created.DocID, &model.PutDocumentRequest{
		Title: "Slide v2", FileKey: "docs/member-2/new.pdf", FileName: "new.pdf",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// owner-1 (the original SourceUserID) deletes the doc. A SourceUserID-
	// based check would wrongly authorize this to delete member-2's file --
	// ownership must follow the CURRENT fileKey's own prefix instead.
	if err := svc.DeleteAccountDocument(context.Background(), "owner-1", acc.AccountID, created.DocID); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(mockS3.deletedKeys) != 0 {
		t.Errorf("expected owner-1's delete to NOT remove member-2's docs/member-2/new.pdf, got %v", mockS3.deletedKeys)
	}
}

func TestDeleteAccountDocument_NonexistentDocReturnsNotFound(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})

	// Deleting a docId that was never created must 404, not silently
	// succeed -- matches the documented API-SPEC contract.
	err := svc.DeleteAccountDocument(context.Background(), "owner-1", acc.AccountID, "never-existed")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestDeleteAccountDocument_NonMemberForbidden(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})
	created, _ := svc.PutDocument(context.Background(), "owner-1", acc.AccountID, &model.PutDocumentRequest{Title: "Note", Markdown: mdPtr("body")})

	err := svc.DeleteAccountDocument(context.Background(), "stranger-9", acc.AccountID, created.DocID)
	if !errors.Is(err, ErrForbidden) {
		t.Errorf("expected ErrForbidden, got %v", err)
	}
}

func TestShareUserDocumentToAccount(t *testing.T) {
	repo := newMockAccountRepo()
	s3mock := &mockS3Deleter{}
	svc := &AccountService{repo: repo, s3: s3mock, bucketName: "test-bucket"}

	acc, err := svc.CreateAccount(context.Background(), "user-1", "u1@example.com", &model.CreateAccountRequest{Name: "테스트 어카운트"})
	if err != nil {
		t.Fatalf("setup: create account failed: %v", err)
	}
	other, err := svc.CreateAccount(context.Background(), "owner-2", "o2@example.com", &model.CreateAccountRequest{Name: "다른 어카운트"})
	if err != nil {
		t.Fatalf("setup: create other account failed: %v", err)
	}

	personalDoc := model.AccountDocument{
		PK: model.PrefixUser + "user-1", SK: model.PrefixDoc + "doc-1",
		DocID: "doc-1", Title: "Deck", DocType: "slide",
		FileKey: "docs/user-1/123_deck.pptx", FileName: "deck.pptx",
		MimeType: "application/vnd.openxmlformats-officedocument.presentationml.presentation",
		FileSize: 100, SourceUserID: "user-1", EntityType: model.EntityTypeUserDoc,
	}
	repo.documents[personalDoc.PK] = append(repo.documents[personalDoc.PK], personalDoc)

	// user-1 is not a member of `other` -- sharing there must be forbidden,
	// and must not have touched S3 (checked below via s3mock.copied == 0).
	if _, err := svc.ShareUserDocumentToAccount(context.Background(), "user-1", "doc-1", other.AccountID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("expected ErrForbidden for non-member account, got %v", err)
	}
	if len(s3mock.copied) != 0 {
		t.Fatalf("expected no S3 copy for a rejected share, got %d", len(s3mock.copied))
	}

	dto, err := svc.ShareUserDocumentToAccount(context.Background(), "user-1", "doc-1", acc.AccountID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dto.Title != "Deck" || dto.FileName != "deck.pptx" {
		t.Errorf("unexpected dto: %+v", dto)
	}
	if len(s3mock.copied) != 1 {
		t.Fatalf("expected 1 CopyObject call, got %d", len(s3mock.copied))
	}
	wantSource := "test-bucket/docs/user-1/123_deck.pptx"
	if got := *s3mock.copied[0].CopySource; got != wantSource {
		t.Errorf("expected CopySource %q, got %q", wantSource, got)
	}
	newKey := *s3mock.copied[0].Key
	if newKey == personalDoc.FileKey {
		t.Error("expected a fresh S3 key for the account copy, not the original personal key")
	}

	accDocs := repo.documents[model.PrefixAccount+acc.AccountID]
	if len(accDocs) != 1 {
		t.Fatalf("expected 1 account doc, got %d", len(accDocs))
	}
	if accDocs[0].FileKey != newKey {
		t.Errorf("expected account doc fileKey %q, got %q", newKey, accDocs[0].FileKey)
	}

	// Deleting the original personal doc must not break the shared copy --
	// this is the whole reason ShareUserDocumentToAccount copies rather
	// than reusing the original fileKey.
	if err := svc.DeleteUserDocument(context.Background(), "user-1", "doc-1"); err != nil {
		t.Fatalf("delete original failed: %v", err)
	}
	if _, err := svc.GetAccountDocument(context.Background(), "user-1", acc.AccountID, accDocs[0].DocID); err != nil {
		t.Errorf("shared copy should survive original deletion, got error: %v", err)
	}
}

func TestShareUserDocumentToAccount_ConcurrentSharesDoNotCollideOnKey(t *testing.T) {
	// Regression: the S3 destination key used to be docs/{userID}/{millis}_{name}
	// -- sharing the same doc to two accounts within the same millisecond
	// produced an identical key, so the second CopyObject silently
	// overwrote the first share's object.
	repo := newMockAccountRepo()
	s3mock := &mockS3Deleter{}
	svc := &AccountService{repo: repo, s3: s3mock, bucketName: "test-bucket"}

	accA, err := svc.CreateAccount(context.Background(), "user-1", "u1@example.com", &model.CreateAccountRequest{Name: "A"})
	if err != nil {
		t.Fatalf("setup: create account A failed: %v", err)
	}
	accB, err := svc.CreateAccount(context.Background(), "user-1", "u1@example.com", &model.CreateAccountRequest{Name: "B"})
	if err != nil {
		t.Fatalf("setup: create account B failed: %v", err)
	}

	doc := model.AccountDocument{
		PK: model.PrefixUser + "user-1", SK: model.PrefixDoc + "doc-1",
		DocID: "doc-1", Title: "Deck", DocType: "slide",
		FileKey: "docs/user-1/123_deck.pptx", FileName: "deck.pptx",
		SourceUserID: "user-1", EntityType: model.EntityTypeUserDoc,
	}
	repo.documents[doc.PK] = append(repo.documents[doc.PK], doc)

	if _, err := svc.ShareUserDocumentToAccount(context.Background(), "user-1", "doc-1", accA.AccountID); err != nil {
		t.Fatalf("share to account A failed: %v", err)
	}
	if _, err := svc.ShareUserDocumentToAccount(context.Background(), "user-1", "doc-1", accB.AccountID); err != nil {
		t.Fatalf("share to account B failed: %v", err)
	}

	if len(s3mock.copied) != 2 {
		t.Fatalf("expected 2 CopyObject calls, got %d", len(s3mock.copied))
	}
	keyA, keyB := *s3mock.copied[0].Key, *s3mock.copied[1].Key
	if keyA == keyB {
		t.Fatalf("expected distinct S3 keys for two shares of the same doc, both got %q", keyA)
	}
}

func TestEncodeS3CopySourceKey(t *testing.T) {
	// x-amz-copy-source must be URL-encoded, but "/" path separators
	// between segments must stay literal, not become "%2F".
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain ascii unaffected", "docs/user-1/deck.pdf", "docs/user-1/deck.pdf"},
		{"korean filename encoded", "docs/user-1/발표자료.pdf", "docs/user-1/%EB%B0%9C%ED%91%9C%EC%9E%90%EB%A3%8C.pdf"},
		{"hash and question mark encoded", "docs/user-1/a#b?c.pdf", "docs/user-1/a%23b%3Fc.pdf"},
		{"space encoded", "docs/user-1/my deck.pdf", "docs/user-1/my%20deck.pdf"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := encodeS3CopySourceKey(tc.in); got != tc.want {
				t.Errorf("encodeS3CopySourceKey(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestShareUserDocumentToAccount_KoreanFileNameCopySourceEncoded(t *testing.T) {
	// Regression: an unencoded CopySource containing the Korean file names
	// routine for this product would be malformed per the x-amz-copy-source
	// contract.
	repo := newMockAccountRepo()
	s3mock := &mockS3Deleter{}
	svc := &AccountService{repo: repo, s3: s3mock, bucketName: "test-bucket"}

	acc, err := svc.CreateAccount(context.Background(), "user-1", "u1@example.com", &model.CreateAccountRequest{Name: "A"})
	if err != nil {
		t.Fatalf("setup: create account failed: %v", err)
	}

	doc := model.AccountDocument{
		PK: model.PrefixUser + "user-1", SK: model.PrefixDoc + "doc-1",
		DocID: "doc-1", Title: "발표자료", DocType: "slide",
		FileKey: "docs/user-1/발표자료.pdf", FileName: "발표자료.pdf",
		SourceUserID: "user-1", EntityType: model.EntityTypeUserDoc,
	}
	repo.documents[doc.PK] = append(repo.documents[doc.PK], doc)

	if _, err := svc.ShareUserDocumentToAccount(context.Background(), "user-1", "doc-1", acc.AccountID); err != nil {
		t.Fatalf("share failed: %v", err)
	}

	want := "test-bucket/docs/user-1/%EB%B0%9C%ED%91%9C%EC%9E%90%EB%A3%8C.pdf"
	if got := *s3mock.copied[0].CopySource; got != want {
		t.Errorf("expected encoded CopySource %q, got %q", want, got)
	}
}

func TestUserDocPublicShare_LifecycleAndScope(t *testing.T) {
	repo := newMockAccountRepo()
	svc := &AccountService{repo: repo, s3: &mockS3Deleter{}, bucketName: "test-bucket"}

	slide := model.AccountDocument{
		PK: model.PrefixUser + "user-1", SK: model.PrefixDoc + "slide-1",
		DocID: "slide-1", Title: "Deck", DocType: "slide",
		FileKey: "docs/user-1/123_deck.pdf", FileName: "deck.pdf",
		SourceUserID: "user-1", EntityType: model.EntityTypeUserDoc,
	}
	repo.documents[slide.PK] = append(repo.documents[slide.PK], slide)

	note := model.AccountDocument{
		PK: model.PrefixUser + "user-1", SK: model.PrefixDoc + "note-1",
		DocID: "note-1", Title: "Note", DocType: "note", Content: "body",
		SourceUserID: "user-1", EntityType: model.EntityTypeUserDoc,
	}
	repo.documents[note.PK] = append(repo.documents[note.PK], note)

	// Markdown docs are out of scope -- must be rejected.
	if _, err := svc.CreateUserDocPublicShare(context.Background(), "user-1", "note-1"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput for a markdown doc, got %v", err)
	}

	token, err := svc.CreateUserDocPublicShare(context.Background(), "user-1", "slide-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if token == "" {
		t.Fatal("expected a non-empty token")
	}

	// Idempotent: sharing again returns the same token, not a second one.
	token2, err := svc.CreateUserDocPublicShare(context.Background(), "user-1", "slide-1")
	if err != nil || token2 != token {
		t.Fatalf("expected idempotent re-share to return %q, got %q err=%v", token, token2, err)
	}

	// Regression: GetUserDocument's AccountDocumentDetail must surface the
	// token (DocDetailClient.tsx's setPublicToken(detail.publicShareToken)
	// on load depends on this) -- toDocumentDTO doesn't map it since it
	// lives on AccountDocumentDetail, not the embedded DTO.
	detail, err := svc.GetUserDocument(context.Background(), "user-1", "slide-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if detail.PublicShareToken != token {
		t.Errorf("expected GetUserDocument to surface PublicShareToken %q, got %q", token, detail.PublicShareToken)
	}

	resolved, err := svc.ResolvePublicShare(context.Background(), token)
	if err != nil {
		t.Fatalf("unexpected error resolving token: %v", err)
	}
	if resolved.DocID != "slide-1" {
		t.Errorf("expected resolved doc slide-1, got %s", resolved.DocID)
	}

	if _, err := svc.ResolvePublicShare(context.Background(), "bogus-token"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound for a bogus token, got %v", err)
	}

	if err := svc.RevokeUserDocPublicShare(context.Background(), "user-1", "slide-1"); err != nil {
		t.Fatalf("unexpected error revoking: %v", err)
	}
	if _, err := svc.ResolvePublicShare(context.Background(), token); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound after revoke, got %v", err)
	}

	// Revoking a never-shared doc is a no-op, not an error.
	if err := svc.RevokeUserDocPublicShare(context.Background(), "user-1", "note-1"); err != nil {
		t.Errorf("expected revoke on unshared doc to be a no-op, got %v", err)
	}
}
