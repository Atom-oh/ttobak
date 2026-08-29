#!/usr/bin/env bash
# Re-sign the Tauri-built .app bundle with explicit entitlements.
#
# Why this exists:
#   Tauri 2's default ad-hoc signing (when bundle.macOS.signingIdentity is
#   null) only runs through the linker and does NOT invoke
#   `codesign --entitlements`. As a result, Entitlements.plist is shipped
#   inside the bundle but is never actually applied to the binary, so macOS
#   silently refuses to expose getUserMedia / mediaDevices, never prompts
#   for the mic permission, and ScreenCaptureKit fails to start.
#
# What this does:
#   For every .app under src-tauri/target/.../bundle/macos/, force a
#   deep ad-hoc re-sign with --entitlements pointing at our plist, then
#   strip the quarantine attribute so the first launch is friction-free.
#
# Usage:
#   bash mac-app/scripts/sign.sh
#   # or via npm: npm run build:signed (in mac-app/)

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SRC_TAURI="$SCRIPT_DIR/../src-tauri"
ENTITLEMENTS="$SRC_TAURI/Entitlements.plist"

if [[ ! -f "$ENTITLEMENTS" ]]; then
  echo "error: $ENTITLEMENTS not found" >&2
  exit 1
fi

# Find every .app bundle under target/.../bundle/macos/.
# Use shell globbing instead of `mapfile` because macOS ships bash 3.2
# (no `mapfile`, no readarray). Covers both the default `target/release`
# layout and per-target `target/<triple>/release` (cross-arch builds).
shopt -s nullglob
APPS=(
  "$SRC_TAURI"/target/release/bundle/macos/*.app
  "$SRC_TAURI"/target/*/release/bundle/macos/*.app
)
shopt -u nullglob

if [[ ${#APPS[@]} -eq 0 ]]; then
  echo "error: no .app bundles found under $SRC_TAURI/target/*/bundle/macos/" >&2
  echo "       run 'npm run build' first" >&2
  exit 1
fi

for APP in "${APPS[@]}"; do
  echo "==> re-signing $APP"
  # NOTE: --deep is deprecated by Apple (it signs nested code with the
  # OUTER bundle's identity/entitlements in one pass, rather than the
  # correct inside-out order: sign nested binaries first, then the bundle).
  # This works today because signing is ad-hoc (`--sign -`, no real
  # identity/notarization involved), but --deep would need to be replaced
  # with an explicit inside-out signing loop before this app is ever
  # notarized with a real Developer ID.
  codesign --force --deep --sign - --entitlements "$ENTITLEMENTS" "$APP"

  echo "    verifying entitlements..."
  if codesign -d --entitlements - "$APP" 2>&1 | grep -q "com.apple.security.device.audio-input"; then
    echo "    ✓ audio-input entitlement embedded"
  else
    echo "    ✗ audio-input entitlement missing — codesign step failed" >&2
    exit 1
  fi

  # Strip quarantine for friction-free local launch.
  xattr -cr "$APP" || true
done

echo
echo "Done. Re-signed ${#APPS[@]} bundle(s)."
echo
echo "First-run permission notes:"
echo "  - macOS will prompt for Microphone and Screen Recording on first use."
echo "  - If prompts don't appear (stale TCC cache), reset and relaunch:"
echo "      tccutil reset Microphone   click.atomai.ttobak.mac"
echo "      tccutil reset Camera       click.atomai.ttobak.mac"
echo "      tccutil reset ScreenCapture click.atomai.ttobak.mac"
