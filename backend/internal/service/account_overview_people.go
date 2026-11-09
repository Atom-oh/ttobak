package service

import (
	"context"

	"github.com/ttobak/backend/internal/model"
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
// read grant; a project target must already be a node.
func (s *AccountOverviewService) graphPeople(ctx context.Context, userID, accountID string, b *graphBuilder) error {
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
