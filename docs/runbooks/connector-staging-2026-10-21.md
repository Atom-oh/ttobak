# Connector disabled-staging acceptance — 2026-10-21

**The flags-false staging prerequisite passed. Dedicated private connector
acceptance remains pending.** This dated record accompanies the activation
change and preserves the release runbook's separate live acceptance gate.

## Staging scope and results

The independently reviewed staging source was
`72497ec361a6ff80a828f13c4df9d186ebeb5adc`, including connector infrastructure
revision `b23be840cc0c579616797c1b89ed5dadc0b05b6b`. The frozen assembly manifest
SHA-256 was `790a8940d447b84a8fb4afe9b24a2d401ca90072177cc91355876a45f2a0b056`.

The staging review completed at 09:37 UTC. Between 09:50 and 10:11 UTC on
2026-10-21, the four relevant infrastructure stacks completed deployment and
artifact verification. Both dedicated OAuth issuance and the private bridge
remained disabled. No dedicated user approval was enabled by this staging rollout.

Observed first-party compatibility checks passed: authenticated API reads,
HTTP and WebSocket QA, provider authorization-code/PKCE login, and bounded HTTP
MCP reads. A reviewed candidate stdio bundle also read through the deployed API
using a valid saved cache. That candidate check did not prove installation or
actual provider refresh. Staged cryptographic verification was tested separately;
it did not establish successful producer-role signing, invocation or private grants.

## Required activation acceptance

The staging result does not prove dedicated private operation. Before declaring
activation complete and integrating the release, record dedicated provider and
resource/scope behavior, natural expiry, private current-source reads and QA,
personal-note idempotency and edit/deletion behavior, policy and approval
lifecycle, client isolation, installed stdio refresh, and live flag-off rollback.
Follow the [release sequence](connector-release.md#deployment-sequence).

Later summary-privacy deployment superseded the historical staging baseline.
Capture current versions and parameter values before activation; preserve the
existing writer/reader state and recorded cutoff. Do not restore historical
versions or reset parameters to reproduce this staging result.

## Evidence inventory

Detailed operational receipts remain in the private operator evidence archive.
The relative paths and SHA-256 values below bind this public readiness record to
those original files without publishing operational identifiers, tokens, user
content, or detailed security-probe responses.

| Receipt | SHA-256 |
|---|---|
| `connector-production-staging-content-review.md` | `2a32ff819269907dce9df6c16efe61d483f2b488d7527f1f9166ce4a835a6838` |
| `connector-production/staging-preflight.json` | `e3188f0b794204318c47e4f0d2abcc444a69d34120c1d5e03276ae6105bdb3a1` |
| `connector-production/four-stack-staging-complete.json` | `b365d3edcd13c1ea494863328e44ac4fe338457d4be636a01ad25721d1886e85` |
| `connector-production/staging-artifact-verification.json` | `08dd85e1d77bcadb8ca9b6cee246816961fe11606b70293c916ecfb9aa9fbda3` |
| `connector-production/first-party-http-acceptance.json` | `d6fffddf576d9aa89e0234e6f4d9e0905e56265a5304e8b3d040086b9bba589e` |
| `connector-production/staged-crypto-acceptance.json` | `d8fed153f5170bdcbc218f2aa61b722c7bc62a98d668ca4f2b23ed961912251b` |
| `connector-production/qa-http-acceptance.json` | `5929f7ecef06f692c512fcc8e781c413cc3c68e46ba36a05896dbdfaa4bfc631` |
| `connector-production/qa-ws-positive-acceptance.json` | `c61025513ac0ca69fe2d24067ca41f6f8854c464fc843496bb1a076fffee294c` |
| `connector-production/first-party-oauth-acceptance.json` | `907458118658ebf91c294feb5fe1f139b9bdcf6c8e7ee04b897dcdf181f6695f` |
| `connector-production/first-party-mcp-acceptance.json` | `876f51fbe23f0e8f13c602f7cb945d9a188c1a8f366c8d0d321bc197507a0a2a` |
