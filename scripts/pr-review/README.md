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

Untrusted stdout and stderr are limited separately to 1 MiB of UTF-8 after
process capture, before parsing or scrubbing; this is not a streaming capture
memory limit. Direct result-file reads use the same bound. Decoded string values
share a 1 MiB sanitization budget, and attempt-history reads are bounded too.
Stored result envelopes allow an additional 4 KiB for host metadata.
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
