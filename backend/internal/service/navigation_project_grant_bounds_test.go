package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"testing"

	"github.com/ttobak/backend/internal/model"
)

type navigationGrantBoundsRepo struct {
	*navigationRepo
	membersRead  int
	projectsRead int
	onMember     func(string)
}

func (r *navigationGrantBoundsRepo) GetMember(ctx context.Context, account, user string) (*model.AccountMember, error) {
	r.membersRead++
	member, err := r.navigationRepo.GetMember(ctx, account, user)
	if r.onMember != nil {
		r.onMember(account)
	}
	return member, err
}

func (r *navigationGrantBoundsRepo) GetProjectCatalogMetadata(ctx context.Context, id string) (*model.Project, error) {
	r.projectsRead++
	return r.navigationRepo.GetProjectCatalogMetadata(ctx, id)
}

func navigationGrantBoundsFixture(t *testing.T, links, projects int, direct bool) (*NavigationCatalogService, *navigationGrantBoundsRepo, url.Values) {
	t.Helper()
	t.Setenv(accountDescendantReadsEnv, "1")
	_, base := navigationFixture()
	r := &navigationGrantBoundsRepo{navigationRepo: base}
	for _, pair := range [][2]string{{"root", ""}, {"child", "root"}} {
		base.accounts[pair[0]] = &model.Account{PK: model.PrefixAccount + pair[0], SK: model.SKAccountMeta,
			EntityType: model.EntityTypeAccount, AccountID: pair[0], ParentAccountID: pair[1]}
	}
	base.members["root/reader"] = &model.AccountMember{PK: model.PrefixAccount + "root",
		SK: model.PrefixMember + "reader", EntityType: model.EntityTypeAccountMember, AccountID: "root", UserID: "reader", Role: "viewer"}
	accounts := []string{"child"}
	for i := 0; i < links; i++ {
		accounts = append(accounts, fmt.Sprintf("linked-%04d", i))
	}
	if direct {
		id := accounts[len(accounts)-1]
		// Legacy direct identity remains valid without synthetic canonical rows.
		base.members[id+"/reader"] = &model.AccountMember{AccountID: id, UserID: "reader", Role: "viewer"}
	}
	for i := 0; i < projects; i++ {
		id := fmt.Sprintf("project-%02d", i)
		base.project(id, "author", accounts...)
		base.refs["child"] = append(base.refs["child"], id)
	}
	grant, err := resolveAccountReadGrant(context.Background(), r, "child", "reader")
	if err != nil || grant == nil {
		t.Fatal("invalid fixture grant", err)
	}
	c := navigationCursor{Version: 1, Binding: readingHash([]string{"navigation-descendant-v3", "reader", "projects"}),
		Phase: "accounts", Account: "child", AccountGrant: grant.fingerprint(), LastAccount: true}
	raw, _ := json.Marshal(c)
	r.membersRead, r.projectsRead = 0, 0
	return NewNavigationCatalogService(r), r, url.Values{"pageSize": {"50"}, "cursor": {base64.RawURLEncoding.EncodeToString(raw)}}
}

func TestNavigationProjectGrantScanHasFixedPerRequestBounds(t *testing.T) {
	for _, links := range []int{120, 600} {
		for _, direct := range []bool{false, true} {
			t.Run(fmt.Sprintf("links=%d/direct=%t", links, direct), func(t *testing.T) {
				s, r, q := navigationGrantBoundsFixture(t, links, 2, direct)
				seen, pages, maxMembers := map[string]bool{}, 0, 0
				for ; pages < 100; pages++ {
					r.membersRead, r.projectsRead, r.queries = 0, 0, 0
					value := navigationPage(t, s, "projects", q)
					// Fifty linked checks plus bounded route/selected grant proofs,
					// irrespective of link count, pageSize, or emitted dependencies.
					if r.membersRead > 70 || r.projectsRead > 5 || r.queries > 1 {
						t.Fatalf("unbounded authorization: members=%d metadata=%d queries=%d", r.membersRead, r.projectsRead, r.queries)
					}
					if r.membersRead > maxMembers {
						maxMembers = r.membersRead
					}
					for _, raw := range value["projects"].([]interface{}) {
						row := raw.(map[string]interface{})
						id := row["projectId"].(string)
						if seen[id] {
							t.Fatal("duplicate project", id)
						}
						seen[id] = true
						want := model.PermissionRead
						if direct {
							want = model.PermissionEdit
						}
						if row["accessPermission"] != want {
							t.Fatalf("direct grant precedence lost: %#v", row)
						}
					}
					page := value["page"].(map[string]interface{})
					if page["complete"] == true {
						break
					}
					q.Set("cursor", page["nextCursor"].(string))
				}
				if len(seen) != 2 || pages >= 100 || pages < links/50 {
					t.Fatalf("did not resume complete authorization: projects=%d pages=%d", len(seen), pages)
				}
				t.Logf("links=%d max member reads/request=%d pages=%d", links, maxMembers, pages+1)
			})
		}
	}
}

func TestNavigationProjectGrantRaceAtPublicationFailsClosed(t *testing.T) {
	s, r, q := navigationGrantBoundsFixture(t, 120, 1, true)
	for i := 0; i < 2; i++ {
		value := navigationPage(t, s, "projects", q)
		q.Set("cursor", value["page"].(map[string]interface{})["nextCursor"].(string))
	}
	r.membersRead = 0
	r.onMember = func(id string) {
		if id == "linked-0119" {
			delete(r.members, id+"/reader")
		}
	}
	_, err := s.Read(context.Background(), "reader", "projects", q)
	if !errors.Is(err, ErrStaleReadingCursor) || r.membersRead > 70 {
		t.Fatalf("revocation after selecting direct grant: err=%v member reads=%d", err, r.membersRead)
	}
}

func TestNavigationPendingProjectCursorCannotSkipSourceAuthorization(t *testing.T) {
	for _, tamper := range []string{"negative-offset", "partial-step", "past-end", "private-project"} {
		t.Run(tamper, func(t *testing.T) {
			s, r, q := navigationGrantBoundsFixture(t, 120, 1, true)
			value := navigationPage(t, s, "projects", q)
			c, err := decodeNavigationCursor(value["page"].(map[string]interface{})["nextCursor"].(string))
			if err != nil {
				t.Fatal(err)
			}
			switch tamper {
			case "negative-offset":
				c.ProjectScan.Offset = -50
			case "partial-step":
				c.ProjectScan.Offset = 1
			case "past-end":
				c.ProjectScan.Offset = 500
			case "private-project":
				r.project("private", "author", "unrelated")
				p, _ := normalizedProjectMetadata(r.projects["private"])
				c.ProjectScan.ID, c.ProjectScan.Revision = "private", readingHash(p)
			}
			raw, _ := json.Marshal(c)
			q.Set("cursor", base64.RawURLEncoding.EncodeToString(raw))
			_, err = s.Read(context.Background(), "reader", "projects", q)
			if !errors.Is(err, ErrInvalidReadingCursor) && !errors.Is(err, ErrStaleReadingCursor) {
				t.Fatalf("untrusted scan accepted: %v", err)
			}
		})
	}
}

func TestNavigationProjectGrantOwnerAndAllMemberRolesRemainEditable(t *testing.T) {
	for _, source := range []string{"owner", "project-member", "linked-account"} {
		for _, role := range append([]string{model.RoleOwner, ""}, model.AssignableRoles...) {
			t.Run(source+"/"+role, func(t *testing.T) {
				s, r, q := navigationGrantBoundsFixture(t, 600, 1, false)
				switch source {
				case "owner":
					r.projects["project-00"].OwnerUserID = "reader"
				case "project-member":
					r.direct["project-00/reader"] = &model.ProjectMember{ProjectID: "project-00", UserID: "reader"}
				case "linked-account":
					// The first linked account exercises the short scan path.
					r.members["linked-0000/reader"] = &model.AccountMember{AccountID: "linked-0000", UserID: "reader", Role: role}
				}
				value := navigationPage(t, s, "projects", q)
				rows := value["projects"].([]interface{})
				if len(rows) != 1 || rows[0].(map[string]interface{})["accessPermission"] != model.PermissionEdit ||
					rows[0].(map[string]interface{})["inheritedFromAccountId"] != nil || r.membersRead > 20 {
					t.Fatalf("independent %s/%s grant lost or unbounded: %#v reads=%d", source, role, rows, r.membersRead)
				}
			})
		}
	}
}

func TestNavigationFlagOffLegacyProjectAnchorStillValidates(t *testing.T) {
	for _, via := range []string{"owned", "direct", "accounts"} {
		t.Run(via, func(t *testing.T) {
			t.Setenv(accountDescendantReadsEnv, "0")
			s, r := navigationFixture()
			r.account("account", "reader")
			r.project("project", "reader", "account")
			r.direct["project/reader"] = &model.ProjectMember{ProjectID: "project", UserID: "reader"}
			account := ""
			if via == "accounts" {
				account = "account"
			}
			item, _, err := s.project(context.Background(), "reader", "project", via, account)
			if err != nil {
				t.Fatal(err)
			}
			// Remove fields added by the resumable grant contract.
			item.anchor.GrantKind, item.anchor.GrantAccount, item.anchor.RouteGrant = "", "", ""
			c := navigationCursor{Version: 1, Binding: readingHash([]string{"navigation-v1", "reader", "projects"}),
				Phase: "owned", Anchor: item.anchor}
			raw, _ := json.Marshal(c)
			q := url.Values{"cursor": {base64.RawURLEncoding.EncodeToString(raw)}}
			if _, err := s.Read(context.Background(), "reader", "projects", q); err != nil {
				t.Fatalf("legacy flag-off anchor rejected: %v", err)
			}
			delete(r.members, "account/reader")
			delete(r.direct, "project/reader")
			r.projects["project"].OwnerUserID = "other"
			if _, err := s.Read(context.Background(), "reader", "projects", q); !errors.Is(err, ErrStaleReadingCursor) {
				t.Fatalf("legacy stale anchor accepted: %v", err)
			}
		})
	}
}

func TestNavigationPendingProjectGrantRejectsCurrentSourceChanges(t *testing.T) {
	for _, change := range []string{"route-revoked", "metadata-changed"} {
		t.Run(change, func(t *testing.T) {
			s, r, q := navigationGrantBoundsFixture(t, 120, 1, true)
			value := navigationPage(t, s, "projects", q)
			if len(value["projects"].([]interface{})) != 0 {
				t.Fatal("project published before direct grant search completed")
			}
			q.Set("cursor", value["page"].(map[string]interface{})["nextCursor"].(string))
			if change == "route-revoked" {
				delete(r.members, "root/reader")
			} else {
				r.projects["project-00"].Name = "changed"
			}
			_, err := s.Read(context.Background(), "reader", "projects", q)
			if !errors.Is(err, ErrStaleReadingCursor) {
				t.Fatalf("changed pending source accepted: %v", err)
			}
		})
	}
}

func TestNavigationSelectedDirectProjectGrantRevocationFailsClosed(t *testing.T) {
	s, r, q := navigationGrantBoundsFixture(t, 120, 1, true)
	for i := 0; i < 20; i++ {
		r.membersRead = 0
		value := navigationPage(t, s, "projects", q)
		page := value["page"].(map[string]interface{})
		if len(value["projects"].([]interface{})) != 0 {
			if page["complete"] == true {
				t.Fatal("fixture must leave a continuation for anchor validation")
			}
			q.Set("cursor", page["nextCursor"].(string))
			delete(r.members, "linked-0119/reader")
			r.membersRead = 0
			_, err := s.Read(context.Background(), "reader", "projects", q)
			if !errors.Is(err, ErrStaleReadingCursor) {
				t.Fatalf("revoked selected direct grant fell back to inherited read: %v", err)
			}
			if r.membersRead > 75 {
				t.Fatal("anchor validation rescanned account links")
			}
			return
		}
		q.Set("cursor", page["nextCursor"].(string))
	}
	t.Fatal("direct project never published")
}
