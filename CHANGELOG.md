# Changelog

Repository change history, not a live deployment inventory. Older model aliases,
workflow counts and infrastructure choices below describe their original changes;
current configuration belongs to the project guide and source manifests.

## Unreleased

### Documentation and review

- Consolidate current project guidance, generate one shared review extract and
  embed it in isolated Kiro CI prompts.
- Convert project documentation to concise English; separate current references
  from historical decisions, implementation plans, audits and benchmarks.
- Correct review markers/latest-HEAD completion guidance, Go/Python payload scope,
  test/build instructions, unsafe deployment examples and stale feature claims.
- Add documentation integrity and local review-prompt delivery checks.

## Historical entries retained from the previous Unreleased log

No release/version boundary was recorded for these entries. Do not infer a release
date or that all listed deployment changes are still configured.

### Added

- Editable research titles distinct from immutable prompts; research Markdown/Notion
  export, tab URL synchronization and follow-up research features.
- Admin user lifecycle panel and fail-open last-login tracking (ADR-032).
- Meeting-driven Code Interpreter sizing simulator (ADR-033).
- Pending email shares for invited users, materialized after verified login.
- Mermaid zoom/pan and fullscreen display.
- Multi-model PR review, historically introduced with earlier Opus aliases.
- GPU Spot Whisper with zero scaling, S3 model loading and vocabulary prompt hints.
- AgentCore FastAPI research container, cross-meeting QA and KB recording panel.
- STT benchmark tooling, custom vocabulary, domain allowlist, crawler pipeline and
  browser tab-audio capture.

### Changed

- Split deployment/test/review workflows and move runners to ARC with setup actions.
- Evolve research/summarization model profiles and CUDA/Python image versions;
  inspect present workflow/Dockerfile values before treating an old version as pinned.
- Add Whisper-primary batch processing alongside AWS Transcribe fallback; earlier
  Nova Sonic support remained experimental, not a complete production alternative.
- Load Cognito config from runtime config.json and repair research SPA routing.

### Fixed

- Add mobile waveform/caption manual gesture recovery in addition to automatic
  wake-lock/resume/reconnect watchdog paths; preserve synchronous user activation.
- Repair recovery checkpoint lookup across webm/m4a and preserve exact stable
  checkpoint keys by bypassing timestamp prefixing for allowlisted filenames.
- Synchronize Mermaid/Shiki/table rendering with light/dark theme changes.
- Repair research route rewriting, research read timeouts, non-root AWS CLI installs,
  reserved log-group naming and AgentCore endpoint IAM resources.
- Repair stale frontend chunk/HTML deployment, auth checks, JSON parsing and XML safety.

### Security-related history

- Add PR review and credential-scan hooks, API JWT validation and KMS-backed
  integration-key handling. These additions do not certify complete compliance.
- Add crawler paywall/body-quality filtering. Filtering is not access authorization.
- Public document links later introduced an explicit, narrow unauthenticated route;
  old claims that every API route requires login are superseded by ADR-022.
