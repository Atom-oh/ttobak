package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"testing"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

type boundedDiscoveryRepo struct {
	*descendantNavigationRepo
	accountReads, childQueries int
	emptyDirect, emptyChild    bool
	afterAccount               func(string)
	nonadvancingProjects       bool
}

func (r *boundedDiscoveryRepo) QueryNavigationCandidates(ctx context.Context, user, kind, account, after string, limit int) (*repository.NavigationCandidatePage, error) {
	if kind == "owned" && after != "" && r.nonadvancingProjects {
		r.queries++
		return &repository.NavigationCandidatePage{NextID: after}, nil
	}
	if kind == "accounts" && after == "" && r.emptyDirect {
		r.queries++
		return &repository.NavigationCandidatePage{NextID: "advance"}, nil
	}
	return r.navigationRepo.QueryNavigationCandidates(ctx, user, kind, account, after, limit)
}
func (r *boundedDiscoveryRepo) GetAccount(ctx context.Context, id string) (*model.Account, error) {
	r.accountReads++
	a, err := r.descendantNavigationRepo.GetAccount(ctx, id)
	if r.afterAccount != nil {
		r.afterAccount(id)
	}
	return a, err
}

func TestDescendantRolloutPreservesProjectIndexProgressCheck(t *testing.T) {
	t.Setenv(accountDescendantReadsEnv, "1")
	r := boundedDiscoveryFixture(0)
	r.ownedIndex = []string{"a", "b"}
	r.project("a", "reader")
	r.project("b", "reader")
	r.nonadvancingProjects = true
	s := NewNavigationCatalogService(r)
	first := navigationPage(t, s, "projects", url.Values{"pageSize": {"1"}})
	cursor := first["page"].(map[string]interface{})["nextCursor"].(string)
	if _, err := s.Read(context.Background(), "reader", "projects", url.Values{"pageSize": {"1"}, "cursor": {cursor}}); !errors.Is(err, ErrReadingData) {
		t.Fatalf("rollout disabled project-index progress validation: %v", err)
	}
}

func (r *boundedDiscoveryRepo) ListChildAccountIDsPage(_ context.Context, parent, cursor string, limit int32) ([]string, *string, error) {
	r.childQueries++
	after := ""
	if cursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil {
			return nil, nil, err
		}
		var position struct {
			Parent string `json:"parent"`
			After  string `json:"after"`
		}
		if json.Unmarshal(raw, &position) != nil || position.Parent != parent {
			return nil, nil, ErrReadingData
		}
		after = strings.TrimPrefix(position.After, "CHILDREF#")
	}
	if parent == "parent" && after == "" && r.emptyChild {
		raw, _ := json.Marshal(map[string]string{"parent": parent, "after": "CHILDREF#advance"})
		next := base64.RawURLEncoding.EncodeToString(raw)
		return nil, &next, nil
	}
	var ids []string
	for id, a := range r.hierarchy.accounts {
		if a.ParentAccountID == parent && id > after {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	if len(ids) <= int(limit) {
		return ids, nil, nil
	}
	ids = ids[:limit]
	raw, _ := json.Marshal(struct {
		Parent string `json:"parent"`
		After  string `json:"after"`
	}{parent, "CHILDREF#" + ids[len(ids)-1]})
	next := base64.RawURLEncoding.EncodeToString(raw)
	return ids, &next, nil
}

func TestAccountCatalogDiscoveryTraversesWideTreeInBoundedPages(t *testing.T) {
	t.Setenv(accountDescendantReadsEnv, "1")
	r := boundedDiscoveryFixture(1001)
	// Independent roots may overlap a traversed subtree. Every row keeps its
	// current direct permission and the response declares union semantics.
	r.accountIndex = append(r.accountIndex, "child-0001")
	r.hierarchy.members[memberKey("child-0001", "reader")] = &model.AccountMember{
		PK: "ACCOUNT#child-0001", SK: "MEMBER#reader", AccountID: "child-0001",
		UserID: "reader", EntityType: model.EntityTypeAccountMember, Role: model.RoleAM,
	}
	s := NewNavigationCatalogService(r)
	q := url.Values{"pageSize": {"1"}}
	seen := map[string]bool{}
	for n := 0; ; n++ {
		beforeQueries, beforeChildren, beforeReads := r.queries, r.childQueries, r.accountReads
		page := navigationPage(t, s, "accounts", q)
		if r.queries-beforeQueries+r.childQueries-beforeChildren > 1 || r.accountReads-beforeReads > 20 {
			t.Fatalf("page %d did unbounded provider work", n)
		}
		for _, raw := range page["accounts"].([]interface{}) {
			row := raw.(map[string]interface{})
			id := row["accountId"].(string)
			if page["overlapPossible"] != true || page["unionKey"] != "accountId" {
				t.Fatal("overlapping account roots lack explicit union semantics")
			}
			seen[id] = true
			if id == "child-0001" && row["accessPermission"] != model.PermissionEdit {
				t.Fatal("lost independent direct grant")
			}
		}
		info := page["page"].(map[string]interface{})
		if info["complete"] == true {
			break
		}
		if n > 4000 {
			t.Fatal("discovery never completed")
		}
		cursor := info["nextCursor"].(string)
		if len(cursor) > 2048 {
			t.Fatal("cursor grew with the tree")
		}
		q.Set("cursor", cursor)
	}
	if len(seen) != 1002 {
		t.Fatalf("lost accounts: %d", len(seen))
	}
}

func TestAccountCatalogStaticOneRootDoesNotOverlapRows(t *testing.T) {
	t.Setenv(accountDescendantReadsEnv, "1")
	r := boundedDiscoveryFixture(10)
	s := NewNavigationCatalogService(r)
	q := url.Values{"pageSize": {"1"}}
	seen := map[string]bool{}
	for n := 0; ; n++ {
		page := navigationPage(t, s, "accounts", q)
		for _, raw := range page["accounts"].([]interface{}) {
			id := raw.(map[string]interface{})["accountId"].(string)
			if seen[id] {
				t.Fatalf("static one-root traversal duplicated %s", id)
			}
			seen[id] = true
		}
		info := page["page"].(map[string]interface{})
		if info["complete"] == true {
			break
		}
		if n > 40 {
			t.Fatal("static one-root traversal did not finish")
		}
		q.Set("cursor", info["nextCursor"].(string))
	}
	if len(seen) != 11 {
		t.Fatalf("static one-root traversal lost accounts: %d", len(seen))
	}
}

func TestAccountDiscoveryEmptyAdvancingPages(t *testing.T) {
	t.Setenv(accountDescendantReadsEnv, "1")
	r := boundedDiscoveryFixture(2)
	r.emptyDirect, r.emptyChild = true, true
	s := NewNavigationCatalogService(r)
	q := url.Values{"pageSize": {"1"}}
	seen := map[string]bool{}
	for n := 0; ; n++ {
		page := navigationPage(t, s, "accounts", q)
		for _, raw := range page["accounts"].([]interface{}) {
			seen[raw.(map[string]interface{})["accountId"].(string)] = true
		}
		info := page["page"].(map[string]interface{})
		if info["complete"] == true {
			break
		}
		if n > 20 {
			t.Fatal("empty advancing page looped")
		}
		q.Set("cursor", info["nextCursor"].(string))
	}
	if len(seen) != 3 {
		t.Fatalf("empty page hid accounts: %v", seen)
	}
}

func TestAccountDiscoveryCursorRejectsRevocationMoveAndUntrustedRoot(t *testing.T) {
	t.Setenv(accountDescendantReadsEnv, "1")
	for _, change := range []string{"revoke", "move", "untrusted-root"} {
		t.Run(change, func(t *testing.T) {
			r := boundedDiscoveryFixture(2)
			c := &accountDiscoveryCursor{}
			if _, _, err := nextReadableAccountCandidate(context.Background(), r, "reader", c); err != nil {
				t.Fatal(err)
			}
			if _, _, err := nextReadableAccountCandidate(context.Background(), r, "reader", c); err != nil {
				t.Fatal(err)
			}
			switch change {
			case "revoke":
				delete(r.hierarchy.members, memberKey("parent", "reader"))
			case "move":
				r.hierarchy.accounts[c.Node].ParentAccountID = "sibling"
			case "untrusted-root":
				c.Root = "sibling"
			}
			if _, _, err := nextReadableAccountCandidate(context.Background(), r, "reader", c); !errors.Is(err, ErrStaleReadingCursor) {
				t.Fatalf("invalid cursor accepted: %v", err)
			}
		})
	}
}

func TestAccountDiscoveryKeepsDirectGrantWithBrokenAncestry(t *testing.T) {
	t.Setenv(accountDescendantReadsEnv, "1")
	for _, broken := range []string{"missing", "cycle"} {
		t.Run(broken, func(t *testing.T) {
			r := boundedDiscoveryFixture(0)
			r.accountIndex = []string{"direct-child"}
			r.hierarchy.accounts["direct-child"] = &model.Account{
				PK: "ACCOUNT#direct-child", SK: "META", AccountID: "direct-child",
				ParentAccountID: "parent", EntityType: model.EntityTypeAccount,
			}
			r.hierarchy.members[memberKey("direct-child", "reader")] = &model.AccountMember{
				PK: "ACCOUNT#direct-child", SK: "MEMBER#reader", AccountID: "direct-child",
				UserID: "reader", EntityType: model.EntityTypeAccountMember, Role: model.RoleAM,
			}
			if broken == "missing" {
				delete(r.hierarchy.accounts, "parent")
			} else {
				r.hierarchy.accounts["parent"].ParentAccountID = "direct-child"
			}
			ids, _, err := nextReadableAccountCandidate(context.Background(), r, "reader", &accountDiscoveryCursor{})
			if err != nil || len(ids) != 1 || ids[0] != "direct-child" {
				t.Fatalf("ancestor de-duplication removed an independent direct grant: %v/%v", ids, err)
			}
		})
	}
}

func TestAccountDiscoveryKeepsDirectGrantAcrossSkippedAncestorRoutes(t *testing.T) {
	t.Setenv(accountDescendantReadsEnv, "1")
	for _, barrier := range []string{"malformed-member", "missing-index-candidate"} {
		t.Run(barrier, func(t *testing.T) {
			r := boundedDiscoveryFixture(0)
			for id, parent := range map[string]string{"a": "", "b": "a", "c": "b"} {
				r.hierarchy.accounts[id] = &model.Account{PK: "ACCOUNT#" + id, SK: "META", AccountID: id,
					ParentAccountID: parent, EntityType: model.EntityTypeAccount}
			}
			for _, id := range []string{"a", "c"} {
				r.hierarchy.members[memberKey(id, "reader")] = &model.AccountMember{
					PK: "ACCOUNT#" + id, SK: "MEMBER#reader", AccountID: id, UserID: "reader",
					EntityType: model.EntityTypeAccountMember, Role: model.RoleAM,
				}
			}
			r.accountIndex = []string{"a", "c"}
			if barrier == "malformed-member" {
				r.hierarchy.members[memberKey("b", "reader")] = &model.AccountMember{
					PK: "ACCOUNT#b", SK: "MEMBER#reader", AccountID: "b", UserID: "other",
					EntityType: model.EntityTypeAccountMember, Role: model.RoleAM,
				}
			} else {
				r.accountIndex = []string{"c"}
			}
			c := &accountDiscoveryCursor{}
			found := false
			for n := 0; ; n++ {
				ids, done, err := nextReadableAccountCandidate(context.Background(), r, "reader", c)
				if err != nil {
					t.Fatal(err)
				}
				for _, id := range ids {
					found = found || id == "c"
				}
				if done {
					break
				}
				if n > 20 {
					t.Fatal("independent grant discovery never completed")
				}
			}
			if !found {
				t.Fatal("ancestor de-duplication hid the independently-held c grant")
			}
			assertIndependentRouteCollections(t, r)
		})
	}
}

type routeMeetingCatalogRepo struct {
	*descendantMeetingCatalogRepo
	discovery *boundedDiscoveryRepo
}

func (r *routeMeetingCatalogRepo) QueryNavigationCandidates(ctx context.Context, user, kind, account, after string, limit int) (*repository.NavigationCandidatePage, error) {
	return r.discovery.QueryNavigationCandidates(ctx, user, kind, account, after, limit)
}

type routeVaultRepo struct {
	*descendantVaultRepo
	discovery *boundedDiscoveryRepo
}

func (r *routeVaultRepo) QueryNavigationCandidates(ctx context.Context, user, kind, account, after string, limit int) (*repository.NavigationCandidatePage, error) {
	return r.discovery.QueryNavigationCandidates(ctx, user, kind, account, after, limit)
}

func assertIndependentRouteCollections(t *testing.T, r *boundedDiscoveryRepo) {
	t.Helper()
	api := newAccountServiceWithRepo(&boundedAccountListRepo{r.hierarchy.mockAccountRepo, r})
	q := url.Values{"pageSize": {"100"}}
	found := false
	for n := 0; ; n++ {
		page, err := api.ListAccountsPage(context.Background(), "reader", q)
		if err != nil {
			t.Fatal(err)
		}
		if !page.OverlapPossible || page.UnionKey != "accountId" {
			t.Fatal("account API omitted independent-root union contract")
		}
		for _, a := range page.Accounts {
			if a.AccountID == "c" {
				found = a.AccessPermission == model.PermissionEdit
			}
		}
		if page.NextCursor == "" {
			break
		}
		if n > 20 {
			t.Fatal("account API did not complete independent route")
		}
		q.Set("cursor", page.NextCursor)
	}
	if !found {
		t.Fatal("account API lost independent direct permission")
	}
	r.project("independent", "owner", "c")
	r.refs["c"] = []string{"independent"}
	navigation := NewNavigationCatalogService(r)
	for _, kind := range []string{"accounts", "projects"} {
		q, found = url.Values{"pageSize": {"1"}}, false
		for n := 0; ; n++ {
			page := navigationPage(t, navigation, kind, q)
			if page["overlapPossible"] != true {
				t.Fatal("navigation catalog omitted independent-root overlap")
			}
			for _, raw := range page[kind].([]interface{}) {
				row := raw.(map[string]interface{})
				if row["accountId"] == "c" || row["projectId"] == "independent" {
					found = row["accessPermission"] == model.PermissionEdit
				}
			}
			info := page["page"].(map[string]interface{})
			if info["complete"] == true {
				break
			}
			if n > 30 {
				t.Fatal("navigation catalog did not complete independent route")
			}
			q.Set("cursor", info["nextCursor"].(string))
		}
		if !found {
			t.Fatalf("%s catalog lost independent direct permission", kind)
		}
	}
	meetings := &catalogMeetingRepo{mockMeetingRepo: newMockMeetingRepo()}
	meetings.meetings["owner|independent"] = &model.Meeting{UserID: "owner", MeetingID: "independent",
		AccountID: "c", SharedToAccount: true, Title: "Shared"}
	meetings.meetingRefs["c"] = []model.MeetingRef{{MeetingID: "independent", OwnerUserID: "owner"}}
	mrepo := &routeMeetingCatalogRepo{&descendantMeetingCatalogRepo{meetings, r.hierarchy}, r}
	rows := collectCatalogMeetings(t, NewMeetingCatalogService(mrepo), meetings, MeetingCatalogIdentity{UserID: "reader"}, url.Values{})
	if _, ok := rows["independent"]; !ok {
		t.Fatal("meeting catalog lost independent direct route")
	}
	_, vault := vaultReadingFixture()
	vault.meetings = map[string]*model.Meeting{}
	vault.docs = map[string]map[string]*model.AccountDocument{
		"ACCOUNT#c": {"independent": {PK: "ACCOUNT#c", SK: "DOC#independent", AccountID: "c",
			DocID: "independent", Title: "Shared", Content: "Shared"}},
	}
	vrepo := &routeVaultRepo{&descendantVaultRepo{vault, r.hierarchy}, r}
	manifest := collectVaultManifest(t, &VaultReadingService{repo: vrepo})
	found = false
	for _, row := range manifest {
		if strings.Contains(row["path"].(string), "--c/") {
			found = true
		}
	}
	if !found {
		t.Fatal("vault manifest lost independent direct route")
	}
}

func TestAccountDiscoveryDeepTreeKeepsConstantCursor(t *testing.T) {
	t.Setenv(accountDescendantReadsEnv, "1")
	r := boundedDiscoveryFixture(0)
	parent := "parent"
	for depth := 1; depth <= repository.AccountHierarchyMaxAncestors; depth++ {
		id := fmt.Sprintf("00000000-0000-0000-0000-%012d", depth)
		r.hierarchy.accounts[id] = &model.Account{PK: "ACCOUNT#" + id, SK: "META", AccountID: id,
			ParentAccountID: parent, EntityType: model.EntityTypeAccount}
		parent = id
	}
	c := &accountDiscoveryCursor{}
	seen := map[string]bool{}
	for step := 0; ; step++ {
		before := r.accountReads
		ids, done, err := nextReadableAccountCandidate(context.Background(), r, "reader", c)
		if err != nil {
			t.Fatal(err)
		}
		if r.accountReads-before > 500 {
			t.Fatal("deep traversal exceeded the bounded canonical path work")
		}
		for _, id := range ids {
			if seen[id] {
				t.Fatalf("deep traversal duplicated %s", id)
			}
			seen[id] = true
		}
		raw, _ := json.Marshal(c)
		if len(raw) > 800 {
			t.Fatal("cursor embedded a growing ancestor frontier")
		}
		if done {
			break
		}
		if step > 300 {
			t.Fatal("deep traversal did not finish")
		}
	}
	if len(seen) != repository.AccountHierarchyMaxAncestors+1 {
		t.Fatalf("deep traversal omitted an authorized node: %d", len(seen))
	}
}

type boundedAccountListRepo struct {
	*mockAccountRepo
	discovery *boundedDiscoveryRepo
}

func (r *boundedAccountListRepo) QueryNavigationCandidates(ctx context.Context, user, kind, account, after string, limit int) (*repository.NavigationCandidatePage, error) {
	return r.discovery.QueryNavigationCandidates(ctx, user, kind, account, after, limit)
}
func (r *boundedAccountListRepo) GetAccount(ctx context.Context, id string) (*model.Account, error) {
	return r.discovery.GetAccount(ctx, id)
}
func (r *boundedAccountListRepo) GetMember(ctx context.Context, id, user string) (*model.AccountMember, error) {
	return r.discovery.GetMember(ctx, id, user)
}
func (r *boundedAccountListRepo) ListChildAccountIDsPage(ctx context.Context, id, cursor string, limit int32) ([]string, *string, error) {
	return r.discovery.ListChildAccountIDsPage(ctx, id, cursor, limit)
}

func TestAccountAPIBoundedDiscoveryFitsExistingPageBudget(t *testing.T) {
	t.Setenv(accountDescendantReadsEnv, "1")
	r := boundedDiscoveryFixture(1001)
	s := newAccountServiceWithRepo(&boundedAccountListRepo{r.hierarchy.mockAccountRepo, r})
	q := url.Values{"pageSize": {"100"}}
	seen := map[string]bool{}
	for n := 0; ; n++ {
		beforeQueries := r.queries + r.childQueries
		page, err := s.ListAccountsPage(context.Background(), "reader", q)
		if err != nil {
			t.Fatal(err)
		}
		if r.queries+r.childQueries-beforeQueries > 4 {
			t.Fatal("account API exceeded its fixed provider-step budget")
		}
		for _, a := range page.Accounts {
			if seen[a.AccountID] {
				t.Fatalf("duplicate account %s", a.AccountID)
			}
			seen[a.AccountID] = true
		}
		if page.NextCursor == "" {
			break
		}
		if n >= 999 {
			t.Fatal("bounded account discovery exceeded the existing frontend 1000-page budget")
		}
		q.Set("cursor", page.NextCursor)
	}
	if len(seen) != 1002 {
		t.Fatalf("account API omitted descendants: %d", len(seen))
	}
}

func TestAccountAPIBatchedPageRechecksEveryReturnedGrant(t *testing.T) {
	t.Setenv(accountDescendantReadsEnv, "1")
	for _, change := range []string{"revoke", "move"} {
		t.Run(change, func(t *testing.T) {
			r := boundedDiscoveryFixture(0)
			r.accountIndex = []string{"a-root", "b-root"}
			for _, id := range r.accountIndex {
				r.hierarchy.accounts[id] = &model.Account{PK: "ACCOUNT#" + id, SK: "META", AccountID: id, EntityType: model.EntityTypeAccount}
				r.hierarchy.members[memberKey(id, "reader")] = &model.AccountMember{
					PK: "ACCOUNT#" + id, SK: "MEMBER#reader", AccountID: id, UserID: "reader",
					EntityType: model.EntityTypeAccountMember, Role: model.RoleOwner,
				}
			}
			r.afterAccount = func(id string) {
				if id != "b-root" {
					return
				}
				r.afterAccount = nil
				if change == "revoke" {
					delete(r.hierarchy.members, memberKey("a-root", "reader"))
				} else {
					r.hierarchy.accounts["a-root"].ParentAccountID = "sibling"
				}
			}
			s := newAccountServiceWithRepo(&boundedAccountListRepo{r.hierarchy.mockAccountRepo, r})
			if _, err := s.ListAccountsPage(context.Background(), "reader", url.Values{"pageSize": {"100"}}); !errors.Is(err, ErrStaleReadingCursor) {
				t.Fatalf("earlier source changed while later independent root was read: %v", err)
			}
		})
	}
}

func TestDescendantProjectCatalogContinuesAccountDiscovery(t *testing.T) {
	t.Setenv(accountDescendantReadsEnv, "1")
	r := boundedDiscoveryFixture(2)
	r.project("linked", "owner", "child-0001")
	r.refs["child-0001"] = []string{"linked"}
	s := NewNavigationCatalogService(r)
	q := url.Values{"pageSize": {"1"}}
	found := false
	for n := 0; ; n++ {
		before := r.queries + r.childQueries
		page := navigationPage(t, s, "projects", q)
		if r.queries+r.childQueries-before > 2 {
			t.Fatal("project catalog expanded the whole account tree")
		}
		for _, raw := range page["projects"].([]interface{}) {
			row := raw.(map[string]interface{})
			if row["projectId"] == "linked" {
				if found || row["accessPermission"] != model.PermissionRead {
					t.Fatal("linked project duplicated or inherited permission escalated")
				}
				found = true
			}
		}
		info := page["page"].(map[string]interface{})
		if info["complete"] == true {
			break
		}
		if n > 30 {
			t.Fatal("project account discovery never completed")
		}
		q.Set("cursor", info["nextCursor"].(string))
	}
	if !found {
		t.Fatal("account traversal hid the inherited project")
	}
}

func TestDescendantMeetingCatalogContinuesAccountDiscovery(t *testing.T) {
	t.Setenv(accountDescendantReadsEnv, "1")
	h := descendantContentFixture()
	r := &descendantMeetingCatalogRepo{&catalogMeetingRepo{mockMeetingRepo: newMockMeetingRepo()}, h}
	r.members["parent|reader"] = h.members[memberKey("parent", "reader")]
	r.meetings["owner|shared"] = &model.Meeting{UserID: "owner", MeetingID: "shared", AccountID: "child", SharedToAccount: true, Title: "Shared"}
	r.meetingRefs["child"] = []model.MeetingRef{{MeetingID: "shared", OwnerUserID: "owner"}}
	rows := collectCatalogMeetings(t, NewMeetingCatalogService(r), r.catalogMeetingRepo, MeetingCatalogIdentity{UserID: "reader"}, url.Values{})
	if row, ok := rows["shared"]; len(rows) != 1 || !ok || row.Permission == nil || *row.Permission != model.PermissionRead {
		t.Fatalf("meeting catalog lost inherited traversal: %+v", rows)
	}
}

func TestDescendantVaultManifestContinuesAccountDiscovery(t *testing.T) {
	t.Setenv(accountDescendantReadsEnv, "1")
	_, base := vaultReadingFixture()
	h := descendantContentFixture()
	base.members = map[string]*model.AccountMember{"parent|reader": h.members[memberKey("parent", "reader")]}
	base.docs["ACCOUNT#child"] = map[string]*model.AccountDocument{"doc": &h.documents["ACCOUNT#child"][0]}
	s := &VaultReadingService{repo: &descendantVaultRepo{base, h}}
	rows := collectVaultManifest(t, s)
	found := false
	for _, row := range rows {
		if strings.Contains(row["path"].(string), "child") {
			found = true
		}
	}
	if !found {
		t.Fatalf("vault manifest omitted inherited account: %+v", rows)
	}
}

func boundedDiscoveryFixture(count int) *boundedDiscoveryRepo {
	_, base := navigationFixture()
	hierarchy := descendantContentFixture()
	delete(hierarchy.accounts, "child")
	for n := 0; n < count; n++ {
		id := fmt.Sprintf("child-%04d", n)
		hierarchy.accounts[id] = &model.Account{
			PK: "ACCOUNT#" + id, SK: "META", AccountID: id,
			ParentAccountID: "parent", EntityType: model.EntityTypeAccount,
		}
	}
	base.accountIndex = []string{"parent"}
	return &boundedDiscoveryRepo{descendantNavigationRepo: &descendantNavigationRepo{base, hierarchy}}
}

func TestAccountCatalogDiscoveryWorkDoesNotGrowWithTree(t *testing.T) {
	t.Setenv(accountDescendantReadsEnv, "1")
	for _, count := range []int{10, 1001} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			r := boundedDiscoveryFixture(count)
			_, err := NewNavigationCatalogService(r).Read(context.Background(), "reader", "accounts", url.Values{"pageSize": {"1"}})
			if err != nil {
				t.Fatalf("bounded first page must not enumerate the whole hierarchy: %v", err)
			}
			if r.childQueries > 1 || r.accountReads > 10 || r.queries > 1 {
				t.Fatalf("first page did unbounded work: children=%d accounts=%d indexes=%d", r.childQueries, r.accountReads, r.queries)
			}
		})
	}
}

func TestAccountCatalogCursorCannotRestartAfterRolloutChange(t *testing.T) {
	t.Setenv(accountDescendantReadsEnv, "0")
	s, r := navigationFixture()
	r.account("a", "reader")
	r.account("b", "reader")
	first := navigationPage(t, s, "accounts", url.Values{"pageSize": {"1"}})
	cursor := first["page"].(map[string]interface{})["nextCursor"].(string)
	t.Setenv(accountDescendantReadsEnv, "1")
	if _, err := s.Read(context.Background(), "reader", "accounts", url.Values{"pageSize": {"1"}, "cursor": {cursor}}); !errors.Is(err, ErrStaleReadingCursor) {
		t.Fatalf("old index cursor restarted descendant traversal: %v", err)
	}
}
