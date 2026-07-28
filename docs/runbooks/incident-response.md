# Incident Response Runbook

## Severity Levels

| Level | Description | Response Time | Examples |
|-------|------------|---------------|----------|
| P1 | Service down | 15 min | CloudFront 5xx, API Gateway 500, DynamoDB throttle |
| P2 | Feature broken | 1 hour | STT pipeline stuck, Summarize fails, Auth errors |
| P3 | Degraded | 4 hours | Slow responses, partial crawler failure, Spot interruption |

## Initial Triage

```bash
# 1. Check CloudFront distribution status
aws cloudfront get-distribution --id E3IFMH57E9UTB5 --query 'Distribution.Status'

# 2. Check API Lambda errors (last 30min)
aws logs filter-log-events --log-group-name /aws/lambda/ttobak-api \
  --filter-pattern "ERROR" --start-time $(date -d '30 min ago' +%s000) \
  --region ap-northeast-2 --query 'events[*].message' --output text | head -20

# 3. Check all Lambda invocation errors
for fn in ttobak-api ttobak-transcribe ttobak-summarize ttobak-process-image ttobak-kb ttobak-qa; do
  ERRORS=$(aws cloudwatch get-metric-statistics --namespace AWS/Lambda \
    --metric-name Errors --dimensions Name=FunctionName,Value=$fn \
    --start-time $(date -u -d '1 hour ago' +%FT%TZ) --end-time $(date -u +%FT%TZ) \
    --period 300 --statistics Sum --region ap-northeast-2 \
    --query 'Datapoints[0].Sum' --output text 2>/dev/null)
  echo "$fn: ${ERRORS:-0} errors"
done

# 4. DynamoDB throttling
aws cloudwatch get-metric-statistics --namespace AWS/DynamoDB \
  --metric-name ThrottledRequests --dimensions Name=TableName,Value=ttobak-main \
  --start-time $(date -u -d '1 hour ago' +%FT%TZ) --end-time $(date -u +%FT%TZ) \
  --period 300 --statistics Sum --region ap-northeast-2
```

## Common Issues

### "Both UserPoolId and ClientId are required"
**Cause**: Frontend `config.json` missing from S3 or stale browser cache.
```bash
# Check config.json
curl -s https://ttobak.atomai.click/config.json

# If missing, deploy infra (FrontendStack BucketDeployment creates it)
gh workflow run deploy-infra.yml

# Force CloudFront invalidation
aws cloudfront create-invalidation --distribution-id E3IFMH57E9UTB5 --paths "/*"
```

### Lambda cold start timeouts
```bash
# Check recent duration
aws logs filter-log-events --log-group-name /aws/lambda/ttobak-api \
  --filter-pattern "REPORT" --start-time $(date -d '30 min ago' +%s000) \
  --region ap-northeast-2 | grep -o "Duration: [0-9.]* ms" | sort -t: -k2 -rn | head -5
```

### CDK deploy "Cannot delete ChangeSet"
```bash
# List and delete stuck changesets
aws cloudformation list-change-sets --stack-name <STACK_NAME> --region ap-northeast-2
aws cloudformation delete-change-set --change-set-name <NAME> --stack-name <STACK_NAME> --region ap-northeast-2
```

### ECS Whisper task not starting (Spot capacity)
See [STT Pipeline Troubleshooting](stt-pipeline-troubleshooting.md).

## Rollback

```bash
# Frontend: restore previous S3 version
aws s3 sync s3://ttobak-site-180294183052-ap-northeast-2/ /tmp/current-site/ --delete
# Then deploy previous commit

# CDK: rollback specific stack
aws cloudformation rollback-stack --stack-name <STACK_NAME> --region ap-northeast-2

# Lambda: revert to previous version (if aliased)
aws lambda update-alias --function-name ttobak-api --name live --function-version <PREV_VERSION> --region ap-northeast-2
```

## Contacts
- Primary: Junseok Oh (ojs0106@gmail.com)
