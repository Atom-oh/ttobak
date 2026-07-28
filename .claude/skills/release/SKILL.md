# Release Skill

## Trigger
When user requests a deployment or release.

## Steps
1. Run pre-flight checks:
   - `cd backend && /usr/local/go/bin/go build ./...`
   - `cd frontend && npm run build`
   - `cd infra && npx cdk synth`
2. Build all Lambda binaries:
   ```bash
   cd backend && for dir in cmd/api cmd/transcribe cmd/summarize cmd/process-image cmd/kb; do
     GOOS=linux GOARCH=arm64 /usr/local/go/bin/go build -tags lambda.norpc -o $dir/bootstrap ./$dir
   done
   ```
3. Deploy infrastructure: `cd infra && npx cdk deploy --all --require-approval never`
4. Deploy frontend: `aws s3 sync frontend/out/ s3://ttobak-site-180294183052-ap-northeast-2/ --delete`
5. Invalidate CloudFront: `aws cloudfront create-invalidation --distribution-id E3BPV9VFNI1H2S --paths "/*"`
6. Verify deployment by checking API Gateway endpoint health

## Constraints
- Always confirm with user before deploying to production
- Never deploy with failing builds
- CDK stack order: Auth+Storage → AI → Knowledge → EdgeAuth → Gateway → Frontend
