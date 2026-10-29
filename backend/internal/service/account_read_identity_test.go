package service

import (
	"context"
	"errors"
	"net/url"
	"testing"

	"github.com/ttobak/backend/internal/model"
)

func TestDocumentCatalogAndVaultRejectMalformedDirectMemberIdentity(t *testing.T) {
	ctx := context.Background()
	for _, bad := range []model.AccountMember{{AccountID: "other", UserID: "reader"}, {AccountID: "a", UserID: "other"}} {
		t.Run(bad.AccountID+"/"+bad.UserID, func(t *testing.T) {
			catalog, repo := newDocumentCatalogFixture()
			repo.members["a/reader"] = &bad
			if body, err := catalog.Read(ctx, "reader", url.Values{"accountId": {"a"}}); body != nil || !errors.Is(err, ErrForbidden) {
				t.Fatalf("catalog accepted mismatched direct identity: %s/%v", body, err)
			}
			vault, vrepo := vaultReadingFixture()
			vrepo.members["a|reader"] = &bad
			if source, err := vault.source(ctx, "reader", "document", "a", "d"); source != nil || !errors.Is(err, ErrForbidden) {
				t.Fatalf("vault source accepted mismatched direct identity: %+v/%v", source, err)
			}
			if source, err := vault.source(ctx, "reader", "meeting", "", "m"); err != nil || source == nil || source.accountName != "" {
				t.Fatalf("owned meeting exposed an unauthorized Account name: %+v/%v", source, err)
			}
			grant, err := resolveAccountReadGrant(ctx, vrepo, "a", "reader")
			if err != nil {
				t.Fatal(err)
			}
			cursor, err := encodeVaultCursor(vaultManifestCursor{Version: 1, Binding: compactReadingHash([]string{"vault-manifest-v1", "reader"}), Phase: "account_docs", Account: "a", Grant: grant.fingerprint()})
			if err != nil {
				t.Fatal(err)
			}
			if body, err := vault.Read(ctx, "reader", url.Values{"cursor": {cursor}}); body != nil || !errors.Is(err, ErrStaleReadingCursor) {
				t.Fatalf("vault manifest accepted mismatched direct identity: %s/%v", body, err)
			}
		})
	}
}
