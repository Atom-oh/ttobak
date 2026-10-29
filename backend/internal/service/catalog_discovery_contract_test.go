package service

import (
	"net/url"
	"testing"

	"github.com/ttobak/backend/internal/model"
)

// Installed MCP clients reject extra discovery fields. Union metadata belongs
// to data responses; discovery has no rows to union.
func TestCatalogDiscoveryRetainsInstalledMCPContract(t *testing.T) {
	for _, flag := range []string{"0", "1"} {
		t.Run(flag, func(t *testing.T) {
			t.Setenv(accountDescendantReadsEnv, flag)
			for _, kind := range []string{"meeting", "vault"} {
				t.Run(kind, func(t *testing.T) {
					q := url.Values{"pageSize": {"8000"}}
					var read func(url.Values) map[string]interface{}
					if kind == "meeting" {
						service, repo := catalogMeetingFixture()
						repo.accounts["a"] = &model.Account{PK: "ACCOUNT#a", SK: "META", AccountID: "a", EntityType: model.EntityTypeAccount}
						read = func(q url.Values) map[string]interface{} {
							return readCatalogMeetingPage(t, service, MeetingCatalogIdentity{UserID: "reader"}, q)
						}
					} else {
						service, repo := vaultReadingFixture()
						repo.accounts["a"] = &model.Account{PK: "ACCOUNT#a", SK: "META", AccountID: "a", EntityType: model.EntityTypeAccount}
						read = func(q url.Values) map[string]interface{} { return readVaultPage(t, service, q) }
					}
					discoveries := 0
					for pageNumber := 0; ; pageNumber++ {
						page := read(q)
						if page["discoveryPending"] == true {
							discoveries++
							if len(page) != 4 || page["kind"] == nil || page["phase"] != "discovery" || page["page"] == nil {
								t.Fatalf("installed MCP validator rejects discovery keys: %v", page)
							}
						}
						next := page["page"].(map[string]interface{})
						if next["complete"] == true {
							break
						}
						if pageNumber > 300 {
							t.Fatal("catalog did not finish")
						}
						q.Set("cursor", next["nextCursor"].(string))
					}
					if discoveries == 0 {
						t.Fatal("discovery contract was not exercised")
					}
				})
			}
		})
	}
}
