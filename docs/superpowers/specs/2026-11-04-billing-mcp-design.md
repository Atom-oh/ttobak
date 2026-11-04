# Billing MCP design

Status: historical implementation design; deployment requires current review and release evidence.

## Goal and user workflow

External MCP clients must prepare customer meetings using Billing CUR/COT analysis
and reports alongside existing account, meeting and insight tools. List accounts
with the existing tool, read Billing metadata to select a published source/version,
then read a dataset or report, following opaque continuation until complete.

## Architecture and host decisions

Follow ADR-056: call the same application CloudFront origin with the caller's own
Bearer token. Add a bounded GET `/api/billing/accounts/{accountId}/reading` before
the BFF's expensive all-account discovery middleware. Do not assert identity or
add an application origin. Existing Go/QA signed private transport is unchanged.
Only this new route accepts dedicated connector access tokens; browser tokens
remain verified by existing authentication. Connector tokens require RS256,
Cognito issuer/expiry, dedicated client, exact MCP resource audience, username,
openid and resource read scope, and permitted client-specific scopes. Resolve
current client policy and blocked approval atomically before and after reads.
Use the canonical connector grant resolver shared with MCP, packaged as a small
CommonJS artifact; do not copy its authorization policy.

## Read contract

Three read-only tools: `ttobak_get_billing_account` (metadata),
`ttobak_get_billing_dataset` (allowlisted CUR/COT dataset), and
`ttobak_get_billing_report` (Markdown). Parameters: accountId, source for content,
dataset for dataset reads, optional versionId, pageSize (1000–8000 UTF-8 bytes,
default 4000), cursor (maximum 2048 characters). Reject unknown arguments.
Metadata advertises supported datasets and current version/period/AI status;
its dataset list is a supported selection list, not a promise all files exist.
Published content requires a done upload bound to account, source and version.
Current reads bind to the fresh current pointer; explicit versions can read older
published uploads. Missing data and unauthorized accounts return 404.

Reuse Billing canonical direct/validated ancestor access (ADR-060), including
publication-time rechecks. Dataset rows retain only the requested entity; raw
catalog entries filter entities to that account. CUR and COT selectors cannot
read the other's datasets. Reports remain account-key scoped plain Markdown.
Never return presigned object URLs, credentials or raw client policy rows.

API JSON is capped at 14000 encoded bytes and MCP results at 32000. Content is
UTF-8-safe fragments in dataJson or markdown, with revision, offsets and complete/
nextCursor metadata. Concatenate same-revision fragments, then parse dataJson.
Cursor binds user, account, selection, resolved version, page size and rendered
content revision. Changed selection, current version, source bytes or publication
state rejects continuation with 409. Canonical access is checked independently
on every request and again after source reads; cursors grant no access.
Source stream and parsed dataset are bounded to the existing 48 MiB BFF cap.
Read exact source bytes afresh (no stale agent rerun cache), pin S3 ETag/version
and check identity again before publication. Return 413 for oversized content.

## Delivery and limits

No upload, rerun, mutation or QA tool is added. Browser routes retain their
existing client allowlist. Strongly consistent platform pointer/upload reads back source publication checks.
The edge writer allowlist permits only the exact GET Billing reading route in
addition to its existing MCP path. Shared grant source changes trigger Billing
tests/package/deployment via narrow workflow path filters; no gate is weakened.
BillingStack gets dedicated connector configuration
and restricted metadata-only grant read permissions. Both BFF and MCP packages
must be built/tested by their existing tracked workflows. Deploy Billing and
MCP, verifying both live artifacts and anonymous rejection. An authenticated
client smoke needs an actual user token; do not fabricate identity or claim it
passed without evidence.
