#!/usr/bin/env bash
# Test project structure integrity

echo "# Project structure tests"

# Core files
assert_file_exists "CLAUDE.md" "Root CLAUDE.md exists"
assert_file_exists ".gitignore" ".gitignore exists"
assert_file_exists ".mcp.json" ".mcp.json exists"

# Module CLAUDE.md files
assert_file_exists "backend/CLAUDE.md" "Backend CLAUDE.md exists"
assert_file_exists "frontend/CLAUDE.md" "Frontend CLAUDE.md exists"
assert_file_exists "infra/CLAUDE.md" "Infra CLAUDE.md exists"
assert_file_exists "docs/CLAUDE.md" "Docs CLAUDE.md exists"

# Documentation
assert_file_exists "docs/architecture.md" "Architecture doc exists"
assert_file_exists "docs/onboarding.md" "Onboarding doc exists"
assert_file_exists "docs/decisions/.template.md" "ADR template exists"
assert_file_exists "docs/runbooks/.template.md" "Runbook template exists"

# Skills
assert_file_exists ".claude/skills/code-review/SKILL.md" "Code review skill exists"
assert_file_exists ".claude/skills/refactor/SKILL.md" "Refactor skill exists"
assert_file_exists ".claude/skills/release/SKILL.md" "Release skill exists"
assert_file_exists ".claude/skills/sync-docs/SKILL.md" "Sync docs skill exists"

# Commands
assert_file_exists ".claude/commands/review.md" "Review command exists"
assert_file_exists ".claude/commands/test-all.md" "Test-all command exists"
assert_file_exists ".claude/commands/deploy.md" "Deploy command exists"

# Agents
assert_file_exists ".claude/agents/code-reviewer.yml" "Code reviewer agent exists"
assert_file_exists ".claude/agents/security-auditor.yml" "Security auditor agent exists"

# CLAUDE.md content checks
assert_contains "CLAUDE.md" "Auto-Sync Rules" "CLAUDE.md has Auto-Sync Rules section"
assert_contains "CLAUDE.md" "Build Commands" "CLAUDE.md has Build Commands section"
assert_contains "CLAUDE.md" "Architecture" "CLAUDE.md has Architecture section"

# Backend build files
assert_dir_exists "backend/cmd/api" "Backend api cmd exists"
assert_dir_exists "backend/cmd/transcribe" "Backend transcribe cmd exists"
assert_dir_exists "backend/internal/handler" "Backend handler pkg exists"
assert_dir_exists "backend/internal/service" "Backend service pkg exists"
