# Mac app install and auto-update — design

Status: proposal (2026-11-10). Historical design record once implemented; the ADR
and runbook it produces become the current references.

## Intent

The TTOBAK Mac app (`mac-app/`, Tauri 2) is built and installed by hand on one
Mac today. The owner wants to hand it to teammates and keep everyone current
without walking each person through a rebuild:

- A teammate installs it with one command copied from the TTOBAK web app.
- When a new version is published, the app tells them, and updating is one click.
- The owner publishes a new version from their own Mac with one command.

The window loads the production web app remotely, so web UI changes already
reach every Mac on each frontend deploy. Updates are only needed for the native
side (recording, Core Audio, meeting detection, mute follow, upload). Releases
will therefore be occasional, not continuous.

## Audience and distribution decision

- Users: the owner and teammates. Not the public.
- Code signing: one long-lived self-signed code-signing certificate ("TTOBAK
  Dev", already supported by `mac-app/scripts/sign.sh`). No Apple Developer ID
  and no notarization for now.
- Why this works: files fetched by `curl` or by the in-app updater carry no
  quarantine attribute, so Gatekeeper does not assess them. macOS privacy grants
  (Microphone, System Audio Recording, Accessibility) are bound to the
  designated requirement (bundle ID + certificate leaf), which stays stable as
  long as every release is signed with the same certificate.
- Moving to Developer ID later (outside-team distribution, browser downloads, or
  a managed-Mac policy that requires notarization) changes the designated
  requirement once; every user re-grants permissions once. That migration is
  out of scope.

## Goals

1. **Install:** the web app shows a copyable command
   `curl -fsSL https://ttobak.atomai.click/mac/install.sh | sh`. It installs or
   replaces `/Applications/TTOBAK.app` and launches it.
2. **Update notice:** when the app finds a newer version it shows a popup with
   the version and short release notes, and two buttons: **업데이트** and
   **나중에**.
   - 업데이트: download, verify, install, relaunch.
   - 나중에: close the popup. The newer version stays announced in the app
     menu (see 4) until installed.
3. **Never interrupt a recording.** No popup while recording, finalizing or
   uploading; it appears once that work ends. Install never starts while any of
   that is in progress, including from the menu.
4. **App menu:** the TTOBAK app menu (macOS menu bar) always has an update item:
   - no known update: "업데이트 확인…" (manual check, with a result message
     either way);
   - update known: "업데이트 가능 (vX.Y.Z)…" which opens the same popup.
5. **Tag-driven release:** pushing a `mac-v<version>` tag publishes that
   build; only published tagged builds are announced (see Release trigger).
6. **Integrity:** an update is installed only if its minisign signature verifies
   against the public key compiled into the app, and it was fetched over HTTPS
   from the TTOBAK domain. The installer verifies the archive's SHA-256 from the
   manifest before installing.

## Non-goals

- Silent/background installs without the user clicking 업데이트.
- Downgrades or per-user release channels (beta/stable).
- Intel Macs. The app requires macOS 14.2 and the Core Audio process tap; builds
  target `aarch64-apple-darwin` only, and the installer refuses other
  architectures with a clear message. (Builder's call to revisit if a teammate
  needs Intel.)
- Browser-download (DMG) distribution.
- Authenticating downloads. The archive is a public static file like the MCP
  bundle; the app itself requires TTOBAK sign-in.

## Anti-goals (would be a failure even if it "works")

- Any update or popup that stops, corrupts or delays an in-progress recording or
  its upload, or deletes a WAV the existing leftover logic still owns.
- Releases signed with a different certificate (or ad-hoc), which silently
  resets every user's privacy grants.
- Shipping an updater archive built before `sign.sh` re-signing: the
  entitlements would be missing and the microphone would record silence.
- Letting the remote web origin trigger installs. Update commands are not
  granted to the remote `https://ttobak.atomai.click` capability.
- A new application origin, API route or public bucket.

## Decided "how"

### Hosting

- Static files on the existing frontend bucket and CloudFront distribution,
  under the `mac/` prefix:
  - `mac/install.sh` — installer (`text/plain`, `no-cache`).
  - `mac/latest.json` — update manifest (`application/json`, `no-cache`).
  - `mac/TTOBAK_<version>_aarch64.app.tar.gz` and its `.sig` — immutable,
    versioned (`public,max-age=31536000,immutable`).
- `scripts/publish-frontend.sh` syncs without `--delete`, so release files
  uploaded directly to `mac/` survive frontend deploys. The CloudFront viewer
  function passes paths containing `.` through unchanged, so no `knownPages`
  change is needed. The builder verifies both facts still hold and adds a
  publish-frontend test that `mac/` is never deleted or overwritten.
- `install.sh` lives in the repo (e.g. `mac-app/scripts/install.sh`) and is
  uploaded by the release command, not shipped via `frontend/public`, so the
  installer and the release it points at change together.
- Old archives are kept (no garbage collection) so a previous `latest.json`
  can be restored by hand.

### Manifest

Tauri updater static JSON format, plus a SHA-256 used by `install.sh`:

```json
{
  "version": "0.2.0",
  "notes": "Short Korean release notes",
  "pub_date": "2026-11-10T09:00:00Z",
  "platforms": {
    "darwin-aarch64": {
      "url": "https://ttobak.atomai.click/mac/TTOBAK_0.2.0_aarch64.app.tar.gz",
      "signature": "<minisign signature text>",
      "sha256": "<hex>"
    }
  }
}
```

### App side

- Add `tauri-plugin-updater` (Rust side) with the manifest endpoint and the
  minisign public key in `tauri.conf.json`. `bundle.createUpdaterArtifacts`
  may stay off; the release build produces the archive (see Release trigger).
- Update logic lives in Rust (`mac-app/src-tauri/src/update.rs` or similar):
  check, remember the available version, download, verify, install, relaunch.
  Expose to the UI only through commands granted to the local popup window's
  capability, never the remote capability.
- Check on launch and every 6 hours while running; failures are silent apart
  from the manual check's message. (Interval is the builder's call.)
- Popup: a small local window, following `meeting_prompt.rs` /
  `mac-app/src/meeting-prompt.html` (bundled page, own capability). Korean copy.
- "나중에" records the declined version; the popup does not reappear for that
  version, only for a newer one. The menu item remains.
- Recording guard (design call): "busy" means any of native recording, native
  finalizing, an in-flight `upload_recording`, or a busy phase (recording or
  uploading) last reported by the SPA through `control_report_state`
  (`control.rs`). Leftover WAVs awaiting adoption or upload do **not** block
  updates; leftover handling on the next launch is unchanged. Defer the popup
  until idle; refuse install with a message if work started after the popup
  opened. The SPA's phase reporting ships through frontend deploys, so the
  guard must treat "SPA never reported" as not busy only when native state is
  also idle.
- Relaunch after install via the updater's restart. Leftover WAV handling on the
  next launch is unchanged.
- Version: `tauri.conf.json` `version` and `Cargo.toml` `version` stay in sync
  (semver). The app shows its version in the menu/about.

### Installer (`install.sh`)

POSIX `sh` (bash 3.2-compatible), `set -eu`, run as the user (no `sudo`):

1. Refuse non-macOS, non-arm64, or macOS older than 14.2.
2. Fetch `latest.json`, download the archive to a temp dir, verify SHA-256.
3. If TTOBAK is running, query its status over the existing control socket
   (`~/Library/Application Support/ttobak/control.sock`, ADR-046) using the
   same busy definition as the app. Busy → refuse and tell the user to finish
   the recording. Idle → ask it to quit and wait. Socket unreachable → tell the
   user to quit TTOBAK and rerun. Never `kill`. (Design call; the builder
   chooses the client: the app binary's own CLI mode or a small helper, since
   a socket protocol client in `sh` is fragile.)
4. Replace `/Applications/TTOBAK.app` (move the old one aside, then remove it
   after success), strip quarantine defensively, launch it.
5. Print what happened, including the first-run permission prompts to expect.

`/Applications` must be writable by the user; otherwise fail with a clear
message. (Falling back to `~/Applications` is the builder's call.)

### Release trigger

A version is released by pushing a git tag `mac-v<semver>` (for example
`mac-v0.2.0`) on a `main` commit. Publishing that tag's build is what makes the
app announce the update; nothing is announced for untagged builds.

`main` only changes through reviewed PRs, so the version bump is not made by the
release command (design call): the bump to `X.Y.Z` in `tauri.conf.json` and
`Cargo.toml` lands through a normal PR (alone or with the change it ships).
After it merges, the owner's one command (e.g. `npm run release -- "notes"` in
`mac-app/`) reads the version from current `main`, creates the annotated tag
`mac-vX.Y.Z` with the notes, and publishes. Release notes come from the
command argument (stored in the tag annotation).

**Where the build runs (decided 2026-11-10): on the owner's Mac.** The release
command builds, signs and publishes locally and pushes the tag as the record.
Signing certificate and updater key never leave that Mac; no GitHub-hosted
runner, OIDC role or new IAM is added. The command keeps tag and published
build consistent: refuse if the tag already exists locally or on the remote,
build and verify first, push the tag, then upload (manifest last). If upload
fails after the tag push, rerunning the command for the same version resumes
the upload of that tag's build instead of creating a new tag.

Build/publish steps:

1. Require the tag's commit on `main`, the tag version equal to the
   `tauri.conf.json`/`Cargo.toml` version, and the "TTOBAK Dev" identity;
   refuse ad-hoc signing.
2. `tauri build` for `aarch64-apple-darwin`, then `sign.sh`.
3. Verify the signing identity and the audio-input entitlement.
4. Create the `.app.tar.gz` from the re-signed app; sign it with the minisign
   private key (`tauri signer sign`); compute SHA-256.
5. Upload the archive and signature, then `latest.json` and `install.sh` last;
   invalidate `/mac/latest.json` and `/mac/install.sh`.
6. A tag whose version is not newer than the published `latest.json` is
   refused (no downgrade, no overwrite of an existing versioned archive).

Bucket/distribution names come from configuration or CloudFormation outputs,
not hard-coded IDs.

### Keys

- Code-signing certificate: keep the existing self-signed "TTOBAK Dev"
  certificate already in the owner's login keychain (SHA-1
  `C1D1D726FA434621D65DED0261A722AA70924A26`, valid until 2036-06-01). The
  currently installed app is already signed with a certificate, not ad-hoc, so
  the first release is not a grant-resetting release for the owner (verify on
  the first release). Export a password-protected `.p12` backup kept offline.
  The release refuses any other identity.
- Updater key: minisign key pair from `tauri signer generate`. Public key in the
  repo config. Private key and password never in the repo or environment files:
  keychain or Secrets Manager. Losing it means users must reinstall with the
  curl command; the runbook says so.

### Web

A "Mac 앱" section (Settings page) with the install command, a copy button, a
one-line explanation, and the currently published version read from
`/mac/latest.json`. No download button. (Placement is the builder's call.)

### Documentation

- New ADR (next free number; ADR-066 is taken by the account-shared calendar
  stack) recording the self-signed + curl + Tauri updater decision and the
  Developer ID trigger conditions.
- Runbook `docs/runbooks/mac-app-release.md`: one-time key setup, release,
  verify, restore a previous `latest.json`, key loss.
- Update `docs/DESIGN-SPEC.md` (popup/menu/web section) and the mac-app guide.

## Verification

Mac changes have no CI coverage. Rust unit tests cover pure logic (manifest
parsing, version comparison, declined-version rule, recording guard decision).
Manual acceptance on a real Mac, then on one teammate's Mac:

1. Fresh install with the curl command; grant permissions; record a short meeting.
2. Publish a newer version; popup appears; 나중에 → menu shows it, no popup again.
3. Start recording; trigger update from the menu → refused; stop → install works.
4. After update: permissions still granted, recording works, version shown.
5. Tampered archive or signature → update refused.
6. On a teammate's Mac (managed status unknown as of 2026-11-10): install,
   grant, update. If binary authorization or MDM blocks the self-signed app,
   stop and revisit the Developer ID migration before wider rollout.

## Left to the builder

Exact file/module names, check interval, popup layout, Settings placement,
`~/Applications` fallback, release command shape and how it finds the bucket,
and how the release command handles a dirty tree.
