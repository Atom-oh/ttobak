---
name: deploy
description: Build, validate and deploy the authorized TTOBAK artifacts using current workflows and stack constraints.
---

# Deploy

Follow `docs/runbooks/deployment.md` and the root guide. Prepare build/test results
and a concrete target/revision before any required approval. Respect authorization
already given in the session; do not create redundant confirmation loops.

Never send Slack/email messages without explicit authorization. This skill does
not authorize notifications, public deployment or a new target environment.

Use repository-relative paths and current workflow artifact lists. Go has eight
zip entry points; convert-doc is a container. Never use all-stack or implicit
CDK dependency deployment: select each changed stack with `--exclusively`.
Preserve `config.json`, force HTML refresh and invalidate CloudFront for frontend
deployment. Validate through the serving endpoint after deployment.

Report the deployed revision, changed resources, checks and remaining limitations.
