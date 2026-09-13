# Inactive specialist runtime

This prerequisite hardens the optional specialist helpers. The checked-in CI
workflow and legacy `run-panel.sh` / `synthesize.sh` dispatch remain unchanged;
specialist activation is a separate change. The workflow is the authority for
which protocol actually runs.

## Requests and results

`role_review.py prepare` validates a complete immutable diff/context and creates
the role plan. `issue` persists a nonce-bound request before execution; `record`
requires its matching nonce, validates the response and scrubs decoded output.
`aggregate` revalidates request/result digests and complete required coverage.
Use `COMMAND --help` for CLI arguments. `run_role.py` handles bounded execution;
`synthesize_roles.py` sends only substantive candidates to the chair.
Codex uses JSONL turn events and its designated final-message file; progress
messages are never concatenated or searched for a parsable review. Both the
event stream and the regular final-message file retain the output byte limits.
JSONL records split only at literal LF bytes; Unicode separators inside JSON
strings remain payload. Terminal executor and final-file overflow are handled
before transport parsing or diagnostic concatenation and cannot trigger retry.
The event stream and final-file byte bounds are checked independently before
rejecting malformed/incomplete events; bad framing cannot hide final-file overflow.

The collector accepts only committed base context hooks/adapters with matching
bytes. An exclusions-only result requires explicit `--allow-exclusions-only`
and the exact trusted `--policy` bytes. The plan anchors that policy hash, and
aggregation rechecks the anchor and reports excluded paths. These collector
features do not activate the specialist protocol in the legacy CI workflow.

Validated results are final for that preparation, including clean results,
findings and uncertainties. Reissuing them fails without replacing the receipt
or result. Failed results may be reissued within the executor's existing budget;
their archived terminal diagnostics and duplicate-record flags remain blocking.
The chair cannot waive missing/invalid coverage.

Issuance and recording share a nonblocking POSIX advisory lock per role. A busy
operation fails instead of clearing another writer's claim. Lock files retain
the same inode and must not be removed while workers run. A record claim without
a completed result is uncertain and cannot be cleared by reissue. Run preparation
only before workers start, using a fresh work directory; it is not a concurrent
reset or a way to discard an unfavorable completed review.

The raw-output scrubber and decoded-string scrubber are separate stages.
Unterminated quoted credentials are redacted through the end of the decoded
string. Sanitization is defense in depth, not proof that arbitrary content
contains no secrets. Request digests attest byte binding, not model honesty.
The credential matcher checks keyword membership before consuming the complete
identifier, preserving affixed keys without retrying every keyword/suffix pair.
URI schemes and JWT headers start at whole-token boundaries; JWT header membership
is checked separately from consumption. Bounded subprocess tests cover long
alphabetic runs, repeated keywords/prefixes, separators, and the other scrubber
pattern families while retaining valid credential-redaction checks.
YAML block matching checks indentation without consuming it separately from the
line body. Blocks include blank lines and recognize LF, CRLF, bare CR and EOF;
the environment name/value matcher shares the same line-ending rule.
YAML name/value pairs redact the complete value line, including commas, spaces
and quoted escapes, stopping before the next line. Inline pairs keep their
separate matcher.
Structured JSON and quoted JSON fragments are decoded before redaction. Sensitive
fields and header name/value pairs are masked; credential-shaped object keys also
pass through the token scrubber. Colliding redacted keys receive unique
`[REDACTED-KEY-N]` aliases, preserving every value and ordinary schema key.
Quoted fragments are scanned once; recursive decoding is limited to 32 levels.
The decoded value budget is shared without charging encoded text again for each
decoding layer. Limits and final publication checks remain blocking.
Credential-key classification scans name segments once, including slash/backslash
paths, and masks the entire associated value. PEM markers span array elements:
the complete marked credential is redacted through END (or array end), retaining
ordinary strings and context outside the key. Original elements still consume
the shared byte budget before replacement.
Complete PEM spans are masked before quoted fragments are processed. Adjacent
quoted string literals joined with `+` are decoded and combined without execution
on the same line before credential matching, preserving split PEM markers and
outside context. If decoded content needs no redaction, the original spelling,
quote style, JSON formatting and concatenation expression remain unchanged.
Quoted credential values consume escapes as indivisible pairs, so embedded
quotes cannot terminate masking early. Concatenation never crosses diff lines.
Only exact scrubber marker names are treated as placeholders when classifying
object keys; ordinary credential fields and credential paths remain sensitive.

Untrusted stdout and stderr are limited separately to 1 MiB of UTF-8 after
process capture, before parsing or scrubbing; this is not a streaming capture
memory limit. Direct result-file reads use the same bound. Decoded string values
share a 1 MiB sanitization budget, and attempt-history reads are bounded too.
Stored result envelopes allow an additional 4 KiB for host metadata.
Both stream sizes are checked even on nonzero exit; failed stdout is not parsed
or scrubbed. Final serialized result envelopes and sanitized chair output are
checked again before publication, including any growth caused by redaction.
Oversize produces the static `output_byte_limit` failure and stays blocking on
reissue. Chair overflow, including growth during sanitization, produces FAIL and
failed-chair status without fallback. No review is truncated
or counted as valid partial coverage.

Limits remain 95,000 diff bytes, 3,000 diff lines, 24,000 context bytes and a
complete request below 128 KiB. Trusted collectors own scope and provenance;
callers cannot combine partial PASS reports into complete coverage.

## Offline checks

```bash
python3 -m unittest discover -s scripts/pr-review -p 'test_*.py' -v
bash tests/run-all.sh
bash scripts/pr-review/chair-timeout-policy-check.sh
python3 scripts/docs/check_docs.py
python3 -m unittest discover -s scripts/docs -p 'test_*.py' -v
```

The tests use synthetic responses and provider stubs, including cross-process
issue/record races. They make no live model calls.
