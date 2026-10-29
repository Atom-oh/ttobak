package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"
)

type descendantContentRepo struct {
	*mockAccountRepo
	childPages       map[string][]string
	childNext        map[string]string
	childErr         error
	afterPublication func()
}

func descendantContentFixture() *descendantContentRepo {
	r := &descendantContentRepo{mockAccountRepo: newMockAccountRepo(), childPages: map[string][]string{"parent/": {"child"}}, childNext: map[string]string{}}
	for id, parent := range map[string]string{"parent": "", "child": "parent", "sibling": ""} {
		r.accounts[id] = &model.Account{PK: "ACCOUNT#" + id, SK: "META", AccountID: id, ParentAccountID: parent, Name: id, EntityType: model.EntityTypeAccount}
	}
	r.members[memberKey("parent", "reader")] = &model.AccountMember{PK: "ACCOUNT#parent", SK: "MEMBER#reader", AccountID: "parent", UserID: "reader", Role: model.RoleOwner, EntityType: model.EntityTypeAccountMember}
	r.members[memberKey("child", "other")] = &model.AccountMember{PK: "ACCOUNT#child", SK: "MEMBER#other", AccountID: "child", UserID: "other", Role: model.RoleOwner, EntityType: model.EntityTypeAccountMember}
	r.documents["ACCOUNT#child"] = []model.AccountDocument{{PK: "ACCOUNT#child", SK: "DOC#doc", AccountID: "child", DocID: "doc", Title: "Shared", Content: strings.Repeat("content ", 300), PublicShareToken: "private-capability"}}
	return r
}
func (r *descendantContentRepo) ListChildAccountIDsPage(_ context.Context, parent, cursor string, limit int32) ([]string, *string, error) {
	if r.childErr != nil {
		return nil, nil, r.childErr
	}
	k := parent + "/" + cursor
	if n := r.childNext[k]; n != "" {
		return r.childPages[k], &n, nil
	}
	return r.childPages[k], nil, nil
}
func TestDescendantAccountContentReadOnly(t *testing.T) {
	t.Setenv(accountDescendantReadsEnv, "1")
	r := descendantContentFixture()
	s := newAccountServiceWithRepo(r)
	ctx := context.Background()
	a, err := s.GetAccount(ctx, "reader", "child")
	if err != nil || a == nil {
		t.Fatalf("ancestor account detail: %v", err)
	}
	raw, _ := json.Marshal(a)
	if !strings.Contains(string(raw), `"accessPermission":"read"`) || !strings.Contains(string(raw), `"inheritedFromAccountId":"parent"`) {
		t.Fatalf("read grant missing: %s", raw)
	}
	if len(a.Members) != 1 || a.Members[0].UserID != "other" {
		t.Fatalf("synthetic child membership: %+v", a.Members)
	}
	docs, err := s.ListAccountDocuments(ctx, "reader", "child", "")
	if err != nil || len(docs) != 1 {
		t.Fatalf("documents: %v/%v", docs, err)
	}
	detail, err := s.GetAccountDocument(ctx, "reader", "child", "doc")
	if err != nil || detail == nil || detail.PublicShareToken != "" {
		t.Fatalf("document detail/token: %+v/%v", detail, err)
	}
	if _, err := s.PutDocument(ctx, "reader", "child", &model.PutDocumentRequest{Title: "write"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("ancestor write: %v", err)
	}
	if _, err := s.UpdateMemberRole(ctx, "reader", "child", "other", &model.UpdateMemberRequest{Role: model.RoleAM}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("ancestor role mutation: %v", err)
	}
	delete(r.members, memberKey("parent", "reader"))
	if _, err := s.GetAccountDocument(ctx, "reader", "child", "doc"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("revoked document: %v", err)
	}
}
func TestDescendantAccountDiscovery(t *testing.T) {
	t.Setenv(accountDescendantReadsEnv, "1")
	r := descendantContentFixture()
	r.childPages["parent/"] = nil
	r.childNext["parent/"] = "advance"
	r.childPages["parent/advance"] = []string{"child", "sibling"}
	ids, err := listReadableAccountIDs(context.Background(), r, "reader", nil)
	if err != nil || !reflect.DeepEqual(ids, []string{"child", "parent"}) {
		t.Fatalf("empty advancing/stale child discovery: %v/%v", ids, err)
	}
	accounts, err := newAccountServiceWithRepo(r).ListAccounts(context.Background(), "reader")
	if err != nil || len(accounts) != 2 {
		t.Fatalf("account list: %v/%v", accounts, err)
	}
	r.childNext["parent/advance"] = "advance"
	if _, err := listReadableAccountIDs(context.Background(), r, "reader", nil); !errors.Is(err, ErrReadingData) {
		t.Fatalf("nonadvancing discovery: %v", err)
	}
	r.childNext = map[string]string{}
	r.childErr = errors.New("storage failure")
	if _, err := listReadableAccountIDs(context.Background(), r, "reader", nil); !errors.Is(err, r.childErr) {
		t.Fatalf("storage discovery: %v", err)
	}
}
func TestDescendantAccountDiscoveryExplicitLimit(t *testing.T) {
	t.Setenv(accountDescendantReadsEnv, "1")
	r := descendantContentFixture()
	r.childPages = map[string][]string{}
	for i := 0; i < 1000; i++ {
		id := fmt.Sprintf("child%04d", i)
		r.accounts[id] = &model.Account{PK: "ACCOUNT#" + id, SK: "META", AccountID: id, ParentAccountID: "parent", EntityType: model.EntityTypeAccount}
		page := fmt.Sprintf("p%d", i/100)
		if i/100 == 0 {
			page = ""
		}
		r.childPages["parent/"+page] = append(r.childPages["parent/"+page], id)
		if i/100 < 9 {
			r.childNext["parent/"+page] = fmt.Sprintf("p%d", i/100+1)
		}
	}
	if _, err := listReadableAccountIDs(context.Background(), r, "reader", nil); !errors.Is(err, ErrReadingData) {
		t.Fatalf("1001 readable accounts silently truncated: %v", err)
	}
}
func TestDescendantAccountBoundedReadsAndReparent(t *testing.T) {
	t.Setenv(accountDescendantReadsEnv, "1")
	r := descendantContentFixture()
	ctx := context.Background()
	ar := NewAccountReadingService(&accountReadingRepo{mockAccountRepo: r.mockAccountRepo})
	body, err := ar.Read(ctx, "reader", "child", url.Values{"section": {"detail"}, "pageSize": {"1"}})
	if err != nil {
		t.Fatalf("bounded account read: %v", err)
	}
	var page struct {
		Page struct {
			NextCursor string `json:"nextCursor"`
		} `json:"page"`
	}
	json.Unmarshal(body, &page)
	if page.Page.NextCursor == "" {
		t.Fatal("missing continuation")
	}
	r.accounts["child"].ParentAccountID = "sibling"
	if _, err := ar.Read(ctx, "reader", "child", url.Values{"section": {"detail"}, "pageSize": {"1"}, "cursor": {page.Page.NextCursor}}); !errors.Is(err, ErrForbidden) && !errors.Is(err, ErrStaleReadingCursor) {
		t.Fatalf("reparented account cursor: %v", err)
	}
}
func TestDescendantDocumentBoundedReadsAndRevoke(t *testing.T) {
	t.Setenv(accountDescendantReadsEnv, "1")
	r := descendantContentFixture()
	dr := NewDocumentReadingService(r)
	ctx := context.Background()
	body, err := dr.Read(ctx, "reader", "child", "doc", url.Values{"pageSize": {"4"}})
	if err != nil {
		t.Fatalf("bounded document read: %v", err)
	}
	if !strings.Contains(string(body), `"permission":"read"`) {
		t.Fatalf("inherited document permission: %s", body)
	}
	var page struct {
		Page struct {
			NextCursor string `json:"nextCursor"`
		} `json:"page"`
	}
	json.Unmarshal(body, &page)
	delete(r.members, memberKey("parent", "reader"))
	if _, err := dr.Read(ctx, "reader", "child", "doc", url.Values{"pageSize": {"4"}, "cursor": {page.Page.NextCursor}}); !errors.Is(err, ErrForbidden) && !errors.Is(err, ErrStaleReadingCursor) {
		t.Fatalf("revoked document cursor: %v", err)
	}
}
func TestDescendantInsightsNeverGrandfatherLegacy(t *testing.T) {
	t.Setenv(accountDescendantReadsEnv, "1")
	t.Setenv(SummaryPrivacyReaderEnv, "0")
	r := descendantContentFixture()
	at := time.Unix(10, 0).UTC()
	r.meetings["meeting"] = &model.Meeting{PK: "USER#owner", SK: "MEETING#meeting", UserID: "owner", MeetingID: "meeting", AccountID: "child", SharedToAccount: true, Content: "unsafe", EntityType: "MEETING"}
	r.insightsByAccount["child"] = []model.AccountInsight{{PK: "ACCOUNT#child", SK: "INSIGHT#" + at.Format(time.RFC3339) + "#meeting#0", AccountID: "child", SourceID: "meeting", SourceUserID: "owner", SourceType: "meeting", OccurredAt: at, EntityType: model.EntityTypeInsight, Text: "copied unsafe summary"}}
	if _, err := newAccountServiceWithRepo(r).ListAccountInsights(context.Background(), "reader", "child", time.Time{}, time.Time{}, nil); !errors.Is(err, ErrSummaryReadRestricted) {
		t.Fatalf("legacy inherited insights: %v", err)
	}
}

func TestDescendantAccountsOptionalPagination(t *testing.T) {
	t.Setenv(accountDescendantReadsEnv, "1")
	r := descendantContentFixture()
	s := newAccountServiceWithRepo(r)
	ctx := context.Background()
	first, err := s.ListAccountsPage(ctx, "reader", url.Values{"pageSize": {"1"}})
	if err != nil || len(first.Accounts) != 1 || first.NextCursor == "" {
		t.Fatalf("first page: %+v/%v", first, err)
	}
	second, err := s.ListAccountsPage(ctx, "reader", url.Values{"pageSize": {"1"}, "cursor": {first.NextCursor}})
	if err != nil || len(second.Accounts) != 1 || second.NextCursor == "" || second.Accounts[0].AccountID == first.Accounts[0].AccountID {
		t.Fatalf("second page: %+v/%v", second, err)
	}
	cursor := second.NextCursor
	for pages := 0; cursor != ""; pages++ {
		if pages > 10 {
			t.Fatal("account discovery never completes")
		}
		page, err := s.ListAccountsPage(ctx, "reader", url.Values{"pageSize": {"1"}, "cursor": {cursor}})
		if err != nil || len(page.Accounts) != 0 {
			t.Fatalf("terminal bounded discovery: %+v/%v", page, err)
		}
		cursor = page.NextCursor
	}
	r.accounts["child"].ParentAccountID = "sibling"
	if _, err := s.ListAccountsPage(ctx, "reader", url.Values{"pageSize": {"1"}, "cursor": {second.NextCursor}}); !errors.Is(err, ErrStaleReadingCursor) {
		t.Fatalf("moved child account list cursor: %v", err)
	}
}

type descendantNavigationRepo struct {
	*navigationRepo
	hierarchy *descendantContentRepo
}

func (r *descendantNavigationRepo) GetAccount(ctx context.Context, id string) (*model.Account, error) {
	return r.hierarchy.GetAccount(ctx, id)
}
func (r *descendantNavigationRepo) GetMember(ctx context.Context, id, user string) (*model.AccountMember, error) {
	return r.hierarchy.GetMember(ctx, id, user)
}
func (r *descendantNavigationRepo) ListChildAccountIDsPage(ctx context.Context, id, cursor string, limit int32) ([]string, *string, error) {
	return r.hierarchy.ListChildAccountIDsPage(ctx, id, cursor, limit)
}
func TestDescendantNavigationCatalog(t *testing.T) {
	t.Setenv(accountDescendantReadsEnv, "1")
	_, base := navigationFixture()
	r := &descendantNavigationRepo{base, descendantContentFixture()}
	r.accountIndex = []string{"parent"}
	s := NewNavigationCatalogService(r)
	q := url.Values{}
	foundChild := false
	for pages := 0; ; pages++ {
		page := navigationPage(t, s, "accounts", q)
		for _, raw := range page["accounts"].([]interface{}) {
			row := raw.(map[string]interface{})
			if row["accountId"] == "child" {
				foundChild = row["accessPermission"] == model.PermissionRead
			}
		}
		info := page["page"].(map[string]interface{})
		if info["complete"] == true {
			break
		}
		if pages > 10 {
			t.Fatal("descendant navigation never completes")
		}
		q.Set("cursor", info["nextCursor"].(string))
	}
	if !foundChild {
		t.Fatal("descendant navigation omitted the read-only child")
	}
	base.project("project", "owner", "child")
	base.refs["child"] = []string{"project"}
	item, _, err := s.project(context.Background(), "reader", "project", "accounts", "child")
	if err != nil || item == nil || item.row["accessPermission"] != "read" {
		t.Fatalf("descendant linked project: %+v/%v", item, err)
	}
}

type descendantDocCatalogRepo struct {
	*documentCatalogRepo
	hierarchy *descendantContentRepo
}

func (r *descendantDocCatalogRepo) GetAccount(ctx context.Context, id string) (*model.Account, error) {
	return r.hierarchy.GetAccount(ctx, id)
}
func (r *descendantDocCatalogRepo) GetMember(ctx context.Context, id, user string) (*model.AccountMember, error) {
	return r.hierarchy.GetMember(ctx, id, user)
}
func TestDescendantDocumentCatalog(t *testing.T) {
	t.Setenv(accountDescendantReadsEnv, "1")
	_, base := newDocumentCatalogFixture()
	r := &descendantDocCatalogRepo{base, descendantContentFixture()}
	base.docs["ACCOUNT#child/doc"] = &model.AccountDocument{PK: "ACCOUNT#child", SK: "DOC#doc", DocID: "doc", Title: "Shared"}
	raw, err := NewDocumentCatalogService(r).Read(context.Background(), "reader", url.Values{"accountId": {"child"}})
	if err != nil || !strings.Contains(string(raw), `"docId":"doc"`) || !strings.Contains(string(raw), `"permission":"read"`) {
		t.Fatalf("descendant document catalog: %s/%v", raw, err)
	}
}

type descendantMeetingCatalogRepo struct {
	*catalogMeetingRepo
	hierarchy *descendantContentRepo
}

func (r *descendantMeetingCatalogRepo) GetAccount(ctx context.Context, id string) (*model.Account, error) {
	return r.hierarchy.GetAccount(ctx, id)
}
func (r *descendantMeetingCatalogRepo) GetMember(ctx context.Context, id, user string) (*model.AccountMember, error) {
	return r.hierarchy.GetMember(ctx, id, user)
}
func (r *descendantMeetingCatalogRepo) ListChildAccountIDsPage(ctx context.Context, id, cursor string, limit int32) ([]string, *string, error) {
	return r.hierarchy.ListChildAccountIDsPage(ctx, id, cursor, limit)
}
func TestDescendantMeetingCatalogNeverEscalates(t *testing.T) {
	t.Setenv(accountDescendantReadsEnv, "1")
	t.Setenv(SummaryPrivacyReaderEnv, "0")
	r := &descendantMeetingCatalogRepo{&catalogMeetingRepo{mockMeetingRepo: newMockMeetingRepo()}, descendantContentFixture()}
	r.meetings["owner|meeting"] = &model.Meeting{UserID: "owner", MeetingID: "meeting", AccountID: "child", SharedToAccount: true, Title: "Published", Content: "unsafe", Insights: "unsafe"}
	r.shares["reader|meeting"] = &model.Share{OwnerID: "owner", SharedToID: "reader", MeetingID: "meeting", Permission: model.PermissionEdit, Origin: model.ShareOriginAccount}
	item, _, _, err := NewMeetingCatalogService(r).item(context.Background(), "reader", "all", "team", "child", repository.MeetingCatalogCandidate{OwnerID: "owner", MeetingID: "meeting"})
	if err != nil || item == nil || item.Permission == nil || *item.Permission != "read" || item.Summary != "" {
		t.Fatalf("inherited catalog privacy/permission: %+v/%v", item, err)
	}
}

type descendantVaultRepo struct {
	*vaultReadingRepo
	hierarchy *descendantContentRepo
}

func (r *descendantVaultRepo) GetAccount(ctx context.Context, id string) (*model.Account, error) {
	return r.hierarchy.GetAccount(ctx, id)
}
func (r *descendantVaultRepo) GetMember(ctx context.Context, id, user string) (*model.AccountMember, error) {
	return r.hierarchy.GetMember(ctx, id, user)
}
func (r *descendantVaultRepo) ListChildAccountIDsPage(ctx context.Context, id, cursor string, limit int32) ([]string, *string, error) {
	return r.hierarchy.ListChildAccountIDsPage(ctx, id, cursor, limit)
}
func TestDescendantVaultSourceReparentInvalidates(t *testing.T) {
	t.Setenv(accountDescendantReadsEnv, "1")
	_, base := vaultReadingFixture()
	r := &descendantVaultRepo{base, descendantContentFixture()}
	base.docs["ACCOUNT#child"] = map[string]*model.AccountDocument{"doc": {PK: "ACCOUNT#child", DocID: "doc", Content: "Shared"}}
	s := &VaultReadingService{repo: r}
	source, err := s.source(context.Background(), "reader", "document", "child", "doc")
	if err != nil || source == nil {
		t.Fatalf("descendant vault source: %v", err)
	}
	token := encodeVaultFile(source.token)
	r.hierarchy.accounts["child"].ParentAccountID = "sibling"
	if err := s.checkManifestAnchor(context.Background(), "reader", token); !errors.Is(err, ErrStaleReadingCursor) {
		t.Fatalf("moved vault anchor: %v", err)
	}
}

type descendantLegacyVaultRepo struct {
	*mockVaultRepo
	hierarchy     *descendantContentRepo
	afterDocument func()
}

func (r *descendantLegacyVaultRepo) GetAccount(ctx context.Context, id string) (*model.Account, error) {
	return r.hierarchy.GetAccount(ctx, id)
}
func (r *descendantLegacyVaultRepo) GetMember(ctx context.Context, id, user string) (*model.AccountMember, error) {
	return r.hierarchy.GetMember(ctx, id, user)
}
func (r *descendantLegacyVaultRepo) ListAccountsForUser(ctx context.Context, user string) ([]model.AccountMember, error) {
	return r.hierarchy.ListAccountsForUser(ctx, user)
}
func (r *descendantLegacyVaultRepo) ListChildAccountIDsPage(ctx context.Context, id, cursor string, limit int32) ([]string, *string, error) {
	return r.hierarchy.ListChildAccountIDsPage(ctx, id, cursor, limit)
}
func (r *descendantLegacyVaultRepo) GetAccountDocument(ctx context.Context, pk, id string) (*model.AccountDocument, error) {
	doc, err := r.mockVaultRepo.GetAccountDocument(ctx, pk, id)
	if r.afterDocument != nil {
		fn := r.afterDocument
		r.afterDocument = nil
		fn()
	}
	return doc, err
}
func TestDescendantLegacyVaultExportRechecksGrant(t *testing.T) {
	t.Setenv(accountDescendantReadsEnv, "1")
	h := descendantContentFixture()
	r := &descendantLegacyVaultRepo{mockVaultRepo: &mockVaultRepo{documents: h.documents}, hierarchy: h}
	s := newVaultServiceWithRepo(r)
	files, err := s.exportDocuments(context.Background(), "reader", map[string]string{})
	if err != nil || len(files) != 1 || !strings.Contains(files[0].Path, "Accounts/child/") {
		t.Fatalf("legacy descendant export: %+v/%v", files, err)
	}
	r.afterDocument = func() { delete(h.members, memberKey("parent", "reader")) }
	if _, err := s.exportDocuments(context.Background(), "reader", map[string]string{}); !errors.Is(err, ErrForbidden) && !errors.Is(err, ErrStaleReadingCursor) {
		t.Fatalf("revoked legacy export leaked bytes: %v", err)
	}
}

func TestDescendantAccountMeetingPublicationFiltersPrivateMetadata(t *testing.T) {
	t.Setenv(accountDescendantReadsEnv, "1")
	r := descendantContentFixture()
	at := time.Unix(10, 0).UTC()
	for _, id := range []string{"published", "private"} {
		r.meetings[id] = &model.Meeting{PK: "USER#owner", SK: "MEETING#" + id, UserID: "owner", MeetingID: id, AccountID: "child", SharedToAccount: id == "published", EntityType: "MEETING"}
		r.meetingRefs["child"] = append(r.meetingRefs["child"], model.MeetingRef{PK: "ACCOUNT#child", SK: "MEETINGREF#" + at.Format(time.RFC3339) + "#" + id, AccountID: "child", OwnerUserID: "owner", MeetingID: id, Title: id, Date: at, EntityType: model.EntityTypeMeetingRef})
	}
	rows, err := newAccountServiceWithRepo(r).ListAccountMeetings(context.Background(), "reader", "child")
	if err != nil || len(rows) != 1 || rows[0].MeetingID != "published" {
		t.Fatalf("private metadata: %+v/%v", rows, err)
	}
}

func (r *descendantContentRepo) GetMeetingPublication(ctx context.Context, owner, id string) (*model.Meeting, error) {
	meeting, err := r.mockAccountRepo.GetMeetingPublication(ctx, owner, id)
	if r.afterPublication != nil {
		fn := r.afterPublication
		r.afterPublication = nil
		fn()
	}
	return meeting, err
}
func TestDescendantAccountMeetingPublicationRace(t *testing.T) {
	t.Setenv(accountDescendantReadsEnv, "1")
	r := descendantContentFixture()
	at := time.Unix(10, 0).UTC()
	r.meetings["meeting"] = &model.Meeting{PK: "USER#owner", SK: "MEETING#meeting", UserID: "owner", MeetingID: "meeting", AccountID: "child", SharedToAccount: true, EntityType: "MEETING"}
	r.meetingRefs["child"] = []model.MeetingRef{{PK: "ACCOUNT#child", SK: "MEETINGREF#" + at.Format(time.RFC3339) + "#meeting", AccountID: "child", OwnerUserID: "owner", MeetingID: "meeting", Title: "now private", Date: at, EntityType: model.EntityTypeMeetingRef}}
	r.afterPublication = func() { r.meetings["meeting"].SharedToAccount = false }
	if _, err := newAccountServiceWithRepo(r).ListAccountMeetings(context.Background(), "reader", "child"); !errors.Is(err, ErrForbidden) && !errors.Is(err, ErrStaleReadingCursor) {
		t.Fatalf("unpublished source leaked metadata: %v", err)
	}
}
func TestDescendantNavigationIndependentProjectGrantRemainsEditable(t *testing.T) {
	t.Setenv(accountDescendantReadsEnv, "1")
	_, base := navigationFixture()
	r := &descendantNavigationRepo{base, descendantContentFixture()}
	base.project("project", "owner", "child")
	base.direct["project/reader"] = &model.ProjectMember{ProjectID: "project", UserID: "reader"}
	item, _, err := NewNavigationCatalogService(r).project(context.Background(), "reader", "project", "accounts", "child")
	if err != nil || item == nil || item.row["accessPermission"] != "edit" || item.row["inheritedFromAccountId"] != nil {
		t.Fatalf("independent grant downgraded: %+v/%v", item, err)
	}
}
