#!/usr/bin/env bash
# Feeds sample hook JSON to review-size-check.sh with a stubbed checker.
set -u
DIR="$(cd "$(dirname "$0")" && pwd)"
TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT
fail=0
stub() { printf '#!/usr/bin/env python3\nimport sys\nopen("%s/args","w").write(" ".join(sys.argv[1:]))\nprint("%s")\nsys.exit(%s)\n' "$TMP" "$2" "$1" > "$TMP/checker.py"; }
run() { rm -f "$TMP/args"; printf '{"tool_input":{"command":%s}}' "$(python3 -c 'import json,sys;print(json.dumps(sys.argv[1]))' "$1")" \
  | REVIEW_SIZE_CHECKER="$TMP/checker.py" CLAUDE_PROJECT_DIR="$TMP" bash "$DIR/review-size-check.sh" >/dev/null 2>&1; }
expect() { # name expected_exit expected_ran command
  run "$4"; code=$?; ran=no; [ -f "$TMP/args" ] && ran=yes
  if [ "$code" != "$2" ] || [ "$ran" != "$3" ]; then echo "FAIL $1: exit=$code ran=$ran"; fail=1; else echo "ok   $1"; fi; }
stub 1 "too big"
expect "pr create blocked" 2 yes 'gh pr create --base develop --title x'
grep -q 'origin/develop' "$TMP/args" || { echo "FAIL base arg"; fail=1; }
expect "push blocked" 2 yes 'git push -u origin feature'
expect "ls ignored" 0 no 'ls -la'
expect "dry-run ignored" 0 no 'git push --dry-run origin x'
expect "delete ignored" 0 no 'git push origin --delete old'
expect "tags ignored" 0 no 'git push --tags'
expect "chained push" 2 yes 'cd x && git push origin feature'
stub 0 "fine"
expect "within limit" 0 yes 'git push origin feature'
stub 2 "git broke"
expect "checker error fails open" 0 yes 'git push origin feature'
exit $fail
