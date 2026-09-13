# ADR-018: Bidirectional MCP Knowledge Tools

- Status: Accepted; extends [ADR-003](ADR-003-mcp-server-for-external-meeting-access.md).
  The original documents-only write restriction is superseded by the current tools.
- Decision date: Not recorded in the original ADR.
- Implementation checked: 2026-09-13.

## Context and decision

External agents need to pull account knowledge for preparation and push new notes
back into TTOBAK. Implement thin MCP wrappers over authenticated REST endpoints;
keep ownership, membership, validation, and storage access in backend services.
Direct DynamoDB access would duplicate and bypass those invariants. Keeping all
MCP tools read-only would prevent the intended contribution workflow.

## Current surface and authorization

| Tool group | Backend contract |
| --- | --- |
| Account brief/insights/meetings | Account-member-gated REST reads |
| Vault export | Caller-scoped export and limits in ADR-017 |
| Put/list/get/update document | Personal partition when `accountId` is omitted; account scope when supplied; backend ownership/member gates apply |
| Upload document | Local file to a signed upload URL, then document creation; separate from inline Markdown |
| KB upload/list/delete | Authenticated user's `kb/{userID}/` objects |
| KB sync | Authenticated request to ingest the configured shared data source |
| Create account/add account member | Account creation plus existing-member-gated addition; assignable roles exclude `owner` |
| Project tools | Create/read/update and account-link operations under project service authorization (ADR-025) |

Use the registered `ttobak_*` names in `index.ts`; the old unprefixed names and
fixed tool count are historical. `ttobak_put_document` creates a fresh ID;
`ttobak_update_document` revises an existing document. The adapter exposes no
meeting-content editing tool, but writes are no longer limited to documents.

**ADR-034 supersedes the old owner-only member-add claim.** Any current account
member can add an assignable role; removal/pending-revocation remain owner-only
in the REST service. Account hierarchy does not confer inherited authorization.

The shared KB is shared infrastructure, not unrestricted user-upload visibility.
Q&A filters user uploads to the caller's prefix alongside authorized meetings and
shared crawler documents. S3 deletion alone does not immediately remove the
indexed content; visibility changes with ingestion.

## Ingestion and local-file limits

`SyncKB` returns `skipped` when KB configuration is absent; configured ingestion
failures return an error rather than `skipped`. IaC supplies `KB_ID` and
`KB_DATASOURCE_ID` to the API and a KB-scoped `bedrock:StartIngestionJob` grant.
The summarize worker's variable is separately named `DATA_SOURCE_ID`. These are
source/configuration facts, not proof that a specific deployment contains them.

Meeting-summary ingestion and document-bearing crawler runs can also index the
shared data source. A zero-change crawler run skips ingestion, so these fallback
triggers do not guarantee a deadline for indexing an MCP upload. Older broader
worker ingestion grants remain existing IAM tightening work, not a template for
new wildcard grants.

`guardUploadPath` resolves symlinks, blocks sensitive paths/filenames, and applies
50 MiB KB / 100 MiB document limits. It is not a sandbox; the MCP host's tool-call
approval remains the local-file access gate. Signed S3 PUT requests use the URL's
signature, not the Cognito bearer token.

## Consequences and accepted risks

Agents can contribute through the same service boundaries as the app. Token
compromise now exposes account, project, document, and KB capabilities, so the
original documents-only mitigation no longer describes the blast radius. The
Markdown loop guard prevents accidental reimports, not low-quality or duplicative
content; binary uploads do not run that Markdown guard. Tool/client/bundle/docs
must stay aligned, and uploads/deletions remain subject to asynchronous indexing.

## Evidence

- [index.ts](../../mcp-server/src/index.ts), [api.ts](../../mcp-server/src/api.ts),
  [documents.test.mjs](../../mcp-server/test/documents.test.mjs): tools and contracts.
- [account.go](../../backend/internal/service/account.go),
  [project.go](../../backend/internal/service/project.go),
  [kb.go](../../backend/internal/service/kb.go): actual authorization and ingestion.
- [handler.py](../../backend/python/qa/handler.py): KB access filtering.
- [gateway-stack.ts](../../infra/lib/gateway-stack.ts),
  [ai-stack.ts](../../infra/lib/ai-stack.ts),
  [gateway-stack.test.ts](../../infra/test/gateway-stack.test.ts): env/IAM wiring.
- [ingest_trigger.py](../../backend/python/crawler/ingest_trigger.py): zero-change skip.
