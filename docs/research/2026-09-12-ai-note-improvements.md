# Remaining AI note improvements — delivery record

This follows the 2026-09-11 audit and covers the entire requested improvement
set. Completing one PR does not complete the overall goal.

| Item | Required resulting behavior | Completion evidence | Status |
| --- | --- | --- | --- |
| Extraction recovery | Failed action-item extraction is visibly different from successful `[]`; previous results survive failures; authorized users can retry and persist completion checks without stale writes | Parser, lifecycle, authorization, duplicate-delivery and concurrent-write tests; frontend lint/build; latest-head PR review and deployment | In progress |
| Automatic indexing and unified document search | Meeting and personal/account document create/edit/delete automatically reach search; reordered jobs cannot restore old revisions; discovery and returned data respect current sharing/revocation | End-to-end synthetic create → search → edit/new term → search → delete/revoke tests, worker retry/reconciliation tests, scoped IAM/synth, deployed pipeline evidence | Pending |
| Attachment text extraction | Supported PDF/PPTX/DOCX/MD meeting attachments contribute extracted text to summary and Q&A, with visible pending/failure states and authorized file access | Representative local file fixtures, parser/resource-limit tests, workflow and permission tests, deployment evidence | Pending |
| Summary quality evaluation | A reviewed reference set measures factual preservation, numbers/negation, decisions versus proposals, assignees/deadlines and evidence; actual model results are recorded honestly | Executable evaluation command, scored reference cases, sensitivity tests for intentionally bad answers, real evaluation report | Pending |
| Long-meeting MCP reading | Clients can read notes first, then bounded transcript time ranges/pages with provenance, completeness and continuation information | Real MCP protocol tests for long/Korean data, page boundaries, no omissions/duplication, bundle reproduction and deployment | Pending |

Each PR targets main, completes latest-head review/CI and deployment verification.
Preserve root AGENTS.md user edits. Do not use customer data as test fixtures.

Delivery evidence: PR194/195/196/197 are merged. PR197 frontend and infrastructure deployments succeeded (runs 34705112285/34705112280). The bounded-reading API adds metadata-only action reads and the terminal-state handoff polling follow-up; these still require their own merge/deployment.
