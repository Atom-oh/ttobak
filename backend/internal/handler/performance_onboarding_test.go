package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/service"
)

// onboardingHTTPAccounts reads one Account with members and accepts invites.
type onboardingHTTPAccounts struct{ added []string }

func (*onboardingHTTPAccounts) GetAccount(_ context.Context, _, accountID string) (*model.AccountResponse, error) {
	if accountID != "acc1" {
		return nil, service.ErrForbidden
	}
	return &model.AccountResponse{AccountID: "acc1", Members: []model.AccountMemberDTO{{UserID: "u1", Email: "kim@amazon.com", Role: model.RoleSA}}}, nil
}

func (fake *onboardingHTTPAccounts) AddMember(_ context.Context, _, _ string, req *model.AddMemberRequest) (*model.AccountMemberDTO, error) {
	fake.added = append(fake.added, req.Email)
	return &model.AccountMemberDTO{Email: req.Email, Role: req.Role, Pending: true}, nil
}

const onboardingHTTPTeamBody = `{"batchId":"` + performanceHTTPBatch + `","expectedRevision":0,"complete":true,"mutations":[` +
	`{"op":"upsert","teamMember":{"sfdcAccountId":"A1","alias":"kim","name":"Kim","title":"SA","teamMemberRole":"Solutions Architect","allTeamRoles":"Solutions Architect"}},` +
	`{"op":"upsert","teamMember":{"sfdcAccountId":"A1","alias":"lee","name":"Lee","title":"AM","teamMemberRole":"Account Manager","allTeamRoles":"Account Manager"}}]}`

func TestOnboardingHTTP(test *testing.T) {
	store := newPerformanceMemoryStore()
	accounts := &onboardingHTTPAccounts{}
	api := service.NewPerformanceService(store, accounts)
	if response := performanceHTTPDo(api, "owner", http.MethodPost, "/api/performance/sources/sfdc-assignments/sync", performanceAssignmentBody); response.Code != http.StatusOK {
		test.Fatalf("assignment sync %d %s", response.Code, response.Body.String())
	}
	if response := performanceHTTPDo(api, "owner", http.MethodPost, "/api/performance/sources/sfdc-account-team/sync", onboardingHTTPTeamBody); response.Code != http.StatusOK {
		test.Fatalf("team sync %d %s", response.Code, response.Body.String())
	}
	store.mappings["owner"] = map[string]model.AccountMapping{"A1": {SFDCAccountID: "A1", AccountID: "acc1", Manual: true}}

	response := performanceHTTPDo(api, "owner", http.MethodGet, "/api/performance/onboarding/A1", "")
	var detail model.OnboardingDetail
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &detail) != nil {
		test.Fatalf("detail %d %s", response.Code, response.Body.String())
	}
	if len(detail.Members) != 2 || detail.Members[0].Alias != "lee" || detail.Members[0].Status != "none" || detail.Members[1].Status != "member" {
		test.Fatalf("detail %+v", detail)
	}
	if strings.Contains(response.Body.String(), "@amazon.com") {
		test.Fatalf("detail must not expose emails: %s", response.Body.String())
	}

	if response := performanceHTTPDo(api, "owner", http.MethodGet, "/api/performance/onboarding/NOPE", ""); response.Code != http.StatusNotFound {
		test.Fatalf("unknown SFDC account %d", response.Code)
	}

	invite := `{"items":[{"alias":"lee","role":"AM"}]}`
	if response := performanceHTTPDo(api, "owner-unverified", http.MethodPost, "/api/performance/onboarding/A1/invites", invite); response.Code != http.StatusForbidden {
		test.Fatalf("unverified email must be refused: %d", response.Code)
	}
	if response := performanceHTTPDo(api, "owner", http.MethodPost, "/api/performance/onboarding/A1/invites", `{"items":[{"alias":"lee","role":"AM","email":"x@y"}]}`); response.Code != http.StatusBadRequest {
		test.Fatalf("unknown field must be refused: %d", response.Code)
	}
	response = performanceHTTPDo(api, "owner", http.MethodPost, "/api/performance/onboarding/A1/invites", invite)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"outcome":"invited"`) || len(accounts.added) != 1 || accounts.added[0] != "lee@amazon.com" {
		test.Fatalf("invite %d %s %v", response.Code, response.Body.String(), accounts.added)
	}

	response = performanceHTTPDo(api, "owner", http.MethodGet, "/api/performance/onboarding", "")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"roles":{"AM":"pending","SA":"member"}`) {
		test.Fatalf("summary %d %s", response.Code, response.Body.String())
	}
}
