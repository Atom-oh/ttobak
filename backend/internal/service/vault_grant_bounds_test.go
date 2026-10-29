package service

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/ttobak/backend/internal/model"
)

type vaultGrantBoundsRepo struct {
	*mockVaultRepo
	grants         map[string]*model.AccountMember
	memberReads    int
	documentReads  int
	afterList      func(string)
	afterRead      func(string, string)
	mutateDocument func(*model.AccountDocument)
	memberReadErr  error
	children       map[string][]string
}

func (r *vaultGrantBoundsRepo) ListChildAccountIDsPage(_ context.Context, parent, cursor string, limit int32) ([]string, *string, error) {
	if cursor != "" || len(r.children[parent]) > int(limit) {
		return nil, nil, ErrReadingData
	}
	return append([]string{}, r.children[parent]...), nil, nil
}

func (r *vaultGrantBoundsRepo) GetMember(_ context.Context, account, user string) (*model.AccountMember, error) {
	r.memberReads++
	if r.memberReadErr != nil {
		return nil, r.memberReadErr
	}
	m := r.grants[account+"/"+user]
	if m == nil {
		return nil, nil
	}
	copy := *m
	return &copy, nil
}

func (r *vaultGrantBoundsRepo) ListAccountDocuments(ctx context.Context, pk string) ([]model.AccountDocument, error) {
	rows, err := r.mockVaultRepo.ListAccountDocuments(ctx, pk)
	if r.afterList != nil {
		r.afterList(pk)
	}
	return rows, err
}

func (r *vaultGrantBoundsRepo) GetAccountDocument(ctx context.Context, pk, doc string) (*model.AccountDocument, error) {
	r.documentReads++
	row, err := r.mockVaultRepo.GetAccountDocument(ctx, pk, doc)
	if row != nil && r.mutateDocument != nil {
		r.mutateDocument(row)
	}
	if r.afterRead != nil {
		r.afterRead(pk, doc)
	}
	return row, err
}

func TestVaultScopedGrantChecksPreserveReadErrorsAndDocumentIdentity(t *testing.T) {
	t.Setenv(accountDescendantReadsEnv, "0")
	t.Run("membership-store-error", func(t *testing.T) {
		r := vaultGrantBoundsFixture(1, 1, 0)
		want := errors.New("membership read failed")
		r.afterRead = func(_, _ string) { r.memberReadErr = want }
		files, err := newVaultServiceWithRepo(r).exportDocuments(context.Background(), "reader", map[string]string{})
		if files != nil || !errors.Is(err, want) {
			t.Fatalf("membership store error was hidden: %+v/%v", files, err)
		}
	})
	t.Run("document-identity", func(t *testing.T) {
		r := vaultGrantBoundsFixture(1, 1, 0)
		r.mutateDocument = func(doc *model.AccountDocument) { doc.PK = "ACCOUNT#other" }
		files, err := newVaultServiceWithRepo(r).exportDocuments(context.Background(), "reader", map[string]string{})
		if files != nil || !errors.Is(err, ErrReadingData) {
			t.Fatalf("document identity was not checked: %+v/%v", files, err)
		}
	})
}

func vaultGrantBoundsFixture(scopes, perScope, personal int) *vaultGrantBoundsRepo {
	r := &vaultGrantBoundsRepo{mockVaultRepo: &mockVaultRepo{
		accounts: map[string]*model.Account{}, documents: map[string][]model.AccountDocument{},
	}, grants: map[string]*model.AccountMember{}}
	addDocs := func(pk string, count int) {
		for n := 0; n < count; n++ {
			id := fmt.Sprintf("document-%04d", n)
			r.documents[pk] = append(r.documents[pk], model.AccountDocument{
				PK: pk, DocID: id, Title: id, Content: "Shared content",
			})
		}
	}
	addDocs("USER#reader", personal)
	for n := 0; n < scopes; n++ {
		id := fmt.Sprintf("scope-%03d", n)
		m := &model.AccountMember{PK: "ACCOUNT#" + id, SK: "MEMBER#reader",
			AccountID: id, UserID: "reader", Role: model.RoleAM, EntityType: model.EntityTypeAccountMember}
		r.grants[id+"/reader"] = m
		r.memberships = append(r.memberships, *m)
		r.accounts[id] = &model.Account{PK: "ACCOUNT#" + id, SK: "META", AccountID: id,
			Name: id, EntityType: model.EntityTypeAccount}
		addDocs("ACCOUNT#"+id, perScope)
	}
	return r
}

func TestVaultGrantChecksScaleWithScopesPlusDocuments(t *testing.T) {
	for _, mode := range []string{"0", "1"} {
		for _, personal := range []bool{false, true} {
			t.Run(fmt.Sprintf("flag=%s/personal=%t", mode, personal), func(t *testing.T) {
				t.Setenv(accountDescendantReadsEnv, mode)
				const scopes, docs = 30, 300
				perScope, private := docs/scopes, 0
				if personal {
					perScope, private = 0, docs
				}
				r := vaultGrantBoundsFixture(scopes, perScope, private)
				files, err := newVaultServiceWithRepo(r).exportDocuments(context.Background(), "reader", map[string]string{})
				if err != nil || len(files) != docs || r.documentReads != docs {
					t.Fatalf("document export changed: files=%d reads=%d err=%v", len(files), r.documentReads, err)
				}
				accountDocs := scopes * perScope
				if r.memberReads > 2*accountDocs+5*scopes {
					t.Fatalf("grant validation multiplied scopes by documents: members=%d scopes=%d accountDocs=%d", r.memberReads, scopes, accountDocs)
				}
				t.Logf("scopes=%d documents=%d memberReads=%d", scopes, docs, r.memberReads)
			})
		}
	}
}

func TestVaultInheritedScopeRevocationRejectsDocumentRead(t *testing.T) {
	t.Setenv(accountDescendantReadsEnv, "1")
	r := vaultGrantBoundsFixture(1, 0, 0)
	r.accounts["child"] = &model.Account{PK: "ACCOUNT#child", SK: "META", AccountID: "child",
		ParentAccountID: "scope-000", Name: "child", EntityType: model.EntityTypeAccount}
	r.children = map[string][]string{"scope-000": {"child"}}
	r.documents["ACCOUNT#child"] = []model.AccountDocument{{PK: "ACCOUNT#child",
		DocID: "document-inherited", Title: "Shared", Content: "Shared"}}
	r.afterRead = func(pk, _ string) {
		if pk == "ACCOUNT#child" {
			delete(r.grants, "scope-000/reader")
		}
	}
	files, err := newVaultServiceWithRepo(r).exportDocuments(context.Background(), "reader", map[string]string{})
	if files != nil || !errors.Is(err, ErrForbidden) || r.documentReads != 1 {
		t.Fatalf("inherited scope returned bytes after ancestor revocation: %+v/%v", files, err)
	}
}

func TestVaultCurrentScopeRevocationRejectsDocumentRead(t *testing.T) {
	for _, when := range []string{"before-read", "after-read"} {
		t.Run(when, func(t *testing.T) {
			t.Setenv(accountDescendantReadsEnv, "0")
			r := vaultGrantBoundsFixture(1, 1, 0)
			revoke := func(pk string) {
				if pk == "ACCOUNT#scope-000" {
					delete(r.grants, "scope-000/reader")
				}
			}
			if when == "before-read" {
				r.afterList = revoke
			} else {
				r.afterRead = func(pk, _ string) { revoke(pk) }
			}
			files, err := newVaultServiceWithRepo(r).exportDocuments(context.Background(), "reader", map[string]string{})
			if files != nil || !errors.Is(err, ErrForbidden) {
				t.Fatalf("revoked current scope returned files: %+v/%v", files, err)
			}
			if when == "before-read" && r.documentReads != 0 {
				t.Fatal("read private document bytes after observed scope revocation")
			}
		})
	}
}

func TestVaultFinalGrantPassRejectsOtherScopeRevocation(t *testing.T) {
	t.Setenv(accountDescendantReadsEnv, "0")
	r := vaultGrantBoundsFixture(2, 1, 0)
	r.afterRead = func(pk, _ string) {
		if pk == "ACCOUNT#scope-001" {
			delete(r.grants, "scope-000/reader")
		}
	}
	files, err := newVaultServiceWithRepo(r).exportDocuments(context.Background(), "reader", map[string]string{})
	if files != nil || !errors.Is(err, ErrForbidden) || r.documentReads != 2 {
		t.Fatalf("final scope pass failed closed: files=%+v reads=%d err=%v", files, r.documentReads, err)
	}
}
