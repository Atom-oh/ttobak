package repository

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ttobak/backend/internal/model"
)

func teamTransact(test *testing.T, capture *[]any) *DynamoDBRepository {
	return summarySDKRepo(func(request *http.Request) (*http.Response, error) {
		body := performanceBody(test, request)
		if performanceTarget(request) == "GetItem" {
			return summaryHTTPResponse(200, `{}`, nil), nil
		}
		*capture = body["TransactItems"].([]any)
		return summaryHTTPResponse(200, `{}`, nil), nil
	})
}

func TestTeamExternalIDRoundTrip(test *testing.T) {
	pinPerformanceNow(test)
	var operations []any
	repo := teamTransact(test, &operations)
	req := model.PerformanceSyncRequest{BatchID: performanceTestBatch, Complete: true, Mutations: []model.PerformanceMutation{
		{Op: "upsert", TeamMember: &model.SFDCTeamMember{SFDCAccountID: "001A", Alias: "kim.lee", Name: "Kim", Title: "SA", TeamMemberRole: "Solutions Architect", AllTeamRoles: "Solutions Architect"}},
		{Op: "remove", ExternalID: "001A:old.one"},
	}}
	receipt, err := repo.ApplyPerformanceBatch(test.Context(), "owner", model.PerformanceSourceSFDCAccountTeam, req, performanceTestDigest)
	if err != nil || receipt.Upserted != 1 || receipt.Removed != 1 {
		test.Fatalf("receipt %+v %v", receipt, err)
	}
	if len(operations) != 4 {
		test.Fatalf("source, upsert, removal and receipt must share one transaction: %d", len(operations))
	}
	item := operations[1].(map[string]any)["Put"].(map[string]any)["Item"].(map[string]any)
	if item["SK"].(map[string]any)["S"] != "PERFTEAM#001A#kim.lee" || item["alias"].(map[string]any)["S"] != "kim.lee" {
		test.Fatalf("team row %v", item)
	}
	for _, forbidden := range []string{"email", "userId_sfdc", "accountAccessLevel", "createdDate", "pendingShareExpiresAt"} {
		if _, found := item[forbidden]; found {
			test.Fatalf("team row must not store %s: %v", forbidden, item)
		}
	}
	removal := operations[2].(map[string]any)["Delete"].(map[string]any)["Key"].(map[string]any)
	if removal["SK"].(map[string]any)["S"] != "PERFTEAM#001A#old.one" {
		test.Fatalf("removal key %v", removal)
	}
}

func TestTeamUpsertRejectsInvalid(test *testing.T) {
	pinPerformanceNow(test)
	repo := summarySDKRepo(func(request *http.Request) (*http.Response, error) {
		if performanceTarget(request) == "GetItem" {
			return summaryHTTPResponse(200, `{}`, nil), nil
		}
		test.Fatalf("invalid team batch reached a write")
		return nil, nil
	})
	bad := []model.PerformanceMutation{
		{Op: "upsert", TeamMember: &model.SFDCTeamMember{SFDCAccountID: "001A", Alias: "Kim"}},
		{Op: "upsert", TeamMember: &model.SFDCTeamMember{SFDCAccountID: "001A", Alias: "kim@x"}},
		{Op: "upsert", TeamMember: &model.SFDCTeamMember{SFDCAccountID: "0#1", Alias: "kim"}},
		{Op: "upsert", TeamMember: &model.SFDCTeamMember{SFDCAccountID: "001A", Alias: "kim", Name: strings.Repeat("x", 513)}},
		{Op: "upsert", Assignment: &model.SFDCAssignment{SFDCAccountID: "001A"}},
		{Op: "remove", ExternalID: "001A"},
		{Op: "remove", ExternalID: "001A:kim", TeamMember: &model.SFDCTeamMember{}},
	}
	for _, mutation := range bad {
		req := model.PerformanceSyncRequest{BatchID: performanceTestBatch, Mutations: []model.PerformanceMutation{mutation}}
		if _, err := repo.ApplyPerformanceBatch(test.Context(), "owner", model.PerformanceSourceSFDCAccountTeam, req, performanceTestDigest); !errors.Is(err, ErrInvalidPerformanceBatch) {
			test.Errorf("%+v: %v", mutation, err)
		}
	}
	cross := model.PerformanceSyncRequest{BatchID: performanceTestBatch, Mutations: []model.PerformanceMutation{
		{Op: "upsert", Assignment: &model.SFDCAssignment{SFDCAccountID: "001A"}, TeamMember: &model.SFDCTeamMember{SFDCAccountID: "001A", Alias: "kim"}}}}
	if _, err := repo.ApplyPerformanceBatch(test.Context(), "owner", model.PerformanceSourceSFDCAssignments, cross, performanceTestDigest); !errors.Is(err, ErrInvalidPerformanceBatch) {
		test.Errorf("assignment batch with a team row: %v", err)
	}
}

func TestListPerformanceTeamScopesAndBounds(test *testing.T) {
	var body map[string]any
	repo := summarySDKRepo(func(request *http.Request) (*http.Response, error) {
		body = performanceBody(test, request)
		return summaryHTTPResponse(200, `{"Items":[{"PK":{"S":"USER#owner"},"SK":{"S":"PERFTEAM#001A#kim"},"userId":{"S":"owner"},"sfdcAccountId":{"S":"001A"},"alias":{"S":"kim"},"name":{"S":"Kim"}}]}`, nil), nil
	})
	team, err := repo.ListPerformanceTeam(test.Context(), "owner", "001A")
	if err != nil || len(team) != 1 || team[0].Alias != "kim" || team[0].Name != "Kim" {
		test.Fatalf("team %+v %v", team, err)
	}
	if limit, _ := body["Limit"].(float64); int(limit) != model.PerformanceTeamMaxPerAccount+1 {
		test.Fatalf("Limit %v", body["Limit"])
	}
	values := body["ExpressionAttributeValues"].(map[string]any)
	found := false
	for _, value := range values {
		if value.(map[string]any)["S"] == "PERFTEAM#001A#" {
			found = true
		}
	}
	if !found {
		test.Fatalf("query must use the account prefix: %v", values)
	}
	if _, err := repo.ListPerformanceTeam(test.Context(), "owner", "0#1"); !errors.Is(err, ErrInvalidPerformanceInput) {
		test.Fatalf("bad account id: %v", err)
	}
	// A row for another account under the prefix is corrupt data, never returned.
	repo = summarySDKRepo(func(*http.Request) (*http.Response, error) {
		return summaryHTTPResponse(200, `{"Items":[{"PK":{"S":"USER#owner"},"SK":{"S":"PERFTEAM#001A#kim"},"userId":{"S":"owner"},"sfdcAccountId":{"S":"001B"},"alias":{"S":"kim"}}]}`, nil), nil
	})
	if _, err := repo.ListPerformanceTeam(test.Context(), "owner", "001A"); !errors.Is(err, ErrPerformanceCorruptData) {
		test.Fatalf("mismatched row: %v", err)
	}
}

func TestPerformanceInviteRecord(test *testing.T) {
	pinPerformanceNow(test)
	var item map[string]any
	repo := summarySDKRepo(func(request *http.Request) (*http.Response, error) {
		item = performanceBody(test, request)["Item"].(map[string]any)
		return summaryHTTPResponse(200, `{}`, nil), nil
	})
	invite := model.PerformanceInvite{SFDCAccountID: "001A", Alias: "kim", AccountID: "acc1", Role: model.RoleSA, InvitedAt: "2026-11-10T00:00:00Z"}
	if err := repo.PutPerformanceInvite(test.Context(), "owner", invite); err != nil {
		test.Fatal(err)
	}
	if item["SK"].(map[string]any)["S"] != "PERFINVITE#001A#kim" || item["pendingShareExpiresAt"].(map[string]any)["N"] != mustJSONInt(performanceTestClock.Add(30*24*time.Hour).Unix()) {
		test.Fatalf("invite row %v", item)
	}
	if _, found := item["email"]; found {
		test.Fatalf("invite row must not store an email: %v", item)
	}
	if err := repo.PutPerformanceInvite(test.Context(), "owner", model.PerformanceInvite{SFDCAccountID: "001A", Alias: "Kim", AccountID: "acc1", InvitedAt: "2026-11-10T00:00:00Z"}); !errors.Is(err, ErrInvalidPerformanceInput) {
		test.Fatalf("bad alias: %v", err)
	}

	repo = summarySDKRepo(func(*http.Request) (*http.Response, error) {
		row := func(alias string, expires string) string {
			return `{"PK":{"S":"USER#owner"},"SK":{"S":"PERFINVITE#001A#` + alias + `"},"userId":{"S":"owner"},"sfdcAccountId":{"S":"001A"},"alias":{"S":"` + alias + `"},"accountId":{"S":"acc1"},"invitedAt":{"S":"2026-11-01T00:00:00Z"},"pendingShareExpiresAt":{"N":"` + expires + `"}}`
		}
		return summaryHTTPResponse(200, `{"Items":[`+row("live", "1999999999")+`,`+row("gone", "1")+`]}`, nil), nil
	})
	invites, err := repo.ListPerformanceInvites(test.Context(), "owner", "001A")
	if err != nil || len(invites) != 1 || invites[0].Alias != "live" {
		test.Fatalf("expired invites must be skipped: %+v %v", invites, err)
	}
}
