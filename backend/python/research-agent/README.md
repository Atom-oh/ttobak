# Research Agent (AgentCore Runtime)

FastAPI + Strands Agents container for the Deep Research agent. Deployed
**outside CDK** via `.github/workflows/deploy-research-agent.yml` (push to
`main` touching this directory, or manual `workflow_dispatch`).

## Contract

- `POST /invocations`, `GET /ping` on port 8080 (AgentCore Runtime HTTP contract)
- Long-running research runs in a background thread; `/ping` returns
  `HealthyBusy` to keep the session alive past the request timeout

## Environment variables

Injected by the deploy workflow via `update-agent-runtime
--environment-variables` (not by CDK — this container isn't a CDK-managed
resource):

| Var | Source | Purpose |
|-----|--------|---------|
| `TABLE_NAME` | not injected — falls back to `tools.py`'s hardcoded default (`ttobak-main`) | DynamoDB single-table name |
| `KB_BUCKET_NAME` | not injected — falls back to `tools.py`'s hardcoded default (`ttobak-kb-180294183052`) | KB S3 bucket for `save_report` |
| `WEB_SEARCH_GATEWAY_URL` | GitHub Actions repo variable `WEB_SEARCH_GATEWAY_URL` | AgentCore Gateway MCP endpoint for `web_search` (see `TtobakWebSearchGatewayStack`'s `GatewayUrl` output) |
| `WEB_SEARCH_GATEWAY_REGION` | hardcoded `us-east-1` in `deploy-research-agent.yml` | Gateway's region for SigV4 signing — always `us-east-1` (`TtobakWebSearchGatewayStack` only deploys there), so it's a fixed value in the workflow rather than a repo variable that could drift/typo with no CFN output to catch it |

**After deploying `TtobakWebSearchGatewayStack`**, set the
`WEB_SEARCH_GATEWAY_URL` GitHub Actions repo variable from its `GatewayUrl`
CFN output, then re-run `deploy-research-agent.yml` (or push a no-op change
to this directory) so the running container picks it up.
`deploy-research-agent.yml` fails the deploy outright (`exit 1`) if that
variable is unset — the "misconfigured" `web_search` response below only
applies to an **already-running** container that was deployed before the
variable was set (e.g. before this repo variable requirement existed):
`{"results": [], "message": "Web search is misconfigured"}`.

## IAM

Runtime execution role `ttobak-agentcore-research-role` is a manually
created, pre-existing IAM resource (created once, out-of-band, when the
AgentCore Runtime was first provisioned) — `deploy-research-agent.yml` only
*consumes* it (`--role-arn` on `update-agent-runtime`); it does not create
it, and neither does any other CI pipeline. `TtobakAiStack` imports the
role by ARN to attach the Gateway-invoke policy, so it must already exist
before `cdk deploy TtobakAiStack` — `deploy-infra.yml` runs an `aws iam
get-role` preflight before that deploy to fail fast if it's missing. See
root `CLAUDE.md`'s Known Issues for the full SP1 deploy sequence.
