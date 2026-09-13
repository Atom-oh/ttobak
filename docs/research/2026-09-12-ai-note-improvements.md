# Remaining AI note improvements — delivery record

Historical delivery record begun on 2026-09-12. This follows the 2026-09-11 audit and covers the entire requested improvement
set. Completing one PR does not complete the overall goal.

| Item | Required resulting behavior | Completion evidence | Status |
| --- | --- | --- | --- |
| Extraction recovery | Failed action-item extraction is visibly different from successful `[]`; previous results survive failures; authorized users can retry and persist completion checks without stale writes | PR194–198; Go tests/vet, frontend lint/build, infra tests/synth; merged and deployed | Delivered |
| Automatic indexing and unified document search | Meeting and personal/account document create/edit/delete automatically reach search; reordered jobs cannot restore old revisions; discovery and returned data respect current sharing/revocation | End-to-end synthetic create → search → edit/new term → search → delete/revoke tests, worker retry/reconciliation tests, scoped IAM/synth, deployed pipeline evidence | Pending |
| Attachment text extraction | Supported PDF/PPTX/DOCX/MD meeting attachments contribute extracted text to summary and Q&A, with visible pending/failure states and authorized file access | Representative local file fixtures, parser/resource-limit tests, workflow and permission tests, deployment evidence | Pending |
| Summary quality evaluation | A reviewed reference set measures factual preservation, numbers/negation, decisions versus proposals, assignees/deadlines and evidence; actual model results are recorded honestly | PR199 evaluator and prompt fixes; real workflow 34708738649; [25/25 corpus checks, raw evidence and manual review](evaluations/2026-09-12-note-quality/README.md) | Historical real run and report preserved |
| Long-meeting MCP reading | Clients can read notes first, then bounded transcript time ranges/pages with provenance, completeness and continuation information | Real MCP protocol tests for long/Korean data, page boundaries, no omissions/duplication, bundle reproduction and deployment | Implementation present in main; deployment evidence remains separate |

Each PR targets main, completes latest-head review/CI and deployment verification.
Preserve root AGENTS.md user edits. Do not use customer data as test fixtures.

Delivery evidence: PR194–199 are merged. PR197 frontend and infrastructure deployments succeeded (runs 34705112285/34705112280). PR198's bounded-reading API, metadata-only action reads and terminal-state handoff polling follow-up are deployed (runs 34707218661/34707218669). PR200 supplied the bounded MCP adapter and bundle now present in main; its
historical review/deployment evidence is separate from this documentation sync. Automatic indexing and attachment integration remain open work.

Current source boundary: manual-only private/shared snapshot bootstrap is scheduled;
canonical/all-mode indexing and the prepared strict QA/history integration remain
staged. Parser/worker and attachment state exist, but full producer/consumer
activation must follow ADR-038/039/042. Merged foundation PRs alone do not complete
the end-to-end automatic indexing or attachment-grounding goals.
