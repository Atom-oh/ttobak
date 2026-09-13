# SP1: News Discovery Through AgentCore Web Search

> Historical design record. Original date: 2026-07-09. The original record marked
> this shipped on 2026-07-13 in PR #111. That historical milestone does not verify
> today's deployment, provider terms, or data-residency guarantees.

## Goal and confirmed scope

Replace customer-news Google/site RSS discovery with the AgentCore Gateway Web
Search connector, and use the same search route in the research container. Keep
source configuration, URL deduplication, summarization/tagging, stored news format,
and the existing ingestion workflow. Explicit custom URLs remained a separate
fetch path. RSS fallback on Gateway failure was deliberately excluded.

The planned search-result path retained snippets, summaries, source URLs, and
publication dates instead of fetching full articles. This reduced scraping and
site-specific discovery maintenance; it also limited evidence depth. Search-result
caching or a competing search index was outside scope.

## Architecture and deployment boundaries

The design placed an IAM-authorized Gateway/connector in us-east-1, with signed MCP
calls from Seoul crawler Lambdas and the research runtime. Cross-region stack
references needed appropriate CDK wiring. Search call permission belonged to each
actual caller: the crawler role and the runtime container's execution role. Giving
it only to the Lambda that invokes the container would not authorize its tools.

Runtime environment injection was separate from crawler Lambda configuration.
Keeping independent SigV4/MCP helpers in separate deployment artifacts was an
intentional packaging tradeoff. The final signing service, connector representation,
and runtime-role management were originally open implementation questions.

## Error, evidence, and privacy constraints

A failed query must not silently become proof that no news exists. Other sources
can continue, with failure visible for investigation/retry. The old log-only,
empty-list error sketch has been superseded by a result/error distinction.

The original author described the connector as AWS-private and concluded that
snippet storage satisfied acceptable-use terms. Those are not established by a
signed AWS endpoint or repository code. Preserve attribution and distinguish
public discovery from confidential meeting context; do not infer provider data
residency, query privacy, or legal compliance from this design. It does not
authorize logging private text or sending internal meeting data to search.

## Current evidence and successors

[WebSearchGatewayStack](../../../infra/lib/web-search-gateway-stack.ts),
[infra.ts](../../../infra/bin/infra.ts), and [AiStack](../../../infra/lib/ai-stack.ts)
define the Gateway wiring and imported research execution role.
[news_crawler.py](../../../backend/python/crawler/news_crawler.py) signs with
`bedrock-agentcore`, handles JSON/SSE responses, and returns search errors separately
from empty results. Custom URLs still use direct fetch; official RSS remains in the
separate technical crawler, not a news-search fallback.

[research-agent/tools.py](../../../backend/python/research-agent/tools.py) is a
separate artifact. QA later gained a third client in
[web_search.py](../../../backend/python/qa/web_search.py), covered by
[ADR-028](../../decisions/ADR-028-qa-web-search-and-proactive-question-search.md).
The [enrichment proposal](2026-08-07-knowledge-enrichment-pipeline-design.md) later
proposed mandatory full fetch; it is not evidence that snippet publication changed.

Current references: [documentation map](../../README.md) and
[infrastructure](../../INFRA-SPEC.md). Use current deployment procedures; this record
is not authorization for IAM changes or stack deployment.
