package service

import (
	"context"
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

type navigationRepo struct {
	accounts      map[string]*model.Account
	members       map[string]*model.AccountMember
	projects      map[string]*model.Project
	direct        map[string]*model.ProjectMember
	accountIndex  []string
	ownedIndex    []string
	directIndex   []string
	refs          map[string][]string
	queries       int
	earlierChecks int
	afterProject  func(string)
	reverseSets   bool
}

func navigationFixture() (*NavigationCatalogService, *navigationRepo) {
	r := &navigationRepo{accounts: map[string]*model.Account{}, members: map[string]*model.AccountMember{},
		projects: map[string]*model.Project{}, direct: map[string]*model.ProjectMember{}, refs: map[string][]string{}}
	return NewNavigationCatalogService(r), r
}
func (r *navigationRepo) QueryNavigationCandidates(_ context.Context, user, kind, account, after string, limit int) (*repository.NavigationCandidatePage, error) {
	r.queries++
	var ids []string
	switch kind {
	case "accounts":
		ids = r.accountIndex
	case "owned":
		ids = r.ownedIndex
	case "direct":
		ids = r.directIndex
	case "account-projects":
		ids = r.refs[account]
	default:
		return nil, ErrInvalidInput
	}
	ids = append([]string{}, ids...)
	sort.Strings(ids)
	out := &repository.NavigationCandidatePage{}
	for _, id := range ids {
		if id <= after {
			continue
		}
		if len(out.IDs) == limit {
			out.NextID = out.IDs[len(out.IDs)-1]
			break
		}
		out.IDs = append(out.IDs, id)
	}
	return out, nil
}
func (r *navigationRepo) GetAccount(_ context.Context, id string) (*model.Account, error) {
	if r.accounts[id] == nil {
		return nil, nil
	}
	copy := *r.accounts[id]
	return &copy, nil
}
func (r *navigationRepo) GetMember(_ context.Context, id, user string) (*model.AccountMember, error) {
	if strings.HasPrefix(id, "earlier-") {
		r.earlierChecks++
	}
	if r.members[id+"/"+user] == nil {
		return nil, nil
	}
	copy := *r.members[id+"/"+user]
	return &copy, nil
}
func (r *navigationRepo) GetProjectMember(_ context.Context, id, user string) (*model.ProjectMember, error) {
	if r.direct[id+"/"+user] == nil {
		return nil, nil
	}
	copy := *r.direct[id+"/"+user]
	return &copy, nil
}
func (r *navigationRepo) GetProjectCatalogMetadata(_ context.Context, id string) (*model.Project, error) {
	if r.projects[id] == nil {
		return nil, nil
	}
	copy := *r.projects[id]
	copy.Description, copy.SfdcURL = "", ""
	copy.AccountIDs = append([]string{}, copy.AccountIDs...)
	r.reverseSets = !r.reverseSets
	if r.reverseSets {
		sort.Sort(sort.Reverse(sort.StringSlice(copy.AccountIDs)))
	}
	if r.afterProject != nil {
		callback := r.afterProject
		r.afterProject = nil
		callback(id)
	}
	return &copy, nil
}
func (r *navigationRepo) account(id, user string) {
	r.accountIndex = append(r.accountIndex, id)
	r.accounts[id] = &model.Account{AccountID: id, Name: "Account " + id}
	r.members[id+"/"+user] = &model.AccountMember{AccountID: id, UserID: user, Role: "viewer"}
}
func (r *navigationRepo) project(id, owner string, accounts ...string) {
	r.projects[id] = &model.Project{PK: "PROJECT#" + id, SK: "CONFIG", ProjectID: id,
		Name: "Project " + id, OwnerUserID: owner, AccountIDs: accounts, Description: strings.Repeat("PRIVATE_DESCRIPTION", 10000)}
}

func navigationPage(t *testing.T, s *NavigationCatalogService, kind string, q url.Values) map[string]interface{} {
	t.Helper()
	body, err := s.Read(context.Background(), "reader", kind, q)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) > 10000 || strings.Contains(string(body), "PRIVATE_DESCRIPTION") {
		t.Fatal("navigation exceeded its budget or loaded description data")
	}
	wrapped, err := json.Marshal(map[string]interface{}{
		"content":           []map[string]string{{"type": "text", "text": string(body)}},
		"structuredContent": json.RawMessage(body),
	})
	if err != nil || len(wrapped) > 32000 {
		t.Fatal("navigation does not fit a structured MCP result")
	}
	var value map[string]interface{}
	if json.Unmarshal(body, &value) != nil {
		t.Fatal("invalid navigation JSON")
	}
	return value
}

func TestProjectCatalogUnionsAllCurrentAccessPathsWithoutDuplicatesOrStaleIndexGrants(t *testing.T) {
	s, r := navigationFixture()
	r.account("account-a", "reader")
	r.account("account-b", "reader")
	r.accountIndex = append(r.accountIndex, "stale-account")
	r.project("owned", "reader", "account-a")
	r.project("direct", "author", "account-a")
	r.project("inherited", "author", "account-b", "account-a")
	r.project("closed", "author", "private-account")
	r.project("stale-owned", "author")
	r.project("stale-direct", "author")
	r.ownedIndex = []string{"owned", "stale-owned"}
	r.directIndex = []string{"owned", "direct", "stale-direct"}
	r.direct["owned/reader"] = &model.ProjectMember{ProjectID: "owned", UserID: "reader"}
	r.direct["direct/reader"] = &model.ProjectMember{ProjectID: "direct", UserID: "reader"}
	r.refs["account-a"] = []string{"owned", "direct", "inherited", "closed"}
	r.refs["account-b"] = []string{"inherited"}
	seen, q := map[string]bool{}, url.Values{"pageSize": {"1"}}
	for pages := 0; ; pages++ {
		before := r.queries
		value := navigationPage(t, s, "projects", q)
		if r.queries-before > 2 {
			t.Fatal("unbounded candidate queries in one request")
		}
		for _, raw := range value["projects"].([]interface{}) {
			id := raw.(map[string]interface{})["projectId"].(string)
			if seen[id] {
				t.Fatal("duplicate project", id)
			}
			seen[id] = true
		}
		page := value["page"].(map[string]interface{})
		if page["complete"] == true {
			break
		}
		if pages > 30 {
			t.Fatal("project catalog did not complete")
		}
		q.Set("cursor", page["nextCursor"].(string))
	}
	if len(seen) != 3 || !seen["owned"] || !seen["direct"] || !seen["inherited"] {
		t.Fatalf("incorrect authorized union: %v", seen)
	}
}

func TestProjectCatalogLargeAccountSetsDoNotInferPreviouslyEmittedProjects(t *testing.T) {
	s, r := navigationFixture()
	r.account("zz-account", "reader")
	links := []string{"zz-account"}
	for i := 0; i < 120; i++ {
		links = append(links, "earlier-"+strings.Repeat("x", i+1))
	}
	r.project("large", "author", links...)
	r.refs["zz-account"] = []string{"large"}
	q, seen := url.Values{}, 0
	for pages := 0; ; pages++ {
		before := r.earlierChecks
		value := navigationPage(t, s, "projects", q)
		if r.earlierChecks != before {
			t.Fatal("deduplication inferred an earlier path instead of using emitted IDs")
		}
		seen += len(value["projects"].([]interface{}))
		page := value["page"].(map[string]interface{})
		if page["complete"] == true {
			break
		}
		if pages > 12 {
			t.Fatal("bounded account check did not finish")
		}
		q.Set("cursor", page["nextCursor"].(string))
	}
	if seen != 1 || r.earlierChecks != 0 {
		t.Fatalf("project lost or inferred from another path: seen=%d checks=%d", seen, r.earlierChecks)
	}
}

func TestProjectCatalogExposesOverlapWhenExactCursorMemoryFills(t *testing.T) {
	s, r := navigationFixture()
	r.account("account", "reader")
	for i := 0; i < 40; i++ {
		id := fmt.Sprintf("%03d-%s", i, strings.Repeat("x", 120))
		r.project(id, "author", "account")
		r.directIndex = append(r.directIndex, id)
		r.direct[id+"/reader"] = &model.ProjectMember{ProjectID: id, UserID: "reader"}
		r.refs["account"] = append(r.refs["account"], id)
	}
	seen, q, duplicates, overlap := map[string]bool{}, url.Values{}, 0, false
	for pages := 0; ; pages++ {
		value := navigationPage(t, s, "projects", q)
		if value["unionKey"] != "projectId" {
			t.Fatal("missing union contract")
		}
		overlap = overlap || value["overlapPossible"] == true
		for _, raw := range value["projects"].([]interface{}) {
			id := raw.(map[string]interface{})["projectId"].(string)
			if seen[id] {
				duplicates++
				if value["overlapPossible"] != true {
					t.Fatal("overlap was not explicit")
				}
			}
			seen[id] = true
		}
		page := value["page"].(map[string]interface{})
		if page["complete"] == true {
			break
		}
		cursor := page["nextCursor"].(string)
		if len(cursor) > 2048 || pages > 30 {
			t.Fatal("unbounded cursor or nonadvancing catalog")
		}
		q.Set("cursor", cursor)
	}
	if len(seen) != 40 || duplicates == 0 || !overlap {
		t.Fatalf("bounded union lost data or hid overlap: seen=%d duplicates=%d overlap=%v", len(seen), duplicates, overlap)
	}
}

func TestAccountCatalogRechecksMembershipAndContinuationIdentity(t *testing.T) {
	s, r := navigationFixture()
	r.account("account-a", "reader")
	r.account("account-b", "reader")
	r.accountIndex = append(r.accountIndex, "stale-account")
	value := navigationPage(t, s, "accounts", url.Values{"pageSize": {"1"}})
	cursor := value["page"].(map[string]interface{})["nextCursor"].(string)
	q := url.Values{"cursor": {cursor}}
	if _, err := s.Read(context.Background(), "another", "accounts", q); !errors.Is(err, ErrStaleReadingCursor) {
		t.Fatal("cross-user navigation cursor accepted")
	}
	if _, err := s.Read(context.Background(), "reader", "projects", q); !errors.Is(err, ErrStaleReadingCursor) {
		t.Fatal("cross-kind navigation cursor accepted")
	}
	delete(r.members, "account-a/reader")
	if _, err := s.Read(context.Background(), "reader", "accounts", q); !errors.Is(err, ErrStaleReadingCursor) {
		t.Fatal("revoked account anchor accepted")
	}
	value = navigationPage(t, s, "accounts", nil)
	if len(value["accounts"].([]interface{})) != 1 ||
		value["accounts"].([]interface{})[0].(map[string]interface{})["accountId"] != "account-b" {
		t.Fatal("stale GSI candidates granted account access")
	}
}

func TestProjectCatalogRejectsConcurrentMetadataAndGrantChanges(t *testing.T) {
	for _, mutation := range []string{"metadata", "membership"} {
		s, r := navigationFixture()
		r.project("direct", "author")
		r.directIndex = []string{"direct"}
		r.direct["direct/reader"] = &model.ProjectMember{ProjectID: "direct", UserID: "reader"}
		first := navigationPage(t, s, "projects", nil)
		q := url.Values{"cursor": {first["page"].(map[string]interface{})["nextCursor"].(string)}}
		r.afterProject = func(id string) {
			if mutation == "metadata" {
				r.projects[id].Name = "changed"
			}
		}
		if mutation == "membership" {
			value := navigationPage(t, s, "projects", q)
			q.Set("cursor", value["page"].(map[string]interface{})["nextCursor"].(string))
			delete(r.direct, "direct/reader")
		}
		if body, err := s.Read(context.Background(), "reader", "projects", q); !errors.Is(err, ErrStaleReadingCursor) || body != nil {
			t.Fatalf("changed navigation source published: %s %v", body, err)
		}
	}
}

func TestProjectCatalogKeepsAvailablePathsWhenEarlierGSIEntriesLag(t *testing.T) {
	for _, lag := range []string{"account", "direct"} {
		t.Run(lag, func(t *testing.T) {
			s, r := navigationFixture()
			r.account("account-b", "reader")
			links := []string{"account-b"}
			if lag == "account" {
				links = append(links, "account-a")
				r.members["account-a/reader"] = &model.AccountMember{AccountID: "account-a", UserID: "reader", Role: "viewer"}
			} else {
				r.direct["project/reader"] = &model.ProjectMember{ProjectID: "project", UserID: "reader"}
			}
			// The primary grant is current, but its GSI candidate has not
			// appeared yet. The established account-b path remains available.
			r.project("project", "author", links...)
			r.refs["account-b"] = []string{"project"}
			q, found := url.Values{}, false
			for i := 0; i < 12; i++ {
				value := navigationPage(t, s, "projects", q)
				for _, raw := range value["projects"].([]interface{}) {
					if raw.(map[string]interface{})["projectId"] == "project" {
						found = true
					}
				}
				page := value["page"].(map[string]interface{})
				if page["complete"] == true {
					break
				}
				q.Set("cursor", page["nextCursor"].(string))
			}
			if !found {
				t.Fatal("an earlier lagging GSI path suppressed an available authorized project")
			}
		})
	}
}
