---
name: sync-docs
description: Align concise English documentation and generated review context with the current code revision.
---

# Documentation sync

Read `docs/README.md` and the root guide. All project documentation, including
ADRs/templates, is English-only; do not generate bilingual copies.

1. Read changed code, manifests, tests and relevant current references.
2. Update API, infra, UI or architecture facts in their owning document. Preserve
   policy requirements and explicitly distinguish existing gaps from compliance.
3. Keep plans, research, audits and benchmarks historical. Preserve useful
   rationale/results and name superseding ADRs; remove obsolete copied code.
4. Edit canonical `CLAUDE.md`, then run
   `python3 scripts/docs/sync_review_context.py` when review guidance changes.
   Do not independently edit generated `AGENTS.md` or duplicate per-model rules.
5. Run `python3 scripts/docs/check_docs.py`. For prompt changes, also run the
   review-context tests in `docs/runbooks/pr-review.md`.
6. Report actual changes and verification. Do not invent deployment state,
   compliance scores, test execution or live-model review results.
