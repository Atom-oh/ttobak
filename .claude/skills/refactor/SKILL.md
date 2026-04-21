# Refactor Skill

## Trigger
When user requests code refactoring or cleanup.

## Steps
1. Identify the target scope (file, module, or pattern)
2. Read all affected files to understand current structure
3. Plan refactoring approach:
   - Extract shared logic into reusable functions/hooks
   - Align with existing patterns (e.g., sentinel errors in Go, custom hooks in React)
   - Maintain backward compatibility for API contracts
4. Execute refactoring with incremental changes
5. Verify: `go build ./...` for backend, `npm run build` for frontend, `npx cdk synth` for infra
6. Run existing tests to confirm no regressions

## Constraints
- Do not change public API contracts without explicit approval
- Do not modify DynamoDB key schema
- Keep Lambda cold start impact minimal
