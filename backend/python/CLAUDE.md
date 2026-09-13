# Python artifacts

Follow the root guide. Each artifact owns its dependency manifest and deploy path;
do not apply one runtime/library convention to every Python directory.

| Directory | Role |
|---|---|
| `qa/` | Lambda Q&A/tool loop, detection, WebSocket delivery; HTTP payload 2.0 |
| `crawler/` | Step Functions crawler stages and ingestion |
| `research-agent/` | ARM64 AgentCore Runtime container, FastAPI/Strands |
| `research-tools/` | Legacy Bedrock Agent tool handlers; check callers before changing |
| `sim/` | Async worker for codegen and Code Interpreter execution |

The research container exposes `/invocations` and `/ping` on port 8080; background
research keeps health responsive. Its Dockerfile owns its Python version. Do not
replace its FastAPI bootstrap based on an SDK recommendation without verifying
startup/health behavior. Managed Lambda runtimes are configured in CDK.

SigV4/MCP web-search plumbing is intentionally duplicated in crawler, research,
and QA because they deploy separately. Keep fixes consistent. QA hash-redacts
queries and enforces its per-user hourly abuse limit before gateway calls, with
the accepted fail-open behavior documented in ADR-028.

Simulator codegen receives validated requirements/options, never the raw meeting
transcript. Preserve matching simRunId writes, the interpreter's empty execution
role, and SANDBOX networking.

Use the root unittest commands; crawler/research tests require `boto3<2`. No global
ban on `invoke_model`, HTTP libraries, or SDK packages is implied: inspect each
artifact's actual calls and requirements.
