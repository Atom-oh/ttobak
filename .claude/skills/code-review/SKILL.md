# Code Review Skill

## Trigger
When user runs `/review` or requests a code review.

## Steps
1. Run `git diff --cached` (staged) or `git diff` (unstaged) to get changes
2. For each changed file, analyze:
   - **Correctness**: Logic errors, edge cases, off-by-one errors
   - **Security**: Input validation, auth checks, XSS, injection
   - **Performance**: N+1 queries, unbounded operations, missing pagination
   - **Conventions**: Matches project patterns in CLAUDE.md (sentinel errors, chi router patterns, Tailwind classes)
3. For Go files: Check error handling, context propagation, DynamoDB expression usage
4. For TypeScript files: Check React hooks rules, useEffect cleanup, state management
5. For CDK files: Check IAM least privilege, DLQ presence, resource naming
6. Output findings grouped by severity (critical/major/minor) with file:line references
