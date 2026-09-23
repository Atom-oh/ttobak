# TTOBAK MCP adapter

TypeScript MCP server with stdio and authenticated Streamable HTTP entry points.
Both call TTOBAK HTTPS APIs through CloudFront; neither accesses DynamoDB/S3 with
an AWS execution role. Stdio is the default. The HTTP server implementation does
not, by itself, provision a hosted endpoint.

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

## Streamable HTTP

```bash
npm start                          # stdio (default)
npm start -- --transport http       # installed HTTP server
npm run start:http                  # equivalent HTTP entry point
```

The single-file download is the local stdio adapter; HTTP hosting uses the full
installed package. Both share the registry/API code. `TTOBAK_MCP_TRANSPORT=http`
is another selector; an explicit flag wins. HTTP clients connect by URL and do
not download or launch the server. Tool-search deferral remains a host feature.

HTTP requires the existing API URL, Cognito Hosted UI domain and public client ID,
plus `TTOBAK_USER_POOL_ID` and `TTOBAK_MCP_PUBLIC_URL`. For example:

```bash
export TTOBAK_API_URL=https://ttobak.example.com
export TTOBAK_COGNITO_DOMAIN=https://your-domain.auth.ap-northeast-2.amazoncognito.com
export TTOBAK_CLIENT_ID=yourRegisteredPublicClientId
export TTOBAK_USER_POOL_ID=ap-northeast-2_yourPoolId
export TTOBAK_MCP_PUBLIC_URL=https://ttobak.example.com/api/mcp
npm run start:http
```

Every POST gets a fresh server, transport and API client. Verify RS256/JWKS,
issuer, expiry, client ID, user subject/username, all configured scopes and an
`aud` exactly equal to the public MCP URL before any MCP operation. Cognito's
OAuth request must include that URL as its RFC 8707 `resource`. ID tokens,
unbound access tokens and service identities are rejected. Only public keys are
cached. Clients own PKCE login, refresh and logout; HTTP hides the local
`ttobak_login`/`ttobak_logout` tools and never reads `~/.ttobak/tokens.json`.

`/.well-known/oauth-protected-resource` and its path-specific equivalent are
public discovery documents. A 401 includes their explicit `WWW-Authenticate` URL.
They advertise the actual Cognito issuer and scopes; discovery uses its OIDC
metadata. This process is not an authorization server or DCR service. Use a
pre-registered public OAuth client and register each host's exact callback.

| Optional variable | Default / constraint |
| --- | --- |
| `TTOBAK_HTTP_HOST` | `127.0.0.1`; other interfaces require an HTTPS public URL behind approved ingress |
| `TTOBAK_HTTP_PORT` | `3000`; `0` prints an ephemeral port to stderr |
| `TTOBAK_HTTP_ALLOWED_HOSTS` | Public host plus exact comma-separated proxy/local Host values; no wildcards |
| `TTOBAK_HTTP_ALLOWED_ORIGINS` | Public origin plus exact browser origins; absent Origin is permitted |
| `TTOBAK_MCP_SCOPES` | `openid email profile`; all required, including mandatory `openid` |
| `TTOBAK_HTTP_TIMEOUT_MS` | `55000`; configurable from 1000 to 55000 |

The public resource and upstream API share the application CloudFront origin.
The optional Gateway context `ttobak:mcpResourceAudience` admits this exact HTTPS
`/api/mcp` audience alongside the existing client audience. Without that setup,
API Gateway checks the URL `aud` instead of falling back to `client_id` and rejects
the token. The edge client-ID gate and OriginVerify remain required. The setting
creates no route, listener, callback or authentication bypass and is off by default.

HTTP uses stateless JSON responses; authenticated GET/SSE and DELETE return 405.
Limits: one JSON-RPC message, 1 MiB UTF-8 request, 32 active requests, 32,000-byte
final tool result, 1 MiB general upstream response (reading retains its tighter
32,000-byte wire cap). Compressed bodies are rejected. The absolute deadline
includes auth/body/API work, and disconnects abort upstream HTTP. Writes never
retry automatically: a timeout can leave an already-completed mutation. Inspect
current state before retrying. Oversized reads fail without invented continuation.
Successful writes return a compact completion receipt with saved record IDs and
an explicit omitted-response marker when the full response exceeds the bound.

Upload tools keep their names but accept `fileName` and `contentBase64` instead of
`filePath`, with at most 512 KiB decoded. Document upload also requires `title`.
Both dispatch and API adapters reject server filesystem access. Use stdio or the
app for larger files. Vault export is already API-based but can exceed HTTP's
result bound.

Settings provides the client-specific instructions. Quick is remote-only, uses
User OAuth/public PKCE, has a 60-second timeout and a 100-tool cap, and requires
Sync after custom tool changes. It cannot use custom auth headers. Kiro Crew's
remote-header flow needs separately issued resource-bound user credentials;
never share one user's token across a shared Crew. Kiro CLI Tool Search is enabled
separately with `kiro-cli settings toolSearch.enabled true` and retains its size
thresholds. Existing host approval settings must be preserved.

This implementation does not provision a hosted origin or register real clients.
Route the authenticated endpoint and minimal public discovery through the approved
CloudFront boundary. Publish `mcp: { "url": "/api/mcp" }` in runtime config only
after deployment acceptance. Validate each client's exact callback, OIDC discovery,
resource binding, refresh and representative operations. Access tokens need not
carry email/verified-email claims: do not fabricate them or weaken pending-invite
checks. Source wiring and synthetic tests do not prove live client acceptance.
See [ADR-045](../docs/decisions/ADR-045-dual-mcp-transports.md).

## Tool behavior

`src/index.ts` is the shared tool/schema inventory; `src/api.ts` implements HTTP
contracts and errors. The adapter covers login/status, meeting list/detail/QA,
accounts and briefs/insights, personal/account documents, KB files/ingestion, vault
export, projects and links.

- Omit accountId for personal Document Hub operations; provide it explicitly for
  account space. Directly shared documents can be read, but only owners revise them.
- put_document creates a new ID. update_document preserves the ID and omitted body;
  include required title/metadata fields according to its schema.
- Read Document Hub content directly for current text. Canonical automatic indexing
  is implemented but gated behind snapshot verification and strict QA cutover;
  deployment acceptance must be verified independently of the checked-in mode.
  A saved document or index-status route does not establish active canonical indexing.
- For account hierarchy filters, list accessible accounts and explicitly include
  selected group/descendant IDs in accountIds. Preserve the same filter/cursor
  selection across pages; hierarchy does not grant access.
- Project lists include owner, direct-member and linked-account membership paths.
  Link/mutation permissions remain server-enforced. Any existing account member
  can add another member to an assignable non-owner role (ADR-034).
- HTTP failures are tool errors, not success with error-shaped JSON. Streaming
  decoding must preserve UTF-8 across byte boundaries.

## Bounded meeting and transcript reads

`ttobak_get_meeting({"meetingId":"id"})` now returns saved **notes** first.
Use `section:"summary"` for generated content and `section:"actionItems"` for
full action-item JSON. It no longer returns full A/B transcripts, speakerMap,
attachments or shares. The original tool name/meetingId input remain valid.

Both reading tools use only authenticated
`GET /api/meetings/{meetingId}/reading`. Deploy that API before this adapter.
Missing routes, authorization failures or read errors are tool errors, with no
fallback to full detail or cached text. The backend owns source selection,
Unicode pagination, segment validation and opaque cursors; the adapter does not
reslice server pages.

```json
{"meetingId":"id","section":"summary","pageSize":4000}
```

Meeting pages contain exact notes/content/actionItemsJson and bounded metadata,
action preview and analysis status. Inspect actionItemsPreview's
available/complete/totalItems/metadataTruncated fields. Absent legacy analysis
is unknown, never inferred successful from `[]`. Join **all** actionItemsJson
pages before parsing; each page may end within a JSON string. Full pages preserve
extension fields and completion state omitted from previews.

Use `ttobak_read_transcript` for selected or explicit A/B text:

```json
{"meetingId":"id","source":"selected","pageSize":4000,"startTime":60,"endTime":120}
```

- source defaults to selected; explicit A/B never borrows the selected variant's
  speaker/timing metadata. Optional startTime/endTime must both be finite seconds
  with `0 <= startTime < endTime`.
- Join chunks[].text in order and follow page.nextCursor unchanged with the same
  meeting/section or source/time range. Read every preceding page before treating
  complete=true and nextCursor=null as a complete read of that requested scope.
- Offsets are zero-based Unicode code points with exclusive ends, not UTF-16
  indices or byte offsets. pageSize is 1–8000, default 4000.
- Verified time ranges select whole segments overlapping the interval. A partial
  chunk retains the original segment's whole-segment times; never infer word
  boundaries. Without verified segments, raw text is available but a time-window
  request returns TIME_RANGE_UNAVAILABLE.
- The API caps JSON at 14,000 bytes including its newline and 50 transcript chunks,
  so pages can be shorter than requested. The adapter aborts HTTP bodies above
  32,000 bytes before buffering/JSON parsing and separately caps the wrapped MCP
  result at 32,000 bytes.
- Treat revision/cursor as opaque. Every continuation rechecks access and source
  revision; STALE_CURSOR requires restarting without the cursor.

Notes-only reads avoid transcript hydration on the server; transcript reads still
load the chosen source to validate/page it. See the
[API contract](../docs/API-SPEC.md#bounded-meeting-reading) for response/error details.

## Public bundle provenance

`frontend/public/mcp/ttobak-mcp.mjs` is generated from this module, never hand-edited.
After source changes, build/bundle and copy it from the repository root:

```bash
(cd mcp-server && npm run build && npm run bundle)
cp mcp-server/dist/ttobak-mcp.mjs frontend/public/mcp/ttobak-mcp.mjs
diff mcp-server/dist/ttobak-mcp.mjs frontend/public/mcp/ttobak-mcp.mjs
```

`npm test` builds modules, runs stdio/HTTP protocol and JWT isolation regressions,
and builds the bundle twice to check reproducibility, bounded stdio reads and
HTTP startup in the installed package and rejection of HTTP mode in the client artifact. Fixtures exercise real
HTTP/auth code and oversized-response aborts with synthetic keys and data;
Go tests own source verification, Unicode pagination and cursor validity.
`npm run test:bundle` runs bundle checks alone. Tests do not copy the public
artifact; CI's byte comparison verifies the committed copy.

For connection failures, first verify the executable path, target configuration,
OAuth callback/scopes and current user login. Do not solve authorization failures
by granting anonymous API access. See [API reference](../docs/API-SPEC.md) and
[ADR-003](../docs/decisions/ADR-003-mcp-server-for-external-meeting-access.md).
