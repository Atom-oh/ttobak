package service

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/ttobak/backend/internal/model"
)

const onboardingTestBatch = "6f1d2c4e-8a5b-4c3d-9e7f-1a2b3c4d5e6f"

// onboardingAccountsFake serves GetAccount with members and records AddMember calls.
type onboardingAccountsFake struct {
	members  map[string][]model.AccountMemberDTO // accountID -> members
	denied   map[string]bool
	added    []model.AddMemberRequest
	addErr   map[string]error // email -> error
	pending  map[string]bool  // email -> pending outcome
	getCalls int
	getErr   map[string]error // accountID -> GetAccount error
}

func (fake *onboardingAccountsFake) GetAccount(_ context.Context, _, accountID string) (*model.AccountResponse, error) {
	fake.getCalls++
	if err := fake.getErr[accountID]; err != nil {
		return nil, err
	}
	if fake.denied[accountID] {
		return nil, ErrForbidden
	}
	members, ok := fake.members[accountID]
	if !ok {
		return nil, ErrNotFound
	}
	return &model.AccountResponse{AccountID: accountID, Members: members}, nil
}

func (fake *onboardingAccountsFake) AddMember(_ context.Context, _, _ string, req *model.AddMemberRequest) (*model.AccountMemberDTO, error) {
	fake.added = append(fake.added, *req)
	if err := fake.addErr[req.Email]; err != nil {
		return nil, err
	}
	if fake.pending[req.Email] {
		return &model.AccountMemberDTO{Email: req.Email, Role: req.Role, Pending: true}, nil
	}
	return &model.AccountMemberDTO{UserID: "u-" + req.Email, Email: req.Email, Role: req.Role}, nil
}

func onboardingTeam(account string) []model.SFDCTeamMember {
	return []model.SFDCTeamMember{
		{SFDCAccountID: account, Alias: "zsa", Name: "Z SA", TeamMemberRole: "Solutions Architect", AllTeamRoles: "Solutions Architect"},
		{SFDCAccountID: account, Alias: "dhlee", Name: "Dongho", TeamMemberRole: "Account Manager", AllTeamRoles: "Account Manager"},
		{SFDCAccountID: account, Alias: "atam", Name: "A TAM", TeamMemberRole: "Technical Account Manager", AllTeamRoles: "Technical Account Manager"},
		{SFDCAccountID: account, Alias: "bdm", Name: "BDM", TeamMemberRole: "Business Development Manager", AllTeamRoles: "Other"},
	}
}

func onboardingStore() *performanceStoreFake {
	return &performanceStoreFake{
		assignments: map[string][]model.SFDCAssignment{"owner": {{SFDCAccountID: "001A", Name: "Kakao Pay"}, {SFDCAccountID: "001B", Name: "Other"}}},
		mappings:    map[string][]model.AccountMapping{"owner": {{SFDCAccountID: "001A", AccountID: "acc1", Manual: true}}},
		team:        map[string][]model.SFDCTeamMember{"owner/001A": onboardingTeam("001A")},
	}
}

func TestOnboardingStatusCaseInsensitive(test *testing.T) {
	store := onboardingStore()
	store.invites = map[string][]model.PerformanceInvite{"owner/001A": {
		{SFDCAccountID: "001A", Alias: "atam", AccountID: "acc1", Role: model.RoleTAM, InvitedAt: "2026-11-01T00:00:00Z"},
		{SFDCAccountID: "001A", Alias: "zsa", AccountID: "acc1", Role: model.RoleSA, InvitedAt: "2026-09-01T00:00:00Z"}, // expired
	}}
	accounts := &onboardingAccountsFake{members: map[string][]model.AccountMemberDTO{"acc1": {{UserID: "u1", Email: "DHLee@Amazon.com", Role: model.RoleAM}}}}
	detail, err := performanceTestService(store, accounts).GetOnboarding(test.Context(), "owner", "001A")
	if err != nil {
		test.Fatal(err)
	}
	if !detail.Mapped || !detail.AccountReadable || detail.AccountID != "acc1" || detail.Name != "Kakao Pay" {
		test.Fatalf("detail header %+v", detail)
	}
	got := ""
	for _, member := range detail.Members {
		got += fmt.Sprintf("%s:%s:%s ", member.Alias, member.Role, member.Status)
	}
	if want := "dhlee:AM:member atam:TAM:pending zsa:SA:none bdm::none "; got != want {
		test.Fatalf("members %q, want %q", got, want)
	}
}

func TestOnboardingUnassignedNotFound(test *testing.T) {
	store := onboardingStore()
	store.team["owner/001Z"] = onboardingTeam("001Z")
	_, err := performanceTestService(store, &onboardingAccountsFake{}).GetOnboarding(test.Context(), "owner", "001Z")
	if !errors.Is(err, ErrNotFound) {
		test.Fatalf("unassigned SFDC account must be not found: %v", err)
	}
	if _, err := performanceTestService(store, &onboardingAccountsFake{}).GetOnboarding(test.Context(), "owner", "bad:id"); !errors.Is(err, ErrInvalidInput) {
		test.Fatalf("bad id: %v", err)
	}
}

func TestOnboardingUnreadableAccount(test *testing.T) {
	accounts := &onboardingAccountsFake{denied: map[string]bool{"acc1": true}}
	detail, err := performanceTestService(onboardingStore(), accounts).GetOnboarding(test.Context(), "owner", "001A")
	if err != nil {
		test.Fatal(err)
	}
	if !detail.Mapped || detail.AccountReadable || detail.AccountID != "" {
		test.Fatalf("unreadable target must hide its id: %+v", detail)
	}
	for _, member := range detail.Members {
		if member.Status != model.OnboardingStatusUnknown {
			test.Fatalf("status must be unknown: %+v", member)
		}
	}
}

func TestOnboardingUnmapped(test *testing.T) {
	store := onboardingStore()
	store.team["owner/001B"] = onboardingTeam("001B")
	accounts := &onboardingAccountsFake{}
	detail, err := performanceTestService(store, accounts).GetOnboarding(test.Context(), "owner", "001B")
	if err != nil {
		test.Fatal(err)
	}
	if detail.Mapped || detail.AccountReadable || len(detail.Members) != 4 || accounts.getCalls != 0 {
		test.Fatalf("unmapped detail %+v calls %d", detail, accounts.getCalls)
	}
	for _, member := range detail.Members {
		if member.Status != model.OnboardingStatusNone {
			test.Fatalf("unmapped status %+v", member)
		}
	}
}

func TestOnboardingTruncatesTeam(test *testing.T) {
	store := onboardingStore()
	team := make([]model.SFDCTeamMember, 0, 101)
	for i := 0; i < 101; i++ {
		team = append(team, model.SFDCTeamMember{SFDCAccountID: "001A", Alias: fmt.Sprintf("u%03d", i)})
	}
	store.team["owner/001A"] = team
	detail, err := performanceTestService(store, &onboardingAccountsFake{members: map[string][]model.AccountMemberDTO{"acc1": nil}}).GetOnboarding(test.Context(), "owner", "001A")
	if err != nil || !detail.Truncated || len(detail.Members) != 100 {
		test.Fatalf("truncated %v %d %v", detail.Truncated, len(detail.Members), err)
	}
}

func TestListOnboardingSummary(test *testing.T) {
	store := onboardingStore()
	store.team["owner/001A"] = append(store.team["owner/001A"],
		model.SFDCTeamMember{SFDCAccountID: "001A", Alias: "atam2", TeamMemberRole: "Enterprise Support Manager"})
	store.invites = map[string][]model.PerformanceInvite{"owner/001A": {
		{SFDCAccountID: "001A", Alias: "atam", AccountID: "acc1", InvitedAt: "2026-11-09T00:00:00Z"}}}
	accounts := &onboardingAccountsFake{members: map[string][]model.AccountMemberDTO{"acc1": {{Email: "dhlee@amazon.com"}}}}
	summary, err := performanceTestService(store, accounts).ListOnboarding(test.Context(), "owner")
	if err != nil {
		test.Fatal(err)
	}
	if len(summary.Items) != 1 || summary.Truncated {
		test.Fatalf("summary %+v", summary)
	}
	roles := summary.Items[0].Roles
	if roles[model.RoleAM] != "member" || roles[model.RoleTAM] != "pending" || roles[model.RoleSA] != "missing" || len(roles) != 3 {
		test.Fatalf("roles %v", roles)
	}

	// Roles without an SFDC person are omitted; unreadable targets are skipped.
	store.team["owner/001A"] = []model.SFDCTeamMember{{SFDCAccountID: "001A", Alias: "dhlee", TeamMemberRole: "Account Manager", AllTeamRoles: "Account Manager"}}
	summary, _ = performanceTestService(store, accounts).ListOnboarding(test.Context(), "owner")
	if len(summary.Items[0].Roles) != 1 {
		test.Fatalf("absent roles must be omitted: %v", summary.Items[0].Roles)
	}
	denied := &onboardingAccountsFake{denied: map[string]bool{"acc1": true}}
	if summary, _ = performanceTestService(store, denied).ListOnboarding(test.Context(), "owner"); len(summary.Items) != 0 {
		test.Fatalf("unreadable target must be skipped: %+v", summary)
	}
}

func TestListOnboardingBoundsAccountReads(test *testing.T) {
	store := &performanceStoreFake{assignments: map[string][]model.SFDCAssignment{}, mappings: map[string][]model.AccountMapping{}}
	members := map[string][]model.AccountMemberDTO{}
	for i := 0; i < 105; i++ {
		id := fmt.Sprintf("S%03d", i)
		store.assignments["owner"] = append(store.assignments["owner"], model.SFDCAssignment{SFDCAccountID: id})
		store.mappings["owner"] = append(store.mappings["owner"], model.AccountMapping{SFDCAccountID: id, AccountID: "acc" + id})
		members["acc"+id] = nil
	}
	accounts := &onboardingAccountsFake{members: members}
	summary, err := performanceTestService(store, accounts).ListOnboarding(test.Context(), "owner")
	if err != nil || !summary.Truncated || accounts.getCalls != performanceOnboardingMaxAccounts || len(summary.Items) != performanceOnboardingMaxAccounts {
		test.Fatalf("truncated=%v calls=%d items=%d err=%v", summary.Truncated, accounts.getCalls, len(summary.Items), err)
	}
}

func TestInviteOnboarding(test *testing.T) {
	store := onboardingStore()
	accounts := &onboardingAccountsFake{
		members: map[string][]model.AccountMemberDTO{"acc1": nil},
		pending: map[string]bool{"atam@amazon.com": true},
		addErr:  map[string]error{"zsa@amazon.com": ErrMemberExists, "bdm@amazon.com": ErrInvitationRateLimited},
	}
	svc := performanceTestService(store, accounts)
	response, err := svc.InviteOnboarding(test.Context(), "owner", "001A", model.OnboardingInviteRequest{Items: []model.OnboardingInviteItem{
		{Alias: "DHLee", Role: model.RoleAM}, {Alias: "atam", Role: model.RoleTAM}, {Alias: "zsa", Role: model.RoleSA}, {Alias: "bdm", Role: model.RoleSSA},
	}})
	if err != nil {
		test.Fatal(err)
	}
	got := ""
	for _, result := range response.Results {
		got += result.Alias + ":" + result.Outcome + ":" + result.Code + " "
	}
	if want := "dhlee:added: atam:invited: zsa:already_member: bdm:failed:RATE_LIMITED "; got != want {
		test.Fatalf("results %q, want %q", got, want)
	}
	if accounts.added[0].Email != "dhlee@amazon.com" || accounts.added[0].Role != model.RoleAM {
		test.Fatalf("add request %+v", accounts.added[0])
	}
	if len(store.putInvites) != 1 || store.putInvites[0].Alias != "atam" || store.putInvites[0].AccountID != "acc1" || store.putInvites[0].InvitedAt != "2026-11-10T09:00:00Z" {
		test.Fatalf("invite record %+v", store.putInvites)
	}
}

func TestInviteOnboardingRejects(test *testing.T) {
	items := func(n int) []model.OnboardingInviteItem {
		out := make([]model.OnboardingInviteItem, n)
		for i := range out {
			out[i] = model.OnboardingInviteItem{Alias: fmt.Sprintf("u%02d", i), Role: model.RoleSA}
		}
		return out
	}
	cases := map[string]struct {
		sfdc  string
		items []model.OnboardingInviteItem
		want  error
	}{
		"empty":        {"001A", nil, ErrInvalidInput},
		"over 20":      {"001A", items(21), ErrInvalidInput},
		"owner role":   {"001A", []model.OnboardingInviteItem{{Alias: "dhlee", Role: model.RoleOwner}}, ErrInvalidInput},
		"duplicate":    {"001A", []model.OnboardingInviteItem{{Alias: "dhlee", Role: model.RoleAM}, {Alias: "DHLEE", Role: model.RoleAM}}, ErrInvalidInput},
		"not in team":  {"001A", []model.OnboardingInviteItem{{Alias: "stranger", Role: model.RoleSA}}, ErrNotFound},
		"unmapped":     {"001B", []model.OnboardingInviteItem{{Alias: "dhlee", Role: model.RoleAM}}, ErrNotFound},
		"not assigned": {"001Z", []model.OnboardingInviteItem{{Alias: "dhlee", Role: model.RoleAM}}, ErrNotFound},
	}
	for name, item := range cases {
		accounts := &onboardingAccountsFake{}
		_, err := performanceTestService(onboardingStore(), accounts).InviteOnboarding(test.Context(), "owner", item.sfdc, model.OnboardingInviteRequest{Items: item.items})
		if !errors.Is(err, item.want) || len(accounts.added) != 0 {
			test.Errorf("%s: err %v added %d", name, err, len(accounts.added))
		}
	}
}

func onboardingTeamSync(mutations ...model.PerformanceMutation) model.PerformanceSyncRequest {
	return model.PerformanceSyncRequest{BatchID: onboardingTestBatch, Complete: true, Mutations: mutations}
}

func TestTeamSyncNormalizes(test *testing.T) {
	store := &performanceStoreFake{}
	svc := performanceTestService(store, nil)
	request := onboardingTeamSync(
		model.PerformanceMutation{Op: "upsert", TeamMember: &model.SFDCTeamMember{SFDCAccountID: "001A", Alias: "Kim.Lee", Name: "Kim", TeamMemberRole: "Solutions Architect"}},
		model.PerformanceMutation{Op: "remove", ExternalID: "001A:old.alias"},
	)
	if _, err := svc.Sync(test.Context(), "owner", model.PerformanceSourceSFDCAccountTeam, request); err != nil {
		test.Fatal(err)
	}
	if got := store.requests[0].Mutations[0].TeamMember.Alias; got != "kim.lee" {
		test.Fatalf("alias must be lower-cased: %q", got)
	}
	bad := []model.PerformanceMutation{
		{Op: "upsert", TeamMember: &model.SFDCTeamMember{SFDCAccountID: "001A", Alias: "kim@x"}},
		{Op: "upsert", TeamMember: &model.SFDCTeamMember{SFDCAccountID: "001A", Alias: "kim"}, Task: &model.SFDCTask{TaskID: "T"}},
		{Op: "upsert", Assignment: &model.SFDCAssignment{SFDCAccountID: "001A"}},
		{Op: "remove", ExternalID: "001A"},
		{Op: "remove", ExternalID: "001A:Kim"},
	}
	for _, mutation := range bad {
		if _, err := svc.Sync(test.Context(), "owner", model.PerformanceSourceSFDCAccountTeam, onboardingTeamSync(mutation)); !errors.Is(err, ErrInvalidInput) {
			test.Errorf("%+v must be rejected: %v", mutation, err)
		}
	}
	// Other sources never accept team rows.
	cross := onboardingTeamSync(model.PerformanceMutation{Op: "upsert", Assignment: &model.SFDCAssignment{SFDCAccountID: "001A"}, TeamMember: &model.SFDCTeamMember{SFDCAccountID: "001A", Alias: "kim"}})
	if _, err := svc.Sync(test.Context(), "owner", model.PerformanceSourceSFDCAssignments, cross); !errors.Is(err, ErrInvalidInput) {
		test.Errorf("assignment batch with a team row must be rejected: %v", err)
	}
}

func TestTeamSyncCapPerAccount(test *testing.T) {
	full := make([]model.SFDCTeamMember, 0, 100)
	for i := 0; i < 100; i++ {
		full = append(full, model.SFDCTeamMember{SFDCAccountID: "001A", Alias: fmt.Sprintf("u%03d", i)})
	}
	store := &performanceStoreFake{team: map[string][]model.SFDCTeamMember{"owner/001A": full}}
	svc := performanceTestService(store, nil)
	add := model.PerformanceMutation{Op: "upsert", TeamMember: &model.SFDCTeamMember{SFDCAccountID: "001A", Alias: "new"}}
	if _, err := svc.Sync(test.Context(), "owner", model.PerformanceSourceSFDCAccountTeam, onboardingTeamSync(add)); !errors.Is(err, ErrInvalidInput) {
		test.Fatalf("101st member must be rejected: %v", err)
	}
	update := model.PerformanceMutation{Op: "upsert", TeamMember: &model.SFDCTeamMember{SFDCAccountID: "001A", Alias: "u000", Name: "changed"}}
	swap := model.PerformanceMutation{Op: "remove", ExternalID: "001A:u001"}
	if _, err := svc.Sync(test.Context(), "owner", model.PerformanceSourceSFDCAccountTeam, onboardingTeamSync(update, swap, add)); err != nil {
		test.Fatalf("update plus swap must pass: %v", err)
	}
}

func TestListSourcesIncludesTeam(test *testing.T) {
	list, err := performanceTestService(&performanceStoreFake{}, nil).ListSources(test.Context(), "owner")
	if err != nil || len(list.Sources) != 3 || list.Sources[2].Source != model.PerformanceSourceSFDCAccountTeam {
		test.Fatalf("sources %+v %v", list, err)
	}
}

func TestOnboardingInheritedReaderIsUnreadable(test *testing.T) {
	accounts := &inheritedAccountsFake{}
	detail, err := performanceTestService(onboardingStore(), accounts).GetOnboarding(test.Context(), "owner", "001A")
	if err != nil {
		test.Fatal(err)
	}
	if detail.AccountReadable || detail.AccountID != "" {
		test.Fatalf("an ancestor-only reader cannot invite: %+v", detail)
	}
	for _, member := range detail.Members {
		if member.Status != model.OnboardingStatusUnknown {
			test.Fatalf("status must be unknown: %+v", member)
		}
	}
}

// inheritedAccountsFake reads acc1 only through an ancestor grant.
type inheritedAccountsFake struct{}

func (inheritedAccountsFake) GetAccount(_ context.Context, _, accountID string) (*model.AccountResponse, error) {
	return &model.AccountResponse{AccountID: accountID, InheritedFromAccountID: "parent", Members: []model.AccountMemberDTO{{Email: "dhlee@amazon.com"}}}, nil
}

func TestInviteOnboardingStopsAtTimeBudget(test *testing.T) {
	store := onboardingStore()
	accounts := &onboardingAccountsFake{members: map[string][]model.AccountMemberDTO{"acc1": nil}}
	svc := performanceTestService(store, accounts)
	clock := svc.now()
	svc.now = func() time.Time { return clock }
	// Each AddMember takes 8 seconds: the third item would start after 16 seconds.
	slow := &clockedInviter{onboardingAccountsFake: accounts, advance: func() { clock = clock.Add(8 * time.Second) }}
	svc.accounts = slow
	response, err := svc.InviteOnboarding(test.Context(), "owner", "001A", model.OnboardingInviteRequest{Items: []model.OnboardingInviteItem{
		{Alias: "dhlee", Role: model.RoleAM}, {Alias: "atam", Role: model.RoleTAM}, {Alias: "zsa", Role: model.RoleSA},
	}})
	if err != nil {
		test.Fatal(err)
	}
	if len(accounts.added) != 2 || response.Results[2].Outcome != model.OnboardingInviteNotAttempted || response.Results[1].Outcome != model.OnboardingInviteAdded {
		test.Fatalf("budget: added=%d results=%+v", len(accounts.added), response.Results)
	}
}

type clockedInviter struct {
	*onboardingAccountsFake
	advance func()
}

func (inviter *clockedInviter) AddMember(ctx context.Context, requester, accountID string, req *model.AddMemberRequest) (*model.AccountMemberDTO, error) {
	inviter.advance()
	return inviter.onboardingAccountsFake.AddMember(ctx, requester, accountID, req)
}

func TestRemovedAssignmentDeletesTeam(test *testing.T) {
	store := &performanceStoreFake{}
	svc := performanceTestService(store, nil)
	request := model.PerformanceSyncRequest{BatchID: onboardingTestBatch, Complete: false, Mutations: []model.PerformanceMutation{
		{Op: "remove", ExternalID: "001A"},
		{Op: "upsert", Assignment: &model.SFDCAssignment{SFDCAccountID: "001B", Name: "B"}},
	}}
	if _, err := svc.Sync(test.Context(), "owner", model.PerformanceSourceSFDCAssignments, request); err != nil {
		test.Fatal(err)
	}
	if len(store.teamDeleted) != 1 || store.teamDeleted[0] != "001A" {
		test.Fatalf("removed assignment must delete its team: %v", store.teamDeleted)
	}
}

func TestListOnboardingCountsFailedAccountsInsteadOfFailing(test *testing.T) {
	store := &performanceStoreFake{assignments: map[string][]model.SFDCAssignment{}, mappings: map[string][]model.AccountMapping{}}
	for _, id := range []string{"S1", "S2", "S3"} {
		store.assignments["owner"] = append(store.assignments["owner"], model.SFDCAssignment{SFDCAccountID: id})
		store.mappings["owner"] = append(store.mappings["owner"], model.AccountMapping{SFDCAccountID: id, AccountID: "acc" + id})
	}
	accounts := &onboardingAccountsFake{members: map[string][]model.AccountMemberDTO{"accS1": nil, "accS3": nil},
		getErr: map[string]error{"accS2": errors.New("throttled")}}
	summary, err := performanceTestService(store, accounts).ListOnboarding(test.Context(), "owner")
	if err != nil || summary.Unavailable != 1 || len(summary.Items) != 2 || summary.Items[0].SFDCAccountID != "S1" || summary.Items[1].SFDCAccountID != "S3" {
		test.Fatalf("summary %+v err %v", summary, err)
	}

	ctx, cancel := context.WithCancel(test.Context())
	cancel()
	cancelled := &onboardingAccountsFake{getErr: map[string]error{"accS1": context.Canceled}}
	if _, err := performanceTestService(store, cancelled).ListOnboarding(ctx, "owner"); !errors.Is(err, context.Canceled) {
		test.Fatalf("a cancelled request must fail, got %v", err)
	}
}
