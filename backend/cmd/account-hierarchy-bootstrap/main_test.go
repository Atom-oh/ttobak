package main

import (
	"context"
	"errors"
	"testing"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

type bootstrapFake struct {
	pages                  []*repository.AccountHierarchyPage
	reads                  int
	writes                 int
	conflict               bool
	moveDuringVerification bool
	accounts               map[string]*model.Account
}

func (f *bootstrapFake) ScanAccountHierarchyPage(context.Context, string) (*repository.AccountHierarchyPage, error) {
	if f.reads >= len(f.pages) {
		return nil, errors.New("unexpected scan")
	}
	p := f.pages[f.reads]
	f.reads++
	return p, nil
}
func (f *bootstrapFake) GetAccount(_ context.Context, id string) (*model.Account, error) {
	return f.accounts[id], nil
}
func (f *bootstrapFake) EnsureAccountChildReference(context.Context, string, string) error {
	f.writes++
	if f.conflict {
		return repository.ErrConditionFailed
	}
	return nil
}
func (f *bootstrapFake) ListChildAccountIDsPage(context.Context, string, string, int32) ([]string, *string, error) {
	if f.moveDuringVerification {
		f.accounts["child"].ParentAccountID = "other"
	}
	return []string{"child"}, nil, nil
}
func bootstrapFixture() *bootstrapFake {
	child := &model.Account{PK: "ACCOUNT#child", SK: "META", AccountID: "child", ParentAccountID: "parent", EntityType: model.EntityTypeAccount}
	parent := &model.Account{PK: "ACCOUNT#parent", SK: "META", AccountID: "parent", EntityType: model.EntityTypeAccount}
	return &bootstrapFake{pages: []*repository.AccountHierarchyPage{{NextCursor: "advance"}, {Accounts: []model.Account{*child, *parent}}}, accounts: map[string]*model.Account{"child": child, "parent": parent}}
}

func TestBootstrapConsumesEmptyPagesAndDefaultsToDryRun(t *testing.T) {
	for _, apply := range []bool{false, true} {
		f := bootstrapFixture()
		result, err := bootstrap(context.Background(), f, apply, false)
		if err != nil || result.Accounts != 2 || result.Links != 1 || f.reads != 2 {
			t.Fatalf("lost empty continuation or account metadata: %+v/%v", result, err)
		}
		if (f.writes == 1) != apply {
			t.Fatalf("dry-run mutated refs: apply=%v writes=%d", apply, f.writes)
		}
	}
}

func TestBootstrapReportsConcurrentParentChange(t *testing.T) {
	f := bootstrapFixture()
	f.conflict = true
	result, err := bootstrap(context.Background(), f, true, false)
	if err == nil || result.ConcurrentChanges != 1 || result.Written != 0 {
		t.Fatalf("CAS rejection hidden: %+v/%v", result, err)
	}
}

func TestBootstrapRejectsCyclingScanContinuation(t *testing.T) {
	f := bootstrapFixture()
	f.pages = []*repository.AccountHierarchyPage{{NextCursor: "a"}, {NextCursor: "b"}, {NextCursor: "a"}}
	if _, err := bootstrap(context.Background(), f, false, false); err == nil {
		t.Fatal("scan loop accepted")
	}
}

func TestBootstrapVerificationRejectsMovedChildDespiteStaleReference(t *testing.T) {
	f := bootstrapFixture()
	f.moveDuringVerification = true
	result, err := bootstrap(context.Background(), f, false, true)
	if err == nil || result.ConcurrentChanges != 1 || result.Verified != 0 {
		t.Fatalf("stale reference established verification: %+v/%v", result, err)
	}
}
