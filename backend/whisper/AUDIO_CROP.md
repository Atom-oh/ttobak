# Private audio crop worker

The production Whisper image supports an optional `AUDIO_CROP=1` mode. Normal
transcription and its pinned model/dispatcher remain unchanged. API orchestration
and user-facing activation are separate changes; this worker mode is unused by
the current normal upload path.

The input is an owner-partitioned meeting with status transcribing and a queued
audioCrop containing sourceMeetingId, sourceKey, sourceETag, startSeconds and
endSeconds. Claiming stores runId and resultKey before any output upload. The
worker revalidates the source, downloads with IfMatch, cuts only the selected bytes,
and transcribes that file. Input is capped at 2 GiB and six selected hours within
the first day; FFmpeg validates the decoded duration and has bounded protocols,
containers and conversion time.

A run-bound heartbeat updates the meeting every 30 seconds while media work and
upload run. Table operations are serialized by stopping the heartbeat before
publication. The crop-specific DynamoDB client uses bounded connection/read timeouts
and one attempt, so retries cannot disguise a successful binding as rejection.
Expired queued work is not revived. A lost claim cannot publish; expired owned runs
can still be marked failed. Definitive rejection deletes the unique candidate;
ambiguous binding retains it. Cleanup failures are reported.

Abrupt process/Spot termination can retain an uploaded candidate. Its address is
persisted as audioCrop.resultKey (and derivable from its persisted runId), so an
operator can reconcile it against canonical audioKey before deleting it. There is
no automatic Spot restart or candidate janitor. Do not delete a candidate merely
because its last worker response was missing.

Run `python -m unittest discover -s backend/whisper -p 'test_*.py' -v` from the
repository root with boto3 and FFmpeg installed. Set DYNAMODB_LOCAL_ENDPOINT to a
loopback DynamoDB Local instance to test real claim/publication/expiry expressions
and durable candidate recording. Unit tests cover range bytes, supported containers,
source revision rejection, heartbeat loss, ambiguous writes and cleanup failure.
Synthetic tests do not establish GPU/model or production acceptance.
