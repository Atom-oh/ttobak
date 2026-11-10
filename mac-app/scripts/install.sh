#!/bin/sh
# TTOBAK Mac installer: curl -fsSL https://ttobak.atomai.click/mac/install.sh | sh
# Never reads stdin, never uses sudo, never kills the app.
set -eu

BASE="${TTOBAK_INSTALL_BASE:-https://ttobak.atomai.click/mac}"
DEST="${TTOBAK_INSTALL_DIR:-/Applications}"
BUNDLE_ID="click.atomai.ttobak.mac"
SOCK="${HOME}/Library/Application Support/ttobak/control.sock"

die() {
  code=$1
  shift
  printf '%s\n' "$*" >&2
  exit "$code"
}

main() {
  # 1. Platform checks.
  [ "$(uname -s)" = "Darwin" ] || die 2 "TTOBAK supports macOS only."
  [ "$(uname -m)" = "arm64" ] || die 2 "TTOBAK requires an Apple Silicon (arm64) Mac."
  os_ver=$(sw_vers -productVersion)
  os_major=${os_ver%%.*}
  os_rest=${os_ver#*.}
  [ "$os_rest" = "$os_ver" ] && os_rest=0
  os_minor=${os_rest%%.*}
  case "$os_major$os_minor" in
    ''|*[!0-9]*) die 2 "Cannot parse macOS version: $os_ver" ;;
  esac
  if [ "$os_major" -lt 14 ] || { [ "$os_major" -eq 14 ] && [ "$os_minor" -lt 2 ]; }; then
    die 2 "TTOBAK requires macOS 14.2 or later (found $os_ver)."
  fi

  # 5 (checked early). Install dir must be writable without sudo.
  if [ ! -d "$DEST" ] || [ ! -w "$DEST" ]; then
    die 6 "$DEST is not writable by this user. Fix its permissions (or install the app manually) and rerun."
  fi

  WORK=$(mktemp -d "${TMPDIR:-/tmp}/ttobak-install.XXXXXX")
  cleanup() { rm -rf "$WORK"; }
  trap cleanup EXIT

  # 2. Fetch manifest, download, verify.
  curl -fsSL "$BASE/latest.json" -o "$WORK/latest.json" || die 1 "Could not fetch $BASE/latest.json."
  url=$(plutil -extract platforms.darwin-aarch64.url raw -o - "$WORK/latest.json" 2>/dev/null) ||
    die 1 "Release manifest has no darwin-aarch64 entry."
  want=$(plutil -extract platforms.darwin-aarch64.sha256 raw -o - "$WORK/latest.json" 2>/dev/null) ||
    die 1 "Release manifest has no sha256."
  [ -n "$url" ] && [ -n "$want" ] || die 1 "Release manifest is incomplete."

  printf 'Downloading TTOBAK...\n'
  curl -fsSL "$url" -o "$WORK/app.tar.gz" || die 1 "Download failed: $url"
  got=$(shasum -a 256 "$WORK/app.tar.gz" | awk '{print $1}')
  if [ "$got" != "$want" ]; then
    die 3 "Checksum mismatch; nothing was installed."
  fi

  # 3. Running app: only quit it when it reports not busy.
  if pgrep -x ttobak-mac >/dev/null 2>&1; then
    resp=""
    if [ -S "$SOCK" ]; then
      resp=$(printf '%s\n' '{"v":1,"id":"install","action":"status"}' | nc -w 3 -U "$SOCK" 2>/dev/null || true)
    fi
    case "$resp" in
      *'"updateBusy":true'*|*'"updateBusy": true'*)
        die 4 "녹음이 끝난 뒤 다시 실행해 주세요."
        ;;
      *'"updateBusy":false'*|*'"updateBusy": false'*)
        osascript -e "tell application id \"$BUNDLE_ID\" to quit" >/dev/null 2>&1 || true
        i=0
        while pgrep -x ttobak-mac >/dev/null 2>&1; do
          i=$((i + 1))
          [ "$i" -le 30 ] || die 5 "TTOBAK did not quit. Quit it manually and rerun this command."
          sleep 1
        done
        ;;
      *)
        die 5 "TTOBAK is running but cannot report its state. Quit TTOBAK and rerun this command."
        ;;
    esac
  fi

  # 4. Extract and swap.
  mkdir "$WORK/x"
  tar -xzf "$WORK/app.tar.gz" -C "$WORK/x" || die 1 "Could not extract the archive."
  [ -d "$WORK/x/TTOBAK.app" ] || die 1 "Archive does not contain TTOBAK.app."

  APP="$DEST/TTOBAK.app"
  PREV="$DEST/TTOBAK.app.previous"
  # An interrupted earlier install can leave only the previous copy behind;
  # put it back rather than deleting the last working app.
  if [ ! -e "$APP" ] && [ -e "$PREV" ]; then
    mv "$PREV" "$APP" || die 1 "Could not restore the previous TTOBAK.app."
  fi
  rm -rf "$PREV" || die 1 "Could not clear $PREV."
  had_prev=0
  if [ -e "$APP" ]; then
    mv "$APP" "$PREV" || die 1 "Could not move the existing TTOBAK.app aside."
    had_prev=1
  fi
  if ! mv "$WORK/x/TTOBAK.app" "$APP"; then
    rm -rf "$APP"
    if [ "$had_prev" -eq 1 ]; then
      mv "$PREV" "$APP" || true
    fi
    die 1 "Install failed; the previous TTOBAK.app was restored."
  fi
  rm -rf "$PREV" || true
  xattr -cr "$APP" || true

  open "$APP" || true
  printf 'TTOBAK installed to %s.\n' "$APP"
  printf 'On first launch, allow Microphone and Screen/System Audio Recording permissions when macOS asks.\n'
}

main "$@"
