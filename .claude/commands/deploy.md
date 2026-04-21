# Build and Deploy

Build all artifacts and deploy to AWS. Requires explicit user confirmation.

## Steps
1. Pre-flight: build all Go binaries, frontend, and CDK synth
2. Confirm with user before proceeding
3. Deploy CDK stacks in dependency order
4. Deploy frontend to S3 + CloudFront invalidation
5. Verify API health check
