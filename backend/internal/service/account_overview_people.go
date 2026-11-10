package service

import (
	"context"
	"sort"

	"github.com/ttobak/backend/internal/model"
	"github.com/ttobak/backend/internal/repository"
)

// graphDirectMembers returns the canonical direct members by user ID.
func (s *AccountOverviewService) graphDirectMembers(ctx context.Context, accountID string) (map[string]*model.AccountMember, error) {
	members, err := s.repo.ListAccountMembers(ctx, accountID)
	if err != nil {
		return nil, err
	}
	out := map[string]*model.AccountMember{}
	for i := range members {
		m := &members[i]
		if m.AccountID == accountID && m.PK == model.PrefixAccount+accountID && m.SK == model.PrefixMember+m.UserID && validLinkPlainID(m.UserID) {
			out[m.UserID] = m
		}
	}
	return out, nil
}

func graphMemberLabel(m *model.AccountMember) string {
	name := m.Email
	if name == "" {
		name = m.UserID
	}
	if m.Role != "" {
		name += " (" + m.Role + ")"
	}
	return name
}

// graphPeopleRows reads the people edges to member, contact and account
// targets, then to each project node, together at most maxGraphPeopleRows.
func (s *AccountOverviewService) graphPeopleRows(ctx context.Context, accountID string, b *graphBuilder) ([]model.PeopleLink, error) {
	var rows []model.PeopleLink
	budget := maxGraphPeopleRows
	for _, kind := range []string{model.LinkKindMember, model.LinkKindContact, model.LinkKindAccount} {
		if budget <= 0 {
			b.g.Truncated = true
			return rows, nil
		}
		links, next, err := s.repo.ListPeopleLinksByTarget(ctx, accountID, kind, "", budget)
		if err != nil {
			return nil, err
		}
		budget -= max(len(links), 1)
		b.g.Truncated = b.g.Truncated || next != ""
		rows = append(rows, links...)
	}
	for _, n := range b.g.Nodes {
		if n.Kind != model.GraphNodeProject {
			continue
		}
		if budget <= 0 {
			b.g.Truncated = true
			break
		}
		projectID := n.ID[len(model.GraphNodeProject)+1:]
		links, more, err := s.repo.ListPeopleLinksToTarget(ctx, accountID, model.LinkKindProject, projectID, budget)
		if err != nil {
			return nil, err
		}
		budget -= max(len(links), 1)
		b.g.Truncated = b.g.Truncated || more
		rows = append(rows, links...)
	}
	return rows, nil
}

// graphPeople adds meeting→person edges by role. A row counts only when its
// meeting node is already present (graphMeetings revalidated it), so it adds
// no meeting reads. Members must still be direct members and get member_of
// edges; contacts must be readable; an account target needs the viewer's
// read grant; a project target must already be a node. Under the summary
// guard, edges from action items and insights are left out, as in the flow.
func (s *AccountOverviewService) graphPeople(ctx context.Context, userID, accountID string, member *model.AccountMember, b *graphBuilder, reads *overviewReads) error {
	members, err := s.graphDirectMembers(ctx, accountID)
	if err != nil {
		return err
	}
	b.g.MemberCount = len(members)
	rows, err := s.graphPeopleRows(ctx, accountID, b)
	if err != nil {
		return err
	}
	accountNode := graphNodeID(model.GraphNodeAccount, accountID)
	accounts := map[string]string{} // target account ID -> label, "" when unreadable
	for _, l := range rows {
		meetingNode := graphNodeID(model.GraphNodeMeeting, l.MeetingID)
		if !b.nodes[meetingNode] || l.AccountID != accountID {
			continue
		}
		if m := reads.meetings[l.MeetingID]; m == nil || (summaryDerivedPeopleSource(l.SourceField) &&
			userID != m.UserID && accountMemberNeedsSummaryGuard(member) && !m.AccountSummaryMetadataShareable()) {
			continue
		}
		var kind, label string
		switch l.ToKind {
		case model.LinkKindMember:
			if m := members[l.ToID]; m != nil {
				kind, label = model.GraphNodeMember, graphMemberLabel(m)
			}
		case model.LinkKindContact:
			c, err := s.graphContact(ctx, accountID, l.ToID, b)
			if err != nil {
				return err
			}
			if c != nil {
				kind, label = model.GraphNodeContact, c.Name
			}
		case model.LinkKindAccount:
			name, seen := accounts[l.ToID]
			if !seen {
				// A read grant can walk the ancestry: bound the distinct
				// account targets read, and read none once the node cap is full.
				if len(b.g.Nodes) >= maxGraphNodes || len(accounts) >= maxGraphAccountTargets {
					b.g.Truncated = true
					continue
				}
				if name, err = s.graphAccountLabel(ctx, userID, l.ToID); err != nil {
					return err
				}
				accounts[l.ToID] = name
			}
			if name != "" {
				kind, label = model.GraphNodeAccount, name
			}
		case model.LinkKindProject:
			if b.hasNode(model.GraphNodeProject, l.ToID) {
				kind = model.GraphNodeProject
			}
		}
		if kind == "" || (label != "" && !b.node(kind, l.ToID, label)) {
			continue
		}
		target := graphNodeID(kind, l.ToID)
		b.roleEdge(meetingNode, target, model.GraphEdgePerson, l.Origin, l.Role)
		if kind == model.GraphNodeMember {
			b.edge(target, accountNode, model.GraphEdgeMemberOf, "")
		}
	}
	return nil
}

// graphAccountLabel returns a mentioned account's name when the viewer holds
// a read grant on it, or "".
func (s *AccountOverviewService) graphAccountLabel(ctx context.Context, userID, id string) (string, error) {
	if !validLinkPlainID(id) {
		return "", nil
	}
	grant, err := resolveAccountReadGrant(ctx, s.repo, id, userID)
	if err != nil || grant == nil {
		return "", err
	}
	account, err := s.repo.GetAccount(ctx, id)
	if err != nil || account == nil || account.AccountID != id {
		return "", err
	}
	return account.Name, nil
}

// maxGraphAccountTargets bounds the distinct mentioned accounts whose read
// grant and name the graph reads.
const maxGraphAccountTargets = 20

const (
	maxFlowPeoplePerItem = 5
	// maxFlowPeopleQueries bounds the meeting people edge queries per flow;
	// later meeting items get no People (not an error).
	maxFlowPeopleQueries = 50
	// maxFlowPeopleContactReads bounds the distinct contacts opened per flow.
	maxFlowPeopleContactReads = 100
)

// flowPeopleRoles orders a meeting's people roles; mentions never show.
var flowPeopleRoles = map[string]int{model.PeopleRoleParticipant: 1, model.PeopleRoleSpeaker: 2, model.PeopleRoleAssignee: 3}

// flowPeopleReads caches one flow's people reads.
type flowPeopleReads struct {
	members      map[string]*model.AccountMember
	labels       map[string]string // member user ID -> label
	contacts     map[string]*model.AccountContact
	contactReads int
	meetings     map[string][]model.FlowPerson
	queries      int
	truncated    bool // a query or contact-read bound left people out
}

func (s *AccountOverviewService) flowContact(ctx context.Context, accountID, id string, p *flowPeopleReads) (*model.AccountContact, error) {
	if c, seen := p.contacts[id]; seen {
		return c, nil
	}
	if p.contactReads >= maxFlowPeopleContactReads {
		p.truncated = true
		return nil, nil
	}
	p.contactReads++
	c, err := s.repo.GetAccountContact(ctx, accountID, id)
	if graphRowUnreadable(err, repository.ErrContactUnreadable) {
		c, err = nil, nil
	}
	if err != nil {
		return nil, err
	}
	if c != nil && (c.ContactID != id || c.Name == "") {
		c = nil
	}
	p.contacts[id] = c
	return c, nil
}

// flowMemberLabel labels a direct member by profile name, else email; "" for
// a former member.
func (s *AccountOverviewService) flowMemberLabel(ctx context.Context, accountID, id string, p *flowPeopleReads) (string, error) {
	if p.members == nil {
		members, err := s.graphDirectMembers(ctx, accountID)
		if err != nil {
			return "", err
		}
		p.members = members
	}
	m := p.members[id]
	if m == nil {
		return "", nil
	}
	if label, ok := p.labels[id]; ok {
		return label, nil
	}
	label := m.Email
	user, err := s.repo.GetUserByID(ctx, id)
	if err != nil {
		return "", err
	}
	if user != nil && user.UserID == id && user.Name != "" {
		label = user.Name
	}
	p.labels[id] = label
	return label, nil
}

// meetingPeople lists a published meeting's member and contact edges,
// participant first, one entry per person. Under the summary guard,
// assignees (from action items) are left out.
func (s *AccountOverviewService) meetingPeople(ctx context.Context, userID, accountID string, member *model.AccountMember, linkID string, p *flowPeopleReads, reads *overviewReads) ([]model.FlowPerson, error) {
	if people, seen := p.meetings[linkID]; seen {
		return people, nil
	}
	meeting := reads.meetings[linkID]
	if meeting == nil {
		return nil, nil
	}
	if p.queries >= maxFlowPeopleQueries {
		reads.truncated = true
		return nil, nil
	}
	p.queries++
	links, _, err := s.repo.ListPeopleLinksForMeeting(ctx, accountID, linkID, maxPeopleExistingEdges)
	if err != nil {
		return nil, err
	}
	guarded := userID != meeting.UserID && accountMemberNeedsSummaryGuard(member) && !meeting.AccountSummaryMetadataShareable()
	sort.SliceStable(links, func(i, j int) bool { return flowPeopleRoles[links[i].Role] < flowPeopleRoles[links[j].Role] })
	people := []model.FlowPerson{}
	seen := map[string]bool{}
	for _, l := range links {
		key := l.ToKind + ":" + l.ToID
		if flowPeopleRoles[l.Role] == 0 || seen[key] || len(people) == maxFlowPeoplePerItem ||
			(guarded && summaryDerivedPeopleSource(l.SourceField)) {
			continue
		}
		label := ""
		switch l.ToKind {
		case model.LinkKindMember:
			label, err = s.flowMemberLabel(ctx, accountID, l.ToID, p)
		case model.LinkKindContact:
			var c *model.AccountContact
			if c, err = s.flowContact(ctx, accountID, l.ToID, p); c != nil {
				label = c.Name
			}
		}
		if err != nil {
			return nil, err
		}
		if label != "" {
			seen[key] = true
			people = append(people, model.FlowPerson{Kind: l.ToKind, ID: l.ToID, Label: capRunes(label, maxGraphLabelRunes), Role: l.Role})
		}
	}
	p.meetings[linkID] = people
	return people, nil
}

// flowPeople fills People on activity items (readable attendee contacts)
// and response items (meeting people edges), within the flow bounds.
func (s *AccountOverviewService) flowPeople(ctx context.Context, userID, accountID string, member *model.AccountMember, flow *model.AccountFlow, reads *overviewReads) error {
	p := &flowPeopleReads{labels: map[string]string{}, contacts: map[string]*model.AccountContact{}, meetings: map[string][]model.FlowPerson{}}
	rows := []*model.FlowRow{&flow.Unassigned}
	for i := range flow.Rows {
		rows = append(rows, &flow.Rows[i])
	}
	for _, row := range rows {
		for i := range row.Activity {
			item := &row.Activity[i]
			event := reads.events[item.ID]
			if event == nil {
				continue
			}
			seen := map[string]bool{}
			for _, a := range event.Attendees {
				if len(item.People) == maxFlowPeoplePerItem {
					break
				}
				if !validContactID(a.ContactID) || seen[a.ContactID] {
					continue
				}
				c, err := s.flowContact(ctx, accountID, a.ContactID, p)
				if err != nil {
					return err
				}
				if c != nil {
					seen[a.ContactID] = true
					item.People = append(item.People, model.FlowPerson{Kind: model.LinkKindContact, ID: c.ContactID,
						Label: capRunes(c.Name, maxGraphLabelRunes), Role: "attendee"})
				}
			}
		}
		for i := range row.Response {
			people, err := s.meetingPeople(ctx, userID, accountID, member, row.Response[i].ID, p, reads)
			if err != nil {
				return err
			}
			if len(people) > 0 {
				row.Response[i].People = append([]model.FlowPerson(nil), people...)
			}
		}
	}
	reads.truncated = reads.truncated || p.truncated
	return nil
}

// trimFlowPeople removes one person from the item holding the most; it
// reports whether anything was removed.
func trimFlowPeople(flow *model.AccountFlow) bool {
	var most *model.FlowItem
	rows := []*model.FlowRow{&flow.Unassigned}
	for i := range flow.Rows {
		rows = append(rows, &flow.Rows[i])
	}
	for _, row := range rows {
		for _, col := range [][]model.FlowItem{row.Activity, row.Response} {
			for i := range col {
				if len(col[i].People) > 0 && (most == nil || len(col[i].People) > len(most.People)) {
					most = &col[i]
				}
			}
		}
	}
	if most == nil {
		return false
	}
	most.People = most.People[:len(most.People)-1]
	if len(most.People) == 0 {
		most.People = nil
	}
	return true
}
