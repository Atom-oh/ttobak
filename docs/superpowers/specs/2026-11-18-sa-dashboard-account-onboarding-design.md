# SA dashboard: onboarding SFDC-assigned accounts and their account team

- Status: historical design record (2026-11-10), approved by the owner 2026-11-18.
- Follows: the [SA performance dashboard design](2026-11-10-sa-performance-dashboard-design.md),
  plan 1 (merged).
- Residency: amends ADR-023, SA performance data residency.

## Intent

Every customer the owner is assigned in SFDC should become a working TTOBAK
Account with the right people in it. Today the mapping queue on `/performance`
only links an SFDC account to an Account that already exists. When none exists,
the owner is stuck.

The owner wants two things from the queue:

- create the missing Account in one step, which assigns it immediately, the
  same way Accounts are created elsewhere;
- see the AM, TAM and other colleagues registered on the SFDC account team, and
  invite them at once, also for Accounts that already exist but lack those
  people.

## Owner decisions (2026-11-10)

- Plan 1 and follow-ups 1 to 4 have shipped. This feature is the last follow-up.
- The SFDC account team's alias, name, title and role may be stored in TTOBAK
  cloud as owner-private data. An invitation needs `alias@amazon.com`.
- The owner, the SA, is the Owner of an Account created this way. Team members
  are invited with their mapped role.

## Goals

1. **Collect the account team.**
   - The Mac runner (`ttobak-mcp intake sfdc`) calls
     `get_team_members {memberType:"Account", id}` for every assigned SFDC
     account: registry or activity, the same set as plan 1.
   - It sends a new source, `sfdc-account-team`, holding per account the
     members' alias, name, title, `teamMemberRole` and `all_Team_Roles__c`.
   - It never sends access levels, user IDs or created dates.
   - The owner's own row is skipped.
2. **Map roles into TTOBAK roles** (`backend/internal/model/account.go`), as a
   pure function with table tests:

   | SFDC role | TTOBAK role |
   |---|---|
   | `teamMemberRole` "Account Manager" and `all_Team_Roles__c` containing "Account Manager" | AM. This excludes the Migration BDMs that SFDC also labels Account Manager. |
   | "Technical Account Manager" | TAM |
   | "Solutions Architect" | SA |
   | "Enterprise Support Manager" | TAM (designer's call) |
   | anything else | no role; "other", shown only under "show all" |

3. **Create a missing Account from the queue.**
   - For an unmapped SFDC account with no matching readable Account, show
     a "create customer" button.
   - It opens the existing Account creation flow, prefilled with the SFDC name,
     with the owner as Owner. Creation uses the existing `POST /api/accounts`
     with a `requestId`, so a retry is a safe replay.
   - On success, `PUT /api/performance/mappings/{sfdcAccountId}` binds the new
     Account manually.
   - Then the invite step opens.
4. **Invite the team.**
   - For a mapped Account, list the SFDC team members who are not current
     members or pending invitees of that Account. Core roles (AM, TAM, SA) are
     pre-checked; "show all" reveals the others, unchecked.
   - The invite button sends the selected people to
     `POST /api/performance/onboarding/{sfdcAccountId}/invites`, which runs the
     existing account member-add path once per person with `alias@amazon.com`
     and the mapped role, and records each pending invitation owner-privately
     (the platform keeps no listable pending-invite state; plan ruling).
   - Existing members and pending invitation rules apply unchanged: ADR-015
     for verified-email binding and 30-day expiry, ADR-007 for the allowlist.
   - Each result shows per person: invited, already a member, or failed with a
     reason.
   - No email is sent except the platform's existing invitation path.
5. **Onboarding status in the queue.** Each assigned account shows a summary
   such as "AM done, TAM pending, SA done", computed from the stored team and
   the Account's current members and pending invitations.

## Non-goals

- Writing to SFDC.
- Syncing TTOBAK membership back to the SFDC account team.
- Removing members who left the SFDC team. The owner does that by hand.
- Inviting customer contacts.
- Opportunity teams. Only the Account team is in scope.

## Anti-goals

- Inviting anyone without an explicit click by the owner. There are no
  automatic invitations on sync.
- Granting Owner to anyone through this flow. Only the existing creation rule
  makes the creator Owner.
- Showing the stored team, or the onboarding summary, to anyone but the owner.
  The Account members see only normal membership.
- Storing team access levels, user IDs, phone numbers or anything beyond
  alias, name, title and the two role fields.

## Constraints

- **Residency.**
  - ADR-023 gains a row: SFDC account team, with alias, name, title,
    `teamMemberRole` and `allTeamRoles`, owner-private, with the same retention
    as assignments (refreshed on each complete sync; removed with the
    assignment).
  - Email is derived as `alias@amazon.com` and never stored separately.
- **Storage.** `USER#{id}` / `PERFTEAM#{sfdcAccountId}#{alias}` rows, synced by
  the same revisioned batch protocol and sync route as plan 1, with
  source `sfdc-account-team`. A snapshot removal applies when a member leaves
  the SFDC team.
- **Membership reads.**
  - The invite list needs the Account's current members and pending
    invitations. Use the existing member-list read, which already requires the
    caller's direct membership. The owner must be able to read the Account, or
    the list is empty and the queue says so.
  - Never expose another Account's members.
- **Limits.**
  - At most 100 team members per SFDC account are stored. The rest are counted.
  - The invite action sends at most 20 invitations per click.
- **Existing flows.** Reuse the existing create and add-member flows and UI
  components (`AccountsClient` creation and the member add form). Do not
  duplicate their validation.

## Left to the builder

- The exact UI composition of the create and invite steps. A modal or an inline
  panel are both fine.
- Whether the onboarding summary is computed on the server
  (`GET /api/performance/onboarding`) or the client. Prefer the server for
  bounded reads.
- Paging of `get_team_members` (limit 500; follow `cursor`).

## Verification

- **Unit:**
  - role mapping, using the real role strings seen 2026-11-10, including a
    Migration BDM labelled Account Manager;
  - the residency filter drops access levels and user IDs;
  - the invite list excludes current members and pending invitees;
  - a non-owner gets nothing.
- **Manual, on the owner's Mac:**
  - run `intake sfdc`;
  - create one missing Account from the queue;
  - invite its AM and TAM;
  - confirm the pending invitations appear under the Account's members, and
    that the queue summary updates.
