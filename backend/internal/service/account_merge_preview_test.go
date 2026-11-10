package service

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/ttobak/backend/internal/model"
)

// fakeMergeRepo is a read-only fixture; it has no write methods, so a Preview
// that wrote could not compile against it.
type fakeMergeRepo struct {
	accounts    map[string]*model.Account
	members     map[string]map[string]*model.AccountMember
	children    map[string][]string
	meetingRefs map[string][]model.MeetingRef
	meetings    map[string]*model.Meeting
	projects    map[string]int
	research    map[string]int
	contacts    map[string]int
	events      map[string]int
	prefixes    map[string]bool // accountID+"|"+prefix
}

func newFakeMergeRepo() *fakeMergeRepo {
	return &fakeMergeRepo{accounts: map[string]*model.Account{}, members: map[string]map[string]*model.AccountMember{},
		children: map[string][]string{}, meetingRefs: map[string][]model.MeetingRef{}, meetings: map[string]*model.Meeting{},
		projects: map[string]int{}, research: map[string]int{}, contacts: map[string]int{}, events: map[string]int{},
		prefixes: map[string]bool{}}
}

func (f *fakeMergeRepo) addAccount(id, parent string) *model.Account {
	a := &model.Account{AccountID: id, ParentAccountID: parent, OwnerUserID: "u1"}
	f.accounts[id] = a
	if parent != "" {
		f.children[parent] = append(f.children[parent], id)
	}
	return a
}

func (f *fakeMergeRepo) addMember(account, user, role string) {
	if f.members[account] == nil {
		f.members[account] = map[string]*model.AccountMember{}
	}
	f.members[account][user] = &model.AccountMember{AccountID: account, UserID: user, Role: role}
}

func (f *fakeMergeRepo) GetAccount(_ context.Context, id string) (*model.Account, error) {
	return f.accounts[id], nil
}
func (f *fakeMergeRepo) GetMember(_ context.Context, account, user string) (*model.AccountMember, error) {
	return f.members[account][user], nil
}
func (f *fakeMergeRepo) ListChildAccountIDsPage(_ context.Context, parent, cursor string, limit int32) ([]string, *string, error) {
	ids := f.children[parent]
	start := 0
	fmt.Sscanf(cursor, "%d", &start)
	end := min(start+int(limit), len(ids))
	var next *string
	if end < len(ids) {
		c := fmt.Sprint(end)
		next = &c
	}
	return ids[start:end], next, nil
}
func (f *fakeMergeRepo) ListMeetingRefsForAccountPage(_ context.Context, account, cursor string, limit int32) ([]model.MeetingRef, *string, error) {
	refs := f.meetingRefs[account]
	start := 0
	fmt.Sscanf(cursor, "%d", &start)
	end := min(start+int(limit), len(refs))
	var next *string
	if end < len(refs) {
		c := fmt.Sprint(end)
		next = &c
	}
	return refs[start:end], next, nil
}
func (f *fakeMergeRepo) GetMeetingPublication(_ context.Context, owner, id string) (*model.Meeting, error) {
	return f.meetings[owner+"/"+id], nil
}
func (f *fakeMergeRepo) ListAccountPartitionKeys(_ context.Context, a, prefix string, limit int) ([]string, bool, error) {
	var keys []string
	switch prefix {
	case model.PrefixMember:
		for user := range f.members[a] {
			keys = append(keys, prefix+user)
		}
	case model.PrefixProjectRef:
		keys = synthKeys(prefix, f.projects[a])
	case model.PrefixResearchRef:
		keys = synthKeys(prefix, f.research[a])
	case mergeContactPrefix:
		keys = synthKeys(prefix, f.contacts[a])
	case mergeEventPrefix:
		keys = synthKeys(prefix, f.events[a])
	}
	if len(keys) > limit {
		return keys[:limit], true, nil
	}
	return keys, false, nil
}
func synthKeys(prefix string, n int) []string {
	keys := make([]string, n)
	for i := range keys {
		keys[i] = fmt.Sprintf("%s%d", prefix, i)
	}
	return keys
}
func (f *fakeMergeRepo) AccountPartitionHasPrefix(_ context.Context, a, prefix string) (bool, error) {
	return f.prefixes[a+"|"+prefix], nil
}

// mergeFixture: u1 owns "a" and is a direct member of "b".
func mergeFixture() (*fakeMergeRepo, *AccountMergeService) {
	repo := newFakeMergeRepo()
	repo.addAccount("a", "")
	repo.addAccount("b", "")
	repo.addMember("a", "u1", model.RoleOwner)
	repo.addMember("b", "u1", model.RoleSA)
	return repo, NewAccountMergeService(repo, nil)
}

func TestAccountMergeAuthorize(t *testing.T) {
	chain := func(r *fakeMergeRepo, prefix string, n int, parent string) string {
		for i := 0; i < n; i++ {
			id := fmt.Sprintf("%s%d", prefix, i)
			r.addAccount(id, parent)
			parent = id
		}
		return parent
	}
	tests := []struct {
		name     string
		mutate   func(r *fakeMergeRepo)
		from, to string
		want     error
	}{
		{name: "ok", from: "a", to: "b"},
		{name: "same account", from: "a", to: "a", want: ErrInvalidInput},
		{name: "missing into", from: "a", to: "", want: ErrInvalidInput},
		{name: "member role only", from: "a", to: "b", want: ErrForbidden,
			mutate: func(r *fakeMergeRepo) { r.members["a"]["u1"].Role = model.RoleSA }},
		{name: "meta owner differs", from: "a", to: "b", want: ErrForbidden,
			mutate: func(r *fakeMergeRepo) { r.accounts["a"].OwnerUserID = "u2" }},
		{name: "not member of into", from: "a", to: "b", want: ErrForbidden,
			mutate: func(r *fakeMergeRepo) { delete(r.members["b"], "u1") }},
		{name: "into missing", from: "a", to: "zz", want: ErrNotFound},
		{name: "into descendant", from: "a", to: "c", want: ErrAccountMergeIneligible, mutate: func(r *fakeMergeRepo) {
			r.addAccount("c", "a")
			r.addMember("c", "u1", model.RoleSA)
		}},
		{name: "from merging", from: "a", to: "b", want: ErrAccountMergeIneligible,
			mutate: func(r *fakeMergeRepo) { r.accounts["a"].MergingInto = "x" }},
		{name: "from merged", from: "a", to: "b", want: ErrAccountMergeIneligible,
			mutate: func(r *fakeMergeRepo) { r.accounts["a"].MergedInto = "x" }},
		{name: "into merging", from: "a", to: "b", want: ErrAccountMergeIneligible,
			mutate: func(r *fakeMergeRepo) { r.accounts["b"].MergingInto = "x" }},
		{name: "into merged", from: "a", to: "b", want: ErrAccountMergeIneligible,
			mutate: func(r *fakeMergeRepo) { r.accounts["b"].MergedInto = "x" }},
		{name: "incoming merges", from: "a", to: "b", want: ErrAccountMergeIneligible,
			mutate: func(r *fakeMergeRepo) { r.accounts["a"].IncomingMerges = 1 }},
		{name: "depth 65", from: "a", to: "b", want: ErrAccountMergeIneligible, mutate: func(r *fakeMergeRepo) {
			// depth(b)=1 via parent "p"; subtree height of a = 64 -> 65
			r.accounts["b"].ParentAccountID = "p"
			r.addAccount("p", "")
			chain(r, "d", 64, "a")
		}},
		{name: "depth 64", from: "a", to: "b", mutate: func(r *fakeMergeRepo) {
			r.accounts["b"].ParentAccountID = "p"
			r.addAccount("p", "")
			chain(r, "d", 63, "a")
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo, svc := mergeFixture()
			if tc.mutate != nil {
				tc.mutate(repo)
			}
			got, err := svc.authorize(context.Background(), "u1", tc.from, tc.to)
			if tc.want == nil {
				if err != nil || got == nil || got.From.AccountID != "a" || got.Into.AccountID != "b" {
					t.Fatalf("got %+v, %v", got, err)
				}
				return
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("err=%v want %v", err, tc.want)
			}
		})
	}
}

func TestAccountMergeAuthorizeSubjects(t *testing.T) {
	repo, svc := mergeFixture()
	repo.accounts["b"].ParentAccountID = "p"
	repo.addAccount("p", "")
	repo.addAccount("a1", "a")
	repo.addAccount("a2", "a")
	repo.addAccount("a11", "a1")
	repo.children["a"] = append(repo.children["a"], "stale") // no META row: skipped
	got, err := svc.authorize(context.Background(), "u1", "a", "b")
	if err != nil {
		t.Fatal(err)
	}
	if got.SubtreeHeight != 2 || got.DirectChildren != 2 || len(got.IntoAncestors) != 1 || got.IntoAncestors[0].AccountID != "p" {
		t.Fatalf("subjects %+v", got)
	}
}

func TestAccountMergeAuthorizeRejectsOversizedSubtree(t *testing.T) {
	repo, svc := mergeFixture()
	for i := 0; i < mergeSubtreeMaxAccounts; i++ {
		repo.addAccount(fmt.Sprintf("k%d", i), "a")
	}
	if _, err := svc.authorize(context.Background(), "u1", "a", "b"); !errors.Is(err, ErrAccountMergeIneligible) {
		t.Fatalf("err=%v", err)
	}
}

func TestAccountMergePreviewCounts(t *testing.T) {
	repo, svc := mergeFixture()
	repo.addMember("a", "u2", model.RoleSA)
	repo.addMember("a", "u3", model.RoleSA)
	repo.addMember("b", "u2", model.RoleAM)
	repo.addMember("b", "u4", model.RoleSA)
	repo.accounts["b"].ParentAccountID = "p"
	repo.addAccount("p", "")
	repo.addAccount("a1", "a")
	for _, id := range []string{"m1", "m2", "m3"} {
		repo.meetingRefs["a"] = append(repo.meetingRefs["a"], model.MeetingRef{AccountID: "a", MeetingID: id, OwnerUserID: "o"})
		repo.meetings["o/"+id] = &model.Meeting{PK: "USER#o", SK: "MEETING#" + id, UserID: "o", MeetingID: id,
			EntityType: "MEETING", AccountID: "a", SharedToAccount: true}
	}
	repo.meetings["o/m2"].AccountID = "b" // already moved; stale ref
	repo.meetingRefs["a"] = append(repo.meetingRefs["a"], model.MeetingRef{AccountID: "a", MeetingID: "gone", OwnerUserID: "o"})
	repo.projects["a"], repo.research["a"], repo.contacts["a"], repo.events["a"] = 2, 1, 250, 7
	repo.prefixes["a|"+model.PrefixAccountKBRef] = true
	repo.prefixes["a|"+model.PrefixDictObs] = true

	got, err := svc.Preview(context.Background(), "u1", "a", "b")
	if err != nil {
		t.Fatal(err)
	}
	wantCounts := map[string]int{"members": 3, "meetings": 2, "projects": 2, "research": 1, "contacts": 250, "events": 7, "children": 1}
	if !reflect.DeepEqual(got.Counts, wantCounts) {
		t.Fatalf("counts %v want %v", got.Counts, wantCounts)
	}
	present := map[string]bool{}
	var order []string
	for _, u := range got.Unmoved {
		present[u.Category], order = u.Present, append(order, u.Category)
		if (u.Category == "billing") != u.Unverifiable {
			t.Fatalf("unverifiable flag on %s", u.Category)
		}
	}
	if !reflect.DeepEqual(order, []string{"newsSources", "kb", "documents", "calendarShares", "dictionary", "billing"}) ||
		present["newsSources"] || !present["kb"] || present["documents"] || present["calendarShares"] || !present["dictionary"] || !present["billing"] {
		t.Fatalf("unmoved %+v", got.Unmoved)
	}
	if !reflect.DeepEqual(got.CLIOnly, []string{"specreqTasks", "calendarEvents", "linkOnlyMeetings"}) {
		t.Fatalf("cliOnly %v", got.CLIOnly)
	}
	if got.NewAudience.IntoMembersNotInFrom != 1 || !got.NewAudience.IntoHasAncestors || !got.RequiresAcceptUnmoved ||
		got.FromAccountID != "a" || got.IntoAccountID != "b" {
		t.Fatalf("audience/flags %+v", got)
	}
}

func TestAccountMergePreviewCapsAndEmpty(t *testing.T) {
	repo, svc := mergeFixture()
	repo.contacts["a"] = mergePreviewCountCap
	repo.events["a"] = mergePreviewCountCap + 1
	got, err := svc.Preview(context.Background(), "u1", "a", "b")
	if err != nil {
		t.Fatal(err)
	}
	if got.Counts["contacts"] != mergePreviewCountCap || got.Counts["events"] != -1 {
		t.Fatalf("caps: %v", got.Counts)
	}
	if got.NewAudience.IntoHasAncestors || got.NewAudience.IntoMembersNotInFrom != 0 {
		t.Fatalf("audience %+v", got.NewAudience)
	}
	for i := 0; i < mergePreviewMeetingCap+1; i++ {
		repo.meetingRefs["a"] = append(repo.meetingRefs["a"], model.MeetingRef{AccountID: "a", MeetingID: fmt.Sprint(i), OwnerUserID: "o"})
	}
	if got, err = svc.Preview(context.Background(), "u1", "a", "b"); err != nil || got.Counts["meetings"] != -1 {
		t.Fatalf("meetings cap: %v %v", got, err)
	}
}

func TestAccountMergePreviewRejectsIneligible(t *testing.T) {
	repo, svc := mergeFixture()
	repo.accounts["a"].MergedInto = "x"
	if _, err := svc.Preview(context.Background(), "u1", "a", "b"); !errors.Is(err, ErrAccountMergeIneligible) {
		t.Fatalf("err=%v", err)
	}
}
