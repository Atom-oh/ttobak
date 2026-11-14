# Admin team sync: directory manager into `custom:department`

- Status: historical design record; approved by the owner 2026-11-14.
- Follows: [One-click intake setup](2026-11-12-one-click-intake-setup-design.md)
  and [SFDC contact sync](2026-11-11-sfdc-contact-sync-design.md) (local adapter
  run started from the Mac app, bounded batch to an authenticated API).
- Decisions touched: ADR-007 (admin user management; which Cognito attributes
  the API writes), ADR-003 (local adapter), ADR-023 (data leaving the Mac).

## Intent

The TTOBAK user pool is also the sign-in directory of two sibling apps:
cc-on-bedrock (shared-pool mode, its ADR-016) and the Claude Code usage
dashboard ("ccdash"). The owner wants one place that says which team each
person is on, filled from the internal employee directory (phonetool), so the
sibling apps can group people by team without each keeping its own list.

## Owner decisions (2026-11-14)

- The value is stored in the existing pool attribute `custom:department`,
  overwriting what is there. No new attribute.
- The value is the person's **manager alias** from the directory, because the
  directory's department name is a cost centre (19 of 35 people shared one
  value). Cost-centre names are not stored.
- Only an **administrator's Mac app** writes it, for every pool user at once.
  Users never set their own value.
- All real `@amazon.com` accounts were backfilled by hand on 2026-11-14 with
  this rule (35 accounts, 19 teams). This feature keeps them current.

## Goals

1. An admin clicks one button in the Mac app and sees, per user, the current
   value and the value the directory gives.
2. After the admin confirms, the changed values are written to
   `custom:department`.
3. The admin user list shows each user's team.

## Non-goals

- A schedule or automatic run. The sync is manual.
- Per-user exceptions or a manual team editor in TTOBAK.
- Any change to cc-on-bedrock or ccdash (see Follow-ups).
- Showing which users have a cc role, or what budget a change affects.
- Syncing name, title, department name, phone or any other directory field.

## Anti-goals

- A signed-in non-admin can change anyone's `custom:department`, including
  their own. cc-on-bedrock uses the value for budgets and manager assignment.
- The API writes any `custom:*` attribute other than `custom:department`.
- A sync silently clears or changes a value the admin did not see in the
  preview.

## Constraints

- **cc-on-bedrock reads the value live.** `budget-check.py` keys department
  budgets by it, `user-role-provisioner.py` groups users by exact equality to
  set `custom:dept_manager_sub`, and instance user data puts it into
  `OTEL_RESOURCE_ATTRIBUTES` (no spaces, commas or `=`). cc validates 1 to 50
  characters. A value with no budget row gets cc's global default caps.
- **App clients cannot write it.** Every client's write attributes are `name`
  and `email` (`infra/lib/auth-stack.ts`); cc's plan-time postcondition
  requires that. Writes go through the API's `AdminUpdateUserAttributes`.
- **No IAM change.** The API role already holds
  `cognito-idp:AdminUpdateUserAttributes` on the pool (`ai-stack.ts`). The
  rule that the service writes only `name` is a code and ADR-007 rule, and it
  changes here to: `name`, plus `custom:department` through this endpoint only.
- **The directory is reachable only from the admin's laptop** (corporate
  network and Midway). No Lambda can read it.
- **The Mac window loads the remote SPA**, so every Tauri command is callable
  from that origin. The Mac command is a convenience; the API's `RequireAdmin`
  is the authorization boundary.
- Cognito has no conditional attribute write. A check of the previous value is
  read-then-write and can race another writer (accepted; see Apply).

## Approaches

1. **Admin Mac app runs the local adapter (chosen).** A new adapter command
   reads the pool users from the admin API, looks each alias up through a local
   directory MCP server, and posts the result to a new admin-only endpoint.
   Reuses the intake runner's spawn, progress and kill machinery. Trust in the
   value stays at admin level, which is what cc's ADR-016 assumes of the owning
   stack.
2. **Each user's Mac app writes their own value.** No admin step, but the
   server cannot verify a directory value, so any signed-in user could choose
   their cc budget department by calling the API. Users without the Mac app
   stay empty. Rejected by the owner.
3. **Operator script with AWS credentials only.** This is how the 2026-11-14
   backfill was done. No code in TTOBAK, but it needs local AWS admin
   credentials and has no preview in the product. Kept as the recovery path,
   not the feature.

## Design

### Value rule

For a pool user whose verified email is `<alias>@amazon.com`:

- directory says the person manages others → `team = <alias>` (a manager is
  grouped with their own team);
- otherwise → `team = <manager alias>`;
- alias not found in the directory, or no manager → no value proposed; the
  user is reported as `notFound` and left unchanged.

Users with any other email domain are never read from the directory and never
written. A value must match `^[a-z0-9][a-z0-9-]{0,49}$`; the adapter lowercases
the alias and the server enforces the pattern.

### Flow

```
Admin panel (SPA, Mac app, admin)      Mac app (Rust)         Adapter (node)                 API
[Sync teams] ───────────────────────▶ team_sync_run{preview} ─▶ GET /api/settings/users
                                                                directory lookup per alias
      preview table ◀──────────────── final JSON line ◀──────── {changes, unchanged, notFound}
[Apply N changes] ──────────────────▶ team_sync_run{apply,     ─▶ POST /api/settings/users/team-sync
                                       previewId}                 {items:[{userId, team, expected}]}
      result ◀─────────────────────── final JSON line ◀──────── {updated, conflicts, rejected}
```

### API (Go)

- `GET /api/settings/users` (existing, admin): each user gains `department`
  (the raw `custom:department`, empty when unset).
- `POST /api/settings/users/team-sync` (new, inside the `RequireAdmin` group):
  - body: `{ "items": [ { "userId", "team", "expected" } ] }`, at most 200
    items, body at most 64 KiB; duplicate `userId` rejects the request;
  - per item, in `UserAdminService`: `AdminGetUser`; reject unless the user
    exists, `email_verified` is true and the email ends with `@amazon.com`;
    reject a `team` that fails the pattern; if the current value differs from
    `expected`, report a conflict and write nothing; if it already equals
    `team`, count it unchanged; else `AdminUpdateUserAttributes` with exactly
    one attribute, `custom:department`;
  - response: `{ "updated": [userId], "unchanged": n, "conflicts": [userId],
    "rejected": [{ "userId", "code" }] }`. Per-item failures do not fail the
    request; a Cognito error on one item is reported for that item and the
    rest continue;
  - one log line per write: actor sub, target sub, old and new value. No email.
- Errors use the standard shape and sentinel errors. Self-targeting is allowed
  (an admin is a pool user like any other).

### Adapter (`mcp-server/src`)

- New command `ttobak-mcp team-sync run [--apply --preview-id <id>]`,
  using the stored login like intake. A non-admin login gets the API's 403 and
  the run ends with a clear code.
- Directory connector: `builder-mcp` resolved from the login PATH (it is not
  in the host files), one allowlisted tool, `ReadInternalWebsites`, called
  with exactly one fixed-prefix URL per validated alias, and a required-field
  check (alias, is-manager flag, manager alias). The adapter composes the URL;
  no model does. Lookups run sequentially with a bounded timeout; at most 500
  users per run.
- Preview stores its result in the private intake store (0600) and prints
  `{ok, previewId, changes:[{userId, alias, from, to}], unchanged,
  notFound:[alias], skippedNonDirectory}`. Apply takes only the preview ID,
  sends `from` as `expected`, and removes the stored preview. A preview older
  than 15 minutes is refused. No path crosses the WebView.
- Only user IDs and team values are sent to the API. Directory responses are
  read for the three fields above and discarded; nothing else is logged,
  stored or sent.

### Mac app (Rust)

- Commands `team_sync_run` (`mode` preview/apply, typed; apply carries the
  preview ID) and `team_sync_cancel`, spawning through
  `intake_runner::run_child` in their own run slot, as report runs do
  (`RUN_IN_PROGRESS` while a team sync goes). Events `team-sync://progress`
  and `team-sync://finished`.
- Listed in `build.rs` COMMANDS with `allow-*` in `capabilities/default.json`.

### Admin panel (SPA)

- User table gains a Team column from `department`.
- A [Sync teams] button, shown only to admins inside a Mac app that has the
  command. It opens a preview listing each change as `alias: from → to`, the
  not-found aliases, and a count of unchanged users. [Apply] writes; the result
  lists updated users, conflicts and rejections. With no changes, there is no
  Apply button.
- Copy states that cc-on-bedrock budgets follow this value.

## Rollout order

API, then adapter bundle, then SPA, then a Mac app release. The SPA hides the
button when the Mac command is missing, so an older app shows only the column.

## Tests

- Go: table tests with the mock Cognito client: admin gate, pattern, non-amazon
  and unverified email rejection, conflict, unchanged, per-item Cognito error,
  size and duplicate limits, and that only `custom:department` is written.
- Adapter: value rule (manager, non-manager, not found), preview/apply
  binding and expiry, tool allowlist, no extra directory field in any output.
- Rust: command ACL tests and typed-argument validation. No CI covers the Mac
  module; say so in the PR.

## Documentation

`docs/API-SPEC.md` (endpoint and list field), ADR-007 (attribute write rule
and this endpoint), ADR-003 (new adapter command), `mac-app/CLAUDE.md` wire
table, `docs/DESIGN-SPEC.md` (panel), `CHANGELOG.md`, and the comments in
`auth-stack.ts` and `ai-stack.ts` that say the service never writes `custom:*`.

## Left to the builder

- File, type and command names beyond those above; preview table layout.

Checked 2026-11-14: builder-mcp `ReadInternalWebsites` on
`phonetool.amazon.com/users/<alias>` returns `login`, `is_manager` and
`manager.login`. `a-to-z-people-data-mcp` offers workforce reports, not an
alias lookup, and is not used.

## Follow-ups (other repositories, not this work)

- cc-on-bedrock sends `department=` but not `team=` in telemetry; ccdash groups
  by the `team` resource attribute and does not read Cognito. One of the two
  must change before ccdash shows these teams.
- cc-on-bedrock department budgets are keyed by the old values (`default`,
  `hana`). Budgets for the new team values are set in cc's admin UI.
