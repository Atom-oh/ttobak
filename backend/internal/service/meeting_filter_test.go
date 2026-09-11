package service

import (
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

// Use real cursor shapes at the repository boundary; the older general-purpose
// mock uses numeric offsets and unpaginated shares for legacy tests.
type filterMeetingRepo struct{ *mockMeetingRepo }

func rawFilterCursor(key map[string]string) string {
	b, _ := json.Marshal(key)
	return base64.StdEncoding.EncodeToString(b)
}

func filterRegularCursor(user, tab string, offset int) string {
	key := map[string]string{"PK": "USER#" + user, "SK": "SHARED#" + strconv.Itoa(offset)}
	if tab != "shared" {
		key["SK"] = "MEETING#" + strconv.Itoa(offset)
		key["GSI1PK"] = key["PK"]
		key["GSI1SK"] = "2026-09-11"
	}
	return rawFilterCursor(key)
}

func filterOffset(cursor string) int {
	b, _ := base64.StdEncoding.DecodeString(cursor)
	var key map[string]string
	_ = json.Unmarshal(b, &key)
	var offset int
	for i := len(key["SK"]) - 1; i >= 0; i-- {
		if key["SK"][i] == '#' {
			offset, _ = strconv.Atoi(key["SK"][i+1:])
			break
		}
	}
	return offset
}

func (r *filterMeetingRepo) ListMeetings(ctx context.Context, p repository.ListMeetingsParams) (*repository.ListMeetingsResult, error) {
	if r.listMeetingsFn != nil {
		return r.listMeetingsFn(p)
	}
	result := &repository.ListMeetingsResult{}
	if p.Tab == "shared" {
		for _, sh := range r.shares {
			if sh.SharedToID == p.UserID {
				result.Shares = append(result.Shares, *sh)
			}
		}
		slices.SortFunc(result.Shares, func(a, b model.Share) int {
			return cmp.Compare(a.MeetingID, b.MeetingID)
		})
		start := filterOffset(p.Cursor)
		end := min(start+int(p.Limit), len(result.Shares))
		if end < len(result.Shares) {
			result.NextCursor = aws.String(filterRegularCursor(p.UserID, p.Tab, end))
		}
		result.Shares = result.Shares[start:end]
	} else {
		for _, m := range r.meetings {
			// Deliberately leave account filtering to the real service here.
			if m.UserID == p.UserID {
				result.Meetings = append(result.Meetings, *m)
			}
		}
		slices.SortFunc(result.Meetings, func(a, b model.Meeting) int { return b.Date.Compare(a.Date) })
		start := filterOffset(p.Cursor)
		end := min(start+int(p.Limit), len(result.Meetings))
		if end < len(result.Meetings) {
			result.NextCursor = aws.String(filterRegularCursor(p.UserID, p.Tab, end))
		}
		result.Meetings = result.Meetings[start:end]
	}
	return result, nil
}

func (r *filterMeetingRepo) ListMeetingRefsForAccountPage(ctx context.Context, id, cursor string, limit int32) ([]model.MeetingRef, *string, error) {
	refs, next, err := r.mockMeetingRepo.ListMeetingRefsForAccountPage(ctx, id, strconv.Itoa(filterOffset(cursor)), limit)
	if next != nil {
		next = aws.String(rawFilterCursor(map[string]string{"PK": "ACCOUNT#" + id, "SK": "MEETINGREF#2026-09-11#" + *next}))
	}
	return refs, next, err
}

func filterFixture() (*filterMeetingRepo, *MeetingService) {
	r := &filterMeetingRepo{newMockMeetingRepo()}
	s := newMeetingServiceWithRepo(r)
	for i, id := range []string{"acc-a", "acc-b", "acc-c"} {
		r.addMember(id, "viewer", model.RoleSA)
		for j, stream := range []string{"own", "direct", "team", "private"} {
			owner := "owner"
			if stream == "own" {
				owner = "viewer"
			}
			m := &model.Meeting{
				MeetingID: stream + "-" + id, UserID: owner, AccountID: id,
				Date: time.Date(2026, 9, 11-i-j, 0, 0, 0, 0, time.UTC), SharedToAccount: stream != "private",
			}
			r.addMeeting(m)
			r.meetingRefs[id] = append(r.meetingRefs[id], model.MeetingRef{AccountID: id, MeetingID: m.MeetingID, OwnerUserID: owner, Date: m.Date})
			if stream == "direct" {
				r.shares[shareKey("viewer", m.MeetingID)] = &model.Share{
					MeetingID: m.MeetingID, OwnerID: owner, SharedToID: "viewer", Permission: model.PermissionEdit,
				}
			}
		}
	}
	return r, s
}

func TestListMeetingsForAccounts_UnionAccessAndPagination(t *testing.T) {
	for _, tab := range []string{"all", "shared"} {
		for _, limit := range []int32{1, 2, 20} {
			t.Run(fmt.Sprintf("%s/%d", tab, limit), func(t *testing.T) {
				r, s := filterFixture()
				// A direct share remains valid without membership. Team access
				// cannot inherit from any other selected account.
				delete(r.members, "acc-b|viewer")
				seen := map[string]bool{}
				cursor := ""
				var ownedDates []string
				for page := 0; ; page++ {
					if page > 20 {
						t.Fatal("pagination did not terminate")
					}
					got, err := s.ListMeetingsForAccounts(context.Background(), "viewer", tab, cursor, []string{"acc-b", "acc-a", "acc-a"}, limit)
					if err != nil {
						t.Fatal(err)
					}
					if len(got.Meetings) > int(limit) {
						t.Fatalf("page exceeds limit: %+v", got)
					}
					for _, m := range got.Meetings {
						if seen[m.MeetingID] {
							t.Fatalf("duplicate across streams/pages: %s", m.MeetingID)
						}
						seen[m.MeetingID] = true
						if !m.IsShared {
							ownedDates = append(ownedDates, m.Date)
						}
						if m.MeetingID == "direct-acc-b" && aws.ToString(m.Permission) != model.PermissionEdit {
							t.Fatal("direct permission was downgraded")
						}
					}
					if got.NextCursor == nil {
						break
					}
					cursor = *got.NextCursor
				}
				want := []string{"direct-acc-a", "direct-acc-b", "team-acc-a"}
				if tab == "all" {
					want = append(want, "own-acc-a", "own-acc-b")
				}
				if len(seen) != len(want) {
					t.Fatalf("meeting union = %v, want %v", seen, want)
				}
				for _, id := range want {
					if !seen[id] {
						t.Fatalf("missing %s: %v", id, seen)
					}
				}
				if !slices.IsSortedFunc(ownedDates, func(a, b string) int { return cmp.Compare(b, a) }) {
					t.Fatal("owned chronology changed")
				}
			})
		}
	}
}

func TestListMeetingsForAccounts_CursorContextAndEmptyPages(t *testing.T) {
	for _, tab := range []string{"all", "shared"} {
		t.Run(tab, func(t *testing.T) {
			r, s := filterFixture()
			calls := 0
			r.listMeetingsFn = func(p repository.ListMeetingsParams) (*repository.ListMeetingsResult, error) {
				calls++
				return &repository.ListMeetingsResult{NextCursor: aws.String(filterRegularCursor(p.UserID, p.Tab, calls))}, nil
			}
			first, err := s.ListMeetingsForAccounts(context.Background(), "viewer", tab, "", []string{"acc-b", "acc-a"}, 1)
			if err != nil || first.NextCursor == nil || len(first.Meetings) != 0 || calls > 25 {
				t.Fatalf("bounded empty continuation: %+v, calls=%d, err=%v", first, calls, err)
			}
			for _, tt := range []struct {
				user, tab string
				ids       []string
			}{
				{"other", tab, []string{"acc-a", "acc-b"}},
				{"viewer", map[string]string{"all": "shared", "shared": "all"}[tab], []string{"acc-a", "acc-b"}},
				{"viewer", tab, []string{"acc-a"}},
				{"viewer", tab, nil},
			} {
				before := calls
				_, err := s.ListMeetingsForAccounts(context.Background(), tt.user, tt.tab, *first.NextCursor, tt.ids, 1)
				if !errors.Is(err, ErrInvalidInput) || calls != before {
					t.Fatalf("context mismatch reached storage: %+v error=%v", tt, err)
				}
			}
			if _, err := s.ListMeetingsForAccounts(context.Background(), "viewer", tab, *first.NextCursor, []string{"acc-a", "acc-b", "acc-a"}, 1); err != nil {
				t.Fatalf("equivalent selection invalidated cursor: %v", err)
			}
		})
	}
}

func TestListMeetingsForAccounts_FirstLoginHintsSurviveRegularPages(t *testing.T) {
	r, s := filterFixture()
	r.membershipIndexLag = true
	cursor := ""
	seenTeam := false
	for page := 0; ; page++ {
		if page > 20 {
			t.Fatal("pagination did not terminate")
		}
		hints := []string(nil)
		if page == 0 {
			hints = []string{"acc-a"}
		}
		got, err := s.ListMeetingsForAccounts(context.Background(), "viewer", "all", cursor, nil, 1, hints...)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range got.Meetings {
			seenTeam = seenTeam || m.MeetingID == "team-acc-a"
		}
		if got.NextCursor == nil {
			break
		}
		cursor = *got.NextCursor
	}
	if !seenTeam {
		t.Fatal("newly materialized membership was lost while GSI lagged")
	}
}

func TestListMeetingsForAccounts_TeamEmptyCursorAndRevocation(t *testing.T) {
	r, s := filterFixture()
	r.listMeetingsFn = func(repository.ListMeetingsParams) (*repository.ListMeetingsResult, error) {
		return &repository.ListMeetingsResult{}, nil
	}
	r.meetingRefs["acc-a"] = nil
	for i := 0; i < 25; i++ {
		r.meetingRefs["acc-a"] = append(r.meetingRefs["acc-a"], model.MeetingRef{MeetingID: fmt.Sprintf("deleted-%d", i), OwnerUserID: "owner"})
	}
	r.meetingRefs["acc-a"] = append(r.meetingRefs["acc-a"], model.MeetingRef{MeetingID: "team-acc-a", OwnerUserID: "owner"})
	first, err := s.ListMeetingsForAccounts(context.Background(), "viewer", "shared", "", []string{"acc-a", "acc-b"}, 1)
	if err != nil || first.NextCursor == nil || len(first.Meetings) != 0 || r.teamRefPageCalls != 25 {
		t.Fatalf("empty team page lost continuation/work bound: %+v, err=%v, calls=%d", first, err, r.teamRefPageCalls)
	}
	before := r.teamRefPageCalls
	for _, tt := range []struct {
		user, tab string
		ids       []string
	}{
		{"other", "shared", []string{"acc-a", "acc-b"}},
		{"viewer", "all", []string{"acc-a", "acc-b"}},
		{"viewer", "shared", []string{"acc-a"}},
	} {
		_, err := s.ListMeetingsForAccounts(context.Background(), tt.user, tt.tab, *first.NextCursor, tt.ids, 1)
		if !errors.Is(err, ErrInvalidInput) || r.teamRefPageCalls != before {
			t.Fatalf("team cursor context mismatch reached storage: %v", err)
		}
	}
	next, err := s.ListMeetingsForAccounts(context.Background(), "viewer", "shared", *first.NextCursor, []string{"acc-b", "acc-a", "acc-b"}, 1)
	if err != nil || len(next.Meetings) != 1 || next.Meetings[0].MeetingID != "team-acc-a" || next.NextCursor == nil {
		t.Fatalf("selected team continuation lost: %+v, err=%v", next, err)
	}
	delete(r.members, "acc-b|viewer")
	before = r.teamRefPageCalls
	last, err := s.ListMeetingsForAccounts(context.Background(), "viewer", "shared", *next.NextCursor, []string{"acc-a", "acc-b"}, 1)
	if err != nil || len(last.Meetings) != 0 || last.NextCursor != nil || r.teamRefPageCalls != before {
		t.Fatalf("revoked membership was trusted from cursor: %+v, err=%v", last, err)
	}
}

func TestListMeetingsForAccounts_CursorCandidatesNeverGrantAccess(t *testing.T) {
	for _, tt := range []struct {
		name, candidate string
		selected        []string
	}{
		{"member of unselected account", "acc-c", []string{"acc-a", "acc-b"}},
		{"selected account without membership", "acc-b", []string{"acc-a", "acc-b"}},
		{"unfiltered candidate without membership", "acc-b", []string{}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r, s := filterFixture()
			delete(r.members, "acc-b|viewer")
			team, _ := encodeTeamMeetingCursor(teamMeetingCursor{
				UserID: "viewer", Tab: "shared", Selected: tt.selected, Accounts: []string{tt.candidate},
			})
			outer, _ := encodeMeetingFilterCursor(meetingFilterCursor{
				UserID: "viewer", Tab: "shared", AccountIDs: tt.selected, Stage: "team", Team: *team,
			})
			got, err := s.ListMeetingsForAccounts(context.Background(), "viewer", "shared", *outer, tt.selected, 20)
			if err != nil || len(got.Meetings) != 0 || r.teamRefPageCalls != 0 {
				t.Fatalf("candidate granted meeting access: %+v, err=%v, reads=%d", got, err, r.teamRefPageCalls)
			}
		})
	}
}

func TestListMeetingsForAccounts_RejectsMalformedCursorState(t *testing.T) {
	for _, tt := range []struct {
		name   string
		mutate func(*meetingFilterCursor)
	}{
		{"raw inner cursor", func(c *meetingFilterCursor) { c.Regular = "bad" }},
		{"foreign raw cursor", func(c *meetingFilterCursor) { c.Regular = filterRegularCursor("other", "shared", 2) }},
		{"wrong raw index", func(c *meetingFilterCursor) { c.Regular = filterRegularCursor("viewer", "all", 2) }},
		{"unknown stage", func(c *meetingFilterCursor) { c.Stage = "unknown" }},
		{"missing selected", func(c *meetingFilterCursor) { c.AccountIDs = nil }},
		{"invalid hint", func(c *meetingFilterCursor) { c.Joined = []string{"../acc-a"} }},
		{"mixed streams", func(c *meetingFilterCursor) { c.Team = "team:anything" }},
		{"missing team state", func(c *meetingFilterCursor) { c.Stage = "team" }},
		{"foreign ref partition", func(c *meetingFilterCursor) {
			team, _ := encodeTeamMeetingCursor(teamMeetingCursor{
				UserID: "viewer", Tab: "shared", Selected: []string{"acc-a"}, Accounts: []string{"acc-a"},
				RefCursor: rawFilterCursor(map[string]string{"PK": "ACCOUNT#acc-b", "SK": "MEETINGREF#2026-09-11#x"}),
			})
			c.Stage, c.Team = "team", *team
		}},
		{"malformed ref cursor", func(c *meetingFilterCursor) {
			team, _ := encodeTeamMeetingCursor(teamMeetingCursor{
				UserID: "viewer", Tab: "shared", Selected: []string{"acc-a"}, Accounts: []string{"acc-a"}, RefCursor: "garbage",
			})
			c.Stage, c.Team = "team", *team
		}},
		{"inner filter mismatch", func(c *meetingFilterCursor) {
			team, _ := encodeTeamMeetingCursor(teamMeetingCursor{
				UserID: "viewer", Tab: "shared", Selected: []string{"acc-b"}, Accounts: []string{"acc-b"},
			})
			c.Stage, c.Team = "team", *team
		}},
		{"invalid team index", func(c *meetingFilterCursor) {
			team, _ := encodeTeamMeetingCursor(teamMeetingCursor{
				UserID: "viewer", Tab: "shared", Selected: []string{"acc-a"}, Accounts: []string{"acc-a"}, Index: -1,
			})
			c.Stage, c.Team = "team", *team
		}},
		{"duplicate team candidates", func(c *meetingFilterCursor) {
			team, _ := encodeTeamMeetingCursor(teamMeetingCursor{
				UserID: "viewer", Tab: "shared", Selected: []string{"acc-a"}, Accounts: []string{"acc-a", "acc-a"},
			})
			c.Stage, c.Team = "team", *team
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r, s := filterFixture()
			r.listMeetingsFn = func(repository.ListMeetingsParams) (*repository.ListMeetingsResult, error) {
				t.Fatal("malformed cursor reached repository")
				return nil, nil
			}
			c := meetingFilterCursor{UserID: "viewer", Tab: "shared", AccountIDs: []string{"acc-a"}, Stage: "shared"}
			tt.mutate(&c)
			cursor, _ := encodeMeetingFilterCursor(c)
			_, err := s.ListMeetingsForAccounts(context.Background(), "viewer", "shared", *cursor, []string{"acc-a"}, 20)
			if !errors.Is(err, ErrInvalidInput) || r.teamRefPageCalls != 0 {
				t.Fatalf("invalid cursor not rejected: %v", err)
			}
		})
	}
}

func TestListMeetingsForAccounts_FilterValidationAndLegacyIsolation(t *testing.T) {
	r, s := filterFixture()
	r.listMeetingsFn = func(repository.ListMeetingsParams) (*repository.ListMeetingsResult, error) {
		t.Fatal("invalid filter reached repository")
		return nil, nil
	}
	excess := make([]string, 101)
	for i := range excess {
		excess[i] = fmt.Sprintf("a-%d", i)
	}
	for _, ids := range [][]string{{""}, {"a", ".."}, {strings.Repeat("a", 129)}, excess} {
		if _, err := s.ListMeetingsForAccounts(context.Background(), "viewer", "all", "", ids, 1); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("invalid selection accepted: %v", err)
		}
	}
	c, _ := encodeMeetingFilterCursor(meetingFilterCursor{UserID: "viewer", Tab: "all", AccountIDs: []string{"acc-a"}, Stage: "owned"})
	if _, err := s.ListMeetings(context.Background(), "viewer", "all", *c, "acc-a", 20); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("multi cursor must not enter legacy path: %v", err)
	}
}

func TestListMeetingsForAccounts_AccountSharesUseCanonicalAccountAndLiveMembership(t *testing.T) {
	r, s := filterFixture()
	for _, tt := range []struct {
		id, account, shareAccount string
		published, member         bool
	}{
		{"stale-filter", "acc-c", "acc-a", true, true},
		{"revoked", "acc-b", "acc-b", true, false},
		{"unpublished", "acc-a", "acc-a", false, true},
		{"valid", "acc-a", "acc-c", true, true},
	} {
		r.addMeeting(&model.Meeting{MeetingID: tt.id, UserID: "owner", AccountID: tt.account, SharedToAccount: tt.published})
		r.shares[shareKey("viewer", tt.id)] = &model.Share{
			MeetingID: tt.id, OwnerID: "owner", SharedToID: "viewer", Origin: model.ShareOriginAccount,
			AccountID: tt.shareAccount, Permission: model.PermissionRead,
		}
		if !tt.member {
			delete(r.members, tt.account+"|viewer")
		}
	}
	got, err := s.ListMeetingsForAccounts(context.Background(), "viewer", "shared", "", []string{"acc-a", "acc-b"}, 20)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, m := range got.Meetings {
		ids = append(ids, m.MeetingID)
	}
	slices.Sort(ids)
	if !slices.Equal(ids, []string{"direct-acc-a", "direct-acc-b", "team-acc-a", "valid"}) {
		t.Fatalf("canonical filter/membership failed: %v", ids)
	}
}

func TestListMeetingsForAccounts_HundredIDsHaveCompactTeamContinuation(t *testing.T) {
	r := &filterMeetingRepo{newMockMeetingRepo()}
	s := newMeetingServiceWithRepo(r)
	ids := make([]string, 100)
	for i := range ids {
		ids[i] = fmt.Sprintf("00000000-0000-0000-0000-%012d", i)
	}
	r.addMember(ids[0], "viewer", model.RoleSA)
	for i := 0; i < 2; i++ {
		id := fmt.Sprintf("team-%d", i)
		r.addMeeting(&model.Meeting{MeetingID: id, UserID: "owner", AccountID: ids[0], SharedToAccount: true})
		r.meetingRefs[ids[0]] = append(r.meetingRefs[ids[0]], model.MeetingRef{MeetingID: id, OwnerUserID: "owner"})
	}
	first, err := s.ListMeetingsForAccounts(context.Background(), "viewer", "all", "", ids, 1)
	if err != nil || first.NextCursor == nil || len(first.Meetings) != 1 {
		t.Fatalf("team page missing: %+v, err=%v", first, err)
	}
	requestURL := "/api/meetings?accountIds=" + strings.Join(ids, ",") + "&cursor=" + url.QueryEscape(*first.NextCursor)
	if len(requestURL) > 8192 {
		t.Fatalf("100 selected UUIDs cannot paginate within an 8KB URL: %d bytes", len(requestURL))
	}
	slices.Reverse(ids)
	next, err := s.ListMeetingsForAccounts(context.Background(), "viewer", "all", *first.NextCursor, ids, 1)
	if err != nil || len(next.Meetings) != 1 || next.Meetings[0].MeetingID != "team-1" {
		t.Fatalf("compact continuation lost its position or selection: %+v, err=%v", next, err)
	}
}
