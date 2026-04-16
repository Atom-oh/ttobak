# Infrastructure Module

AWS CDK TypeScript — 7 stacks for the full Ttobak deployment.

## Stack Order
Auth + Storage (parallel) → AI → Knowledge → EdgeAuth (us-east-1) → Gateway → Frontend

## Structure
- `bin/infra.ts` — Stack instantiation and dependency wiring
- `lib/*-stack.ts` — Individual stack definitions
- `test/` — Jest tests (currently skeleton)

## Conventions
- Cross-stack references: Use `Fn.split`/`Fn.select`, not JS string methods
- Lambda@Edge: Must deploy to us-east-1 with `crossRegionReferences: true`
- IAM: Scope policies to specific resource ARNs where possible
- Naming: `ttobak-{resource}` prefix for all resource names
- Build: `npx cdk synth` to validate, `npx cdk deploy --all` to deploy
