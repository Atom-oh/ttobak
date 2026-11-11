package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

const (
	focusMeetingsPerPage = 12
	maxFocusMeetings     = 60
	maxFocusLinkRows     = 600
	maxFocusCursorBytes  = 512
)

// focusRoleOrder is the display order of people roles.
var focusRoleOrder = []string{model.PeopleRoleParticipant, model.PeopleRoleSpeaker, model.PeopleRoleAssignee, model.PeopleRoleMentions}

type focusCursor struct {
	A string `json:"a"`
	F string `json:"f"`
	O int    `json:"o"`
}

// parseFocus accepts contact:{id}, member:{id} or project:{id}.
func parseFocus(focus string) (kind, id string, err error) {
	kind, id, ok := strings.Cut(focus, ":")
	if !ok || !validLinkPlainID(id) {
		return "", "", ErrInvalidInput
	}
	switch kind {
	case model.LinkKindContact, model.LinkKindMember, model.LinkKindProject:
		return kind, id, nil
	}
	return "", "", ErrInvalidInput
}

func encodeFocusCursor(accountID, focus string, offset int) string {
	b, _ := json.Marshal(focusCursor{A: accountID, F: focus, O: offset})
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeFocusCursor(accountID, focus, cursor string) (int, error) {
	if cursor == "" {
		return 0, nil
	}
	if len(cursor) > maxFocusCursorBytes {
		return 0, ErrInvalidInput
	}
	b, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return 0, ErrInvalidInput
	}
	var c focusCursor
	if json.Unmarshal(b, &c) != nil || c.A != accountID || c.F != focus || c.O <= 0 || c.O >= maxFocusMeetings {
		return 0, ErrInvalidInput
	}
	return c.O, nil
}

// focusMeeting reads a meeting's focus state (publication fields, title,
// date, tags, projectIds) and returns it only while it is published to the
// account. It shares the viewer's meeting cache and read bound.
func (v *peopleViewer) focusMeeting(ctx context.Context, linkID string) (*model.Meeting, bool, error) {
	if m, ok := v.focusMeetings[linkID]; ok {
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
	m, err := v.s.repo.GetMeetingFocusState(ctx, owner, id)
	if err != nil {
		return nil, false, err
	}
	if !meetingPublishedToAccount(m, v.accountID, owner, id) {
		m = nil
	}
	if v.focusMeetings == nil {
		v.focusMeetings = map[string]*model.Meeting{}
	}
	v.focusMeetings[linkID] = m
	v.meetings[linkID] = m
	return m, true, nil
}

// visibleRoles returns the roles of rows the viewer may see for a meeting, in
// display order. Summary-derived rows of a guarded meeting are dropped.
func (v *peopleViewer) visibleRoles(m *model.Meeting, rows []model.PeopleLink) []string {
	guarded := v.guarded(m)
	has := map[string]bool{}
	for _, l := range rows {
		if guarded && summaryDerivedPeopleSource(l.SourceField) {
			continue
		}
		has[l.Role] = true
	}
	roles := []string{}
	for _, role := range focusRoleOrder {
		if has[role] {
			roles = append(roles, role)
		}
	}
	return roles
}

type focusCandidate struct {
	linkID string
	rows   []model.PeopleLink
}

// Focus returns one page of the focus graph of a contact, member or project.
// Direct members only, like the people panel. An unreadable or foreign focus
// is not found.
func (s *AccountPeopleService) Focus(ctx context.Context, userID, accountID, focus, cursor string) (*model.AccountFocusGraph, error) {
	kind, id, err := parseFocus(focus)
	if err != nil {
		return nil, err
	}
	offset, err := decodeFocusCursor(accountID, focus, cursor)
	if err != nil {
		return nil, err
	}
	v, err := s.viewer(ctx, accountID, userID)
	if err != nil {
		return nil, err
	}
	card, err := s.focusCard(ctx, v, kind, id)
	if err != nil {
		return nil, err
	}
	out := &model.AccountFocusGraph{Focus: *card, Meetings: []model.FocusMeeting{}, Techs: []model.FocusTech{}, Other: []model.FocusTag{}}

	candidates, truncated, err := s.focusCandidates(ctx, accountID, kind, id)
	if err != nil {
		return nil, err
	}
	out.Truncated = truncated

	type found struct {
		m     *model.Meeting
		id    string
		roles []string
	}
	var meetings []found
	for _, c := range candidates {
		if len(meetings) == maxFocusMeetings {
			out.Truncated = true
			break
		}
		m, known, err := v.focusMeeting(ctx, c.linkID)
		if err != nil {
			return nil, err
		}
		if !known {
			break
		}
		if m == nil {
			continue
		}
		roles := []string{}
		if kind == model.LinkKindProject {
			if !slices.Contains(m.ProjectIDs, id) {
				continue
			}
		} else if roles = v.visibleRoles(m, c.rows); len(roles) == 0 {
			continue
		}
		meetings = append(meetings, found{m: m, id: c.linkID, roles: roles})
	}
	out.Truncated = out.Truncated || v.truncated
	sort.SliceStable(meetings, func(i, j int) bool {
		if !meetings[i].m.Date.Equal(meetings[j].m.Date) {
			return meetings[i].m.Date.After(meetings[j].m.Date)
		}
		return meetings[i].id < meetings[j].id
	})
	out.Focus.Meetings = len(meetings)
	// A valid cursor past the end (meetings unpublished between pages) yields
	// an empty final page.
	offset = min(offset, len(meetings))

	techCounts := map[string]int{}
	otherCounts := map[string]int{}
	techsByMeeting := make([][]string, len(meetings))
	for i, f := range meetings {
		techs := []string{}
		if !v.guarded(f.m) {
			var other []string
			techs, other = matchTechTags(f.m.Tags)
			if techs == nil {
				techs = []string{}
			}
			for _, tag := range other {
				otherCounts[tag]++
			}
		}
		for _, t := range techs {
			techCounts[t]++
		}
		techsByMeeting[i] = techs
	}
	for _, entry := range techCatalog {
		if n := techCounts[entry.ID]; n > 0 {
			out.Techs = append(out.Techs, model.FocusTech{ID: entry.ID, Label: entry.Label, Group: entry.Group, Meetings: n})
		}
	}
	sort.SliceStable(out.Techs, func(i, j int) bool { return out.Techs[i].Meetings > out.Techs[j].Meetings })
	for tag, n := range otherCounts {
		out.Other = append(out.Other, model.FocusTag{Tag: tag, Meetings: n})
	}
	sort.Slice(out.Other, func(i, j int) bool {
		if out.Other[i].Meetings != out.Other[j].Meetings {
			return out.Other[i].Meetings > out.Other[j].Meetings
		}
		return out.Other[i].Tag < out.Other[j].Tag
	})

	end := min(offset+focusMeetingsPerPage, len(meetings))
	for i := offset; i < end; i++ {
		f := meetings[i]
		people, cut, err := s.focusPeople(ctx, v, f.m, f.id)
		if err != nil {
			return nil, err
		}
		out.Truncated = out.Truncated || cut
		out.Meetings = append(out.Meetings, model.FocusMeeting{ID: f.id, Title: f.m.Title, Date: f.m.Date.UTC().Format(time.RFC3339),
			Roles: f.roles, Techs: techsByMeeting[i], People: people})
	}
	if end < len(meetings) {
		out.NextCursor = encodeFocusCursor(accountID, focus, end)
	}
	return out, nil
}

// focusCard checks the focus is readable in the account and describes it.
func (s *AccountPeopleService) focusCard(ctx context.Context, v *peopleViewer, kind, id string) (*model.FocusCard, error) {
	label, ok, err := v.label(ctx, kind, id)
	if err != nil {
		return nil, err
	}
	if !ok || label == "" {
		return nil, ErrNotFound
	}
	card := &model.FocusCard{Kind: kind, ID: id, Label: label}
	switch kind {
	case model.LinkKindContact:
		c, err := s.repo.GetAccountContact(ctx, v.accountID, id)
		if err != nil && !errors.Is(err, repository.ErrContactUnreadable) {
			return nil, err
		}
		if c != nil && c.ContactID == id {
			card.Detail = joinNonEmpty(" · ", c.Title, c.Department)
		}
	case model.LinkKindMember:
		if m := v.members[id]; m != nil {
			card.Detail = m.Role
		}
	case model.LinkKindProject:
		p, err := s.repo.GetProject(ctx, id)
		if err != nil {
			return nil, err
		}
		if p != nil && p.ProjectID == id {
			card.Detail = p.Stage
		}
	}
	return card, nil
}

// focusCandidates lists the stored rows that connect the focus to meetings:
// people links to a person (reverse-index order, not date order), or
// project meeting refs (the repository returns them newest first).
func (s *AccountPeopleService) focusCandidates(ctx context.Context, accountID, kind, id string) ([]focusCandidate, bool, error) {
	var out []focusCandidate
	if kind == model.LinkKindProject {
		refs, err := s.repo.ListProjectMeetingRefsForProject(ctx, id)
		if err != nil {
			return nil, false, err
		}
		seen := map[string]bool{}
		for _, ref := range refs {
			if ref.ProjectID != id || !validLinkPlainID(ref.OwnerUserID) || !validLinkPlainID(ref.MeetingID) {
				continue
			}
			linkID := ref.OwnerUserID + ":" + ref.MeetingID
			if !seen[linkID] {
				seen[linkID] = true
				out = append(out, focusCandidate{linkID: linkID})
			}
		}
		return out, false, nil
	}
	rows, more, err := s.repo.ListPeopleLinksToTarget(ctx, accountID, kind, id, maxFocusLinkRows)
	if err != nil {
		return nil, false, err
	}
	index := map[string]int{}
	for _, l := range rows {
		if l.AccountID != accountID || l.ToKind != kind || l.ToID != id {
			continue
		}
		i, seen := index[l.MeetingID]
		if !seen {
			i = len(out)
			index[l.MeetingID] = i
			out = append(out, focusCandidate{linkID: l.MeetingID})
		}
		out[i].rows = append(out[i].rows, l)
	}
	return out, more, nil
}

// focusPeople returns the readable members and contacts of one meeting with
// their visible roles: contacts first, then members, each by label.
func (s *AccountPeopleService) focusPeople(ctx context.Context, v *peopleViewer, m *model.Meeting, linkID string) ([]model.FocusPerson, bool, error) {
	links, cut, err := s.repo.ListPeopleLinksForMeeting(ctx, v.accountID, linkID, maxPeopleExistingEdges)
	if err != nil {
		return nil, false, err
	}
	rows := map[string][]model.PeopleLink{}
	var keys []string
	for _, l := range links {
		if l.AccountID != v.accountID || l.MeetingID != linkID || (l.ToKind != model.LinkKindContact && l.ToKind != model.LinkKindMember) {
			continue
		}
		key := l.ToKind + ":" + l.ToID
		if _, seen := rows[key]; !seen {
			keys = append(keys, key)
		}
		rows[key] = append(rows[key], l)
	}
	people := []model.FocusPerson{}
	for _, key := range keys {
		kind, id, _ := strings.Cut(key, ":")
		roles := v.visibleRoles(m, rows[key])
		if len(roles) == 0 {
			continue
		}
		label, ok, err := v.label(ctx, kind, id)
		if err != nil {
			return nil, false, err
		}
		if !ok || label == "" {
			continue
		}
		person := model.FocusPerson{Kind: kind, ID: id, Label: label, Roles: roles}
		if kind == model.LinkKindMember {
			if member := v.members[id]; member != nil {
				person.Detail = member.Role
			}
		}
		people = append(people, person)
	}
	sort.SliceStable(people, func(i, j int) bool {
		if people[i].Kind != people[j].Kind {
			return people[i].Kind == model.LinkKindContact
		}
		return people[i].Label < people[j].Label
	})
	return people, cut, nil
}

func joinNonEmpty(sep string, parts ...string) string {
	var kept []string
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, sep)
}
