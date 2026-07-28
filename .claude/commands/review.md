# Code Review

Review uncommitted changes for bugs, security issues, and convention violations.

## Steps
1. Get diff: `git diff` for unstaged, `git diff --cached` for staged
2. Analyze each changed file against project conventions
3. Check Go error handling, React hooks rules, CDK IAM policies
4. Report findings by severity with file:line references
5. Suggest fixes for critical and major issues
