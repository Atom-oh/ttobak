# Run All Tests

Execute the full test suite across all project modules.

## Steps
1. Backend Go tests: `cd backend && /usr/local/go/bin/go test ./...`
2. Frontend lint: `cd frontend && npm run lint`
3. Frontend build (type check): `cd frontend && npm run build`
4. CDK tests: `cd infra && npm test`
5. Report pass/fail summary for each module
6. If any failures, show detailed error output
