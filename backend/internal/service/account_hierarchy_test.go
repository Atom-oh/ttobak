package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

// The fake serializes conditional writes while returning independent snapshots
// on reads. Hooks run before a transaction, to reproduce stale-read races.
type hierarchyRepo struct {
	*mockAccountRepo
	mu       sync.Mutex
	reads    int
	writes   int
	before   func()
	writeErr error
}

func newHierarchyRepo() *hierarchyRepo {
	return &hierarchyRepo{mockAccountRepo: newMockAccountRepo()}
}

func (r *hierarchyRepo) seed(id, parent, owner string) {
	r.accounts[id] = &model.Account{AccountID: id, ParentAccountID: parent, Name: id, OwnerUserID: owner}
	r.members[memberKey(id, owner)] = &model.AccountMember{AccountID: id, UserID: owner, Role: model.RoleOwner}
}

func (r *hierarchyRepo) grant(account, user string) {
	r.members[memberKey(account, user)] = &model.AccountMember{AccountID: account, UserID: user, Role: model.RoleTAM}
}

func (r *hierarchyRepo) GetAccount(ctx context.Context, id string) (*model.Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reads++
	return r.mockAccountRepo.GetAccount(ctx, id)
}

func (r *hierarchyRepo) GetMember(ctx context.Context, id, user string) (*model.AccountMember, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reads++
	return r.mockAccountRepo.GetMember(ctx, id, user)
}

func (r *hierarchyRepo) CreateAccount(ctx context.Context, a *model.Account, owner *model.AccountMember) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.writes++
	return r.mockAccountRepo.CreateAccount(ctx, a, owner)
}

func (r *hierarchyRepo) checkParent(user, parent string, ancestors []repository.AccountParentLink) error {
	if parent != "" && r.members[memberKey(parent, user)] == nil {
		return repository.ErrConditionFailed
	}
	for _, observed := range ancestors {
		a := r.accounts[observed.AccountID]
		if a == nil || a.ParentAccountID != observed.ParentAccountID {
			return repository.ErrConditionFailed
		}
	}
	return r.writeErr
}

func (r *hierarchyRepo) UpdateAccountParent(_ context.Context, id, user, oldParent, parent string, ancestors []repository.AccountParentLink) error {
	if r.before != nil {
		r.before()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.writes++
	a, member := r.accounts[id], r.members[memberKey(id, user)]
	if a == nil || a.ParentAccountID != oldParent || a.OwnerUserID != user || member == nil || member.Role != model.RoleOwner {
		return repository.ErrConditionFailed
	}
	if err := r.checkParent(user, parent, ancestors); err != nil {
		return err
	}
	a.ParentAccountID = parent
	return nil
}

func (r *hierarchyRepo) CreateAccountWithParent(ctx context.Context, a *model.Account, owner *model.AccountMember, ancestors []repository.AccountParentLink) error {
	if r.before != nil {
		r.before()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.writes++
	if r.accounts[a.AccountID] != nil || r.members[memberKey(a.AccountID, owner.UserID)] != nil {
		return repository.ErrConditionFailed
	}
	if err := r.checkParent(owner.UserID, a.ParentAccountID, ancestors); err != nil {
		return err
	}
	return r.mockAccountRepo.CreateAccount(ctx, a, owner)
}

func TestAccountHierarchyAuthorizationAndCycles(t *testing.T) {
	for _, tc := range []struct {
		name, user, child, parent string
		setup                     func(*hierarchyRepo)
		want                      error
	}{
		{name: "owner can attach", user: "owner", child: "child", parent: "parent"},
		{name: "member cannot move", user: "member", child: "child", parent: "parent", want: ErrForbidden},
		{name: "stranger cannot move", user: "stranger", child: "child", parent: "parent", want: ErrForbidden},
		{name: "missing child", user: "owner", child: "missing", parent: "parent", want: ErrNotFound},
		{name: "parent membership required", user: "owner", child: "child", parent: "private", want: ErrForbidden},
		{name: "missing parent", user: "owner", child: "child", parent: "missing", want: ErrNotFound},
		{name: "self parent", user: "owner", child: "child", parent: "child", want: ErrInvalidInput},
		{name: "descendant cycle", user: "owner", child: "child", parent: "parent", setup: func(r *hierarchyRepo) {
			r.accounts["parent"].ParentAccountID = "child"
		}, want: ErrInvalidInput},
		{name: "existing ancestor cycle", user: "owner", child: "child", parent: "parent", setup: func(r *hierarchyRepo) {
			r.accounts["parent"].ParentAccountID = "private"
			r.accounts["private"].ParentAccountID = "parent"
		}, want: ErrInvalidInput},
		{name: "broken ancestry", user: "owner", child: "child", parent: "parent", setup: func(r *hierarchyRepo) {
			r.accounts["parent"].ParentAccountID = "missing"
		}, want: ErrNotFound},
		{name: "private ancestor needs no membership", user: "owner", child: "child", parent: "parent", setup: func(r *hierarchyRepo) {
			r.accounts["parent"].ParentAccountID = "private"
		}},
		{name: "detach without old parent access", user: "owner", child: "child", setup: func(r *hierarchyRepo) {
			r.accounts["child"].ParentAccountID = "private"
		}},
		{name: "root detach", user: "owner", child: "child"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newHierarchyRepo()
			r.seed("child", "", "owner")
			r.seed("parent", "", "other")
			r.seed("private", "", "other")
			r.grant("child", "member")
			r.grant("parent", "owner")
			if tc.setup != nil {
				tc.setup(r)
			}
			before, beforeMembers := "", len(r.members)
			if a := r.accounts[tc.child]; a != nil {
				before = a.ParentAccountID
			}
			s := newAccountServiceWithRepo(r)
			got, err := s.UpdateAccountParent(context.Background(), tc.user, tc.child, &model.UpdateAccountParentRequest{ParentAccountID: &tc.parent})
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
			if tc.want == nil {
				if got.AccountID != tc.child || got.ParentAccountID != tc.parent || r.accounts[tc.child].ParentAccountID != tc.parent {
					t.Fatalf("parent not persisted/returned: %+v", got)
				}
			} else if a := r.accounts[tc.child]; a != nil && a.ParentAccountID != before {
				t.Fatal("failed operation changed parent")
			}
			if len(r.members) != beforeMembers {
				t.Fatal("hierarchy operation changed access grants")
			}
		})
	}
}

func TestAccountHierarchyRequiredParent(t *testing.T) {
	s := newAccountServiceWithRepo(newHierarchyRepo())
	for _, req := range []*model.UpdateAccountParentRequest{nil, {}} {
		if _, err := s.UpdateAccountParent(context.Background(), "owner", "child", req); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("missing parent accepted: %v", err)
		}
	}
}

func TestAccountHierarchyRejectsMalformedIDsBeforeRepositoryAccess(t *testing.T) {
	for _, tc := range []struct {
		name, id string
	}{
		{"empty child", ""},
		{"traversal", ".."},
		{"slash", "/"},
		{"path", "account/a"},
		{"backslash", `account\a`},
		{"dot", "account.a"},
		{"spaces only", "   "},
		{"tab only", "\t"},
		{"newline only", "\n"},
		{"leading whitespace", " account-a"},
		{"trailing whitespace", "account-a "},
		{"internal whitespace", "account a"},
		{"encoded separator", "account%2Fa"},
		{"non-ASCII", "계정"},
		{"null byte", "account\x00"},
		{"DEL byte", "account\x7f"},
		{"129 bytes", strings.Repeat("a", 129)},
		{"oversized DynamoDB key", strings.Repeat("a", 2049)},
	} {
		for _, operation := range []string{"update child", "update parent", "create parent"} {
			// The exact empty parent is allowed for both detach and root creation.
			if tc.id == "" && operation != "update child" {
				continue
			}
			t.Run(tc.name+"/"+operation, func(t *testing.T) {
				r := newHierarchyRepo()
				r.seed("account-a", "", "owner")
				r.seed("parent-a", "", "owner")
				s := newAccountServiceWithRepo(r)
				var err error
				if operation == "create parent" {
					_, err = s.CreateAccount(context.Background(), "owner", "owner@example.com",
						&model.CreateAccountRequest{Name: "Account", ParentAccountID: tc.id})
				} else {
					child, parent := "account-a", "parent-a"
					if operation == "update child" {
						child = tc.id
					} else {
						parent = tc.id
					}
					_, err = s.UpdateAccountParent(context.Background(), "owner", child,
						&model.UpdateAccountParentRequest{ParentAccountID: &parent})
				}
				if !errors.Is(err, ErrInvalidInput) {
					t.Errorf("error=%v, want ErrInvalidInput", err)
				}
				if r.reads != 0 || r.writes != 0 {
					t.Errorf("malformed ID reached repository: reads=%d writes=%d", r.reads, r.writes)
				}
			})
		}
	}
}

func TestAccountHierarchyAcceptsValidIDBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, child, parent string
	}{
		{"fixture IDs", "account-a", "account-b"},
		{"ASCII alphabet", "Account_09-A", "Parent_B2-03"},
		{"UUIDs", "69e04b41-c359-42c4-b6b1-c0ec35d046d2", "60983978-8d47-4d98-9bc2-33183a84b704"},
		{"single character", "a", "B"},
		{"128 bytes", strings.Repeat("a", 128), strings.Repeat("b", 128)},
		{"exact empty parent", "account-a", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newHierarchyRepo()
			r.seed(tc.child, "", "owner")
			if tc.parent != "" {
				r.seed(tc.parent, "", "owner")
			}
			s := newAccountServiceWithRepo(r)
			response, err := s.UpdateAccountParent(context.Background(), "owner", tc.child,
				&model.UpdateAccountParentRequest{ParentAccountID: &tc.parent})
			if err != nil {
				t.Fatalf("valid update rejected: %v", err)
			}
			if response.AccountID != tc.child || response.ParentAccountID != tc.parent ||
				r.accounts[tc.child].ParentAccountID != tc.parent {
				t.Fatalf("update normalized or lost IDs: %+v", response)
			}
			created, err := s.CreateAccount(context.Background(), "owner", "owner@example.com",
				&model.CreateAccountRequest{Name: "Account", ParentAccountID: tc.parent})
			if err != nil {
				t.Fatalf("valid creation rejected: %v", err)
			}
			if created.ParentAccountID != tc.parent || r.accounts[created.AccountID].ParentAccountID != tc.parent {
				t.Fatalf("creation normalized or lost parent: %+v", created)
			}
		})
	}
}

func TestAccountHierarchyDepthBound(t *testing.T) {
	for _, count := range []int{64, 65} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			r := newHierarchyRepo()
			r.seed("child", "", "owner")
			for i := 0; i < count; i++ {
				parent := ""
				if i+1 < count {
					parent = fmt.Sprintf("a%d", i+1)
				}
				r.seed(fmt.Sprintf("a%d", i), parent, "owner")
			}
			parent := "a0"
			_, err := newAccountServiceWithRepo(r).UpdateAccountParent(context.Background(), "owner", "child", &model.UpdateAccountParentRequest{ParentAccountID: &parent})
			if count == 64 && err != nil || count == 65 && !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("depth %d: %v", count, err)
			}
		})
	}
}

func TestAccountHierarchyConflictFreshReads(t *testing.T) {
	for _, change := range []string{"ancestor", "child", "membership", "owner", "deleted parent", "deleted child", "exhausted", "storage error"} {
		t.Run(change, func(t *testing.T) {
			r := newHierarchyRepo()
			r.seed("child", "", "owner")
			r.seed("parent", "root", "owner")
			r.seed("root", "", "owner")
			var once sync.Once
			storageErr := errors.New("storage unavailable")
			r.before = func() {
				once.Do(func() {
					switch change {
					case "ancestor":
						r.accounts["root"].ParentAccountID = "child"
					case "child":
						r.accounts["child"].ParentAccountID = "root"
					case "membership":
						delete(r.members, memberKey("parent", "owner"))
					case "owner":
						r.members[memberKey("child", "owner")].Role = model.RoleTAM
					case "deleted parent":
						delete(r.accounts, "parent")
					case "deleted child":
						delete(r.accounts, "child")
					case "exhausted":
						r.writeErr = fmt.Errorf("racing: %w", repository.ErrConditionFailed)
					case "storage error":
						r.writeErr = storageErr
					}
				})
			}
			parent := "parent"
			_, err := newAccountServiceWithRepo(r).UpdateAccountParent(context.Background(), "owner", "child", &model.UpdateAccountParentRequest{ParentAccountID: &parent})
			want, writes := ErrInvalidInput, 1
			switch change {
			case "child":
				want, writes = nil, 2
			case "membership", "owner":
				want = ErrForbidden
			case "deleted parent", "deleted child":
				want = ErrNotFound
			case "exhausted":
				want, writes = ErrAccountHierarchyConflict, 3
			case "storage error":
				want = storageErr
			}
			if !errors.Is(err, want) || r.writes != writes {
				t.Fatalf("error=%v writes=%d, want %v/%d", err, r.writes, want, writes)
			}
		})
	}
}

func TestAccountHierarchyOpposingConcurrentMoves(t *testing.T) {
	r := newHierarchyRepo()
	r.seed("a", "", "owner")
	r.seed("b", "", "owner")
	var barrier sync.WaitGroup
	barrier.Add(2)
	r.before = func() { barrier.Done(); barrier.Wait() }
	s := newAccountServiceWithRepo(r)
	results := make(chan error, 2)
	for _, pair := range [][2]string{{"a", "b"}, {"b", "a"}} {
		go func(child, parent string) {
			_, err := s.UpdateAccountParent(context.Background(), "owner", child, &model.UpdateAccountParentRequest{ParentAccountID: &parent})
			results <- err
		}(pair[0], pair[1])
	}
	success, rejected := 0, 0
	for range 2 {
		err := <-results
		switch {
		case err == nil:
			success++
		case errors.Is(err, ErrInvalidInput):
			rejected++
		default:
			t.Fatalf("unexpected move result: %v", err)
		}
	}
	if success != 1 || rejected != 1 || r.accounts["a"].ParentAccountID == "b" && r.accounts["b"].ParentAccountID == "a" {
		t.Fatalf("opposing moves allowed cycle: success=%d rejected=%d", success, rejected)
	}
}

func TestAccountHierarchyCreateAtomicAndNoInheritedAccess(t *testing.T) {
	for _, scenario := range []string{"success", "missing", "forbidden", "revoked", "ancestor changed", "conflicts"} {
		t.Run(scenario, func(t *testing.T) {
			r := newHierarchyRepo()
			r.seed("parent", "", "other")
			r.grant("parent", "owner")
			var once sync.Once
			switch scenario {
			case "missing":
				delete(r.accounts, "parent")
			case "forbidden":
				delete(r.members, memberKey("parent", "owner"))
			case "revoked":
				r.before = func() { once.Do(func() { delete(r.members, memberKey("parent", "owner")) }) }
			case "ancestor changed":
				r.seed("root", "", "other")
				r.before = func() { once.Do(func() { r.accounts["parent"].ParentAccountID = "root" }) }
			case "conflicts":
				r.writeErr = repository.ErrConditionFailed
			}
			accounts, members := len(r.accounts), len(r.members)
			s := newAccountServiceWithRepo(r)
			a, err := s.CreateAccount(context.Background(), "owner", "owner@example.com", &model.CreateAccountRequest{Name: "자회사", ParentAccountID: "parent"})
			want := map[string]error{"missing": ErrNotFound, "forbidden": ErrForbidden, "revoked": ErrForbidden, "conflicts": ErrAccountHierarchyConflict}[scenario]
			if !errors.Is(err, want) {
				t.Fatalf("error=%v, want %v", err, want)
			}
			if err != nil {
				if len(r.accounts) != accounts || len(r.members) > members {
					t.Fatal("failed creation left a partial account/grant")
				}
				return
			}
			if a.ParentAccountID != "parent" || len(r.accounts) != accounts+1 || len(r.members) != members+1 {
				t.Fatalf("creation not atomic: %+v", a)
			}
			if _, err := s.GetAccount(context.Background(), "other", a.AccountID); !errors.Is(err, ErrForbidden) {
				t.Fatalf("parent owner inherited child access: %v", err)
			}
		})
	}
}

func TestAccountHierarchyResponsesAndLegacyRoots(t *testing.T) {
	r := newHierarchyRepo()
	r.seed("child", "private-parent", "owner")
	r.seed("legacy", "", "owner")
	s := newAccountServiceWithRepo(r)
	list, err := s.ListAccounts(context.Background(), "owner")
	if err != nil || len(list) != 2 {
		t.Fatalf("list=%+v error=%v", list, err)
	}
	for _, item := range list {
		detail, err := s.GetAccount(context.Background(), "owner", item.AccountID)
		if err != nil {
			t.Fatal(err)
		}
		if detail.ParentAccountID != item.ParentAccountID {
			t.Fatal("list/detail hierarchy mismatch")
		}
		for _, dto := range []any{item, detail} {
			raw, err := json.Marshal(dto)
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]any
			if err := json.Unmarshal(raw, &fields); err != nil {
				t.Fatal(err)
			}
			parent, present := fields["parentAccountId"]
			if item.AccountID == "legacy" && present || item.AccountID == "child" && parent != "private-parent" {
				t.Fatalf("incorrect optional field: %s", raw)
			}
		}
	}
}
