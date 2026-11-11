package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

const (
	maxPeoplePerPage        = 200
	maxMentionGroupsPerPage = 100
	maxPeopleRowsPerPage    = 2000
	maxPeopleMeetingReads   = 300
	maxPeopleCursorBytes    = 1024
	peopleCursorMention     = "mention"
)

// peopleListKinds is the ListPeople order: members, then contacts.
var peopleListKinds = []string{model.LinkKindMember, model.LinkKindContact}

// peopleCursor resumes a people or mention page strictly after S, the last
// sort key of the last complete person or group, within kind K.
type peopleCursor struct {
	A string `json:"a"`
	K string `json:"k"`
	S string `json:"s,omitempty"`
}

func encodePeopleCursor(accountID, kind, sk string) string {
	b, _ := json.Marshal(peopleCursor{A: accountID, K: kind, S: sk})
	return base64.RawURLEncoding.EncodeToString(b)
}

// decodePeopleCursor accepts a cursor of this account whose kind is one of
// kinds and whose sort key, when set, is in that kind's range.
func decodePeopleCursor(accountID, cursor string, kinds ...string) (peopleCursor, error) {
	if cursor == "" {
		return peopleCursor{K: kinds[0]}, nil
	}
	if len(cursor) > maxPeopleCursorBytes {
		return peopleCursor{}, ErrInvalidInput
	}
	b, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return peopleCursor{}, ErrInvalidInput
	}
	var c peopleCursor
	if json.Unmarshal(b, &c) != nil || c.A != accountID || !slices.Contains(kinds, c.K) {
		return peopleCursor{}, ErrInvalidInput
	}
	prefix := model.PrefixAccountLinkTo + c.K + "#"
	if c.K == peopleCursorMention {
		prefix = model.PrefixAccountMention
	}
	if c.S != "" && !strings.HasPrefix(c.S, prefix) {
		return peopleCursor{}, ErrInvalidInput
	}
	return c, nil
}

// peopleViewer holds one request's authorization and bounded, cached reads.
type peopleViewer struct {
	s         *AccountPeopleService
	accountID string
	userID    string
	member    *model.AccountMember

	meetings map[string]*model.Meeting
	// focusMeetings caches focus-state reads (a superset of publication).
	focusMeetings map[string]*model.Meeting
	reads         int
	truncated     bool

	members map[string]*model.AccountMember
	labels  map[string]string
}

// viewer requires a direct member of an existing account, on every request.
func (s *AccountPeopleService) viewer(ctx context.Context, accountID, userID string) (*peopleViewer, error) {
	if !validLinkPlainID(accountID) || !validLinkPlainID(userID) {
		return nil, ErrInvalidInput
	}
	account, err := s.repo.GetAccount(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if !canonicalReadAccount(account, accountID) {
		return nil, ErrForbidden
	}
	member, err := s.repo.GetMember(ctx, accountID, userID)
	if err != nil {
		return nil, err
	}
	if !canonicalReadMember(member, accountID, userID) {
		return nil, ErrForbidden
	}
	return &peopleViewer{s: s, accountID: accountID, userID: userID, member: member,
		meetings: map[string]*model.Meeting{}, labels: map[string]string{}}, nil
}

// meeting returns the meeting when it is still published to the account, or
// nil. known is false once the read bound is spent; that sets truncated.
func (v *peopleViewer) meeting(ctx context.Context, linkID string) (*model.Meeting, bool, error) {
	if m, ok := v.meetings[linkID]; ok {
		return m, true, nil
	}
	owner, id, ok := splitMeetingLinkID(linkID)
	if !ok {
		return nil, true, nil
	}
	if v.reads >= maxPeopleMeetingReads {
		v.truncated = true
		return nil, false, nil
	}
	v.reads++
	m, err := v.s.repo.GetMeetingPublication(ctx, owner, id)
	if err != nil {
		return nil, false, err
	}
	if !meetingPublishedToAccount(m, v.accountID, owner, id) {
		m = nil
	}
	v.meetings[linkID] = m
	return m, true, nil
}

// guarded reports the project 1 evidence rule: summary-derived text of this
// meeting is hidden from the viewer.
func (v *peopleViewer) guarded(m *model.Meeting) bool {
	return v.userID != m.UserID && accountMemberNeedsSummaryGuard(v.member) && !m.AccountSummaryMetadataShareable()
}

func summaryDerivedPeopleSource(field string) bool {
	return field == model.PeopleSourceActionItems || field == model.PeopleSourceInsights
}

// label returns the current label of a target the viewer may read; ok is
// false for former members, unreadable contacts, accounts without a read
// grant and projects that no longer list the account.
func (v *peopleViewer) label(ctx context.Context, kind, id string) (string, bool, error) {
	key := kind + ":" + id // "" caches an unreadable target
	label, ok := v.labels[key]
	if !ok {
		var err error
		if label, err = v.loadLabel(ctx, kind, id); err != nil {
			return "", false, err
		}
		v.labels[key] = label
	}
	return label, label != "", nil
}

func (v *peopleViewer) loadLabel(ctx context.Context, kind, id string) (string, error) {
	repo := v.s.repo
	switch kind {
	case model.LinkKindMember:
		if v.members == nil {
			members, err := repo.ListAccountMembers(ctx, v.accountID)
			if err != nil {
				return "", err
			}
			v.members = map[string]*model.AccountMember{}
			for i := range members {
				if m := &members[i]; canonicalReadMember(m, v.accountID, m.UserID) {
					v.members[m.UserID] = m
				}
			}
		}
		m := v.members[id]
		if m == nil {
			return "", nil
		}
		user, err := repo.GetUserByID(ctx, id)
		if err != nil {
			return "", err
		}
		if user != nil && user.UserID == id && strings.TrimSpace(user.Name) != "" {
			return user.Name, nil
		}
		return m.Email, nil
	case model.LinkKindContact:
		c, err := repo.GetAccountContact(ctx, v.accountID, id)
		if errors.Is(err, repository.ErrContactUnreadable) {
			return "", nil
		}
		if err != nil {
			return "", err
		}
		if c == nil || c.ContactID != id {
			return "", nil
		}
		return strings.TrimSpace(c.Name), nil
	case model.LinkKindAccount:
		grant, err := resolveAccountReadGrant(ctx, repo, id, v.userID)
		if err != nil || grant == nil {
			return "", err
		}
		account, err := repo.GetAccount(ctx, id)
		if err != nil || !canonicalReadAccount(account, id) {
			return "", err
		}
		return account.Name, nil
	case model.LinkKindProject:
		p, err := repo.GetProject(ctx, id)
		if err != nil || p == nil || p.ProjectID != id || !contains(p.AccountIDs, v.accountID) {
			return "", err
		}
		return p.Name, nil
	}
	return "", nil
}

type peopleGroup struct {
	id     string
	links  []model.PeopleLink
	lastSK string
}

// groupPeopleLinks splits sorted reverse rows into per-target groups.
func groupPeopleLinks(links []model.PeopleLink) []peopleGroup {
	var groups []peopleGroup
	for _, l := range links {
		if n := len(groups); n == 0 || groups[n-1].id != l.ToID {
			groups = append(groups, peopleGroup{id: l.ToID})
		}
		g := &groups[len(groups)-1]
		g.links = append(g.links, l)
		g.lastSK = model.PeopleLinkToSortKey(l.Key())
	}
	return groups
}

// completePeopleGroups drops the last group when the row bound may have cut
// it. A single group that fills the bound is kept and reported as cut; last
// moves its resume key past the target's whole range.
func completePeopleGroups[T any](groups []T, more bool, last func(*T)) ([]T, bool) {
	if !more || len(groups) == 0 {
		return groups, false
	}
	if len(groups) > 1 {
		return groups[:len(groups)-1], false
	}
	last(&groups[0])
	return groups, true
}

// ListPeople returns one page of members and contacts with counts over the
// meetings still published to the account. A person is never split across
// pages unless it alone exceeds the row bound.
func (s *AccountPeopleService) ListPeople(ctx context.Context, userID, accountID, cursor string) (*model.AccountPeoplePage, error) {
	v, err := s.viewer(ctx, accountID, userID)
	if err != nil {
		return nil, err
	}
	cur, err := decodePeopleCursor(accountID, cursor, peopleListKinds...)
	if err != nil {
		return nil, err
	}
	page := &model.AccountPeoplePage{People: []model.AccountPerson{}}
	budget := maxPeopleRowsPerPage
	start := slices.Index(peopleListKinds, cur.K)
	for ki := start; ki < len(peopleListKinds); ki++ {
		kind := peopleListKinds[ki]
		after := ""
		if ki == start {
			after = cur.S
		}
		if budget <= 0 {
			page.NextCursor = encodePeopleCursor(accountID, kind, after)
			break
		}
		links, next, err := s.repo.ListPeopleLinksByTarget(ctx, accountID, kind, after, budget)
		if err != nil {
			return nil, err
		}
		budget -= max(len(links), 1)
		groups, cut := completePeopleGroups(groupPeopleLinks(links), next != "", func(g *peopleGroup) {
			g.lastSK = model.PrefixAccountLinkTo + kind + "#" + g.id + "#" + model.LinkKindMeeting + "$"
		})
		page.Truncated = page.Truncated || cut
		resume := next
		for i, g := range groups {
			person, ok, err := v.person(ctx, kind, g)
			if err != nil {
				return nil, err
			}
			resume = g.lastSK
			if ok {
				page.People = append(page.People, person)
			}
			if len(page.People) == maxPeoplePerPage && (i < len(groups)-1 || next != "" || ki < len(peopleListKinds)-1) {
				page.NextCursor = encodePeopleCursor(accountID, kind, resume)
				page.Truncated = page.Truncated || v.truncated
				return page, nil
			}
		}
		if next != "" {
			page.NextCursor = encodePeopleCursor(accountID, kind, resume)
			break
		}
	}
	page.Truncated = page.Truncated || v.truncated
	return page, nil
}

// person counts one target's visible edges; ok is false when none is visible
// or the target is no longer readable.
func (v *peopleViewer) person(ctx context.Context, kind string, g peopleGroup) (model.AccountPerson, bool, error) {
	p := model.AccountPerson{Kind: kind, ID: g.id, Roles: map[string]int{}}
	seen := map[string]bool{}
	for _, l := range g.links {
		m, known, err := v.meeting(ctx, l.MeetingID)
		if err != nil {
			return p, false, err
		}
		if !known || m == nil {
			continue
		}
		p.Roles[l.Role]++
		seen[l.MeetingID] = true
	}
	if len(seen) == 0 {
		return p, false, nil
	}
	label, ok, err := v.label(ctx, kind, g.id)
	if err != nil || !ok {
		return p, false, err
	}
	p.Label, p.Meetings = label, len(seen)
	return p, true, nil
}

type mentionGroupRows struct {
	hash   string
	rows   []model.UnresolvedMention
	lastSK string
}

// visibleMention: the meeting is still published and, for summary-derived
// rows, the evidence guard passes.
func (v *peopleViewer) visibleMention(ctx context.Context, row model.UnresolvedMention) (bool, error) {
	m, known, err := v.meeting(ctx, row.MeetingID)
	if err != nil || !known || m == nil {
		return false, err
	}
	return !summaryDerivedPeopleSource(row.SourceField) || !v.guarded(m), nil
}

// ListMentionGroups returns one page of unresolved mention groups, each
// counted over its visible rows. Ignored and fully hidden groups are omitted.
func (s *AccountPeopleService) ListMentionGroups(ctx context.Context, userID, accountID, cursor string) (*model.MentionGroupsPage, error) {
	v, err := s.viewer(ctx, accountID, userID)
	if err != nil {
		return nil, err
	}
	cur, err := decodePeopleCursor(accountID, cursor, peopleCursorMention)
	if err != nil {
		return nil, err
	}
	rows, next, err := s.repo.ListUnresolvedMentions(ctx, accountID, cur.S, maxPeopleRowsPerPage)
	if err != nil {
		return nil, err
	}
	var groups []mentionGroupRows
	for _, r := range rows {
		if n := len(groups); n == 0 || groups[n-1].hash != r.Hash {
			groups = append(groups, mentionGroupRows{hash: r.Hash})
		}
		g := &groups[len(groups)-1]
		g.rows = append(g.rows, r)
		g.lastSK = model.MentionSortKey(r.Hash, r.MeetingID, r.Role)
	}
	groups, cut := completePeopleGroups(groups, next != "", func(g *mentionGroupRows) {
		g.lastSK = model.PrefixAccountMention + g.hash + "$"
	})
	hashes := make([]string, 0, len(groups))
	for _, g := range groups {
		hashes = append(hashes, g.hash)
	}
	ignored := map[string]bool{}
	if len(hashes) > 0 {
		if ignored, err = s.repo.GetMentionIgnores(ctx, accountID, hashes); err != nil {
			return nil, err
		}
	}
	page := &model.MentionGroupsPage{Groups: []model.MentionGroup{}, Truncated: cut}
	resume := next
	for i, g := range groups {
		resume = g.lastSK
		if !ignored[g.hash] {
			group, ok, err := v.mentionGroup(ctx, g)
			if err != nil {
				return nil, err
			}
			if ok {
				page.Groups = append(page.Groups, group)
			}
		}
		if len(page.Groups) == maxMentionGroupsPerPage && (i < len(groups)-1 || next != "") {
			page.NextCursor = encodePeopleCursor(accountID, peopleCursorMention, resume)
			page.Truncated = page.Truncated || v.truncated
			return page, nil
		}
	}
	if next != "" {
		page.NextCursor = encodePeopleCursor(accountID, peopleCursorMention, resume)
	}
	page.Truncated = page.Truncated || v.truncated
	return page, nil
}

func (v *peopleViewer) mentionGroup(ctx context.Context, g mentionGroupRows) (model.MentionGroup, bool, error) {
	out := model.MentionGroup{Key: g.hash, Roles: []string{}}
	meetings := map[string]bool{}
	for _, r := range g.rows {
		ok, err := v.visibleMention(ctx, r)
		if err != nil {
			return out, false, err
		}
		if !ok {
			continue
		}
		if out.Text == "" {
			out.Text = r.Mention
		}
		meetings[r.MeetingID] = true
		if !slices.Contains(out.Roles, r.Role) {
			out.Roles = append(out.Roles, r.Role)
		}
	}
	slices.Sort(out.Roles)
	out.Meetings = len(meetings)
	return out, len(meetings) > 0, nil
}

// MeetingEdges returns the people edges of one published meeting with the
// labels of the targets the viewer can read; edges to other targets are
// omitted. Summary-derived text is blanked under the evidence guard.
func (s *AccountPeopleService) MeetingEdges(ctx context.Context, userID, accountID, meetingID string) (*model.MeetingPeopleEdges, error) {
	if _, _, ok := splitMeetingLinkID(meetingID); !ok {
		return nil, ErrInvalidInput
	}
	v, err := s.viewer(ctx, accountID, userID)
	if err != nil {
		return nil, err
	}
	m, _, err := v.meeting(ctx, meetingID)
	if err != nil {
		return nil, err
	}
	if m == nil {
		return nil, ErrNotFound
	}
	links, _, err := s.repo.ListPeopleLinksForMeeting(ctx, accountID, meetingID, maxPeopleExistingEdges)
	if err != nil {
		return nil, err
	}
	out := &model.MeetingPeopleEdges{Edges: []model.PeopleLink{}, Labels: map[string]string{}}
	guarded := v.guarded(m)
	for _, l := range links {
		label, ok, err := v.label(ctx, l.ToKind, l.ToID)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		if guarded && summaryDerivedPeopleSource(l.SourceField) {
			l.Mention, l.Evidence = "", ""
		}
		out.Edges = append(out.Edges, l)
		out.Labels[l.ToKind+":"+l.ToID] = label
	}
	return out, nil
}
