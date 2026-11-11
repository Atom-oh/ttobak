# Billing MCP Implementation Plan

Status: historical implementation plan; current API references own the contract.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let external MCP clients read authorized customer Billing metadata, datasets and reports for meeting preparation.

**Architecture:** ADR-012 caller-token CloudFront reads, a bounded Billing route with current connector grants, and three read-only MCP tools. Reuse connector authorization instead of copying its rules.

**Tech Stack:** Node.js 24, Express, TypeScript MCP, AWS SDK v3, Cognito, CDK.

**Spec:** `docs/superpowers/specs/2026-11-04-billing-mcp-design.md`

## Global Constraints

- API JSON maximum 14000 encoded bytes; MCP results maximum 32000 bytes.
- Page size 1000–8000 UTF-8 bytes, default 4000; cursor maximum 2048 characters.
- Source cap 48 MiB; no new public origins or asserted identity headers.
- Connector tokens accepted only on the new GET reading route, with current grants.
- Preserve direct and validated ancestor Billing access and source/version validation.

## Review Focus

- Expired/wrong-audience/read-only-client write-scope tokens never authorize reads.
- Membership/client blocking during a source read prevents publication.
- A rerun or rollback between pages rejects stale continuation.
- Korean/emoji/escaped JSON and a single large row remain bounded and reconstruct exactly.
- Missing source files and older unpublished uploads never become current evidence.

### Task 1: Billing bounded reading and authentication

**Files:** Create `services/billing/bff/lib/reading.js`, `lib/connector-auth.js`, tests; modify `app.js`, `lib/server.js`, package scripts; create shared authorization bundle entry in `mcp-server/src`.

**Interfaces:** `createReadingRoutes({s3,store,access,bucket,authenticate,revalidate})` returns an Express router; `createConnectorAuthenticate` verifies only resource-bound connector reads and exposes current grant revalidation.

- [x] Write security, source identity, byte pagination and stale cursor tests; run RED.
- [x] Implement route with strongly consistent pointer/upload reads and reuse canonical grant resolver/effective capabilities through the packaged artifact.
- [x] Run Billing tests GREEN and production package check.
- [ ] Commit the bounded server contract.

### Task 2: MCP tools and transport

**Files:** Create `mcp-server/src/billing-reading.ts` and tests; modify `api.ts`, `index.ts`.

**Interfaces:** `billingReadingOptions(args,section)` validates tool input; `billingReadingResult(value,options)` validates bounded server pages; `TtobakApi.readBilling(options)` uses only the exact authenticated CloudFront reading route.

- [x] Write tool registration, transport and hostile result/argument tests; run RED.
- [x] Implement three read-only tools and caller-token Billing transport while preserving Go/QA private transport.
- [x] Run full MCP tests and Lambda/stdio bundle checks GREEN.
- [ ] Commit the MCP tool integration.

### Task 3: Infrastructure and release

**Files:** Modify `infra/lib/billing-stack.ts`, `infra/bin/infra.ts`, `infra/lib/edge-auth-stack.ts`, edge/Billing tests, Billing workflow path filters, MCP README and affected current API/infrastructure references.

**Interfaces:** BillingStack receives dedicated connector client IDs and resource audience, plus restricted TransactGetItems metadata grants; existing browser clients remain isolated.

- [x] Write infra/edge assertions RED; allow writer tokens only on the exact GET Billing reading route, implement configuration and IAM, run synth/tests GREEN.
- [x] Add only shared-source Billing workflow path filters; verify clean Billing-only package build without preinstalled MCP dependencies.
- [x] Update current documentation and run documentation checks.
- [ ] Obtain independent code/security and content review (content score >=85).
- [ ] Commit/push PR, apply latest-HEAD AI feedback, pass required CI, merge.
- [ ] Track Billing and MCP deployment; verify live artifacts and auth rejection.
