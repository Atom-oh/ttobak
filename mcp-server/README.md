# TTOBAK MCP adapter

TypeScript MCP adapter with local stdio and authenticated Streamable HTTP modes.
Both call TTOBAK HTTPS APIs through CloudFront without an AWS execution role.
The downloadable single-file client remains stdio; HTTP hosting uses the installed
package and requires separately configured infrastructure and OAuth callbacks.

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

## HTTP server (installed package)

```bash
npm start                         # stdio default
npm run start:http                # installed HTTP server
```

HTTP adds required `TTOBAK_USER_POOL_ID` and `TTOBAK_MCP_PUBLIC_URL` to the existing
configuration. The latter is the exact HTTPS resource URL, normally the same-site
`/api/mcp`. The upstream API must explicitly accept that resource audience while
retaining client-ID/issuer/expiry and user authorization checks. No HTTP origin,
callback, anonymous application route or audience change is provisioned here.

The server verifies Cognito user access tokens: RS256/JWKS, issuer, expiry,
client ID, user subject/username, exact resource audience and configured scopes.
ID tokens and service identities are not accepted. Defaults are `openid email
profile`; clients own PKCE login/refresh/logout and must register exact callbacks.
Cognito's OAuth `resource` must equal the advertised MCP URL. Local token files
are never shared. Public resource metadata and explicit 401 discovery headers
point to the actual Cognito issuer. Live host login remains an acceptance gate.

Optional HTTP variables: `TTOBAK_HTTP_HOST` (127.0.0.1), `TTOBAK_HTTP_PORT` (3000),
`TTOBAK_HTTP_ALLOWED_HOSTS`, `TTOBAK_HTTP_ALLOWED_ORIGINS` (exact values, no wildcards),
`TTOBAK_MCP_SCOPES`, and `TTOBAK_HTTP_TIMEOUT_MS` (at most 55000). Non-loopback
listening requires approved HTTPS ingress. Each POST gets a fresh authenticated
server/transport/API instance. GET streams and DELETE sessions are unsupported.

Bounds: 1 MiB JSON request, 32 active calls, 32,000-byte tool results, 1 MiB general
upstream responses, and the existing tighter reading-page bounds. Compressed bodies
are rejected. Disconnects/deadlines abort HTTP work. Writes never retry automatically;
a timeout may leave a completed mutation, so inspect current state before retrying.

HTTP hides local login/logout tools. Uploads use `fileName` plus `contentBase64`
(at most 512 KiB decoded), never server-local paths. Document uploads also need
`title`. Larger files use stdio or the app. Large exports fail instead of being
truncated or assigned invented continuation. Quick's 60-second operation limit
still applies. Missing email claims are never fabricated to bypass invitation gates.
See [ADR-045](../docs/decisions/ADR-045-dual-mcp-transports.md).

## Tool behavior

`src/index.ts` is the exact tool/schema inventory; `src/api.ts` implements HTTP
contracts and errors. The adapter covers login/status, meeting list/detail/QA,
accounts and briefs/insights, personal/account documents, KB files/ingestion, vault
export, projects and links.

- Omit accountId for personal Document Hub operations; provide it explicitly for
  account space. Directly shared documents can be read, but only owners revise them.
- put_document creates a new ID. update_document preserves the ID and omitted body;
  include required title/metadata fields according to its schema.
- Read Document Hub content directly for current text. Canonical automatic indexing
  is implemented but gated behind snapshot verification and strict QA cutover;
  the app currently enables only manual-only snapshot scheduling. A saved document
  or index-status route does not establish active canonical indexing.
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

`npm test` builds modules, runs protocol regressions, and builds the bundle twice
to check reproducibility and the same bounded-reading behavior in the standalone
artifact. Fixtures exercise real HTTP/auth code and oversized-response aborts;
Go tests own source verification, Unicode pagination and cursor validity.
`npm run test:bundle` runs bundle checks alone. Tests do not copy the public
artifact; CI's byte comparison verifies the committed copy.

For connection failures, first verify the executable path, target configuration,
OAuth callback/scopes and current user login. Do not solve authorization failures
by granting anonymous API access. See [API reference](../docs/API-SPEC.md) and
[ADR-003](../docs/decisions/ADR-003-mcp-server-for-external-meeting-access.md).
