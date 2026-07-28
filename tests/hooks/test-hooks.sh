#!/usr/bin/env bash
# Test hook files exist and are executable

echo "# Hook tests"

assert_file_exists ".claude/hooks/session-context.sh" "session-context hook exists"
assert_file_exists ".claude/hooks/secret-scan.sh" "secret-scan hook exists"
assert_file_exists ".claude/hooks/check-doc-sync.sh" "check-doc-sync hook exists"
assert_file_exists ".claude/hooks/notify.sh" "notify hook exists"

assert_executable ".claude/hooks/session-context.sh" "session-context hook is executable"
assert_executable ".claude/hooks/secret-scan.sh" "secret-scan hook is executable"
assert_executable ".claude/hooks/check-doc-sync.sh" "check-doc-sync hook is executable"
assert_executable ".claude/hooks/notify.sh" "notify hook is executable"

# Check settings.json registers hooks
assert_file_exists ".claude/settings.json" "settings.json exists"
assert_contains ".claude/settings.json" "SessionStart" "settings.json has SessionStart hook"
assert_contains ".claude/settings.json" "PreToolUse" "settings.json has PreToolUse hook"
assert_contains ".claude/settings.json" "PostToolUse" "settings.json has PostToolUse hook"
