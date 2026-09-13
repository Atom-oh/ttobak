# TTOBAK MCP adapter

Local TypeScript stdio MCP server. It calls authenticated TTOBAK HTTPS APIs through
CloudFront; it is not a hosted HTTP MCP service and does not access DynamoDB/S3 with
an AWS execution role. Any compatible host can launch its Node entry point.

## Build and configure

```bash
npm ci
npm test
npm run bundle
```

Configure the host to run `node` with the absolute path to `dist/index.js` and
these environment values for the target deployment:

| Variable | Value |
|---|---|
| TTOBAK_COGNITO_DOMAIN | Cognito hosted domain URL |
| TTOBAK_CLIENT_ID | Public OAuth-capable client ID |
| TTOBAK_API_URL | Application CloudFront URL |
| TTOBAK_REGION | Application region |

The OAuth client must allow authorization-code PKCE, openid/email/profile scopes
and `http://localhost:9876/callback`. Current AuthStack configures the SPA client
for this use; inspect it instead of inventing another mandatory client. The
setup helper reads CloudFormation and writes host configuration; inspect its
selected values before running it. Normal MCP calls need Cognito login, not AWS
credentials. Keep stdout reserved for MCP and diagnostics on stderr.

First authentication opens the browser, validates callback state and exchanges the
PKCE code. Tokens are stored at `~/.ttobak/tokens.json` with restrictive creation
permissions. Refresh is attempted before requiring another login; do not promise
a fixed login duration independent of pool policy or revocation. Logout clears
local tokens. Do not commit/copy token files into documentation or diagnostics.

## Tool behavior

`src/index.ts` is the exact tool/schema inventory; `src/api.ts` implements HTTP
contracts and errors. The adapter covers login/status, meeting list/detail/QA,
accounts and briefs/insights, personal/account documents, KB files/ingestion, vault
export, projects and links.

- Omit accountId for personal Document Hub operations; provide it explicitly for
  account space. Directly shared documents can be read, but only owners revise them.
- put_document creates a new ID. update_document preserves the ID and omitted body;
  include required title/metadata fields according to its schema.
- Document Hub Markdown is not automatically indexed by the QA KB. Read documents
  directly for their current contents; KB upload/ingestion is a different path.
- For account hierarchy filters, list accessible accounts and explicitly include
  selected group/descendant IDs in accountIds. Preserve the same filter/cursor
  selection across pages; hierarchy does not grant access.
- Project lists include owner, direct-member and linked-account membership paths.
  Link/mutation permissions remain server-enforced. Any existing account member
  can add another member to an assignable non-owner role (ADR-034).
- HTTP failures are tool errors, not success with error-shaped JSON. Streaming
  decoding must preserve UTF-8 across byte boundaries.

## Public bundle provenance

`frontend/public/mcp/ttobak-mcp.mjs` is generated from this module, never hand-edited.
After source changes, build/bundle and copy it from the repository root:

```bash
(cd mcp-server && npm run build && npm run bundle)
cp mcp-server/dist/ttobak-mcp.mjs frontend/public/mcp/ttobak-mcp.mjs
diff mcp-server/dist/ttobak-mcp.mjs frontend/public/mcp/ttobak-mcp.mjs
```

For connection failures, first verify the executable path, target configuration,
OAuth callback/scopes and current user login. Do not solve authorization failures
by granting anonymous API access. See [API reference](../docs/API-SPEC.md) and
[ADR-003](../docs/decisions/ADR-003-mcp-server-for-external-meeting-access.md).
