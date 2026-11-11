# Billing shared team upload implementation record

Historical record; implementation/deployment authorized on 2026-11-04.

**Goal:** Direct and ancestor Account team Billing access; core-three COT ZIPs
with five-file compatibility. Requirements: [ADR-012](../../decisions/ADR-012-billing-origin-identity-and-team-access.md).
Delivery: [Billing release](../../runbooks/billing-release.md).

Local verification: BFF145; worker527 with two smoke tests; frontend lint,
262 tests/build; static page12; infra290/synth; docs checks/six tests.
Private ZIP local/container results:31 sheets,57 datasets,five checks.
Conditional UpdateItem leases fit the existing role. Production upload/AI
acceptance and exact-head CI/merge/deployment require separate evidence.
