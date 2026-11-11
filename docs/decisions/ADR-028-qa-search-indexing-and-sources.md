# ADR-028: QA: web search, note indexing and current-source answers

- Status: Accepted. Merges earlier records; each section below keeps one record's
  rules, and dates inside mark owner decisions.
- Implementation checked: 2026-11-11.

QA can search the web, reads notes through a canonical index, and answers only from sources the caller can read now. The sections below keep the web search record, the canonical note indexing record and the current-source QA record.

## QA web search and opt-in proactive questions

### Status notes

- Status: Accepted.
- Decision date: 2026-07-31.
- Code checked: 2026-09-13; Gateway availability and deployed quotas were not checked.

### Decision and rationale

Add current-information search to live QA and optionally answer detected factual questions automatically. Reuse the us-east-1 AgentCore Web Search Gateway. Keep separate SigV4/MCP implementations in crawler, research-agent, and QA because they ship as independent artifacts; transport changes must stay synchronized.

### Current behavior and egress boundary

- QA signs HTTPS Gateway requests with service `bedrock-agentcore`, using the configured Gateway region (default `us-east-1`). Redirects are refused, responses and wait time are bounded, and result URLs are HTTPS-only. IAM grants `InvokeGateway` to the configured Gateway ARN.
- `search_web` distinguishes transport/configuration failures from genuine zero results. Missing Gateway configuration returns a tool error rather than removing the tool, consuming a tool round.
- **Proactive consent controls automatic questions only.** The toggle defaults off, is keyed by Cognito sub, and is shared across mounted panels. A manually submitted question may invoke web search even with the toggle off; model-generated queries leave the account for an external search provider.
- Prompts prohibit customer/participant names, internal code names, and meeting-specific amounts in queries and treat transcript/tool text as untrusted. These are soft instructions, not a deterministic egress filter: prompt injection or model error can still put sensitive text in an outbound query.
- Known free-text tool-input fields use a per-process HMAC reference plus length in logs. Web-search logs use the transmitted query's reference. Preserve the no-plaintext-query policy; the fixed redaction-key list must be extended for new free-text fields, and exception logging needs separate care.
- Detection supports legacy string responses and a `proactive` subset. Shared claims allow one automatic question per detection generation, at most two attempts per question per recording, with pauses while typing, hidden, or answering. Failed claims can retry in a later generation; consumed generations are not reopened. Success records the asked question, and generation checks reject stale responses.

### Quota semantics

`check_web_search_limit` atomically increments `USER#{id}/WEBSEARCH_HOURLY#{hour}` **before** calling the Gateway. Default `WEB_SEARCH_HOURLY_LIMIT` is 30; nonpositive values disable it and malformed configuration uses the default. Counter rows use `pendingShareExpiresAt`, the table's configured TTL attribute. QA history/cache rows using uppercase `TTL` do not receive physical cleanup from that sweep.

Exceeding the limit denies the call and attempts a compensating decrement. A failed decrement never reverses denial. An increment failure **fails open** for availability; missing authenticated user/checker context in `execute_tool` instead fails closed. Fixed-hour boundaries can allow nearly twice the hourly limit in a short interval. This is a spending/abuse brake, not a security boundary or a global Gateway quota; crawler/research usage is outside it.

### Tradeoffs and accepted residual risks

Automatic search adds model/provider cost; opt-in and attempt limits bound UI behavior but do not authorize unrestricted data egress. Manual search retains the documented prompt-injection/exfiltration risk. Per-user preference storage prevents another login inheriting consent; explicit sign-out and 401 teardown also clear preference/claim state.

### Evidence

- [Gateway transport/redaction](../../backend/python/qa/web_search.py), [tool dispatch](../../backend/python/qa/tools.py), [quota/detection](../../backend/python/qa/handler.py), [QA tests](../../backend/python/qa/test_handler.py).
- [Shared consent/claims](../../frontend/src/lib/proactiveSearch.ts), [panel guards](../../frontend/src/components/LiveQAPanel.tsx), [detection lifecycle](../../frontend/src/hooks/useLiveSummary.ts).
- [Gateway IAM](../../infra/lib/ai-stack.ts), [IAM tests](../../infra/test/ai-stack.test.ts), [Gateway configuration](../../infra/lib/gateway-stack.ts).

## Canonical note indexing

### Status notes

- Status: Accepted, including the private/shared bootstrap extension, 2026-09-12.
- Activation configuration (2026-09-13): `infra/bin/infra.ts` selects `all`
  with its one-minute schedule retained. Merge/deployment remains gated on
  completed manual snapshot acceptance and deployed, verified current-source QA.
  This is a configuration change, not evidence that activation has deployed.
  The bootstrap runbook records the dated producer and public-consumer
  qualification, including direct async REST acceptance with UI activation off.
  Canonical backfill and lifecycle acceptance follow the reviewed activation.

### Context and decision

Manual meeting exports miss edits and deletion. Direct ingestion would create
a second writer beside the existing S3 sync. Use the same S3 data source,
immutable projections and a coordinator that coalesces full syncs.

Canonical sources are meetings and personal/account documents. Durable jobs
bind saved fields and source object ETags/versions to a revision and lease.
Completion requires successful ingestion, exact document status, immutable
projection inventory and fresh source checks. Paginated reconciliation
recovers missed events and deletion; changing or failed sources back off
without holding other jobs.

Private `kb/{owner}/{filename}` and authenticated-shared `shared/**` originals
also receive immutable `manual-kb/v1/` and `shared-kb/v1/` snapshots, without
inventing canonical document rows. Preserve owner isolation and authenticated
sharing. In `all` mode the indexer role can read and
scan the whole table (`ReadCanonicalIndexRecords` in `infra/lib/ai-stack.ts`);
only its index-state writes are limited by leading key, so owner isolation for
index records is enforced in code, not by IAM. This scope is a known residual
until it is narrowed. Metadata alone cannot prove that old binary chunks match current bytes.

### Activation order

1. Prepare the mode-aware worker with delivery off, then explicitly enable
   `manual-only` snapshot production with its restricted permissions.
   The reconciliation schedule stays enabled ahead of all-mode activation.
2. Verify immutable private/shared snapshots and current-byte recall while
   retaining originals and legacy meeting exports.
3. Deploy and verify the complete strict QA consumer, including source
   permissions, snapshot checks and conversation-history revalidation.
4. Enable `all` with canonical permissions, stream delivery and reconciliation.
   Existing manual batches finish under their original ingestion token.

Mode is durable: `all → manual-only` and a manual-only start over a canonical
batch are rejected. Restore `all` after an accidental downgrade; never edit
coordinator state or invoke ad-hoc global ticks to bypass the guard.

### Consequences

Search is eventually consistent and adds request/cleanup cost. An indexed
status never replaces current authorization or source checks. Old QA recognizes
legacy `meetings/{owner}/` exports; retiring them before strict QA loses recall.
After retirement, old-QA rollback requires explicit re-export. Stopping the
worker does not restore deleted exports or justify serving stale chunks.

### Evidence

- [Worker](../../backend/cmd/kb/main.go),
  [coordinator](../../backend/internal/service/indexing.go),
  [mode guard](../../backend/internal/service/index_rollout.go).
- [Source/provider contract](../../backend/internal/service/INDEX_SOURCE_CONTRACT.md),
  [migration contract](../../backend/cmd/kb/KNOWLEDGE_MIGRATION.md).
- [Bootstrap runbook](../runbooks/knowledge-index-bootstrap.md),
  [QA rollout](../runbooks/qa-current-source-rollout.md).
- [Deployed manual producer acceptance](../research/evaluations/2026-09-13-manual-kb-bootstrap/README.md):
  synthetic private/shared replacement, retrieval and deletion, including cleanup.
  This establishes the producer prerequisite only; it does not prove QA or
  canonical activation.

## Current-source QA and conversation history

### Status notes

- Status: Accepted.
- Handler registration and deployment acceptance status:
  [SOURCE_CONTRACT.md](../../backend/python/qa/SOURCE_CONTRACT.md).

### Context and decision

Index chunks and conversation history can outlive edits, deletion or revoked
access. A TTL cannot prove current access. Dropping history after every list
tool breaks normal follow-up questions; replacing a matching excerpt with a
file's introduction loses facts later in the document.

Use the index for discovery, authorize canonical sources again and hydrate
current saved text. Binary excerpts require matching immutable snapshots.
Validate legacy text excerpts against current bytes and expose bounded,
revision-bound continuation.

Keep source dependencies beside model messages and recheck them before
replay, subsequent model rounds and final output. Changed, missing,
inaccessible or unverifiable sources invalidate the entire dependent history,
including assistant paraphrases. Removing tool blocks alone is insufficient.
Unverifiable legacy histories cannot be replayed.

Read-only list/account tools retain continuity through bounded fingerprints
of complete, freshly authorized results. Replay invokes only allowlisted
read callbacks with the current authenticated user. Failed reads are not empty
successes. Tracking overflow preserves the current result but explicitly marks
history nonreplayable.

Never repeat `start_research` or another mutation to validate history. A
successful creation receipt records a past event, not current research access
or completion; current facts require a separate authorized read.

Expose additive `sourceDetails` alongside legacy `sources` strings, using an
explicit public-field allowlist rather than arbitrary index metadata. Preserve
partial/error states and document locations; file evidence has no audio
timestamp.

### Rollout and consequences

Deploy source-read permissions and the private/shared snapshot producer,
verify snapshot recall, then enable the complete strict consumer. Canonical
backfill and retirement of legacy meeting exports follow. Pending-only binary
retrieval is not an acceptable replacement for existing file answerability.
Use the manual-only bootstrap order in the activation order above.

Current reads add cost, and edits or old unverifiable sessions can reset
continuity. Stable authorized tool results retain follow-up references without
allowing stale private text to survive through cached chunks or paraphrases.

### Evidence

- [Source contract](../../backend/python/qa/SOURCE_CONTRACT.md),
  [history contract](../../backend/python/qa/TOOL_HISTORY_CONTRACT.md),
  [strict account readers](../../backend/python/qa/ACCOUNT_READS_CONTRACT.md).
- [Active handler](../../backend/python/qa/handler.py),
  [rollout and acceptance](../runbooks/qa-current-source-rollout.md).
