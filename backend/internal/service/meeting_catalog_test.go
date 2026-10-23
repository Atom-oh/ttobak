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
	"unicode/utf8"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

type catalogMeetingRepo struct {
	*mockMeetingRepo
	queries, metadataReads, invitations int
	hideMembershipIndex                 bool
	directIndex                         map[string]bool
	afterQuery                          func()
}

func (r *catalogMeetingRepo) GetOrCreateUser(context.Context, string, string, string) (*model.User, bool, error) {
	panic("read-only catalog must not create or update a profile")
}

func (r *catalogMeetingRepo) ListMeetings(context.Context, repository.ListMeetingsParams) (*repository.ListMeetingsResult, error) {
	return nil, errors.New("unbounded legacy list")
}
func (r *catalogMeetingRepo) ListAccountsForUser(context.Context, string) ([]model.AccountMember, error) {
	return nil, errors.New("unbounded account discovery")
}
func (r *catalogMeetingRepo) ListPendingShares(context.Context, string) ([]model.PendingShare, error) {
	return nil, errors.New("unbounded invitation queue")
}
func (r *catalogMeetingRepo) GetMeetingCatalogMetadata(ctx context.Context, owner, id string) (*model.Meeting, error) {
	r.metadataReads++
	m, err := r.mockMeetingRepo.GetMeeting(ctx, owner, id)
	if m == nil || err != nil {
		return m, err
	}
	copy := *m
	return &copy, nil
}
func (r *catalogMeetingRepo) QueryMeetingCatalog(_ context.Context, user, phase, account, after string, limit int) (*repository.MeetingCatalogPage, error) {
	r.queries++
	rows := map[string]repository.MeetingCatalogCandidate{}
	switch phase {
	case "owned":
		for _, m := range r.meetings {
			if m.UserID == user {
				rows[m.MeetingID] = repository.MeetingCatalogCandidate{MeetingID: m.MeetingID, OwnerID: user}
			}
		}
	case "direct":
		for _, s := range r.shares {
			if s.SharedToID == user && (r.directIndex == nil || r.directIndex[s.MeetingID]) {
				rows[s.MeetingID] = repository.MeetingCatalogCandidate{MeetingID: s.MeetingID}
			}
		}
	case "team":
		for _, ref := range r.meetingRefs[account] {
			rows[ref.MeetingID] = repository.MeetingCatalogCandidate{MeetingID: ref.MeetingID, OwnerID: ref.OwnerUserID}
		}
	default:
		return nil, ErrInvalidInput
	}
	keys := []string{}
	for k := range rows {
		if k > after {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	page := &repository.MeetingCatalogPage{Rows: []repository.MeetingCatalogCandidate{}}
	for i, k := range keys {
		if i == limit {
			page.NextKey = keys[i-1]
			break
		}
		page.Rows = append(page.Rows, rows[k])
	}
	if r.afterQuery != nil {
		fn := r.afterQuery
		r.afterQuery = nil
		fn()
	}
	return page, nil
}
func (r *catalogMeetingRepo) QueryNavigationCandidates(_ context.Context, user, kind, account, after string, limit int) (*repository.NavigationCandidatePage, error) {
	if limit != 1 || kind != "accounts" {
		return nil, ErrInvalidInput
	}
	page := &repository.NavigationCandidatePage{IDs: []string{}}
	if r.hideMembershipIndex {
		return page, nil
	}
	keys := []string{}
	for _, m := range r.members {
		if m.UserID == user && m.AccountID > after {
			keys = append(keys, m.AccountID)
		}
	}
	sort.Strings(keys)
	if len(keys) > 0 {
		page.IDs = append(page.IDs, keys[0])
		if len(keys) > 1 {
			page.NextID = keys[0]
		}
	}
	return page, nil
}
func (r *catalogMeetingRepo) QueryMeetingCatalogInvitation(_ context.Context, email, after string) (*model.PendingShare, string, error) {
	r.invitations++
	rows := []*model.PendingShare{}
	for _, p := range r.pendingShares {
		if p.Email == email && p.SK > after {
			rows = append(rows, p)
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].SK < rows[j].SK })
	if len(rows) == 0 {
		return nil, "", nil
	}
	copy := *rows[0]
	next := ""
	if len(rows) > 1 {
		next = copy.SK
	}
	return &copy, next, nil
}
func catalogMeetingFixture() (*MeetingCatalogService, *catalogMeetingRepo) {
	r := &catalogMeetingRepo{mockMeetingRepo: newMockMeetingRepo()}
	r.members["a|reader"] = &model.AccountMember{AccountID: "a", UserID: "reader", Role: "viewer"}
	for kind, count := range map[string]int{"owned": 21, "direct": 12, "team": 12} {
		for i := 0; i < count; i++ {
			id := fmt.Sprintf("%s-%02d", kind, i)
			owner := "author"
			if kind == "owned" {
				owner = "reader"
			}
			m := &model.Meeting{MeetingID: id, UserID: owner, AccountID: "a", Title: "회의 " + id, Content: strings.Repeat("한국어😀", 100), Date: time.Now().UTC(), SharedToAccount: kind == "team"}
			if i == 0 {
				m.Title = strings.Repeat("회의😀\"\\\n", 5000)
				m.Participants = []string{strings.Repeat("참여자😀", 2000)}
				m.Tags = []string{strings.Repeat("태그\\\"", 2000)}
			}
			r.meetings[meetingKey(owner, id)] = m
			if kind == "direct" {
				r.shares[shareKey("reader", id)] = &model.Share{MeetingID: id, OwnerID: owner, SharedToID: "reader", Permission: "edit"}
			}
			if kind == "team" {
				r.meetingRefs["a"] = append(r.meetingRefs["a"], model.MeetingRef{MeetingID: id, OwnerUserID: owner})
			}
		}
	}
	return NewMeetingCatalogService(r), r
}
func readCatalogMeetingPage(t *testing.T, s *MeetingCatalogService, id MeetingCatalogIdentity, q url.Values) map[string]interface{} {
	t.Helper()
	raw, err := s.Read(context.Background(), id, q)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > 10000 || !utf8.Valid(raw) {
		t.Fatal("encoded page bound")
	}
	envelope, _ := json.Marshal(map[string]interface{}{"content": []map[string]string{{"type": "text", "text": string(raw)}}, "structuredContent": json.RawMessage(raw)})
	if len(envelope) > 32000 {
		t.Fatal("MCP structured envelope exceeds bound")
	}
	var page map[string]interface{}
	if json.Unmarshal(raw, &page) != nil {
		t.Fatal("invalid JSON")
	}
	return page
}
func collectCatalogMeetings(t *testing.T, s *MeetingCatalogService, r *catalogMeetingRepo, id MeetingCatalogIdentity, q url.Values) map[string]model.MeetingListItem {
	t.Helper()
	rows := map[string]model.MeetingListItem{}
	var buffer strings.Builder
	revision := ""
	for n := 0; ; n++ {
		before, invites := r.queries, r.invitations
		page := readCatalogMeetingPage(t, s, id, q)
		if r.queries-before > 2 || r.invitations-invites > 1 {
			t.Fatal("unbounded provider work")
		}
		if page["discoveryPending"] == true {
			if page["dataJson"] != nil {
				t.Fatal("discovery invented meeting data")
			}
		} else {
			fragment := page["fragment"].(map[string]interface{})
			if fragment["startOffset"] == float64(0) {
				if buffer.Len() != 0 {
					t.Fatal("lost JSON fragment")
				}
				revision = page["revision"].(string)
			} else if revision != page["revision"] {
				t.Fatal("changed source under fragment")
			}
			buffer.WriteString(page["dataJson"].(string))
			if fragment["complete"] == true {
				var group []model.MeetingListItem
				if json.Unmarshal([]byte(buffer.String()), &group) != nil {
					t.Fatal("JSON group cannot restore")
				}
				for _, row := range group {
					rows[row.MeetingID] = row
				}
				buffer.Reset()
			}
		}
		more := page["page"].(map[string]interface{})
		if more["complete"] == true {
			break
		}
		if n > 900 {
			t.Fatal("never completes")
		}
		cursor := more["nextCursor"].(string)
		if len(cursor) > 2048 {
			t.Fatal("cursor bound")
		}
		q.Set("cursor", cursor)
	}
	return rows
}
func TestMeetingCatalogRestoresFullFieldsAcrossOwnedDirectAndTeamPaths(t *testing.T) {
	s, r := catalogMeetingFixture()
	id := MeetingCatalogIdentity{UserID: "reader"}
	rows := collectCatalogMeetings(t, s, r, id, url.Values{"limit": {"3"}, "pageSize": {"8000"}})
	if len(rows) != 45 {
		t.Fatalf("lost meetings: %d", len(rows))
	}
	for _, m := range r.meetings {
		got := rows[m.MeetingID]
		if got.Title != m.Title || strings.Join(got.Participants, "") != strings.Join(m.Participants, "") || strings.Join(got.Tags, "") != strings.Join(m.Tags, "") {
			t.Fatal("valid metadata was truncated")
		}
		if !utf8.ValidString(got.Summary) || utf8.RuneCountInString(got.Summary) != 203 {
			t.Fatal("summary split Unicode")
		}
	}
}
func TestMeetingCatalogFiltersClassifyNeverGrantAndStaleRefsCannotPublish(t *testing.T) {
	s, r := catalogMeetingFixture()
	r.meetings["author|team-01"].SharedToAccount = false
	r.meetings["author|team-02"].AccountID = "other"
	r.shares["reader|direct-01"].Origin = model.ShareOriginAccount
	r.meetings["author|direct-01"].SharedToAccount = false
	q := url.Values{"tab": {"shared"}, "accountIds": {"a"}}
	rows := collectCatalogMeetings(t, s, r, MeetingCatalogIdentity{UserID: "reader"}, q)
	if len(rows) != 21 || rows["team-01"].MeetingID != "" || rows["team-02"].MeetingID != "" || rows["direct-01"].MeetingID != "" {
		t.Fatalf("scope/publication failed: %d", len(rows))
	}
	delete(r.members, "a|reader")
	rows = collectCatalogMeetings(t, s, r, MeetingCatalogIdentity{UserID: "reader"}, url.Values{"tab": {"shared"}, "accountIds": {"a"}})
	if len(rows) != 11 {
		t.Fatalf("classification granted team access or denied direct grant: %d", len(rows))
	}
}
func TestMeetingCatalogTeamPathSurvivesMissingDirectIndex(t *testing.T) {
	s, r := catalogMeetingFixture()
	r.directIndex = map[string]bool{}
	r.shares["reader|team-01"] = &model.Share{MeetingID: "team-01", OwnerID: "author", SharedToID: "reader", Permission: "edit"}
	rows := collectCatalogMeetings(t, s, r, MeetingCatalogIdentity{UserID: "reader"}, url.Values{"tab": {"shared"}})
	if len(rows) != 12 || rows["team-01"].Permission == nil || *rows["team-01"].Permission != "edit" {
		t.Fatal("inferred earlier dedup omitted valid team path")
	}
}
func TestMeetingCatalogPinsAllCurrentGrantProofsSelectionAndBytes(t *testing.T) {
	for _, change := range []string{"selection", "limit", "title", "grant", "revoke"} {
		t.Run(change, func(t *testing.T) {
			s, r := catalogMeetingFixture()
			q := url.Values{"tab": {"shared"}, "pageSize": {"10"}}
			identity := MeetingCatalogIdentity{UserID: "reader"}
			page := readCatalogMeetingPage(t, s, identity, q)
			q.Set("cursor", page["page"].(map[string]interface{})["nextCursor"].(string))
			switch change {
			case "selection":
				q.Set("accountIds", "a")
			case "limit":
				q.Set("limit", "1")
			case "title":
				r.meetings["author|direct-00"].Title = "changed"
			case "grant":
				r.shares["reader|direct-00"].CreatedAt = time.Now()
			case "revoke":
				delete(r.shares, "reader|direct-00")
			}
			raw, err := s.Read(context.Background(), identity, q)
			if raw != nil || !errors.Is(err, ErrStaleReadingCursor) {
				t.Fatal("changed scope/source/grant accepted", err)
			}
		})
	}
}
func TestMeetingCatalogReadsNeverMaterializeInvitations(t *testing.T) {
	r := &catalogMeetingRepo{mockMeetingRepo: newMockMeetingRepo(), hideMembershipIndex: true}
	s := NewMeetingCatalogService(r)
	identity := MeetingCatalogIdentity{UserID: "reader", Email: "reader@example.invalid", EmailVerified: true}
	for i := 0; i < 8; i++ {
		a := fmt.Sprintf("a-%02d", i)
		m := fmt.Sprintf("m-%02d", i)
		r.members[a+"|owner"] = &model.AccountMember{AccountID: a, UserID: "owner", Role: model.RoleOwner}
		r.meetings["owner|"+m] = &model.Meeting{MeetingID: m, UserID: "owner", AccountID: a, SharedToAccount: true}
		r.meetingRefs[a] = []model.MeetingRef{{MeetingID: m, OwnerUserID: "owner"}}
		r.pendingShares = append(r.pendingShares, &model.PendingShare{SK: "PENDING_ACCOUNT#" + a, Email: identity.Email, AccountID: a, Kind: model.PendingShareKindAccount, Role: "viewer", InvitedByUserID: "owner", InvitedCognitoSub: "reader", TTL: time.Now().Add(time.Hour).Unix()})
	}
	rows := collectCatalogMeetings(t, s, r, identity, url.Values{})
	if len(rows) != 0 || len(r.pendingShares) != 8 || r.invitations != 0 {
		t.Fatal("read-only catalog processed invitations", len(rows), len(r.pendingShares), r.invitations)
	}
}

func TestMeetingCatalogProtectsDerivedTeamContentAndPreservesIndependentGrants(t *testing.T) {
	s, r := catalogMeetingFixture()
	r.members["a|reader"].SummaryPrivacyVersion = 1
	r.shares["reader|team-01"] = &model.Share{MeetingID: "team-01", OwnerID: "author", SharedToID: "reader", Permission: "edit"}
	r.shares["reader|team-02"] = &model.Share{MeetingID: "team-02", OwnerID: "author", SharedToID: "reader", Permission: "read", Origin: model.ShareOriginAccount}
	rows := collectCatalogMeetings(t, s, r, MeetingCatalogIdentity{UserID: "reader"}, url.Values{})
	for _, id := range []string{"team-00", "team-02"} {
		if rows[id].Summary != "" || len(rows[id].Tags) != 0 || rows[id].Sentiment != "" ||
			rows[id].SummaryReadRestriction != "SHARING_REGEN_REQUIRED" {
			t.Fatalf("derived content leaked through %s", id)
		}
	}
	for _, id := range []string{"owned-00", "direct-00", "team-01"} {
		if rows[id].Summary == "" || rows[id].SummaryReadRestriction != "" {
			t.Fatalf("independent grant lost derived content: %s", id)
		}
	}
	if r.meetings["author|team-00"].Content == "" || len(r.meetings["author|team-00"].Tags) == 0 {
		t.Fatal("read projection mutated stored meeting")
	}
}

func TestMeetingCatalogPrivacyMarkerAloneInvalidatesContinuation(t *testing.T) {
	s, r := catalogMeetingFixture()
	r.directIndex = map[string]bool{}
	q := url.Values{"tab": {"shared"}, "pageSize": {"10"}}
	identity := MeetingCatalogIdentity{UserID: "reader"}
	for n := 0; n < 10; n++ {
		page := readCatalogMeetingPage(t, s, identity, q)
		q.Set("cursor", page["page"].(map[string]interface{})["nextCursor"].(string))
		if page["phase"] == "team" {
			r.members["a|reader"].SummaryPrivacyVersion = 1
			raw, err := s.Read(context.Background(), identity, q)
			if raw != nil || !errors.Is(err, ErrStaleReadingCursor) {
				t.Fatal("privacy-only membership change replayed old team content", err)
			}
			return
		}
	}
	t.Fatal("team phase not reached")
}
func TestMeetingCatalogUnverifiedOrForeignPendingIdentityCannotGrant(t *testing.T) {
	for _, verified := range []bool{false, true} {
		r := &catalogMeetingRepo{mockMeetingRepo: newMockMeetingRepo()}
		r.members["a|owner"] = &model.AccountMember{AccountID: "a", UserID: "owner", Role: model.RoleOwner}
		r.pendingShares = []*model.PendingShare{{SK: "PENDING_ACCOUNT#a", Kind: model.PendingShareKindAccount, Email: "e@example.invalid", AccountID: "a", InvitedByUserID: "owner", InvitedCognitoSub: "foreign", TTL: time.Now().Add(time.Hour).Unix()}}
		s := NewMeetingCatalogService(r)
		collectCatalogMeetings(t, s, r, MeetingCatalogIdentity{UserID: "reader", Email: "e@example.invalid", EmailVerified: verified}, url.Values{})
		if r.members["a|reader"] != nil || len(r.pendingShares) != 1 {
			t.Fatal("wrong identity claimed invite")
		}
	}
}

func TestMeetingCatalogMaximumIdentifiersAndEscapingFitOpaqueCursor(t *testing.T) {
	r := &catalogMeetingRepo{mockMeetingRepo: newMockMeetingRepo()}
	s := NewMeetingCatalogService(r)
	account, owner, id := strings.Repeat("a", 128), strings.Repeat("o", 128), strings.Repeat("m", 128)
	r.members[account+"|reader"] = &model.AccountMember{AccountID: account, UserID: "reader", Role: "viewer"}
	r.meetings[owner+"|"+id] = &model.Meeting{MeetingID: id, UserID: owner, AccountID: account, SharedToAccount: true, Title: strings.Repeat("한😀\"\\", 12000)}
	r.meetingRefs[account] = []model.MeetingRef{{MeetingID: id, OwnerUserID: owner}}
	rows := collectCatalogMeetings(t, s, r, MeetingCatalogIdentity{UserID: "reader"}, url.Values{"tab": {"shared"}, "accountIds": {account}})
	if len(rows) != 1 || rows[id].Title != r.meetings[owner+"|"+id].Title {
		t.Fatal("max identifier source omitted")
	}
}
