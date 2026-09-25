# Private audio crop worker

The production Whisper image supports an optional `AUDIO_CROP=1` mode. Normal
transcription and its pinned model/dispatcher remain unchanged. API orchestration
and user-facing activation are separate changes; this worker mode is unused by
the current normal upload path. API readers recognize crop metadata and reject
replacement audio, checkpoint recovery, rediarization and recording-state resets.
Ordinary S3 events for crop meetings are ignored. Deploy the transcribe consumer filter before
running crop-mode tasks: exact crop_result WAVs bypass ordinary S3-triggered STT.

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
Expired queued work is not revived. Failure fencing requires no bound audio, so
a replacement binding cannot be regressed. Once audio is bound, an unconfirmed
transcript write leaves status transcribing so any committed S3 event can proceed. A lost claim cannot publish; expired owned runs
can still be marked failed. Definitive rejection deletes the unique candidate;
ambiguous binding retains it. Cleanup failures are reported.

Abrupt process/Spot termination can retain an uploaded candidate. Its address is
persisted as audioCrop.resultKey (and derivable from its persisted runId), so an
operator can reconcile it against canonical audioKey. Before deletion, confirm
the task has stopped and conditionally fence that same run as failed; retain any
object referenced by canonical audioKey. A live run must not be able to publish
after the cleanup check. There is
no automatic Spot restart or candidate janitor. Do not delete a candidate merely
because its last worker response was missing.

Run `python -m unittest discover -s backend/whisper -p 'test_*.py' -v` from the
repository root with boto3 and FFmpeg installed. Set DYNAMODB_LOCAL_ENDPOINT to a
loopback DynamoDB Local instance to test real claim/publication/expiry expressions
and durable candidate recording. Unit tests cover range bytes, supported containers,
source revision rejection, heartbeat loss, ambiguous writes and cleanup failure.
Synthetic tests do not establish GPU/model or production acceptance.

## Bound audio without a transcript

After a confirmed task stop, inspect the same run and canonical audioKey. If the
transcript object already exists, retain the audio and investigate the normal
summary/event path; do not restart the crop or delete the object. If the transcript
is confirmed absent, retain the bound WAV. Mark that stopped run failed only with
conditions on its runId, state done and meeting status transcribing; do not regress
an advanced meeting status.

Request a new copy from the original with a new request ID once crop orchestration
is available. If the original is unavailable, download the canonical WAV through
authorized access and upload it as a new private meeting using the existing upload
flow. Preserve any saved notes when doing so. Never reset the old run to queued or
replace its binding. This is explicit operator/user recovery, not automatic resume.
