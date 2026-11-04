# ADR-060: Billing shared team access

- Status: Accepted on 2026-11-04 following the request to allow all shared team members to upload Billing files.
- Supersedes ADR-056's direct-membership-only Account access and ADR-058's Billing exclusion. Other ADR-058 mutation restrictions remain.

Billing grants read, upload, status, rerun and version-management access to any current direct member or member of an ancestor Account, independent of member role. Descendant membership does not grant parent or sibling access. Pending invitations grant nothing.

The BFF resolves membership and the complete canonical parent chain with strongly consistent uncached reads. Missing, malformed, cyclic or more-than-64-ancestor inherited paths deny access. Direct membership retains its existing behavior independently of hierarchy validity. Removal and reparenting revoke subsequent requests; display metadata is never authorization evidence.

GSI1 memberships and paginated `ACCOUNT#{parent}/CHILDREF#{child}` keys only discover candidates. Every candidate is independently authorized. Discovery has a 1,000-account and 4,096-DynamoDB-request budget; exhaustion returns an explicit unavailable response instead of partial results. Current child references must already be bootstrapped under ADR-058.

The Account frontend shows Billing only when the authenticated BFF returns an authorized entry. Other Account edit controls retain direct-membership restrictions. JWT verification, CSRF checks, Account-scoped multipart keys and CloudFront ingress remain.

Release requires exact-head reviews, tests, the content gate and deployment of only BillingStack plus frontend. Deployment and authenticated-user acceptance require separate evidence; this decision alone proves neither.
