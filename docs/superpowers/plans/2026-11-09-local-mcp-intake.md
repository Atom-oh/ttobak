# Local MCP intake — implementation plan

Status: historical implementation plan (2026-11-09). Phase 1 is implemented
(ADR-003, runbook `docs/runbooks/local-mcp-intake.md`); later phases are not.
Design: [local MCP intake](../specs/2026-11-09-local-mcp-intake-design.md).
Each phase ships independently and leaves the system consistent.

Conventions for every phase:

- Node: `mcp-server/src/intake/*`, tests in `mcp-server/test/intake-*.test.mjs`
  (`node --test`, run by `npm test`). The bundle check (`scripts/check-bundle.mjs`)
  must still pass.
- Rust: `mac-app/src-tauri/src/intake.rs` (+ pure helpers in a Tauri-free
  module for unit tests), `cargo fmt --check && cargo clippy --all-targets -- -D
  warnings && cargo test`. No CI covers this; report the macOS-only limits.
- Frontend: lint, `npm run test:unit`, `npm run build`.
- Go: `go test ./... -count=1`, `go vet ./...`.
- Secret-canary rule: every parser, runner, status and plist test seeds fixture
  `env`, `args`, `headers` and `url` with a unique sentinel string. It asserts
  the sentinel never appears in stdout, stderr, status JSON, journal, plist or
  thrown error text.
- Fixtures are synthetic. Never commit captured real calendar, SFDC or SpecReq
  output.

---

## Phase 0 — Discovery spike (no repo code; user present)

Goal: replace assumptions with facts before writing parsers.

| Task | Output |
|---|---|
| With user consent, call `tools/list` on aws-outlook-mcp, amazon-meetings-mcp, aws-sentral-mcp and spec-req-mcp (after `mwinit -o`). | Tool names and input schemas only |
| With consent, run one read call per calendar server for a one-day window. Inspect locally whether results carry `structuredContent` or JSON text, a stable **occurrence** ID, cancellation and sensitivity flags, time zones and pagination. | A short shape note (field names and types only); the user picks the canonical calendar server |
| Same for `search_opportunities` (`limit: 2`) and the SpecReq list tool. | Field names; residency decision for SpecReq |
| Observe Midway failure output (expired cookie) for each server. | Error classification patterns (no cookie data) |
| Check whether Codex `otel` and the shell OTel exporter would capture headless prompts and tool output. | Go/no-go note for Phase 5 |

Exit: user answers design §11 Q1–Q3; synthetic fixtures are written that
mirror the observed *shapes*.

---

## Phase 1 — Calendar intake runner, CLI only (smallest useful slice)

Value: the user's Outlook calendar flows into the existing private calendar
every 10 minutes. It needs no backend change and no Mac UI. It is scheduled by
a documented, user-installed LaunchAgent.

### Files

| File | Change |
|---|---|
| `mcp-server/src/index.ts` | Extend argv dispatch: keep `--transport` behavior byte-compatible; add `intake <subcommand>` routing. Usage text update. |
| `mcp-server/src/intake/hosts.ts` | Parsers for Claude JSON (top-level + `projects.*.mcpServers`), Codex TOML (`mcp_servers`, `enabled`), Kiro JSON (`disabled`). Size cap, owner/symlink checks, per-host error isolation, name validation. Two outputs: `listServers()` (name, host, transport, resolvable, disabled) and `resolveServer(host,name)`, which returns the spawn spec in memory only. |
| `mcp-server/src/intake/toml.ts` or a dependency | Bounded TOML read. Prefer a small, pinned, audited dependency (`smol-toml`); confirm with the user before adding it to `package.json`. |
| `mcp-server/src/intake/path.ts` | Relative command resolution against an approved `PATH` list; spawn argv, never a shell. |
| `mcp-server/src/intake/mcp-client.ts` | SDK `Client` + `StdioClientTransport`. Start timeout, call timeout, stdout/result byte caps, stderr discarded (or byte-capped to memory for classification only), child killed on exit. `tools/list` schema guard. |
| `mcp-server/src/intake/errors.ts` | Error codes: `SERVER_NOT_CONFIGURED`, `SERVER_START_FAILED`, `AUTH_EXPIRED_MIDWAY`, `SCHEMA_CHANGED`, `PARSE_FAILED`, `TTOBAK_LOGIN_REQUIRED`, `TTOBAK_CONFLICT`, `TTOBAK_UNAVAILABLE`, `LOCKED`. |
| `mcp-server/src/intake/calendar-connector.ts` | Read tool and arguments for the Phase 0-chosen server; parse to the runbook's normalized event; exclusions; explicit removals from the ledger; batching ≤20; journal before send; identical retry; `complete:true` then checkpoint. Reuse validators from `src/calendar.ts` (export the pure ones). |
| `mcp-server/src/intake/store.ts` | `~/.ttobak/intake/` (0700, files 0600, atomic rename) holding `selection.json`, `calendar/<sourceId>/{ledger,journal,checkpoint}.json`, `status.json` and a lock file. Same ownership and symlink checks as the token cache. |
| `mcp-server/src/intake/run.ts` | Subcommands: `intake hosts` (JSON list, names only); `intake select calendar --host --server` (writes selection); `intake calendar --dry-run` (normalized counts + local preview to stdout, no writes); `intake calendar` (run); `intake status`. Background mode (`--noninteractive`) never opens a browser login. |
| `docs/runbooks/local-mcp-intake.md` | New: setup, dry run, manual acceptance (reuse calendar runbook step 5), LaunchAgent template with only non-secret keys, uninstall. Cross-link from `local-outlook-calendar-sync.md` and `mcp-server/README.md`. |
| `docs/decisions/ADR-003-mcp-access.md` | Decision: deterministic local connectors, config references not copies, AI proposals only, residency fields, stdio-only writes. |

### Tests (node:test)

- `intake-hosts.test.mjs`: each host format; disabled and enabled flags;
  project-scoped Claude entries; malformed file isolation; oversize; symlinked
  file rejected; names-only output; secret canary.
- `intake-path.test.mjs`: relative, absolute and missing commands; no shell
  metacharacter interpretation.
- `intake-mcp-client.test.mjs`: a fake stdio MCP server fixture (a node script)
  covering timeouts, oversize result, missing tool, schema drift and child
  cleanup.
- `intake-calendar.test.mjs`: synthetic shapes covering upsert, reschedule with
  the same ID, cancellation leading to explicit remove, private/confidential
  reclassification, recurring occurrences, all-day DST, a later page failing
  (no completion, no checkpoint), identical retry after an ambiguous send, and
  409 leading to a reconcile state with no rebase. Auth-expired output yields
  `AUTH_EXPIRED_MIDWAY` with zero writes. An empty result never yields removals.
- `intake-store.test.mjs`: permissions, atomic writes, lock contention
  (`LOCKED`) and crash recovery of a pending batch.
- Existing suites unchanged; `--transport` argv compatibility test.

### Real-device verification (user's Mac)

- Dry run against the user's real calendar, then the calendar runbook's manual
  acceptance list with user-approved synthetic events.
- A LaunchAgent run with a minimal environment finds the commands through the
  approved PATH.
- Behavior after sleep/wake (coalesced runs, no overlap).
- Expired Midway reports the right code, with recovery after `mwinit -o`.
- Expired TTOBAK refresh reports `TTOBAK_LOGIN_REQUIRED` and opens no browser.
- `~/Library/Logs/ttobak/` contains no titles, args or env.

---

## Phase 2 — Mac Settings and schedule management

Value: no terminal needed. Discover servers, pick sources, consent, toggle the
schedule and see status.

| File | Change |
|---|---|
| `mac-app/src-tauri/src/intake.rs` | Locate runner (`~/.ttobak/server.mjs` + node path, resolved once from login shell, user-confirmable). Spawn with fixed argv (`intake hosts|status|calendar --dry-run|calendar`), bounded output, timeout. Native confirmation dialogs (Rust-rendered) before enable, LaunchAgent install and B enable. LaunchAgent write/remove (`~/Library/LaunchAgents/click.atomai.ttobak.intake.plist`) and `launchctl bootstrap/bootout gui/$UID`. Never accepts argv, tool names, prompts or paths from IPC. |
| `mac-app/src-tauri/src/intake_plist.rs` (Tauri-free) | Pure plist rendering and validation (fixed keys, PATH dirs only) for unit tests. |
| `mac-app/src-tauri/src/lib.rs` | Register commands `local_intake_list`, `local_intake_get`, `local_intake_set`, `local_intake_run_now`, `local_intake_status`, `local_intake_disable_all`. |
| `mac-app/src-tauri/build.rs`, `capabilities/default.json` | Add the commands and their `allow-*` grants (enforced by `command_acl_tests`). |
| `mac-app/CLAUDE.md` | Wire contract rows and invariants (no generic exec, native consent, references only). |
| `frontend/src/lib/tauri.ts` | Typed wrappers, `isCommandNotFound` fallback for older app builds. |
| `frontend/src/components/LocalIntakeSettings.tsx` | Sections from design §8. Korean copy. Hidden outside Tauri. |
| `frontend/src/app/settings/page.tsx` | Mount at `#local-intake`. |
| `docs/DESIGN-SPEC.md` | New settings section. |

Tests:

- cargo:
  - plist rendering (fixed keys, escaping, no env);
  - IPC argument validation rejects unknown connector, host, server names and
    oversize options;
  - runner output byte cap and timeout;
  - secret canary through status relay;
  - ACL test updated.
- Frontend `node:test`: state mapping from status codes to Korean messages; the
  connector picker only offers compatible discovered pairs; hidden when not
  Tauri.

Real-device: build:signed app; dialogs appear and cannot be bypassed from the
SPA. Also check the macOS background-item notice, LaunchAgent install/remove,
"Run now" overlapping a scheduled run (lock), app quit (schedule continues) and
TCC prompts (none expected).

---

## Phase 3 — SFDC opportunities (backend + connector)

Value: account pages show current opportunity stage and days in stage. Only
ID, name and stage reach the cloud.

Backend (Go, handler → service → repository, expression builder):

| File | Change |
|---|---|
| `backend/internal/model/account.go` / new `sfdc.go` | SFDC account mapping row + reverse lookup; opportunity uniqueness row `SFDCOPP#{id}`; `STAGEHIST#` rows (shared with roadmap project 1; coordinate to avoid two designs). |
| `backend/internal/repository/sfdc.go` | Transactional mapping create/delete with reverse row; conditional opportunity upsert (create project + ProjectRef + uniqueness row in one transaction; update only name/stage/stageChangedAt with a condition). |
| `backend/internal/service/sfdc.go` | Direct-member authorization (re-read account/member), field allowlist, bounded IDs (`^[A-Za-z0-9]{15,18}$`), sentinel errors. |
| `backend/internal/handler/sfdc.go`, `cmd/api/main.go` | `PUT/DELETE /api/accounts/{id}/sfdc-accounts/{sfdcAccountId}`, `GET` mapping list, `PUT /api/accounts/{id}/opportunities/{sfdcOpptyId}`. 64 KiB cap, strict decoding. |
| `mcp-server/src/intake/sfdc-connector.ts` | For mapped accounts, page `search_opportunities` and project the allowlisted fields to the upsert. Idempotent re-runs; no deletes. |
| `mcp-server/src/api.ts` | Client methods; stdio-only MCP tool for mapping (optional). |
| Frontend account page | Mapping UI (member), "SFDC synced Xh ago". |
| Docs | `API-SPEC.md`, `DESIGN-SPEC.md`, ADR-003 amendment. |

Tests: Go table-driven tests for authorization (non-member 404/403, removed
member), condition-failure retry, a concurrent double upsert creating one
project, the field allowlist rejecting `amount`, and stage history appended
only on change. Node: connector projection drops every non-allowlisted field
(canary on amount and contact fields), pagination and auth-expired with no
writes. Real-device: a run against the user's associated opportunities after
mapping 1–2 accounts, with the user verifying stages.

---

## Phase 4 — SpecReq

Gated on Phase 0 results and the residency decision. Mirror Phase 3:
`ACCOUNT#…/SPECREQ#{id}` rows with an allowlisted set, an idempotent upsert,
member reads, `specreq-connector.ts`, account page list and docs. Same test
pattern, including a canary for disallowed text fields.

---

## Phase 5 — Optional AI proposals job (option B)

Value: suggestions for unmapped SFDC accounts and calendar events without an
account. It never writes.

| File | Change |
|---|---|
| `mcp-server/src/intake/proposals/*.ts` | Versioned prompt templates; output JSON Schemas; host adapters building argv for `claude -p` (`--allowedTools` read-only list, `--permission-prompts none`, `--max-budget-usd`, `--no-session-persistence`, `--json-schema`, explicit env unset matching the zsh wrapper), `codex exec` (`--ephemeral -s read-only --output-schema`, `-c` disabling unselected servers) and `kiro-cli chat --no-interactive` (generated agent, `--trust-tools`, `--require-mcp-startup`). No ttobak MCP in the run. |
| `mcp-server/src/intake/proposals/validate.ts` | Strict schema validation; reject IDs not present in the TTOBAK reads the runner fetched itself. Results go to the local `proposals.json`. |
| Mac/Frontend | Phase 2 card for B, consent text (provider, telemetry, cost) and a proposals list linking to TTOBAK apply flows. |

Tests: argv builders (no write tools, no `--dangerously-*`, no ttobak MCP),
schema rejection, injection fixture (a source text instructing "call
update_opportunity" changes nothing because the tool is not allowed and the
output is schema-bound), and cost or exit-code handling including Kiro exit 3.
Real-device: one run per chosen host, inspection of the provider-side telemetry
destination, and a budget cap check.

---

## Not planned

- Scheduled ingestion of builder-mcp, aws-knowledge or aws-documentation
  (interactive use only).
- Highspot (no server exists).
- Slack or mail bodies.
- Remote (`url`) MCP servers in the runner.
- Editing host config files.

## Sequencing and dependencies

Phase 0 → 1 → 2 is the minimum product. Phase 3 depends on the roadmap
project 1 stage-history decision; align `STAGEHIST#` before implementing.
Phase 4 depends on Phase 0. Phase 5 is independent after Phase 2, and is gated on
Phase 0's telemetry note.
