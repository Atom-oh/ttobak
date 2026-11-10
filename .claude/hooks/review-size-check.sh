#!/usr/bin/env bash
# PreToolUse hook (Bash) — blocks `gh pr create` / `git push` when the AI review
# input would exceed the CI diff limits. Never blocks on checker or git errors.
set -uo pipefail

INPUT=$(cat)
ROOT="${CLAUDE_PROJECT_DIR:-$(pwd)}"
CHECKER="${REVIEW_SIZE_CHECKER:-$ROOT/scripts/pr-review/check_review_size.py}"

# Prints "none" (not relevant), "create <base>" or "push" for the command.
DECISION=$(printf '%s' "$INPUT" | python3 -c '
import json, re, shlex, sys
try:
    command = json.load(sys.stdin).get("tool_input", {}).get("command", "")
except Exception:
    command = ""
result = "none"
for segment in re.split(r"&&|\|\||[;|\n]", command):
    try:
        words = shlex.split(segment)
    except ValueError:
        continue
    while words and re.fullmatch(r"[A-Za-z_][A-Za-z0-9_]*=.*", words[0]):
        words.pop(0)
    if words[:3] == ["gh", "pr", "create"]:
        if "--help" in words or "-h" in words:
            continue
        base = ""
        for i, word in enumerate(words):
            if word in ("--base", "-B") and i + 1 < len(words):
                base = words[i + 1]
            elif word.startswith("--base="):
                base = word[7:]
        result = "create " + base
        break
    if words[:2] == ["git", "push"]:
        flags = [w for w in words[2:] if w.startswith("-")]
        args = [w for w in words[2:] if not w.startswith("-")]
        if any(f in ("--dry-run", "-n", "--delete", "-d") for f in flags):
            continue
        refs = args[1:]
        if "--tags" in flags and not refs:
            continue
        if refs and all(r.startswith("refs/tags/") or r == "tag" or r.startswith(":") for r in refs):
            continue
        result = "push"
        break
print(result)
' 2>/dev/null || echo none)

case "$DECISION" in
  create*) BASE="${DECISION#create}"; BASE="${BASE# }" ;;
  push) BASE="" ;;
  *) exit 0 ;;
esac

if [ -z "$BASE" ]; then
  if [ "$DECISION" = "push" ]; then
    BASE=$(cd "$ROOT" && gh pr view --json baseRefName -q .baseRefName 2>/dev/null || true)
  fi
  BASE="${BASE:-main}"
fi
BASE="${BASE#origin/}"

OUTPUT=$(cd "$ROOT" && python3 "$CHECKER" --base "origin/$BASE" 2>&1)
STATUS=$?
case "$STATUS" in
  0) [ -n "$OUTPUT" ] && echo "$OUTPUT" | grep -i '^WARNING' >&2; exit 0 ;;
  1) echo "$OUTPUT" >&2; exit 2 ;;
  *) echo "review-size-check: skipped (checker exit $STATUS): $OUTPUT" >&2; exit 0 ;;
esac
