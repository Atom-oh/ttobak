# QA pre-activation failure recovery

Status: historical implementation plan; current behavior is documented in
`backend/python/qa/ASYNC_CONTRACT.md`. This record does not prove deployment.

Scope: the two confirmed pre-activation gaps from PR242's reviewed `11bd828`
head. Start from merged `07dbd861`. Keep `qaAsyncJobs=false`; no activation,
deployment, AWS/model calls or PR227 changes belong to this work.

## Meeting QA isolation

QAPanel currently retains its session after a failed HTTP request. A server job
can still write that session after the next question starts. Keep successful
conversation continuity, but discard the failed session reference immediately
on every error. The next explicit question gets a fresh UUID-based session.
Keep visible prior entries/drafts and the existing meeting-key/unmount guards.
Do not automatically resend a question.

Reproduce using the real rendered panel and mocked HTTP: fail request A, start
B, complete B, then finish A's simulated server write. Verify distinct sessions,
B's retained server/UI result, exactly two user-requested submissions, successful
turn continuity, and route/unmount isolation. Include both sync and async modes.

## Deadline receipt preservation

`JobDeadline` deliberately bypasses ordinary `Exception` fallbacks. Catch only
that deadline at the agent-loop boundary and keep its failed outcome. Capture
bounded, immutable checkpoints of completed tool-use/result pairs as they become
available. Exclude unfinished tool calls and partial assistant text. A checkpoint
cannot replace a prior valid one with untracked/invalid source state.

On deadline, use a separate two-second cleanup budget to revalidate the captured
sources and attempt one history save with an explicit interruption note. Apply
a bounded snapshot-size limit. No model or mutation executes during cleanup;
normal read-only source validation is allowed. Failed source validation, cleanup
timeout or an unacknowledged write must not claim saved history or success.
Always propagate the original deadline so the job remains interrupted/failed.

Tests must cover a confirmed research receipt followed by model timeout, timeout
during a later tool, revoked/unavailable source, rejected/ambiguous history write,
oversized history, and a real alarm enforcing the cleanup budget. Verify no
creation replay and no successful job/result publication.

## Validation and delivery

Run focused RED/GREEN regressions, full QA unittest suites, frontend changed-file
lint and production build, and documentation checks. Browser evidence uses local
transport mocks only. Publish a draft PR when concrete; the host owns merge and
deployment to keep current acceptance evidence stable.
