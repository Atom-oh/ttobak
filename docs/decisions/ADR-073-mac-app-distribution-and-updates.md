# ADR-073: Mac app distribution and in-app updates

- Status: Accepted (2026-11-10); implementation pending owner acceptance (key
  setup, first release, teammate Mac).
- Decision date: 2026-11-10.
- Builds on: ADR-024 and ADR-046 (Mac app, signing and local control path).

## Context

The Mac app was built and installed by hand on one Mac. The owner wants teammates
to install it with one command and to receive native updates with one click. The
window loads the production web app, so UI changes already ship through frontend
deploys; releases are needed only for native code and are occasional. Audience is
the owner and teammates, not the public.

## Decision

- **Signing:** every release uses the existing self-signed "TTOBAK Dev"
  certificate (leaf SHA-1 `C1D1D726FA434621D65DED0261A722AA70924A26`, valid until
  2036-06-01). No Apple Developer ID and no notarization. `release.sh` refuses
  ad-hoc signing and any other identity.
- **Why this works:** files fetched by `curl` or by the in-app updater carry no
  quarantine attribute, so Gatekeeper does not assess them. Privacy grants
  (Microphone, System Audio Recording, Accessibility) bind to the designated
  requirement (bundle ID plus certificate leaf), which stays stable while the
  certificate does.
- **Hosting:** static files under `mac/` on the existing frontend bucket and
  CloudFront distribution: `install.sh` and `latest.json` (no-cache), and
  versioned `TTOBAK_<version>_aarch64.app.tar.gz` plus `.sig` (immutable). No new
  origin, route, bucket or IAM role. `scripts/publish-frontend.sh` excludes `mac/*`
  so frontend deploys never overwrite release files.
- **Installer:** `curl -fsSL https://ttobak.atomai.click/mac/install.sh | sh`
  (`mac-app/scripts/install.sh`). It verifies the manifest SHA-256, asks a running
  app for `updateBusy` over the control socket, and never kills the app.
- **Updater:** `tauri-plugin-updater` against `mac/latest.json` with a minisign
  public key in `tauri.conf.json` and `requireSignedVersion: true`, so a signature
  is bound to its version and an old signed archive cannot be replayed as a newer
  release. The update logic is Rust (`update.rs`, `update_policy.rs`); a local popup
  (`update_prompt.rs`, `update-prompt.html`) offers Update and Later (Korean labels). Update
  commands are granted only to the local popup capability, never to the remote
  origin (a test enforces it).
- **Busy guard:** no popup and no install while native recording, finalizing, an
  in-flight upload or a busy SPA phase is reported. Install is refused if work
  starts after the popup opened, and the restart is deferred if work starts after
  install.
- **Release:** `npm run release -- "notes"` runs on the owner's Mac (build, re-sign,
  verify, archive, sign, tag `mac-v<version>`, upload, invalidate). The tag is the
  record; resume after a failed upload reuses the tagged build. See the
  [runbook](../runbooks/mac-app-release.md).
- **Web:** Settings shows the install command and published version
  (`MacAppInstall.tsx`).

## Consequences

- No CI covers the Mac module; menu, popup, notification, install and restart need
  manual verification on a real Mac and then on a teammate's Mac.
- Updater private key loss means users must reinstall with the curl command.
- Resume of a failed release is impossible once `origin/main` moves past the tag;
  bump to a new version instead.
- The installer needs a running app that already reports `updateBusy` (this
  release or later); an older running app makes it ask the user to quit TTOBAK.
- If binary authorization or MDM blocks the self-signed app on a teammate's Mac,
  stop and revisit Developer ID before wider rollout.

## Developer ID triggers

Move to Developer ID signing and notarization when any of these holds: distribution
outside the team, browser (DMG) downloads, a managed-Mac policy that requires
notarization, or the certificate nears expiry. The designated requirement changes
once, so every user re-grants permissions once.

## Accepted risks

- The archive is a public static file, like the MCP bundle; the app itself
  requires TTOBAK sign-in.
- The certificate is self-signed: a user is trusting the TTOBAK domain and the
  pinned minisign key, not an Apple-issued identity.
- Intel Macs, downgrades, release channels and silent installs are out of scope.
