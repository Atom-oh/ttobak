# Billing shared team upload implementation record

Historical record; implementation and deployment authorized on 2026-11-04.

**Goal:** Permit current direct and ancestor Account team members to upload
Billing data; accept core-three COT exports while retaining five-file support.

Current requirements: [ADR-060](../../decisions/ADR-060-billing-shared-team-access.md).
Delivery and acceptance: [Billing release](../../runbooks/billing-release.md).

**Verified locally:** Atomic inherited-access race regressions and 145 BFF tests;
527 worker tests including two smoke tests; frontend lint, 262 tests and build;
12 static-page tests; 290 infra tests and CDK synth; docs checks and six tests.
The supplied ZIP passed credential-free local/container runs: 31 CSV sheets,
57 datasets, five reconciliation checks. Lease acquisition uses conditional
UpdateItem compatible with the existing worker role. Production upload and AI
acceptance remain separate; exact-head CI/review, merge and deployment are pending.
