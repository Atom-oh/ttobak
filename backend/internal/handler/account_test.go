package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/ttobak/backend/internal/middleware"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
	"github.com/ttobak/backend/internal/service"
)

// withUserEmailCtx injects both userID and email into the request context.
func withUserEmailCtx(r *http.Request, userID, email string) *http.Request {
	ctx := context.WithValue(r.Context(), middleware.UserIDKey, userID)
	ctx = context.WithValue(ctx, middleware.UserEmailKey, email)
	return r.WithContext(ctx)
}

// mockHandlerAccountRepo implements service.AccountRepo for handler tests.
type mockHandlerAccountRepo struct {
	accounts          map[string]*model.Account
	members           map[string]*model.AccountMember
	users             map[string]*model.User
	meetingRefs       map[string][]model.MeetingRef
	insightsByAccount map[string][]model.AccountInsight
	documents         map[string][]model.AccountDocument
	shares            map[string]*model.Share   // "sharedToID|meetingID" -> share
	meetings          map[string]*model.Meeting // meetingID -> meeting
	shareOpErr        map[string]error          // meetingID -> forced GetShare/DeleteShare error
	publicShares      map[string]*model.PublicShare
	docShares         map[string]*model.Share // "sharedToID|docID" -> per-user doc share
}

func newMockHandlerAccountRepo() *mockHandlerAccountRepo {
	return &mockHandlerAccountRepo{
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

func handlerDocShareKey(sharedToID, docID string) string { return sharedToID + "|" + docID }

func (m *mockHandlerAccountRepo) CreateDocShare(_ context.Context, docID, ownerID, ownerEmail, sharedToID, email string) (*model.Share, error) {
	sh := &model.Share{
		MeetingID: docID, OwnerID: ownerID, OwnerEmail: ownerEmail,
		SharedToID: sharedToID, Email: email,
		Permission: model.PermissionRead, EntityType: model.EntityTypeDocShare,
	}
	m.docShares[handlerDocShareKey(sharedToID, docID)] = sh
	return sh, nil
}

func (m *mockHandlerAccountRepo) GetDocShare(_ context.Context, sharedToID, docID string) (*model.Share, error) {
	sh, ok := m.docShares[handlerDocShareKey(sharedToID, docID)]
	if !ok {
		return nil, nil
	}
	c := *sh
	return &c, nil
}

func (m *mockHandlerAccountRepo) DeleteDocShare(_ context.Context, sharedToID, docID string) error {
	delete(m.docShares, handlerDocShareKey(sharedToID, docID))
	return nil
}

func (m *mockHandlerAccountRepo) PutPendingShare(_ context.Context, _ *model.PendingShare) error {
	return nil
}

func (m *mockHandlerAccountRepo) GetPendingShare(_ context.Context, _, _ string) (*model.PendingShare, error) {
	return nil, nil
}

func (m *mockHandlerAccountRepo) DeletePendingShare(_ context.Context, _, _ string) (bool, error) {
	return true, nil
}

func (m *mockHandlerAccountRepo) ListDocSharesForUser(_ context.Context, userID string) ([]model.Share, error) {
	var out []model.Share
	for _, sh := range m.docShares {
		if sh.SharedToID == userID {
			out = append(out, *sh)
		}
	}
	return out, nil
}

func (m *mockHandlerAccountRepo) ListDocSharesForDoc(_ context.Context, docID string) ([]model.Share, error) {
	var out []model.Share
	for _, sh := range m.docShares {
		if sh.MeetingID == docID {
			out = append(out, *sh)
		}
	}
	return out, nil
}

func (m *mockHandlerAccountRepo) GetMeetingByID(_ context.Context, meetingID string) (*model.Meeting, error) {
	mtg, ok := m.meetings[meetingID]
	if !ok {
		return nil, nil
	}
	c := *mtg
	return &c, nil
}

func acctMemberKey(accountID, userID string) string { return accountID + "|" + userID }

func (m *mockHandlerAccountRepo) CreateAccount(_ context.Context, a *model.Account, owner *model.AccountMember) error {
	ac := *a
	m.accounts[a.AccountID] = &ac
	oc := *owner
	m.members[acctMemberKey(owner.AccountID, owner.UserID)] = &oc
	return nil
}
func (m *mockHandlerAccountRepo) GetAccount(_ context.Context, id string) (*model.Account, error) {
	a, ok := m.accounts[id]
	if !ok {
		return nil, nil
	}
	c := *a
	return &c, nil
}
func (m *mockHandlerAccountRepo) GetMember(_ context.Context, accountID, userID string) (*model.AccountMember, error) {
	v, ok := m.members[acctMemberKey(accountID, userID)]
	if !ok {
		return nil, nil
	}
	c := *v
	return &c, nil
}
func (m *mockHandlerAccountRepo) PutMember(_ context.Context, member *model.AccountMember) error {
	c := *member
	m.members[acctMemberKey(member.AccountID, member.UserID)] = &c
	return nil
}
func (m *mockHandlerAccountRepo) DeleteMember(_ context.Context, accountID, userID string) error {
	key := acctMemberKey(accountID, userID)
	if _, ok := m.members[key]; !ok {
		return fmt.Errorf("%w: member %s not found", repository.ErrConditionFailed, userID)
	}
	delete(m.members, key)
	return nil
}
func (m *mockHandlerAccountRepo) UpdateMemberRole(_ context.Context, accountID, userID, role string) error {
	key := acctMemberKey(accountID, userID)
	member, ok := m.members[key]
	if !ok {
		return fmt.Errorf("%w: member %s not found", repository.ErrConditionFailed, userID)
	}
	member.Role = role
	return nil
}
func (m *mockHandlerAccountRepo) GetShare(_ context.Context, sharedToID, meetingID string) (*model.Share, error) {
	if err, ok := m.shareOpErr[meetingID]; ok {
		return nil, err
	}
	sh, ok := m.shares[sharedToID+"|"+meetingID]
	if !ok {
		return nil, nil
	}
	cp := *sh
	return &cp, nil
}
func (m *mockHandlerAccountRepo) DeleteShareIfAccountOrigin(_ context.Context, accountID, sharedToID, meetingID string) error {
	if err, ok := m.shareOpErr[meetingID]; ok {
		return err
	}
	key := sharedToID + "|" + meetingID
	existing, ok := m.shares[key]
	if !ok || existing.Origin != model.ShareOriginAccount || existing.AccountID != accountID {
		return fmt.Errorf("%w: share %s not account-origin for account %s", repository.ErrConditionFailed, key, accountID)
	}
	delete(m.shares, key)
	return nil
}
func (m *mockHandlerAccountRepo) ListAccountMembers(_ context.Context, accountID string) ([]model.AccountMember, error) {
	out := []model.AccountMember{}
	for _, v := range m.members {
		if v.AccountID == accountID {
			out = append(out, *v)
		}
	}
	return out, nil
}
func (m *mockHandlerAccountRepo) ListAccountsForUser(_ context.Context, userID string) ([]model.AccountMember, error) {
	out := []model.AccountMember{}
	for _, v := range m.members {
		if v.UserID == userID {
			out = append(out, *v)
		}
	}
	return out, nil
}
func (m *mockHandlerAccountRepo) GetUserByEmail(_ context.Context, email string) (*model.User, error) {
	u, ok := m.users[email]
	if !ok {
		return nil, nil
	}
	c := *u
	return &c, nil
}
func (m *mockHandlerAccountRepo) ListMeetingRefsForAccount(_ context.Context, accountID string) ([]model.MeetingRef, error) {
	return append([]model.MeetingRef(nil), m.meetingRefs[accountID]...), nil
}
func (m *mockHandlerAccountRepo) ListInsightsForAccount(_ context.Context, accountID string) ([]model.AccountInsight, error) {
	return append([]model.AccountInsight(nil), m.insightsByAccount[accountID]...), nil
}
func (m *mockHandlerAccountRepo) UpdateAccountDocumentFields(_ context.Context, pk, docID string, fields map[string]interface{}, removeFields []string) (map[string]string, error) {
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

func (m *mockHandlerAccountRepo) PutAccountDocument(_ context.Context, doc *model.AccountDocument) error {
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
func (m *mockHandlerAccountRepo) ListAccountDocuments(_ context.Context, pk string) ([]model.AccountDocument, error) {
	return append([]model.AccountDocument(nil), m.documents[pk]...), nil
}
func (m *mockHandlerAccountRepo) GetAccountDocument(_ context.Context, pk, docID string) (*model.AccountDocument, error) {
	for _, d := range m.documents[pk] {
		if d.DocID == docID {
			cp := d
			return &cp, nil
		}
	}
	return nil, nil
}
func (m *mockHandlerAccountRepo) DeleteAccountDocument(_ context.Context, pk, docID string) error {
	docs := m.documents[pk]
	for i, d := range docs {
		if d.DocID == docID {
			m.documents[pk] = append(docs[:i], docs[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("%w: doc %s not found", repository.ErrConditionFailed, docID)
}
func (m *mockHandlerAccountRepo) PutPublicShare(_ context.Context, share *model.PublicShare) error {
	cp := *share
	m.publicShares[share.Token] = &cp
	return nil
}
func (m *mockHandlerAccountRepo) GetPublicShare(_ context.Context, token string) (*model.PublicShare, error) {
	s, ok := m.publicShares[token]
	if !ok {
		return nil, nil
	}
	cp := *s
	return &cp, nil
}
func (m *mockHandlerAccountRepo) DeletePublicShare(_ context.Context, token string) error {
	delete(m.publicShares, token)
	return nil
}
func (m *mockHandlerAccountRepo) SetPublicShareTokenIfAbsent(_ context.Context, pk, docID, token string) error {
	docs := m.documents[pk]
	for i, d := range docs {
		if d.DocID == docID {
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

func (m *mockHandlerAccountRepo) ClearPublicShareTokenIfMatches(_ context.Context, pk, docID, expectedToken string) error {
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

func newStubAccountHandler() (*AccountHandler, *mockHandlerAccountRepo) {
	repo := newMockHandlerAccountRepo()
	svc := service.NewAccountServiceForTest(repo)
	return &AccountHandler{accountService: svc}, repo
}

func mdPtr(s string) *string { return &s }

func TestHandlerCreateAccount_Created(t *testing.T) {
	h, repo := newStubAccountHandler()
	body, _ := json.Marshal(model.CreateAccountRequest{Name: "하나은행"})
	r := httptest.NewRequest(http.MethodPost, "/api/accounts", bytes.NewReader(body))
	r = withUserEmailCtx(r, "owner-1", "o@x.com")
	w := httptest.NewRecorder()

	h.CreateAccount(w, r)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d (%s)", w.Code, w.Body.String())
	}
	var resp model.AccountResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.AccountID == "" || resp.OwnerUserID != "owner-1" {
		t.Errorf("unexpected response: %+v", resp)
	}
	if _, ok := repo.accounts[resp.AccountID]; !ok {
		t.Error("account not persisted")
	}
}

func TestHandlerGetAccount_Forbidden(t *testing.T) {
	h, repo := newStubAccountHandler()
	// seed an account owned by someone else
	repo.accounts["acc-1"] = &model.Account{AccountID: "acc-1", Name: "하나은행", OwnerUserID: "owner-1"}
	repo.members[acctMemberKey("acc-1", "owner-1")] = &model.AccountMember{AccountID: "acc-1", UserID: "owner-1", Role: model.RoleOwner}

	r := httptest.NewRequest(http.MethodGet, "/api/accounts/acc-1", nil)
	r = withUserEmailCtx(r, "stranger-9", "s@x.com")
	r = withChiParam(r, "accountId", "acc-1")
	w := httptest.NewRecorder()

	h.GetAccount(w, r)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d (%s)", w.Code, w.Body.String())
	}
}

func TestHandlerListAccountMeetings_Forbidden(t *testing.T) {
	h, repo := newStubAccountHandler()
	repo.accounts["acc-1"] = &model.Account{AccountID: "acc-1", Name: "하나은행", OwnerUserID: "owner-1"}
	repo.members[acctMemberKey("acc-1", "owner-1")] = &model.AccountMember{AccountID: "acc-1", UserID: "owner-1", Role: model.RoleOwner}

	r := httptest.NewRequest(http.MethodGet, "/api/accounts/acc-1/meetings", nil)
	r = withUserEmailCtx(r, "stranger-9", "s@x.com")
	r = withChiParam(r, "accountId", "acc-1")
	w := httptest.NewRecorder()

	h.ListAccountMeetings(w, r)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d (%s)", w.Code, w.Body.String())
	}
}

func TestHandlerRemoveMember_NoContent(t *testing.T) {
	h, repo := newStubAccountHandler()
	repo.accounts["acc-1"] = &model.Account{AccountID: "acc-1", Name: "하나은행", OwnerUserID: "owner-1"}
	repo.members[acctMemberKey("acc-1", "owner-1")] = &model.AccountMember{AccountID: "acc-1", UserID: "owner-1", Role: model.RoleOwner}
	repo.members[acctMemberKey("acc-1", "tam-1")] = &model.AccountMember{AccountID: "acc-1", UserID: "tam-1", Role: model.RoleTAM}

	r := httptest.NewRequest(http.MethodDelete, "/api/accounts/acc-1/members/tam-1", nil)
	r = withUserEmailCtx(r, "owner-1", "o@x.com")
	r = withChiParam(r, "accountId", "acc-1")
	r = withChiParam(r, "userId", "tam-1")
	w := httptest.NewRecorder()

	h.RemoveMember(w, r)

	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d (%s)", w.Code, w.Body.String())
	}
	if _, ok := repo.members[acctMemberKey("acc-1", "tam-1")]; ok {
		t.Error("member not removed")
	}
}

func TestHandlerRemoveMember_OwnerTargetBadRequest(t *testing.T) {
	h, repo := newStubAccountHandler()
	repo.accounts["acc-1"] = &model.Account{AccountID: "acc-1", Name: "하나은행", OwnerUserID: "owner-1"}
	repo.members[acctMemberKey("acc-1", "owner-1")] = &model.AccountMember{AccountID: "acc-1", UserID: "owner-1", Role: model.RoleOwner}

	r := httptest.NewRequest(http.MethodDelete, "/api/accounts/acc-1/members/owner-1", nil)
	r = withUserEmailCtx(r, "owner-1", "o@x.com")
	r = withChiParam(r, "accountId", "acc-1")
	r = withChiParam(r, "userId", "owner-1")
	w := httptest.NewRecorder()

	h.RemoveMember(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d (%s)", w.Code, w.Body.String())
	}
}

func TestHandlerRemoveMember_PartialCleanupFailureReturns200WithBody(t *testing.T) {
	h, repo := newStubAccountHandler()
	repo.accounts["acc-1"] = &model.Account{AccountID: "acc-1", Name: "하나은행", OwnerUserID: "owner-1"}
	repo.members[acctMemberKey("acc-1", "owner-1")] = &model.AccountMember{AccountID: "acc-1", UserID: "owner-1", Role: model.RoleOwner}
	repo.members[acctMemberKey("acc-1", "tam-1")] = &model.AccountMember{AccountID: "acc-1", UserID: "tam-1", Role: model.RoleTAM}
	repo.meetingRefs["acc-1"] = []model.MeetingRef{{AccountID: "acc-1", MeetingID: "m-1"}}
	repo.meetings["m-1"] = &model.Meeting{MeetingID: "m-1", AccountID: "acc-1", SharedToAccount: true}
	repo.shareOpErr["m-1"] = fmt.Errorf("simulated transient error")

	// force=true: without it the precheck now fails closed on this same
	// transient error before membership is even deleted (see
	// TestHandlerRemoveMember_PrecheckFailsClosedOnTransientError) -- this
	// test targets the post-delete cleanup loop's own soft-fail handling.
	r := httptest.NewRequest(http.MethodDelete, "/api/accounts/acc-1/members/tam-1?force=true", nil)
	r = withUserEmailCtx(r, "owner-1", "o@x.com")
	r = withChiParam(r, "accountId", "acc-1")
	r = withChiParam(r, "userId", "tam-1")
	w := httptest.NewRecorder()

	h.RemoveMember(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	failed, _ := resp["cleanupFailedForMeetings"].([]interface{})
	if len(failed) != 1 || failed[0] != "m-1" {
		t.Errorf("expected cleanupFailedForMeetings=[m-1], got %+v", resp)
	}
	if _, ok := repo.members[acctMemberKey("acc-1", "tam-1")]; ok {
		t.Error("member should still be removed despite cleanup failure")
	}
}

func TestHandlerRemoveMember_AmbiguousShareBlockedWithoutForce(t *testing.T) {
	h, repo := newStubAccountHandler()
	repo.accounts["acc-1"] = &model.Account{AccountID: "acc-1", Name: "하나은행", OwnerUserID: "owner-1"}
	repo.members[acctMemberKey("acc-1", "owner-1")] = &model.AccountMember{AccountID: "acc-1", UserID: "owner-1", Role: model.RoleOwner}
	repo.members[acctMemberKey("acc-1", "tam-1")] = &model.AccountMember{AccountID: "acc-1", UserID: "tam-1", Role: model.RoleTAM}
	repo.meetingRefs["acc-1"] = []model.MeetingRef{{AccountID: "acc-1", MeetingID: "m-1"}}
	repo.meetings["m-1"] = &model.Meeting{MeetingID: "m-1", AccountID: "acc-1", SharedToAccount: true}
	repo.shares["tam-1|m-1"] = &model.Share{
		MeetingID: "m-1", SharedToID: "tam-1", Permission: model.PermissionRead, Origin: "", // ambiguous shape
	}

	r := httptest.NewRequest(http.MethodDelete, "/api/accounts/acc-1/members/tam-1", nil)
	r = withUserEmailCtx(r, "owner-1", "o@x.com")
	r = withChiParam(r, "accountId", "acc-1")
	r = withChiParam(r, "userId", "tam-1")
	w := httptest.NewRecorder()

	h.RemoveMember(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d (%s)", w.Code, w.Body.String())
	}
	if _, ok := repo.members[acctMemberKey("acc-1", "tam-1")]; !ok {
		t.Error("expected membership to be preserved (untouched) when removal is blocked, but member was removed")
	}
}

func TestHandlerRemoveMember_ForceAmbiguousShareReturns200WithBody(t *testing.T) {
	h, repo := newStubAccountHandler()
	repo.accounts["acc-1"] = &model.Account{AccountID: "acc-1", Name: "하나은행", OwnerUserID: "owner-1"}
	repo.members[acctMemberKey("acc-1", "owner-1")] = &model.AccountMember{AccountID: "acc-1", UserID: "owner-1", Role: model.RoleOwner}
	repo.members[acctMemberKey("acc-1", "tam-1")] = &model.AccountMember{AccountID: "acc-1", UserID: "tam-1", Role: model.RoleTAM}
	repo.meetingRefs["acc-1"] = []model.MeetingRef{{AccountID: "acc-1", MeetingID: "m-1"}}
	repo.meetings["m-1"] = &model.Meeting{MeetingID: "m-1", AccountID: "acc-1", SharedToAccount: true}
	repo.shares["tam-1|m-1"] = &model.Share{
		MeetingID: "m-1", SharedToID: "tam-1", Permission: model.PermissionRead, Origin: "", // ambiguous shape
	}

	r := httptest.NewRequest(http.MethodDelete, "/api/accounts/acc-1/members/tam-1?force=true", nil)
	r = withUserEmailCtx(r, "owner-1", "o@x.com")
	r = withChiParam(r, "accountId", "acc-1")
	r = withChiParam(r, "userId", "tam-1")
	w := httptest.NewRecorder()

	h.RemoveMember(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	failed, ok := resp["cleanupFailedForMeetings"].([]interface{})
	if !ok || len(failed) != 0 {
		t.Errorf("expected cleanupFailedForMeetings=[] (present, empty), got %+v", resp)
	}
	ambiguous, _ := resp["ambiguousUntaggedMeetingIDs"].([]interface{})
	if len(ambiguous) != 1 || ambiguous[0] != "m-1" {
		t.Errorf("expected ambiguousUntaggedMeetingIDs=[m-1], got %+v", resp)
	}
}

func TestHandlerRemoveMember_PrecheckFailsClosedOnTransientError(t *testing.T) {
	h, repo := newStubAccountHandler()
	repo.accounts["acc-1"] = &model.Account{AccountID: "acc-1", Name: "하나은행", OwnerUserID: "owner-1"}
	repo.members[acctMemberKey("acc-1", "owner-1")] = &model.AccountMember{AccountID: "acc-1", UserID: "owner-1", Role: model.RoleOwner}
	repo.members[acctMemberKey("acc-1", "tam-1")] = &model.AccountMember{AccountID: "acc-1", UserID: "tam-1", Role: model.RoleTAM}
	repo.meetingRefs["acc-1"] = []model.MeetingRef{{AccountID: "acc-1", MeetingID: "m-1"}}
	repo.meetings["m-1"] = &model.Meeting{MeetingID: "m-1", AccountID: "acc-1", SharedToAccount: true}
	repo.shareOpErr["m-1"] = fmt.Errorf("simulated transient error")

	r := httptest.NewRequest(http.MethodDelete, "/api/accounts/acc-1/members/tam-1", nil)
	r = withUserEmailCtx(r, "owner-1", "o@x.com")
	r = withChiParam(r, "accountId", "acc-1")
	r = withChiParam(r, "userId", "tam-1")
	w := httptest.NewRecorder()

	h.RemoveMember(w, r)

	if w.Code == http.StatusOK {
		t.Fatalf("expected a non-2xx status when the ambiguous-share precheck fails closed on a transient error, got 200 (%s)", w.Body.String())
	}
	if _, ok := repo.members[acctMemberKey("acc-1", "tam-1")]; !ok {
		t.Error("expected membership to be preserved when the precheck fails closed on a transient error")
	}
}

func TestHandlerRemoveMember_LinkOnlyMeetingShareDoesNotBlock(t *testing.T) {
	h, repo := newStubAccountHandler()
	repo.accounts["acc-1"] = &model.Account{AccountID: "acc-1", Name: "하나은행", OwnerUserID: "owner-1"}
	repo.members[acctMemberKey("acc-1", "owner-1")] = &model.AccountMember{AccountID: "acc-1", UserID: "owner-1", Role: model.RoleOwner}
	repo.members[acctMemberKey("acc-1", "tam-1")] = &model.AccountMember{AccountID: "acc-1", UserID: "tam-1", Role: model.RoleTAM}
	repo.meetingRefs["acc-1"] = []model.MeetingRef{{AccountID: "acc-1", MeetingID: "m-1"}}
	// Link-only: AccountID set but SharedToAccount false -- never a team
	// grant, so tam-1's direct share here must not block removal.
	repo.meetings["m-1"] = &model.Meeting{MeetingID: "m-1", AccountID: "acc-1", SharedToAccount: false}
	repo.shares["tam-1|m-1"] = &model.Share{
		MeetingID: "m-1", SharedToID: "tam-1", Permission: model.PermissionRead, Origin: "",
	}

	r := httptest.NewRequest(http.MethodDelete, "/api/accounts/acc-1/members/tam-1", nil)
	r = withUserEmailCtx(r, "owner-1", "o@x.com")
	r = withChiParam(r, "accountId", "acc-1")
	r = withChiParam(r, "userId", "tam-1")
	w := httptest.NewRecorder()

	h.RemoveMember(w, r)

	// 204: the Link-only share is neither a cleanup failure nor an ambiguous
	// untagged share (it's outside the account-membership grant entirely),
	// so this is the fully-clean case -- it must not be blocked with a 400.
	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204 (Link-only share must not block removal), got %d (%s)", w.Code, w.Body.String())
	}
	if repo.shares["tam-1|m-1"] == nil {
		t.Error("expected Link-only meeting's share to be left untouched")
	}
}

func TestHandlerUpdateMemberRole_OK(t *testing.T) {
	h, repo := newStubAccountHandler()
	repo.accounts["acc-1"] = &model.Account{AccountID: "acc-1", Name: "하나은행", OwnerUserID: "owner-1"}
	repo.members[acctMemberKey("acc-1", "owner-1")] = &model.AccountMember{AccountID: "acc-1", UserID: "owner-1", Role: model.RoleOwner}
	repo.members[acctMemberKey("acc-1", "tam-1")] = &model.AccountMember{AccountID: "acc-1", UserID: "tam-1", Email: "tam@x.com", Role: model.RoleTAM}

	body, _ := json.Marshal(model.UpdateMemberRequest{Role: model.RoleSSA})
	r := httptest.NewRequest(http.MethodPut, "/api/accounts/acc-1/members/tam-1", bytes.NewReader(body))
	r = withUserEmailCtx(r, "owner-1", "o@x.com")
	r = withChiParam(r, "accountId", "acc-1")
	r = withChiParam(r, "userId", "tam-1")
	w := httptest.NewRecorder()

	h.UpdateMemberRole(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", w.Code, w.Body.String())
	}
	var dto model.AccountMemberDTO
	json.Unmarshal(w.Body.Bytes(), &dto)
	if dto.Role != model.RoleSSA {
		t.Errorf("unexpected dto: %+v", dto)
	}
}

// TestHandlerUpdateMemberRole_NonOwnerMemberAllowed pins ADR-034: a
// non-owner member changing another member's role is no longer forbidden.
func TestHandlerUpdateMemberRole_NonOwnerMemberAllowed(t *testing.T) {
	h, repo := newStubAccountHandler()
	repo.accounts["acc-1"] = &model.Account{AccountID: "acc-1", Name: "하나은행", OwnerUserID: "owner-1"}
	repo.members[acctMemberKey("acc-1", "owner-1")] = &model.AccountMember{AccountID: "acc-1", UserID: "owner-1", Role: model.RoleOwner}
	repo.members[acctMemberKey("acc-1", "tam-1")] = &model.AccountMember{AccountID: "acc-1", UserID: "tam-1", Role: model.RoleTAM}

	body, _ := json.Marshal(model.UpdateMemberRequest{Role: model.RoleSSA})
	r := httptest.NewRequest(http.MethodPut, "/api/accounts/acc-1/members/tam-1", bytes.NewReader(body))
	r = withUserEmailCtx(r, "tam-1", "tam@x.com")
	r = withChiParam(r, "accountId", "acc-1")
	r = withChiParam(r, "userId", "tam-1")
	w := httptest.NewRecorder()

	h.UpdateMemberRole(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", w.Code, w.Body.String())
	}
}

// TestHandlerUpdateMemberRole_NonMemberForbidden: a caller with no
// membership on the account at all is still forbidden.
func TestHandlerUpdateMemberRole_NonMemberForbidden(t *testing.T) {
	h, repo := newStubAccountHandler()
	repo.accounts["acc-1"] = &model.Account{AccountID: "acc-1", Name: "하나은행", OwnerUserID: "owner-1"}
	repo.members[acctMemberKey("acc-1", "owner-1")] = &model.AccountMember{AccountID: "acc-1", UserID: "owner-1", Role: model.RoleOwner}
	repo.members[acctMemberKey("acc-1", "tam-1")] = &model.AccountMember{AccountID: "acc-1", UserID: "tam-1", Role: model.RoleTAM}

	body, _ := json.Marshal(model.UpdateMemberRequest{Role: model.RoleSSA})
	r := httptest.NewRequest(http.MethodPut, "/api/accounts/acc-1/members/tam-1", bytes.NewReader(body))
	r = withUserEmailCtx(r, "stranger-1", "stranger@x.com")
	r = withChiParam(r, "accountId", "acc-1")
	r = withChiParam(r, "userId", "tam-1")
	w := httptest.NewRecorder()

	h.UpdateMemberRole(w, r)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d (%s)", w.Code, w.Body.String())
	}
}

var _ = chi.URLParam // ensure chi import is used if helpers are trimmed

func TestHandlerListAccountInsights_Forbidden(t *testing.T) {
	h, repo := newStubAccountHandler()
	repo.accounts["acc-1"] = &model.Account{AccountID: "acc-1", Name: "하나은행", OwnerUserID: "owner-1"}
	repo.members[acctMemberKey("acc-1", "owner-1")] = &model.AccountMember{AccountID: "acc-1", UserID: "owner-1", Role: model.RoleOwner}

	r := httptest.NewRequest(http.MethodGet, "/api/accounts/acc-1/insights", nil)
	r = withUserEmailCtx(r, "stranger-9", "s@x.com")
	r = withChiParam(r, "accountId", "acc-1")
	w := httptest.NewRecorder()

	h.ListAccountInsights(w, r)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d (%s)", w.Code, w.Body.String())
	}
}

func TestHandlerGetAccountBrief_Forbidden(t *testing.T) {
	h, repo := newStubAccountHandler()
	repo.accounts["acc-1"] = &model.Account{AccountID: "acc-1", Name: "하나은행", OwnerUserID: "owner-1"}
	repo.members[acctMemberKey("acc-1", "owner-1")] = &model.AccountMember{AccountID: "acc-1", UserID: "owner-1", Role: model.RoleOwner}

	r := httptest.NewRequest(http.MethodGet, "/api/accounts/acc-1/brief", nil)
	r = withUserEmailCtx(r, "stranger-9", "s@x.com")
	r = withChiParam(r, "accountId", "acc-1")
	w := httptest.NewRecorder()

	h.GetAccountBrief(w, r)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d (%s)", w.Code, w.Body.String())
	}
}

func TestHandlerGetAccountBrief_InvalidType(t *testing.T) {
	h, repo := newStubAccountHandler()
	repo.accounts["acc-1"] = &model.Account{AccountID: "acc-1", Name: "하나은행", OwnerUserID: "owner-1"}
	repo.members[acctMemberKey("acc-1", "owner-1")] = &model.AccountMember{AccountID: "acc-1", UserID: "owner-1", Role: model.RoleOwner}

	// Invalid type must 400 (matching ListAccountInsights), not silently filter to empty.
	r := httptest.NewRequest(http.MethodGet, "/api/accounts/acc-1/brief?types=bogus", nil)
	r = withUserEmailCtx(r, "owner-1", "o@x.com")
	r = withChiParam(r, "accountId", "acc-1")
	w := httptest.NewRecorder()

	h.GetAccountBrief(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid type, got %d (%s)", w.Code, w.Body.String())
	}
}

func TestHandlerPutDocument_Forbidden(t *testing.T) {
	h, repo := newStubAccountHandler()
	repo.accounts["acc-1"] = &model.Account{AccountID: "acc-1", Name: "하나은행", OwnerUserID: "owner-1"}
	repo.members[acctMemberKey("acc-1", "owner-1")] = &model.AccountMember{AccountID: "acc-1", UserID: "owner-1", Role: model.RoleOwner}
	body, _ := json.Marshal(model.PutDocumentRequest{Title: "t", Markdown: mdPtr("x")})
	r := httptest.NewRequest(http.MethodPost, "/api/accounts/acc-1/documents", bytes.NewReader(body))
	r = withUserEmailCtx(r, "stranger-9", "s@x.com")
	r = withChiParam(r, "accountId", "acc-1")
	w := httptest.NewRecorder()
	h.PutDocument(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d (%s)", w.Code, w.Body.String())
	}
}

func seedDocForUpdateDeleteTests(t *testing.T, repo *mockHandlerAccountRepo, h *AccountHandler) string {
	t.Helper()
	repo.accounts["acc-1"] = &model.Account{AccountID: "acc-1", Name: "하나은행", OwnerUserID: "owner-1"}
	repo.members[acctMemberKey("acc-1", "owner-1")] = &model.AccountMember{AccountID: "acc-1", UserID: "owner-1", Role: model.RoleOwner}
	body, _ := json.Marshal(model.PutDocumentRequest{Title: "Note", Markdown: mdPtr("body")})
	r := httptest.NewRequest(http.MethodPost, "/api/accounts/acc-1/documents", bytes.NewReader(body))
	r = withUserEmailCtx(r, "owner-1", "o@x.com")
	r = withChiParam(r, "accountId", "acc-1")
	w := httptest.NewRecorder()
	h.PutDocument(w, r)
	var dto model.AccountDocumentDTO
	json.Unmarshal(w.Body.Bytes(), &dto)
	return dto.DocID
}

func TestHandlerUpdateDocument_OK(t *testing.T) {
	h, repo := newStubAccountHandler()
	docID := seedDocForUpdateDeleteTests(t, repo, h)

	body, _ := json.Marshal(model.PutDocumentRequest{Title: "Note v2", Markdown: mdPtr("updated body")})
	r := httptest.NewRequest(http.MethodPut, "/api/accounts/acc-1/documents/"+docID, bytes.NewReader(body))
	r = withUserEmailCtx(r, "owner-1", "o@x.com")
	r = withChiParam(r, "accountId", "acc-1")
	r = withChiParam(r, "docId", docID)
	w := httptest.NewRecorder()

	h.UpdateDocument(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", w.Code, w.Body.String())
	}
	var dto model.AccountDocumentDTO
	json.Unmarshal(w.Body.Bytes(), &dto)
	if dto.Title != "Note v2" {
		t.Errorf("unexpected dto: %+v", dto)
	}
}

func TestHandlerUpdateDocument_Forbidden(t *testing.T) {
	h, repo := newStubAccountHandler()
	docID := seedDocForUpdateDeleteTests(t, repo, h)

	body, _ := json.Marshal(model.PutDocumentRequest{Title: "Note v2", Markdown: mdPtr("updated body")})
	r := httptest.NewRequest(http.MethodPut, "/api/accounts/acc-1/documents/"+docID, bytes.NewReader(body))
	r = withUserEmailCtx(r, "stranger-9", "s@x.com")
	r = withChiParam(r, "accountId", "acc-1")
	r = withChiParam(r, "docId", docID)
	w := httptest.NewRecorder()

	h.UpdateDocument(w, r)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d (%s)", w.Code, w.Body.String())
	}
}

func TestHandlerDeleteDocument_OK(t *testing.T) {
	h, repo := newStubAccountHandler()
	docID := seedDocForUpdateDeleteTests(t, repo, h)

	r := httptest.NewRequest(http.MethodDelete, "/api/accounts/acc-1/documents/"+docID, nil)
	r = withUserEmailCtx(r, "owner-1", "o@x.com")
	r = withChiParam(r, "accountId", "acc-1")
	r = withChiParam(r, "docId", docID)
	w := httptest.NewRecorder()

	h.DeleteDocument(w, r)

	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d (%s)", w.Code, w.Body.String())
	}
}

func TestHandlerDeleteDocument_Forbidden(t *testing.T) {
	h, repo := newStubAccountHandler()
	docID := seedDocForUpdateDeleteTests(t, repo, h)

	r := httptest.NewRequest(http.MethodDelete, "/api/accounts/acc-1/documents/"+docID, nil)
	r = withUserEmailCtx(r, "stranger-9", "s@x.com")
	r = withChiParam(r, "accountId", "acc-1")
	r = withChiParam(r, "docId", docID)
	w := httptest.NewRecorder()

	h.DeleteDocument(w, r)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d (%s)", w.Code, w.Body.String())
	}
}
