# Developer onboarding

Read [CLAUDE.md](../CLAUDE.md) and the [documentation map](README.md) first.
Use Go 1.25, a supported Node runtime (CI uses Node 24 for infra), Python 3.12 for
Lambda tests, AWS CLI v2 for authorized cloud operations, and Docker for container
builds. Mac recording needs macOS/Rust/Tauri native dependencies.

From the repository root:

```bash
(cd frontend && npm ci)
(cd infra && npm ci)
(cd mcp-server && npm ci)
(cd backend && /usr/local/go/bin/go mod download)
python3 -m venv /tmp/ttobak-doc-dev-venv
/tmp/ttobak-doc-dev-venv/bin/pip install 'boto3<2'
(cd frontend && npm run dev)
```

The frontend reads `/config.json` at runtime. Use environment-appropriate Cognito
configuration; an administrator must invite the account. `NEW_PASSWORD_REQUIRED`
completes first login; password reset is separate from signup.

Run relevant checks from the project guide. Go `./...` includes command-package
tests. Frontend has lint/build only. Infra has real Jest assertions, not placeholder
tests. Linux-only Rust checks do not validate ScreenCaptureKit.

Add HTTP routes in `backend/cmd/api/main.go`, handlers in `internal/handler`,
business rules in services, and persistence in repositories. Go API payload is
1.0; Python QA payload is 2.0. Update the API reference when changing contracts.
New frontend routes also require the CloudFront router's `knownPages` entry.

The batch pipeline uses Whisper and configured AWS Transcribe fallback. Historical
Nova Sonic experiments are not the current A/B provider architecture. Verify the
selected A/B transcript and segment freshness behavior in meeting service before
changing note generation.

Use [deployment](runbooks/deployment.md) for cloud changes. Never run a generic
all-stack deployment or assume local code is already deployed.
