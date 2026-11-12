# One-click Mac intake setup with org and personal skill packages

- Status: historical design record; draft for owner approval (2026-11-12).
- Follows: [Mac intake settings and SA report](2026-11-11-mac-intake-settings-and-sa-report-design.md).

## Why

On 2026-11-11 the owner's SA report and intake failed for five separate local
reasons, each fixed by hand:

1. The app's login-shell `PATH` (`$SHELL -l -c`, cached as `loginPath` in the
   app's `intake.json`) lacks `~/.toolbox/bin`, which only `~/.zshrc` adds.
   The AIM wrappers in `~/.aim/mcp-servers` `exec aim ...`, so `slack-mcp`
   and `amazon-meetings-mcp` exited 127 and intake reported
   `SERVER_START_FAILED`. Fixing `~/.zshenv` does not help an app that has
   already cached `loginPath`.
2. Pressing the adapter [Update] left `~/.ttobak/server.mjs` unchanged with
   no visible error, although `fetch_adapter` works against production.
3. The `sa-performance-report` skill was an old layout (`SKILL_OUTDATED`).
   The owner had to find and run the internal-mcp-setup installer by hand.
4. Report settings had Bedrock on without an AWS profile. The CLI ended with
   `Request timed out`, shown only as a generic failure (`REPORT_INVALID`).
5. Report Slack statistics include channels unrelated to work.

The owner wants one [Setup] button that leaves the Mac ready to collect and
report, with no terminal steps, and a way to publish new skill packages.

## Goals

- One [Setup] button in the Mac app performs, in order and with visible
  per-step results: PATH repair, adapter refresh, skill package install,
  missing MCP server install, then the existing `intake setup` (connector
  check, first run, optional schedule).
- Skill packages come from TTOBAK, never from the user's Slack, mail or
  Downloads folder.
- Two package levels:
  - **Organization package:** uploaded by an admin; the default for every
    user. Seeded with internal-mcp-setup v1.5.0 (2026-11-10).
  - **Personal package:** uploaded by any user for themselves; never visible
    to or applied for anyone else.
- Effective package = merge of org and personal:
  - Skills merge by skill directory name. A personal skill replaces the org
    skill of the same name; org skills absent from the personal package stay.
  - MCP server lists are unioned: a server listed in either package is
    installed when missing. The merge never removes a server.
- Report Slack channels: by default every channel where the owner posted in
  the last three months is included; the owner picks channels to exclude
  (for example a hobby channel). The exclusions are honored by every report
  run.
- Bedrock cannot be enabled without an AWS profile, and a Bedrock timeout
  has its own message.

## Non-goals

- Searching Slack (or any other user data source) for packages. Forbidden by
  the owner.
- Running `setup-internal-mcp.sh` or any other script from a package. The app
  reads only the package's `skills/` tree and its server lists.
- Editing the user's shell startup files.
- Removing skills or MCP servers the user installed outside TTOBAK.
- Windows/Linux support; the Mac app only.
- Changing which MCP tools a report run may call (see Constraints).

## Anti-goals (failure even if it works)

- A package upload becomes code execution on other users' Macs without the
  receiving user pressing [Setup].
- A personal package changes anything for another user.
- A failed step is reported as success, or the button ends silently.
- Skill files overwritten without a backup.

## Package format and validation

A package is a zip in the internal-mcp-setup layout:

```
internal-mcp-setup-vX.Y.Z/
  README.md
  setup-internal-mcp.sh      # read as text only, never executed
  skills/<name>/SKILL.md ... # one or more skills
```

- The server list comes from an optional `ttobak-manifest.json` at the
  package root (`{"version","toolboxRegistries":[],"toolboxServers":[],
  "aimServers":[],"httpServers":{name:url}}`). Without it, the lists are
  parsed from `setup-internal-mcp.sh`'s literal `TOOLBOX_REGISTRIES`,
  `TOOLBOX_MCPS_BASE`, `AIM_MCPS_BASE` and `HTTP_MCPS` arrays with a strict
  grammar (quoted or bare tokens, no expansion); anything else in the script
  is ignored. The `*_EXTRA` (`--all`) arrays are not installed.
- Version comes from the manifest, else the script's `VERSION="..."` line,
  else the top-level folder name.
- Validation (server on upload, and again on the Mac before install):
  - zip at most 20 MiB, at most 500 entries, total uncompressed at most
    50 MiB, no absolute paths, no `..`, no symlinks or special files, one
    top-level folder;
  - each `skills/<name>` has `SKILL.md`; skill names and server names match
    `^[a-z0-9][a-z0-9-]{0,63}$`; registry values are `s3://` URLs; HTTP
    server URLs are `https://`;
  - at least one skill.
- If `sa-performance-report` is present it must have the files in
  `SKILL_LAYOUT` (`mcp-server/src/report/claude.ts`); otherwise the upload is
  rejected with a message naming the missing files.

## Server side (TTOBAK)

- Storage: S3 under a new private prefix, for example
  `skillpkg/org/<sha256>.zip` and `skillpkg/user/<userId>/<sha256>.zip`.
  Metadata rows in `ttobak-main` hold the current org package and each
  user's current personal package (version, sha256, size, uploader,
  uploadedAt, skill names, server lists). Keep the last few org versions for
  rollback (builder's call on how many).
- Routes (all authenticated; admin via `RequireAdmin` on verified
  `cognito:groups`):
  - `GET /api/skill-packages/effective` returns both metadata records and
    short-lived download URLs for each zip.
  - `PUT/DELETE /api/skill-packages/personal` (upload through a presigned
    POST plus a completion call that validates the object).
  - `PUT /api/skill-packages/org` and rollback for admins.
- The seed org package is v1.5.0; the builder decides whether it is seeded
  by an admin upload after deploy or by a one-time script, but it is not
  committed as a static public file.
- No new public route; downloads use the existing signed-URL pattern.
  Update API-SPEC, INFRA-SPEC (new S3 prefix, OAC allowlist if served via
  `/media/*`) and record package trust and merge in ADR-003, the record that
  owns local intake (decision records are topic records, ADR-001 to ADR-030).

## Web UI

- Settings, new "Skill packages" section (desktop and in-app SPA):
  - shows org version and date, my personal version (or none), and the
    effective skill list with the source of each skill (org or personal);
  - [Upload personal zip] and [Delete personal package];
  - admins also see [Upload org zip] and org version history with rollback.
- Validation errors are shown in Korean with the failing rule.

## Mac app: the [Setup] button

One run, its own progress list (reusing the intake progress view), each step
ending ok / skipped / failed with a Korean reason. A failed step stops later
steps that depend on it; independent steps still run (builder's call on the
dependency graph, but PATH and adapter come first).

1. **PATH repair.** Recapture the login-shell PATH on every press (do not
   reuse the cached `loginPath`), then append, when they exist and are
   missing, `~/.toolbox/bin`, `~/.aim/mcp-servers` and `~/.local/bin`. Store
   the result as `loginPath`. This is the PATH for every later step and for
   intake and report runs.
2. **Adapter refresh.** Always fetch and install the adapter (existing
   `fetch_adapter`/`install_adapter`), reporting installed / already current
   / failed. Investigate and fix why the separate [Update] button showed
   no effect on 2026-11-11 (the fetch itself succeeds), and make that button
   report its result too.
3. **Package fetch.** Ask TTOBAK for the effective packages (via the adapter
   CLI, which already holds the TTOBAK login), download, verify sha256 and
   re-validate locally.
4. **Skill install.** For each effective skill, copy into
   `~/.claude/skills/<name>/` only when content differs; first copy the
   existing directory to `~/.claude/skills-backup/<name>-<timestamp>/`.
   Files present locally but absent from the package are kept (matches the
   installer). Never write outside `~/.claude/skills` and the backup folder;
   check containment before writing.
5. **MCP servers.** For the unioned lists, install only what is missing,
   using fixed argv (no shell):
   - toolbox registries: `toolbox registry add <s3url>` when absent from
     `toolbox registry list`;
   - toolbox servers: `toolbox install <name>` when absent from
     `toolbox list --installed`, then `claude mcp add -s user <name> -- <name>`
     when absent from `claude mcp list`;
   - AIM servers: `aim mcp install --clients claude-code <name>` when absent
     from `~/.aim/config.json` (the installer's rule);
   - HTTP servers: `claude mcp add -s user -t http <name> <url>`.
   Owner decision: before installing, the app lists every registry, server
   and HTTP URL that is neither already present nor previously approved on
   this Mac, and installs only after the user presses [Install]. Present or
   previously approved items install without asking. Approvals are stored
   locally per item.
   Midway must be valid; if not, stop this step with the existing
   `AUTH_EXPIRED_MIDWAY` message (`mwinit -o`).
6. **Intake setup.** Run the existing `intake setup --noninteractive`, which
   re-detects servers with the new PATH and re-enables sources that now
   start (today's run disabled meetings and Slack after the failure).

The button is idempotent: a second press with nothing new reports every step
as skipped or current.

## Report Slack channel exclusions

- Settings, report options: [Slack channels] lists channels where the owner
  posted in the last three months (via the owner's `slack-mcp`
  `search from:<@me> after:<date>` counts per channel, respecting the
  15-per-minute limit), all checked; the owner unchecks channels to exclude.
  Refresh on demand; the list is cached locally.
- Exclusions are stored locally in the app's intake settings (owner-private;
  channel names only). The report CLI gets them as an argument (for example
  `--exclude-slack-channel` repeated, bounded count and length) and passes
  them into the run prompt, and the stored report JSON must not contain
  excluded channels (validation drops or rejects them; builder's call).
- Semantics are an exclusion list (owner decision): every channel is
  included unless excluded. The three-month list is only the picker's
  candidate set; a channel active earlier in a half-year or year period but
  not in the last three months is still included.

## Report Bedrock guard

- The report dialog cannot save Bedrock on without a profile.
- The CLI maps a failed result whose text is a timeout while Bedrock is on to
  a new code (for example `CLAUDE_CLI_BEDROCK`) with a Korean message to pick
  a profile or turn Bedrock off.
- The existing `useBedrock: true, awsProfile: null` setting is shown as
  needing attention instead of silently running.

## Constraints

- CLAUDE.md security rules: no new unauthenticated route; S3 Block Public
  Access and OAC kept; admin checks on verified JWT groups; validate keys and
  ownership server-side; bounded request sizes; no secrets in code.
- The report run's tool allowlist (`mcp-server/src/report/tools.ts`) is a
  security boundary. New skill versions may reference servers or scripts it
  does not allow (v1.5.0 adds `interview_count.py`, `pfr_ci_summary.py` and
  mentions `ccmt-mcp`). Installing a package never widens the allowlist at
  runtime. Owner decision: this work includes a separately reviewed
  allowlist change for v1.5.0's read-only additions (`interview_count.py`,
  `pfr_ci_summary.py`, and only the read tools of `ccmt-mcp` that its
  data-sources call), so a v1.5.0 report works end to end. Candidate data
  stays out of the report as the skill requires. For later packages the
  setup result says when the installed skill references tools the run will
  deny.
- Mac changes have no CI; native validation needs the owner's Mac.
- Keep `~/.ttobak/server.mjs` the only adapter path and its origin pinning.

## Inputs and decisions made by the author (owner may override)

- The v1.5.0 zip is the owner's `~/Downloads/internal-mcp-setup-v1.5.0.zip`.
  Its internal skill text is not committed: tests use synthetic packages with
  the same layout and script grammar. Capture real
  outputs of `toolbox registry list`, `toolbox list --installed`,
  `claude mcp list` and the `~/.aim/config.json` shape on the owner's Mac
  as fixtures before writing the "already present?" checks.
- Users without `toolbox`/`aim` (non-Amazon accounts): steps 5 and the
  Midway check report "skipped (no toolbox or aim)"; skill install still
  runs. The package routes are available to every authenticated user; only
  org upload is admin-only.
- The 2026-11-11 [Update] failure has no logs; the builder adds result
  reporting first, then reproduces on the owner's Mac.

## Left to the builder

- Exact route names, row keys, version retention, and whether the zip
  validator lives in Go (upload) and Rust/TypeScript (Mac) or is shared via
  the adapter.
- Whether steps 3 to 5 run in the Rust app or in the adapter CLI (the CLI
  already has TTOBAK auth and Node zip handling); the Rust side must still
  show per-step results.
- Step dependency handling and progress copy.
