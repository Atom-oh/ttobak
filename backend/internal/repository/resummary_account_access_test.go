package repository

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
)

// Account-team requesters are fenced by the live membership that grants them
// creator-equivalent edit access (ADR-016 2026-11-06 amendment).
func TestResummaryCaptureAccountTeamAccess(t *testing.T) {
	published := `{"PK":{"S":"USER#owner"},"SK":{"S":"MEETING#meeting"},"meetingId":{"S":"meeting"},"userId":{"S":"owner"},"status":{"S":"done"},"content":{"S":"saved"},"accountId":{"S":"child"},"sharedToAccount":{"BOOL":true}}`
	private := `{"PK":{"S":"USER#owner"},"SK":{"S":"MEETING#meeting"},"meetingId":{"S":"meeting"},"userId":{"S":"owner"},"status":{"S":"done"},"content":{"S":"saved"},"accountId":{"S":"child"}}`
	member := func(account string) string {
		return `{"PK":{"S":"ACCOUNT#` + account + `"},"SK":{"S":"MEMBER#user"},"accountId":{"S":"` + account + `"},"userId":{"S":"user"},"addedAt":{"S":"2026-01-01T00:00:00Z"}}`
	}
	meta := func(account, parent string) string {
		item := `{"PK":{"S":"ACCOUNT#` + account + `"},"SK":{"S":"META"},"accountId":{"S":"` + account + `"}`
		if parent != "" {
			item += `,"parentAccountId":{"S":"` + parent + `"}`
		}
		return item + `}`
	}
	share := func(permission, origin string) string {
		item := `{"PK":{"S":"USER#user"},"SK":{"S":"SHARED#meeting"},"ownerId":{"S":"owner"},"meetingId":{"S":"meeting"},"permission":{"S":"` + permission + `"}`
		if origin != "" {
			item += `,"origin":{"S":"` + origin + `"},"accountId":{"S":"child"}`
		}
		return item + `}`
	}
	for _, tc := range []struct {
		name        string
		requester   string
		descendants bool
		items       map[string]string
		ok          bool
		wantChecks  []string
	}{
		{name: "owner", requester: "owner", items: map[string]string{"USER#owner|MEETING#meeting": published}, ok: true,
			wantChecks: []string{"USER#owner|MEETING#meeting"}},
		{name: "direct edit share", requester: "user", items: map[string]string{
			"USER#owner|MEETING#meeting": private, "USER#user|SHARED#meeting": share("edit", "")}, ok: true,
			wantChecks: []string{"USER#user|SHARED#meeting"}},
		{name: "direct read share without membership", requester: "user", items: map[string]string{
			"USER#owner|MEETING#meeting": published, "USER#user|SHARED#meeting": share("read", "")}},
		{name: "direct read share yields to team membership", requester: "user", items: map[string]string{
			"USER#owner|MEETING#meeting": published, "USER#user|SHARED#meeting": share("read", ""),
			"ACCOUNT#child|MEMBER#user": member("child")}, ok: true,
			wantChecks: []string{"USER#user|SHARED#meeting", "ACCOUNT#child|MEMBER#user"}},
		{name: "account-origin share with member row", requester: "user", items: map[string]string{
			"USER#owner|MEETING#meeting": published, "USER#user|SHARED#meeting": share("read", "account"),
			"ACCOUNT#child|MEMBER#user": member("child")}, ok: true,
			wantChecks: []string{"USER#user|SHARED#meeting", "ACCOUNT#child|MEMBER#user"}},
		{name: "member without share row", requester: "user", items: map[string]string{
			"USER#owner|MEETING#meeting": published, "ACCOUNT#child|MEMBER#user": member("child")}, ok: true,
			wantChecks: []string{"USER#user|SHARED#meeting", "ACCOUNT#child|MEMBER#user"}},
		{name: "no member row and descendants off", requester: "user", items: map[string]string{
			"USER#owner|MEETING#meeting": published, "ACCOUNT#child|META": meta("child", "parent"),
			"ACCOUNT#parent|MEMBER#user": member("parent")}},
		{name: "ancestor member with descendants on", requester: "user", descendants: true, items: map[string]string{
			"USER#owner|MEETING#meeting": published, "ACCOUNT#child|META": meta("child", "mid"),
			"ACCOUNT#mid|META": meta("mid", "root"), "ACCOUNT#root|MEMBER#user": member("root")}, ok: true,
			wantChecks: []string{"ACCOUNT#child|META", "ACCOUNT#mid|META", "ACCOUNT#root|MEMBER#user"}},
		{name: "no ancestor member with descendants on", requester: "user", descendants: true, items: map[string]string{
			"USER#owner|MEETING#meeting": published, "ACCOUNT#child|META": meta("child", "root"),
			"ACCOUNT#root|META": meta("root", "")}},
		{name: "member of unpublished meeting", requester: "user", items: map[string]string{
			"USER#owner|MEETING#meeting": private, "ACCOUNT#child|MEMBER#user": member("child")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.descendants {
				t.Setenv("ACCOUNT_DESCENDANT_READS_ENABLED", "1")
			} else {
				t.Setenv("ACCOUNT_DESCENDANT_READS_ENABLED", "")
			}
			repo := summarySDKRepo(func(req *http.Request) (*http.Response, error) {
				var body map[string]any
				json.NewDecoder(req.Body).Decode(&body)
				if strings.HasSuffix(req.Header.Get("X-Amz-Target"), ".Query") {
					return summaryHTTPResponse(200, `{"Items":[]}`, nil), nil
				}
				key := body["Key"].(map[string]any)
				pk := key["PK"].(map[string]any)["S"].(string)
				sk := key["SK"].(map[string]any)["S"].(string)
				if item, ok := tc.items[pk+"|"+sk]; ok {
					return summaryHTTPResponse(200, `{"Item":`+item+`}`, nil), nil
				}
				return summaryHTTPResponse(200, `{}`, nil), nil
			})
			snapshot, err := repo.CaptureResummary(context.Background(), "owner", "meeting", tc.requester)
			if !tc.ok {
				if !errors.Is(err, ErrSummaryAccess) {
					t.Fatalf("want ErrSummaryAccess, got %v", err)
				}
				return
			}
			if err != nil || snapshot == nil {
				t.Fatalf("capture: %v", err)
			}
			checks := map[string]bool{}
			for _, check := range snapshot.Checks {
				checks[check.PK+"|"+check.SK] = true
			}
			for _, want := range tc.wantChecks {
				if !checks[want] {
					t.Fatalf("missing fence %s in %+v", want, snapshot.Checks)
				}
			}
		})
	}
}
