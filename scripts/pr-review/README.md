# Specialist review protocol

CI selects `ROLE_REVIEW=1`. Trusted inputs feed specialist executors; validated
results feed aggregation and, when needed, the chair. See
[the project contract](../../docs/pr-review-specialists.md).

| Tag | Requested model | Scope |
| --- | --- | --- |
| codex | `global.openai.gpt-6-astra` | Implementation/tests |
| kiro-fable | `claude-opus-5` | AWS/IAM/network |
| kiro-sol | `gpt-5.6-sol` | Deployment/contracts/recovery |
| claude-self | `global.anthropic.claude-fable-5-1` | Auth/data/API/ADR |

`kiro-fable` means Opus. `ROLES` governs specialists; legacy files govern legacy
execution. Kiro/Bedrock IDs differ. English is requested, not validated; configured
IDs do not attest model weights.

## API and input

`python3 scripts/pr-review/role_review.py COMMAND --help` lists flags.

| Command | Contract |
| --- | --- |
| prepare | Diff/context, HEAD/base, work; optional paths/provenance → `role-plan.json`, `roles/TAG.txt/.diff`. |
| issue | Work/tag → nonce, exact `requests/TAG.prompt/.input`, `slot/TAG-request.json`. Call before each attempt. |
| record | Tag, output/stderr, exit code, issued nonce → validated, scrubbed `slot/TAG-result.json`. |
| aggregate | Validate results/receipts → `role-summary.json`, `responded.txt`, `chair-mode.txt`, applicable report/flag. |

The executor sends issued bytes; hashes bind inputs, not transport. Keep tool data
out of diagnostics.

`--paths`: UTF-8 JSON array of unique repository-relative paths matching the patch,
e.g. `["src/api.ts"]`. Renames use destinations; the collector checks both sides.
Omit only for authoritative, unambiguous patch paths.

`--provenance`: JSON object. Required `head_sha`/`base_sha` equal the lowercase
40-character CLI revisions; `diff_sha256` hashes exact raw diff bytes. Example:

```json
{"head_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","base_sha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","diff_sha256":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"}
```

Optional `input_failures` contains codes matching `[a-z][a-z0-9_:.-]{0,63}`; any code
blocks. Invalid provenance is discarded and blocks; stored values are scrubbed.
Optional `path_only: list[str]` identifies collector-approved metadata-only
deletions. Verify eligibility before withholding bodies.

## Coverage and lifecycle

Codex/Claude are required for reviewable source; trusted routing may deactivate
irrelevant Kiro roles. App Router React is conservative. Failed output is never
N/A. Parsing misses whole omissions/some cut prefixes: verify Git scope/hashes.

Exclusions-only NOT_APPLICABLE/PASS requires `--allow-exclusions-only --policy FILE`.
The schema-1 policy bytes must match the 64-hex `input_policy_sha256`; the private
`exclusions-policy.json` anchor is rechecked on aggregation. Require empty diff,
`--paths` file containing `[]`, `scope_exception: configured_exclusions_only`, and
identical nonempty unique safe `scope_paths`/`excluded_paths`. The trusted BASE
collector must verify policy and all Git paths. Missing opt-in, accidental empty
input or mismatch blocks. The report discloses exclusions/hash and no model review.
New exclusions require policy review; project-specific rules remain.

Start fresh work before collection. `prepare` clears owned results/receipts, claims,
duplicate/terminal flags and histories; upstream flags remain. Issue/record exclude
each other; interrupted operations require fresh work. Duplicate records retain
the first result and block. Finish writers before aggregation. Reissue archives
32 prior results in `slot/TAG-attempts.json`; model-selection/fallback/quota/preflight
failures block until new preparation. Summaries retain history. All `*.flag` files
block except root `coverage-severe.flag`. `failure_codes` is canonical; `failures` aliases it.

The trusted specialist parent probes every active Kiro model once before releasing
any Kiro review. If any required probe fails, all active Kiro roles remain blocked;
Codex/Claude still run. Inactive Kiro roles are not probed. Each review starts in a
fresh private directory with the same no-tools agent and no probe canary.
The shared decision stays in parent memory, bound to the plan digest, model roster
and agent configuration; each issued request is rechecked before delivery. There
is no environment or stored-receipt bypass. Standalone Kiro execution also checks
the complete active Kiro roster before sending input.

Exit 2 means blocked. Aggregate exit 0: `deterministic` permits the report when no
blocking candidate/uncertainty exists (Minor/Info remain); `review` needs a chair.
Blocked input yields deterministic FAIL; the chair cannot waive coverage failures.

Publish scrubbed reports/receipts/metadata only; never raw `roles/*.diff` or
`requests/*.input/.prompt`.

## Limits and checks

Limits: 95,000 diff bytes (UTF-8), 3,000 lines, 24,000 context bytes, <128 KiB
request; projects may lower them. Oversize blocks. No chunk coordinator or
combining partial PASS results; preserve custody/budgets.

Run `python3 -m unittest discover -s scripts/pr-review -p 'test_*.py'`.
Offline CI: `.github/workflows/pr-review-roles-tests.yml`. Also verify
executor/adapter, limit and exact-HEAD publication tests; offline success proves
no live provider execution.

Sol replaces this repository's legacy Terra slot in this workflow; application
inference models remain unchanged.

Valid results cannot be reissued. Failed retries retain diagnostics; prepare
again for a new review.

Codex uses structured transport events plus its CLI-designated final-output file.
Tool output and progress text are not review results. Recovered transport notices
remain visible; terminal provider errors still block.
Claude uses [`--output-format json --json-schema`](https://code.claude.com/docs/en/headless)
and accepts only a successful result envelope containing an object in
`structured_output`. There is no prose, fenced-JSON or `.result` fallback.
Outer model-selection/fallback/quota diagnostics remain blocking; reported
`modelUsage`, when present, must include the requested Bedrock profile or its
exact Anthropic model name. This does not attest model weights. The extracted
review still passes the existing nonce, HEAD, role, path, coverage and publication
checks. Failed envelopes and nonzero exits cannot become successful reviews.
Terminal envelope diagnostics are inspected even on nonzero exit and cannot be
erased by a clean retry; ordinary nonzero transient failures retain bounded retries.
The schema bytes count toward the existing complete-request limit.
Codex JSONL records split only at literal LF bytes; Unicode separators inside JSON
strings remain payload. Terminal executor and final-file overflow are handled
before transport parsing or diagnostic concatenation and cannot trigger retry.
The event stream and final-file byte bounds are checked independently before
rejecting malformed/incomplete events; bad framing cannot hide final-file overflow.

## Synchronization and bounded publication

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
Complete PEM spans are masked immediately after control stripping, before any
structured JSON or quoted-fragment decoding at every string level. Adjacent
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
