#!/usr/bin/env bash
# Release the TTOBAK Mac app from the owner's Mac (no CI runner, no new IAM).
#
# Usage (in mac-app/, after the version-bump PR merged to main):
#   npm run release -- "release notes"
#   npm run release -- --dry-run "release notes"
#
# Steps: guards -> published-version check -> tag state -> build + re-sign +
# verify -> archive + minisign signature + manifest -> create/push tag
# mac-v<version> -> upload archive and .sig (immutable, never overwritten) ->
# upload install.sh and latest.json last (no-cache) -> invalidate those two.
#
# Resume: when mac-v<version> already exists on HEAD (locally or on origin),
# the run resumes that release instead of tagging again. If the versioned
# archive is not on S3 yet, it rebuilds and uploads. If it is, nothing is
# rebuilt: the local build in src-tauri/target/release-artifacts/<version>/
# must match the remote archive (SHA-256) and the remote .sig (byte-equal, or
# absent and then uploaded), else the run refuses. A tag on another commit is
# always refused.
#
# --dry-run runs every check and the local build/sign/verify steps, and prints
# the tag, push, upload and invalidation commands instead of running them.
#
# Secrets (never printed, never written to disk by this script):
#   TAURI_SIGNING_PRIVATE_KEY_PATH      updater private key file (required to build).
#   TAURI_SIGNING_PRIVATE_KEY_PASSWORD  its password, if set (may be empty). Otherwise
#                                       the login keychain item with service
#                                       "ttobak-updater-key" and account $USER, else a
#                                       silent prompt on a TTY. The password is passed
#                                       only in the environment of `tauri signer sign`.
# Signing identity: only the "TTOBAK Dev" certificate with the pinned leaf SHA-1.
# Bucket/distribution: CloudFormation exports TtobakSiteBucketName/TtobakDistributionId.
set -euo pipefail
set +x

LEAF_SHA1="C1D1D726FA434621D65DED0261A722AA70924A26"
BASE_URL="https://ttobak.atomai.click/mac"
KEYCHAIN_SERVICE="ttobak-updater-key"
PLACEHOLDER_PUBKEY="REPLACE_WITH_TTOBAK_UPDATER_PUBKEY"
IMMUTABLE="public,max-age=31536000,immutable"
NO_CACHE="no-cache,max-age=0,must-revalidate"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
APP_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
ROOT="$(cd "$APP_DIR/.." && pwd)"
TAURI_DIR="$APP_DIR/src-tauri"
MANIFEST_PY="$SCRIPT_DIR/release_manifest.py"

die() { printf 'release: %s\n' "$*" >&2; exit 1; }
usage() {
  printf 'usage: npm run release -- [--dry-run] "release notes"\n' >&2
  exit 2
}

DRY_RUN=0
NOTES=""
HAVE_NOTES=0
for arg in "$@"; do
  case "$arg" in
    --dry-run) DRY_RUN=1 ;;
    -h|--help) usage ;;
    -*) usage ;;
    *)
      [[ $HAVE_NOTES -eq 0 ]] || usage
      NOTES="$arg"
      HAVE_NOTES=1
      ;;
  esac
done
[[ -n "$(printf '%s' "$NOTES" | tr -d '[:space:]')" ]] || usage

# Mutating commands go through plan: printed in dry-run, executed otherwise.
plan() {
  if [[ $DRY_RUN -eq 1 ]]; then
    printf '[dry-run] would run:'
    printf ' %q' "$@"
    printf '\n'
  else
    "$@"
  fi
}

WORK="$(mktemp -d "${TMPDIR:-/tmp}/ttobak-release.XXXXXX")"
cleanup() { rm -rf "${WORK:?}"; }
trap cleanup EXIT

# 1. Guards.
[[ "$(uname -s)" == "Darwin" && "$(uname -m)" == "arm64" ]] ||
  die "release must run on an Apple Silicon Mac"
[[ -z "$(git -C "$ROOT" status --porcelain --untracked-files=normal)" ]] ||
  die "working tree must be clean"
git -C "$ROOT" fetch --quiet origin main || die "could not fetch origin main"
HEAD_SHA="$(git -C "$ROOT" rev-parse HEAD)"
MAIN_SHA="$(git -C "$ROOT" rev-parse origin/main)"
[[ "$HEAD_SHA" == "$MAIN_SHA" ]] || die "HEAD must equal origin/main (check out and pull main)"

VERSION="$(python3 "$MANIFEST_PY" read-version --tauri-conf "$TAURI_DIR/tauri.conf.json" \
  --cargo-toml "$TAURI_DIR/Cargo.toml")" ||
  die "tauri.conf.json and Cargo.toml must carry the same MAJOR.MINOR.PATCH version"

UPDATER_CONF="$(python3 -c 'import json,sys
u=json.load(open(sys.argv[1]))["plugins"]["updater"]
print(u.get("pubkey","")); print(" ".join(u.get("endpoints",[])))' "$TAURI_DIR/tauri.conf.json")" ||
  die "tauri.conf.json has no plugins.updater section"
PUBKEY="$(printf '%s\n' "$UPDATER_CONF" | sed -n 1p)"
ENDPOINTS="$(printf '%s\n' "$UPDATER_CONF" | sed -n 2p)"
[[ -n "$PUBKEY" && "$PUBKEY" != "$PLACEHOLDER_PUBKEY" ]] ||
  die "plugins.updater.pubkey is not set; merge the real updater pubkey first"
[[ "$ENDPOINTS" == "$BASE_URL/latest.json" ]] ||
  die "plugins.updater.endpoints must be exactly $BASE_URL/latest.json"

# Captured before grep: with pipefail, grep -q closing early could fail the pipe.
IDENTITIES="$(security find-identity -v -p codesigning 2>/dev/null || true)"
printf '%s\n' "$IDENTITIES" | grep -i -- "$LEAF_SHA1" | grep '"TTOBAK Dev"' >/dev/null ||
  die "the \"TTOBAK Dev\" certificate ($LEAF_SHA1) is not a valid signing identity in the keychain"

# 2. Hosting target and the published version.
# The CLI applies --query to each result page, so a filter like `| [0]` prints
# one line per page (`None` on pages without the export). Keep the non-empty
# values and require exactly one.
export_value() {
  local out vals
  out="$(aws cloudformation list-exports --query "Exports[?Name=='$1'].Value" --output text)" ||
    return 1
  vals="$(printf '%s\n' "$out" | tr '\t' '\n' | grep -v -e '^None$' -e '^[[:space:]]*$' | sort -u || true)"
  [[ -n "$vals" && "$vals" != *$'\n'* ]] || return 1
  printf '%s\n' "$vals"
}
BUCKET="$(export_value TtobakSiteBucketName)" || die "could not read CloudFormation exports"
DIST="$(export_value TtobakDistributionId)" || die "could not read CloudFormation exports"
[[ "$BUCKET" =~ ^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$ ]] || die "invalid site bucket export"
[[ "$DIST" =~ ^[A-Z0-9]+$ ]] || die "invalid distribution ID export"

# Exit 0 when the key exists, 1 when S3 answers 404; anything else aborts.
s3_exists() {
  local err
  if err="$(aws s3api head-object --bucket "$BUCKET" --key "$1" 2>&1 >/dev/null)"; then
    return 0
  fi
  case "$err" in
    *"(404)"*|*"Not Found"*) return 1 ;;
  esac
  die "could not check s3://$BUCKET/$1"
}

NAME="TTOBAK_${VERSION}_aarch64.app.tar.gz"
ARCHIVE_KEY="mac/$NAME"
SIG_KEY="mac/$NAME.sig"
TAG="mac-v$VERSION"

if s3_exists mac/latest.json; then
  aws s3 cp "s3://$BUCKET/mac/latest.json" "$WORK/published.json" --only-show-errors ||
    die "could not download the published latest.json"
fi

# 3. Tag state: absent, on HEAD (resume) or elsewhere (refuse).
LOCAL_TAG="$(git -C "$ROOT" rev-parse -q --verify "refs/tags/$TAG^{commit}" 2>/dev/null || true)"
REMOTE_REFS="$(git -C "$ROOT" ls-remote --tags origin "refs/tags/$TAG" "refs/tags/$TAG^{}")" ||
  die "could not list origin tags"
REMOTE_TAG="$(printf '%s\n' "$REMOTE_REFS" | awk -v r="refs/tags/$TAG^{}" '$2 == r { print $1 }')"
if [[ -z "$REMOTE_TAG" ]]; then
  REMOTE_TAG="$(printf '%s\n' "$REMOTE_REFS" | awk -v r="refs/tags/$TAG" '$2 == r { print $1 }')"
fi
RESUME=0
for sha in "$LOCAL_TAG" "$REMOTE_TAG"; do
  [[ -z "$sha" ]] && continue
  [[ "$sha" == "$HEAD_SHA" ]] || die "tag $TAG already exists on another commit ($sha)"
  RESUME=1
done

# No downgrade or re-release; a resumed run may find its own manifest live.
NEWER_ARGS=(check-newer --published "$WORK/published.json" --version "$VERSION")
[[ $RESUME -eq 1 ]] && NEWER_ARGS+=(--allow-equal)
rc=0
python3 "$MANIFEST_PY" "${NEWER_ARGS[@]}" || rc=$?
case $rc in
  0) ;;
  1) die "version $VERSION is not newer than the published release" ;;
  *) die "the published latest.json is malformed; fix it before releasing" ;;
esac

if [[ $RESUME -eq 1 ]]; then
  if [[ -z "$LOCAL_TAG" ]]; then
    git -C "$ROOT" fetch --quiet origin "refs/tags/$TAG:refs/tags/$TAG" ||
      die "could not fetch tag $TAG"
  fi
  # The tag is stored with --cleanup=verbatim, so the annotation is the notes
  # byte for byte; $(...) drops trailing newlines on both sides.
  TAG_NOTES="$(git -C "$ROOT" for-each-ref --format='%(contents)' "refs/tags/$TAG")"
  [[ "$TAG_NOTES" == "$(printf '%s' "$NOTES")" ]] ||
    die "resuming $TAG: pass the same notes as the tag annotation"
  echo "note: $TAG exists on HEAD; resuming that release"
fi

# 4. What still has to be built or uploaded.
ART="$TAURI_DIR/target/release-artifacts/$VERSION"
ARCHIVE="$ART/$NAME"
SIG="$ARCHIVE.sig"
BUILD=0
UPLOAD_ARCHIVE=0
UPLOAD_SIG=0
REMOTE_SIG=0
s3_exists "$SIG_KEY" && REMOTE_SIG=1
if s3_exists "$ARCHIVE_KEY"; then
  [[ $RESUME -eq 1 ]] || die "$ARCHIVE_KEY is already published without tag $TAG; refusing to overwrite"
  [[ -f "$ARCHIVE" && -f "$SIG" ]] ||
    die "$ARCHIVE_KEY is published but the local build in $ART is missing; cannot prove it matches"
  aws s3 cp "s3://$BUCKET/$ARCHIVE_KEY" "$WORK/remote.tar.gz" --only-show-errors ||
    die "could not download $ARCHIVE_KEY"
  [[ "$(shasum -a 256 "$WORK/remote.tar.gz" | awk '{print $1}')" == \
     "$(shasum -a 256 "$ARCHIVE" | awk '{print $1}')" ]] ||
    die "published archive differs from the local build; refusing"
  if [[ $REMOTE_SIG -eq 1 ]]; then
    aws s3 cp "s3://$BUCKET/$SIG_KEY" "$WORK/remote.sig" --only-show-errors ||
      die "could not download $SIG_KEY"
    cmp -s "$WORK/remote.sig" "$SIG" ||
      die "published signature differs from the local signature; refusing"
  else
    UPLOAD_SIG=1
  fi
else
  [[ $REMOTE_SIG -eq 0 ]] ||
    die "$SIG_KEY is published without its archive; remove it by hand after checking"
  BUILD=1
  UPLOAD_ARCHIVE=1
  UPLOAD_SIG=1
fi

# 5. Build, re-sign, verify, archive and sign (local only).
KEY_PW=""
read_key_password() {
  if [[ "${TAURI_SIGNING_PRIVATE_KEY_PASSWORD+set}" == "set" ]]; then
    KEY_PW="$TAURI_SIGNING_PRIVATE_KEY_PASSWORD"
  elif KEY_PW="$(security find-generic-password -a "$USER" -s "$KEYCHAIN_SERVICE" -w 2>/dev/null)"; then
    :
  elif [[ -t 0 ]]; then
    printf 'Updater key password: ' >&2
    IFS= read -rs KEY_PW
    printf '\n' >&2
  else
    die "no updater key password: set TAURI_SIGNING_PRIVATE_KEY_PASSWORD, add keychain item \"$KEYCHAIN_SERVICE\", or run on a TTY"
  fi
}

if [[ $BUILD -eq 1 ]]; then
  KEY="${TAURI_SIGNING_PRIVATE_KEY_PATH:-}"
  [[ -n "$KEY" && -f "$KEY" && -r "$KEY" ]] ||
    die "set TAURI_SIGNING_PRIVATE_KEY_PATH to the readable updater private key file"
  SIGNER_HELP="$(cd "$APP_DIR" && npx --no-install tauri signer sign --help 2>/dev/null || true)"
  [[ "$SIGNER_HELP" == *"--app-version"* ]] ||
    die "Tauri CLI with 'signer sign --app-version' (>= 2.12.4) is required; run npm install"
  read_key_password

  (cd "$APP_DIR" && env -u TAURI_SIGNING_PRIVATE_KEY -u TAURI_SIGNING_PRIVATE_KEY_PASSWORD \
    npx --no-install tauri build --target aarch64-apple-darwin) || die "tauri build failed"
  BUNDLE="$TAURI_DIR/target/aarch64-apple-darwin/release/bundle/macos/TTOBAK.app"
  [[ -d "$BUNDLE" ]] || die "build produced no $BUNDLE"
  TTOBAK_SIGN_IDENTITY="$LEAF_SHA1" bash "$SCRIPT_DIR/sign.sh" || die "sign.sh failed"

  leaf_lower="$(printf '%s' "$LEAF_SHA1" | tr 'A-F' 'a-f')"
  requirement="$(codesign -d -r- "$BUNDLE" 2>&1 || true)"
  [[ "$requirement" == *"certificate leaf = H\"$leaf_lower\""* ]] ||
    die "bundle is not signed with the TTOBAK Dev certificate"
  entitlements="$(codesign -d --entitlements - "$BUNDLE" 2>&1 || true)"
  [[ "$entitlements" == *"com.apple.security.device.audio-input"* ]] ||
    die "bundle lacks the audio-input entitlement"
  codesign --verify --strict --deep "$BUNDLE" || die "codesign verification failed"
  bundle_version="$(/usr/libexec/PlistBuddy -c 'Print :CFBundleShortVersionString' \
    "$BUNDLE/Contents/Info.plist")"
  [[ "$bundle_version" == "$VERSION" ]] || die "bundle version $bundle_version != $VERSION"

  rm -rf "${ART:?}"
  mkdir -p "$ART"
  COPYFILE_DISABLE=1 tar -czf "$ARCHIVE" -C "$(dirname "$BUNDLE")" "$(basename "$BUNDLE")"
  TAURI_SIGNING_PRIVATE_KEY_PASSWORD="$KEY_PW" env -u TAURI_SIGNING_PRIVATE_KEY \
    npx --no-install tauri signer sign --app-version "$VERSION" -f "$KEY" "$ARCHIVE" \
    >/dev/null || { KEY_PW=""; die "tauri signer sign failed"; }
  KEY_PW=""
  [[ -s "$SIG" ]] || die "signer produced no $SIG"
fi

python3 "$MANIFEST_PY" build --version "$VERSION" --notes "$NOTES" --url "$BASE_URL/$NAME" \
  --signature-file "$SIG" --archive "$ARCHIVE" --pubkey "$PUBKEY" >"$WORK/latest.json" ||
  die "could not build latest.json"
sh -n "$SCRIPT_DIR/install.sh" || die "install.sh has a syntax error"

# 6. Tag first, then immutable files, then the mutable entry points.
if [[ -z "$LOCAL_TAG" && -z "$REMOTE_TAG" ]]; then
  plan git -C "$ROOT" tag -a --cleanup=verbatim "$TAG" -m "$NOTES" "$HEAD_SHA"
fi
if [[ -z "$REMOTE_TAG" ]]; then
  plan git -C "$ROOT" push origin "refs/tags/$TAG" || die "could not push $TAG"
fi

upload_immutable() {
  local file=$1 key=$2 type=$3
  ! s3_exists "$key" || die "s3://$BUCKET/$key already exists; refusing to overwrite"
  plan aws s3 cp "$file" "s3://$BUCKET/$key" --cache-control "$IMMUTABLE" \
    --content-type "$type" --only-show-errors || die "upload of $key failed; rerun to resume"
}
[[ $UPLOAD_ARCHIVE -eq 1 ]] && upload_immutable "$ARCHIVE" "$ARCHIVE_KEY" "application/gzip"
[[ $UPLOAD_SIG -eq 1 ]] && upload_immutable "$SIG" "$SIG_KEY" "text/plain; charset=utf-8"

plan aws s3 cp "$SCRIPT_DIR/install.sh" "s3://$BUCKET/mac/install.sh" --cache-control "$NO_CACHE" \
  --content-type "text/plain; charset=utf-8" --only-show-errors ||
  die "upload of mac/install.sh failed; rerun to resume"
plan aws s3 cp "$WORK/latest.json" "s3://$BUCKET/mac/latest.json" --cache-control "$NO_CACHE" \
  --content-type "application/json" --only-show-errors ||
  die "upload of mac/latest.json failed; rerun to resume"
plan aws cloudfront create-invalidation --distribution-id "$DIST" \
  --paths /mac/latest.json /mac/install.sh --query Invalidation.Id --output text ||
  die "invalidation failed; rerun to resume"

if [[ $DRY_RUN -eq 1 ]]; then
  echo "dry-run complete for $TAG; manifest that would be published as mac/latest.json:"
  cat "$WORK/latest.json"
else
  echo "released $TAG: $BASE_URL/latest.json"
fi
