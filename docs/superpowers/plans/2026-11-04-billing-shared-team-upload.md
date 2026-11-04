# Billing shared team upload implementation plan

> Historical implementation record. Status: In progress; implementation and deployment authorized on 2026-11-04.

> For agentic workers: use `superpowers:executing-plans` to implement the tasks.

**Goal:** Allow all current members of an Account or its ancestors to work on that Account's Billing, and accept the user's three-file COT export.

**Architecture:** The Billing BFF resolves fresh canonical membership and complete parent chains. Child references only discover candidates. The Account page trusts the BFF's authorized Billing entries.

**Tech stack:** Node.js 24, Express, AWS SDK v3, DynamoDB, Next.js, CDK.

**Spec:** User request of 2026-11-04: all shared team members must have Billing upload permission. This intentionally refines ADR-056 and ADR-058 only for Billing.

## Constraints

- Preserve direct membership and unrelated Account/document mutation rules.
- Check strongly consistent canonical membership and hierarchy; no authorization cache.
- Deny missing, malformed, cyclic or more-than-64-ancestor inherited paths.
- Bound discovery; fail explicitly instead of truncating.
- Keep JWT verification, CSRF, account upload key isolation and CloudFront ingress.
- Deploy BillingStack, frontend and the existing companion ECS worker image after exact-head review and the content gate. Preserve the companion stack's other resources and settings.

## Tasks

- [x] Add failing inherited upload/discovery/revocation and frontend regressions.
- [x] Implement Billing-only ancestor authorization and paginated descendant discovery.
- [x] Scope DynamoDB Query IAM to Account child references and projected attributes.
- [x] Show authorized inherited Billing entries on Account pages; update policy references.
- [x] Add an atomic hierarchy/membership confirmation after inherited access traversal.
- [x] Accept the core three-file COT ZIP; retain five-file support and bounded, collision-safe workbook extraction.
- [x] Verify staging and datasets against the supplied ZIP without publishing private input or cell values.
- [ ] Run BFF, frontend, infra and documentation verification; independent code/security review.
- [ ] Submit PR, complete exact-head required reviews/checks, merge, content review and deploy.

## Review focus

Stale references must not grant access. Removing membership or changing a parent must revoke subsequent access. Invalid hierarchy must fail closed without blocking direct membership. Discovery must consume empty advancing pages and reject its budget limit. Uploads must remain isolated to the authorized Account.

## Verification record

2026-11-04: inherited registration/upload first failed with 403 instead of 200; the inherited Billing tab first failed visibility. After implementation, BFF 142 tests, frontend 262 tests and production build, infra 290 tests and CDK synth, and documentation checks/tests passed. The first infra run exhausted `/tmp`; the complete rerun used `/home/atomoh/.tmp` and passed.

The native review exposed a sequential-read race. Interleaved reparent/membership and grant-removal regressions failed before atomic confirmation and passed afterward; BFF now passes 145 tests. The supplied ZIP reproduced the separate missing `Template.xlsx` / `PPT_Companion.pptx` rejection. The user authorized three-file support; Current State and COH must both contribute when the combined workbook is absent.

The integrated worker passes 527 tests with both pipeline smoke tests enabled.
The supplied three-file ZIP passes local and credential-free container runs:
31 CSV sheets, 57 datasets and all five reconciliation checks. Frontend lint,
262 unit tests and the production build, 12 static-page tests and six docs tests
pass. Operational review found the existing task role lacks `PutItem`; rerun
lease acquisition now uses conditional `UpdateItem` with a denied-PutItem
regression. These results do not claim production upload or AI acceptance.
