# ADR-046: Account-scoped browser recording recovery

Status: Accepted (implementation; no claim of production acceptance)
Date: 2026-09-25

## Decision

Browser microphone/tab recording stores delivered audio chunks and current notes
in IndexedDB, keyed by Cognito subject and a random recording ID. Metadata and each
chunk commit together; the displayed saved time advances only after commit. Web
Locks permit only one active recorder/recovery handle per recording. A storage or
lock failure must not stop capture or block uploading the complete in-memory audio.

The page reserves the recording flow from stop through asynchronous finalization;
a generation check rejects a late blob from an older flow.
Stopping can retain audio and final notes for a later upload. Recovery uses the
original account, compares saved notes, and retains the same uploaded object key
across acknowledgement retries. A missing/deleted draft clears its old meeting and
upload identity before creating a new meeting; permission or network errors retain
that identity. Delete the local copy only after acknowledged
upload completion or explicit deletion. No scheduled TTL or sign-out purge applies:
those would remove the only copy of a recording the user intentionally deferred.

## Retention and limits

The application lists and opens only the current account's rows; this is application
isolation, not encryption or an OS security boundary. Audio/notes remain on the
browser profile after sign-out and are accessible to someone controlling that
profile or same-origin script. The recovery card states the shared-device caveat
and offers download and explicit deletion. The independent Mac leftover-file
policy in ADR-024 is unchanged.

Recovery covers committed chunks only. Browser eviction/data deletion, storage
quota, unavailable Web Locks, a suspended recorder, or a missed final callback can
limit recovery. No guarantee of a final upload or callback is made when closing a
lid or tab. Rows with no delivered audio are not shown; housekeeping for those
metadata-only rows is deferred.

## Validation

Run `node --test frontend/scripts/recording-backup.test.mjs
frontend/scripts/recording-recovery.test.mjs`, frontend lint/build, and full Go
tests/vet. Test reload, account switch, lock contention, quota failure, saved notes,
receipt replay, canonical server recovery binding, and upload deferral in a real
browser with synthetic data. CDK orders the updated transcribe consumer before the
recovery API producer. Runtime rollout remains distinct from these local checks.
