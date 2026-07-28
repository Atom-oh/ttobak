---
name: deploy
description: Build and deploy ttobak frontend/backend with Slack approval. Sends approval request to #devops, then uses AskUserQuestion for terminal-based approval OR polls Slack thread+channel for approve/reject response.
---

# Deploy with Slack Approval

Interactive deployment workflow: build → Slack notification → approval → deploy → Slack result.

## Approval Modes

### Mode A: Terminal Interactive (Recommended)
1. Build the target
2. Send informational message to `#devops` (channel ID: `C02USFNQMT5`)
3. Use `AskUserQuestion` with approve/reject/diff choices in terminal
4. On approve → deploy → notify Slack with result

### Mode B: Slack Polling (for unattended)
1. Build the target
2. Send approval request to `#devops`
3. Poll every 60s using `ScheduleWakeup` (max 10 rounds = 10 min timeout)
4. Check BOTH `read_thread` AND `read_channel` for responses
   - Thread replies: check replies to parent message
   - Channel messages: check messages posted after the approval request timestamp
   - Match "approve" or "reject" (case-insensitive) from non-bot users
5. On approve → deploy → notify Slack

## Slack Config
- Channel: `#devops` (ID: `C02USFNQMT5`)
- Bot user ID: check message sender to exclude bot's own messages

## Deploy Commands

### Frontend
```bash
cd /home/ec2-user/ttobak/frontend && npm run build

# 1) sync chunks/assets (uploads new content-hashed chunks, deletes stale ones)
aws s3 sync out/ s3://ttobak-site-180294183052-ap-northeast-2/ --delete --exclude "config.json"

# 2) ⚠️ MANDATORY re-upload of ALL html — DO NOT SKIP.
#    `aws s3 sync` judges index.html "unchanged" by size+mtime and SKIPS it, while
#    step 1 deletes the old chunks it referenced → stale index.html → 403 on a
#    deleted chunk → ChunkLoadError → login page crashes. (Happened twice: 2026-06-03/04.)
#    Unconditional cp guarantees every html points at the freshly-uploaded chunks.
aws s3 cp out/ s3://ttobak-site-180294183052-ap-northeast-2/ --recursive \
  --exclude "*" --include "*.html" \
  --content-type "text/html; charset=utf-8" --cache-control "no-cache"

# 3) invalidate (flushes CloudFront's cached old html)
aws cloudfront create-invalidation --distribution-id E3IFMH57E9UTB5 --paths "/*"
```

**Post-deploy verification (run after invalidation completes):** confirm the bucket is
self-consistent — every html must reference a chunk that exists.
```bash
# live index.html chunks should all return 200 (never 403)
curl -s https://d2olomx8td8txt.cloudfront.net/ \
  | grep -oE '/_next/static/[^"]+\.(js|css)' | sort -u \
  | while read c; do curl -s -o /dev/null -w "$c => %{http_code}\n" "https://d2olomx8td8txt.cloudfront.net$c"; done
```
Any non-200 (especially 403, since OAC returns 403 not 404 for missing objects) = broken deploy.

### Backend (all lambdas)
```bash
cd /home/ec2-user/ttobak/backend && for dir in cmd/api cmd/transcribe cmd/summarize cmd/process-image cmd/kb; do
  GOOS=linux GOARCH=arm64 /usr/local/go/bin/go build -tags lambda.norpc -o $dir/bootstrap ./$dir
done
# ⚠️ Use --exclusively TtobakGatewayStack, NOT --all. `cdk deploy --all` fails on
#    TtobakKnowledgeStack (intentional Bedrock KB teardown is staged but undeployed;
#    cross-stack export BedrockDataSourceDataSourceId is in use by GatewayStack → CFN
#    refuses to delete it → rollback). GatewayStack hosts all 5 Lambdas + API routes.
cd /home/ec2-user/ttobak/infra && npx cdk deploy TtobakGatewayStack --exclusively --require-approval never
```

## Slack Message Templates

### Approval Request
```
[배포 승인 요청] {target}
브랜치: {branch} | 커밋: {hash} {message}
변경: {summary}
빌드: {pass/fail}
스레드에 답장: approve / reject
```

### Result Messages
- Approve detected: `✅ 승인 확인 — 배포를 시작합니다.`
- Deploy complete: `🎉 배포 완료! S3 sync + html 강제 재업로드 done, CF invalidation: {id}`
- Reject: `❌ 배포 중단 — reject 되었습니다.`
- Timeout: `⏰ 10분 타임아웃 — 승인 없이 배포를 중단합니다.`
