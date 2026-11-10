package service

import (
	"context"
	"errors"
	"testing"

	"github.com/ttobak/backend/internal/model"
)

// tombstone marks source as merged into target in the mock repo.
func tombstone(repo *mockAccountRepo, source, target string) {
	repo.accounts[source].MergedInto = target
}

func TestListAccountsHidesMergedTombstone(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	ctx := context.Background()
	a, _ := svc.CreateAccount(ctx, "u1", "u1@x.com", &model.CreateAccountRequest{Name: "A"})
	b, _ := svc.CreateAccount(ctx, "u1", "u1@x.com", &model.CreateAccountRequest{Name: "B"})
	tombstone(repo, a.AccountID, b.AccountID)
	list, err := svc.ListAccounts(ctx, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].AccountID != b.AccountID {
		t.Fatalf("list=%+v, want only %s", list, b.AccountID)
	}
}

func TestGetAccountReportsMergedInto(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	ctx := context.Background()
	a, _ := svc.CreateAccount(ctx, "u1", "u1@x.com", &model.CreateAccountRequest{Name: "A"})
	tombstone(repo, a.AccountID, "b")
	repo.accounts[a.AccountID].MergingInto = "c"
	resp, err := svc.GetAccount(ctx, "u1", a.AccountID)
	if err != nil {
		t.Fatal(err)
	}
	if resp.MergedInto != "b" || resp.MergingInto != "c" {
		t.Fatalf("resp merge fields = %q/%q", resp.MergedInto, resp.MergingInto)
	}
}

func TestResolveAccountByAliasSkipsTombstone(t *testing.T) {
	repo := newMockAccountRepo()
	svc := newAccountServiceWithRepo(repo)
	ctx := context.Background()
	a, _ := svc.CreateAccount(ctx, "u1", "u1@x.com", &model.CreateAccountRequest{Name: "Shared"})
	b, _ := svc.CreateAccount(ctx, "u1", "u1@x.com", &model.CreateAccountRequest{Name: "B", Aliases: []string{"shared"}})
	tombstone(repo, a.AccountID, b.AccountID)
	got, err := svc.ResolveAccountByAlias(ctx, "u1", "Shared")
	if errors.Is(err, ErrAmbiguousAlias) {
		t.Fatal("tombstone made the alias ambiguous")
	}
	if err != nil || got.AccountID != b.AccountID {
		t.Fatalf("got=%+v err=%v, want %s", got, err, b.AccountID)
	}
}

func TestRevokePendingMemberRejectsMergeStates(t *testing.T) {
	for _, tc := range []struct {
		name  string
		apply func(*model.Account)
	}{
		{"mergingInto", func(a *model.Account) { a.MergingInto = "b" }},
		{"mergedInto", func(a *model.Account) { a.MergedInto = "b" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := newMockAccountRepo()
			svc := newAccountServiceWithRepo(repo)
			ctx := context.Background()
			acc, _ := svc.CreateAccount(ctx, "owner-1", "o@x.com", &model.CreateAccountRequest{Name: "A"})
			repo.pendingShares = append(repo.pendingShares, &model.PendingShare{
				Email: "invited@x.com", Kind: model.PendingShareKindAccount,
				AccountID: acc.AccountID, Role: model.RoleSSA,
				SK: model.PrefixPendingAccount + acc.AccountID,
			})
			tc.apply(repo.accounts[acc.AccountID])
			err := svc.RevokePendingMember(ctx, "owner-1", acc.AccountID, "invited@x.com")
			if !errors.Is(err, ErrAccountMerging) {
				t.Fatalf("err=%v, want ErrAccountMerging", err)
			}
			if len(repo.pendingShares) != 1 {
				t.Fatalf("pending row must stay, got %d", len(repo.pendingShares))
			}
		})
	}
}
