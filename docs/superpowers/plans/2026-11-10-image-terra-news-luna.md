# Image Terra and news Luna implementation plan

**Status:** Historical implementation record (2026-11-10); delivery and runtime acceptance are pending.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Run uploaded-image analysis on Terra and both news-summary paths on Luna.

**Architecture:** Keep the existing Bedrock clients and publication boundaries.
Use native multimodal OpenAI requests for images and Converse for news, with
separate workload settings and exact model grants.

**Tech Stack:** Go, boto3 Python, TypeScript CDK, static HTML documentation.

**Spec:** `docs/superpowers/specs/2026-11-10-image-terra-news-luna-design.md`

## Global constraints

- Image: `global.openai.gpt-6.1-terra`; news: `global.openai.gpt-6-luna`.
- Image bytes <=16 MiB; model response bytes <=128 KiB.
- News input <=100,000 tokens; existing output and repair ceilings unchanged.
- No model fallback, activation changes, new dependencies or public routes.
- Content score >=85 and current-head review precede deployment.

## Review focus

- Unsupported media and oversized images fail before a model call.
- Blank, refused and reasoning-only responses do not publish image analysis.
- Both image calls send image bytes to Terra independently of summary settings.
- Both news paths disable reasoning using the actual Converse field contract.
- Exact new IAM grants and outgoing permission overlap permit a safe rollout.

### Task 1: Uploaded-image migration

**Files:** `backend/internal/service/bedrock.go`, new `bedrock_image.go`,
new `bedrock_image_test.go`, `ai_note_source_test.go`; `infra/lib/ai-stack.ts`,
`gateway-stack.ts`, `model-config.ts`, `infra/test/model-deployment.test.ts`.

**Interface:** `invokeImageModel(ctx, systemPrompt, userPrompt, imageBase64,
mediaType, maxTokens)` returns visible text or an error, using the same runtime.

- [x] Add real HTTP-boundary tests for both Terra image calls and payload format.
- [x] Observe the tests failing against Claude transport.
- [x] Implement the independent setting, bounded read and native request parser.
- [x] Test media/size boundaries and partial, empty and refused completions.
- [x] Add exact regional IAM resources and Lambda environment configuration.
- [x] Run all Go tests, vet, ARM64 builds and CDK configuration tests.

### Task 2: News migration and delivery

**Files:** `backend/python/crawler/news_crawler.py`, `news_enrichment.py`,
`news_model.py` and their existing tests; `infra/lib/crawler-stack.ts`,
`news-publication.ts`, `infra/test/news-publication.test.ts`; current model
documentation and public guide.

**Interface:** Existing Converse requests/results and news artifacts remain
unchanged except the model and reasoning configuration.

- [x] Assert both real summary paths send Luna and `reasoning.effort=none`.
- [x] Observe request-contract failures before implementing.
- [x] Update requests, exact IAM grants and verified metering rates.
- [x] Run crawler tests, full infra tests/synth and synthetic runtime probes.
- [x] Synchronize references and validate the guide and its content score.
- [ ] Commit, create a PR and resolve current-head review findings.
- [ ] Merge after required gates, watch deployment, verify live configuration.
