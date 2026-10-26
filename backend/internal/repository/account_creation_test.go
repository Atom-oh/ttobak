package repository

import (
	"context"
	"testing"

	"github.com/ttobak/backend/internal/model"
)

func TestCreateAccountInitialMembers_AtomicPrivacyStamped(t *testing.T) {
	for _, keep := range []bool{false, true} {
		t.Run(map[bool]string{false: "owner-only", true: "owner-and-AM"}[keep], func(t *testing.T) {
			t.Setenv("SUMMARY_PRIVACY_WRITER_ENABLED", "1")
			a := &model.Account{PK: "ACCOUNT#child", SK: "META", AccountID: "child", ParentAccountID: "parent", OwnerUserID: "colleague", CreatedByUserID: "creator"}
			owner := &model.AccountMember{PK: a.PK, SK: "MEMBER#colleague", AccountID: "child", UserID: "colleague", Role: model.RoleOwner}
			var creator *model.AccountMember
			if keep {
				creator = &model.AccountMember{PK: a.PK, SK: "MEMBER#creator", AccountID: "child", UserID: "creator", Role: model.RoleAM}
			}
			r := hierarchyWireRepo(t, func(items []hierarchyWireItem) {
				want := 4
				if keep {
					want++
				}
				if len(items) != want {
					t.Fatalf("atomic rows/checks=%d want %d", len(items), want)
				}
				members := 0
				for _, it := range items {
					if it.Put != nil {
						requireHierarchyCondition(t, it.Put, "attribute_not_exists(PK)")
						if _, ok := it.Put.Item["userId"]; ok {
							members++
							v := it.Put.Item["summaryPrivacyVersion"].(map[string]any)["N"]
							if v != "1" {
								t.Fatalf("membership privacy=%v", v)
							}
						}
					}
					if it.ConditionCheck != nil && it.ConditionCheck.Key["SK"]["S"] == "MEMBER#creator" {
						requireHierarchyCondition(t, it.ConditionCheck, "attribute_exists(PK)")
					}
					if it.ConditionCheck != nil && it.ConditionCheck.Key["SK"]["S"] == "MEMBER#colleague" {
						t.Fatal("checked selected Owner rather than creator's parent access")
					}
				}
				wantMembers := 1
				if keep {
					wantMembers++
				}
				if members != wantMembers {
					t.Fatalf("members=%d", members)
				}
			}, 200, "{}")
			err := r.CreateAccountWithInitialMembers(context.Background(), a, owner, creator, "creator", []AccountParentLink{{AccountID: "parent"}})
			if err != nil {
				t.Fatal(err)
			}
			if owner.SummaryPrivacyVersion != 0 || (creator != nil && creator.SummaryPrivacyVersion != 0) {
				t.Fatal("mutated caller memberships")
			}
		})
	}
}
