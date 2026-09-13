# Deployment

Use `.github/workflows/deploy-infra.yml`, `deploy-frontend.yml`,
`deploy-whisper.yml`, and `deploy-research-agent.yml` as executable references.
Check their triggers, selected revision, target environment and stack list before
running a manual deployment. Do not treat document examples as deployment evidence.

## Infrastructure and Go

Run the relevant build/tests from the root project guide. `scripts/build.sh` is a legacy partial zip build; verify its list and explicitly
build any changed entry point it omits. The current CI loops are partial too; `convert-doc` is a separate Docker image built from
`backend/cmd/convert-doc/Dockerfile` by CDK. Python artifacts have separate packaging.

Never deploy all stacks or implicit dependencies. KnowledgeStack contains a
staged, undeployed teardown. For an authorized change, review its synth/diff and
deploy each changed stack with `--exclusively`, following `infra/bin/infra.ts`:

```bash
(cd infra && npx cdk diff TtobakGatewayStack --exclusively)
(cd infra && npx cdk deploy TtobakGatewayStack --exclusively)
```

That command deploys GatewayStack only; it does not apply Auth, AI, Storage,
Frontend or image changes. WebSearchGateway and EdgeAuth use us-east-1; consumers
use the configured application region. AI imports a pre-created research role.
Preserve the OAC custom resource's changing Timestamp so policy tightening runs.

Whisper image and infra workflows can run independently. Keep diarization bundle
selection in the image and preserve the WhisperX dispatcher entrypoint. Rollback
of pyannote major versions requires a coherent image/pins/bundle pairing, not a
key-only or one-package revert; see ADR-035.

## Frontend

Build with `npm run build` in `frontend/`. The current workflow preserves the
CDK-generated `config.json` during S3 sync and invalidates CloudFront. It does not
perform an unconditional HTML copy or set no-cache metadata: forced HTML refresh
remains a release requirement and a workflow gap, not implemented CI behavior.
A bare sync without the config exclusion breaks runtime auth configuration.

For an authorized manual release, insert the following after sync and before
invalidation, with TTOBAK_SITE_BUCKET set to the verified target bucket:

```bash
aws s3 cp frontend/out/ "s3://${TTOBAK_SITE_BUCKET:?set the verified site bucket}/" \
  --recursive --exclude "*" --include "*.html" \
  --content-type "text/html; charset=utf-8" --cache-control "no-cache"
```

This forces HTML to reference the uploaded chunks even when sync would skip an
equal-size file. Preserve config.json and use the target distribution's
invalidation; do not infer that running the unchanged workflow adds this step.

Verify `/config.json`, current HTML and the JS/CSS chunks referenced by that HTML
through CloudFront. A successful upload alone does not prove the site is usable.

## Recovery

Identify the last working revision and the failing artifact. Rebuild/redeploy that
artifact with the same deployment constraints. Do not assume Lambda aliases exist,
blindly reset meeting rows, or execute generic all-stack rollback commands.
Inspect CloudFormation state before using stack recovery operations.

For a missing runtime config, verify the FrontendStack-generated object and restore
it through the authorized frontend/stack path. For STT failures, use
[STT troubleshooting](stt-pipeline-troubleshooting.md). Record the actual deployed
revision and verification result; a merged PR does not prove deployment succeeded.
