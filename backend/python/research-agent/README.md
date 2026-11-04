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
before `cdk deploy TtobakAiStack --exclusively` — `deploy-infra.yml` runs an `aws iam
get-role` preflight before that deploy to fail fast if it's missing. See
`docs/runbooks/deployment.md` for current deployment constraints. The separate
`TtobakResearchAgentStack` declares legacy Bedrock Agent resources; it does not
make this container a CDK-managed AgentCore Runtime.

## Platform version (V2 readiness)

The runtime runs on AgentCore Runtime platform V1. `deploy-research-agent.yml`
calls `update-agent-runtime` without `--platform-version`, which keeps the
current version. Platform V2 restores each instance from a snapshot taken after
startup. As of 2026-11-04 the AgentCore developer guide lists V2 Regions as
us-east-1, us-east-2, us-west-2, eu-west-1 and ap-northeast-1, not
ap-northeast-2, so the runtime stays on V1.

The code is snapshot-safe for V2:
- per-request values (message IDs via `secrets`, timestamps, SigV4
  credentials) are created in the handler;
- boto3 clients are created on first use and refresh their credentials;
- `_last_activity`, reported by `/ping`, is set by the first ping or the first
  activity, never at import.

When V2 is available in ap-northeast-2, the switch is an in-place
`update-agent-runtime --platform-version V2`. It creates a new runtime version,
moves the `DEFAULT` endpoint and takes several minutes to reach `READY`. The
container must report healthy within 120 seconds. Keep new startup code free of
random values, timestamps, credentials and host/PID identifiers.
