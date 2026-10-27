# Configurable AI Models Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Select meeting-note and high-quality translation models through deployment environment variables or CDK context, with matching Lambda configuration and exact IAM grants.

**Architecture:** Resolve one immutable configuration in the CDK entry point and pass it to AiStack and GatewayStack. Keep Sol/Luna defaults, offer standard/terra/sonnet presets, and normalize bare model IDs to global inference profiles. Workflow inputs override repository variables; individual model selections override their preset.

**Tech Stack:** TypeScript, AWS CDK, Jest, GitHub Actions.

**Spec:** The user requested deployment model selection such as `model-meeting-summery: openai.gpt-6.1-terra`, including translation. This document records that request and its implementation; it is historical evidence, not deployment acceptance.

## Global Constraints

- Preserve Sol/Luna defaults and all existing source/publication/privacy guards.
- Only the API role receives the translation model grant; only the summary role receives the final-note model grant.
- Grant exact model/profile resources; reject wildcards, ARNs, paths and unsupported request formats before synthesis.
- Preserve release coordinator, prerequisite checks, explicit deployment order and KnowledgeStack exclusion.
- Do not automatically activate a new model or enable disabled deployment workflows.
- During model transitions, retain exact outgoing grants; the coordinator must fail before mutations if an active model would lose permission.
- Documentation is English; never log credentials or private meeting data.

## Review Focus

1. Explicit context versus environment versus preset precedence must be deterministic (configuration tests).
2. Blank workflow values must preserve repository variables and defaults (workflow and configuration tests).
3. Unsupported translation families must fail before synthesis instead of silently using fallback (configuration tests).
4. Selected Lambda IDs and IAM resources must refer to the same models (stack assertions).
5. The user's `summery` spelling must resolve or fail explicitly rather than being ignored (alias/conflict tests).

### Task 1: Shared model configuration and stack wiring

**Files:** `infra/lib/model-config.ts`, `infra/bin/infra.ts`, `infra/lib/ai-stack.ts`, `infra/lib/gateway-stack.ts`, `infra/test/model-config.test.ts`, existing stack tests.

- [x] Add failing tests for presets, normalization, precedence, aliases and rejected IDs.
- [x] Observe RED; implement typed configuration and validation.
- [x] Add failing stack tests for selectable model environment and exact per-role grants.
- [x] Observe RED; resolve once and pass the same config into both stacks.
- [x] Run build and relevant Jest suites; commit.

### Task 2: Deployment controls and operational documentation

**Files:** `.github/workflows/deploy-infra.yml`, `infra/test/model-deployment-workflow.test.ts`, `docs/runbooks/model-deployment.md`, `docs/INFRA-SPEC.md`, `docs/README.md`.

Retain the completed synthetic cost/quality comparison unchanged under
`docs/research/evaluations/2026-10-27-model-candidates/` so pricing claims link to
their measured evidence and recording limitations.

- [x] Add failing workflow contract tests for shared model environment and preserved deployment gates.
- [x] Observe RED; add optional dispatch inputs and shared repository-variable configuration.
- [x] Document examples, precedence, persistent selections, verification, rollback and pricing evidence limits.
- [x] Run full infra tests/build, complete-app synth for each preset and documentation checks.
- [ ] Commit and request one fresh-context code/IAM/workflow review; address material findings and rerun affected checks.

### Review fix: Versioned IDs and safe model cutover

**Files:** Model configuration/stacks/tests above; `scripts/operations/http_origin_release.py`,
`scripts/operations/test_model_cutover.py`, `scripts/operations/test_http_origin_release.py`,
the infrastructure workflows and model runbook.

- [x] Reproduce rejection of valid numeric version suffixes and missing outgoing model grants.
- [x] Add bounded explicit retained-model selections and deployment metadata.
- [x] Gate release before mutations on exact active/target grants bound to the correct roles; block premature grant cleanup.
- [x] Correct API alias versus unqualified summary verification instructions.
- [ ] Rerun full checks and ask the same independent reviewer to verify the fix.
