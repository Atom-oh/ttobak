package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

type accountReadingRepo struct {
	*mockAccountRepo
	queries          int
	afterQuery       func()
	afterQueryAt     int
	afterPublication func()
}

func (r *accountReadingRepo) GetMeetingPublication(ctx context.Context, owner, meeting string) (*model.Meeting, error) {
	value, err := r.mockAccountRepo.GetMeetingPublication(ctx, owner, meeting)
	if value != nil && value.SummaryPrivacy != nil {
		privacy := *value.SummaryPrivacy
		value.SummaryPrivacy = &privacy
	}
	if r.afterPublication != nil {
		callback := r.afterPublication
		r.afterPublication = nil
		callback()
	}
	return value, err
}

func (r *accountReadingRepo) ListAccountMembers(context.Context, string) ([]model.AccountMember, error) {
	return nil, errors.New("unbounded member collection called")
}
func (r *accountReadingRepo) ListMeetingRefsForAccount(context.Context, string) ([]model.MeetingRef, error) {
	return nil, errors.New("unbounded meeting collection called")
}
func (r *accountReadingRepo) ListInsightsForAccount(context.Context, string) ([]model.AccountInsight, error) {
	return nil, errors.New("unbounded insight collection called")
}
func (r *accountReadingRepo) QueryAccountReading(_ context.Context, account, phase, after string) (*repository.AccountReadingPage, error) {
	r.queries++
	rows := map[string]interface{}{}
	switch phase {
	case "members":
		for _, member := range r.members {
			if member.AccountID == account {
				rows[member.SK] = *member
			}
		}
	case "meetings":
		for _, ref := range r.meetingRefs[account] {
			rows[ref.SK] = ref
		}
	case "insights":
		for _, insight := range r.insightsByAccount[account] {
			insight.Evidence = ""
			rows[insight.SK] = insight
		}
	default:
		return nil, ErrInvalidInput
	}
	keys := []string{}
	for key := range rows {
		if key > after {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	page := &repository.AccountReadingPage{}
	for i, key := range keys {
		if i == 10 {
			page.NextKey = keys[i-1]
			break
		}
		switch value := rows[key].(type) {
		case model.AccountMember:
			page.Members = append(page.Members, value)
		case model.MeetingRef:
			page.Meetings = append(page.Meetings, value)
		case model.AccountInsight:
			page.Insights = append(page.Insights, value)
		}
	}
	if r.afterQuery != nil && (r.afterQueryAt == 0 || r.queries == r.afterQueryAt) {
		callback := r.afterQuery
		r.afterQuery = nil
		callback()
	}
	return page, nil
}

func accountReadingFixture() (*AccountReadingService, *accountReadingRepo) {
	r := &accountReadingRepo{mockAccountRepo: newMockAccountRepo()}
	r.accounts["acct"] = &model.Account{PK: "ACCOUNT#acct", SK: model.SKAccountMeta,
		EntityType: model.EntityTypeAccount, AccountID: "acct", Name: strings.Repeat("한😀\"\\\n", 6000), OwnerUserID: "reader"}
	r.members["acct|reader"] = &model.AccountMember{
		PK: "ACCOUNT#acct", SK: "MEMBER#reader", AccountID: "acct", UserID: "reader", Role: "owner",
		EntityType: model.EntityTypeAccountMember,
	}
	for i := 0; i < 25; i++ {
		id := fmt.Sprintf("u-%02d", i)
		r.members["acct|"+id] = &model.AccountMember{PK: "ACCOUNT#acct", SK: "MEMBER#" + id,
			AccountID: "acct", UserID: id, Role: "viewer", EntityType: model.EntityTypeAccountMember}
	}
	now := time.Date(2026, 10, 1, 1, 2, 3, 0, time.UTC)
	for i := 0; i < 15; i++ {
		id := fmt.Sprintf("m-%02d", i)
		r.meetings[id] = &model.Meeting{PK: "USER#author", SK: "MEETING#" + id, EntityType: "MEETING",
			MeetingID: id, UserID: "author", AccountID: "acct", SharedToAccount: true}
		r.meetingRefs["acct"] = append(r.meetingRefs["acct"], model.MeetingRef{
			PK: "ACCOUNT#acct", SK: model.PrefixMeetingRef + now.Format(time.RFC3339) + "#" + id,
			AccountID: "acct", MeetingID: id, OwnerUserID: "author", Title: "Meeting " + id, Date: now,
			EntityType: model.EntityTypeMeetingRef,
		})
		r.insightsByAccount["acct"] = append(r.insightsByAccount["acct"], model.AccountInsight{
			PK: "ACCOUNT#acct", SK: fmt.Sprintf("INSIGHT#%03d", i), AccountID: "acct",
			Type: model.InsightNeed, Text: strings.Repeat("인사이트😀\"\\", 1000),
			Evidence: "PRIVATE_EVIDENCE", SourceType: "news", SourceID: fmt.Sprintf("news-%02d", i),
			OccurredAt: now, EntityType: model.EntityTypeInsight,
		})
	}
	return NewAccountReadingService(r), r
}

func collectAccountReading(t *testing.T, s *AccountReadingService, r *accountReadingRepo, q url.Values) map[string][]interface{} {
	t.Helper()
	groups := map[string][]interface{}{}
	var buffer strings.Builder
	groupID := ""
	for n := 0; ; n++ {
		before := r.queries
		body, err := s.Read(context.Background(), "reader", "acct", q)
		if err != nil {
			t.Fatal(err)
		}
		if r.queries-before > 2 || len(body) > 10000 || strings.Contains(string(body), "PRIVATE_EVIDENCE") {
			t.Fatal("account reader exceeded bounds or exposed suppressed Evidence")
		}
		envelope, _ := json.Marshal(map[string]interface{}{
			"content":           []map[string]string{{"type": "text", "text": string(body)}},
			"structuredContent": json.RawMessage(body),
		})
		if len(envelope) > 32000 {
			t.Fatal("account page cannot fit structured MCP output")
		}
		var page map[string]interface{}
		if json.Unmarshal(body, &page) != nil {
			t.Fatal("invalid account JSON")
		}
		fragment := page["fragment"].(map[string]interface{})
		currentID := page["phase"].(string) + "/" + page["revision"].(string)
		if fragment["startOffset"] == float64(0) {
			if buffer.Len() != 0 {
				t.Fatal("new JSON group started before previous completion")
			}
			groupID = currentID
		} else if currentID != groupID {
			t.Fatal("JSON fragment changed source identity")
		}
		buffer.WriteString(page["dataJson"].(string))
		if fragment["complete"] == true {
			var value interface{}
			if json.Unmarshal([]byte(buffer.String()), &value) != nil {
				t.Fatal("JSON group did not restore exactly")
			}
			phase := page["phase"].(string)
			groups[phase] = append(groups[phase], value)
			buffer.Reset()
		}
		continuation := page["page"].(map[string]interface{})
		if continuation["complete"] == true {
			break
		}
		if n > 600 {
			t.Fatal("account read did not finish")
		}
		q.Set("cursor", continuation["nextCursor"].(string))
	}
	return groups
}

func TestAccountReadingRestoresWholeLargeBriefAcrossBoundedSourcesAndFragments(t *testing.T) {
	s, r := accountReadingFixture()
	groups := collectAccountReading(t, s, r, url.Values{"section": {"brief"}, "pageSize": {"8000"}})
	metadata := groups["metadata"][0].(map[string]interface{})
	if metadata["name"] != r.accounts["acct"].Name || metadata["members"] != nil {
		t.Fatal("metadata was truncated or falsely claimed an empty member list")
	}
	for phase, count := range map[string]int{"members": 26, "meetings": 15, "insights": 15} {
		total := 0
		for _, raw := range groups[phase] {
			total += len(raw.([]interface{}))
		}
		if total != count {
			t.Fatalf("brief omitted %s records: got %d want %d", phase, total, count)
		}
	}
}

func TestAccountReadingPinsSelectionAndPayloadWithoutTrustingTimestamps(t *testing.T) {
	s, r := accountReadingFixture()
	q := url.Values{"section": {"metadata"}, "pageSize": {"10"}}
	body, err := s.Read(context.Background(), "reader", "acct", q)
	if err != nil {
		t.Fatal(err)
	}
	var page struct {
		Page struct {
			NextCursor string `json:"nextCursor"`
		} `json:"page"`
	}
	json.Unmarshal(body, &page)
	q.Set("cursor", page.Page.NextCursor)
	q.Set("section", "members")
	if _, err := s.Read(context.Background(), "reader", "acct", q); !errors.Is(err, ErrStaleReadingCursor) {
		t.Fatal("selection changed under a continuation", err)
	}
	q.Set("section", "metadata")
	r.accounts["acct"].Name = "changed without a timestamp"
	if _, err := s.Read(context.Background(), "reader", "acct", q); !errors.Is(err, ErrStaleReadingCursor) {
		t.Fatal("changed account bytes accepted", err)
	}
	delete(r.members, "acct|reader")
	if _, err := s.Read(context.Background(), "reader", "acct", q); !errors.Is(err, ErrForbidden) {
		t.Fatal("revoked membership accepted", err)
	}
}

func TestAccountReadingRevalidatesPublicationAndMembershipDuringPageAssembly(t *testing.T) {
	for _, change := range []string{"membership", "publication"} {
		s, r := accountReadingFixture()
		r.afterQuery = func() {
			if change == "membership" {
				delete(r.members, "acct|reader")
			} else {
				r.meetings["m-00"].SharedToAccount = false
			}
		}
		body, err := s.Read(context.Background(), "reader", "acct", url.Values{"section": {"meetings"}})
		if change == "membership" {
			if err == nil || body != nil {
				t.Fatal("revoked membership received meeting data")
			}
		} else {
			if err != nil {
				t.Fatal(err)
			}
			var value map[string]interface{}
			json.Unmarshal(body, &value)
			if strings.Contains(value["dataJson"].(string), "m-00") {
				t.Fatal("classification or stale ref granted publication access")
			}
		}
	}
}

func TestAccountReadingInsightFiltersAndEmptyGroupsRetainContinuation(t *testing.T) {
	s, r := accountReadingFixture()
	groups := collectAccountReading(t, s, r, url.Values{"section": {"insights"}, "types": {"risk"}})
	if len(groups["insights"]) != 2 {
		t.Fatal("empty filtered provider pages were dropped")
	}
	for _, value := range groups["insights"] {
		if len(value.([]interface{})) != 0 {
			t.Fatal("wrong insight filter")
		}
	}
}

func accountReadingMeetingInsightFixture() (*AccountReadingService, *accountReadingRepo) {
	s, r := accountReadingFixture()
	r.members["acct|reader"].SummaryPrivacyVersion = model.SummaryPrivacyVersion
	meeting := r.meetings["m-00"]
	meeting.InsightsRevision = "current-insights"
	meeting.SummaryPrivacy = &model.SummaryPrivacy{
		Version: model.SummaryPrivacyVersion, State: model.SummaryPrivacySafe,
		GenerationID: "current-generation", ContentSHA256: model.SummaryPrivacyHash("current summary"),
	}
	date := r.meetingRefs["acct"][0].Date
	r.insightsByAccount["acct"] = []model.AccountInsight{{
		PK: "ACCOUNT#acct", SK: "INSIGHT#" + date.Format(time.RFC3339) + "#m-00#0",
		EntityType: model.EntityTypeInsight, AccountID: "acct", Type: model.InsightNeed,
		Text: strings.Repeat("CURRENT_INSIGHT 한😀\"\\", 1000), Evidence: "PRIVATE_EVIDENCE",
		SourceType: "meeting", SourceID: "m-00", SourceUserID: "author", OccurredAt: date,
		SummaryGeneration: "current-generation", InsightsRevision: "current-insights",
	}}
	return s, r
}

func accountReadingContinuation(t *testing.T, body []byte) string {
	t.Helper()
	var page struct {
		Page struct {
			Complete   bool   `json:"complete"`
			NextCursor string `json:"nextCursor"`
		} `json:"page"`
	}
	if err := json.Unmarshal(body, &page); err != nil || page.Page.Complete || page.Page.NextCursor == "" {
		t.Fatal("expected a bounded continuation", err)
	}
	return page.Page.NextCursor
}

func TestAccountReadingPreservesPrivateAndCurrentInsightGuards(t *testing.T) {
	for _, condition := range []string{"current", "stale-generation", "stale-insights", "private", "unknown"} {
		t.Run(condition, func(t *testing.T) {
			s, r := accountReadingMeetingInsightFixture()
			switch condition {
			case "stale-generation":
				r.insightsByAccount["acct"][0].SummaryGeneration = "old-generation"
			case "stale-insights":
				r.insightsByAccount["acct"][0].InsightsRevision = "old-insights"
			case "private":
				r.meetings["m-00"].SummaryPrivacy.State = model.SummaryPrivacyPrivate
			case "unknown":
				r.meetings["m-00"].SummaryPrivacy = nil
			}
			body, err := s.Read(context.Background(), "reader", "acct", url.Values{"section": {"insights"}})
			if condition == "private" || condition == "unknown" {
				if !errors.Is(err, ErrSummaryReadRestricted) || body != nil {
					t.Fatal("restricted source became successful account evidence", err)
				}
				return
			}
			if err != nil || len(body) > 10000 || strings.Contains(string(body), "PRIVATE_EVIDENCE") {
				t.Fatal("current insight read failed its bounds/privacy contract", err)
			}
			visible := strings.Contains(string(body), "CURRENT_INSIGHT")
			if visible != (condition == "current") {
				t.Fatal("insight freshness was not bound to the current source", condition)
			}
		})
	}
}

func TestAccountReadingPinsInsightBytesAndSourceReadinessAcrossFragments(t *testing.T) {
	for _, change := range []string{"bytes", "generation", "revision", "publication", "privacy", "grant-epoch"} {
		t.Run(change, func(t *testing.T) {
			s, r := accountReadingMeetingInsightFixture()
			q := url.Values{"section": {"insights"}, "pageSize": {"20"}}
			body, err := s.Read(context.Background(), "reader", "acct", q)
			if err != nil {
				t.Fatal(err)
			}
			q.Set("cursor", accountReadingContinuation(t, body))
			want := ErrStaleReadingCursor
			switch change {
			case "bytes":
				r.insightsByAccount["acct"][0].Text += "changed without timestamp"
			case "generation":
				r.meetings["m-00"].SummaryPrivacy.GenerationID = "new-generation"
			case "revision":
				r.meetings["m-00"].InsightsRevision = "new-insights"
			case "publication":
				r.meetings["m-00"].SharedToAccount = false
			case "privacy":
				r.meetings["m-00"].SummaryPrivacy.State = model.SummaryPrivacyPrivate
				want = ErrSummaryReadRestricted
			case "grant-epoch":
				r.members["acct|reader"].SummaryPrivacyVersion++
			}
			body, err = s.Read(context.Background(), "reader", "acct", q)
			if !errors.Is(err, want) || body != nil {
				t.Fatal("changed source/grant continued a previous fragment", err)
			}
		})
	}
}

func TestAccountReadingRechecksInsightSourceBeforeReturningBytes(t *testing.T) {
	for _, change := range []string{"generation", "revision", "publication", "privacy"} {
		t.Run(change, func(t *testing.T) {
			s, r := accountReadingMeetingInsightFixture()
			r.afterPublication = func() {
				switch change {
				case "generation":
					r.meetings["m-00"].SummaryPrivacy.GenerationID = "replaced"
				case "revision":
					r.meetings["m-00"].InsightsRevision = "replaced"
				case "publication":
					r.meetings["m-00"].SharedToAccount = false
				case "privacy":
					r.meetings["m-00"].SummaryPrivacy.State = model.SummaryPrivacyPrivate
				}
			}
			body, err := s.Read(context.Background(), "reader", "acct", url.Values{"section": {"insights"}})
			if err == nil || body != nil {
				t.Fatal("source changed during assembly but derived bytes escaped", change)
			}
		})
	}
}

func TestAccountReadingPrivacyEpochInvalidatesMetadataContinuation(t *testing.T) {
	s, r := accountReadingFixture()
	q := url.Values{"section": {"metadata"}, "pageSize": {"10"}}
	body, err := s.Read(context.Background(), "reader", "acct", q)
	if err != nil {
		t.Fatal(err)
	}
	q.Set("cursor", accountReadingContinuation(t, body))
	r.members["acct|reader"].SummaryPrivacyVersion = model.SummaryPrivacyVersion
	body, err = s.Read(context.Background(), "reader", "acct", q)
	if !errors.Is(err, ErrStaleReadingCursor) || body != nil {
		t.Fatal("JSON-hidden authorization epoch did not invalidate continuation", err)
	}
}

func TestAccountReadingRechecksGrantAfterFinalSourceWindow(t *testing.T) {
	s, r := accountReadingFixture()
	r.afterQueryAt = 2
	r.afterQuery = func() {
		r.members["acct|reader"].SummaryPrivacyVersion = model.SummaryPrivacyVersion
	}
	body, err := s.Read(context.Background(), "reader", "acct", url.Values{"section": {"members"}})
	if !errors.Is(err, ErrStaleReadingCursor) || body != nil || r.queries != 2 {
		t.Fatal("grant changed during final source validation but bytes escaped", err)
	}
}

func TestAccountReadingRejectsNoncanonicalMembershipIdentity(t *testing.T) {
	for _, field := range []string{"partition", "member-key", "entity"} {
		t.Run(field, func(t *testing.T) {
			s, r := accountReadingFixture()
			member := r.members["acct|reader"]
			switch field {
			case "partition":
				member.PK = "ACCOUNT#other"
			case "member-key":
				member.SK = "MEMBER#other"
			case "entity":
				member.EntityType = model.EntityTypeInsight
			}
			body, err := s.Read(context.Background(), "reader", "acct", url.Values{"section": {"metadata"}})
			if !errors.Is(err, ErrForbidden) || body != nil || r.queries != 0 {
				t.Fatal("a mismatched canonical grant authorized account data", err)
			}
		})
	}
}
