# Research Agent Migration to AgentCore Runtime

> Historical design record. Original date: 2026-04-22 (filename); no original status
> was recorded. The target architecture and removal list are not completion evidence.

## Motivation and decision

The classic Bedrock Agent design used action groups and separate tool Lambdas.
The migration proposed a Python/Strands agent in AgentCore Runtime so tool logic,
research control flow, and dependencies could evolve together. It intended to
retain research IDs, REST endpoints, DynamoDB metadata, S3 report paths, and the
Insights experience while changing execution behind the API.

## Proposed scope

The target placed web search, page fetch, report save, and optional prior-KB search
inside the agent container. It proposed replacing classic invocation with signed
Runtime invocation and retiring classic agent/alias/action-group resources and
legacy tool files. A direct frontend Gateway, managed agent memory, multi-agent
coordination, and WebSocket research streaming were explicitly deferred.

The original inline Python, unsigned HTTP sketch with a signing comment, endpoint
variable, CLI sequence, model version, and service-duration comparisons were
illustrations. They were neither safe deployment instructions nor proof that the
runtime or a direct client path existed. Checking HTTP(S) alone is not sufficient
SSRF protection for a page-fetch tool.

## Rationale and tradeoffs

Container-local tools reduced action-group plumbing and increased control over
agent execution. The tradeoff was a separately deployed runtime, execution-role
permissions, SDK/runtime dependencies, observability, and request/session lifecycle
management. Removing classic resources required a separate inventory and migration;
keeping REST behavior did not imply that all old resources were gone.

## Current evidence

The actual path is [ResearchService](../../../backend/internal/service/research.go)
to Step Functions, then [research-worker](../../../backend/cmd/research-worker/main.go)
using the AWS SDK's `InvokeAgentRuntime`, then the
[Python agent](../../../backend/python/research-agent/agent.py). The container is
deployed separately by [deploy-research-agent.yml](../../../.github/workflows/deploy-research-agent.yml).
It is not a raw unsigned HTTP call from the API Lambda or a direct browser bypass.

[ResearchAgentStack](../../../infra/lib/research-agent-stack.ts) still declares
classic agent/alias and tool Lambda resources; the
[legacy tool directory](../../../backend/python/research-tools) still exists.
This record does not authorize removing them. The runtime tool registry in
[tools.py](../../../backend/python/research-agent/tools.py), not the proposed list,
defines available functions. [ADR-011](../../decisions/ADR-011-interactive-deep-research.md)
records conversational research and later implementation differences.

Current references: [documentation map](../../README.md), [architecture](../../architecture.md),
and [infrastructure](../../INFRA-SPEC.md). Source declarations do not prove live deployment.
