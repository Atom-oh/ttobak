package service

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	cognitoidp "github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider"
	cognitoidptypes "github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

// mockAccountRepo implements accountRepo with in-memory maps.
type mockAccountRepo struct {
	accounts          map[string]*model.Account       // accountID
	members           map[string]*model.AccountMember // accountID|userID
	users             map[string]*model.User          // email
	meetingRefs       map[string][]model.MeetingRef   // accountID -> refs
	insightsByAccount map[string][]model.AccountInsight
	documents         map[string][]model.AccountDocument // PK -> docs
	shares            map[string]*model.Share            // "sharedToID|meetingID" -> share
	meetings          map[string]*model.Meeting          // meetingID -> meeting
	publicShares      map[string]*model.PublicShare      // token -> share
	docShares         map[string]*model.Share            // "sharedToID|docID" -> per-user doc share
	pendingShares     []*model.PendingShare              // PutPendingShare calls, in order

	// shareOpErr, when non-nil, is returned by GetShare/DeleteShare for the
	// specific meetingID it's keyed to test cleanup-failure handling without
	// affecting every meeting in a multi-meeting test.
	shareOpErr map[string]error // meetingID -> forced error

	// deleteMemberAfterGet simulates a concurrent RemoveMember landing in the
	// gap right after a GetMember read returns: when non-empty and matching
	// the requested memberKey, the member is deleted from the map AFTER
	// building the returned copy (so the caller's GetMember still sees the
	// member, but any subsequent write in the same service call hits a
	// since-deleted row). Named distinctly from deleteAfterGet (below, keyed
	// by docID) since the two simulate races on different entities.
	deleteMemberAfterGet string

	// listMeetingRefsErr, when non-nil, is returned by ListMeetingRefsForAccount
	// to test that a total-list failure surfaces as an error (not a silent
	// success) and leaves membership untouched.
	listMeetingRefsErr error

	// replaceWithDirectShareAfterGet simulates an owner creating a new direct
	// share for this exact "sharedToID|meetingID" key in the gap between
	// RemoveMember's cleanup GetShare read and its delete: when set to this
	// key, GetShare's returned copy still reflects the account-origin share
	// (the read that happened first), but the underlying map is overwritten
	// with a direct share immediately after, so a subsequent
	// DeleteShareIfAccountOrigin call sees the new direct share, not the one
	// GetShare read.
	replaceWithDirectShareAfterGet string

	// replaceWithOtherAccountShareAfterGet simulates the cross-account
	// re-share race the review flagged: the meeting is re-shared from THIS
	// account to a different account (fresh CreateShareIfMember grant) in
	// the gap between cleanup's GetMeetingByID+GetShare reads and its
	// delete. GetShare's returned copy still reflects the row as it was at
	// read time (this account's origin=="account" share), but the
	// underlying map is overwritten with the other account's fresh grant
	// immediately after, so DeleteShareIfAccountOrigin's own accountId
	// condition -- not the earlier, non-atomic meeting.AccountID read -- is
	// what has to refuse to delete it.
	replaceWithOtherAccountShareAfterGet string

	// raceWinnerToken, if set, simulates a concurrent request winning the
	// SetPublicShareTokenIfAbsent race: the first call writes this token to
	// the doc instead of the caller's and returns ErrConditionFailed.
	raceWinnerToken string
	// deleteAfterGet, if set, simulates a concurrent delete landing right
	// after the next GetAccountDocument read for this docID: the entry is
	// removed immediately after the read returns its copy.
	deleteAfterGet string
	// raceTokenAfterGet, if set, simulates a concurrent write landing right
	// after a GetAccountDocument read: the very next read mutates the
	// stored doc's PublicShareToken to this value and clears itself.
	raceTokenAfterGet string
	// raceClearFileKeyAfterGet, if set, simulates a concurrent
	// file->markdown conversion landing right after a GetAccountDocument
	// read for this docID: the very next read for that docID clears the
	// stored doc's FileKey and clears this hook.
	raceClearFileKeyAfterGet string
}

func newMockAccountRepo() *mockAccountRepo {
	return &mockAccountRepo{
		accounts:          make(map[string]*model.Account),
		members:           make(map[string]*model.AccountMember),
		users:             make(map[string]*model.User),
		meetingRefs:       make(map[string][]model.MeetingRef),
		insightsByAccount: make(map[string][]model.AccountInsight),
		documents:         make(map[string][]model.AccountDocument),
		shares:            make(map[string]*model.Share),
		meetings:          make(map[string]*model.Meeting),
		shareOpErr:        make(map[string]error),
		publicShares:      make(map[string]*model.PublicShare),
		docShares:         make(map[string]*model.Share),
	}
}

// --- per-user document shares (SHAREDDOC#) ---

func docShareKey(sharedToID, docID string) string { return sharedToID + "|" + docID }

func (m *mockAccountRepo) CreateDocShare(_ context.Context, docID, ownerID, ownerEmail, sharedToID, email string) (*model.Share, error) {
	sh := &model.Share{
		MeetingID: docID, OwnerID: ownerID, OwnerEmail: ownerEmail,
		SharedToID: sharedToID, Email: email,
		Permission: model.PermissionRead, EntityType: model.EntityTypeDocShare,
	}
	m.docShares[docShareKey(sharedToID, docID)] = sh
	return sh, nil
}

func (m *mockAccountRepo) GetDocShare(_ context.Context, sharedToID, docID string) (*model.Share, error) {
	sh, ok := m.docShares[docShareKey(sharedToID, docID)]
	if !ok {
		return nil, nil
	}
	cp := *sh
	return &cp, nil
}

func (m *mockAccountRepo) DeleteDocShare(_ context.Context, sharedToID, docID string) error {
	delete(m.docShares, docShareKey(sharedToID, docID))
	return nil
}

func (m *mockAccountRepo) PutPendingShare(_ context.Context, share *model.PendingShare) error {
	cp := *share
	m.pendingShares = append(m.pendingShares, &cp)
	return nil
}

func (m *mockAccountRepo) GetPendingShare(_ context.Context, email, sk string) (*model.PendingShare, error) {
	for _, p := range m.pendingShares {
		if p.Email == email && p.SK == sk {
			cp := *p
			return &cp, nil
		}
	}
	return nil, nil
}

func (m *mockAccountRepo) DeletePendingShare(_ context.Context, email, sk string) (bool, error) {
	deleted := false
	kept := m.pendingShares[:0]
	for _, p := range m.pendingShares {
		if p.Email == email && p.SK == sk {
			deleted = true
			continue
		}
		kept = append(kept, p)
	}
	m.pendingShares = kept
	return deleted, nil
}

func (m *mockAccountRepo) ListDocSharesForUser(_ context.Context, userID string) ([]model.Share, error) {
	var out []model.Share
	for _, sh := range m.docShares {
		if sh.SharedToID == userID {
			out = append(out, *sh)
		}
	}
	return out, nil
}

func (m *mockAccountRepo) ListDocSharesForDoc(_ context.Context, docID string) ([]model.Share, error) {
	var out []model.Share
	for _, sh := range m.docShares {
		if sh.MeetingID == docID {
			out = append(out, *sh)
		}
	}
	return out, nil
}

func (m *mockAccountRepo) GetMeetingByID(_ context.Context, meetingID string) (*model.Meeting, error) {
	mtg, ok := m.meetings[meetingID]
	if !ok {
		return nil, nil
	}
	cp := *mtg
	return &cp, nil
}

func acctShareKey(sharedToID, meetingID string) string { return sharedToID + "|" + meetingID }

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
	key := memberKey(accountID, userID)
	mem, ok := m.members[key]
	if !ok {
		return nil, nil
	}
	cp := *mem
	if m.deleteMemberAfterGet != "" && m.deleteMemberAfterGet == key {
		delete(m.members, key)
	}
	return &cp, nil
}

func (m *mockAccountRepo) PutMember(_ context.Context, member *model.AccountMember) error {
	cp := *member
	m.members[memberKey(member.AccountID, member.UserID)] = &cp
	return nil
}

func (m *mockAccountRepo) DeleteMember(_ context.Context, accountID, userID string) error {
	key := memberKey(accountID, userID)
	if _, ok := m.members[key]; !ok {
		return fmt.Errorf("%w: member %s not found", repository.ErrConditionFailed, userID)
	}
	delete(m.members, key)
	return nil
}

func (m *mockAccountRepo) UpdateMemberRole(_ context.Context, accountID, userID, role string) error {
	key := memberKey(accountID, userID)
	member, ok := m.members[key]
	if !ok {
		return fmt.Errorf("%w: member %s not found", repository.ErrConditionFailed, userID)
	}
	member.Role = role
	return nil
}

func (m *mockAccountRepo) GetShare(_ context.Context, sharedToID, meetingID string) (*model.Share, error) {
	if err, ok := m.shareOpErr[meetingID]; ok {
		return nil, err
	}
	key := acctShareKey(sharedToID, meetingID)
	sh, ok := m.shares[key]
	if !ok {
		return nil, nil
	}
	cp := *sh
	if m.replaceWithDirectShareAfterGet != "" && m.replaceWithDirectShareAfterGet == key {
		m.shares[key] = &model.Share{
			MeetingID: meetingID, SharedToID: sharedToID, Permission: model.PermissionEdit, Origin: "",
		}
	}
	if m.replaceWithOtherAccountShareAfterGet != "" && m.replaceWithOtherAccountShareAfterGet == key {
		m.shares[key] = &model.Share{
			MeetingID: meetingID, SharedToID: sharedToID, Permission: model.PermissionRead, Origin: model.ShareOriginAccount, AccountID: "other-acc",
		}
	}
	return &cp, nil
}

func (m *mockAccountRepo) DeleteShareIfAccountOrigin(_ context.Context, accountID, sharedToID, meetingID string) error {
	if err, ok := m.shareOpErr[meetingID]; ok {
		return err
	}
	key := acctShareKey(sharedToID, meetingID)
	existing, ok := m.shares[key]
	if !ok || existing.Origin != model.ShareOriginAccount || existing.AccountID != accountID {
		return fmt.Errorf("%w: share %s not account-origin for account %s", repository.ErrConditionFailed, key, accountID)
	}
	delete(m.shares, key)
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
	if m.listMeetingRefsErr != nil {
		return nil, m.listMeetingRefsErr
	}
	return append([]model.MeetingRef(nil), m.meetingRefs[accountID]...), nil
}

func (m *mockAccountRepo) ListInsightsForAccount(_ context.Context, accountID string) ([]model.AccountInsight, error) {
	return append([]model.AccountInsight(nil), m.insightsByAccount[accountID]...), nil
}

// UpdateAccountDocumentFields applies exactly the given fields, mirroring
// the real repo's field-scoped UpdateItem -- notably, it must NOT touch
// PublicShareToken (fields never includes it; this mock doesn't special-case
// it either, so a bug that added it to the caller's fields map would show
// up here too).
func (m *mockAccountRepo) UpdateAccountDocumentFields(_ context.Context, pk, docID string, fields map[string]interface{}, removeFields []string) (map[string]string, error) {
	docs := m.documents[pk]
	for i, d := range docs {
		if d.DocID == docID {
			for k, v := range fields {
				switch k {
				case "title":
					d.Title = v.(string)
				case "docType":
					d.DocType = v.(string)
				case "path":
					d.Path = v.(string)
				case "content":
					d.Content = v.(string)
				case "links":
					d.Links = v.([]string)
				case "fileKey":
					d.FileKey = v.(string)
				case "fileName":
					d.FileName = v.(string)
				case "mimeType":
					d.MimeType = v.(string)
				case "fileSize":
					d.FileSize = v.(int64)
				case "updatedAt":
					d.UpdatedAt = v.(time.Time)
				default:
					return nil, fmt.Errorf("unexpected field %q in UpdateAccountDocumentFields", k)
				}
			}
			oldValues := make(map[string]string, len(removeFields))
			for _, k := range removeFields {
				switch k {
				case "publicShareToken":
					if d.PublicShareToken != "" {
						oldValues[k] = d.PublicShareToken
					}
					d.PublicShareToken = ""
				default:
					return nil, fmt.Errorf("unexpected removeField %q in UpdateAccountDocumentFields", k)
				}
			}
			docs[i] = d
			m.documents[pk] = docs
			return oldValues, nil
		}
	}
	return nil, fmt.Errorf("%w: doc %s not found", repository.ErrConditionFailed, docID)
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
	for i, d := range m.documents[pk] {
		if d.DocID == docID {
			cp := d
			if m.deleteAfterGet == docID {
				m.documents[pk] = append(m.documents[pk][:i], m.documents[pk][i+1:]...)
				m.deleteAfterGet = ""
				return &cp, nil
			}
			if m.raceTokenAfterGet != "" {
				// Simulate a concurrent write landing between this read
				// and whatever the caller does next: the stored doc gets
				// a different token than the one just handed back.
				m.documents[pk][i].PublicShareToken = m.raceTokenAfterGet
				m.raceTokenAfterGet = ""
			}
			if m.raceClearFileKeyAfterGet == docID {
				m.documents[pk][i].FileKey = ""
				m.raceClearFileKeyAfterGet = ""
			}
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

// SetPublicShareTokenIfAbsent mirrors the real repo's conditional UpdateItem:
// only wins if the doc's publicShareToken is currently empty.
func (m *mockAccountRepo) SetPublicShareTokenIfAbsent(_ context.Context, pk, docID, token string) error {
	docs := m.documents[pk]
	for i, d := range docs {
		if d.DocID == docID {
			if m.raceWinnerToken != "" {
				docs[i].PublicShareToken = m.raceWinnerToken
				m.documents[pk] = docs
				m.raceWinnerToken = ""
				return fmt.Errorf("%w: doc %s already has a public share token", repository.ErrConditionFailed, docID)
			}
			// Mirrors the real repo's condition: mint only succeeds
			// against the doc's LIVE fileKey, not a caller's read-time
			// snapshot -- closes the race where a concurrent
			// file->markdown conversion clears fileKey between
			// CreateUserDocPublicShare's own upfront check and this write.
			if d.FileKey == "" {
				return fmt.Errorf("%w: doc %s has no fileKey", repository.ErrConditionFailed, docID)
			}
			if d.PublicShareToken != "" {
				return fmt.Errorf("%w: doc %s already has a public share token", repository.ErrConditionFailed, docID)
			}
			docs[i].PublicShareToken = token
			m.documents[pk] = docs
			return nil
		}
	}
	return fmt.Errorf("%w: doc %s not found", repository.ErrConditionFailed, docID)
}

func (m *mockAccountRepo) ClearPublicShareTokenIfMatches(_ context.Context, pk, docID, expectedToken string) error {
	docs := m.documents[pk]
	for i, d := range docs {
		if d.DocID == docID {
			if d.PublicShareToken != expectedToken {
				return fmt.Errorf("%w: doc %s's public share token no longer matches", repository.ErrConditionFailed, docID)
			}
			docs[i].PublicShareToken = ""
			m.documents[pk] = docs
			return nil
		}
	}
	return fmt.Errorf("%w: doc %s not found", repository.ErrConditionFailed, docID)
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

// TestAddMember_MemberCanAddMember pins ADR-033: adding a member is no
// longer owner-only -- any existing (non-owner) member may add another.
func TestAddMember_MemberCanAddMember(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})
	seedUser(repo, "tam-1", "tam@x.com")
	// make tam-1 a non-owner member first
	_, _ = svc.AddMember(context.Background(), "owner-1", acc.AccountID, &model.AddMemberRequest{Email: "tam@x.com", Role: model.RoleTAM})
	seedUser(repo, "ssa-1", "ssa@x.com")

	dto, err := svc.AddMember(context.Background(), "tam-1", acc.AccountID, &model.AddMemberRequest{Email: "ssa@x.com", Role: model.RoleSSA})
	if err != nil {
		t.Fatalf("expected non-owner member to be able to add another member, got %v", err)
	}
	if dto.UserID != "ssa-1" || dto.Role != model.RoleSSA {
		t.Errorf("unexpected dto: %+v", dto)
	}
}

// TestAddMember_NonMemberForbidden: someone with no membership at all on
// this account still cannot add members.
func TestAddMember_NonMemberForbidden(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})
	seedUser(repo, "ssa-1", "ssa@x.com")

	_, err := svc.AddMember(context.Background(), "stranger-1", acc.AccountID, &model.AddMemberRequest{Email: "ssa@x.com", Role: model.RoleSSA})
	if !errors.Is(err, ErrForbidden) {
		t.Errorf("expected ErrForbidden, got %v", err)
	}
}

// TestRemoveMember_NonOwnerMemberForbidden pins that removal, unlike add/
// update-role, stays owner-only (ADR-033).
func TestRemoveMember_NonOwnerMemberForbidden(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})
	seedUser(repo, "tam-1", "tam@x.com")
	seedUser(repo, "ssa-1", "ssa@x.com")
	_, _ = svc.AddMember(context.Background(), "owner-1", acc.AccountID, &model.AddMemberRequest{Email: "tam@x.com", Role: model.RoleTAM})
	_, _ = svc.AddMember(context.Background(), "owner-1", acc.AccountID, &model.AddMemberRequest{Email: "ssa@x.com", Role: model.RoleSSA})

	if _, err := svc.RemoveMember(context.Background(), "tam-1", acc.AccountID, "ssa-1", false); !errors.Is(err, ErrForbidden) {
		t.Errorf("expected ErrForbidden for non-owner member removal, got %v", err)
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

func TestRevokePendingMember_OwnerCanRevoke(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})
	repo.pendingShares = append(repo.pendingShares, &model.PendingShare{
		Email: "invited@x.com", Kind: model.PendingShareKindAccount,
		AccountID: acc.AccountID, Role: model.RoleSSA,
		SK: model.PrefixPendingAccount + acc.AccountID,
	})

	if err := svc.RevokePendingMember(context.Background(), "owner-1", acc.AccountID, "invited@x.com"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(repo.pendingShares) != 0 {
		t.Errorf("expected the pending share to be revoked, got %d remaining", len(repo.pendingShares))
	}
}

func TestRevokePendingMember_NonOwnerForbidden(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})
	seedUser(repo, "tam-1", "tam@x.com")
	svc.AddMember(context.Background(), "owner-1", acc.AccountID, &model.AddMemberRequest{Email: "tam@x.com", Role: model.RoleTAM})
	repo.pendingShares = append(repo.pendingShares, &model.PendingShare{
		Email: "invited@x.com", Kind: model.PendingShareKindAccount,
		AccountID: acc.AccountID, Role: model.RoleSSA,
		SK: model.PrefixPendingAccount + acc.AccountID,
	})

	err := svc.RevokePendingMember(context.Background(), "tam-1", acc.AccountID, "invited@x.com")
	if !errors.Is(err, ErrForbidden) {
		t.Errorf("expected ErrForbidden, got %v", err)
	}
	if len(repo.pendingShares) != 1 {
		t.Errorf("expected the pending share to survive a non-owner's revoke attempt, got %d remaining", len(repo.pendingShares))
	}
}

func TestRevokePendingMember_AccountNotFound(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)

	err := svc.RevokePendingMember(context.Background(), "owner-1", "nonexistent-account", "invited@x.com")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestRevokePendingMember_NoOpWhenNothingQueued(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})

	if err := svc.RevokePendingMember(context.Background(), "owner-1", acc.AccountID, "nobody-invited@x.com"); err != nil {
		t.Errorf("expected a no-op revoke to succeed silently, got %v", err)
	}
}

// TestRevokePendingMember_AlreadyClaimedByMaterialize covers the race this
// error exists for: the invitee logged in and MaterializePendingShares
// turned the queued row into a real AccountMember (clearing the pending row
// in the same transaction) before the owner's revoke landed. The delete
// finds nothing, but email now has live access -- this must be reported as
// ErrPendingAlreadyClaimed, not a silent success.
func TestRevokePendingMember_AlreadyClaimedByMaterialize(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	svc.SetCognitoAdminAPI(&fakeCognitoAdminAPI{
		adminGetUserFn: func(_ context.Context, _ *cognitoidp.AdminGetUserInput) (*cognitoidp.AdminGetUserOutput, error) {
			return &cognitoidp.AdminGetUserOutput{
				Username:       aws.String("claimed-1"),
				UserAttributes: []cognitoidptypes.AttributeType{{Name: aws.String("sub"), Value: aws.String("claimed-1")}},
			}, nil
		},
	}, "pool-1")
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})
	seedUser(repo, "claimed-1", "invited@x.com")
	svc.AddMember(context.Background(), "owner-1", acc.AccountID, &model.AddMemberRequest{Email: "invited@x.com", Role: model.RoleSSA})
	// No PendingShare row seeded -- materialize already cleared it. The
	// revoke's already-claimed check must resolve email -> userID via
	// Cognito (the fake above), not GetUserByEmail's GSI2 mock.

	err := svc.RevokePendingMember(context.Background(), "owner-1", acc.AccountID, "invited@x.com")
	if !errors.Is(err, ErrPendingAlreadyClaimed) {
		t.Errorf("expected ErrPendingAlreadyClaimed, got %v", err)
	}
}

func TestAddMember_InvitedButNotYetLoggedIn_QueuesPendingShare(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	svc.SetCognitoAdminAPI(&fakeCognitoAdminAPI{
		adminGetUserFn: func(_ context.Context, _ *cognitoidp.AdminGetUserInput) (*cognitoidp.AdminGetUserOutput, error) {
			return &cognitoidp.AdminGetUserOutput{
				Username:       aws.String("invitee-username-1"),
				UserAttributes: []cognitoidptypes.AttributeType{{Name: aws.String("sub"), Value: aws.String("invitee-sub-1")}},
			}, nil
		},
	}, "pool-1")
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})

	dto, err := svc.AddMember(context.Background(), "owner-1", acc.AccountID, &model.AddMemberRequest{Email: "invited@x.com", Role: model.RoleSSA})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !dto.Pending || dto.UserID != "" || dto.Email != "invited@x.com" || dto.Role != model.RoleSSA {
		t.Errorf("unexpected pending dto: %+v", dto)
	}
	if len(repo.pendingShares) != 1 {
		t.Fatalf("expected 1 pending share, got %d", len(repo.pendingShares))
	}
	p := repo.pendingShares[0]
	if p.Kind != model.PendingShareKindAccount || p.AccountID != acc.AccountID || p.Role != model.RoleSSA || p.InvitedByUserID != "owner-1" || p.InvitedCognitoSub != "invitee-sub-1" {
		t.Errorf("unexpected pending share: %+v", p)
	}
}

func TestAddMember_PendingSelfInviteRejected(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	svc.SetCognitoAdminAPI(&fakeCognitoAdminAPI{
		adminGetUserFn: func(_ context.Context, _ *cognitoidp.AdminGetUserInput) (*cognitoidp.AdminGetUserOutput, error) {
			return &cognitoidp.AdminGetUserOutput{
				Username:       aws.String("owner-1"),
				UserAttributes: []cognitoidptypes.AttributeType{{Name: aws.String("sub"), Value: aws.String("owner-1")}},
			}, nil
		},
	}, "pool-1")
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})

	_, err := svc.AddMember(context.Background(), "owner-1", acc.AccountID, &model.AddMemberRequest{Email: "not-yet-logged-in@x.com", Role: model.RoleSSA})
	if !errors.Is(err, ErrSelfShare) {
		t.Errorf("expected ErrSelfShare, got %v", err)
	}
	if len(repo.pendingShares) != 0 {
		t.Errorf("expected no pending share to be queued for a self-invite, got %d", len(repo.pendingShares))
	}
}

func TestAddMember_UnknownEmail_NotInvited_StillErrUserNotFound(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	svc.SetCognitoAdminAPI(&fakeCognitoAdminAPI{
		adminGetUserFn: func(_ context.Context, _ *cognitoidp.AdminGetUserInput) (*cognitoidp.AdminGetUserOutput, error) {
			return nil, &cognitoidptypes.UserNotFoundException{}
		},
	}, "pool-1")
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})

	_, err := svc.AddMember(context.Background(), "owner-1", acc.AccountID, &model.AddMemberRequest{Email: "ghost@x.com", Role: model.RoleSSA})
	if !errors.Is(err, ErrUserNotFound) {
		t.Errorf("expected ErrUserNotFound, got %v", err)
	}
	if len(repo.pendingShares) != 0 {
		t.Errorf("expected no pending share for a never-invited email, got %d", len(repo.pendingShares))
	}
}

func TestAddMember_RejectsQueuingWithEmptyCognitoSub(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	// The `sub` attribute is a required response field per the AWS API
	// contract and should never actually be missing/empty on a real invited
	// user, but the authorization gate must fail closed if it somehow is --
	// queuing an unclaimable grant (no identity to bind it to) would be
	// worse than just rejecting the invite.
	svc.SetCognitoAdminAPI(&fakeCognitoAdminAPI{
		adminGetUserFn: func(_ context.Context, _ *cognitoidp.AdminGetUserInput) (*cognitoidp.AdminGetUserOutput, error) {
			return &cognitoidp.AdminGetUserOutput{
				Username:       aws.String("invitee-username-1"),
				UserAttributes: []cognitoidptypes.AttributeType{{Name: aws.String("sub"), Value: aws.String("")}},
			}, nil
		},
	}, "pool-1")
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})

	_, err := svc.AddMember(context.Background(), "owner-1", acc.AccountID, &model.AddMemberRequest{Email: "invited@x.com", Role: model.RoleSSA})
	if !errors.Is(err, ErrUserNotFound) {
		t.Errorf("expected ErrUserNotFound when the invite resolves to an empty sub, got %v", err)
	}
	if len(repo.pendingShares) != 0 {
		t.Errorf("expected no pending share to be queued with an empty sub, got %d", len(repo.pendingShares))
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

func TestRemoveMember_OwnerRemovesMember(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})
	seedUser(repo, "tam-1", "tam@x.com")
	svc.AddMember(context.Background(), "owner-1", acc.AccountID, &model.AddMemberRequest{Email: "tam@x.com", Role: model.RoleTAM})

	if _, err := svc.RemoveMember(context.Background(), "owner-1", acc.AccountID, "tam-1", false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	mem, _ := repo.GetMember(context.Background(), acc.AccountID, "tam-1")
	if mem != nil {
		t.Errorf("expected member removed, got %+v", mem)
	}
}

func TestRemoveMember_NonOwnerForbidden(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})
	seedUser(repo, "tam-1", "tam@x.com")
	svc.AddMember(context.Background(), "owner-1", acc.AccountID, &model.AddMemberRequest{Email: "tam@x.com", Role: model.RoleTAM})
	seedUser(repo, "ssa-1", "ssa@x.com")
	svc.AddMember(context.Background(), "owner-1", acc.AccountID, &model.AddMemberRequest{Email: "ssa@x.com", Role: model.RoleSSA})

	_, err := svc.RemoveMember(context.Background(), "tam-1", acc.AccountID, "ssa-1", false)
	if !errors.Is(err, ErrForbidden) {
		t.Errorf("expected ErrForbidden, got %v", err)
	}
}

func TestRemoveMember_OwnerCannotBeRemoved(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})

	_, err := svc.RemoveMember(context.Background(), "owner-1", acc.AccountID, "owner-1", false)
	if !errors.Is(err, ErrInvalidInput) {
		t.Errorf("expected ErrInvalidInput, got %v", err)
	}
}

func TestRemoveMember_MissingMemberNotFound(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})

	_, err := svc.RemoveMember(context.Background(), "owner-1", acc.AccountID, "ghost-1", false)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestRemoveMember_ListRefsFailurePreservesMembershipAndErrors(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})
	seedUser(repo, "tam-1", "tam@x.com")
	svc.AddMember(context.Background(), "owner-1", acc.AccountID, &model.AddMemberRequest{Email: "tam@x.com", Role: model.RoleTAM})
	repo.listMeetingRefsErr = errors.New("dynamodb unavailable")

	_, err := svc.RemoveMember(context.Background(), "owner-1", acc.AccountID, "tam-1", false)
	if err == nil {
		t.Fatal("expected an error when ListMeetingRefsForAccount fails, got nil")
	}
	mem, _ := repo.GetMember(context.Background(), acc.AccountID, "tam-1")
	if mem == nil {
		t.Error("expected membership to be preserved (untouched) when the refs list fails before delete, but member was removed")
	}
}

func TestRemoveMember_RevokesAccountOriginShare(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})
	seedUser(repo, "tam-1", "tam@x.com")
	svc.AddMember(context.Background(), "owner-1", acc.AccountID, &model.AddMemberRequest{Email: "tam@x.com", Role: model.RoleTAM})
	repo.meetingRefs[acc.AccountID] = []model.MeetingRef{{AccountID: acc.AccountID, MeetingID: "m-1"}}
	repo.meetings["m-1"] = &model.Meeting{MeetingID: "m-1", AccountID: acc.AccountID, SharedToAccount: true}
	repo.shares[acctShareKey("tam-1", "m-1")] = &model.Share{
		MeetingID: "m-1", SharedToID: "tam-1", Permission: model.PermissionRead, Origin: model.ShareOriginAccount, AccountID: acc.AccountID,
	}

	if _, err := svc.RemoveMember(context.Background(), "owner-1", acc.AccountID, "tam-1", false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if repo.shares[acctShareKey("tam-1", "m-1")] != nil {
		t.Error("expected account-origin share to be revoked")
	}
}

func TestRemoveMember_AmbiguousShareBlocksRemovalWithoutForce(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})
	seedUser(repo, "tam-1", "tam@x.com")
	svc.AddMember(context.Background(), "owner-1", acc.AccountID, &model.AddMemberRequest{Email: "tam@x.com", Role: model.RoleTAM})
	repo.meetingRefs[acc.AccountID] = []model.MeetingRef{{AccountID: acc.AccountID, MeetingID: "m-1"}}
	repo.meetings["m-1"] = &model.Meeting{MeetingID: "m-1", AccountID: acc.AccountID, SharedToAccount: true}
	repo.shares[acctShareKey("tam-1", "m-1")] = &model.Share{
		MeetingID: "m-1", SharedToID: "tam-1", Permission: model.PermissionRead, Origin: "", // direct share (or legacy account-share -- ambiguous)
	}

	// This is the fix this round's plan-gate MAJOR called for: without force,
	// removal must be REFUSED (not silently succeed and merely report the
	// ambiguity afterward) so membership is never deleted out from under a
	// member who might only be holding legacy shares.
	_, err := svc.RemoveMember(context.Background(), "owner-1", acc.AccountID, "tam-1", false)
	if !errors.Is(err, ErrAmbiguousShareBlocksRemoval) {
		t.Errorf("expected ErrAmbiguousShareBlocksRemoval, got %v", err)
	}
	if mem, _ := repo.GetMember(context.Background(), acc.AccountID, "tam-1"); mem == nil {
		t.Error("expected membership to be preserved (untouched) when removal is blocked, but member was removed")
	}
}

func TestRemoveMember_ForcePreservesDirectShare(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})
	seedUser(repo, "tam-1", "tam@x.com")
	svc.AddMember(context.Background(), "owner-1", acc.AccountID, &model.AddMemberRequest{Email: "tam@x.com", Role: model.RoleTAM})
	repo.meetingRefs[acc.AccountID] = []model.MeetingRef{{AccountID: acc.AccountID, MeetingID: "m-1"}}
	repo.meetings["m-1"] = &model.Meeting{MeetingID: "m-1", AccountID: acc.AccountID, SharedToAccount: true}
	repo.shares[acctShareKey("tam-1", "m-1")] = &model.Share{
		MeetingID: "m-1", SharedToID: "tam-1", Permission: model.PermissionRead, Origin: "", // direct share
	}

	if _, err := svc.RemoveMember(context.Background(), "owner-1", acc.AccountID, "tam-1", true); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if repo.shares[acctShareKey("tam-1", "m-1")] == nil {
		t.Error("expected direct share to be preserved (not account-origin)")
	}
}

func TestRemoveMember_RaceCreatingDirectShareDuringCleanupIsNotDeleted(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})
	seedUser(repo, "tam-1", "tam@x.com")
	svc.AddMember(context.Background(), "owner-1", acc.AccountID, &model.AddMemberRequest{Email: "tam@x.com", Role: model.RoleTAM})
	repo.meetingRefs[acc.AccountID] = []model.MeetingRef{{AccountID: acc.AccountID, MeetingID: "m-1"}}
	repo.meetings["m-1"] = &model.Meeting{MeetingID: "m-1", AccountID: acc.AccountID, SharedToAccount: true}
	repo.shares[acctShareKey("tam-1", "m-1")] = &model.Share{
		MeetingID: "m-1", SharedToID: "tam-1", Permission: model.PermissionRead, Origin: model.ShareOriginAccount, AccountID: acc.AccountID,
	}
	// Simulates the owner creating a new direct share for tam-1/m-1 in the
	// window between cleanup's GetShare read and its DeleteShareIfAccountOrigin
	// call -- the exact race the review flagged: without the delete-time
	// condition, that new direct grant would be silently swept up by the
	// delete decided from the stale (pre-race) GetShare read.
	repo.replaceWithDirectShareAfterGet = acctShareKey("tam-1", "m-1")

	// force=true: this test targets the cleanup loop's OWN GetShare->Delete
	// race window specifically. The mock's replaceWithDirectShareAfterGet
	// swap fires on its FIRST matching GetShare call and is a one-shot side
	// effect -- if force=false ran the ambiguous-share precheck first, that
	// precheck's own GetShare call would consume the swap before the
	// cleanup loop ever ran, changing which code path this test exercises.
	if _, err := svc.RemoveMember(context.Background(), "owner-1", acc.AccountID, "tam-1", true); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	share := repo.shares[acctShareKey("tam-1", "m-1")]
	if share == nil {
		t.Fatal("expected the racily-created direct share to survive cleanup, got nil")
	}
	if share.Origin == model.ShareOriginAccount {
		t.Error("expected the surviving share to be the new direct grant, not the stale account-origin one")
	}
}

func TestRemoveMember_CleanupFailureDoesNotFailRemoval(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})
	seedUser(repo, "tam-1", "tam@x.com")
	svc.AddMember(context.Background(), "owner-1", acc.AccountID, &model.AddMemberRequest{Email: "tam@x.com", Role: model.RoleTAM})
	repo.meetingRefs[acc.AccountID] = []model.MeetingRef{{AccountID: acc.AccountID, MeetingID: "m-1"}}
	repo.meetings["m-1"] = &model.Meeting{MeetingID: "m-1", AccountID: acc.AccountID, SharedToAccount: true}
	repo.shareOpErr["m-1"] = errors.New("simulated transient DynamoDB error")

	// force=true: with force=false the precheck now fails closed on this
	// same transient error (see TestRemoveMember_PrecheckFailsClosedOnTransientError)
	// and never reaches membership deletion at all. This test targets the
	// cleanup loop's OWN soft-fail handling, which only runs post-delete --
	// force=true skips the precheck to get there.
	result, err := svc.RemoveMember(context.Background(), "owner-1", acc.AccountID, "tam-1", true)
	if err != nil {
		t.Fatalf("expected RemoveMember to succeed despite share-cleanup failure, got: %v", err)
	}
	if mem, _ := repo.GetMember(context.Background(), acc.AccountID, "tam-1"); mem != nil {
		t.Errorf("expected member to remain removed despite cleanup failure, got %+v", mem)
	}
	if len(result.FailedMeetingIDs) != 1 || result.FailedMeetingIDs[0] != "m-1" {
		t.Errorf("expected FailedMeetingIDs=[m-1], got %v", result.FailedMeetingIDs)
	}
}

func TestRemoveMember_SurfacesAmbiguousLegacyShare(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})
	seedUser(repo, "tam-1", "tam@x.com")
	svc.AddMember(context.Background(), "owner-1", acc.AccountID, &model.AddMemberRequest{Email: "tam@x.com", Role: model.RoleTAM})
	repo.meetingRefs[acc.AccountID] = []model.MeetingRef{{AccountID: acc.AccountID, MeetingID: "m-1"}}
	repo.meetings["m-1"] = &model.Meeting{MeetingID: "m-1", AccountID: acc.AccountID, SharedToAccount: true}
	repo.shares[acctShareKey("tam-1", "m-1")] = &model.Share{
		MeetingID: "m-1", SharedToID: "tam-1", Permission: model.PermissionRead, Origin: "", // ambiguous: legacy account-share OR direct grant
	}

	// force=true: the ambiguous share would otherwise block this removal
	// outright (see TestRemoveMember_AmbiguousShareBlocksRemovalWithoutForce)
	// -- this test covers what happens once an owner has consciously
	// overridden that block.
	result, err := svc.RemoveMember(context.Background(), "owner-1", acc.AccountID, "tam-1", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.AmbiguousUntaggedMeetingIDs) != 1 || result.AmbiguousUntaggedMeetingIDs[0] != "m-1" {
		t.Errorf("expected AmbiguousUntaggedMeetingIDs=[m-1], got %v", result.AmbiguousUntaggedMeetingIDs)
	}
	if len(result.FailedMeetingIDs) != 0 {
		t.Errorf("expected no failures (this is not a failure), got %v", result.FailedMeetingIDs)
	}
	if repo.shares[acctShareKey("tam-1", "m-1")] == nil {
		t.Error("expected the ambiguous share to be left untouched")
	}
}

func TestRemoveMember_EmptyResultListsAreNotNil(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})
	seedUser(repo, "tam-1", "tam@x.com")
	svc.AddMember(context.Background(), "owner-1", acc.AccountID, &model.AddMemberRequest{Email: "tam@x.com", Role: model.RoleTAM})
	// No meeting refs at all -- the cleanup loop never runs, so nothing is
	// ever appended to either result slice. They must still encode as JSON
	// [] (pre-initialized), not null (the nil zero value).

	result, err := svc.RemoveMember(context.Background(), "owner-1", acc.AccountID, "tam-1", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.FailedMeetingIDs == nil {
		t.Error("expected FailedMeetingIDs to be a non-nil empty slice, got nil")
	}
	if result.AmbiguousUntaggedMeetingIDs == nil {
		t.Error("expected AmbiguousUntaggedMeetingIDs to be a non-nil empty slice, got nil")
	}
}

func TestRemoveMember_CrossAccountMeetingRefNotTouched(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})
	seedUser(repo, "tam-1", "tam@x.com")
	svc.AddMember(context.Background(), "owner-1", acc.AccountID, &model.AddMemberRequest{Email: "tam@x.com", Role: model.RoleTAM})
	// Simulates ADR-016's known non-transactional write-order gap: the
	// meeting was re-shared from acc.AccountID to a DIFFERENT account
	// ("other-acc") without acc.AccountID's stale MeetingRef ever being
	// cleaned up. Without the meeting.AccountID == accountID guard,
	// RemoveMember here would delete a share that actually belongs to
	// other-acc's membership grant.
	repo.meetingRefs[acc.AccountID] = []model.MeetingRef{{AccountID: acc.AccountID, MeetingID: "m-1"}}
	repo.meetings["m-1"] = &model.Meeting{MeetingID: "m-1", AccountID: "other-acc", SharedToAccount: true}
	repo.shares[acctShareKey("tam-1", "m-1")] = &model.Share{
		// AccountID is other-acc's fresh grant, not acc.AccountID's -- this is
		// exactly the row DeleteShareIfAccountOrigin's accountId condition
		// must refuse to touch even though its origin is also "account".
		MeetingID: "m-1", SharedToID: "tam-1", Permission: model.PermissionRead, Origin: model.ShareOriginAccount, AccountID: "other-acc",
	}

	if _, err := svc.RemoveMember(context.Background(), "owner-1", acc.AccountID, "tam-1", false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if repo.shares[acctShareKey("tam-1", "m-1")] == nil {
		t.Error("expected share belonging to a different account's re-share to survive this account's RemoveMember cleanup")
	}
}

func TestRemoveMember_CrossAccountReshareRaceDuringCleanupIsNotDeleted(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})
	seedUser(repo, "tam-1", "tam@x.com")
	svc.AddMember(context.Background(), "owner-1", acc.AccountID, &model.AddMemberRequest{Email: "tam@x.com", Role: model.RoleTAM})
	repo.meetingRefs[acc.AccountID] = []model.MeetingRef{{AccountID: acc.AccountID, MeetingID: "m-1"}}
	// Unlike TestRemoveMember_CrossAccountMeetingRefNotTouched (where the
	// read-time meeting.AccountID check already excludes the ref), this
	// meeting still shows acc.AccountID at GetMeetingByID time -- the
	// re-share to "other-acc" happens AFTER that read, in the same gap
	// before DeleteShareIfAccountOrigin's delete. The review's MAJOR: this
	// exact race is not excluded by any read, only by a condition on the
	// row being deleted.
	repo.meetings["m-1"] = &model.Meeting{MeetingID: "m-1", AccountID: acc.AccountID, SharedToAccount: true}
	repo.shares[acctShareKey("tam-1", "m-1")] = &model.Share{
		MeetingID: "m-1", SharedToID: "tam-1", Permission: model.PermissionRead, Origin: model.ShareOriginAccount, AccountID: acc.AccountID,
	}
	repo.replaceWithOtherAccountShareAfterGet = acctShareKey("tam-1", "m-1")

	if _, err := svc.RemoveMember(context.Background(), "owner-1", acc.AccountID, "tam-1", false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	share := repo.shares[acctShareKey("tam-1", "m-1")]
	if share == nil {
		t.Fatal("expected the racily-created other-account share to survive cleanup, got nil")
	}
	if share.AccountID != "other-acc" {
		t.Errorf("expected the surviving share to be other-acc's fresh grant, got AccountID=%q", share.AccountID)
	}
}

func TestRemoveMember_PrecheckFailsClosedOnTransientError(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})
	seedUser(repo, "tam-1", "tam@x.com")
	svc.AddMember(context.Background(), "owner-1", acc.AccountID, &model.AddMemberRequest{Email: "tam@x.com", Role: model.RoleTAM})
	repo.meetingRefs[acc.AccountID] = []model.MeetingRef{{AccountID: acc.AccountID, MeetingID: "m-1"}}
	repo.meetings["m-1"] = &model.Meeting{MeetingID: "m-1", AccountID: acc.AccountID, SharedToAccount: true}
	repo.shareOpErr["m-1"] = errors.New("simulated transient DynamoDB error")

	// The review's MAJOR finding: checkNoAmbiguousShares used to swallow a
	// transient GetShare error (log + continue), reopening the exact
	// fail-open access-retention gap this precheck exists to close. It must
	// now fail closed: return the error and leave membership untouched.
	_, err := svc.RemoveMember(context.Background(), "owner-1", acc.AccountID, "tam-1", false)
	if err == nil {
		t.Fatal("expected a transient precheck read error to block removal, got nil")
	}
	if errors.Is(err, ErrAmbiguousShareBlocksRemoval) {
		t.Error("a transient read error is not a confirmed ambiguity -- should not be ErrAmbiguousShareBlocksRemoval")
	}
	if mem, _ := repo.GetMember(context.Background(), acc.AccountID, "tam-1"); mem == nil {
		t.Error("expected membership to be preserved when the precheck fails closed on a transient error")
	}
}

func TestRemoveMember_LinkOnlyMeetingShareDoesNotBlockOrGetTouched(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})
	seedUser(repo, "tam-1", "tam@x.com")
	svc.AddMember(context.Background(), "owner-1", acc.AccountID, &model.AddMemberRequest{Email: "tam@x.com", Role: model.RoleTAM})
	repo.meetingRefs[acc.AccountID] = []model.MeetingRef{{AccountID: acc.AccountID, MeetingID: "m-1"}}
	// Link-only: AccountID is set (the account can browse it via the link)
	// but SharedToAccount is false -- this was never a team-share grant, so
	// a direct share tam-1 happens to hold here is unrelated to this
	// account's membership. The review flagged that the precheck and
	// cleanup loop, checking only meeting.AccountID, would wrongly treat
	// this as ambiguous (blocking removal) or sweep it into
	// AmbiguousUntaggedMeetingIDs -- neither should happen.
	repo.meetings["m-1"] = &model.Meeting{MeetingID: "m-1", AccountID: acc.AccountID, SharedToAccount: false}
	repo.shares[acctShareKey("tam-1", "m-1")] = &model.Share{
		MeetingID: "m-1", SharedToID: "tam-1", Permission: model.PermissionRead, Origin: "",
	}

	result, err := svc.RemoveMember(context.Background(), "owner-1", acc.AccountID, "tam-1", false)
	if err != nil {
		t.Fatalf("unexpected error (Link-only share must not block removal): %v", err)
	}
	if len(result.AmbiguousUntaggedMeetingIDs) != 0 {
		t.Errorf("expected Link-only meeting's share to not be reported as ambiguous, got %v", result.AmbiguousUntaggedMeetingIDs)
	}
	if repo.shares[acctShareKey("tam-1", "m-1")] == nil {
		t.Error("expected Link-only meeting's share to be left untouched")
	}
}

func TestUpdateMemberRole_OwnerChangesRole(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})
	seedUser(repo, "tam-1", "tam@x.com")
	svc.AddMember(context.Background(), "owner-1", acc.AccountID, &model.AddMemberRequest{Email: "tam@x.com", Role: model.RoleTAM})
	before, _ := repo.GetMember(context.Background(), acc.AccountID, "tam-1")

	dto, err := svc.UpdateMemberRole(context.Background(), "owner-1", acc.AccountID, "tam-1", &model.UpdateMemberRequest{Role: model.RoleSSA})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dto.Role != model.RoleSSA {
		t.Errorf("expected role SSA, got %+v", dto)
	}
	after, _ := repo.GetMember(context.Background(), acc.AccountID, "tam-1")
	if !after.AddedAt.Equal(before.AddedAt) {
		t.Errorf("expected AddedAt preserved, before=%v after=%v", before.AddedAt, after.AddedAt)
	}
}

func TestUpdateMemberRole_ConcurrentlyRemovedMemberNotFound(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})
	seedUser(repo, "tam-1", "tam@x.com")
	svc.AddMember(context.Background(), "owner-1", acc.AccountID, &model.AddMemberRequest{Email: "tam@x.com", Role: model.RoleTAM})

	// Simulate a concurrent RemoveMember completing in the gap right after
	// UpdateMemberRole's own GetMember(target) call returns -- the read sees
	// the member, but the write below must not resurrect it.
	repo.deleteMemberAfterGet = memberKey(acc.AccountID, "tam-1")

	_, err := svc.UpdateMemberRole(context.Background(), "owner-1", acc.AccountID, "tam-1", &model.UpdateMemberRequest{Role: model.RoleSSA})
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
	if mem, _ := repo.GetMember(context.Background(), acc.AccountID, "tam-1"); mem != nil {
		t.Errorf("expected member to remain deleted, got %+v", mem)
	}
}

func TestUpdateMemberRole_InvalidRoleRejected(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})
	seedUser(repo, "tam-1", "tam@x.com")
	svc.AddMember(context.Background(), "owner-1", acc.AccountID, &model.AddMemberRequest{Email: "tam@x.com", Role: model.RoleTAM})

	_, err := svc.UpdateMemberRole(context.Background(), "owner-1", acc.AccountID, "tam-1", &model.UpdateMemberRequest{Role: "owner"})
	if !errors.Is(err, ErrInvalidInput) {
		t.Errorf("expected ErrInvalidInput, got %v", err)
	}
}

func TestUpdateMemberRole_OwnerTargetRejected(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})

	_, err := svc.UpdateMemberRole(context.Background(), "owner-1", acc.AccountID, "owner-1", &model.UpdateMemberRequest{Role: model.RoleTAM})
	if !errors.Is(err, ErrInvalidInput) {
		t.Errorf("expected ErrInvalidInput, got %v", err)
	}
}

// TestUpdateMemberRole_MemberCanChangeAnotherMembersRole pins ADR-033: role
// changes are no longer owner-only either.
func TestUpdateMemberRole_MemberCanChangeAnotherMembersRole(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})
	seedUser(repo, "tam-1", "tam@x.com")
	svc.AddMember(context.Background(), "owner-1", acc.AccountID, &model.AddMemberRequest{Email: "tam@x.com", Role: model.RoleTAM})
	seedUser(repo, "ssa-1", "ssa@x.com")
	svc.AddMember(context.Background(), "owner-1", acc.AccountID, &model.AddMemberRequest{Email: "ssa@x.com", Role: model.RoleSSA})

	dto, err := svc.UpdateMemberRole(context.Background(), "tam-1", acc.AccountID, "ssa-1", &model.UpdateMemberRequest{Role: model.RoleAM})
	if err != nil {
		t.Fatalf("expected a non-owner member to be able to change another member's role, got %v", err)
	}
	if dto.Role != model.RoleAM {
		t.Errorf("expected role AM, got %+v", dto)
	}
}

// TestUpdateMemberRole_NonMemberForbidden: someone with no membership at all
// on this account still cannot change roles.
func TestUpdateMemberRole_NonMemberForbidden(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})
	seedUser(repo, "tam-1", "tam@x.com")
	svc.AddMember(context.Background(), "owner-1", acc.AccountID, &model.AddMemberRequest{Email: "tam@x.com", Role: model.RoleTAM})

	_, err := svc.UpdateMemberRole(context.Background(), "stranger-1", acc.AccountID, "tam-1", &model.UpdateMemberRequest{Role: model.RoleSSA})
	if !errors.Is(err, ErrForbidden) {
		t.Errorf("expected ErrForbidden, got %v", err)
	}
}

// TestUpdateMemberRole_MemberCannotSetOwnerRole: the role allowlist still
// excludes "owner", regardless of who the requester is.
func TestUpdateMemberRole_MemberCannotSetOwnerRole(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})
	seedUser(repo, "tam-1", "tam@x.com")
	svc.AddMember(context.Background(), "owner-1", acc.AccountID, &model.AddMemberRequest{Email: "tam@x.com", Role: model.RoleTAM})

	_, err := svc.UpdateMemberRole(context.Background(), "tam-1", acc.AccountID, "tam-1", &model.UpdateMemberRequest{Role: model.RoleOwner})
	if !errors.Is(err, ErrInvalidInput) {
		t.Errorf("expected ErrInvalidInput, got %v", err)
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

func TestAddMember_NewRoleAccepted(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})
	seedUser(repo, "x-1", "x@x.com")
	dto, err := svc.AddMember(context.Background(), "owner-1", acc.AccountID, &model.AddMemberRequest{Email: "x@x.com", Role: model.RoleSAManager})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dto.Role != model.RoleSAManager {
		t.Errorf("expected role %q, got %q", model.RoleSAManager, dto.Role)
	}
}

func TestAddMember_ArbitraryRoleRejected(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})
	seedUser(repo, "x-1", "x@x.com")
	_, err := svc.AddMember(context.Background(), "owner-1", acc.AccountID, &model.AddMemberRequest{Email: "x@x.com", Role: "PM"})
	if !errors.Is(err, ErrInvalidInput) {
		t.Errorf("expected ErrInvalidInput, got %v", err)
	}
}

func TestAddMember_LowercaseRoleRejected(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})
	seedUser(repo, "x-1", "x@x.com")
	_, err := svc.AddMember(context.Background(), "owner-1", acc.AccountID, &model.AddMemberRequest{Email: "x@x.com", Role: "ssa"})
	if !errors.Is(err, ErrInvalidInput) {
		t.Errorf("expected ErrInvalidInput (case-sensitive), got %v", err)
	}
}

func TestListAccountInsights_FilterByType(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	acc, _ := svc.CreateAccount(context.Background(), "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "하나은행"})
	d := time.Date(2026, 5, 12, 9, 0, 0, 0, time.UTC)
	repo.insightsByAccount[acc.AccountID] = []model.AccountInsight{
		{AccountID: acc.AccountID, Type: model.InsightRisk, Text: "지연", Evidence: "일정 미확정", Implication: "오픈 지연", NextAction: "일정 확정", OccurredAt: d, SourceID: "m-1", SourceType: "meeting"},
		{AccountID: acc.AccountID, Type: model.InsightTech, Text: "EKS", OccurredAt: d, SourceID: "m-1", SourceType: "meeting"},
	}
	got, err := svc.ListAccountInsights(context.Background(), "owner-1", acc.AccountID, time.Time{}, time.Time{}, []string{model.InsightRisk})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].Type != model.InsightRisk {
		t.Errorf("expected only risk, got %+v", got)
	}
	if got[0].Implication != "오픈 지연" || got[0].NextAction != "일정 확정" {
		t.Errorf("structured fields were not mapped to DTO: %+v", got[0])
	}
	// Evidence non-exposure needs no runtime assert: AccountInsightDTO has no
	// Evidence field at all, so a mapping regression is a compile error.
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

func TestUpdateUserDocument_SlideToNoteRevokesPublicShare(t *testing.T) {
	// Regression: converting a publicly-shared slide to a markdown note
	// used to leave PublicShareToken on the doc untouched (updateDoc's
	// whole-item PutAccountDocument just carried the stale snapshot
	// value through). ResolvePublicShare's FileKey=="" check happened to
	// mask this immediately, but attaching a *new* file to the doc later
	// would have resurrected the old link, exposing the new file to
	// whoever held the old token without the owner ever re-sharing.
	repo := newMockAccountRepo()
	svc := &AccountService{repo: repo, s3: &mockS3Deleter{}, bucketName: "test-bucket"}

	created, err := svc.PutUserDocument(context.Background(), "user-1", &model.PutDocumentRequest{
		Title: "Deck", FileKey: "docs/user-1/deck.pdf", FileName: "deck.pdf",
	})
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}
	token, err := svc.CreateUserDocPublicShare(context.Background(), "user-1", created.DocID)
	if err != nil {
		t.Fatalf("share failed: %v", err)
	}

	if _, err := svc.UpdateUserDocument(context.Background(), "user-1", created.DocID, &model.PutDocumentRequest{
		Title: "Now a note", Markdown: mdPtr("body"),
	}); err != nil {
		t.Fatalf("update failed: %v", err)
	}

	detail, err := svc.GetUserDocument(context.Background(), "user-1", created.DocID)
	if err != nil {
		t.Fatalf("get failed: %v", err)
	}
	if detail.PublicShareToken != "" {
		t.Errorf("expected PublicShareToken cleared on file->markdown conversion, got %q", detail.PublicShareToken)
	}
	if _, ok := repo.publicShares[token]; ok {
		t.Errorf("expected the old token's pointer to be cleaned up, not left behind")
	}
}

func TestCreateUserDocPublicShare_FailsIfFileKeyClearedConcurrently(t *testing.T) {
	// Regression (round-7 review): CreateUserDocPublicShare's own upfront
	// `doc.FileKey == ""` check only looks at its own getDoc-time
	// snapshot. If a concurrent updateDoc clears fileKey (file->markdown
	// conversion) in the gap between that check and
	// SetPublicShareTokenIfAbsent's write, the mint would otherwise still
	// succeed against a now-file-less doc -- inert only until some future
	// edit reattaches a file, at which point the token would
	// unauthenticated-expose that new file. SetPublicShareTokenIfAbsent's
	// condition now also requires the LIVE fileKey to still be non-empty.
	repo := newMockAccountRepo()
	svc := &AccountService{repo: repo, s3: &mockS3Deleter{}, bucketName: "test-bucket"}

	created, err := svc.PutUserDocument(context.Background(), "user-1", &model.PutDocumentRequest{
		Title: "Deck", FileKey: "docs/user-1/deck.pdf", FileName: "deck.pdf",
	})
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}

	// Simulate a concurrent file->markdown conversion landing right after
	// CreateUserDocPublicShare's own getDoc read (which sees FileKey
	// non-empty and passes the upfront check).
	repo.raceClearFileKeyAfterGet = created.DocID

	if _, err := svc.CreateUserDocPublicShare(context.Background(), "user-1", created.DocID); err == nil {
		t.Fatal("expected the mint to fail once the live fileKey is empty, got nil error")
	}

	got := repo.documents[model.PrefixUser+"user-1"][0].PublicShareToken
	if got != "" {
		t.Fatalf("expected no token to be minted against a file-less doc, got %q", got)
	}
}

func TestUpdateUserDocument_SlideToNoteRemovesTokenMintedAfterOwnRead(t *testing.T) {
	// Regression (round-7 review): a file->markdown conversion's token
	// cleanup used to be gated on the *snapshot* PublicShareToken read at
	// the top of updateDoc. If a concurrent CreateUserDocPublicShare
	// minted a token in the gap between that read and updateDoc's write,
	// the cleanup branch never ran (snapshot showed ""), leaving a token
	// that's inert only until some future edit reattaches a file -- at
	// which point it would unauthenticated-expose the new file. The fix
	// removes publicShareToken unconditionally, atomically with clearing
	// fileKey, regardless of what updateDoc's own read saw.
	repo := newMockAccountRepo()
	svc := &AccountService{repo: repo, s3: &mockS3Deleter{}, bucketName: "test-bucket"}

	created, err := svc.PutUserDocument(context.Background(), "user-1", &model.PutDocumentRequest{
		Title: "Deck", FileKey: "docs/user-1/deck.pdf", FileName: "deck.pdf",
	})
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}

	// Simulate a concurrent CreateUserDocPublicShare landing right after
	// updateDoc's own getDoc read (which sees PublicShareToken=="").
	repo.raceTokenAfterGet = "concurrent-mint"
	repo.publicShares["concurrent-mint"] = &model.PublicShare{Token: "concurrent-mint", DocPK: model.PrefixUser + "user-1", DocID: created.DocID}

	if _, err := svc.UpdateUserDocument(context.Background(), "user-1", created.DocID, &model.PutDocumentRequest{
		Title: "Now a note", Markdown: mdPtr("body"),
	}); err != nil {
		t.Fatalf("update failed: %v", err)
	}

	got := repo.documents[model.PrefixUser+"user-1"][0].PublicShareToken
	if got != "" {
		t.Fatalf("expected the concurrently-minted token to be removed too, got %q", got)
	}
	if _, ok := repo.publicShares["concurrent-mint"]; ok {
		t.Errorf("expected the concurrently-minted token's pointer to be cleaned up")
	}
}

func TestUpdateUserDocument_TitleOnlyEditDoesNotClobberConcurrentToken(t *testing.T) {
	// Regression: updateDoc used to carry existing.PublicShareToken through
	// from its initial getDoc snapshot into a final whole-item
	// PutAccountDocument. A title-only edit (no file/body change at all)
	// shouldn't touch sharing state at all -- the fix (UpdateAccountDocumentFields,
	// a field-scoped UpdateItem that never includes "publicShareToken")
	// makes that structural rather than best-effort: a concurrent
	// share/revoke landing mid-request simply can't be clobbered by a
	// write that never names the field.
	repo := newMockAccountRepo()
	svc := &AccountService{repo: repo, s3: &mockS3Deleter{}, bucketName: "test-bucket"}

	created, err := svc.PutUserDocument(context.Background(), "user-1", &model.PutDocumentRequest{
		Title: "Deck", FileKey: "docs/user-1/deck.pdf", FileName: "deck.pdf",
	})
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}
	if _, err := svc.CreateUserDocPublicShare(context.Background(), "user-1", created.DocID); err != nil {
		t.Fatalf("share failed: %v", err)
	}

	// Simulate a concurrent revoke+reshare landing right after this
	// update's initial getDoc read: the stored token changes to a
	// different value before the update's own write happens.
	repo.raceTokenAfterGet = "concurrent-new-token"

	if _, err := svc.UpdateUserDocument(context.Background(), "user-1", created.DocID, &model.PutDocumentRequest{
		Title: "Deck v2",
	}); err != nil {
		t.Fatalf("update failed: %v", err)
	}

	got := repo.documents[model.PrefixUser+"user-1"][0].PublicShareToken
	if got != "concurrent-new-token" {
		t.Fatalf("expected the concurrently-changed token to survive the title-only edit, got %q", got)
	}
}

func TestUpdateUserDocument_DeletedBetweenGetAndUpdate_ReturnsNotFound(t *testing.T) {
	// UpdateAccountDocumentFields' attribute_exists(PK) condition should
	// fail closed with ErrNotFound rather than the raw ErrConditionFailed,
	// matching LinkAccount/UnlinkAccount's mapping for the same race.
	repo := newMockAccountRepo()
	svc := &AccountService{repo: repo, s3: &mockS3Deleter{}, bucketName: "test-bucket"}

	created, err := svc.PutUserDocument(context.Background(), "user-1", &model.PutDocumentRequest{
		Title: "Note", Markdown: mdPtr("body"),
	})
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}
	repo.deleteAfterGet = created.DocID

	if _, err := svc.UpdateUserDocument(context.Background(), "user-1", created.DocID, &model.PutDocumentRequest{
		Title: "Note v2",
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
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

func TestRevokeUserDocPublicShare_DoesNotClobberConcurrentReshare(t *testing.T) {
	// Regression: RevokeUserDocPublicShare used to read the doc, then
	// PutAccountDocument the *entire* stale snapshot back with the token
	// field cleared. If the doc's token changed between that read and
	// write (e.g. a concurrent revoke+reshare), the revoke would silently
	// stomp the new token, breaking a share the caller never asked to
	// touch. The fix makes the clear a conditional update scoped to the
	// exact token read, so a mismatch is a no-op rather than a clobber --
	// and, since the clear now happens BEFORE the pointer delete (not
	// after), a mismatch also means the pointer delete never runs, so the
	// stale token's pointer is left as an inert orphan rather than
	// unconditionally deleted regardless of what else changed underneath.
	repo := newMockAccountRepo()
	svc := &AccountService{repo: repo, s3: &mockS3Deleter{}, bucketName: "test-bucket"}

	slide := model.AccountDocument{
		PK: model.PrefixUser + "user-1", SK: model.PrefixDoc + "slide-1",
		DocID: "slide-1", Title: "Deck", DocType: "slide",
		FileKey: "docs/user-1/123_deck.pdf", FileName: "deck.pdf",
		SourceUserID: "user-1", EntityType: model.EntityTypeUserDoc,
	}
	repo.documents[slide.PK] = append(repo.documents[slide.PK], slide)

	oldToken, err := svc.CreateUserDocPublicShare(context.Background(), "user-1", "slide-1")
	if err != nil {
		t.Fatalf("initial share failed: %v", err)
	}

	// Simulate a concurrent revoke+reshare landing between RevokeUserDocPublicShare's
	// getDoc read (which will see oldToken) and its write: right after that
	// read, the stored doc's token changes to "newer-token".
	repo.raceTokenAfterGet = "newer-token"

	if err := svc.RevokeUserDocPublicShare(context.Background(), "user-1", "slide-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := repo.documents[slide.PK][0].PublicShareToken
	if got != "newer-token" {
		t.Fatalf("expected the newer token to survive the stale revoke, got %q", got)
	}
	// The old token's pointer is left as an inert orphan (not deleted) --
	// ResolvePublicShare still fails it closed via the doc's
	// PublicShareToken != token check, so this is safe, just not tidy.
	if _, ok := repo.publicShares[oldToken]; !ok {
		t.Errorf("expected the old token's now-orphaned pointer to still exist (clear-before-delete ordering)")
	}
}

func TestCreateUserDocPublicShare_ConcurrentMintReturnsWinnerToken(t *testing.T) {
	// Regression: two concurrent CreateUserDocPublicShare calls both read
	// PublicShareToken=="" before either writes. Without a conditional
	// write, the second doc write would silently clobber the first,
	// leaving the first caller holding a token whose PublicShare pointer
	// exists but no longer matches the doc -- ResolvePublicShare rejects it
	// as stale. The loser must instead discard its own token and return
	// the actual winner's.
	repo := newMockAccountRepo()
	svc := &AccountService{repo: repo, s3: &mockS3Deleter{}, bucketName: "test-bucket"}

	slide := model.AccountDocument{
		PK: model.PrefixUser + "user-1", SK: model.PrefixDoc + "slide-1",
		DocID: "slide-1", Title: "Deck", DocType: "slide",
		FileKey: "docs/user-1/123_deck.pdf", FileName: "deck.pdf",
		SourceUserID: "user-1", EntityType: model.EntityTypeUserDoc,
	}
	repo.documents[slide.PK] = append(repo.documents[slide.PK], slide)

	repo.raceWinnerToken = "winner-token"

	token, err := svc.CreateUserDocPublicShare(context.Background(), "user-1", "slide-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if token != "winner-token" {
		t.Fatalf("expected the race winner's token, got %q", token)
	}

	// The loser's own pointer must have been cleaned up -- only the
	// winner's token should resolve.
	found := 0
	for tok := range repo.publicShares {
		if tok == "winner-token" {
			continue
		}
		found++
	}
	if found != 0 {
		t.Fatalf("expected the loser's orphaned PublicShare pointer to be deleted, found %d extra", found)
	}
}

func TestDeleteUserDocument_CleansUpPublicSharePointer(t *testing.T) {
	// Regression: deleting a publicly-shared doc used to leave its
	// PUBSHARE# pointer item behind -- inert (getDoc on the deleted doc
	// 404s) but an orphan that accumulates forever.
	repo := newMockAccountRepo()
	svc := &AccountService{repo: repo, s3: &mockS3Deleter{}, bucketName: "test-bucket"}

	slide := model.AccountDocument{
		PK: model.PrefixUser + "user-1", SK: model.PrefixDoc + "slide-1",
		DocID: "slide-1", Title: "Deck", DocType: "slide",
		FileKey: "docs/user-1/123_deck.pdf", FileName: "deck.pdf",
		SourceUserID: "user-1", EntityType: model.EntityTypeUserDoc,
	}
	repo.documents[slide.PK] = append(repo.documents[slide.PK], slide)

	token, err := svc.CreateUserDocPublicShare(context.Background(), "user-1", "slide-1")
	if err != nil {
		t.Fatalf("share failed: %v", err)
	}

	if err := svc.DeleteUserDocument(context.Background(), "user-1", "slide-1"); err != nil {
		t.Fatalf("delete failed: %v", err)
	}

	if _, ok := repo.publicShares[token]; ok {
		t.Fatalf("expected PublicShare pointer %q to be cleaned up on doc delete", token)
	}
}

// --- per-user document sharing (reference, read-only) ---

func seedUserDoc(repo *mockAccountRepo, ownerID, docID, title string) {
	pk := model.PrefixUser + ownerID
	repo.documents[pk] = append(repo.documents[pk], model.AccountDocument{
		PK: pk, SK: model.PrefixDoc + docID, DocID: docID, Title: title,
		Content: "# " + title, SourceUserID: ownerID, EntityType: model.EntityTypeUserDoc,
	})
}

func TestShareUserDocumentByEmail(t *testing.T) {
	ctx := context.Background()
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	repo.users["b@example.com"] = &model.User{UserID: "user-b", Email: "b@example.com"}
	repo.users["a@example.com"] = &model.User{UserID: "user-a", Email: "a@example.com"}
	seedUserDoc(repo, "user-a", "doc-1", "노트")

	if _, err := svc.ShareUserDocumentByEmail(ctx, "user-a", "a@example.com", "missing", "b@example.com"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound for unknown doc, got %v", err)
	}
	// A non-owner cannot share: the doc isn't in their partition at all.
	if _, err := svc.ShareUserDocumentByEmail(ctx, "user-b", "b@example.com", "doc-1", "a@example.com"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound for non-owner sharer, got %v", err)
	}
	if _, err := svc.ShareUserDocumentByEmail(ctx, "user-a", "a@example.com", "doc-1", "nobody@example.com"); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("expected ErrUserNotFound, got %v", err)
	}
	if _, err := svc.ShareUserDocumentByEmail(ctx, "user-a", "a@example.com", "doc-1", "a@example.com"); !errors.Is(err, ErrSelfShare) {
		t.Fatalf("expected ErrSelfShare, got %v", err)
	}

	share, err := svc.ShareUserDocumentByEmail(ctx, "user-a", "a@example.com", "doc-1", "b@example.com")
	if err != nil {
		t.Fatalf("share failed: %v", err)
	}
	if share.SharedToID != "user-b" || share.Permission != model.PermissionRead {
		t.Fatalf("expected read-only share to user-b, got %+v", share)
	}

	// The recipient reads the OWNER's item -- no copy was made.
	detail, err := svc.GetUserDocument(ctx, "user-b", "doc-1")
	if err != nil {
		t.Fatalf("recipient get failed: %v", err)
	}
	if detail.Content != "# 노트" || detail.SharedBy != "a@example.com" {
		t.Fatalf("expected shared content + sharedBy, got %+v", detail)
	}
	if len(repo.documents[model.PrefixUser+"user-b"]) != 0 {
		t.Fatal("share must not copy the document into the recipient's partition")
	}
	list, err := svc.ListUserDocuments(ctx, "user-b", "")
	if err != nil || len(list) != 1 || list[0].DocID != "doc-1" {
		t.Fatalf("expected shared doc in recipient's list, got %v (%v)", list, err)
	}

	// Read-only: the recipient can neither update nor delete.
	if _, err := svc.UpdateUserDocument(ctx, "user-b", "doc-1", &model.PutDocumentRequest{Title: "hijack", Markdown: mdPtr("x")}); err == nil {
		t.Fatal("expected recipient update to fail")
	}
	if err := svc.DeleteUserDocument(ctx, "user-b", "doc-1"); err == nil {
		t.Fatal("expected recipient delete to fail")
	}
	// ...nor re-share it onward.
	if _, err := svc.ShareUserDocumentByEmail(ctx, "user-b", "b@example.com", "doc-1", "a@example.com"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected recipient re-share to fail, got %v", err)
	}

	// Revoke is owner-only and actually revokes.
	if err := svc.RevokeUserDocShare(ctx, "user-b", "doc-1", "user-b"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected non-owner revoke to fail, got %v", err)
	}
	if err := svc.RevokeUserDocShare(ctx, "user-a", "doc-1", "user-b"); err != nil {
		t.Fatalf("revoke failed: %v", err)
	}
	if _, err := svc.GetUserDocument(ctx, "user-b", "doc-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound after revoke, got %v", err)
	}
}

func TestListUserDocuments_SkipsDeletedSharedDoc(t *testing.T) {
	ctx := context.Background()
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	// A share row pointing at a doc the owner has since deleted (no cascade)
	// must be skipped, not fail the whole listing.
	repo.docShares[docShareKey("user-b", "gone")] = &model.Share{
		MeetingID: "gone", OwnerID: "user-a", OwnerEmail: "a@example.com",
		SharedToID: "user-b", Permission: model.PermissionRead,
		EntityType: model.EntityTypeDocShare,
	}
	list, err := svc.ListUserDocuments(ctx, "user-b", "")
	if err != nil {
		t.Fatalf("expected dangling share to be skipped, got error: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("expected empty list, got %v", list)
	}
}

func TestListUserDocShares(t *testing.T) {
	ctx := context.Background()
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	repo.users["b@example.com"] = &model.User{UserID: "user-b", Email: "b@example.com"}
	seedUserDoc(repo, "user-a", "doc-1", "노트")
	if _, err := svc.ShareUserDocumentByEmail(ctx, "user-a", "a@example.com", "doc-1", "b@example.com"); err != nil {
		t.Fatalf("share failed: %v", err)
	}
	if _, err := svc.ListUserDocShares(ctx, "user-b", "doc-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected non-owner list to fail, got %v", err)
	}
	shares, err := svc.ListUserDocShares(ctx, "user-a", "doc-1")
	if err != nil {
		t.Fatalf("list shares failed: %v", err)
	}
	if len(shares) != 1 || shares[0].Email != "b@example.com" || shares[0].Permission != model.PermissionRead {
		t.Fatalf("unexpected shares: %+v", shares)
	}
}
