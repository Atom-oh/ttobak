# Descendant Account Sharing Implementation Plan

> Historical implementation record. Status: In progress following the user's
> 2026-10-29 instruction to implement and deploy.
>
> **For agentic workers:** REQUIRED SUB-SKILL: Use
> superpowers:subagent-driven-development or superpowers:executing-plans.
> Execute the steps without another approval pause; implementation and deployment
> are explicitly authorized.

**Goal:** Ancestor Account members can discover and read existing and future
shared descendant content, including the reported child meeting.

**Architecture:** Keep direct membership and mutation checks intact. Resolve
inherited read grants from current strongly read parent links and memberships.
Transactional child references support discovery; Go and Python consumers
revalidate grants and resource publication before returning content.

**Tech Stack:** Go 1.25, DynamoDB, Python/boto3, Next.js 16, TypeScript CDK.

**Spec:** `docs/superpowers/specs/2026-10-29-account-descendant-sharing-design.md`

## Global constraints

- Inherited access is read-only, including an ancestor Owner.
- Maximum 64 ancestors; invalid IDs, cycles and missing chain nodes deny access.
- Exact membership primitives and mutation paths retain their existing contract.
- Meeting classification alone grants no access; canonical publication is required.
- New inherited readers always enforce sharing-safe summary eligibility.
- No credentials or meeting text in logs; no new public route/origin.
- Feature flag `ACCOUNT_DESCENDANT_READS_ENABLED` defaults off in Go and Python.
- Existing independent resource grants retain their existing permissions.
- Storage errors propagate; indexes/cursors never substitute for authorization.

## Review focus

- A moved child must not remain accessible through stale child references.
- Ancestor Owner and stale account-origin edit shares must not authorize writes.
- Private project-linked meetings must not leak titles or aggregated insights.
- Membership/reparenting changes must invalidate source continuations and QA history.
- Empty advancing pages and large subtrees must not silently lose discovery.

## Shared interfaces and ownership

The root implementer owns repository relations, the Go grant resolver, meeting
and project/research consumers, infrastructure, documentation, release tooling and
deployment. The Account worker owns Account read consumers/discovery, bounded
Account/document reading, handler pagination, Account response access fields and
their tests. The Python worker owns QA authorization/discovery and contract tests.
The frontend worker owns all frontend access UI and list pagination/tests.

All workers share one isolated checkout. Stage only owned paths and preserve
others' edits. Do not modify billing authorization or send invitation messages.

Go grant API in `backend/internal/service/account_read_access.go`:

- `resolveAccountReadGrant(ctx, repo, accountID, userID) (*accountReadGrant, error)`.
- `accountReadGrant` fields: `AccountID`, `Member`,
  `InheritedFromAccountID`, `Path`.
- Methods: `inherited() bool`, `permission() string`, `privacyMember()
  *model.AccountMember`, `fingerprint() string`.
- `revalidateAccountReadGrant(ctx, repo, grant, userID) (bool, error)`.
- `repo` requires exact `GetMember`; ancestor reads use its optional
  `GetAccount(context.Context, string) (*model.Account, error)` capability.

Repository discovery API in `backend/internal/repository/account_children.go`:

- `ListChildAccountIDsPage(ctx, parentID, cursor string, limit int32)
  ([]string, *string, error)`.
- `EnsureAccountChildReference(ctx, childID, expectedParentID string) error`.
- Child reference keys are `ACCOUNT#{parentId}/CHILDREF#{childId}`.

Account discovery API, owned by the Account worker:

- `listReadableAccountIDs(ctx, repo, userID string, hints []string)
  ([]string, error)` returns validated direct and descendant IDs, with a
  documented explicit 1,000-Account compatibility limit rather than truncation.
- Optional paged list requests return `{accounts, nextCursor}`; legacy requests
  retain the array contract. Pagination candidates remain untrusted.
- Account responses and summaries add `accessPermission: "read" | "edit"` and
  optional `inheritedFromAccountId`. Direct member arrays remain canonical.

## Task 1: Canonical read grants and child relation persistence

**Files:** Create `backend/internal/service/account_read_access.go` and tests;
create `backend/internal/repository/account_children.go` and tests; modify
`backend/internal/repository/account_hierarchy.go`, `account_creation.go`.

- [ ] Write resolver tests for parent/grandparent reads, revoked membership,
  direct grant precedence, malformed/missing/cyclic/deep ancestry and storage errors.
- [ ] Run the focused tests and record the expected failure before implementation.
- [ ] Implement the shared grant API, strict inherited identity validation,
  fingerprint/revalidation, flag gating and forced inherited summary guard.
- [ ] Write relation tests proving creates and moves transact child references
  with the canonical parent update and preserving existing transaction conditions.
- [ ] Observe RED, implement paginated references and condition-bound bootstrap
  writes, then observe GREEN and run repository/service regressions.
- [ ] Commit only Task 1 paths.

## Task 2: Account content and discovery

**Files:** `backend/internal/service/account.go`, `account_reading.go`,
`document_reading.go`, new `account_read_discovery.go`, their tests;
`backend/internal/handler/account.go` and tests;
`backend/internal/model/account.go`.

- [ ] Write failing tests for inherited Account detail/list/meetings/insights,
  document read, bounded reads, stale grant continuation and denied mutations.
- [ ] Implement Account discovery using the shared resolver and child pages,
  consuming empty advancing pages and rejecting malformed/non-advancing cursors.
- [ ] Add the paged list contract and maintain legacy-array compatibility.
- [ ] Adopt read grants only on reads/alias discovery; preserve exact direct
  membership on invitation, linking and document mutation methods.
- [ ] Bind bounded reads to the full read-grant fingerprint, recheck before
  returning bytes, enforce inherited summary guards and return access DTO fields.
- [ ] Run focused GREEN and full Go regression verification; commit owned paths.

## Task 3: Meeting, project and research consumers

**Files:** `backend/internal/service/meeting.go`, `meeting_team_list.go`,
`meeting_reading.go`, `project.go`, `project_reading.go`, `research.go`,
related tests and direct-membership read call sites discovered during the audit.

- [ ] Add failing parent-only detail/list tests, including no preexisting share
  row, future membership, private link-only meeting and account-origin stale edit.
- [ ] Resolve inherited grants on meeting reads and discovery; bind the complete
  grant into bounded read continuation/recheck.
- [ ] Split project read and mutation checks before inherited project reads.
- [ ] Filter inherited-only project resources through independent current
  resource access before exposing titles or source-derived insights.
- [ ] Adopt inherited Account grants on research reads and descendant discovery.
- [ ] Verify direct grants, mutations, attachments, KB consumers and all Go suites.
- [ ] Commit owned paths.

## Task 4: Python QA authorization, discovery and replay

**Files:** Create `backend/python/qa/account_read_access.py` and tests;
modify `handler.py`, `account_reads.py`, `document_context.py`,
`indexed_retrieval.py` and related QA/source/history contracts.

- [ ] Write behavioral RED tests using realistic DynamoDB fakes for inherited
  meeting/document/context/discovery and revoked/moved grant history.
- [ ] Implement flag-gated current read grants with the same chain/depth/privacy
  rules as Go; consume all child reference pages with explicit bounded failures.
- [ ] Route both transports, Account reads, document reads and indexed discovery
  through that helper, leaving mutations/direct grants intact.
- [ ] Ensure cached/replayed content reauthorizes and binds current source/grant
  identity; unsafe generated context remains restricted for inherited readers.
- [ ] Run focused GREEN and `python3 -m unittest test_handler -v` plus all affected
  source/history tests; commit owned paths.

## Task 5: Frontend inherited access and pagination

**Files:** `frontend/src/types/meeting.ts`, `src/lib/api.ts`,
`src/components/AccountDetailClient.tsx`, Account/document components and tests.

- [ ] Write behavioral tests for paged Account discovery, inherited read access
  indication, forbidden mutation controls and normal direct membership controls.
- [ ] Consume all optional Account list pages; keep a bounded explicit failure
  for repeated/non-advancing cursors.
- [ ] Show ancestor-derived read access, include descendants in the Account tree,
  hide writes/invitations/billing UI when the child is inherited-only and explain
  readership on the existing parent editor.
- [ ] Audit document upload/edit/delete/public-link controls and linked project
  reads for correct server-provided access permissions.
- [ ] Run lint, unit suites and production static build, then commit owned paths.

## Task 6: Review, release and consumer acceptance

**Files:** `infra/lib/gateway-stack.ts` and tests; new operator bootstrap command;
affected guides/ADR/runbook, generated `AGENTS.md`; deployment evidence outside
tracked source contains only metadata.

- [ ] Record an ADR superseding ADR-036's no-inheritance rule and update affected
  current contracts. Mark the approved spec's scope confirmation as resolved.
- [ ] Add the nonsecret activation configuration to the Go and Python function
  configuration, default off until child references are verified.
- [ ] Run full Go tests/vet/ARM64 API build, frontend checks, affected Python/MCP
  suites, infra synth/tests and documentation checks.
- [ ] Perform independent task/whole-branch and AWS security review; resolve
  concrete findings, create a PR, apply required exact-HEAD review feedback and
  merge only with the repository's required checks satisfied.
- [ ] Deploy staged producers, execute the idempotent metadata-only relation
  bootstrap, verify current relation coverage, then deploy/activate consumers
  and publish frontend under existing release procedures.
- [ ] Verify the actual deployed parent-only member's access to the reported
  meeting without printing content, plus negative private/mutation tests and
  rollback behavior. Record exact deployed code/config revisions.
- [ ] Report completion only after consumer acceptance; preserve original source
  checkout edits and unrelated stacks/functions.

## Execution rulings and evidence

- Ruling: The user's implement-and-deploy instruction approves the proposed
  read-only inheritance scope and authorizes its production release.
- Ruling: Use subagent implementation for independent Account, QA and frontend
  tasks, with root-owned shared interfaces and release coordination.
- Ruling: The documented `/usr/local/go/bin/go` is absent in this environment;
  `/home/atomoh/go-sdk/go/bin/go` is Go 1.25.0 and is the verification executable.
- Baseline Go verification: running; evidence
  `/tmp/ttobak-descendant-baseline-go.log`.
