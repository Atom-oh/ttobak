# Account merge operator runbook

Operator procedure for [ADR-072](../decisions/ADR-072-account-merge.md) merges that
need the CLI: starting a merge, moving the user-partition rows only the CLI can
reach, checking progress, resuming a stalled job and recovering a job that failed
with `REQUESTER_REVOKED`. This is a checked-in procedure, not deployed acceptance.

## What the CLI is

`backend/cmd/merge-accounts` runs the same service paths as
`POST /api/accounts/{accountId}/merge` and its worker. It acts as a named
`-requester` and never bypasses eligibility: that user must be the Owner of the
source (`MEMBER` role and META `ownerUserId`) and a direct member of the target, and
every start check applies (tombstones, concurrent merges, subtree depth, contact
index bound, `-accept-unmoved` when any unmoved category is present; billing always
is). Output is JSON counts and job state only. Errors print a code only.

| Flag | Meaning |
|---|---|
| `-from`, `-into` | Source account A (becomes the tombstone) and target account B |
| `-requester` | Required user ID of the requester; same authorization as the API |
| `-apply` | Write. Without it the CLI prints preview counts and writes nothing, not even a lease |
| `-accept-unmoved` | Accept the categories v1 leaves on A |
| `-scan-users` | Also count (dry run) or rewrite the CLI-only rows below |
| `-max-pages` | Page limit per scan segment, default 100000 |
| `-status` | Print the job state and counts; needs only `-from` and `-requester` |

Environment: `TABLE_NAME` (no fallback) and standard AWS credentials and region;
`KMS_KEY_ID` is also required with `-apply` because contacts, events and calendar
rows are sealed. The CLI host needs DynamoDB, KMS (encrypt and decrypt under the
application key) and EventBridge `PutEvents` for the people resolve request. Run it
from `backend/` on a host that already holds those grants; do not widen IAM for it.

`-scan-users` reads user partitions with a bounded four-segment table scan, so run
it off peak and one merge at a time. It handles three kinds of rows:

- SpecReq tasks (`SRTASK#`): `accountId` A to B.
- Calendar events (`CALEVENT#`): `accountId`, and `shareAccountId` when it names A,
  on the row and inside the sealed payload together, with A replaced by B in the
  payload's candidate account IDs. Calendar share rows (`CALSHARE#`) stay on A (an
  unmoved category).
- Link-only meetings: moved with the merge's dedicated meeting writer, which never
  publishes. Published meetings are counted as `publishedMeetings` and move in the
  worker's meetings step.

Each rewrite is conditioned on `accountId` still naming A (and on the row version or
revision the scan read). A changed row is counted as `Skipped`: rerun to pick it up.
Any other failure is counted in `errors`; the CLI then releases its lease and does
not start the worker, so nothing finishes while rows are left behind.

## 1. Preview (dry run)

```bash
cd backend
TABLE_NAME=ttobak-main go run ./cmd/merge-accounts \
  -from SOURCE_ID -into TARGET_ID -requester USER_ID -scan-users
```

Review `counts`, `unmovedPresent`, `requiresAcceptUnmoved` and the `userRows` counts
(`specreqTasks`, `calendarEvents`, `linkOnlyMeetings`, `publishedMeetings`).
`invalid` counts matched rows whose keys failed validation; they are never moved.
An ineligible pair fails here with a code (`FORBIDDEN`, `NOT_FOUND`, `INELIGIBLE`,
`INVALID_INPUT`). Tell the account's members what the preview reports as the new
audience before applying; the merge cannot be undone.

## 2. Start

```bash
TABLE_NAME=ttobak-main KMS_KEY_ID=KEY_ID go run ./cmd/merge-accounts \
  -from SOURCE_ID -into TARGET_ID -requester USER_ID -scan-users -accept-unmoved -apply
```

The CLI starts the job, claims the job lease as `cli-<host>-<pid>`, rewrites the
user-partition rows under that lease, releases it, then runs the worker in this
process until the job is `done` or `failed`. Keep the session alive; it can run a
long time. Result fields: `state`, `step`, `errorCode`, `counts` (job counts keyed
`{step}.{moved|skipped|resumed|unreadable|keptSeparate}`) and `userRows` (suffix
`Moved`, `Skipped`; `errors`).

Start codes: `JOB_RUNNING` (a worker holds a live lease, wait and check status),
`START_CONFLICT` (an account changed during start, rerun),
`CONTACT_INDEX_TOO_LARGE` (source and target contacts exceed 10,000),
`FENCE_LOST` (the lease moved or the requester lost authority mid-scan),
`NO_PROGRESS` (the worker loop saw no progress for about ten minutes, usually a
lease held by another worker), `SCAN_INCOMPLETE` (see above).

Starting through the API instead leaves the user-partition rows unmoved; run the
CLI with `-scan-users -apply` for the same pair afterwards only while the job is
still `running` or `failed`, because the meeting writer needs the running job's
fence. SpecReq tasks and calendar events carry no fence and can still be rewritten after
the job is done, but their rows keep naming A until then.

## 3. Status

```bash
TABLE_NAME=ttobak-main go run ./cmd/merge-accounts -from SOURCE_ID -requester USER_ID -status
```

The requester must be a direct member of the source or of the job's target (the
same rule as `GET /api/accounts/{accountId}/merge`). `state` is `running`, `failed`
or `done`; `step` is the next step to run (`members`, `children`, `projects`,
`research`, `contacts`, `events`, `meetings`, `metadata`, `finish`); `errorCode` is
`REQUESTER_REVOKED`, `CONTACT_INDEX_TOO_LARGE`, `MERGE_CONFLICT`, `STEP_FAILED` or
`DISPATCH_FAILED`.

## 4. Resume a stalled job

A job is stalled when `state` is `running` but `updatedAt` stops advancing (the
worker crashed or timed out without releasing). A lease lasts five minutes; wait for
it to expire, then rerun the step 2 command (add `-scan-users` only if the scan did
not complete). Start hands a `running` job whose lease expired to this process and
the saved step and cursor continue. A live lease returns `JOB_RUNNING`: do not force
it. Every mover is idempotent, so a repeated step moves nothing twice.

A `failed` job restarts the same way from its saved step; the source stays fenced
after a failure and nothing is rolled back. `DISPATCH_FAILED`, `MERGE_CONFLICT` and
`STEP_FAILED` are retry codes: rerun once the cause is gone. For `STEP_FAILED`, check
the worker logs (codes only, no row text) before rerunning.

## 5. Recover `REQUESTER_REVOKED`

The requester lost Owner of the source or membership of the target mid-job. The
fenced movers refused every later write, so nothing was partially published, and the
job is `failed` at the step it reached.

1. Confirm with `-status` that `errorCode` is `REQUESTER_REVOKED`.
2. Decide who should finish the merge. Either restore the original requester's
   Owner role and target membership through the normal member and owner-transfer
   routes, or choose another user who is Owner of the source and a direct member of
   the target (a new Owner of the source through ADR-071 owner transfer, joined to
   the target).
3. Rerun the step 2 command with `-requester` set to that user and the same `-into`
   (the target must match the failed job). Start re-authorizes the new requester and
   resumes the job from its saved step with that user as the job's requester.
4. If the same code returns, the chosen user fails an eligibility check; run the dry
   run to see the code.

A different `-into` cannot resume a failed job: it is rejected while the source is
still `mergingInto` the original target.

## 6. Verify

After `state` is `done`:

- Source META carries `mergedInto` B and no `mergingInto`; the target's
  `incomingMerges` is back to its prior value. `GET /api/accounts/{sourceId}` reports
  `mergedInto`, and the account list no longer shows A.
- Run `-scan-users` without `-apply` for the same pair. `specreqTasks`,
  `calendarEvents` and `linkOnlyMeetings` should be 0. A nonzero
  `publishedMeetings` means published meetings are still on A (their `MEETINGREF#`
  row was missing); report them, because the worker will not move them.
- `counts` should show `members`, `contacts`, `events` and `meetings` moved or
  skipped with `unreadable` and `keptSeparate` reviewed.
- In the web app as a target member, open the account: moved meetings, contacts and
  events appear, and a former source member still opens the unmoved categories
  (news sources, KB, documents, calendar shares, dictionary) through the tombstone.
- The worker requests a people resolve run for the target when the job finishes;
  links on A stay on the tombstone (ADR-072).

There is no undo. Do not delete the tombstone, the job row or the `MERGE#` mapping
rows: a resume or a later `-scan-users` run depends on them.
