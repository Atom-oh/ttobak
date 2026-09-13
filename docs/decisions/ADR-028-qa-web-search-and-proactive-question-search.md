# ADR-028: QA web search and opt-in proactive questions

- Status: Accepted; PR #143.
- Decision date: 2026-07-31.
- Code checked: 2026-09-13; Gateway availability and deployed quotas were not checked.

## Original decision and rationale

Add current-information search to live QA and optionally answer detected factual questions automatically. Reuse the us-east-1 AgentCore Web Search Gateway. Keep separate SigV4/MCP implementations in crawler, research-agent, and QA because they ship as independent artifacts; transport changes must stay synchronized.

## Current behavior and egress boundary

- QA signs HTTPS Gateway requests with service `bedrock-agentcore`, using the configured Gateway region (default `us-east-1`). Redirects are refused, responses and wait time are bounded, and result URLs are HTTPS-only. IAM grants `InvokeGateway` to the configured Gateway ARN.
- `search_web` distinguishes transport/configuration failures from genuine zero results. Missing Gateway configuration returns a tool error rather than removing the tool, consuming a tool round.
- **Proactive consent controls automatic questions only.** The toggle defaults off, is keyed by Cognito sub, and is shared across mounted panels. A manually submitted question may invoke web search even with the toggle off; model-generated queries leave the account for an external search provider.
- Prompts prohibit customer/participant names, internal code names, and meeting-specific amounts in queries and treat transcript/tool text as untrusted. These are soft instructions, not a deterministic egress filter: prompt injection or model error can still put sensitive text in an outbound query.
- Known free-text tool-input fields use a per-process HMAC reference plus length in logs. Web-search logs use the transmitted query's reference. Preserve the no-plaintext-query policy; the fixed redaction-key list must be extended for new free-text fields, and exception logging needs separate care.
- Detection supports legacy string responses and a `proactive` subset. Shared claims allow one automatic question per detection generation, at most two attempts per question per recording, with pauses while typing, hidden, or answering. Failed claims can retry in a later generation; consumed generations are not reopened. Success records the asked question, and generation checks reject stale responses.

## Quota semantics

`check_web_search_limit` atomically increments `USER#{id}/WEBSEARCH_HOURLY#{hour}` **before** calling the Gateway. Default `WEB_SEARCH_HOURLY_LIMIT` is 30; nonpositive values disable it and malformed configuration uses the default. Counter rows use `pendingShareExpiresAt`, the table's configured TTL attribute. QA history/cache rows using uppercase `TTL` do not receive physical cleanup from that sweep.

Exceeding the limit denies the call and attempts a compensating decrement. A failed decrement never reverses denial. An increment failure **fails open** for availability; missing authenticated user/checker context in `execute_tool` instead fails closed. Fixed-hour boundaries can allow nearly twice the hourly limit in a short interval. This is a spending/abuse brake, not a security boundary or a global Gateway quota; crawler/research usage is outside it.

## Tradeoffs and accepted residual risks

Automatic search adds model/provider cost; opt-in and attempt limits bound UI behavior but do not authorize unrestricted data egress. Manual search retains the documented prompt-injection/exfiltration risk. Per-user preference storage prevents another login inheriting consent; explicit sign-out and 401 teardown also clear preference/claim state.

## Evidence

- [Gateway transport/redaction](../../backend/python/qa/web_search.py), [tool dispatch](../../backend/python/qa/tools.py), [quota/detection](../../backend/python/qa/handler.py), [QA tests](../../backend/python/qa/test_handler.py).
- [Shared consent/claims](../../frontend/src/lib/proactiveSearch.ts), [panel guards](../../frontend/src/components/LiveQAPanel.tsx), [detection lifecycle](../../frontend/src/hooks/useLiveSummary.ts).
- [Gateway IAM](../../infra/lib/ai-stack.ts), [IAM tests](../../infra/test/ai-stack.test.ts), [Gateway configuration](../../infra/lib/gateway-stack.ts).
