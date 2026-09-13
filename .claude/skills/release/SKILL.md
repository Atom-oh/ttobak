---
name: release
description: Validate and deliver an authorized TTOBAK release using the project's current PR and deployment runbooks.
---

# Release

Use the root validation commands, `docs/runbooks/pr-review.md`, and
`docs/runbooks/deployment.md`. Do not substitute a build for required tests.

For assigned PR delivery, finish current-HEAD review/fixes and merge under the
user's standing conditions. Deployment is a separate authorized operation: build
all changed artifacts, inspect the target/revision and deploy only selected
stacks with `--exclusively` in the app's dependency order. Never use `--all` or
apply KnowledgeStack's staged teardown as a side effect.

Preserve frontend config.json, force HTML refresh, invalidate the actual target
CloudFront distribution and verify through CloudFront. Report actual deployed
state; do not use an old distribution ID or direct Gateway health as proof.
Honor existing session authorization and do not invent another confirmation loop.
