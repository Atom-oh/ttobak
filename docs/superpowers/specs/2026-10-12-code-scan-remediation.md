# Code scan remediation

Historical planning record from 2026-10-12. It records the triage of a
whole-repository scan of `origin/main` (`8c187a3`) and the slice chosen for repair.
It does not describe deployment state; verify current code and the linked PRs.

Each finding below was reproduced by reading the cited code on that revision.
Scan items that the 2026-10-11 live-caption rewrite (#314) had already removed are
listed separately and are not changed.

## Fix in this slice

### Application (backend and frontend)

| ID | Finding | Evidence | Change | Test |
|---|---|---|---|---|
| A1 | KB delete can remove a different file. `DeleteFile` deletes the first key whose name *ends with* `fileId`, from one unpaginated listing. `ListFiles` exposes `filepath.Base(key)`, and meeting-attachment copies use `kb/{user}/{fileName}` without a timestamp, so deleting `report.pdf` can hit `2026-report.pdf`; past 1,000 objects the target is never found. | `service/kb.go` `DeleteFile`, `CopyMeetingFileToKB` key format, `ListFiles` file ID | Exact base-name match over all pages. Reject IDs with a path separator or `..`. Two or more matches return a conflict instead of guessing. Use sentinel errors; the handler maps them and stops echoing raw errors. | Table-driven service tests with a fake S3 lister/deleter: suffix collision, multi-page, ambiguous, traversal, not found; handler status mapping. |
| A2 | Live summary ignores its `meetingId`, so any signed-in user can bill model calls for arbitrary text, with an unbounded body. Production logs also showed every live-summary request uses the browser-only `meeting_<epoch ms>` ID: the record page's transcript callback is created once and reads the stale `serverMeetingId` (`null`). | `handler/summarize_live.go` (`_ = chi.URLParam`); `app/record/page.tsx` `onTranscriptUpdate`; `ttobak-api` access logs (`/api/meetings/meeting_179…/summarize`) | Frontend reads the server draft meeting ID through a ref and skips live summary until it exists. Backend requires owner or `edit` access (same rule as re-summary) before invoking the model: 404 without access, 403 read-only. Body capped at 1 MiB because the recorder sends the whole running transcript. Deploy order between the API and the frontend is not guaranteed, so a legacy `meeting_<digits>` ID keeps the previous behavior until a follow-up removes it after the frontend deploy. | Handler table test (owner, none, read-only, lookup failure, legacy ID, oversized, empty) and an `AuthorizeEdit` service test; lint and build. |
| A3 | Research mode errors are matched by message substring. | `handler/research.go` `strings.Contains(err.Error(), "invalid mode")` | Sentinel `ErrInvalidResearchMode` wrapped with `%w`, matched by `errors.Is`. | Handler test for 400 with a wrapped error. |
| A4 | A panic in a parallel refinement goroutine crashes the summarize Lambda; chi recovery does not cover goroutines. | `service/bedrock.go` `refineParallel` | Recover inside the goroutine and use the existing raw-segment fallback for that chunk. | Unit test of the recover helper. |
| A5 | Meeting detail polling keeps the timeout flag and counter across meetings when the route component is reused. | `MeetingDetailClient.tsx` `pollCountRef` / `pollTimedOut` | Reset both when the polled meeting ID changes. | Lint and production build (no frontend unit framework). |

### Edge and infrastructure

| ID | Finding | Evidence | Change | Test |
|---|---|---|---|---|
| B1 | An unauthenticated `/api/mcp` request gets the edge function's 401 without `WWW-Authenticate`, so MCP clients do not learn the discovery URL. Verified live on 2026-10-11 (`x-cache: LambdaGeneratedResponse`, no challenge header). | `edge-auth-stack.ts` inline handler | For `/api/mcp` only, add `WWW-Authenticate: Bearer resource_metadata="https://{viewer host}/.well-known/oauth-protected-resource/api/mcp"` to edge 401s. Other paths are unchanged. | Execute the inline handler in Jest with synthetic CloudFront events. |
| B2 | A missing `ttobak:originVerifySecret` silently disables origin verification for the Go API and QA. | `bin/infra.ts`, `gateway-stack.ts`, QA handler | Full-app synthesis fails when the context is missing. Runtime semantics are unchanged. | `infra.test.ts` assertion on the guard. |
| B3 | A missing `ttobak:cloudfrontDomain` produces the CORS origin `https://undefined`. | `gateway-stack.ts` `corsPreflight` | GatewayStack throws when the context is missing. | Gateway test. |
| B4 | The summarize role still allows the outgoing GPT-6 Sol model after the 2026-10-09 cutover (#311 follow-up). | `ai-stack.ts` `InvokeFinalSummaryModel` | Keep only the GPT-6.1 Sol model and profile. The deployed environment already uses GPT-6.1 Sol. | `ai-stack.test.ts` exact two-resource assertion. |

## Not changed, with reasons

- **Chat answer deadline timer on unmount (scan claim):** false positive. The unmount effect calls `finishRequest`, which clears the timer, and `failRequest` is also guarded by `aliveRef`.
- **Already fixed by #314:**
  - Live translation failures are now visible (`status: 'error'`).
  - Late responses after stop are discarded (`isCurrent`).
  - Line-split misalignment is gone.
  - The Web Speech path no longer translates with a fixed `ko` source.
- **MCP schema versus handler** (`ttobak_add_account_member` role enum, `accountIds` bounds): the Go API enforces `model.AssignableRoles` and the 100-ID limit, so this is a message-quality issue only.
- **Unbounded `io.ReadAll` of internal S3 transcripts, AgentCore output and JWKS:** these sources are produced or trusted by the system. A limit needs an explicit size contract per producer first.
- **Research container default bucket and table names:** the container is deployed outside CDK. Changing defaults without workflow-injected values could break production, so this needs its own container and workflow change.
- **Unconditioned `Resource: "*"` IAM statements:** tracked legacy debt in `ai-stack.ts`.
- **Origin secret as a Lambda environment value:** existing documented discrepancy. It should move to Secrets Manager like the WebSocket origin proof.
- **Browser token storage in `localStorage`:** a deliberate existing design. No unsanitized HTML sink was found.
- **DynamoDB whole-item `PutItem` sites:** they need a dedicated audit of which fields other paths update.

## Delivery

- **Application PR:** A1–A5 plus this record.
- **Follow-up:** remove A2's legacy `meeting_<digits>` allowance after the frontend deploy is confirmed.
- **Infrastructure PR:** B1–B4.
- **Deploy:** both reach production through `deploy-infra` and the frontend workflow after review.
- **Ordering:**
  - A2 adds no route and changes no client contract.
  - B1 changes only the response for `/api/mcp`.
  - B4 must stay after GatewayStack's environment change, which already happened on 2026-10-09.
