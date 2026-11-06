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

## Rollback

Set `ttobak:otelCollectorEnabled` to `"false"` and deploy to remove the service;
senders must tolerate an unavailable collector (export failures never fail a
request). Removing the stack deletes the endpoint and ALB; the ClickHouse data
and the provider-side principal remain.
