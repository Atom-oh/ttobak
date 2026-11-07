# Observability (OTel → mgmt ClickHouse)

`TtobakObservabilityStack` (`infra/lib/observability-stack.ts`) runs an OTLP/HTTP
collector that writes ttobak traces, metrics and logs into the shared mgmt
observability ClickHouse (`otel` database, the same `otel_traces`/`otel_logs`
schema the mall collectors use, 30-day TTL). Grafana on `mall-apne2-mgmt` reads
that database.

## Network path

All traffic is private. Senders (VPC-attached Lambdas, and later the
CloudFront VPC origin for browser spans) post OTLP/HTTP to the internal ALB
`ttobak-otel` in `fsi-demo-vpc` isolated subnets. The ALB admits only the
`ttobak-otel-client` security group. Collector tasks (ECS Fargate, ARM64) write
to ClickHouse on TCP 9000 through an interface endpoint for the mgmt ClickHouse
PrivateLink service `vpce-svc-04e00839390fb475d` (owner: AWS-Demo-Platform,
NLB `k8s-observab-clickhou-*` in `production-vpc`). The service exists only in
`ap-northeast-2a`/`2c`, so the endpoint uses the supported `2a` subnet
(`lookupSupportedAzs`). No VPC, TGW attachment or public ingress is added.

Senders find the collector through SSM `/ttobak/otel/collector-endpoint`, not
cross-stack exports.

## Credentials

The collector authenticates as the ClickHouse user `ttobak` (limited to the
`otel` database), defined in AWS-Demo-Platform
`k8s/system/clickhouse-mgmt` with its password from Secrets Manager
`/demo-platform/clickhouse/ttobak` via ESO. The task reads the same secret
(`username`, `password` keys) through ECS secrets; nothing is in environment
values or code. Never print the secret.

## First activation (one time)

Status: steps 1-4 completed 2026-11-06 (endpoint `vpce-0b8c6ce5bf16f0489`
accepted and `available`); step 5 enables the collector.

1. Provider permission (done 2026-11-06):
   `aws ec2 modify-vpc-endpoint-service-permissions --service-id vpce-svc-04e00839390fb475d --add-allowed-principals arn:aws:iam::180294183052:root`.
2. Deploy `TtobakObservabilityStack` with `ttobak:otelCollectorEnabled` `"false"`
   (a new environment starts there). This creates the endpoint, ALB, security groups and
   SSM parameter without a collector service.
3. Accept the pending endpoint connection as the service owner:
   `aws ec2 describe-vpc-endpoint-connections --filters Name=service-id,Values=vpce-svc-04e00839390fb475d`,
   then `aws ec2 accept-vpc-endpoint-connections --service-id vpce-svc-04e00839390fb475d --vpc-endpoint-ids <id>`.
   Wait until the endpoint state is `available`.
4. Confirm the AWS-Demo-Platform change is synced: the `clickhouse-ttobak`
   Secret exists in `observability` and the CHI reconciled the `ttobak` user.
5. Set `ttobak:otelCollectorEnabled` to `"true"` in a reviewed PR and deploy. The
   service uses a deployment circuit breaker; check that the ALB target is
   healthy and that collector logs show no ClickHouse connection errors.

## API tracing (ttobak-api)

`ttobak-api` exports OTLP traces and logs when context
`ttobak:apiTelemetryEnabled` is `"true"` (default `"false"`; off leaves the
function configuration unchanged). The flag VPC-attaches the function to the
`fsi-demo-vpc` private-with-egress subnets with its own `ttobak-api` group (no
ingress, TCP 443 egress for AWS APIs through endpoints and NAT) plus the
existing `ttobak-otel-client` group, and sets `OTEL_EXPORTER_OTLP_ENDPOINT` from
SSM `/ttobak/otel/collector-endpoint` and `OTEL_SERVICE_NAME=ttobak-api`. The
endpoint's first DNS label (`http://internal-<alb>-<id>`) is also bound into the
`live` version description (compact keys keep it within Lambda's 256-character
limit), so a replaced collector ALB publishes a new version. GatewayStack owns the region-scoped ENI
grant (`ApiNetworkInterfaces`) on the API role and keeps it regardless of the
flag: Lambda deletes Hyperplane ENIs asynchronously with that role after a
rollback. The grant is the only flag-off difference from the pre-telemetry
GatewayStack template; the function itself is unchanged when the flag is off.

Recorded data (`backend/internal/telemetry`):

- One server span per request named `METHOD /route/{pattern}`; attributes are
  only `http.request.method`, `http.route`, `http.response.status_code` and
  `error.type` (5xx). No URL, query, header, body, identity or user content.
  Only the trace ID and parent span ID of a single valid W3C `traceparent` are
  adopted (for future frontend propagation); the parent is rebuilt with an
  empty `tracestate`, `baggage` is never extracted, and every request is
  sampled regardless of the caller's sampled flag.
- One client span per AWS SDK operation (`DynamoDB.GetItem`, `S3.PutObject`,
  ...) with `rpc.*` and `aws.error_code`/`aws.error_fault` only. SDK internal
  phases, error messages, endpoints and payloads are dropped, and no trace
  headers are sent to AWS. Streaming Bedrock spans end when the call returns,
  not when the stream is consumed.
- The `request completed` log line is also emitted as an OTLP log record with
  the same fields and the trace/span IDs.

Each invocation flushes before returning, bounded by 150 ms or the remaining
invocation time minus 50 ms, whichever is smaller (no flush without budget). A failed or timed
out flush is logged once and flushes are skipped for 30 s; queues are bounded
(2048) and overflow is dropped, so export never fails a request. With an empty
or invalid endpoint the providers are not installed and the middleware is a
pass-through.

Activation:

1. Confirm the collector is serving (`ttobak:otelCollectorEnabled` `"true"`,
   ALB target healthy) and the SSM parameter exists.
2. The release-coordinator gate verifies the VPC consumer's private Secrets
   Manager path before mutation (`vpc_secret_path_verified`, see
   `scripts/operations/HTTP_ORIGIN_RELEASE.md`); it fails closed with
   `ACTIVATION_CONSUMER_VPC_UNVERIFIED` when any condition is unobserved.
3. Set `ttobak:apiTelemetryEnabled` to `"true"` in a reviewed PR and deploy
   `TtobakObservabilityStack` (if changed), then `TtobakGatewayStack`
   `--exclusively`. The VPC update publishes a new version for the `live` alias.
4. Verify `/api/health` and an authenticated read still succeed, then query
   ClickHouse.

Rollback:

1. Set the flag to `"false"` and deploy `TtobakGatewayStack` `--exclusively`.
   The function leaves the VPC and exports nothing; the ENI grant stays.
2. Wait for the Hyperplane ENIs to drain (Lambda releases them asynchronously;
   this can take tens of minutes) until
   `aws ec2 describe-network-interfaces --filters Name=group-id,Values=<sg-id>`
   returns nothing.
3. Delete the `ttobak-api` security group if CloudFormation's cleanup could not
   (`DependencyViolation` while ENIs remain):
   `aws ec2 delete-security-group --group-id <sg-id>`. A leftover group with
   that fixed name makes a later re-enable fail. Never detach Lambda-owned ENIs
   by hand, and do not remove the ENI grant.

Verification queries:

```sql
-- Latency by route (ms)
SELECT SpanName, quantile(0.5)(Duration)/1e6 AS p50_ms,
       quantile(0.9)(Duration)/1e6 AS p90_ms, count() AS n
FROM otel.otel_traces
WHERE ServiceName = 'ttobak-api' AND SpanKind = 'Server'
  AND Timestamp > now() - INTERVAL 1 HOUR
GROUP BY SpanName ORDER BY p90_ms DESC;

-- Slowest AWS operations
SELECT SpanName, quantile(0.9)(Duration)/1e6 AS p90_ms, count() AS n
FROM otel.otel_traces
WHERE ServiceName = 'ttobak-api' AND SpanKind = 'Client'
  AND Timestamp > now() - INTERVAL 1 HOUR
GROUP BY SpanName ORDER BY p90_ms DESC LIMIT 20;

-- Request logs correlated with traces
SELECT Timestamp, TraceId, LogAttributes['route'], LogAttributes['status'],
       LogAttributes['duration_ms']
FROM otel.otel_logs
WHERE ServiceName = 'ttobak-api' AND Timestamp > now() - INTERVAL 15 MINUTE
ORDER BY Timestamp DESC LIMIT 50;
```

## Image and startup

The collector image is a CDK image asset (`infra/assets/otel-collector/Dockerfile`)
wrapping the official contrib 0.118.0 image pinned by digest. The account's
`ghcr` ECR pull-through rule failed upstream authentication on 2026-11-06, so
the stack does not depend on it. With `create_schema: true` the ClickHouse
exporter connects at startup; an unreachable ClickHouse makes the collector exit
(`create database: dial tcp ...`), and ECS restarts the task. Check collector
logs first when the deployment circuit breaker rolls back.

## Rollback

Set `ttobak:otelCollectorEnabled` to `"false"` and deploy to remove the service;
senders must tolerate an unavailable collector (export failures never fail a
request). Removing the stack deletes the endpoint and ALB; the ClickHouse data
and the provider-side principal remain.
