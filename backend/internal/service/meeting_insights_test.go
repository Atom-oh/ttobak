package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/ttobak/backend/internal/model"
)

func TestMeetingDetailPreparationProjection(t *testing.T) {
	for _, permission := range []string{"owner", "read", "edit", ""} {
		t.Run("permission="+permission, func(t *testing.T) {
			repo := newMockMeetingRepo()
			repo.addMeeting(&model.Meeting{MeetingID: "m1", UserID: "owner", Status: model.StatusDone,
				AccountID: "account", SharedToAccount: true, ProjectIDs: []string{"private-project"},
				Insights: `[{"id":"ins_7","type":"need","text":"capacity","evidence":"meeting quotation"}]`, UpdatedAt: time.Now().UTC()})
			caller := "viewer"
			if permission == "owner" {
				caller = "owner"
			} else if permission != "" {
				repo.shares[shareKey(caller, "m1")] = &model.Share{OwnerID: "owner", Permission: permission}
			}
			detail, err := newMeetingServiceWithRepo(repo).GetMeetingDetail(context.Background(), caller, "m1")
			if permission == "" {
				if err != ErrNotFound || detail != nil {
					t.Fatal("unauthorized reader received meeting insights")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !detail.SupportsNotesComparison || !detail.SupportsPrivateAccountLink {
				t.Fatal("detail did not advertise the guarded workflow contracts")
			}
			body, _ := json.Marshal(detail)
			var got struct {
				AccountID              string                 `json:"accountId"`
				SharedToAccount        bool                   `json:"sharedToAccount"`
				ProjectIDs             []string               `json:"projectIds"`
				FieldInsights          []model.MeetingInsight `json:"fieldInsights"`
				FieldInsightsFreshness string                 `json:"fieldInsightsFreshness"`
			}
			json.Unmarshal(body, &got)
			if got.AccountID != "account" || !got.SharedToAccount || got.FieldInsightsFreshness != "unknown" ||
				len(got.FieldInsights) != 1 || got.FieldInsights[0].ID != "ins_7" || got.FieldInsights[0].Evidence != "meeting quotation" {
				t.Fatalf("missing authorized preparation projection: %s", body)
			}
			if permission == "owner" && (len(got.ProjectIDs) != 1 || got.ProjectIDs[0] != "private-project") {
				t.Fatal("owner lost project associations")
			}
			if permission != "owner" && strings.Contains(string(body), "projectIds") {
				t.Fatal("shared reader received private project associations")
			}
		})
	}
}

func TestMeetingDetailInsightsAreBoundedAndFailuresVisible(t *testing.T) {
	many := make([]model.MeetingInsight, 51)
	for i := range many {
		many[i] = model.MeetingInsight{ID: "stable-id", Type: "need", Text: "need"}
	}
	manyJSON, _ := json.Marshal(many)
	long := model.MeetingInsight{ID: "original-id", Type: "risk", Text: strings.Repeat("😀", 6000),
		Evidence: strings.Repeat("한", 6000), Implication: strings.Repeat("<", 6000), NextAction: strings.Repeat(">", 6000),
		TsMarker: strings.Repeat("T", 300), Entities: make([]string, 30)}
	for i := range long.Entities {
		long.Entities[i] = strings.Repeat("고객", 300)
	}
	longJSON, _ := json.Marshal([]model.MeetingInsight{long})
	large := make([]model.MeetingInsight, 50)
	for i := range large {
		large[i] = long
	}
	largeJSON, _ := json.Marshal(large)
	for _, tc := range []struct {
		name, raw string
		wantError bool
		truncated bool
		count     int
	}{
		{"absent", "", false, false, 0},
		{"empty array", "[]", false, false, 0},
		{"invalid JSON", `{"private":"unparseable"`, true, false, 0},
		{"wrong shape", `{}`, true, false, 0},
		{"null array", `null`, true, false, 0},
		{"invalid type", `[{"type":"invented","text":"bad"}]`, true, false, 0},
		{"count cap", string(manyJSON), false, true, 50},
		{"field caps", string(longJSON), false, true, 1},
		{"encoded budget", string(largeJSON), false, true, -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := newMockMeetingRepo()
			repo.addMeeting(&model.Meeting{MeetingID: "m1", UserID: "owner", Status: model.StatusDone, Insights: tc.raw})
			detail, err := newMeetingServiceWithRepo(repo).GetMeetingDetail(context.Background(), "owner", "m1")
			if err != nil {
				t.Fatal(err)
			}
			body, _ := json.Marshal(detail)
			var got struct {
				Insights  []model.MeetingInsight `json:"fieldInsights"`
				Error     string                 `json:"fieldInsightsError"`
				Truncated bool                   `json:"fieldInsightsTruncated"`
			}
			json.Unmarshal(body, &got)
			if got.Insights == nil || (tc.count >= 0 && len(got.Insights) != tc.count) || len(got.Insights) > 50 ||
				(got.Error != "") != tc.wantError || got.Truncated != tc.truncated {
				t.Fatalf("bad insight projection: count=%d error=%q truncated=%v", len(got.Insights), got.Error, got.Truncated)
			}
			encoded, _ := json.Marshal(got.Insights)
			if len(encoded) > 64*1024 || !utf8.Valid(encoded) {
				t.Fatalf("unbounded/invalid encoded insight payload: %d bytes", len(encoded))
			}
			if tc.name == "field caps" && (utf8.RuneCountInString(got.Insights[0].Text) > 2000 ||
				utf8.RuneCountInString(got.Insights[0].Evidence) > 2000 || len(got.Insights[0].Entities) > 20) {
				t.Fatal("long source fields bypassed display bounds")
			}
			if tc.name == "encoded budget" && len(got.Insights) == 0 {
				t.Fatal("bounded projection discarded every usable insight")
			}
		})
	}
}
