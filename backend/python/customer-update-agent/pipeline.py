"""Pair pipeline: research -> write -> code checks -> review, with at most one rewrite.

Only LedgerRefused("not_passed") propagates; every other failure becomes a
PairResult. Discard reasons are stable codes, never model prose. Logs carry
runId, urlHash[:12] and codes only.
"""

import logging
from dataclasses import dataclass, replace
from typing import Callable, Optional

import checks
import llm
import researcher
import reviewer
import writer
from ledger import LedgerRefused

logger = logging.getLogger("customer-update-agent")

DRAFTED, DISCARDED, DEFERRED = "drafted", "discarded", "deferred"
WHY_INVALID = "why_invalid"
WRITER_ERROR = "writer_error"
PIPELINE_ERROR = "pipeline_error"
REVIEW_REJECTED = "review_rejected"
# Body-only rules that do not apply to the SA-facing `why` note.
_WHY_SKIPPED = frozenset({"empty", "too_long", "link_missing", "no_meeting_offer"})


@dataclass(frozen=True)
class Deps:
    ask_research: Callable  # (prompt, researcher.ResearchTools) -> dict
    ask_write: Callable  # (prompt, required) -> dict
    ask_review: Callable  # (prompt, required) -> dict
    web_search: Callable
    fetch_public: Callable
    run_id: str


@dataclass(frozen=True)
class PairResult:
    outcome: str
    draft: Optional[writer.Draft]
    discard_reason: str


def _research_tool_functions(research_tools):
    """Plain functions; llm.build_stoppable_agent makes them Strands tools, and a
    call past researcher.MAX_TOOL_CALLS (AgentLimit) ends the researcher's loop."""

    def web_search(query: str) -> str:
        """Search the public web. Returns up to five results as an untrusted data block."""
        return research_tools.search(query)

    def fetch_page(url: str) -> str:
        """Fetch one https page from a search result or the article URL, as an untrusted data block."""
        return research_tools.fetch(url)

    return [web_search, fetch_page]


def build_deps(run_id):
    """Real wiring: researcher and writer use the writer model, the reviewer uses luna."""
    import tools

    writer_agent = llm.build_agent("writer", writer.SYSTEM_PROMPT)
    reviewer_agent = llm.build_agent("reviewer", reviewer.SYSTEM_PROMPT)

    def ask_research(prompt, research_tools):
        agent = llm.build_stoppable_agent("researcher", researcher.SYSTEM_PROMPT,
                                          _research_tool_functions(research_tools))
        return llm.ask_json(agent, prompt, set(researcher.REQUIRED))

    return Deps(
        ask_research=ask_research,
        ask_write=lambda prompt, required: llm.ask_json(writer_agent, prompt, required),
        ask_review=lambda prompt, required: llm.ask_json(reviewer_agent, prompt, required),
        web_search=tools.web_search,
        fetch_public=tools.fetch_public,
        run_id=run_id,
    )


def _code_failures(draft, account, allowed):
    stages = [o.stage for o in account.opportunities if o.stage]
    codes = set(checks.check_draft(draft.body, list(draft.links), allowed_urls=allowed,
                                   block_list=list(account.block_list), stages=stages))
    if not draft.why.strip(checks.WHITESPACE) or len(draft.why) > writer.MAX_WHY:
        codes.add(WHY_INVALID)
    else:
        why_codes = checks.check_draft(draft.why, list(draft.links), allowed_urls=allowed,
                                       block_list=list(account.block_list), stages=stages)
        codes.update(c for c in why_codes if c not in _WHY_SKIPPED)
    order = list(checks.CODES) + [WHY_INVALID]
    return [c for c in order if c in codes]


def _attempt(content, account, research, feedback, allowed, deps):
    """Returns (draft | None, failure codes, feedback for a rewrite)."""
    try:
        draft = writer.write(content, account, research, feedback, deps.ask_write)
    except llm.ModelOutputError:
        return None, [WRITER_ERROR], "writer_error: reply with valid JSON in the required shape"
    codes = _code_failures(draft, account, allowed)
    if codes:
        return None, codes, "failed checks: " + ", ".join(codes)
    verdict = reviewer.review(draft, content, research, account, deps.ask_review)
    if verdict.ok:
        return draft, [], ""
    code = reviewer.REVIEW_ERROR if verdict.reason == reviewer.REVIEW_ERROR else REVIEW_REJECTED
    return None, ["review:" + code], "reviewer: " + verdict.reason


def _run(content, account, ledger, pair, deps):
    ledger.add_fetched(pair, content.url)  # the article itself is always an allowed link
    research = researcher.research(content, account, ledger, pair, deps.ask_research,
                                   deps.web_search, deps.fetch_public)
    allowed = ledger.allowed_urls(pair)
    feedback = None
    while True:
        draft, codes, feedback = _attempt(content, account, research, feedback, allowed, deps)
        if draft is not None:
            ledger.record_draft(pair)
            return PairResult(DRAFTED, replace(
                draft, source_url=content.url, source_title=content.title,
                opportunity_ids=tuple(o.project_id for o in account.opportunities),
                fetched_urls=tuple(sorted(allowed))), "")
        if not ledger.take_rewrite(pair):
            ledger.record_discard(pair)
            return PairResult(DISCARDED, None, ",".join(codes))


def draft_pair(content, account, decision, ledger, deps):
    """Draft one gated pair. `decision` is the gate's PairDecision (the ledger holds the pass).

    Raises LedgerRefused("not_passed") before any model call when the gate did not pass.
    """
    pair = (content.url_hash, account.account_id)
    ledger.require_gate_pass(*pair)
    try:
        ledger.take_research(*pair)
    except LedgerRefused as refused:
        if refused.code == "already_drafted":
            return PairResult(DISCARDED, None, "already_drafted")
        ledger.record_defer(pair)
        logger.info("pair deferred run=%s url=%s reason=%s", deps.run_id, pair[0][:12],
                    refused.code)
        return PairResult(DEFERRED, None, refused.code)
    try:
        result = _run(content, account, ledger, pair, deps)
    except Exception as error:
        logger.warning("pair failed run=%s url=%s type=%s", deps.run_id, pair[0][:12],
                       type(error).__name__)
        ledger.record_discard(pair)
        return PairResult(DISCARDED, None, PIPELINE_ERROR)
    logger.info("pair %s run=%s url=%s reason=%s", result.outcome, deps.run_id, pair[0][:12],
                result.discard_reason or "-")
    return result
