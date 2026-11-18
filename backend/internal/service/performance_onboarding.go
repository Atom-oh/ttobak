package service

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/ttobak/backend/internal/model"
)

// Account onboarding joins the owner's stored SFDC account team with the
// mapped TTOBAK Account. Like every performance read it is owner-private:
// member emails are compared here and never returned.

// performanceOnboardingMaxAccounts bounds the Account reads of one summary.
const performanceOnboardingMaxAccounts = 100

// performanceInviteTTL matches the platform's pending-invite expiry.
const performanceInviteTTL = 30 * 24 * time.Hour

// PerformanceMemberInviter is the AccountService member-add path. Onboarding
// invitations go through it unchanged, so its membership, role and invite
// rules apply.
type PerformanceMemberInviter interface {
	AddMember(ctx context.Context, requesterUserID, accountID string, req *model.AddMemberRequest) (*model.AccountMemberDTO, error)
}

var onboardingRoleOrder = map[string]int{model.RoleAM: 0, model.RoleTAM: 1, model.RoleSA: 2}

func onboardingEmail(alias string) string { return alias + "@amazon.com" }

// onboardingAssignment returns the caller's assignment for sfdcAccountID or ErrNotFound.
func (svc *PerformanceService) onboardingAssignment(ctx context.Context, userID, sfdcAccountID string) (*model.SFDCAssignment, error) {
	if !performanceUserPattern.MatchString(userID) {
		return nil, ErrForbidden
	}
	if !model.ValidPerformanceTeamAccountID(sfdcAccountID) {
		return nil, ErrInvalidInput
	}
	assignments, err := svc.store.ListPerformanceAssignments(ctx, userID)
	if err != nil {
		return nil, err
	}
	for _, assignment := range assignments {
		if assignment.SFDCAccountID == sfdcAccountID {
			found := assignment
			return &found, nil
		}
	}
	return nil, ErrNotFound
}

// onboardingMembers reads the mapped Account's member emails. ok is false
// when the caller cannot read that Account.
func (svc *PerformanceService) onboardingMembers(ctx context.Context, userID, accountID string) (map[string]bool, bool, error) {
	if svc.accounts == nil {
		return nil, false, nil
	}
	account, err := svc.accounts.GetAccount(ctx, userID, accountID)
	if errors.Is(err, ErrForbidden) || errors.Is(err, ErrNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if account == nil {
		return nil, false, nil
	}
	emails := make(map[string]bool, len(account.Members))
	for _, member := range account.Members {
		if member.Email != "" {
			emails[strings.ToLower(member.Email)] = true
		}
	}
	return emails, true, nil
}

// onboardingPending returns the aliases with an unexpired invite to accountID.
func (svc *PerformanceService) onboardingPending(ctx context.Context, userID, sfdcAccountID, accountID string) (map[string]bool, error) {
	invites, err := svc.store.ListPerformanceInvites(ctx, userID, sfdcAccountID)
	if err != nil {
		return nil, err
	}
	cutoff := svc.now().UTC().Add(-performanceInviteTTL)
	pending := make(map[string]bool, len(invites))
	for _, invite := range invites {
		at, err := time.Parse(time.RFC3339, invite.InvitedAt)
		if err == nil && invite.AccountID == accountID && at.After(cutoff) {
			pending[invite.Alias] = true
		}
	}
	return pending, nil
}

func (svc *PerformanceService) onboardingMapping(ctx context.Context, userID, sfdcAccountID string) (string, error) {
	mappings, err := svc.store.GetAccountMappings(ctx, userID, []string{sfdcAccountID})
	if err != nil {
		return "", err
	}
	return mappings[sfdcAccountID].AccountID, nil
}

// GetOnboarding returns one assigned SFDC account's stored team with each
// person's mapped role and status against the mapped Account.
func (svc *PerformanceService) GetOnboarding(ctx context.Context, userID, sfdcAccountID string) (*model.OnboardingDetail, error) {
	assignment, err := svc.onboardingAssignment(ctx, userID, sfdcAccountID)
	if err != nil {
		return nil, err
	}
	accountID, err := svc.onboardingMapping(ctx, userID, sfdcAccountID)
	if err != nil {
		return nil, err
	}
	return svc.onboardingDetail(ctx, userID, *assignment, accountID)
}

// onboardingDetail builds the detail of one assignment mapped to accountID
// (empty when unmapped).
func (svc *PerformanceService) onboardingDetail(ctx context.Context, userID string, assignment model.SFDCAssignment, accountID string) (*model.OnboardingDetail, error) {
	sfdcAccountID := assignment.SFDCAccountID
	team, err := svc.store.ListPerformanceTeam(ctx, userID, sfdcAccountID)
	if err != nil {
		return nil, err
	}
	detail := &model.OnboardingDetail{SFDCAccountID: sfdcAccountID, Name: assignment.Name, Members: []model.OnboardingMember{}}
	if len(team) > model.PerformanceTeamMaxPerAccount {
		team, detail.Truncated = team[:model.PerformanceTeamMaxPerAccount], true
	}
	var emails, pending map[string]bool
	if accountID != "" {
		var readable bool
		if emails, readable, err = svc.onboardingMembers(ctx, userID, accountID); err != nil {
			return nil, err
		}
		if readable {
			detail.Mapped, detail.AccountID, detail.AccountReadable = true, accountID, true
			if pending, err = svc.onboardingPending(ctx, userID, sfdcAccountID, accountID); err != nil {
				return nil, err
			}
		} else {
			// The mapping target is not readable: report it like ListMappings
			// does, without its ID or any member state.
			detail.Mapped = true
		}
	}
	for _, person := range team {
		status := model.OnboardingStatusNone
		switch {
		case detail.Mapped && !detail.AccountReadable:
			status = model.OnboardingStatusUnknown
		case emails[strings.ToLower(onboardingEmail(person.Alias))]:
			status = model.OnboardingStatusMember
		case pending[person.Alias]:
			status = model.OnboardingStatusPending
		}
		detail.Members = append(detail.Members, model.OnboardingMember{SFDCTeamMember: person,
			Role: model.MapSFDCTeamRole(person.TeamMemberRole, person.AllTeamRoles), Status: status})
	}
	sort.SliceStable(detail.Members, func(i, j int) bool {
		ri, iCore := onboardingRoleOrder[detail.Members[i].Role]
		rj, jCore := onboardingRoleOrder[detail.Members[j].Role]
		if iCore != jCore {
			return iCore
		}
		if iCore && ri != rj {
			return ri < rj
		}
		return detail.Members[i].Alias < detail.Members[j].Alias
	})
	return detail, nil
}

// ListOnboarding summarizes the core roles of each assigned SFDC account
// whose mapping target the owner can read. At most
// performanceOnboardingMaxAccounts Accounts are read; Truncated reports more.
func (svc *PerformanceService) ListOnboarding(ctx context.Context, userID string) (*model.OnboardingSummary, error) {
	if !performanceUserPattern.MatchString(userID) {
		return nil, ErrForbidden
	}
	assignments, err := svc.store.ListPerformanceAssignments(ctx, userID)
	if err != nil {
		return nil, err
	}
	sort.Slice(assignments, func(i, j int) bool { return assignments[i].SFDCAccountID < assignments[j].SFDCAccountID })
	if len(assignments) > MaxPerformanceAssignments {
		assignments = assignments[:MaxPerformanceAssignments]
	}
	ids := make([]string, len(assignments))
	for i, assignment := range assignments {
		ids[i] = assignment.SFDCAccountID
	}
	mappings, err := svc.store.GetAccountMappings(ctx, userID, ids)
	if err != nil {
		return nil, err
	}
	summary := &model.OnboardingSummary{Items: []model.OnboardingSummaryItem{}}
	read := 0
	for _, assignment := range assignments {
		accountID := mappings[assignment.SFDCAccountID].AccountID
		if accountID == "" {
			continue
		}
		if read == performanceOnboardingMaxAccounts {
			summary.Truncated = true
			break
		}
		read++
		detail, err := svc.onboardingDetail(ctx, userID, assignment, accountID)
		if err != nil {
			return nil, err
		}
		if !detail.AccountReadable {
			continue
		}
		item := model.OnboardingSummaryItem{SFDCAccountID: assignment.SFDCAccountID, Name: assignment.Name,
			AccountID: detail.AccountID, Roles: map[string]string{}}
		for _, person := range detail.Members {
			if _, core := onboardingRoleOrder[person.Role]; !core {
				continue
			}
			current := item.Roles[person.Role]
			switch {
			case person.Status == model.OnboardingStatusMember:
				item.Roles[person.Role] = model.OnboardingStatusMember
			case person.Status == model.OnboardingStatusPending && current != model.OnboardingStatusMember:
				item.Roles[person.Role] = model.OnboardingStatusPending
			case current == "":
				item.Roles[person.Role] = model.OnboardingStatusMissing
			}
		}
		summary.Items = append(summary.Items, item)
	}
	return summary, nil
}

// InviteOnboarding invites selected stored team members to the mapped
// Account through AccountService.AddMember, one at a time, and records each
// pending invitation in the owner's partition. A failed item does not stop
// the rest.
func (svc *PerformanceService) InviteOnboarding(ctx context.Context, userID, sfdcAccountID string, request model.OnboardingInviteRequest) (*model.OnboardingInviteResponse, error) {
	if len(request.Items) == 0 || len(request.Items) > model.OnboardingInviteMaxItems {
		return nil, ErrInvalidInput
	}
	seen := make(map[string]bool, len(request.Items))
	for i, item := range request.Items {
		alias := strings.ToLower(item.Alias)
		if !model.PerformanceTeamAlias.MatchString(alias) || !isAssignableRole(item.Role) || seen[alias] {
			return nil, ErrInvalidInput
		}
		seen[alias] = true
		request.Items[i].Alias = alias
	}
	if _, err := svc.onboardingAssignment(ctx, userID, sfdcAccountID); err != nil {
		return nil, err
	}
	inviter, ok := svc.accounts.(PerformanceMemberInviter)
	if !ok {
		return nil, ErrForbidden
	}
	accountID, err := svc.onboardingMapping(ctx, userID, sfdcAccountID)
	if err != nil {
		return nil, err
	}
	if accountID == "" {
		return nil, ErrNotFound
	}
	team, err := svc.store.ListPerformanceTeam(ctx, userID, sfdcAccountID)
	if err != nil {
		return nil, err
	}
	stored := make(map[string]bool, len(team))
	for _, person := range team {
		stored[person.Alias] = true
	}
	for _, item := range request.Items {
		if !stored[item.Alias] {
			return nil, ErrNotFound
		}
	}
	response := &model.OnboardingInviteResponse{Results: make([]model.OnboardingInviteResult, 0, len(request.Items))}
	for _, item := range request.Items {
		result := model.OnboardingInviteResult{Alias: item.Alias}
		dto, err := inviter.AddMember(ctx, userID, accountID, &model.AddMemberRequest{Email: onboardingEmail(item.Alias), Role: item.Role})
		switch {
		case errors.Is(err, ErrMemberExists):
			result.Outcome = model.OnboardingInviteAlreadyMember
		case err != nil:
			result.Outcome, result.Code = model.OnboardingInviteFailed, onboardingInviteCode(err)
		case dto != nil && dto.Pending:
			result.Outcome = model.OnboardingInviteInvited
			invite := model.PerformanceInvite{SFDCAccountID: sfdcAccountID, Alias: item.Alias, AccountID: accountID,
				Role: item.Role, InvitedAt: svc.now().UTC().Format(time.RFC3339)}
			if err := svc.store.PutPerformanceInvite(ctx, userID, invite); err != nil {
				// The invitation exists; only its private record failed.
				result.Code = model.ErrCodeInternalError
			}
		default:
			result.Outcome = model.OnboardingInviteAdded
		}
		response.Results = append(response.Results, result)
	}
	return response, nil
}

func onboardingInviteCode(err error) string {
	switch {
	case errors.Is(err, ErrInvitationRateLimited):
		return model.ErrCodeRateLimited
	case errors.Is(err, ErrInvitationGrantFailed):
		return model.ErrCodeInvitationGrantFailed
	case errors.Is(err, ErrForbidden):
		return model.ErrCodeForbidden
	case errors.Is(err, ErrAccountMerging):
		return model.ErrCodeConflict
	case errors.Is(err, ErrInvalidInviteEmail), errors.Is(err, ErrInvitationRejected), errors.Is(err, ErrInvalidInput),
		errors.Is(err, ErrSelfShare), errors.Is(err, ErrNotFound):
		return model.ErrCodeBadRequest
	}
	return model.ErrCodeInternalError
}
