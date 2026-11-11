# Account Entity Resolution and People Edges Implementation Plan

Status: historical implementation plan (2026-11-09); not a current reference.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Free-text people mentions in account meetings (participants, speaker
names, action item assignees, insight entities) resolve deterministically to
existing members, contacts, sub-accounts and projects. The results are stored as
typed, provenance-bearing account link rows that the people panel, graph and
flow read back under the existing access rules.

**Architecture:**
- People edges reuse the `LINK#`/`LINKTO#`/`LINKDENY#` account-partition rows.
  The forward key is `LINK#meeting#{meetingLinkId}#{toKind}#{toId}#{role}`, a
  5-segment key next to the 4-segment project 1 keys.
- A pure resolver (normalizer + rules) plans each meeting's auto edge set and
  its unresolved mentions.
- A runner applies the plan:
  - it writes missing auto edges;
  - it removes its own stale auto edges;
  - it replaces `MENTION#` rows for unresolved mentions.
- The summarize Lambda runs it inline after summary publication. Speaker map and
  contact changes reach it through a new `PeopleResolveRequested` EventBridge
  event. A CLI backfills old meetings.
- New authenticated, non-delegable API routes serve the people panel, mention
  review and speaker binding. The graph and flow read the same rows.

**Tech Stack:** Go 1.27 (chi, aws-sdk-go-v2 DynamoDB expression builder,
`golang.org/x/text` for NFKC and case folding, stdlib tests), CDK TypeScript
(jest), Next.js 16 / React 19 / Tailwind 4 (node:test unit scripts).

**Spec:** `docs/superpowers/specs/2026-11-09-account-entity-resolution-design.md`

## Global Constraints

- Handler → service → repository; DynamoDB expression builder only.
- Sentinel errors via `errors.Is`: `service.ErrForbidden`, `service.ErrNotFound`,
  `service.ErrInvalidInput`, `service.ErrConflict`,
  `repository.ErrConditionFailed` and the project 1 link errors
  (`repository.ErrAccountLinkExists|Target|Denied|Access`).
- API error shape `{ "error": { "code": "...", "message": "..." } }`.
- A forward row and its reverse row change together in one `TransactWriteItems`.
- Authorization:
  - All new routes are authenticated and registered inside the existing
    `AuthWithConnectorGrants` group in `backend/cmd/api/main.go`. They are
    `delegationAllowed: false` in `contracts/connector/v1/operations.json`.
    No new unauthenticated route.
  - The viewer is a *direct* account member (`repo.GetMember`), checked on every
    request and never cached.
  - A meeting-side row is shown only when the meeting is currently published to
    the account (`meetingPublishedToAccount` on a strong read).
  - Contact labels come from the sealed contact row at read time. Link and
    mention rows never copy contact fields; unreadable contacts are skipped.
  - Text from summary-derived fields (`actionItems`, `insights`) is hidden when
    `accountMemberNeedsSummaryGuard(member) && !meeting.AccountSummaryMetadataShareable()`
    and the viewer is not the meeting owner. This is the project 1 evidence rule.
- Resolver (spec E3):
  - No model call.
  - Rules, in order: email, then name, then (insight entities only) alias or
    project.
  - The first rule with exactly one candidate wins. Several candidates at any
    rule stop resolution, so a later rule never breaks a tie.
  - Never store a guess.
- Values copied from the spec:
  - Roles: `participant`, `speaker`, `assignee`, `mentions`.
  - Origins: `auto`, `manual`.
  - Rules: `email`, `name`, `alias`, `project`.
  - Mention text is at most 120 code points.
  - Per meeting: ≤ 50 mentions considered, ≤ 100 edges written.
  - People panel: ≤ 200 resolved people, ≤ 100 unresolved groups, with a cursor
    for more.
  - Graph caps stay at 150 nodes and 300 edges, with `Truncated`.
  - Contacts per resolver run: ≤ 2,000, and the run result records `truncated`.
  - Contact-triggered backfill covers the last 90 days and ≤ 200 meetings.
- Manual wins:
  - Auto never overwrites a manual edge.
  - Auto never re-creates an edge that has a deny row.
  - Auto never re-lists a mention the user resolved or ignored.
  - A user removing an auto edge writes a deny row.
  - The runner's own stale-edge cleanup writes no deny row.
- Logs carry IDs, counts and codes only. They never contain mention text, names,
  emails or contact fields.
- Docs are English. The Korean honorific list is data in Go
  (`peopleHonorificSuffixes`); docs refer to the symbol. UI copy is Korean,
  defined as exported constants and asserted by tests.
- Go is not installed locally. Run Go checks on CI (`test`) or the build host:
  `(cd backend && go test ./... -count=1)`, `(cd backend && go vet ./...)`.
- No `cdk deploy` in this plan. Deploy order when released: summarize consumer +
  rule + DLQ first, then the API publishers and routes, then the frontend, then
  the backfill dry run, then the real backfill (requires user authorization).

## Delivery parts (stacked PRs, each diff ≤ 95,000 bytes)

Each part branches from the previous one. Docs-only PRs cannot merge, so the
spec and this plan ship with part 1 code.

| Part | Tasks | Content |
|---|---|---|
| 1 | spec + plan, Task 1 | People link model, keys, validation |
| 2 | Tasks 2–3 | Pure normalizer/resolver; people link repository + project 1 listing range fix |
| 3 | Tasks 4–5 | Mention/ignore/run rows; resolver runner service |
| 4 | Tasks 6–7 | Summarize hook, `PeopleResolveRequested` consumer + CDK rule/DLQ + INFRA-SPEC; backfill CLI |
| 5 | Tasks 8–10 | People read/write services, publishers, handlers, routes, connector contract, API-SPEC route inventory |
| 6 | Tasks 11–12 | Graph people edges and member-node change; flow people |
| 7 | Tasks 13–16 | Frontend people panel, speaker binding, graph roles; ADR-067 and doc updates |

If a part's diff exceeds 95,000 bytes, split it at the task boundary inside it
(for example part 5 into Tasks 8–9 and Task 10). Never split inside a task.
Part 1 carries about 75,000 bytes of spec and plan, so Task 1 must stay under
about 15,000 bytes of code and tests. If it does not fit, move the
`UnresolvedMention`, `PeopleRun` and `MentionHash` additions to Task 4.

## Review Focus

1. **People rows starve project 1 listings.** `LINK#meeting#` sorts between
   `LINK#event#` and `LINK#news#`. Without a fix, people rows would fill
   `GET /links` pages and the graph's 1,000-row link budget, and a people key
   used as a cursor would be rejected. Task 3:
   `TestListAccountLinks_SkipsPeopleRange` and
   `TestListAccountLinks_CursorAcrossPeopleRange`.
2. **A member and a contact share a name, or two contacts do.** The mention
   stays unresolved and nothing is written. Task 2:
   `TestResolveMention_AmbiguousAcrossMemberAndContact` and
   `TestResolveMention_AmbiguityStopsLaterRules`.
3. **A teammate under the summary-privacy guard opens the people panel.**
   Assignee and insight mention text and evidence are hidden; participant and
   speaker rows still show. Task 8:
   `TestPeopleMentions_HidesSummaryDerivedTextUnderGuard` and
   `TestPeopleEdges_BlanksEvidenceUnderGuard`.
4. **A user resolves or ignores a mention, or deletes an auto edge, and the
   meeting is re-summarized.** None of it comes back. Task 5:
   `TestResolveMeeting_ManualResolutionNotRelisted`,
   `TestResolveMeeting_IgnoredMentionSkipped` and
   `TestResolveMeeting_RespectsDeny`.
5. **A speaker save and a summary run hit the same meeting at once.** One run
   holds the lease and the other marks a rerun, so the final edges match the
   newest meeting fields. Tasks 4–5: `TestClaimPeopleRun_BusySetsRerun` and
   `TestResolveMeeting_RerunsWhenRequested`.

---

### Task 1: People link model, keys and validation (part 1)

**Files:**
- Create: `backend/internal/model/account_people.go`
- Modify: `backend/internal/model/account_link.go` (doc comment only: point to the people key grammar)
- Test: `backend/internal/model/account_people_test.go`

**Interfaces:**
- Produces:
  ```go
  const (
      LinkKindMember  = "member"
      LinkKindContact = "contact"
      LinkKindAccount = "account"
      LinkKindMention = "mention" // deny-only: LINKDENY#mention#{hash}

      PeopleRoleParticipant = "participant"
      PeopleRoleSpeaker     = "speaker"
      PeopleRoleAssignee    = "assignee"
      PeopleRoleMentions    = "mentions"

      PeopleRuleEmail, PeopleRuleName, PeopleRuleAlias, PeopleRuleProject = "email", "name", "alias", "project"

      PeopleSourceParticipants = "participants"
      PeopleSourceSpeakerMap   = "speakerMap"
      PeopleSourceActionItems  = "actionItems"
      PeopleSourceInsights     = "insights"

      PrefixAccountMention   = "MENTION#"   // ACCOUNT#{a} / MENTION#{hash}#{meetingLinkId}#{role}
      PrefixAccountPeopleRun = "PEOPLERUN#" // ACCOUNT#{a} / PEOPLERUN#{meetingLinkId} or PEOPLERUN#account

      MaxPeopleMentionRunes   = 120
      MaxPeopleEvidenceRunes  = 200
      MaxPeopleSourceRefBytes = 200
      MaxPeopleMentionsPerMeeting = 50
      MaxPeopleEdgesPerMeeting    = 100
  )
  type PeopleLinkKey struct { MeetingID, ToKind, ToID, Role string } // MeetingID is {ownerUserId}:{meetingId}
  type PeopleLink struct {
      AccountID   string `dynamodbav:"accountId" json:"-"`
      MeetingID   string `dynamodbav:"meetingId" json:"meetingId"`
      ToKind      string `dynamodbav:"toKind" json:"toKind"`
      ToID        string `dynamodbav:"toId" json:"toId"`
      Role        string `dynamodbav:"role" json:"role"`
      Origin      string `dynamodbav:"origin" json:"origin"`
      Rule        string `dynamodbav:"rule,omitempty" json:"rule,omitempty"`
      SourceField string `dynamodbav:"sourceField" json:"sourceField"`
      Mention     string `dynamodbav:"mention,omitempty" json:"mention,omitempty"`
      SourceRef   string `dynamodbav:"sourceRef,omitempty" json:"sourceRef,omitempty"`
      Evidence    string `dynamodbav:"evidence,omitempty" json:"evidence,omitempty"`
      CreatedBy   string `dynamodbav:"createdBy,omitempty" json:"createdBy,omitempty"`
      CreatedAt   string `dynamodbav:"createdAt" json:"createdAt"`
  }
  func (l PeopleLink) Key() PeopleLinkKey
  func ValidatePeopleLinkKey(k PeopleLinkKey) error // returns ErrInvalidAccountLink
  func PeopleLinkSortKey(k PeopleLinkKey) string     // LINK#meeting#{m}#{toKind}#{toId}#{role}
  func PeopleLinkToSortKey(k PeopleLinkKey) string   // LINKTO#{toKind}#{toId}#meeting#{m}#{role}
  func PeopleLinkDenySortKey(k PeopleLinkKey) string // LINKDENY#meeting#{m}#{toKind}#{toId}#{role}
  func ParsePeopleLinkSortKey(sk string) (PeopleLinkKey, bool)
  func ParsePeopleLinkToSortKey(sk string) (PeopleLinkKey, bool)
  func IsPeopleLinkSortKey(sk string) bool // LINK#meeting# prefix
  func MentionHash(normalized string) string // first 32 lowercase hex of sha256(normalized)
  func MentionSortKey(hash, meetingID, role string) string // MENTION#{hash}#{m}#{role}
  func MentionIgnoreSortKey(hash string) string           // LINKDENY#mention#{hash}
  type UnresolvedMention struct {
      AccountID, Hash, MeetingID, Role, SourceField, Mention, SourceRef, CreatedAt string // dynamodbav/json camelCase
  }
  type PeopleRun struct {
      AccountID, Scope string // Scope: meetingLinkId or "account"
      Mentions   []string     // MENTION# sort keys written by the last finished run
      LeaseToken string
      LeaseUntil int64        // unix seconds
      Rerun      bool
      RunAt      string
      Resolved, Unresolved int
      Truncated  bool
  }
  ```
- Validation rules:
  - The meeting ID has the form `{owner}:{meetingId}`, with both parts
    satisfying `validAccountLinkID` and neither containing `:`.
  - `ToID` satisfies `validAccountLinkID` and contains no `:`.
  - Allowed role → kinds:
    - `participant`, `speaker`, `assignee` → `member`, `contact`;
    - `mentions` → `member`, `contact`, `account`, `project`.
  - The project 1 `ValidateAccountLink` and `allowedAccountLinkPairs` are
    unchanged. `meeting→project` stays rejected for generic links.

- [ ] **Step 1: Write failing tests**
  - `TestValidatePeopleLinkKey`, table-driven:
    - `speaker→contact` ok;
    - `mentions→project` ok;
    - `speaker→project` rejected;
    - `participant→account` rejected;
    - an unknown role rejected;
    - `#` in any ID rejected;
    - a meeting ID without `:` rejected;
    - a `ToID` with `:` rejected.
  - `TestPeopleLinkSortKeys_RoundTrip`: forward, reverse and deny keys for
    `{MeetingID:"u1:m1", ToKind:"contact", ToID:"c1", Role:"speaker"}` equal
    `LINK#meeting#u1:m1#contact#c1#speaker`,
    `LINKTO#contact#c1#meeting#u1:m1#speaker` and
    `LINKDENY#meeting#u1:m1#contact#c1#speaker`. Both parsers return the key.
  - `TestParseAccountLinkSortKey_RejectsPeopleKey`: the project 1 parser returns
    `ok=false` for a 5-segment people key, and `ParsePeopleLinkSortKey` returns
    `ok=false` for a 4-segment project 1 key.
  - `TestMentionHash_StableAnd32Hex`: the same input gives the same value,
    `len == 32`, and the value matches `^[0-9a-f]{32}$`.
- [ ] **Step 2: Run** `(cd backend && go test ./internal/model/ -run 'People|Mention|ParseAccountLink' -count=1)`. Expected: FAIL (undefined).
- [ ] **Step 3: Implement** in `account_people.go`. Reuse `validAccountLinkID`.
- [ ] **Step 4: Run** the same command. Expected: PASS.
- [ ] **Step 5: Commit** spec, plan and Task 1: `feat(accounts): people link keys and validation`

### Task 2: Name normalization and deterministic resolver (part 2)

**Files:**
- Create: `backend/internal/service/account_people_resolve.go` (pure; no repo access)
- Test: `backend/internal/service/account_people_resolve_test.go`

**Interfaces:**
- Consumes: Task 1 constants and types.
- Produces:
  ```go
  // peopleHonorificSuffixes: exactly these 18 strings, longest-first matching.
  var peopleHonorificSuffixes = []string{ /* see code points below */ }
  func NormalizePersonName(s string) string             // NFKC, trim, collapse whitespace runs to one space, cases.Fold
  func StripHonorific(normalized string) (string, bool) // strip one trailing suffix, with or without a preceding space; false when nothing stripped or the remainder would be empty
  type PersonCandidate struct { Kind, ID, Name, Email string } // Kind member|contact
  type NamedCandidate struct { Kind, ID, Name string; Aliases []string } // Kind account|project
  type PeopleDirectory struct {
      People   []PersonCandidate // members (profile name, falling back to email) and contacts
      Entities []NamedCandidate  // descendant accounts and projects listing the account
      ContactsTruncated bool
  }
  type PeopleMention struct {
      Text, Role, SourceField, SourceRef, Evidence string
      AllowEntities bool // true only for insight entities (rule 3)
  }
  type MentionResolution struct { Kind, ID, Rule string; Resolved bool; Normalized string }
  func ResolveMention(dir *PeopleDirectory, m PeopleMention) MentionResolution
  func CollectMeetingMentions(meeting *model.Meeting) (mentions []PeopleMention, truncated bool)
  func PlanMeetingPeople(accountID, meetingLinkID string, dir *PeopleDirectory, mentions []PeopleMention, now time.Time) (edges []model.PeopleLink, unresolved []model.UnresolvedMention, truncated bool)
  ```
- Honorific code points (one string per entry):
  - one syllable: `U+B2D8`, `U+C528`;
  - two syllables: `U+D300 U+C7A5`, `U+D504 U+B85C`, `U+CC45 U+C784`,
    `U+C218 U+C11D`, `U+C120 U+C784`, `U+B300 U+B9AC`, `U+ACFC U+C7A5`,
    `U+CC28 U+C7A5`, `U+BD80 U+C7A5`, `U+C774 U+C0AC`, `U+C0C1 U+BB34`,
    `U+C804 U+BB34`, `U+B300 U+D45C`;
  - three syllables: `U+B9E4 U+B2C8 U+C800`, `U+D300 U+C7A5 U+B2D8`;
  - four syllables: `U+B9E4 U+B2C8 U+C800 U+B2D8`.
  - Write them as Hangul literals in Go (not `\u` escapes). The test asserts the
    code points.
- `ResolveMention` algorithm:
  1. Email rule: if the text contains exactly one email address
     (`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`), compare it
     case-insensitively with `People[].Email`. One match → `email`. Several →
     unresolved (stop).
  2. Name rule: take the text with the email and any `<>()` removed and
     normalize it. Compare with `NormalizePersonName(People[].Name)`. Exactly
     one → `name`. Several → stop. Zero → retry once with `StripHonorific` of
     the mention; candidate names are never stripped.
  3. Only when `AllowEntities` is true: compare the normalized mention with each
     entity's normalized name and aliases. One account → `alias`; one project →
     `project`. Several (any mix) → unresolved.
  - Distinct candidates are counted by `(Kind, ID)`. The same person reachable
    through two names counts once.
- `CollectMeetingMentions` order and rules:
  - Sources, in order:
    1. `Participants`;
    2. `SpeakerMap` values sorted by label, skipping empty values and default
       labels (`spk_N` and namespaced variants);
    3. `ActionItems` JSON `[]ActionItem` `.Assignee`, with `SourceRef` = item ID;
    4. `Insights` JSON `[]MeetingInsight` `.Entities`, with `SourceRef` =
       insight ID, `Evidence` = `type + ": " + text` capped at 200 code points,
       and `AllowEntities=true`.
  - Text is trimmed and capped at 120 code points. Empty text, or text with
    control or `Cf` characters, is skipped.
  - Invalid JSON in a field skips that field only.
  - The first 50 mentions are kept, and `truncated` reports more.
- `PlanMeetingPeople`:
  - One edge per `PeopleLinkKey`. The first mention wins its `Mention`,
    `SourceRef`, `Evidence` and `Rule`.
  - `Origin=auto` and `CreatedAt=now` (RFC 3339).
  - At most 100 edges, and `truncated` reports more.
  - Unresolved mentions are deduplicated by `(hash, role)` with
    `Hash = MentionHash(normalized)`.

- [ ] **Step 1: Write failing tests**
  - `TestNormalizePersonName`, table-driven:
    - a full-width `ＫＩＭ  Min` gives `kim min`;
    - leading and trailing spaces are trimmed;
    - NFKC compatibility characters are folded.
  - `TestStripHonorific`:
    - each of the 18 suffixes after a name, with and without a space, is
      stripped;
    - the four-syllable suffix wins over its three-syllable prefix;
    - a bare suffix alone gives `ok=false`;
    - only one suffix is stripped when two are stacked.
  - `TestHonorificSuffixesCodePoints`: the list has 18 entries equal to the code
    points above.
  - `TestResolveMention_EmailRule`: `"Kim <KIM@x.com>"` resolves to the contact
    with `kim@x.com` and rule `email`.
  - `TestResolveMention_NameRuleWithHonorific`: a contact name plus the
    `U+B2D8` suffix resolves with rule `name`.
  - `TestResolveMention_AmbiguousAcrossMemberAndContact`: a member and a contact
    with the same name give `Resolved=false`.
  - `TestResolveMention_AmbiguityStopsLaterRules`: two people share the name and
    one account has it as an alias, with `AllowEntities=true`. The result is
    unresolved.
  - `TestResolveMention_EntitiesOnlyForInsights`: an account alias match with
    `AllowEntities=false` gives unresolved.
  - `TestResolveMention_AliasAndProject`: one alias gives `alias`; one project
    name gives `project`; an account and a project with the same name give
    unresolved.
  - `TestCollectMeetingMentions_OrderAndCaps`:
    - 60 participants give 50 mentions and `truncated=true`;
    - default `spk_3` values are skipped;
    - a 130-rune name is capped at 120;
    - invalid `ActionItems` JSON skips only that field.
  - `TestPlanMeetingPeople_DedupesAndCaps`:
    - the same contact as participant twice gives one edge;
    - the same contact as participant and speaker gives two edges;
    - 120 distinct resolvable mentions give 100 edges and `truncated`.
- [ ] **Step 2: Run** `(cd backend && go test ./internal/service/ -run 'Normalize|Honorific|ResolveMention|CollectMeeting|PlanMeeting' -count=1)`. Expected: FAIL.
- [ ] **Step 3: Implement.** Use `golang.org/x/text/unicode/norm` and `golang.org/x/text/cases` (`cases.Fold()`).
- [ ] **Step 4: Run** the same command. Expected: PASS.
- [ ] **Step 5: Commit** `feat(accounts): deterministic people mention resolver`

### Task 3: People link repository and project 1 listing range (part 2)

**Files:**
- Create: `backend/internal/repository/account_people.go`
- Modify: `backend/internal/repository/account_links.go` (`ListAccountLinks`, `decodeAccountLinkCursor`)
- Test: `backend/internal/repository/account_people_test.go`, `backend/internal/repository/account_links_test.go` (extend)

**Interfaces:**
- Consumes: Task 1.
- Produces:
  ```go
  func (r *DynamoDBRepository) PutPeopleLink(ctx context.Context, l *model.PeopleLink, actorID string) error
  func (r *DynamoDBRepository) GetPeopleLink(ctx context.Context, accountID string, k model.PeopleLinkKey) (*model.PeopleLink, error)
  func (r *DynamoDBRepository) DeletePeopleLink(ctx context.Context, accountID, actorID string, k model.PeopleLinkKey, expectOrigin string, deny bool) error
  func (r *DynamoDBRepository) ListPeopleLinksForMeeting(ctx context.Context, accountID, meetingID string, maxRows int) ([]model.PeopleLink, bool, error) // LINK#meeting#{m}#
  func (r *DynamoDBRepository) ListPeopleLinksByTarget(ctx context.Context, accountID, toKind, afterSK string, maxRows int) ([]model.PeopleLink, string, error) // LINKTO#{toKind}#; returns last SK read
  func (r *DynamoDBRepository) ListPeopleLinksToTarget(ctx context.Context, accountID, toKind, toID string, maxRows int) ([]model.PeopleLink, bool, error) // LINKTO#{toKind}#{toID}#meeting#
  ```
- `PutPeopleLink` transaction (role names in parentheses feed the shared
  `classifyAccountLinkPut`):
  - Put `LINK#` (`exists`):
    - for auto, the condition is `attribute_not_exists(PK)`;
    - for manual, it is `attribute_not_exists(PK) OR origin = :auto`, so a
      manual write replaces an auto row and never another manual row.
  - Put `LINKTO#` (`reverse`).
  - ConditionCheck that the meeting is published to the account, with the same
    condition as project 1's meeting target (`target`).
  - ConditionCheck that the target exists (`target`):
    - member: `ACCOUNT#{a}/MEMBER#{id}` exists;
    - contact: `ACCOUNT#{a}/CONTACT#{id}` exists;
    - account: `ACCOUNT#{id}/META` exists;
    - project: `PROJECT#{id}/CONFIG contains(accountIds, :a)`.
  - Auto only: ConditionCheck that `LINKDENY#` is absent (`deny`).
  - `accountLinkAccessChecks(accountID, actorID)` (`access`).
  - Manual requires a non-empty `actorID` and `CreatedBy == actorID`; auto
    requires `actorID == ""`.
- `DeletePeopleLink`:
  - Delete `LINK#` with `attribute_exists(PK) AND origin = :expectOrigin`, and
    delete `LINKTO#`.
  - When `deny` is set, Put the `LINKDENY#` row.
  - Run the access checks.
  - The system cleanup passes `actorID == ""`, which is allowed only with
    `expectOrigin == auto && !deny`; in that case only the META check runs.
  - Errors map like `DeleteAccountLink`.
- `ListAccountLinks` (project 1) queries two ranges in order and never returns
  or counts `LINK#meeting#` rows:
  - first `SK BETWEEN "LINK#" AND "LINK#meeting"`;
  - then `SK BETWEEN "LINK#meeting$" AND "LINK$"`.
  - The cursor stays the last 4-segment SK, and decoding picks its range.
  - A people key as a cursor stays `ErrInvalidAccountLinkCursor`, which never
    happens because people keys are never returned.
- Decoders take endpoints from the sort key and drop rows with an invalid key
  or origin.

- [ ] **Step 1: Write failing tests** (fake-client pattern from `account_links_test.go`)
  - `TestPutPeopleLink_AutoTransactionShape`: items are
    `[Put LINK#, Put LINKTO#, CC meeting, CC MEMBER#target, CC LINKDENY#, CC META]`.
    Assert the `LINK#` condition is `attribute_not_exists`.
  - `TestPutPeopleLink_ManualReplacesAutoOnly`: the condition contains
    `origin = :v` with `auto`, and there are CC `META` and `MEMBER#{actor}` items.
  - `TestPutPeopleLink_ProjectTargetUsesAccountIds` and
    `TestPutPeopleLink_ContactTargetChecksContactRow`.
  - `TestPutPeopleLink_ClassifiesDenied`: a cancellation with
    `ConditionalCheckFailed` at the deny index gives
    `errors.Is(err, ErrAccountLinkDenied)`.
  - `TestDeletePeopleLink_SystemCleanupNoDeny`: the actor is `""`, auto,
    deny=false. The transaction has no `LINKDENY#` put and no member check.
    `actorID==""` with `deny=true` gives an error before any call.
  - `TestListAccountLinks_SkipsPeopleRange`: the first query's key condition is
    `BETWEEN LINK# AND LINK#meeting`. When it ends, a second query uses
    `LINK#meeting$`..`LINK$`.
  - `TestListAccountLinks_CursorAcrossPeopleRange`: a cursor whose SK is
    `LINK#event#e1#project#p1` resumes in range 1, and `LINK#news#...` resumes
    in range 2.
  - `TestListPeopleLinksByTarget_ResumesAfterSK`: `afterSK` is passed as the
    `BETWEEN` lower bound (exclusive by skipping an equal first row), and the
    returned last SK equals the final item's SK.
- [ ] **Step 2: Run** `(cd backend && go test ./internal/repository/ -run 'PeopleLink|ListAccountLinks' -count=1)`. Expected: FAIL.
- [ ] **Step 3: Implement.** Share the transaction-check helper with `PutAccountLink` instead of copying it.
- [ ] **Step 4: Run** the backend tests and `go vet`. Expected: PASS, including the existing project 1 link, graph and flow tests.
- [ ] **Step 5: Commit** `feat(accounts): people link rows with reverse rows`

### Task 4: Mention, ignore and run rows (part 3)

**Files:**
- Modify: `backend/internal/repository/account_people.go`
- Test: `backend/internal/repository/account_people_test.go`

**Interfaces:**
- Produces:
  ```go
  func (r *DynamoDBRepository) PutMentionIgnore(ctx context.Context, accountID, actorID, hash string) error // Put LINKDENY#mention#{hash} + META + MEMBER#{actor} checks
  func (r *DynamoDBRepository) GetMentionIgnores(ctx context.Context, accountID string, hashes []string) (map[string]bool, error) // BatchGet, ≤100 per call, retries unprocessed keys
  func (r *DynamoDBRepository) ReplaceUnresolvedMentions(ctx context.Context, accountID, meetingID string, rows []model.UnresolvedMention, prior []string) error
  func (r *DynamoDBRepository) ListUnresolvedMentions(ctx context.Context, accountID, afterSK string, maxRows int) ([]model.UnresolvedMention, string, error) // MENTION# in SK order
  func (r *DynamoDBRepository) ListUnresolvedMentionsByHash(ctx context.Context, accountID, hash string, maxRows int) ([]model.UnresolvedMention, bool, error)
  func (r *DynamoDBRepository) DeleteUnresolvedMention(ctx context.Context, accountID, hash, meetingID, role string) error
  func (r *DynamoDBRepository) ClaimPeopleRun(ctx context.Context, accountID, scope, token string, now time.Time, lease time.Duration) (*model.PeopleRun, bool, error)
  func (r *DynamoDBRepository) FinishPeopleRun(ctx context.Context, run *model.PeopleRun) (rerun bool, err error)
  ```
- `ReplaceUnresolvedMentions`:
  - Puts every row.
  - Deletes each `prior` SK that is not among the new rows' SKs.
  - Uses BatchWrite in chunks of 25, retrying unprocessed items up to 3 times,
    then returns an error.
  - `prior` comes from the run row, so no scan is needed.
- `ClaimPeopleRun` is a conditional `UpdateItem` on `PEOPLERUN#{scope}`:
  - The condition is
    `attribute_not_exists(leaseUntil) OR leaseUntil < :now OR leaseToken = :token`.
  - It sets `leaseToken`, `leaseUntil = now+lease` and `rerun = false`, and
    returns the stored row, including the prior `Mentions`.
  - On condition failure it runs a second unconditional
    `SET rerun = :true` and returns `claimed=false`.
- `FinishPeopleRun` uses the condition `leaseToken = :token`:
  - It sets `mentions`, `runAt`, the counts and `truncated`, and removes
    `leaseUntil` and `leaseToken`.
  - It returns the `rerun` value read with `ReturnValues: ALL_OLD`.
  - On condition failure it returns `ErrConditionFailed`.

- [ ] **Step 1: Write failing tests**
  - `TestPutMentionIgnore_RequiresMember`: CC `MEMBER#{actor}` is present, and
    an empty actor gives an error.
  - `TestReplaceUnresolvedMentions_DeletesOnlyStale`: prior `[A, B]` and new
    `[B, C]` give a put of B and C and a delete of A only.
  - `TestClaimPeopleRun_ClaimsFreeLease`: the condition expression and the
    returned prior `Mentions`.
  - `TestClaimPeopleRun_BusySetsRerun`: the first update fails its condition.
    A second update sets `rerun`, and the result is `claimed=false`.
  - `TestFinishPeopleRun_ReportsRerun`: `ALL_OLD` with `rerun=true` gives
    `true`, and a token mismatch gives `ErrConditionFailed`.
  - `TestListUnresolvedMentions_GroupsContiguous`: the query uses the
    `MENTION#` prefix with `afterSK` as the lower bound, and rows decode the
    hash, meeting and role from the SK.
- [ ] **Step 2: Run** `(cd backend && go test ./internal/repository/ -run 'Mention|PeopleRun' -count=1)`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run.** Expected: PASS.
- [ ] **Step 5: Commit** `feat(accounts): unresolved mention, ignore and run rows`

### Task 5: Resolver runner service (part 3)

**Files:**
- Create: `backend/internal/service/account_people.go`
- Test: `backend/internal/service/account_people_test.go`

**Interfaces:**
- Consumes: Tasks 2–4.
- Produces:
  ```go
  type accountPeopleRepo interface { /* GetAccount, GetMember, GetMeeting, GetUserByID, ListAccountMembers,
      ListAccountContacts, GetAccountContact, ListChildAccountIDsPage, ListProjectRefsForAccount, GetProject,
      ListMeetingRefsForAccountPage, GetMeetingActionState, and Tasks 3–4 methods */ }
  type PeopleRunOptions struct { DryRun bool }
  type PeopleRunResult struct { Meetings, Mentions, Resolved, Unresolved, Written, Removed, Skipped int; Busy, Truncated bool }
  func (r PeopleRunResult) String() string // counts and flags only
  type AccountPeopleService struct { repo accountPeopleRepo; now func() time.Time; newToken func() string }
  func NewAccountPeopleService(repo accountPeopleRepo) *AccountPeopleService
  func (s *AccountPeopleService) ResolveMeeting(ctx context.Context, ownerID, meetingID string, opt PeopleRunOptions) (PeopleRunResult, error)
  func (s *AccountPeopleService) ResolveAccount(ctx context.Context, accountID string, since time.Time, maxMeetings int, opt PeopleRunOptions) (PeopleRunResult, error)
  func (s *AccountPeopleService) loadDirectory(ctx context.Context, accountID string) (*PeopleDirectory, error)
  const (
      peopleRunLease          = 2 * time.Minute
      maxPeopleContactsPerRun = 2000
      maxPeopleDescendants    = 200
      peopleBackfillWindow    = 90 * 24 * time.Hour
      maxPeopleBackfillMeetings = 200
      maxPeopleRunPasses      = 2
  )
  ```
- `loadDirectory`:
  - Members come from `ListAccountMembers`, keeping only rows that are
    canonical for the account. Each label is the profile `Name`
    (`GetUserByID`), falling back to the member email; the email is the member
    email.
  - Contacts come from `ListAccountContacts` pages, up to 2,000, with
    `ContactsTruncated` when more remain.
  - Entities:
    - descendant accounts, read breadth-first through
      `ListChildAccountIDsPage` (≤ 200) and `GetAccount` (name and aliases),
      excluding this account;
    - projects from `ListProjectRefsForAccount`, revalidated by `GetProject`
      with `contains(AccountIDs, accountID)`.
- `ResolveMeeting` flow:
  1. `GetMeeting`. A meeting that is not published (`SharedToAccount` and
     `AccountID`) returns `Skipped=1` and writes nothing.
  2. `ClaimPeopleRun(scope = owner:meeting)`. When it is not claimed, return
     `Busy=true` and nil.
  3. Load the directory, collect the mentions and drop those whose hash
     `GetMentionIgnores` reports.
  4. Read the existing edges with `ListPeopleLinksForMeeting` (≤ 300).
  5. Drop unresolved mentions that a manual edge already covers: a manual edge
     with the same role whose `MentionHash(NormalizePersonName(Mention))` equals
     the mention's hash.
  6. Plan the edges.
  7. Unless `DryRun`:
     - Put each planned edge that has no existing row, skipping
       `ErrAccountLinkExists` and `ErrAccountLinkDenied`.
     - Delete with system cleanup each existing auto edge that is not in the
       plan.
     - Call `ReplaceUnresolvedMentions(prior = run.Mentions)`.
     - Call `FinishPeopleRun`.
  8. When `FinishPeopleRun` reports `rerun`, repeat from step 1, at most
     `maxPeopleRunPasses` passes in total.
  - `ErrAccountLinkTarget` on one edge (a contact deleted mid-run) counts as
    `Skipped` and does not fail the run.
- `ResolveAccount`:
  - It claims `PEOPLERUN#account` the same way, so busy calls set rerun.
  - It loads the directory once.
  - It pages `ListMeetingRefsForAccountPage` newest first, stops at refs older
    than `since` or after `maxMeetings`, and runs the per-meeting steps with the
    shared directory.
  - It honours `ctx` deadlines: when the context ends it stops early with
    `Truncated=true` and returns a nil error.

- [ ] **Step 1: Write failing tests** (mock repo, table-driven where natural)
  - `TestResolveMeeting_WritesAutoEdgesWithProvenance`: a participant email
    matching a contact gives a written edge with `Origin=auto`, `Rule=email`,
    `SourceField=participants` and `Mention` equal to the text.
  - `TestResolveMeeting_RemovesOnlyOwnStaleAutoEdges`: existing edges are auto
    `speaker→c1` (no longer produced) and manual `speaker→c2`. Only c1 is
    deleted, with `actorID==""` and no deny.
  - `TestResolveMeeting_RespectsDeny`: the put returns `ErrAccountLinkDenied`.
    No error results and `Skipped` increases.
  - `TestResolveMeeting_ManualResolutionNotRelisted`: an unresolved mention text
    equals the `Mention` of a manual edge with the same role. No
    `UnresolvedMention` row is written for it.
  - `TestResolveMeeting_IgnoredMentionSkipped`: an ignored hash gives no
    unresolved row and no edge.
  - `TestResolveMeeting_UnpublishedSkipped`: `SharedToAccount=false` gives
    `Skipped=1` and no claim.
  - `TestResolveMeeting_BusyReturnsBusy` and
    `TestResolveMeeting_RerunsWhenRequested`: a finish returning `rerun=true`
    gives exactly 2 passes; a second rerun does not give a third.
  - `TestResolveMeeting_DryRunWritesNothing`.
  - `TestLoadDirectory_ContactsCappedAt2000`: 2,001 contacts give 2,000 and
    `ContactsTruncated`.
  - `TestLoadDirectory_ExcludesProjectNoLongerInAccount`.
  - `TestResolveAccount_WindowAndCap`: refs at 10, 80 and 100 days with
    `since = now-90d` run 2 meetings; 250 refs inside the window run 200.
- [ ] **Step 2: Run** `(cd backend && go test ./internal/service/ -run 'ResolveMeeting|ResolveAccount|LoadDirectory' -count=1)`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** the full backend tests. Expected: PASS.
- [ ] **Step 5: Commit** `feat(accounts): people resolver runner with lease and refresh`

### Task 6: Summarize hook, resolve event and CDK rule (part 4)

**Files:**
- Create: `backend/internal/model/people_events.go`, `backend/internal/service/account_people_events.go`, `backend/cmd/summarize/account_people_test.go`
- Modify: `backend/cmd/summarize/main.go` (package vars near `linkAccountNews`, wiring near :169, dispatch near :320, `finishSummaryAnalysis` :700)
- Modify: `infra/lib/gateway-stack.ts` (rule + DLQ next to `TermsRequestedRule`; API publish permission if detail-type scoped), `infra/test/gateway-stack.test.ts`, `docs/INFRA-SPEC.md`
- Test: `backend/internal/service/account_people_events_test.go`

**Interfaces:**
- Consumes: `AccountPeopleService` (Task 5).
- Produces:
  ```go
  type PeopleResolveRequested struct {
      AccountID string `json:"accountId"`
      OwnerID   string `json:"ownerId,omitempty"`   // with MeetingID: one meeting
      MeetingID string `json:"meetingId,omitempty"` // empty: account run (90 days, ≤200)
      Reason    string `json:"reason"`              // "speakers" | "contacts"
  }
  func ValidatePeopleResolveRequested(e PeopleResolveRequested) error // IDs via validateAccountHierarchyIDs; OwnerID and MeetingID both set or both empty; known reason
  func PeopleResolvePublisher(client actionEventClient) func(context.Context, model.PeopleResolveRequested) error // Source ttobak.analysis, DetailType PeopleResolveRequested
  // cmd/summarize package vars:
  var resolveMeetingPeople = func(context.Context, *model.Meeting) {}
  var processPeopleResolveRequested = func(context.Context, model.PeopleResolveRequested) error { return nil }
  const peopleResolveTimeout = 30 * time.Second
  ```
- Hook placement and behaviour:
  - `finishSummaryAnalysis` calls `resolveMeetingPeople(ctx, current)` right
    after `linkAccountNews`, under the same
    `SharedToAccount && AccountID != ""` guard, before the privacy early return.
  - The hook has its own `context.WithTimeout(peopleResolveTimeout)`.
  - It logs the meeting ID plus `PeopleRunResult.String()` or an error code,
    and never fails the summary.
- The event branch:
  - Decode and validate, then run `ResolveMeeting` or `ResolveAccount(now-90d, 200)`.
  - Return the error so EventBridge retries.
  - A busy result returns nil, because the running pass reruns.
  - A meeting event whose meeting is not in `AccountID` is skipped.
- CDK:
  - Rule `ttobak-people-resolve-requested` with pattern
    `{source:['ttobak.analysis'], detailType:['PeopleResolveRequested']}`.
  - Target: the summarize function, `maxEventAge` 5 minutes, `retryAttempts` 3,
    DLQ `ttobak-people-resolve-dlq` (SQS-managed SSE, 7 days, `DENY_ALL`
    redrive).
  - `pipelineFailures.watchQueue('PeopleResolve', dlq)`.
  - If the API role's `events:PutEvents` is scoped by detail type, add
    `PeopleResolveRequested`; otherwise change nothing.
  - The summarize role already reads and writes `ttobak-main`. Confirm this in
    Step 1 and record it in the commit message.

- [ ] **Step 1: Read** the summarize and API IAM statements in `gateway-stack.ts` and note the result.
- [ ] **Step 2: Write failing tests**
  - `TestValidatePeopleResolveRequested`, table-driven: owner without meeting
    rejected, unknown reason rejected, `#` in account ID rejected.
  - `TestPeopleResolvePublisher_Envelope`: source, detail type and JSON detail.
  - In `cmd/summarize`:
    - `TestResolveMeetingPeople_RunsAfterNewsLinks`: with both package vars
      stubbed, the order is `linkAccountNews` then `resolveMeetingPeople`.
    - `TestResolveMeetingPeople_SkipsPrivateMeeting`.
    - `TestResolveMeetingPeople_RunsBeforePrivacyEarlyReturn`: modelled on
      `account_news_link_test.go`.
    - `TestDispatch_PeopleResolveRequested`: an envelope reaches
      `processPeopleResolveRequested` with the decoded detail.
  - Infra test
    `people resolve requests reach only summarize through a bounded rule, DLQ and alarm`,
    modelled on the terms test at `gateway-stack.test.ts:876`.
- [ ] **Step 3: Run** `(cd backend && go test ./cmd/summarize/ ./internal/service/ -run 'People' -count=1)` and `(cd infra && npm test -- gateway-stack)`. Expected: FAIL.
- [ ] **Step 4: Implement**, then update `docs/INFRA-SPEC.md` (rule, DLQ, alarm) in the same commit.
- [ ] **Step 5: Run** the backend tests, `(cd infra && npx cdk synth && npm test)` and `python3 scripts/docs/check_docs.py`. Expected: PASS.
- [ ] **Step 6: Commit** `feat(summarize): resolve meeting people after summary and on request`

### Task 7: Backfill CLI (part 4)

**Files:**
- Create: `backend/cmd/backfill-account-people/main.go`, `backend/cmd/backfill-account-people/main_test.go`

**Interfaces:**
- Consumes: `AccountPeopleService.ResolveMeeting` (Task 5),
  `ScanAccountHierarchyPage` (existing operator scan) for `-all`, and
  `ListMeetingRefsForAccountPage`.
- Produces a CLI with the same header doc style as `backfill-account-kb`:
  - `-account ID` or `-all` (exactly one).
  - `-before RFC3339`: only meetings dated strictly before the cutoff. It is
    required in apply mode.
  - `-dry-run`, default true.
  - `-limit N`: meetings per account, default 200.
  - `-prune`: delete people and mention rows of meetings whose row no longer
    exists (`GetMeeting` is nil), through the system cleanup. Manual rows are
    included only in this case.
  - Env `TABLE_NAME`, with no fallback.
  - Output is JSON counts only (`accounts`, `meetings`, `written`, `removed`,
    `unresolved`, `skipped`, `busy`, `pruned`), with no IDs or text.

- [ ] **Step 1: Write failing tests**
  - `TestBackfill_DryRunWritesNothing`.
  - `TestBackfill_ApplyRequiresBefore`.
  - `TestBackfill_AccountXorAll`.
  - `TestBackfill_BeforeCutoffExcludesNewer`.
  - `TestBackfill_PruneOnlyMissingMeetings`: an unpublished but existing meeting
    keeps its manual edges.
  - `TestBackfill_OutputHasNoIDs`: the output does not contain the test account
    or meeting IDs.
- [ ] **Step 2: Run** `(cd backend && go test ./cmd/backfill-account-people/ -count=1)`. Expected: FAIL.
- [ ] **Step 3: Implement.** Use interfaces for the repo and service, as in `backfill-account-kb`.
- [ ] **Step 4: Run** the full backend tests. Expected: PASS.
- [ ] **Step 5: Commit** `feat(accounts): people resolution backfill CLI`

### Task 8: People read service (part 5)

**Files:**
- Create: `backend/internal/service/account_people_read.go`, `backend/internal/model/account_people_api.go`
- Test: `backend/internal/service/account_people_read_test.go`

**Interfaces:**
- Produces:
  ```go
  type AccountPerson struct {
      Kind     string         `json:"kind"` // member|contact
      ID       string         `json:"id"`
      Label    string         `json:"label"`
      Meetings int            `json:"meetings"` // distinct visible meetings
      Roles    map[string]int `json:"roles"`    // role → visible edge count
  }
  type AccountPeoplePage struct { People []AccountPerson `json:"people"`; NextCursor string `json:"nextCursor,omitempty"`; Truncated bool `json:"truncated"` }
  type MentionGroup struct {
      Key      string   `json:"key"`      // the 32-hex hash
      Text     string   `json:"text"`     // one visible original mention, ≤120 code points
      Meetings int      `json:"meetings"` // visible meetings in this group
      Roles    []string `json:"roles"`
  }
  type MentionGroupsPage struct { Groups []MentionGroup `json:"groups"`; NextCursor string `json:"nextCursor,omitempty"`; Truncated bool `json:"truncated"` }
  type MeetingPeopleEdges struct { Edges []PeopleLink `json:"edges"`; Labels map[string]string `json:"labels"` } // label key "{kind}:{id}"
  func (s *AccountPeopleService) ListPeople(ctx context.Context, userID, accountID, cursor string) (*model.AccountPeoplePage, error)
  func (s *AccountPeopleService) ListMentionGroups(ctx context.Context, userID, accountID, cursor string) (*model.MentionGroupsPage, error)
  func (s *AccountPeopleService) MeetingEdges(ctx context.Context, userID, accountID, meetingID string) (*model.MeetingPeopleEdges, error)
  const ( maxPeoplePerPage = 200; maxMentionGroupsPerPage = 100; maxPeopleRowsPerPage = 2000; maxPeopleMeetingReads = 300 )
  ```
- `ListPeople`:
  - It reads `ListPeopleLinksByTarget` for `member`, then `contact`, up to 2,000
    rows per page.
  - It aggregates by person, counting only edges whose meeting revalidates. The
    publication reads are cached and limited to 300 distinct meetings, after
    which the rest count as unknown and `Truncated` is set.
  - Members must still be direct members (`ListAccountMembers` once). Each
    label is the profile name, falling back to the email.
  - Contacts are hydrated with `GetAccountContact`; nil or unreadable contacts
    are skipped.
  - A page stops at 200 people. The cursor is base64url JSON
    `{a: accountId, k: kind, s: lastSK}`, where `lastSK` is the last row of the
    last complete person. The cursor is rejected when `a` differs, when it is
    longer than 1,024 bytes, or when the SK has the wrong prefix.
- `ListMentionGroups`:
  - It pages `ListUnresolvedMentions`. The groups are contiguous by hash.
  - A row counts only when its meeting revalidates and, for `actionItems` or
    `insights` rows, the summary guard passes.
  - Groups with zero visible rows are omitted.
  - It returns ≤ 100 groups, with a cursor in the same form as `ListPeople`.
- `MeetingEdges`:
  - The meeting must be published, otherwise `ErrNotFound`.
  - It returns all people edges of the meeting.
  - Under the guard, it blanks `Mention` and `Evidence` on rows whose
    `SourceField` is `actionItems` or `insights`.
  - Labels are hydrated as in `ListPeople`, skipping unreadable contacts. An
    account target is labelled only when `resolveAccountReadGrant` returns a
    grant for the viewer. A project target is labelled when it still lists the
    account.

- [ ] **Step 1: Write failing tests**
  - `TestListPeople_CountsOnlyPublishedMeetings`.
  - `TestListPeople_RemovedMemberForbidden`: `GetMember` nil gives
    `ErrForbidden`.
  - `TestListPeople_SkipsDeletedContact`.
  - `TestListPeople_SkipsFormerMember`.
  - `TestListPeople_PageBoundaryKeepsPersonWhole`: 201 people give 200 on
    page 1, and page 2 starts with person 201 with full counts.
  - `TestListPeople_RejectsForeignCursor`.
  - `TestPeopleMentions_HidesSummaryDerivedTextUnderGuard`: a group made only of
    `insights` rows is omitted for a guarded non-owner and shown for the owner.
  - `TestPeopleMentions_UnpublishedMeetingNotCounted`.
  - `TestPeopleEdges_BlanksEvidenceUnderGuard`.
  - `TestPeopleEdges_AccountLabelNeedsReadGrant`.
- [ ] **Step 2: Run** `(cd backend && go test ./internal/service/ -run 'ListPeople|PeopleMentions|PeopleEdges' -count=1)`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run.** Expected: PASS.
- [ ] **Step 5: Commit** `feat(accounts): people and unresolved mention reads`

### Task 9: People write service and resolve publishers (part 5)

**Files:**
- Create: `backend/internal/service/account_people_write.go`
- Modify: `backend/internal/service/meeting.go` (`UpdateSpeakers` :937, publish after a successful save), `backend/internal/service/account_contacts.go` (`Mutate` :137, publish after a successful create or update)
- Test: `backend/internal/service/account_people_write_test.go`, `backend/internal/service/meeting_speakers_people_test.go`, `backend/internal/service/account_contacts_people_test.go`

**Interfaces:**
- Produces:
  ```go
  type PeopleEdgeInput struct { MeetingID, ToKind, ToID, Role, SourceRef string } // json camelCase
  type ResolveMentionInput struct { Kind, ID string }
  type ResolveMentionResult struct { Written int `json:"written"`; Remaining bool `json:"remaining"` }
  func (s *AccountPeopleService) BindEdge(ctx context.Context, userID, accountID string, in model.PeopleEdgeInput) (*model.PeopleLink, error)
  func (s *AccountPeopleService) UnbindEdge(ctx context.Context, userID, accountID string, k model.PeopleLinkKey) error
  func (s *AccountPeopleService) ResolveMentionGroup(ctx context.Context, userID, accountID, hash string, in model.ResolveMentionInput) (*model.ResolveMentionResult, error)
  func (s *AccountPeopleService) IgnoreMentionGroup(ctx context.Context, userID, accountID, hash string) error
  func (s *MeetingService) SetPeopleResolvePublisher(fn func(context.Context, model.PeopleResolveRequested) error)
  func (s *AccountContactsService) SetPeopleResolvePublisher(fn func(context.Context, model.PeopleResolveRequested) error)
  const maxMentionResolveMeetings = 100
  ```
- `BindEdge` (spec E4):
  - The caller is a direct member and the meeting is published, otherwise
    `ErrForbidden` or `ErrNotFound`.
  - The key validates.
  - For `speaker`, `SourceRef` must be a key of `meeting.SpeakerMap` or a
    default label of the meeting. `Mention` is set to the current map value.
  - It writes with `PutPeopleLink(manual, actor=userID)`.
  - `ErrAccountLinkExists` means a manual row exists, and the call returns it
    (idempotent).
  - `ErrAccountLinkTarget` maps to `ErrInvalidInput`, and
    `ErrAccountLinkAccess` to `ErrForbidden`.
- `UnbindEdge`:
  - It reads the row (nil gives `ErrNotFound`).
  - It calls `DeletePeopleLink(actor, origin, deny = origin==auto)` and retries
    once on `ErrConditionFailed`, then returns `ErrConflict`.
- `ResolveMentionGroup` (spec E5 "this is ..."):
  - The person kind is `member` or `contact` and the target exists.
  - It reads `ListUnresolvedMentionsByHash` (≤ 100) and keeps the rows visible
    to the caller, using the same checks as Task 8.
  - For each row, it writes a manual edge (role from the row, with `Mention`,
    `SourceRef` and `SourceField` from the row and an empty `Rule`), then
    deletes the mention row.
  - `Remaining` reports more rows than the bound.
  - Per-row `ErrAccountLinkTarget` (the meeting was unpublished) is skipped.
- `IgnoreMentionGroup`:
  - It calls `PutMentionIgnore`, then deletes the visible mention rows of the
    hash (≤ 100).
  - Later runs skip the hash (Task 5).
- Publishers:
  - `UpdateSpeakers` publishes `{AccountID, OwnerID, MeetingID, Reason:"speakers"}`
    after a successful save, only when the saved meeting is
    `SharedToAccount && AccountID != ""`.
  - `Mutate` publishes `{AccountID, Reason:"contacts"}` after a successful
    create or update.
  - A publish failure is logged with an ID and code, and never fails the
    request.
  - Wiring happens in `cmd/api/main.go` (Task 10).

- [ ] **Step 1: Write failing tests**
  - `TestBindEdge_SpeakerRequiresKnownLabel`.
  - `TestBindEdge_NonMemberForbidden`.
  - `TestBindEdge_ReplacesAutoEdge`: the repo receives manual with
    `CreatedBy=userID`.
  - `TestBindEdge_IdempotentOnManual`.
  - `TestUnbindEdge_AutoWritesDeny` and `TestUnbindEdge_ManualNoDeny`.
  - `TestResolveMentionGroup_WritesManualPerVisibleMeeting`: 3 rows, one of
    them unpublished, give `Written=2`, and the 2 mention rows are deleted.
  - `TestResolveMentionGroup_RejectsProjectKind`.
  - `TestIgnoreMentionGroup_WritesIgnoreAndDeletesRows`.
  - `TestUpdateSpeakers_PublishesForPublishedMeeting` and
    `TestUpdateSpeakers_NoPublishForPrivateMeeting`.
  - `TestUpdateSpeakers_PublishFailureDoesNotFail`.
  - `TestContactCreate_PublishesAccountResolve`.
- [ ] **Step 2: Run** `(cd backend && go test ./internal/service/ -run 'BindEdge|UnbindEdge|MentionGroup|UpdateSpeakers_Publish|UpdateSpeakers_NoPublish|ContactCreate_Publishes' -count=1)`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** the full backend tests. Expected: PASS.
- [ ] **Step 5: Commit** `feat(accounts): speaker binding and mention review writes`

### Task 10: Handlers, routes and connector contract (part 5)

**Files:**
- Create: `backend/internal/handler/account_people.go`
- Modify: `backend/cmd/api/main.go` (routes after `/links` :346; publisher wiring), `contracts/connector/v1/operations.json`, the generated `backend/internal/connector/operations_generated.go`, `mcp-server/src/connector-operations.ts` and `backend/python/qa/connector_operations.py` (via `python3 scripts/connector/generate_contract.py`), `docs/API-SPEC.md` (only the `<!-- BEGIN GO ROUTES -->` inventory in this task)
- Test: `backend/internal/handler/account_people_test.go`

**Interfaces:**
- Routes (all `Cache-Control: no-store`; bodies ≤ 4 KiB via the existing JSON decode helper):

  | Method | Path | Handler | Operation |
  |---|---|---|---|
  | GET | `/api/accounts/{accountId}/people` | `accountPeopleHandler.List` | `api.accountPeople.list` ["read"] |
  | GET | `/api/accounts/{accountId}/people/mentions` | `accountPeopleHandler.Mentions` | `api.accountPeople.mentions` ["read"] |
  | POST | `/api/accounts/{accountId}/people/mentions/{mentionKey}/resolve` | `accountPeopleHandler.ResolveMention` | `api.accountPeople.resolveMention` ["read","write"] |
  | POST | `/api/accounts/{accountId}/people/mentions/{mentionKey}/ignore` | `accountPeopleHandler.IgnoreMention` | `api.accountPeople.ignoreMention` ["read","write"] |
  | GET | `/api/accounts/{accountId}/people/edges` | `accountPeopleHandler.Edges` | `api.accountPeople.edges` ["read"] |
  | POST | `/api/accounts/{accountId}/people/edges` | `accountPeopleHandler.Bind` | `api.accountPeople.bind` ["read","write"] |
  | DELETE | `/api/accounts/{accountId}/people/edges` | `accountPeopleHandler.Unbind` | `api.accountPeople.unbind` ["delete","read"] |

  - Every operation is `delegationAllowed: false`.
  - `GET /people/edges` takes `?meetingId=`, and
    `DELETE /people/edges` takes `?meetingId=&toKind=&toId=&role=`.
  - `{mentionKey}` must match `^[0-9a-f]{32}$`; anything else gives 400.
- Error mapping:
  - `ErrInvalidInput` → 400 `INVALID_INPUT`;
  - `ErrForbidden` → 403;
  - `ErrNotFound` → 404;
  - `ErrConflict` → 409.

- [ ] **Step 1: Write failing tests**
  - `TestAccountPeopleHandler_RoutesAndErrors`, table-driven over the 7
    routes: success status, 403 mapping and `Cache-Control: no-store`.
  - `TestAccountPeopleHandler_RejectsBadMentionKey`.
  - `TestAccountPeopleHandler_UnbindQueryValidation`: a missing `role` gives
    400.
  - The existing `go test ./internal/connectorinventory` must fail until
    `operations.json` lists the routes.
- [ ] **Step 2: Run** `(cd backend && go test ./internal/handler/ ./internal/connectorinventory/ -count=1)`. Expected: FAIL.
- [ ] **Step 3: Implement:**
  - Register the routes.
  - Wire `NewAccountPeopleService(repo)` and `PeopleResolvePublisher(eventsClient)`
    into `MeetingService` and `AccountContactsService`.
  - Add the 7 rows to `operations.json` and run
    `python3 scripts/connector/generate_contract.py`, then
    `python3 scripts/connector/generate_contract.py --check`.
  - Update the API-SPEC route inventory block to match `main.go`.
- [ ] **Step 4: Run:**
  - backend tests and vet;
  - the arm64 api build command from `CLAUDE.md`;
  - `(cd mcp-server && npm test)`;
  - `python3 scripts/docs/check_docs.py`, which must report 192 Go routes (185 + 7).
  Expected: PASS.
- [ ] **Step 5: Commit** `feat(accounts): people panel, mention review and speaker binding routes`

### Task 11: Graph people edges and member nodes (part 6)

**Files:**
- Modify: `backend/internal/service/account_overview.go` (`Graph` steps :1060, replace `graphMembers` :1428), `backend/internal/model/account_overview.go` (graph types :127-175)
- Test: `backend/internal/service/account_overview_graph_test.go` (extend)

**Interfaces:**
- Consumes: `ListPeopleLinksByTarget`, `ListPeopleLinksToTarget` (Task 3), `resolveAccountReadGrant`.
- Produces:
  - `GraphEdge` gains `Role string \`json:"role,omitempty"\``.
  - A new edge kind `GraphEdgePerson = "person"` is used for every people edge,
    with `Role` set to the edge's role.
  - `AccountGraph` gains `MemberCount int \`json:"memberCount"\``, the count of
    canonical direct members.
  - `const maxGraphPeopleRows = 1000`.
- `graphPeople` replaces `graphMembers` as the last step:
  - It reads `LINKTO#member#`, `LINKTO#contact#` and `LINKTO#account#` rows,
    together at most 1,000.
  - For each project node present, it reads `ListPeopleLinksToTarget(project)`
    within the same budget.
  - An edge is added only when its meeting node is already in the graph, which
    `graphMeetings` revalidated, so it adds no meeting reads.
  - Members must still be direct members. Their label is unchanged
    (`email (role)`), and each gets a `member_of` edge.
  - Contacts are hydrated within `maxGraphContactReads` and shared with the
    attendee reads. Unreadable contacts are skipped.
  - An account target needs a read grant for the viewer, and a project target
    must already be a node.
  - Members with no people edge are no longer listed as nodes. Node and edge
    caps and `fitGraph` are unchanged, and any bound sets `Truncated`.

- [ ] **Step 1: Write failing tests**
  - `TestGraph_PersonEdgesByRole`: a meeting node with speaker and participant
    edges to one contact gives two `person` edges with distinct `Role`.
  - `TestGraph_MembersOnlyWhenConnected`: 3 members, one of them connected,
    give 1 member node and `MemberCount == 3`.
  - `TestGraph_PersonEdgeNeedsPresentMeeting`: an edge from an unpublished
    meeting adds no node.
  - `TestGraph_SkipsUnreadableContact`.
  - `TestGraph_AccountMentionNeedsReadGrant`.
  - `TestGraph_PeopleRowBoundSetsTruncated`.
  - The existing graph tests stay green; update only the ones that asserted
    every member as a node, and name each in the commit.
- [ ] **Step 2–4: Run FAIL → implement → PASS** with `(cd backend && go test ./internal/service/ -run Graph -count=1)`, then the full backend tests.
- [ ] **Step 5: Commit** `feat(accounts): people edges in the account graph`

### Task 12: Flow people (part 6)

**Files:**
- Modify: `backend/internal/service/account_overview.go` (flow item builders), `backend/internal/model/account_overview.go` (`FlowItem` :111)
- Test: `backend/internal/service/account_overview_flow_test.go` (extend; create if the flow tests live elsewhere, after checking with `grep -l 'func TestFlow' backend/internal/service`)

**Interfaces:**
- Produces:
  ```go
  type FlowPerson struct { Kind, ID, Label, Role string } // json camelCase; Role is attendee|participant|speaker|assignee
  // FlowItem gains: People []FlowPerson `json:"people,omitempty"` // at most 5
  const ( maxFlowPeoplePerItem = 5; maxFlowPeopleQueries = 50 )
  ```
- Activity (event) items take attendees with a `ContactID` whose contact is
  readable, with role `attendee`.
- Response (meeting) items take `ListPeopleLinksForMeeting` member and contact
  edges, excluding `mentions`, with the roles ordered participant, speaker,
  assignee.
- Labels follow Task 8.
- After 50 meeting queries in one flow, later items get no `People`. This is
  not an error.
- The existing flow byte bound still applies: `People` is trimmed before rows
  are dropped.

- [ ] **Step 1: Tests**
  - `TestFlow_ResponseItemPeople`.
  - `TestFlow_ActivityAttendeesFromContacts`.
  - `TestFlow_PeopleCappedAtFive`.
  - `TestFlow_PeopleQueryBound`.
  - `TestFlow_SkipsDeletedContactPerson`.
- [ ] **Step 2–4: Run FAIL → implement → PASS.**
- [ ] **Step 5: Commit** `feat(accounts): people in the engagement flow`

### Task 13: Frontend people panel (part 7)

**Files:**
- Create: `frontend/src/lib/accountPeople.ts`, `frontend/src/components/account/AccountPeople.tsx`
- Modify: `frontend/src/lib/api.ts` (`accountApi.people`), `frontend/src/types/meeting.ts`, `frontend/src/components/AccountDetailClient.tsx` (general tab after `AccountFlow` :623, loaded with `useAccountSection(..., overviewVisible, onAccessLost)`)
- Test: `frontend/scripts/account-people.test.mjs`

**Interfaces:**
- Produces:
  - `accountApi.people.list(id, cursor?)`, `.mentions(id, cursor?)`,
    `.resolveMention(id, key, {kind, id})`, `.ignoreMention(id, key)`,
    `.edges(id, meetingId)`, `.bind(id, input)` and
    `.unbind(id, {meetingId, toKind, toId, role})`.
  - `accountPeople.ts` exports:
    - the Korean constants `PEOPLE_PANEL_TITLE`, `PEOPLE_ROLE_LABELS` (for
      participant, speaker, assignee, mentions and attendee),
      `MENTION_RESOLVE_LABEL`, `MENTION_IGNORE_LABEL` and
      `PEOPLE_TRUNCATED_NOTE`;
    - `roleSummary(roles: Record<string, number>): string`, ordered
      participant, speaker, assignee, mentions, skipping zeros;
    - `personOptionValue(kind, id)`;
    - `parsePersonOption(value): {kind:'member'|'contact', id} | null`.
- UI:
  - The resolved list shows the label, meeting count and `roleSummary`.
  - The unresolved list shows group text and meeting count, with a person
    picker reusing the `speakerTeam`/`contacts` suggestion loaders. Choosing a
    person and confirming calls `resolveMention`. The ignore action calls
    `ignoreMention`.
  - After an action the list refreshes, and `remaining` shows the Korean
    "more remain" note.
  - Use `globals.css` tokens only.

- [ ] **Step 1: Tests**
  - `roleSummary({speaker:2, participant:1, assignee:0})` equals the
    participant label + `1` and the speaker label + `2`, joined by `' · '`.
  - `parsePersonOption('project:p1') === null`.
  - A round trip through `personOptionValue('contact','c1')`.
- [ ] **Step 2–4: Run FAIL → implement → PASS** with `(cd frontend && npm run lint && npm run test:unit && npm run build)`.
- [ ] **Step 5: Commit** `feat(web): account people panel and mention review`

### Task 14: Speaker map binding (part 7)

**Files:**
- Modify: `frontend/src/components/meeting/SpeakerMapEditor.tsx` (suggestion select :200, save :144), its caller (find it with `grep -rn '<SpeakerMapEditor' frontend/src`), `frontend/src/lib/contactSuggestions.ts`
- Test: `frontend/scripts/contact-suggestions.test.mjs` (extend, or create when absent)

**Interfaces:**
- Produces:
  - In `contactSuggestions.ts`:
    ```ts
    export interface SpeakerPick { label: string; kind: 'member' | 'contact'; id: string; name: string }
    export function bindingsToSave(saved: Record<string, string>, picks: SpeakerPick[]): SpeakerPick[]
    ```
    It keeps a pick only when `saved[label] === name`, so a name edited after
    picking binds nothing. The last pick per label wins.
  - New editor props: `meetingOwnerId?: string`, `meetingId?: string` and
    `sharedToAccount?: boolean`. Binding is active only when `accountId`,
    `userId` and `sharedToAccount` are all set.
- Behaviour:
  - Choosing a suggestion records a pick.
  - After `onSave` succeeds, the editor calls `accountApi.people.bind` for each
    `bindingsToSave` entry, with role `speaker` and `sourceRef = label`.
  - A binding failure shows a non-blocking Korean notice. The saved names stay
    as they are, because free text keeps working.
  - Existing bindings are shown from `accountApi.people.edges`, each with an
    unbind action.

- [ ] **Step 1: Tests**
  - `bindingsToSave` with an edited name drops the pick.
  - With two picks for one label, the last one is kept.
  - A label missing from `saved` is dropped.
- [ ] **Step 2–4: Run FAIL → implement → PASS** (lint, unit, build).
- [ ] **Step 5: Commit** `feat(web): bind speaker labels to members and contacts`

### Task 15: Graph rendering of people edges (part 7)

**Files:**
- Modify: `frontend/src/lib/accountGraph.ts`, `frontend/src/types/meeting.ts` (`GraphEdge.role`, `AccountGraph.memberCount`), `frontend/src/components/account/AccountGraph.tsx` (show the Korean "{n} members" note from `memberCount`)
- Test: `frontend/scripts/account-graph.test.mjs` (extend)

**Interfaces:**
- `toMermaid` labels a `person` edge with `escapeLabel(PEOPLE_ROLE_LABELS[role])`
  as `-- "label" -->` (auto: `-. "label" .->`). Unknown roles get no label.
  Raw IDs still never appear.

- [ ] **Step 1: Tests**
  - An auto speaker edge renders `-. "<speaker label>" .->`.
  - An unknown role gives an unlabeled arrow.
  - No raw contact ID appears in the output.
- [ ] **Step 2–4: Run FAIL → implement → PASS** (lint, unit, build).
- [ ] **Step 5: Commit** `feat(web): role-labelled people edges in the account graph`

### Task 16: ADR-067 and documentation (part 7)

**Files:**
- Create: `docs/decisions/ADR-067-account-people-edges.md`
- Modify: `docs/API-SPEC.md` (account section prose for the 7 routes and the `graph`/`flow` field additions), `docs/DESIGN-SPEC.md` (`## Accounts, filters and sharing`: people panel, speaker binding), `docs/architecture.md` (summarize → people resolver, `PeopleResolveRequested`), `docs/README.md` (index ADR-067), `docs/decisions/ADR-064-account-relationship-links.md` (one "Related" line pointing to ADR-067; no other change)

- [ ] **Step 1: Write ADR-067.** It records:
  - the people key grammar and why the role is in the key;
  - the role → kind table;
  - the resolver rules, the stop-on-ambiguity ruling and the
    `peopleHonorificSuffixes` symbol;
  - manual-wins, deny and ignore rows, and auto refresh without a deny;
  - `MENTION#` and `PEOPLERUN#` rows, the lease and rerun;
  - triggers (inline after summary, event for speakers and contacts) and the
    backfill CLI;
  - the project 1 listing range change;
  - read-time revalidation and the summary-guard rule for derived text;
  - graph member nodes now appear only when connected;
  - non-delegable routes;
  - known limitations: rows of deleted meetings persist until `-prune`; there
    is no un-ignore route; KMS cost of up to 2,000 contact opens per run;
    a bulk contact import publishes one event per contact, and the lease only
    coalesces events that overlap a running pass.
- [ ] **Step 2: Run** `python3 scripts/docs/check_docs.py`. Expected: `Documentation checks passed` with 192 Go routes.
- [ ] **Step 3: Commit** `docs: account people edges (ADR-067)`

## Spec rulings made in this plan

- **Role in the key.** The spec leaves the sort-key extension open. People
  edges use a 5th segment, `LINK#meeting#{m}#{kind}#{id}#{role}`, and the
  project 1 parsers keep rejecting it. Project 1 `ListAccountLinks` skips the
  `LINK#meeting#` range (Task 3).
- **Unresolved mention storage.** E5 needs unresolved mentions grouped by text,
  which no existing row holds. Task 4 adds `MENTION#{hash}#{m}#{role}` rows and
  the `PEOPLERUN#` run and lease rows, beside the reused link prefixes.
- **Ignore scope.** "Not a person / ignore" is account-wide per normalized text:
  `LINKDENY#mention#{hash}`. A per-meeting deny would re-list the text on the
  next meeting.
- **Ambiguity stops resolution.** Several candidates at any rule leave the
  mention unresolved. A later rule never breaks the tie (anti-goal "picking one
  of several").
- **Honorifics strip only the mention side**, and only after an exact
  full-name miss. Real names ending in a title syllable still match exactly.
- **Async triggers.** Speaker map and contact changes publish
  `PeopleResolveRequested` to the summarize Lambda, because a resolver run can
  open up to 2,000 sealed contacts. The post-summary hook runs inline with its
  own 30-second bound, as the spec says.
- **Flow people** come from people edges on response (meeting) items and from
  existing contact attendees on activity (event) items.
- **Sub-account targets** need only an existing META in the write transaction.
  Descendancy and the viewer's read grant are checked at read time.
