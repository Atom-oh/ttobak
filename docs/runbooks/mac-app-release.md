# Mac app release

Release procedure for the TTOBAK Mac app ([ADR-019](../decisions/ADR-019-mac-app-distribution-and-updates.md)).
It runs on the owner's Mac; no CI, runner or new IAM is involved. Passing it does not
prove a teammate's Mac accepts the app: record manual acceptance separately.

## Constraints

- Target `aarch64-apple-darwin`, macOS 14.2 or later.
- Sign only with the "TTOBAK Dev" certificate (leaf SHA-1
  `C1D1D726FA434621D65DED0261A722AA70924A26`, valid until 2036-06-01).
- Tauri CLI 2.12.4 or later (`npm install` in `mac-app/`): `signer sign --app-version`
  binds the version, and the app sets `requireSignedVersion: true`.
- Tag `mac-v<semver>`; the tag version equals `tauri.conf.json` and `Cargo.toml`.
- AWS credentials able to read the CloudFormation exports `TtobakSiteBucketName` and
  `TtobakDistributionId`, write `mac/*` and invalidate CloudFront.

## One-time updater key setup

While `plugins.updater.pubkey` in `mac-app/src-tauri/tauri.conf.json` is the
placeholder, the app disables update checks and `npm run release` refuses.

1. `cd mac-app && npm install && npx tauri signer generate -w ~/.tauri/ttobak-updater.key`.
2. Store the password in the login keychain (service `ttobak-updater-key`, account
   `$USER`) or set `TAURI_SIGNING_PRIVATE_KEY_PASSWORD` per run. Never commit the key
   or password, or put them in env files.
3. Put the public key in `tauri.conf.json` through a normal PR.
4. Keep offline backups of the private key and a password-protected `.p12` export of
   the "TTOBAK Dev" certificate.
5. Export `TAURI_SIGNING_PRIVATE_KEY_PATH=~/.tauri/ttobak-updater.key` when releasing.

## Release

1. Bump `version` in `tauri.conf.json` and `Cargo.toml` (same value) in a PR and merge it.
2. In `mac-app/`, on a clean checkout whose HEAD equals `origin/main`:
   `npm run release -- --dry-run "notes"`, then `npm run release -- "notes"`.
3. The command checks guards (version, pubkey, endpoint, identity, newer than the
   published manifest, tag state), builds, re-signs with `sign.sh`, verifies the
   certificate leaf, the audio-input entitlement and the bundle version, archives,
   signs the archive, creates and pushes the annotated tag (the notes), uploads the
   archive and `.sig` (immutable, never overwritten), then `install.sh` and
   `latest.json` last, then invalidates those two paths.

## Verify

- `curl -sI https://ttobak.atomai.click/mac/latest.json`: `cache-control` is
  `no-cache,max-age=0,must-revalidate`; the archive and `.sig` are
  `public,max-age=31536000,immutable`.
- `curl -s https://ttobak.atomai.click/mac/latest.json`: `version`, and
  `platforms.darwin-aarch64` with `url`, `signature`, `sha256`.
- Manual acceptance on a real Mac, then a teammate's Mac:
  1. Fresh install with the curl command; grant permissions; record a short meeting.
  2. Publish a newer version: popup appears; Later keeps the app menu item
     showing the available version and shows no second popup for that version.
  3. During a recording the menu update is refused; after it ends, install works.
  4. After the update: permissions still granted, recording works, version shown.
  5. A tampered archive or signature is refused.
  6. The installer quits a running app via osascript, which may trigger a macOS
     Automation prompt (Terminal -> TTOBAK); denying it makes the installer wait 30 s
     and exit 5.

Mac changes have no CI. The menu item, popup, notifications, install and restart are
manual-only checks. If MDM or binary authorization blocks the app, stop and revisit
Developer ID (ADR-019).

## Failed upload (resume)

Rerun the same command with the same notes. If `mac-v<version>` exists on HEAD
(locally or on origin), the run resumes: it never retags, rebuilds only if the
archive is not on S3, and otherwise requires the local build in
`mac-app/src-tauri/target/release-artifacts/<version>/` to match the remote archive
and `.sig`. A tag on another commit is refused. Once `origin/main` moves past the
tag, resume is not possible: bump to a new version instead.

## Restore a previous manifest

Old archives are kept. To roll back the announcement, upload the earlier
`latest.json` to `s3://<bucket>/mac/latest.json` with
`--cache-control no-cache,max-age=0,must-revalidate` and invalidate `/mac/latest.json`.
Installed newer apps do not downgrade (`allowDowngrades` is off). Fix forward with a
higher version.

## Key loss and expiry

- Updater key lost or leaked: generate a new key, replace the public key through a PR,
  release; installed apps cannot verify the new key, so users must reinstall with the
  curl command.
- "TTOBAK Dev" expires 2036-06-01 or is replaced: the designated requirement changes
  and every user re-grants permissions once; plan the Developer ID move (ADR-019).

## Installer limits

`install.sh` needs a running app of this release or later to report `updateBusy`;
otherwise it asks the user to quit TTOBAK and rerun. It never kills the app and
refuses while a recording or upload is in progress.
