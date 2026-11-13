# Customer Update Drafts, Plan 5: Mac Slack link collection

Status: historical implementation plan for 2026-11-13; current behavior is in the code and ADR-026.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let a user pick Slack channels in the Mac app; the local intake reads new messages there, extracts public links and uploads only those links.

**Architecture:** A new `links` sync connector in the `ttobak-mcp` intake adapter talks to the user's own Slack MCP server with two read tools. Channel selection and per-channel cursors stay under `~/.ttobak/intake`. Each selected channel is registered as a `slack` source in TTOBAK with a label and an opaque local ID. The Mac app adds two typed Tauri commands and a Mac-only settings block.

**Tech Stack:** TypeScript (`mcp-server`, `node --test`), Rust (Tauri 2), Next.js.

**Spec:** `docs/superpowers/specs/2026-11-13-customer-update-drafts-design.md`

**Depends on:** Plan 1 merged (source and link routes). Independent of Plans 2-4.

## Global Constraints

- Korean UI strings are keys in `contracts/customer-updates/ui-copy-ko-v1.json`; names in backticks such as `mac.links.save` refer to that file.
- Slack message text, author, thread, permalink and channel ID never leave the Mac and are never written to disk, status files or logs. Only filtered `https` URLs and a `seenAt` time are uploaded.
- Slack tools allowed for this connector: `list_my_channels` and `get_messages` only. No write tool, no search.
- First collection of a channel reads at most the last 3 days and uploads at most 50 links. Later runs upload at most 200 links per channel per run.
- At most 20 selected channels. Label at most 100 code points.
- Rust spawns the adapter with argv, never a shell. The remote SPA passes typed values only. A new command needs all four registrations (`#[tauri::command]`, `generate_handler!`, `build.rs` `COMMANDS`, `capabilities/default.json`).
- A cursor advances only after its links were uploaded successfully.
- Mac changes have no CI. Report that limit; do not claim a Mac build passed unless it was run on macOS.

## Review Focus

1. A message containing `<https://example.com/a|read this>` and a bare URL must yield both links once each.
2. An upload that fails halfway must not advance the cursor, and the next run must not lose or duplicate links (the server de-duplicates).
3. A channel the user left, or a renamed channel, must report a per-channel result and not fail the other channels.
4. An expired Midway session must surface as `AUTH_EXPIRED_MIDWAY`, as other connectors do, with no retry loop.
5. An older Mac app opening the new settings block must show an update hint (`isCommandNotFound`), not an error.

---

## File Structure

| File | Responsibility |
|---|---|
| `mcp-server/src/intake/link-extract.ts` | Pure: URL extraction and the Mac-side filter |
| `mcp-server/src/intake/links.ts` | Channel listing, selection, collection, cursors |
| `mcp-server/src/intake/connectors.ts`, `run.ts`, `setup.ts` | Register the connector |
| `mcp-server/src/api.ts` | Source and link upload calls |
| `mac-app/src-tauri/src/intake_plan.rs`, `intake_runner.rs`, `lib.rs`, `build.rs`, `capabilities/default.json` | Typed actions and commands |
| `frontend/src/lib/intake.ts`, `frontend/src/lib/tauri.ts` | Command wrappers |
| `frontend/src/components/SlackLinkSourcesSettings.tsx` | Mac-only channel picker |

---

### Task 1: Link extraction and filter

**Files:**
- Create: `mcp-server/src/intake/link-extract.ts`, `mcp-server/test/intake-link-extract.test.mjs`

**Interfaces:**
- Produces:
  - `INTERNAL_HOST_SUFFIXES: readonly string[]` mirroring `contracts/customer-updates/internal-hosts-v1.json`.
  - `extractUrls(text: string): string[]`: handles Slack's `<url>` and `<url|label>` forms and bare URLs, unescapes `&amp;`, strips trailing `.,)!?]`, de-duplicates in order, returns at most 20 per message.
  - `publicUrl(raw: string): string | null`: returns the URL when it is `https`, has no user info, has a dotted non-IP host, is not an internal suffix and is at most 2,048 characters; otherwise `null`.

- [ ] **Step 1: Write failing tests:**

```js
assert.deepEqual(extractUrls('see <https://a.example/x|read this> and https://b.example/y.'),
  ['https://a.example/x', 'https://b.example/y']);
assert.deepEqual(extractUrls('<https://a.example/x?p=1&amp;q=2>'), ['https://a.example/x?p=1&q=2']);
assert.equal(publicUrl('http://a.example/x'), null);
assert.equal(publicUrl('https://team.slack.com/archives/C1/p1'), null);
assert.equal(publicUrl('https://w.amazon.com/bin/view/X'), null);
assert.equal(publicUrl('https://10.0.0.1/x'), null);
assert.equal(publicUrl('https://aws.amazon.com/about-aws/whats-new/2026/11/x/'),
  'https://aws.amazon.com/about-aws/whats-new/2026/11/x/');
```

Add a test that `INTERNAL_HOST_SUFFIXES` equals the contract file's `suffixes`.

- [ ] **Step 2: Run** `(cd mcp-server && npm test)`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run again.** Expected: PASS.
- [ ] **Step 5: Commit** `feat(intake): extract and filter public links`.

### Task 2: API calls and the links connector

**Files:**
- Create: `mcp-server/src/intake/links.ts`, `mcp-server/test/intake-links.test.mjs`
- Modify: `mcp-server/src/api.ts`, `mcp-server/src/intake/connectors.ts`, `mcp-server/test/fixtures/intake-work-server.mjs` (add the two read tools behind `FAKE_WORK_*` variables)

**Interfaces:**
- `api.ts`: `listCustomerUpdateSources()`, `addCustomerUpdateSource(body: {kind: 'slack'; label: string; localSourceId: string})`, `deleteCustomerUpdateSource(sourceId: string)`, `uploadCustomerUpdateLinks(body: {sourceId: string; links: {url: string; seenAt: string}[]})` returning `{accepted, duplicate, rejected}`.
- `connectors.ts`: `ConnectorId` gains `'links'`; `export const LINKS_TOOLS: ToolExpectation[] = [{ name: 'list_my_channels', properties: [] }, { name: 'get_messages', properties: ['channel', 'since', 'limit', 'cursor', 'includeThreadReplies'] }]`.
- `links.ts`:
  - `listChannels(session: McpSession): Promise<{id: string; name: string}[]>` calls `list_my_channels` with `{excludeArchived: true, compactOutput: true}` and flattens the sections. An unrecognized shape throws `IntakeError('SCHEMA_CHANGED', …)`.
  - `selectChannels(store, api, choices: {id: string; label: string}[], now): Promise<LinksSelection>`: validates (at most 20, id `^[A-Z0-9]{6,20}$`, label 1-100 code points), registers a cloud source for each new channel with a fresh UUID as `localSourceId`, deletes the cloud source of each removed channel, and stores `{channels: [{id, label, localSourceId, sourceId}]}` in `selection.json` under `links`.
  - `collectLinks(deps: {store, api, session, scope, now, dryRun}): Promise<LinksResult>` with `LinksResult{channels: {label, result: 'ok' | IntakeCode, messages, links, uploaded, rejected}[]}`.
- Cursor ledger: `['links', scope, 'cursors.json']` mapping channel ID to the ISO time of the newest processed message. Scope is `accountScope(apiUrl, subject)`.

Collection per channel: `since` = cursor, or now minus 3 days on the first run. Call `get_messages` with `{channel: id, since, limit: 200, includeThreadReplies: false}`, following `cursor` for at most 5 pages. For each message read `text` and its time (`ts` as epoch seconds, or an ISO `timestamp`); a message with neither a string `text` nor a usable time is skipped; a result without a `messages` array throws `SCHEMA_CHANGED`. Extract, filter, de-duplicate, cap (50 on the first run, 200 otherwise, oldest first). Upload in batches of 200. Advance the cursor to the newest message time only when every batch succeeded and `dryRun` is false. One channel's error is recorded as its `result` and the loop continues.

- [ ] **Step 1: Write failing tests** with a fake session and fake API (the `fakeSlack()` / `fakeTaskApi()` style of `intake-tasks.test.mjs`): first run sends `since` 3 days back and caps at 50; a second run sends the stored cursor; an upload error leaves the cursor unchanged and marks only that channel; `dryRun` uploads nothing and keeps the cursor; the store files contain no message text (read every file under the temp home and assert the fixture text is absent); `selectChannels` adds one source, removes one, and rejects a 21st channel and a lowercase ID; a result without `messages` gives `SCHEMA_CHANGED`.
- [ ] **Step 2: Run** `(cd mcp-server && npm test)`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run again.** Expected: PASS.
- [ ] **Step 5: Commit** `feat(intake): add Slack links connector`.

### Task 3: CLI wiring

**Files:**
- Modify: `mcp-server/src/intake/run.ts`, `setup.ts`, the CLI entry that dispatches `intake` subcommands, `mcp-server/test/` (CLI tests beside the existing intake ones), `docs/runbooks/local-mcp-intake.md`

**Interfaces:**
- `SYNC_CONNECTORS` becomes `['calendar', 'sfdc', 'links']`; `--only links` is accepted; `RunOutput.results.links` carries `{result, channels, links, uploaded}`.
- `links` reuses the Slack server selection (`DEFAULT_SERVERS.slack`) and opens its own session with `LINKS_TOOLS`. With no selected channel it returns `{result: 'ok', channels: 0}` without opening a session.
- `setup.ts`: `LABELS.links` is copy key `mac.links.setupLabel`; setup verifies the two tools exist and never selects channels.
- New subcommands, each printing one JSON line:
  - `ttobak-mcp intake links channels` → `{ok: true, channels: [{id, name}]}`
  - `ttobak-mcp intake links select --channel <id>=<label>` (repeatable; no `--channel` clears the selection) → `{ok: true, channels: [{id, label}]}`

- [ ] **Step 1: Write failing tests:** `intake run --only links` with no channels returns `ok` and opens no session; `links select --channel C0123456=whats-new` stores the selection and calls `addCustomerUpdateSource` once; a malformed `--channel` value gives `INVALID_ARGUMENT`; `links channels` prints names and IDs; setup with a Slack server lacking `get_messages` reports `SCHEMA_CHANGED` for `links` only.
- [ ] **Step 2: Run** `(cd mcp-server && npm test)`. Expected: FAIL.
- [ ] **Step 3: Implement** and document the commands in the runbook's "Advanced: manual commands".
- [ ] **Step 4: Run** `(cd mcp-server && npm test)` and `python3 scripts/docs/check_docs.py`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(intake): wire links connector into run and setup`. Open the adapter PR.

### Task 4: Mac app commands

**Files:**
- Modify: `mac-app/src-tauri/src/intake_plan.rs`, `intake_runner.rs`, `lib.rs`, `build.rs`, `capabilities/default.json`

**Interfaces:**
- `intake_plan.rs`: `Connector::Links` (`as_str` → `"links"`, included in `ALL`); `ChannelChoice { id: String, label: String }`; `IntakeAction::LinksChannels` with argv `["intake", "links", "channels"]`; `IntakeAction::LinksSelect { channels: Vec<ChannelChoice> }` with argv `["intake", "links", "select", "--channel", "<id>=<label>", …]`. `ChannelChoice::validate()` requires an ID matching `^[A-Z0-9]{6,20}$` and a label of 1-100 characters with no control characters and no `=`.
- Commands: `intake_links_channels() -> Result<Vec<ChannelView>, IntakeUiError>` with `ChannelView { id, name }`; `intake_links_select(channels: Vec<ChannelChoice>) -> Result<Vec<ChannelChoice>, IntakeUiError>`. Both take the intake lock that `intake_run_now` uses and return the `mac.links.busy` message when it is held.
- `intake_get_state`'s selection view includes the stored `links` channels as `{id, label}`.

- [ ] **Step 1: Write failing unit tests:** argv for both actions; `validate` rejects `c0123`, an empty label, a label containing `=` and 21 channels; `command_acl_tests::registered_commands_are_declared_and_granted` fails until all four registrations exist.
- [ ] **Step 2: Run** `(cd mac-app/src-tauri && cargo test)`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `(cd mac-app/src-tauri && cargo fmt --check && cargo clippy --all-targets -- -D warnings && cargo test)`. Expected: PASS on macOS. On Linux, report which of these could not run.
- [ ] **Step 5: Commit** `feat(mac): add Slack link channel commands`.

### Task 5: Mac-only settings block and docs

**Files:**
- Create: `frontend/src/components/SlackLinkSourcesSettings.tsx`
- Modify: `frontend/src/lib/intake.ts` (`IntakeConnector` and `INTAKE_CONNECTORS` gain `'links'`; `listLinkChannels()`, `selectLinkChannels(channels)`), `frontend/src/lib/tauri.ts` if the wrappers live there, `frontend/scripts/intake.test.mjs`, `frontend/src/components/CustomerUpdateSourcesSettings.tsx` (render the block inside the same section), `docs/DESIGN-SPEC.md`, `docs/decisions/ADR-003-mcp-access.md` (the connector and its two read tools)

Behavior: the block renders only when `isTauri()` is true. It shows the selected channels with their labels; the `mac.links.load` button calls `listLinkChannels()` and shows a searchable checkbox list; checking a channel adds a label field prefilled with the channel name and editable; the `mac.links.save` button calls `selectLinkChannels`. Fixed note under the list: `mac.links.note`. `isCommandNotFound` shows `mac.links.updateApp`. Selecting more than 20 disables save with `mac.links.tooMany`.

- [ ] **Step 1: Write the failing test** in `intake.test.mjs`: `INTAKE_CONNECTORS` includes `'links'`, and the selection-view parser accepts a `links` entry with channels.
- [ ] **Step 2: Run** `(cd frontend && npm run test:unit)`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `(cd frontend && npm run lint && npm run test:unit && npm run build)` and `python3 scripts/docs/check_docs.py`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(customer-updates): add Mac Slack channel picker`. Open the Mac and web PR.

## Owner acceptance on the Mac

1. Update the adapter (`~/.ttobak/server.mjs`) and rebuild the Mac app.
2. In Settings, load channels, select `#aws-korea-summarizer` and `#aws-whats-new`, save.
3. Run the collect-now button (`intake.runNow`). Confirm the `links` result shows uploaded counts, and that `~/.ttobak/intake` contains no message text.
4. In the web Settings, confirm two Slack sources are listed. After Plans 2-4 are live, confirm drafts appear.
5. If `get_messages` returns a shape the connector does not recognize (`SCHEMA_CHANGED`), capture the field names only (not message content) and report them; the parser's accepted fields are in `links.ts`.
