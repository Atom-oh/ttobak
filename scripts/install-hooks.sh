#!/usr/bin/env bash
# Install git hooks
set -euo pipefail

HOOKS_DIR="$(git rev-parse --git-dir)/hooks"
mkdir -p "$HOOKS_DIR"

# commit-msg hook: remove Co-Authored-By lines
cat > "$HOOKS_DIR/commit-msg" << 'HOOK'
#!/usr/bin/env bash
# Remove Co-Authored-By lines from commit messages
TEMP=$(mktemp)
grep -v '^Co-Authored-By:' "$1" > "$TEMP" || true
# Remove trailing blank lines
sed -e :a -e '/^\n*$/{$d;N;ba' -e '}' "$TEMP" > "$1"
rm -f "$TEMP"
HOOK

chmod +x "$HOOKS_DIR/commit-msg"
echo "Installed commit-msg hook (removes Co-Authored-By lines)"
