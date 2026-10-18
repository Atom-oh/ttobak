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
| TTOBAK_USER_POOL_ID | Optional for stdio; explicit IDs still require a match with the site's public `/config.json` |
| TTOBAK_PROFILE | Optional local credential profile; defaults to `default` |

The OAuth client must allow authorization-code PKCE, openid/email/profile scopes
and `http://localhost:9876/callback`. Current AuthStack configures the SPA client
for this use; inspect it instead of inventing another mandatory client. The
setup helper reads CloudFormation and writes host configuration; inspect its
selected values before running it. Normal MCP calls need Cognito login, not AWS
credentials. Keep stdout reserved for MCP and diagnostics on stderr.

First authentication starts a callback listener on IPv4 loopback (`127.0.0.1`),
then opens the browser. The registered redirect remains
`http://localhost:9876/callback`. Every callback must match the login state before
its result is handled; unrelated callbacks do not terminate the pending login.
Concurrent authentication calls share one login or refresh operation.

Stdio verifies the configured issuer and Hosted UI against Cognito discovery,
then verifies both returned/cached tokens' signatures, issuer, client, token use,
subject and expiration before sending an ID token to the API. Existing
three-variable host configurations continue to work when the site's public
`/config.json` identifies the matching client and pool. Explicit pool IDs support
other registered clients without inferring an issuer from an unverified token.
Token use and status require reachable application configuration and Cognito
discovery/JWKS; an explicit pool ID does not bypass the current API pool check.

Credentials are stored atomically under
`~/.ttobak/tokens/<deployment-hash>.json`; the hash binds issuer, Hosted UI,
client, API origin and profile. On POSIX systems, directories use mode 0700 and
files 0600; the parent `~/.ttobak` directory must be owned by the current user and
must not be a symlink. Windows does not provide these same POSIX permission guarantees.
The former `~/.ttobak/tokens.json` is ignored and never migrated automatically:
upgrading requires a new login. After confirming the new connection, manually
remove the old file; logout does not remove that unused global cache.
`ttobak_status` sends the verified subject, username and verified email to the
MCP client as tool text. Logout cancels pending authentication and removes saved
credentials locally without discovery. An explicit pool selects its exact cache;
discovered-pool configurations remove issuer variants matching the configured
API, Hosted UI, client and profile. Other configurations are preserved. A bounded
scan or filesystem failure returns an error instead of claiming completed removal.
Refresh is attempted before another login; its lifetime depends on pool policy
and revocation. Do not copy token files into documentation or diagnostics.

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
`ttobak_login`/`ttobak_logout` tools and never reads local credential caches.

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
The Gateway context `ttobak:mcpResourceAudience` (set in `infra/cdk.json`) admits
this exact HTTPS `/api/mcp` audience alongside the existing client audience and
enables the hosted Lambda described below. Without it, API Gateway checks the URL
`aud` instead of falling back to `client_id` and rejects the token. The edge
client-ID gate and OriginVerify remain required.

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

Hosted deployment: `src/lambda.ts` runs this same server on a loopback port inside
the `ttobak-mcp` Lambda (`npm run bundle:lambda` builds `dist/lambda-bundle/`).
GatewayStack serves `ANY /api/mcp` with the Cognito JWT authorizer and the two
discovery GETs without one; CloudFront routes only those exact discovery paths
around the edge function. The wrapper rejects a missing or wrong `x-origin-verify`,
pins Host to the public host and forwards only MCP-relevant headers. API Gateway
limits integrations to 30s, so the hosted deadline is 25s. A missing or invalid
token is rejected at the CloudFront edge with a 401 whose `WWW-Authenticate` names
`/.well-known/oauth-protected-resource/api/mcp`. Cognito issues the required `aud` only through managed login
with the MCP URL registered as a resource server (AuthStack does both). The pool's
OIDC metadata omits `code_challenge_methods_supported`; PKCE S256 still works, but
a client that insists on that field may refuse. Clients use the SPA public client
and an exactly registered callback. The SPA client retains
`http://localhost:9876/callback` and adds the configured Codex host's callback,
`http://localhost:9876/callback/Z3RALQJi3TX1`. Codex appends this server-specific
path because Cognito discovery does not advertise authorization-response `iss`.
Use callback port `9876`, callback base `http://localhost:9876/callback`, and
explicit login scopes `openid,email,profile`; the issuer also advertises `phone`,
which this client does not grant. Compare the final redirect URI with the
deployed client's callback list before signing in. On a remote Codex host,
forward the browser's localhost port to that host so the authorization code
returns directly to the login process; never paste codes or tokens into chat.
Publish `mcp: { "url": "/api/mcp" }` in runtime config only
after deployment acceptance. Validate each client's exact callback, OIDC discovery,
resource binding, refresh and representative operations. Access tokens need not
carry email/verified-email claims: do not fabricate them or weaken pending-invite
checks. Source wiring and synthetic tests do not prove live client acceptance.
See [ADR-045](../docs/decisions/ADR-045-dual-mcp-transports.md).

### HTTP MCP from code-server

Register the same Streamable HTTP endpoint in the Codex configuration used by
the remote host. Codex CLI and the Codex IDE extension share that configuration.
Use your existing authenticated code-server HTTPS origin and the callback ID
displayed for this exact MCP URL:

```toml
[mcp_servers.ttobak]
url = "https://ttobak.atomai.click/api/mcp"

[mcp_servers.ttobak.oauth]
client_id = "33rh85mv6l9n7tn3s5h16prfdr"
callback_port = 9876
callback_url = "https://YOUR_CODE_SERVER_HOST/absproxy/9876/callback/YOUR_CALLBACK_ID"
```

Codex discovers the resource from the protected-resource metadata. Do not add an
explicit `oauth_resource` for this deployment: Codex CLI 0.162.1 then sends the
same resource twice, and Cognito rejects the request before showing login with
`invalid_request` / `multi-resource binding is not supported`. Verify that the
authorization request has one `resource` equal to the MCP URL.

The public client ID is deployment configuration, not a secret. Keep the
code-server browser session signed in, then run this in its terminal:

```sh
codex mcp login ttobak --scopes openid,email,profile
```

Open the displayed authorization link in that browser and sign in to TTOBAK.
The callback uses the existing authenticated code-server HTTPS proxy to reach
the remote listener on port `9876`. Use `/absproxy/`, which preserves the entire
callback path; `/proxy/` removes its prefix. This path does not require an SSH
tunnel or access to the browser machine's localhost. Codex binds a listener for
an external callback URL to `0.0.0.0`; keep that development port behind the
existing network controls and code-server authentication. Do not add public
ingress or disable proxy authentication.

Before signing in, verify that this exact redirect URI is deployed in the
Cognito client's callback list. AuthStack adds private deployment callbacks from
the SSM StringList `/ttobak/auth/mcp-callback-urls` to the two retained local
callbacks. An operator must create this parameter in the deployment account and
region before deploying AuthStack; a missing parameter blocks deployment.
Populate it with the exact approved client redirect URIs, without empty entries
or duplicate local callbacks. Keep workstation hostnames in that private
configuration, outside repository source, CDK context and published history.
Callback URLs are configuration; never put authorization codes or tokens in the
parameter. Verify proxy reachability before configuring a host callback.

Another code-server hostname or MCP URL needs its own exact callback
registration; do not assume a wildcard or the same server-specific suffix.
No new TTOBAK public route or application origin is introduced.

Claude Code and Kiro use separate client configuration and authentication.
Codex TOML does not register this server in either client. Run only one login
at a time when the clients share callback port `9876`.

For Claude Code, register the public client in its user configuration:

```sh
claude mcp add --transport http --scope user \
  --client-id 33rh85mv6l9n7tn3s5h16prfdr --callback-port 9876 \
  ttobak https://ttobak.atomai.click/api/mcp
claude mcp login ttobak --no-browser
```

Open the authorization link in your browser. When the browser redirects to
`http://localhost:9876/callback`, paste the complete redirect URL into Claude
Code's own interactive terminal prompt. That URL contains a short-lived code;
never put it in chat, logs or a shared document. This manual completion works
without the browser reaching the remote listener. Claude Code's documented
callback-port option does not configure an HTTPS callback.

For Kiro CLI, merge the following entry into the existing `mcpServers` object
in `~/.kiro/settings/mcp.json`, preserving other servers and file permissions:

```json
{
  "mcpServers": {
    "ttobak": {
      "url": "https://ttobak.atomai.click/api/mcp",
      "oauth": {
        "clientId": "33rh85mv6l9n7tn3s5h16prfdr",
        "redirectUri": "http://localhost:9876/callback",
        "oauthScopes": ["openid", "email", "profile"]
      }
    }
  }
}
```

Kiro CLI 2.29.1 accepts only an HTTP loopback `redirectUri`. Its HTTPS MCP
transport does not change this OAuth callback requirement, and an HTTPS
code-server proxy URL cannot replace that URI. Before starting `/mcp auth`,
forward the browser machine's localhost port `9876` to the remote host's
port `9876` through your existing SSH or IDE connection. The destination must
reach the host or container in which Kiro's listener actually runs. The fixed
port setting alone does not create a forward. Confirm the browser's localhost
resolves to the address covered by your forward. No Kiro manual callback paste
flow has been established for this version.

These instructions describe the installed terminal clients, including terminals
inside code-server. Kiro IDE and other editor extensions are separate clients.
Verify each client's final redirect against the deployed allowlist and confirm
its granted scopes, resource binding, token refresh and representative API
operations independently. Registration and a synthetic proxy check do not prove
authenticated client acceptance.

VS Code's native MCP registry is separate from Codex. Its workspace declaration
uses `servers`, rather than Codex TOML:

```json
{
  "servers": {
    "ttobak": {
      "type": "http",
      "url": "https://ttobak.atomai.click/api/mcp"
    }
  }
}
```

Save that declaration as `.vscode/mcp.json` only when using a native MCP client.
The installed code-server 4.106.3 / VS Code 1.106.3 schema has no per-server
OAuth client-ID or callback setting. This declaration registers the transport;
native-client OAuth acceptance still needs a supported client-registration flow
and its own exactly registered callback. Do not copy Codex OAuth fields into
this JSON or claim native sign-in passed.

Code-server proxy reachability, deployed callbacks, authenticated HTTP
initialization, refresh and representative bounded reads require separate live
acceptance. A configuration entry or successful infrastructure deployment alone
does not establish user authentication or R9 consumer acceptance.

## Tool behavior

`src/index.ts` is the shared tool/schema inventory; `src/api.ts` implements HTTP
contracts and errors. The adapter covers login/status, meeting list/detail/QA,
accounts and briefs/insights, personal/account documents, KB files/ingestion, vault
export, projects and links.

- Omit accountId for personal Document Hub operations; provide it explicitly for
  account space. Directly shared documents can be read, but only owners revise them.
- list_documents uses the bounded `/api/document-catalog` endpoint. Keep the same
  accountId/docType while following cursors, including empty filtered pages.
  It returns current metadata previews without loading bodies; the list is live,
  and changed continuation anchors or grants require a fresh read. A returned
  `metadataRevision` is not a body revision for get_document's `expectedRevision`.
  Deploy this authenticated route before publishing the adapter.
- put_document creates a new ID. update_document preserves the ID and omitted body;
  include required title/metadata fields according to its schema.
- get_document uses authenticated document `/reading` pages for both transports.
  Follow `page.nextCursor` with the same docId, accountId and section; restart
  after `STALE_CURSOR`. Default pages have a 10,000-byte encoded API budget,
  original HTTP bodies and final tool results each retain their 32,000-byte cap.
  Deploy the API route before publishing the adapter: it never falls back to a
  full-document read or invents continuation. File documents preserve authorized
  five-minute download/preview URLs and explicitly report unavailable extracted
  text. If unusually long links require `previewStatus=deferred`, follow the
  returned `previewRead` to retrieve the intact preview URL. Public share tokens
  are never returned.
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

## Local meeting recording (stdio only)

`ttobak_list_audio_devices`, `ttobak_start_recording`, `ttobak_recording_status`,
`ttobak_stop_recording` and `ttobak_upload_audio` exist only on stdio; the HTTP
transport never lists them (`LOCAL_ONLY_TOOLS`, ADR-045).

- Requires macOS and ffmpeg (`brew install ffmpeg`, or set `TTOBAK_FFMPEG`).
  `src/recorder.ts` records the microphone only (avfoundation, 16 kHz mono WAV,
  about 1.9 MB/min), not other apps' audio. It auto-stops after `maxMinutes`
  (default and maximum 240, the Transcribe fallback's limit). One recording at a
  time per server process; overlapping starts are refused.
- ffmpeg inherits the MCP host app's macOS microphone permission (Claude Desktop,
  Terminal/iTerm for Codex or Kiro CLI, an IDE...). Grant it under System Settings >
  Privacy & Security > Microphone. Without it the file is silent: stop runs a
  volume check and keeps a silent file locally unless `allowSilent` is true.
- Start contacts no API. Stop finalizes the WAV, then creates the meeting
  (`POST /api/meetings`, date = recording start), presigns an `audio` upload,
  streams the file and calls `/api/upload/complete`; the S3 PUT itself starts
  transcription. The local file is deleted only after complete succeeds.
- Recordings live in `~/Library/Application Support/ttobak/recordings`
  (`TTOBAK_RECORDINGS_DIR` overrides) with a sidecar JSON, so kept files survive a
  host restart. Several hosts can share the directory: a capture is offered for
  upload only once finalized (or once its server and ffmpeg have both exited), and
  every upload holds an exclusive `<id>.lock`. Server shutdown stops ffmpeg
  gracefully after in-flight uploads settle (at most 20 s; a second signal exits
  at once, and an interrupted upload resumes from its saved progress). A hard
  kill may leave ffmpeg running until `maxMinutes`. Kept
  recordings are not aged out; delete them manually if they are not needed.
- The sidecar records upload progress before each irreversible step, so
  `ttobak_upload_audio` with a `recordingId` resumes: it reuses the meeting it
  created, only completes an object already stored, and when an earlier PUT's
  outcome is unknown it requires `previousUpload` (`complete` or `reupload`)
  instead of guessing. A complete-only retry keeps the local recording and warns:
  transcription may have run while the meeting was still `recording`, so notes
  may never appear; `previousUpload: "reupload"` then uploads it again. Callers cannot pass a `meetingId`: upload-complete would
  replace that meeting's audio.
- `ttobak_upload_audio` also accepts an absolute `filePath` (m4a, mp4, mp3, wav,
  webm, ogg, flac: containers both Whisper and the Transcribe fallback accept; at
  most 2 GiB) under the same path guard as document
  uploads. It validates the file before creating a meeting, uploads under a fixed
  `mcp_upload_*` object name, never deletes caller files, and names the failed
  phase in its error. An unconfirmed PUT or upload-complete is reported as an
  unknown outcome: check the meeting, and only if it shows no audio delete it
  and upload again. The TTOBAK API completes the same key idempotently, so a
  recording's complete-only retry never resets a meeting's progress.

## Mac app recording control (stdio only)

`ttobak_app_status`, `ttobak_app_start_recording` and `ttobak_app_stop_recording`
drive the TTOBAK Mac app, which records system audio (Zoom, Teams, Meet) mixed with
the microphone, even with headphones (ADR-046). Prefer them over the ffmpeg
microphone tools when the app is running. The adapter sends one JSON line to the
app's socket, `~/Library/Application Support/ttobak/control.sock`
(`TTOBAK_APP_SOCKET` overrides). The app accepts only the same macOS user and
forwards start/stop to its signed-in page, which creates the meeting and uploads
on stop; the adapter never sees audio or tokens.

- Start returns the meetingId once capture runs; the app window comes forward and
  a notification names the recording. Stop uploads by default (`upload: false`
  leaves the app's notes step open) and returns the meetingId; follow progress
  with `ttobak_app_status`.
- `app_not_running` means the app is closed or an older build; `app_not_ready` /
  `login_required` mean its window has not loaded or is signed out; `busy` means
  a recording, stop or upload is already in progress; `recording_unowned` means
  capture is running but its recording screen was closed (stop it in the app);
  `browser_recording` means a browser-mode recording in the app window is
  active, which only its own stop button ends; `notes_too_long` means the stop
  succeeded but the app kept the recording at its notes step;
  `start_interrupted` / `stop_interrupted` mean the recording screen closed
  mid-request. A
  `timeout` leaves the app's state unknown: check status before retrying.
- Listed in `LOCAL_ONLY_TOOLS`; the HTTP transport never exposes them.

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

KB file discovery uses bounded `/api/kb/files/reading` pages. Preserve opaque
continuations until complete and restart stale anchors. File IDs stay exact;
shortened names are marked. Deploy the API producer before this adapter.

Account/project discovery uses bounded navigation pages and canonical grants.
Follow empty continuation pages; merge project entries by `projectId` when
`overlapPossible` is true. Linked accounts do not authorize meeting content.
Deploy both navigation API producers before this adapter.
