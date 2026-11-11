# Account merge operator runbook

Operator procedure for [ADR-072](../decisions/ADR-072-account-merge.md) merges that
need the CLI: starting a merge, moving the user-partition rows only the CLI can
reach (during the merge or after it is done), checking progress, resuming a stalled
job, recovering a job that failed with `REQUESTER_REVOKED`, and aborting a stuck job. This is a checked-in procedure, not deployed acceptance.

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
| `-scan-users` | Also count (dry run) or rewrite the CLI-only rows below; with `-apply` it runs inside the invocation that starts or resumes the job, or, once the job is `done`, under the post-merge fence (section 7) |
| `-verify` | Read-only count of CLI-only rows still naming `-from`; skips the preview, so it works after the merge is done; the requester must be a direct member of `-into` |
| `-max-pages` | Page limit per scan segment, default 100000 |
| `-status` | Print the job state and counts; needs only `-from` and `-requester` |
| `-abort` | End a `failed` job or a `running` one with an expired lease (section 8); writes only with `-apply` |

Environment: `TABLE_NAME` (no fallback) and standard AWS credentials and region;
`KMS_KEY_ID` is also required with `-apply` because contacts, events and calendar
rows are sealed. The CLI host needs DynamoDB, KMS (encrypt and decrypt under the
application key) and EventBridge `PutEvents` for the people resolve request. Run it
from `backend/` on a host that already holds those grants; do not widen IAM for it.

`-scan-users` reads user partitions with a bounded four-segment, eventually consistent table scan, so run
it off peak and one merge at a time. It handles three kinds of rows:

- SpecReq tasks (`SRTASK#`): `accountId` A to B.
- Calendar events (`CALEVENT#`): `accountId` on the row and inside the sealed
  payload together, with A replaced by B in the payload's candidate account IDs
  (duplicates and the share account are dropped, and the payload must pass the same
  normalization and size cap readers apply). `shareAccountId`, share state and
  meeting key are not rewritten and must be unchanged since the scan: calendar share
  rows (`CALSHARE#`) stay on A (an unmoved category), so repointing would orphan
  them (ADR-072 amendment).
- Link-only meetings: moved with the merge's dedicated meeting writer, which never
  publishes. Published meetings are counted as `publishedMeetings` and move in the
  worker's meetings step.

Each rewrite is one transaction with the merge fence checks (running job, CLI lease,
source owner, requester membership of the target, open target; after the job is done
the post-merge fence of section 7) and is conditioned on
`accountId` still naming A and on the row state the scan read. A lost fence stops
the scan. A changed row is counted as `Skipped` and the scan runs again, up to three
passes. During a job the CLI holds the job lease from the scan through its
in-process worker. If rows remain skipped or any row fails, it marks the job `failed`
with `SCAN_INCOMPLETE` instead of releasing the lease, so no worker delivery can
finish the job over rows left behind (a lost fence fails it too); rerun the step 2
command to restart it from its saved step. A row that fails on every run (decrypt or
payload) blocks the merge until fixed, or until `-abort` (section 8); v1 has no
per-row skip. A post-merge run (section 7) reports the same code while rows stay
skipped. A calendar move also bumps the row's `shareVersion`, so a sync that read the
row before the move fails and re-reads it, and a sync naming a merging or merged
account is refused with 409 `ACCOUNT_MERGING`. A sync authorized before the start
that reads the row after the move can still write A back (an accepted narrow window):
`-verify` reports such rows and a post-merge run moves them. Any other
failure is counted in `errors` and by reason in `errors.decrypt`, `errors.payload`,
`errors.encrypt` or `errors.write`, and fails the job the same way. The scan reads
eventually consistent, so a row committed just before the start can be missed; the
worker is not started while any observed row remains, and `-verify` reports the rest.

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
user-partition rows under that lease (renewed before every row), then runs the
worker in this process as the same lease owner, without releasing the lease in
between, until the job is `done` or `failed`. Keep the session alive; it can run a
long time. Result fields: `state`, `step`, `errorCode`, `counts` (job counts keyed
`{step}.{moved|skipped|resumed|unreadable|keptSeparate}`) and `userRows` (suffix
`Moved`, `Skipped`; `errors`).

Start codes: `JOB_RUNNING` (a worker holds a live lease, wait and check status),
`START_CONFLICT` (an account changed during start, rerun),
`CONTACT_INDEX_TOO_LARGE` (source and target contacts exceed 10,000),
`FENCE_LOST` (the lease moved or the requester lost authority mid-scan),
`NO_PROGRESS` (the worker loop saw no progress for about ten minutes, usually a
lease held by another worker), `SCAN_INCOMPLETE` (see above).

While the job runs, `-scan-users` rewrites rows inside the `-apply` invocation that
starts or resumes it, under the job lease. A merge started through the API (the web
dialog) leaves these rows unmoved: if its job is still `running` (lease expired) or
`failed`, rerun this command with `-scan-users`, which takes over the job; once it is
`done`, use the post-merge procedure in section 7.

## 3. Status

```bash
TABLE_NAME=ttobak-main go run ./cmd/merge-accounts -from SOURCE_ID -requester USER_ID -status
```

The requester must be a direct member of the source or of the job's target (the
same rule as `GET /api/accounts/{accountId}/merge`). `state` is `running`, `failed`,
`done` or `aborted`; `step` is the next step to run (`members`, `children`, `projects`,
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
   routes, or choose another user who is already Owner of the source and a direct
   member of the target. ADR-015 owner transfer is refused while the source carries
   `mergingInto`, so it cannot appoint a new Owner during a fenced job.
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
- Run the read-only check for the same pair as a direct member of the target:
  `go run ./cmd/merge-accounts -from SOURCE_ID -into TARGET_ID -requester USER_ID
  -scan-users -verify`. It skips the preview (a merged source is ineligible) and
  counts rows with `accountId` still A: `specreqTasks`, `calendarEvents` and
  `linkOnlyMeetings` should be 0. A nonzero `publishedMeetings` means published
  meetings are still on A (their `MEETINGREF#` row was missing); report them, because
  the worker will not move them. Nonzero task, event or link-only meeting counts
  after `done` are moved with the post-merge procedure in section 7.
- `counts` should show `members`, `contacts`, `events` and `meetings` moved or
  skipped with `unreadable` and `keptSeparate` reviewed.
- In the web app as a target member, open the account: moved meetings, contacts and
  events appear, and a former source member still opens the unmoved categories
  (news sources, KB, documents, calendar shares, dictionary) through the tombstone.
- The worker requests a people resolve run for the target when the job finishes;
  links on A stay on the tombstone (ADR-072). If that publish failed, the job keeps
  `resolvePending` and the redelivered worker event republishes it; worker logs show
  `people resolve republished`.

There is no undo. Do not delete the tombstone, the job row or the `MERGE#` mapping
rows: a resume or a later `-verify` run depends on them.

## 7. Move CLI-only rows after the merge is done

A web-started merge finishes without touching SpecReq tasks, calendar events or
link-only meetings. After `state` is `done`, run as a direct member of the target who
was the job's requester or is the target's owner:

```bash
TABLE_NAME=ttobak-main KMS_KEY_ID=KEY_ID go run ./cmd/merge-accounts \
  -from SOURCE_ID -into TARGET_ID -requester USER_ID -scan-users -apply
```

The CLI sees the `done` job and does not start, lease or run the worker (`mode`
`post-merge`). It refuses before scanning when `-into` is not the job's target or the
requester lacks that authority (`FORBIDDEN`), when the source is not the tombstone
`mergedInto` the target (`NOT_MERGED`), or without `-scan-users` (`MERGE_DONE`). Each
row write carries the post-merge fence: the job is `done` for the target, the source
META is `mergedInto` the target with no `mergingInto`, the requester is still a direct
member of the target (and was the job's requester, or owns the target), and the
target is open. Rows stay conditioned on `accountId` naming the source; nothing writes
into the source. Link-only meetings stay link-only (no grants, no target reference).
A published meeting still on the source is counted in `publishedMeetings` and left
alone. Codes and counts are as in section 2: a lost fence is `FENCE_LOST`, changed
rows are `Skipped`, failures are counted in `errors` and the command can be rerun.
Finish with the `-verify` check of section 6.

Until this scan moves a calendar row, a calendar sync that sends that row's
`accountId` of a merging or merged account is rejected with 409 `ACCOUNT_MERGING`;
reads keep listing the event with its stored account.

## 8. Abort a stuck job

Use this only when a job cannot be finished: it is `failed`, or `running` with no
progress and an expired lease, and resuming (section 4) is not wanted. The
requester must be the job's requester and still Owner of the source.

```bash
# Dry run: prints the job, writes nothing.
TABLE_NAME=ttobak-main go run ./cmd/merge-accounts -from SOURCE_ID -into TARGET_ID -requester USER_ID -abort
# Abort.
TABLE_NAME=ttobak-main go run ./cmd/merge-accounts -from SOURCE_ID -into TARGET_ID -requester USER_ID -abort -apply
```

One transaction, conditioned on the job's observed state and lease owner, sets the
job `aborted`, removes the source's `mergingInto` (the source opens for writes again)
and decrements the target's `incomingMerges`. Nothing is rolled back: members,
children, projects, research, contacts, events and meetings already moved stay on
the target, and the job `counts` record what moved. Codes: `NOT_ABORTABLE` (the job
is `done` or a worker holds a live lease), `FORBIDDEN` (not the job's requester, a
different `-into`, or the requester is no longer Owner of the source),
`START_CONFLICT` (the job or the target changed; check `-status` and rerun),
`NOT_FOUND` (no job). A later merge of the source starts as a fresh job.
