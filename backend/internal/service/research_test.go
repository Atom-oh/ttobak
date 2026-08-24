package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

// newTestResearchService wires repo/mainRepo like newResearchServiceWithRepo,
// plus cross-links the two mocks so LinkAccountTransactional/UnlinkAccountTransactional
// can simulate a real TransactWriteItems call spanning both -- in production
// both writes land in the same DynamoDB table (just different PK/SK) via one
// client, but these mocks split research's own accountIds set and the
// RESEARCHREF# item across two separate structs.
func newTestResearchService(repo *mockResearchRepo, mainRepo *mockResearchMainRepo) *ResearchService {
	repo.mainRepo = mainRepo
	return newResearchServiceWithRepo(repo, mainRepo)
}

// mockResearchRepo implements researchRepo with an in-memory map.
type mockResearchRepo struct {
	byID   map[string]*model.Research
	shares map[string]*model.Share // sharedToID|researchID
	// deleteAfterGet, if set, simulates a concurrent delete landing right
	// after the next GetResearch read for this id: the entry is removed
	// from byID immediately after the read returns its copy.
	deleteAfterGet string
	// mainRepo lets LinkAccountTransactional/UnlinkAccountTransactional
	// simulate a real TransactWriteItems call, which spans both the
	// research's own accountIds set AND the RESEARCHREF# item that in
	// production lives in the same DynamoDB table (just a different
	// PK/SK) but in these mocks lives in a separate struct. Wired up by
	// newResearchServiceWithRepo.
	mainRepo *mockResearchMainRepo
}

func newMockResearchRepo() *mockResearchRepo {
	return &mockResearchRepo{
		byID:   make(map[string]*model.Research),
		shares: make(map[string]*model.Share),
	}
}

func (m *mockResearchRepo) CreateResearch(_ context.Context, r *model.Research) error {
	cp := *r
	m.byID[r.ResearchID] = &cp
	return nil
}

func (m *mockResearchRepo) GetResearch(_ context.Context, id string) (*model.Research, error) {
	r, ok := m.byID[id]
	if !ok {
		return nil, nil
	}
	cp := *r
	if m.deleteAfterGet == id {
		delete(m.byID, id)
		m.deleteAfterGet = ""
	}
	return &cp, nil
}

func (m *mockResearchRepo) UpdateResearchFieldsConditional(_ context.Context, id string, fields map[string]interface{}, expectedStatus string) error {
	r, ok := m.byID[id]
	if !ok {
		return errors.New("not found")
	}
	if r.Status != expectedStatus {
		return ErrStatusMismatch
	}
	return m.applyFields(id, fields)
}

func (m *mockResearchRepo) UpdateResearchFields(_ context.Context, id string, fields map[string]interface{}) error {
	return m.applyFields(id, fields)
}

func (m *mockResearchRepo) AddAccountLink(_ context.Context, id, accountID string) error {
	r, ok := m.byID[id]
	if !ok {
		// Mirrors the real repo's attribute_exists(PK) condition failure --
		// the research was deleted between the caller's GetResearch and
		// this call.
		return fmt.Errorf("%w: research %s not found", repository.ErrConditionFailed, id)
	}
	for _, existing := range r.AccountIDs {
		if existing == accountID {
			return nil // ADD on a set already containing the value is a no-op
		}
	}
	r.AccountIDs = append(r.AccountIDs, accountID)
	return nil
}

func (m *mockResearchRepo) RemoveAccountLink(_ context.Context, id, accountID string) error {
	r, ok := m.byID[id]
	if !ok {
		return fmt.Errorf("%w: research %s not found", repository.ErrConditionFailed, id)
	}
	remaining := make([]string, 0, len(r.AccountIDs))
	for _, existing := range r.AccountIDs {
		if existing != accountID {
			remaining = append(remaining, existing)
		}
	}
	r.AccountIDs = remaining
	return nil
}

// LinkAccountTransactional/UnlinkAccountTransactional simulate a
// TransactWriteItems call by running the set-update and the ref write/delete
// back-to-back against the two mocks (m and m.mainRepo, wired by
// newTestResearchService) -- not truly atomic/rollback-capable like the
// real DynamoDB transaction, but sufficient to test the outcome both writes
// are meant to produce together.
func (m *mockResearchRepo) LinkAccountTransactional(ctx context.Context, id, accountID string, ref *model.ResearchRef) error {
	if err := m.AddAccountLink(ctx, id, accountID); err != nil {
		return err
	}
	if m.mainRepo != nil {
		return m.mainRepo.PutResearchRef(ctx, ref)
	}
	return nil
}

func (m *mockResearchRepo) UnlinkAccountTransactional(ctx context.Context, id, accountID string) error {
	if err := m.RemoveAccountLink(ctx, id, accountID); err != nil {
		return err
	}
	if m.mainRepo != nil {
		return m.mainRepo.DeleteResearchRef(ctx, accountID, id)
	}
	return nil
}

func (m *mockResearchRepo) applyFields(id string, fields map[string]interface{}) error {
	r, ok := m.byID[id]
	if !ok {
		return errors.New("not found")
	}
	if v, ok := fields["accountIds"]; ok {
		r.AccountIDs = v.([]string)
	}
	if v, ok := fields["status"]; ok {
		r.Status = v.(string)
	}
	if v, ok := fields["title"]; ok {
		r.Title = v.(string)
	}
	return nil
}

func (m *mockResearchRepo) ListUserResearch(_ context.Context, userId string) ([]model.Research, error) {
	out := []model.Research{}
	for _, r := range m.byID {
		if r.UserID == userId {
			out = append(out, *r)
		}
	}
	return out, nil
}

// BatchGetResearch deliberately returns items in reverse of the requested
// id order -- real DynamoDB BatchGetItem does not preserve request order,
// and a mock that happened to preserve it would hide a caller bug that
// relies on BatchGetResearch's output order matching its input order.
func (m *mockResearchRepo) BatchGetResearch(_ context.Context, ids []string) ([]model.Research, error) {
	out := []model.Research{}
	for i := len(ids) - 1; i >= 0; i-- {
		if r, ok := m.byID[ids[i]]; ok {
			out = append(out, *r)
		}
	}
	return out, nil
}

func (m *mockResearchRepo) ListSubPages(_ context.Context, userId, parentId string) ([]model.Research, error) {
	out := []model.Research{}
	for _, r := range m.byID {
		if r.UserID == userId && r.ParentID == parentId {
			out = append(out, *r)
		}
	}
	return out, nil
}

func (m *mockResearchRepo) RemoveResearchField(_ context.Context, id, fieldName string) error {
	r, ok := m.byID[id]
	if !ok {
		return errors.New("not found")
	}
	if fieldName == "trashedAt" {
		r.TrashedAt = ""
	}
	return nil
}

func (m *mockResearchRepo) DeleteResearch(_ context.Context, id, userId string) error {
	delete(m.byID, id)
	return nil
}

func (m *mockResearchRepo) CreateResearchShare(_ context.Context, researchID, ownerID, ownerEmail, sharedToID, email, permission string) (*model.Share, error) {
	share := &model.Share{MeetingID: researchID, OwnerID: ownerID, OwnerEmail: ownerEmail, SharedToID: sharedToID, Email: email, Permission: permission}
	m.shares[sharedToID+"|"+researchID] = share
	return share, nil
}

func (m *mockResearchRepo) GetResearchShare(_ context.Context, sharedToID, researchID string) (*model.Share, error) {
	return m.shares[sharedToID+"|"+researchID], nil
}

func (m *mockResearchRepo) DeleteResearchShare(_ context.Context, sharedToID, researchID string) error {
	delete(m.shares, sharedToID+"|"+researchID)
	return nil
}

func (m *mockResearchRepo) ListSharesForResearch(_ context.Context, researchID string) ([]model.Share, error) {
	out := []model.Share{}
	for _, s := range m.shares {
		if s.MeetingID == researchID {
			out = append(out, *s)
		}
	}
	return out, nil
}

// mockResearchMainRepo implements researchMainRepo with in-memory maps.
type mockResearchMainRepo struct {
	members       map[string]*model.AccountMember // accountID|userID
	refs          map[string][]model.ResearchRef  // accountID -> refs
	failPutRefFor string                          // if set, PutResearchRef fails for this accountID
}

func newMockResearchMainRepo() *mockResearchMainRepo {
	return &mockResearchMainRepo{
		members: make(map[string]*model.AccountMember),
		refs:    make(map[string][]model.ResearchRef),
	}
}

func (m *mockResearchMainRepo) ListSharesForUser(_ context.Context, userID string) ([]model.Share, error) {
	return nil, nil
}

func (m *mockResearchMainRepo) GetUserByEmail(_ context.Context, email string) (*model.User, error) {
	return nil, nil
}

func (m *mockResearchMainRepo) GetMember(_ context.Context, accountID, userID string) (*model.AccountMember, error) {
	mem, ok := m.members[accountID+"|"+userID]
	if !ok {
		return nil, nil
	}
	cp := *mem
	return &cp, nil
}

func (m *mockResearchMainRepo) addMember(accountID, userID string) {
	m.members[accountID+"|"+userID] = &model.AccountMember{AccountID: accountID, UserID: userID, Role: model.RoleAM}
}

// PutResearchRef mirrors DynamoDB PutItem's overwrite-by-key semantics: the
// real repo keys this item by PK=ACCOUNT#{accountID}, SK=RESEARCH_REF#{researchID},
// so re-linking the same pair overwrites the existing item rather than
// duplicating it.
func (m *mockResearchMainRepo) PutResearchRef(_ context.Context, ref *model.ResearchRef) error {
	if m.failPutRefFor == ref.AccountID {
		return errors.New("simulated transient write failure")
	}
	refs := m.refs[ref.AccountID]
	for i, r := range refs {
		if r.ResearchID == ref.ResearchID {
			refs[i] = *ref
			return nil
		}
	}
	m.refs[ref.AccountID] = append(refs, *ref)
	return nil
}

func (m *mockResearchMainRepo) DeleteResearchRef(_ context.Context, accountID, researchID string) error {
	refs := m.refs[accountID]
	for i, r := range refs {
		if r.ResearchID == researchID {
			m.refs[accountID] = append(refs[:i], refs[i+1:]...)
			return nil
		}
	}
	return nil
}

// ListResearchRefsForAccount sorts by CreatedAt descending (newest first),
// mirroring the real repo's fix -- SK-based ScanIndexForward was never
// actually chronological (SK is a random researchId, not a timestamp).
func (m *mockResearchMainRepo) ListResearchRefsForAccount(_ context.Context, accountID string) ([]model.ResearchRef, error) {
	refs := append([]model.ResearchRef(nil), m.refs[accountID]...)
	sort.Slice(refs, func(i, j int) bool { return refs[i].CreatedAt.After(refs[j].CreatedAt) })
	return refs, nil
}

func seedResearch(repo *mockResearchRepo, id, userID string) *model.Research {
	r := &model.Research{ResearchID: id, UserID: userID, Topic: "topic-" + id, Status: "done", CreatedAt: time.Now().UTC().Format(time.RFC3339)}
	repo.byID[id] = r
	return r
}

func TestLinkAccount_OwnerAndMember_Succeeds(t *testing.T) {
	repo := newMockResearchRepo()
	mainRepo := newMockResearchMainRepo()
	svc := newTestResearchService(repo, mainRepo)

	seedResearch(repo, "r1", "owner-1")
	mainRepo.addMember("acc-1", "owner-1")

	ids, err := svc.LinkAccount(context.Background(), "owner-1", "r1", "acc-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ids) != 1 || ids[0] != "acc-1" {
		t.Fatalf("expected [acc-1], got %v", ids)
	}
	// idempotent re-link
	ids2, err := svc.LinkAccount(context.Background(), "owner-1", "r1", "acc-1")
	if err != nil || len(ids2) != 1 {
		t.Fatalf("expected idempotent re-link to stay [acc-1], got %v err=%v", ids2, err)
	}
	refs, _ := mainRepo.ListResearchRefsForAccount(context.Background(), "acc-1")
	if len(refs) != 1 {
		t.Fatalf("expected 1 research ref, got %d", len(refs))
	}
}

func TestLinkAccount_NonMemberOfAccount_Forbidden(t *testing.T) {
	repo := newMockResearchRepo()
	mainRepo := newMockResearchMainRepo()
	svc := newTestResearchService(repo, mainRepo)

	seedResearch(repo, "r1", "owner-1")
	// owner-1 is NOT a member of acc-1

	_, err := svc.LinkAccount(context.Background(), "owner-1", "r1", "acc-1")
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("expected ErrForbidden, got %v", err)
	}
}

func TestLinkAccount_NonOwnerOfResearch_Forbidden(t *testing.T) {
	repo := newMockResearchRepo()
	mainRepo := newMockResearchMainRepo()
	svc := newTestResearchService(repo, mainRepo)

	seedResearch(repo, "r1", "owner-1")
	mainRepo.addMember("acc-1", "stranger")

	_, err := svc.LinkAccount(context.Background(), "stranger", "r1", "acc-1")
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("expected ErrForbidden, got %v", err)
	}
}

func TestUnlinkAccount_RemovesRefAndField(t *testing.T) {
	repo := newMockResearchRepo()
	mainRepo := newMockResearchMainRepo()
	svc := newTestResearchService(repo, mainRepo)

	seedResearch(repo, "r1", "owner-1")
	mainRepo.addMember("acc-1", "owner-1")
	if _, err := svc.LinkAccount(context.Background(), "owner-1", "r1", "acc-1"); err != nil {
		t.Fatalf("setup link failed: %v", err)
	}

	ids, err := svc.UnlinkAccount(context.Background(), "owner-1", "r1", "acc-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ids) != 0 {
		t.Fatalf("expected empty accountIds, got %v", ids)
	}
	refs, _ := mainRepo.ListResearchRefsForAccount(context.Background(), "acc-1")
	if len(refs) != 0 {
		t.Fatalf("expected ref removed, got %d", len(refs))
	}
}

func TestGetResearchDetail_AccountMemberAccess(t *testing.T) {
	repo := newMockResearchRepo()
	mainRepo := newMockResearchMainRepo()
	svc := newTestResearchService(repo, mainRepo)

	seedResearch(repo, "r1", "owner-1")
	mainRepo.addMember("acc-1", "owner-1")
	mainRepo.addMember("acc-1", "member-2")
	if _, err := svc.LinkAccount(context.Background(), "owner-1", "r1", "acc-1"); err != nil {
		t.Fatalf("setup link failed: %v", err)
	}

	// s3Client is nil, so GetResearchDetail must not attempt to read content
	// for a "done" research with S3Key set -- clear those to avoid a nil
	// dereference unrelated to the auth path under test.
	repo.byID["r1"].Status = "planning"
	repo.byID["r1"].S3Key = ""

	// account member (not owner, not directly shared) can read
	resp, err := svc.GetResearchDetail(context.Background(), "r1", "member-2")
	if err != nil {
		t.Fatalf("expected account member to read research, got error: %v", err)
	}
	if !resp.IsShared {
		t.Errorf("expected IsShared=true for account-member viewer (hides owner-only chips UI)")
	}

	// stranger with no share and no account membership is forbidden
	_, err = svc.GetResearchDetail(context.Background(), "r1", "stranger")
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("expected ErrForbidden for stranger, got %v", err)
	}

	// trashed research is hidden from non-owners even if account-linked
	repo.byID["r1"].TrashedAt = time.Now().UTC().Format(time.RFC3339)
	_, err = svc.GetResearchDetail(context.Background(), "r1", "member-2")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound for trashed research viewed by non-owner, got %v", err)
	}
}

func TestListAccountResearch_MemberOnlyExcludesTrashed(t *testing.T) {
	repo := newMockResearchRepo()
	mainRepo := newMockResearchMainRepo()
	svc := newTestResearchService(repo, mainRepo)

	seedResearch(repo, "r1", "owner-1")
	seedResearch(repo, "r2", "owner-1")
	mainRepo.addMember("acc-1", "owner-1")
	mainRepo.addMember("acc-1", "member-2")
	if _, err := svc.LinkAccount(context.Background(), "owner-1", "r1", "acc-1"); err != nil {
		t.Fatalf("link r1 failed: %v", err)
	}
	if _, err := svc.LinkAccount(context.Background(), "owner-1", "r2", "acc-1"); err != nil {
		t.Fatalf("link r2 failed: %v", err)
	}
	repo.byID["r2"].TrashedAt = time.Now().UTC().Format(time.RFC3339)

	items, err := svc.ListAccountResearch(context.Background(), "member-2", "acc-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(items) != 1 || items[0].ResearchID != "r1" {
		t.Fatalf("expected only r1 (trashed r2 excluded), got %v", items)
	}

	_, err = svc.ListAccountResearch(context.Background(), "stranger", "acc-1")
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("expected ErrForbidden for non-member, got %v", err)
	}
}

func TestListAccountResearch_StaleRefExcludedAfterUnlinkFailure(t *testing.T) {
	repo := newMockResearchRepo()
	mainRepo := newMockResearchMainRepo()
	svc := newTestResearchService(repo, mainRepo)

	seedResearch(repo, "r1", "owner-1")
	mainRepo.addMember("acc-1", "owner-1")
	mainRepo.addMember("acc-1", "member-2")
	if _, err := svc.LinkAccount(context.Background(), "owner-1", "r1", "acc-1"); err != nil {
		t.Fatalf("link failed: %v", err)
	}

	// Simulate exactly the failure mode LinkAccount/UnlinkAccount's
	// best-effort ref write/delete leaves behind: the canonical accountIds
	// no longer lists acc-1 (RemoveAccountLink succeeded), but the
	// RESEARCHREF# index item was never cleaned up (DeleteResearchRef
	// failed and was only logged, not surfaced as an error). Before this
	// fix, ListAccountResearch trusted the stale ref alone and kept
	// exposing the research to the account.
	repo.byID["r1"].AccountIDs = nil

	items, err := svc.ListAccountResearch(context.Background(), "member-2", "acc-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("expected stale ref to be excluded (fail-closed), got %v", items)
	}
}

func TestLinkAccount_MultipleAccountsAllPersist(t *testing.T) {
	// Regression: the old LinkAccount read research.AccountIDs, appended
	// one accountID, and SET the whole list back. AddAccountLink instead
	// does an atomic set ADD, so linking a second account can never lose
	// the first one's link (the actual race two concurrent LinkAccount
	// calls for different accounts on the same research could hit).
	repo := newMockResearchRepo()
	mainRepo := newMockResearchMainRepo()
	svc := newTestResearchService(repo, mainRepo)

	seedResearch(repo, "r1", "owner-1")
	mainRepo.addMember("acc-1", "owner-1")
	mainRepo.addMember("acc-2", "owner-1")

	if _, err := svc.LinkAccount(context.Background(), "owner-1", "r1", "acc-1"); err != nil {
		t.Fatalf("link acc-1 failed: %v", err)
	}
	if _, err := svc.LinkAccount(context.Background(), "owner-1", "r1", "acc-2"); err != nil {
		t.Fatalf("link acc-2 failed: %v", err)
	}

	if !contains(repo.byID["r1"].AccountIDs, "acc-1") || !contains(repo.byID["r1"].AccountIDs, "acc-2") {
		t.Fatalf("expected both accounts linked, got %v", repo.byID["r1"].AccountIDs)
	}
}

func TestLinkAccount_ReLinkHealsMissingRef(t *testing.T) {
	// Regression: LinkAccount's "already linked" fast path used to return
	// immediately without ever touching the ref. If a *prior* LinkAccount
	// call's canonical AddAccountLink succeeded but its PutResearchRef
	// failed (logged, not retried), the ref stayed missing forever --
	// re-calling LinkAccount for the same pair is the only realistic way
	// to heal it, so that path must attempt the ref write too.
	repo := newMockResearchRepo()
	mainRepo := newMockResearchMainRepo()
	svc := newTestResearchService(repo, mainRepo)

	seedResearch(repo, "r1", "owner-1")
	mainRepo.addMember("acc-1", "owner-1")
	if _, err := svc.LinkAccount(context.Background(), "owner-1", "r1", "acc-1"); err != nil {
		t.Fatalf("initial link failed: %v", err)
	}

	// Simulate the failure this fix targets: canonical link exists, but the
	// ref write never landed (or was since lost).
	mainRepo.refs["acc-1"] = nil
	if refs, _ := mainRepo.ListResearchRefsForAccount(context.Background(), "acc-1"); len(refs) != 0 {
		t.Fatalf("setup: expected ref cleared, got %v", refs)
	}

	if _, err := svc.LinkAccount(context.Background(), "owner-1", "r1", "acc-1"); err != nil {
		t.Fatalf("re-link failed: %v", err)
	}

	refs, err := mainRepo.ListResearchRefsForAccount(context.Background(), "acc-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(refs) != 1 || refs[0].ResearchID != "r1" {
		t.Fatalf("expected the missing ref to be healed by re-linking, got %v", refs)
	}
}

func TestLinkAccount_RefWriteFailureIsSurfacedNotSwallowed(t *testing.T) {
	// Regression: LinkAccount used to log-and-swallow a PutResearchRef
	// failure, returning 200 with the canonical link updated but the
	// account's reverse index never written -- permanently, since nothing
	// else retries it. It must now return an error so the caller knows to
	// retry (which takes the already-linked healing path).
	repo := newMockResearchRepo()
	mainRepo := newMockResearchMainRepo()
	svc := newTestResearchService(repo, mainRepo)

	seedResearch(repo, "r1", "owner-1")
	mainRepo.addMember("acc-1", "owner-1")
	mainRepo.failPutRefFor = "acc-1"

	if _, err := svc.LinkAccount(context.Background(), "owner-1", "r1", "acc-1"); err == nil {
		t.Fatal("expected LinkAccount to surface the ref write failure, got nil error")
	}

	if !contains(repo.byID["r1"].AccountIDs, "acc-1") {
		t.Fatalf("expected the canonical link to still have succeeded, got %v", repo.byID["r1"].AccountIDs)
	}
	if refs, _ := mainRepo.ListResearchRefsForAccount(context.Background(), "acc-1"); len(refs) != 0 {
		t.Fatalf("expected no ref written while the write was failing, got %v", refs)
	}

	mainRepo.failPutRefFor = ""
	if _, err := svc.LinkAccount(context.Background(), "owner-1", "r1", "acc-1"); err != nil {
		t.Fatalf("expected retry to heal, got error: %v", err)
	}
	if refs, _ := mainRepo.ListResearchRefsForAccount(context.Background(), "acc-1"); len(refs) != 1 {
		t.Fatalf("expected retry to write the ref, got %v", refs)
	}
}

func TestLinkAccount_DeletedBetweenGetAndLink_ReturnsNotFound(t *testing.T) {
	// Regression: AddAccountLink's UpdateItem upserts by default. Without
	// an attribute_exists(PK) condition, a research deleted between this
	// call's GetResearch and the AddAccountLink write would make that
	// write silently CREATE a zombie RESEARCH#{id}/CONFIG item containing
	// only accountIds, instead of failing closed with ErrNotFound.
	repo := newMockResearchRepo()
	mainRepo := newMockResearchMainRepo()
	svc := newTestResearchService(repo, mainRepo)

	seedResearch(repo, "r1", "owner-1")
	mainRepo.addMember("acc-1", "owner-1")
	repo.deleteAfterGet = "r1"

	if _, err := svc.LinkAccount(context.Background(), "owner-1", "r1", "acc-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	if _, ok := repo.byID["r1"]; ok {
		t.Fatalf("expected no zombie research record to be created")
	}
}

func TestUnlinkAccount_DeletedBetweenGetAndUnlink_ReturnsNotFound(t *testing.T) {
	repo := newMockResearchRepo()
	mainRepo := newMockResearchMainRepo()
	svc := newTestResearchService(repo, mainRepo)

	seedResearch(repo, "r1", "owner-1")
	repo.byID["r1"].AccountIDs = []string{"acc-1"}
	repo.deleteAfterGet = "r1"

	if _, err := svc.UnlinkAccount(context.Background(), "owner-1", "r1", "acc-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestListAccountResearch_PreservesRefOrderAfterBatchGet(t *testing.T) {
	// Regression: BatchGetItem (what BatchGetResearch wraps) does not
	// preserve request order. ListAccountResearch used to iterate
	// BatchGetResearch's return value directly, so its output order was
	// whatever DynamoDB happened to return rather than the refs' intended
	// (newest-first, by CreatedAt) order. The mock's BatchGetResearch
	// deliberately returns reversed to catch exactly this.
	repo := newMockResearchRepo()
	mainRepo := newMockResearchMainRepo()
	svc := newTestResearchService(repo, mainRepo)

	seedResearch(repo, "r1", "owner-1")
	seedResearch(repo, "r2", "owner-1")
	seedResearch(repo, "r3", "owner-1")
	mainRepo.addMember("acc-1", "owner-1")
	for _, id := range []string{"r1", "r2", "r3"} {
		if _, err := svc.LinkAccount(context.Background(), "owner-1", id, "acc-1"); err != nil {
			t.Fatalf("link %s failed: %v", id, err)
		}
	}

	items, err := svc.ListAccountResearch(context.Background(), "owner-1", "acc-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// r3 was linked last, so it has the latest CreatedAt -- newest-first
	// means it comes first, not r1 (link/insertion order).
	want := []string{"r3", "r2", "r1"}
	if len(items) != len(want) {
		t.Fatalf("expected %d items, got %v", len(want), items)
	}
	for i, id := range want {
		if items[i].ResearchID != id {
			t.Fatalf("expected newest-first refs order %v, got %v", want, items)
		}
	}
}

func TestUpdateResearch_RenamesTitleWithoutTouchingTopic(t *testing.T) {
	repo := newMockResearchRepo()
	mainRepo := newMockResearchMainRepo()
	svc := newTestResearchService(repo, mainRepo)

	seedResearch(repo, "r1", "owner-1")

	if err := svc.UpdateResearch(context.Background(), "r1", "owner-1", "  My Renamed Title  "); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updated, _ := repo.GetResearch(context.Background(), "r1")
	if updated.Title != "My Renamed Title" {
		t.Fatalf("expected trimmed title to persist, got %q", updated.Title)
	}
	if updated.Topic != "topic-r1" {
		t.Fatalf("rename must never touch the original topic, got %q", updated.Topic)
	}
}

func TestUpdateResearch_NonOwner_Forbidden(t *testing.T) {
	repo := newMockResearchRepo()
	mainRepo := newMockResearchMainRepo()
	svc := newTestResearchService(repo, mainRepo)

	seedResearch(repo, "r1", "owner-1")

	err := svc.UpdateResearch(context.Background(), "r1", "someone-else", "hijacked title")
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("expected ErrForbidden, got %v", err)
	}
}

func TestUpdateResearch_NotFound(t *testing.T) {
	repo := newMockResearchRepo()
	mainRepo := newMockResearchMainRepo()
	svc := newTestResearchService(repo, mainRepo)

	err := svc.UpdateResearch(context.Background(), "does-not-exist", "owner-1", "new title")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestUpdateResearch_EmptyTitleRejected(t *testing.T) {
	repo := newMockResearchRepo()
	mainRepo := newMockResearchMainRepo()
	svc := newTestResearchService(repo, mainRepo)

	seedResearch(repo, "r1", "owner-1")

	err := svc.UpdateResearch(context.Background(), "r1", "owner-1", "   ")
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput for blank title, got %v", err)
	}
}

func TestUpdateResearch_TooLongRejected(t *testing.T) {
	repo := newMockResearchRepo()
	mainRepo := newMockResearchMainRepo()
	svc := newTestResearchService(repo, mainRepo)

	seedResearch(repo, "r1", "owner-1")

	long := make([]byte, 201)
	for i := range long {
		long[i] = 'a'
	}
	err := svc.UpdateResearch(context.Background(), "r1", "owner-1", string(long))
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput for a >200 char title, got %v", err)
	}
}

func TestUpdateResearch_KoreanTitleUsesRuneCountNotByteLength(t *testing.T) {
	repo := newMockResearchRepo()
	mainRepo := newMockResearchMainRepo()
	svc := newTestResearchService(repo, mainRepo)

	seedResearch(repo, "r1", "owner-1")

	// Each 가 is 3 bytes in UTF-8: 150 runes = 450 bytes, comfortably over a
	// byte-length check's 200 limit but well under the intended
	// 200-*character* limit -- this must succeed, not be rejected.
	runes := make([]rune, 150)
	for i := range runes {
		runes[i] = '가'
	}
	title := string(runes)

	if err := svc.UpdateResearch(context.Background(), "r1", "owner-1", title); err != nil {
		t.Fatalf("expected a 150-rune (450-byte) Korean title to be accepted, got: %v", err)
	}
}
