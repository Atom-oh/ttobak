# Historical code review

Historical report from 2026-03-05 covering 41 files: 6 infrastructure, 16 Go backend
and 19 frontend files. It recorded 2 High, 8 Medium and 9 Low candidates. The report
mixed observations, questions and recommendations; these counts are not current
validated findings or PR acceptance criteria.

## Recorded topics

- Infrastructure: predictable origin-verification fallback, HTTP-only ALB design,
  wildcard CORS and inconsistent authentication strategy.
- Backend: attachment update helper alleged missing, mixed summarize-event formats,
  missing JWT verification, duplicate/error-helper/import concerns, per-share
  lookup cost, incomplete S3 URL decoding, default resource-name mismatch and
  ambiguous image-classification failure handling.
- Frontend: local token storage, direct Cognito versus old ALB auth design, mock
  data, TipTap dependencies, persistence fields leaking into client types, expired
  upload URLs, AudioContext cleanup and microphone selection.

The original recommendation prioritized authentication and event-contract alignment,
then error handling, pagination and frontend integration. It did not establish
that every candidate was an exploitable or reproducible defect.

## Corrections and successors

`updateAttachmentByKey` and recording AudioContext cleanup are implemented.
`ParseVerifiedJWT` verifies signatures; GatewayStack has HTTP JWT authorizers.
Summarization uses EventBridge transcript/custom events, not an assumed DynamoDB
stream trigger. The current stack is HTTP API/CloudFront with a separate QA
WebSocket, not the old ALB-auth proposal. Go/Python/infra/MCP tests exist; frontend
uses lint/build. Default resource names are overridden by deployment configuration.

Current requirements and accepted gaps are in [CLAUDE.md](../CLAUDE.md),
[API](API-SPEC.md), [infrastructure](INFRA-SPEC.md), and the relevant ADRs. Reproduce
against current code before filing an old candidate again. A documented existing
risk is not automatically fixed or approved for expansion.
