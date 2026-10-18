# Deployment

Use `.github/workflows/deploy-infra.yml`, `deploy-frontend.yml`,
`deploy-whisper.yml`, and `deploy-research-agent.yml` as executable references.
Check their triggers, selected revision, target environment and stack list before
running a manual deployment. Do not treat document examples as deployment evidence.

## Infrastructure and Go

Run the relevant build/tests from the root project guide. `scripts/build.sh` and
the test/deploy CI loops build all eight Go zip functions, including websocket and
ws-authorizer, with ARM64/lambda.norpc. `convert-doc` is a separate Docker image built from
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

Run `npm ci`, then `npm run lint && npm run test:unit && npm run build` in
`frontend/` for local verification. The deployment workflow installs dependencies,
runs `npm run test:unit` (the `frontend/scripts/*.test.mjs` suites), then runs
the required lint and production build before the publication contract tests.
Each failed check stops the workflow before upload. Pull requests that touch
`frontend/` run the unit suites and lint in `test-frontend.yml`.

The workflow's shared publisher uploads immutable assets first, preserves
CDK-generated `config.json`, force-copies HTML and RSC payloads with no-cache
metadata, then invalidates CloudFront. Old chunks are retained for existing
pages; no deleting sync is used. The manual helper defaults to frontend and
rejects generic infra/all deployment. See the
[security, publication and recovery contract](frontend-release.md).

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
