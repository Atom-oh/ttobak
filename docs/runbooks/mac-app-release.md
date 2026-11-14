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
4. Back up the private key, its password and a password-protected `.p12` export of
   the "TTOBAK Dev" identity ([Backup and restore](#backup-and-restore)).
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

## Backup and restore

Backups live in AWS Secrets Manager in the TTOBAK account, region `ap-northeast-2`,
tagged `project=ttobak` and `purpose=mac-release-backup` (stored 2026-11-14):

| Secret | Content |
|---|---|
| `ttobak/mac/updater-key` | Minisign private key (`~/.tauri/ttobak-updater.key`, no trailing newline) |
| `ttobak/mac/updater-key-pub` | Matching public key (`~/.tauri/ttobak-updater.key.pub`) |
| `ttobak/mac/updater-key-password` | Private key password (keychain service `ttobak-updater-key`) |
| `ttobak/mac/dev-cert-p12` | Binary `.p12` of the "TTOBAK Dev" certificate and private key |
| `ttobak/mac/dev-cert-p12-password` | The `.p12` export password |

Anyone who can read these secrets in that account can sign releases. Keep read
access to account administrators. Writing them is an owner action, run in the
owner's own terminal.

- **Back up:** create each secret with `aws secretsmanager create-secret --name …`.
  Pass `--secret-string file://…` for the key files, and `--secret-binary fileb://…`
  for the `.p12`. Export only the "TTOBAK Dev" identity, selected by its SHA-1, never
  the whole keychain (`security export -t identities` exports every identity).
- **Verify a backup without printing it:** compare SHA-256 digests. AWS CLI text
  output adds a trailing newline, so strip it first:
  `printf %s "$(aws secretsmanager get-secret-value --secret-id ttobak/mac/updater-key --query SecretString --output text)" | shasum -a 256`
  must equal `shasum -a 256 < ~/.tauri/ttobak-updater.key`.
- **Restore on a new Mac:**
  1. Write the key files with `printf %s "$(aws … --query SecretString --output text)" > ~/.tauri/ttobak-updater.key`.
     Use `chmod 700 ~/.tauri` and `chmod 600` on the private key.
  2. Restore the keychain password with `security add-generic-password -s ttobak-updater-key -a "$USER" -w "<value>"`.
  3. Write the `.p12` with `aws … --query SecretBinary --output text | base64 -d`, then
     import it by double-clicking it with the stored password.
  4. Trust the certificate for code signing:
     `security add-trusted-cert -r trustRoot -p codeSign -k ~/Library/Keychains/login.keychain-db <cert.pem>`.
  5. Confirm `security find-identity -v -p codesigning` lists `C1D1D726…4A26`, then
     delete the temporary `.p12`.
- The `.p12` uses legacy RC2 encryption: Homebrew OpenSSL 3 needs `-legacy`, while
  `/usr/bin/openssl` (LibreSSL) reads it directly.

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
