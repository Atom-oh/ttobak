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
aws s3 sync frontend/out/ s3://ttobak-site-180294183052-ap-northeast-2/ --delete --exclude "config.json"
aws cloudfront create-invalidation --distribution-id E3IFMH57E9UTB5 --paths "/*"
```

### Backend (all lambdas)
```bash
cd /home/ec2-user/ttobak/backend && for dir in cmd/api cmd/transcribe cmd/summarize cmd/process-image cmd/kb; do
  GOOS=linux GOARCH=arm64 /usr/local/go/bin/go build -tags lambda.norpc -o $dir/bootstrap ./$dir
done
cd /home/ec2-user/ttobak/infra && npx cdk deploy --all --require-approval never
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
- Deploy complete: `🎉 배포 완료! S3 sync done, CF invalidation: {id}`
- Reject: `❌ 배포 중단 — reject 되었습니다.`
- Timeout: `⏰ 10분 타임아웃 — 승인 없이 배포를 중단합니다.`
