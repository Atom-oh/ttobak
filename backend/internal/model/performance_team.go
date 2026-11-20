package model

import (
	"regexp"
	"strings"
)

// PrefixPerformanceTeam keys owner-private SFDC account team rows:
// USER#{id} / PERFTEAM#{sfdcAccountId}#{alias}.
const PrefixPerformanceTeam = "PERFTEAM#"

// PerformanceSourceSFDCAccountTeam syncs the SFDC account team of each
// assigned account.
const PerformanceSourceSFDCAccountTeam = "sfdc-account-team"

// PerformanceTeamMaxPerAccount caps stored team members per SFDC account.
const PerformanceTeamMaxPerAccount = 100

// Onboarding statuses of one SFDC team member against the mapped Account.
const (
	OnboardingStatusMember  = "member"
	OnboardingStatusPending = "pending"
	OnboardingStatusNone    = "none"
	OnboardingStatusUnknown = "unknown"
	// OnboardingStatusMissing is a summary-only value: the SFDC team has a
	// person in that role, but none is a member or pending invitee.
	OnboardingStatusMissing = "missing"
)

var (
	performanceTeamAccount = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	// PerformanceTeamAlias is the stored, lower-case alias grammar.
	PerformanceTeamAlias = regexp.MustCompile(`^[a-z0-9._-]{1,64}$`)
)

// SFDCTeamMember holds exactly the stored account-team fields (ADR-023).
// Email is derived as alias@amazon.com and never stored.
type SFDCTeamMember struct {
	SFDCAccountID  string `json:"sfdcAccountId" dynamodbav:"sfdcAccountId"`
	Alias          string `json:"alias" dynamodbav:"alias"`
	Name           string `json:"name" dynamodbav:"name"`
	Title          string `json:"title" dynamodbav:"title"`
	TeamMemberRole string `json:"teamMemberRole" dynamodbav:"teamMemberRole"`
	AllTeamRoles   string `json:"allTeamRoles" dynamodbav:"allTeamRoles"`
}

func PerformanceTeamSortKey(sfdcAccountID, alias string) string {
	return PrefixPerformanceTeam + sfdcAccountID + "#" + alias
}

// PerformanceTeamAccountPrefix is the sort-key prefix of one account's team.
func PerformanceTeamAccountPrefix(sfdcAccountID string) string {
	return PrefixPerformanceTeam + sfdcAccountID + "#"
}

// PerformanceTeamExternalID is the remove ID of a team row: {sfdcAccountId}:{alias}.
func PerformanceTeamExternalID(sfdcAccountID, alias string) string {
	return sfdcAccountID + ":" + alias
}

// ParsePerformanceTeamExternalID splits and validates a team remove ID.
func ParsePerformanceTeamExternalID(id string) (string, string, bool) {
	account, alias, found := strings.Cut(id, ":")
	if !found || !performanceTeamAccount.MatchString(account) || !PerformanceTeamAlias.MatchString(alias) {
		return "", "", false
	}
	return account, alias, true
}

// ValidPerformanceTeamAccountID reports whether id is a valid SFDC account ID.
func ValidPerformanceTeamAccountID(id string) bool { return performanceTeamAccount.MatchString(id) }

// MapSFDCTeamRole maps SFDC account-team roles to an invitable TTOBAK role,
// or "" for anyone else. SFDC also labels Migration BDMs "Account Manager",
// so AM additionally requires "Account Manager" among all team roles.
func MapSFDCTeamRole(teamMemberRole, allTeamRoles string) string {
	switch strings.TrimSpace(teamMemberRole) {
	case "Account Manager":
		for _, role := range strings.FieldsFunc(allTeamRoles, func(r rune) bool { return r == ';' || r == ',' }) {
			if strings.TrimSpace(role) == "Account Manager" {
				return RoleAM
			}
		}
		return ""
	case "Technical Account Manager", "Enterprise Support Manager":
		return RoleTAM
	case "Solutions Architect":
		return RoleSA
	}
	return ""
}

// OnboardingMember is one stored team member with its mapped role and status.
type OnboardingMember struct {
	SFDCTeamMember
	Role   string `json:"role"`
	Status string `json:"status"`
}

// OnboardingDetail joins one assigned SFDC account's stored team with the
// mapped Account's members. AccountReadable is false when the owner cannot
// read that Account; every status is then "unknown".
type OnboardingDetail struct {
	SFDCAccountID   string             `json:"sfdcAccountId"`
	Name            string             `json:"name"`
	AccountID       string             `json:"accountId,omitempty"`
	Mapped          bool               `json:"mapped"`
	AccountReadable bool               `json:"accountReadable"`
	Truncated       bool               `json:"truncated"`
	Members         []OnboardingMember `json:"members"`
}

// OnboardingSummaryItem maps each core role present in the SFDC team (AM,
// TAM, SA) to member, pending or missing.
type OnboardingSummaryItem struct {
	SFDCAccountID string            `json:"sfdcAccountId"`
	Name          string            `json:"name"`
	AccountID     string            `json:"accountId"`
	Roles         map[string]string `json:"roles"`
}

type OnboardingSummary struct {
	Items     []OnboardingSummaryItem `json:"items"`
	Truncated bool                    `json:"truncated"`

	// Unavailable counts mapped Accounts whose read failed; they are left out
	// of Items instead of failing the whole summary.
	Unavailable int `json:"unavailable"`
}

// PrefixPerformanceInvite keys the owner's private record of invitations sent
// from onboarding: USER#{id} / PERFINVITE#{sfdcAccountId}#{alias}. The
// platform keeps no listable pending-invite state, so this record is what
// "pending" means here.
const PrefixPerformanceInvite = "PERFINVITE#"

// OnboardingInviteMaxItems caps invitations per request.
const OnboardingInviteMaxItems = 20

// Invite outcomes.
const (
	OnboardingInviteAdded         = "added"
	OnboardingInviteInvited       = "invited"
	OnboardingInviteAlreadyMember = "already_member"
	OnboardingInviteFailed        = "failed"
	// OnboardingInviteNotAttempted: the request's time budget ran out first; retry it.
	OnboardingInviteNotAttempted = "not_attempted"
)

func PerformanceInviteSortKey(sfdcAccountID, alias string) string {
	return PrefixPerformanceInvite + sfdcAccountID + "#" + alias
}

// PerformanceInviteAccountPrefix is the sort-key prefix of one account's invites.
func PerformanceInviteAccountPrefix(sfdcAccountID string) string {
	return PrefixPerformanceInvite + sfdcAccountID + "#"
}

// PerformanceInvite is one sent invitation. InvitedAt is RFC3339 UTC.
type PerformanceInvite struct {
	SFDCAccountID string `json:"sfdcAccountId" dynamodbav:"sfdcAccountId"`
	Alias         string `json:"alias" dynamodbav:"alias"`
	AccountID     string `json:"accountId" dynamodbav:"accountId"`
	Role          string `json:"role" dynamodbav:"role"`
	InvitedAt     string `json:"invitedAt" dynamodbav:"invitedAt"`
}

type OnboardingInviteItem struct {
	Alias string `json:"alias"`
	Role  string `json:"role"`
}

type OnboardingInviteRequest struct {
	Items []OnboardingInviteItem `json:"items"`
}

// OnboardingInviteResult reports one item. Code is the API error code of a
// failed item and empty otherwise.
type OnboardingInviteResult struct {
	Alias   string `json:"alias"`
	Outcome string `json:"outcome"`
	Code    string `json:"code,omitempty"`
}

type OnboardingInviteResponse struct {
	Results []OnboardingInviteResult `json:"results"`
}
