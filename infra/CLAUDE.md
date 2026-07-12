# Infrastructure Module

AWS CDK TypeScript — 11 stacks for the full TTOBAK deployment. See the root `CLAUDE.md`'s "CDK Stack Dependency Order" for the actual `addDependency` graph.

## Structure
- `bin/infra.ts` — Stack instantiation and dependency wiring
- `lib/*-stack.ts` — Individual stack definitions
- `test/` — Jest tests (currently skeleton)

## Conventions
- Cross-stack references: Use `Fn.split`/`Fn.select`, not JS string methods
- Lambda@Edge / cross-region stacks (`WebSearchGatewayStack`, `EdgeAuthStack`): must deploy to us-east-1 with `crossRegionReferences: true` on both the producer and any consumer stack
- IAM: Scope policies to specific resource ARNs where possible
- Naming: `ttobak-{resource}` prefix for all resource names
- Build: `npx cdk synth` to validate. **Never `cdk deploy --all`** — always `npx cdk deploy TtobakGatewayStack --exclusively` (see root `CLAUDE.md` Known Issues for why)
