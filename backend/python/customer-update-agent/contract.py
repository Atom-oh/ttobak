"""Parser for the run-v1 payload (contracts/customer-updates/run-v1.json)."""
import json
import re
from dataclasses import dataclass, field
from urllib.parse import urlsplit

MAX_URL_CHARS = 2048

MAX_ITEMS = 30
MAX_ACCOUNTS = 40
MAX_ACCOUNT_BYTES = 8000
MAX_RESEARCH_PAIRS = 10
MAX_DRAFTS = 30

_RUN_ID = re.compile(r"^[0-9a-f]{32}$")
_URL_HASH = re.compile(r"^[0-9a-f]{64}$")
_ACCOUNT_ID = re.compile(r"^[A-Za-z0-9_-]{1,64}$")
_TOP_KEYS = {"version", "runId", "items", "accounts", "budgets"}
_ACCOUNT_KEYS = {
    "accountId", "name", "services", "opportunities", "news", "insights", "notes", "blockList", "draftsRemaining",
}


class ContractError(Exception):
    """Payload rejected; `code` is a short stable string."""

    def __init__(self, code):
        super().__init__(code)
        self.code = code


@dataclass(frozen=True)
class Item:
    url_hash: str
    url: str
    # Accounts whose pair with this item already has a final decision.
    decided_accounts: frozenset = field(default_factory=frozenset)


@dataclass(frozen=True)
class Opportunity:
    project_id: str
    name: str
    stage: str


@dataclass(frozen=True)
class AccountPack:
    account_id: str
    name: str
    services: tuple
    opportunities: tuple
    news: tuple
    insights: tuple
    notes: tuple
    block_list: tuple
    drafts_remaining: int


@dataclass(frozen=True)
class Budgets:
    max_research_pairs: int
    max_drafts: int


@dataclass(frozen=True)
class RunInput:
    run_id: str
    items: tuple
    accounts: tuple
    budgets: Budgets


def _is_int(v):
    return isinstance(v, int) and not isinstance(v, bool)


def _str_list(v):
    if v is None:
        return ()
    if not isinstance(v, list) or not all(isinstance(s, str) for s in v):
        raise ContractError("shape")
    return tuple(v)


def _dict_list(v, keys):
    """List of dicts, each with string fields `keys`; returns tuple of tuples."""
    if v is None:
        return ()
    if not isinstance(v, list):
        raise ContractError("shape")
    out = []
    for d in v:
        if not isinstance(d, dict) or not all(isinstance(d.get(k), str) for k in keys):
            raise ContractError("shape")
        out.append(tuple(d[k] for k in keys))
    return tuple(out)


def _parse_item(raw):
    if not isinstance(raw, dict):
        raise ContractError("shape")
    h, url = raw.get("urlHash"), raw.get("url")
    if not isinstance(h, str) or not _URL_HASH.fullmatch(h):
        raise ContractError("item_hash")
    if not _valid_url(url):
        raise ContractError("item_url")
    # Optional; each id must name a payload account (checked in parse_run), so
    # unique ids also cap the list at the number of accounts.
    decided = raw.get("decidedAccounts", [])
    if not isinstance(decided, list) or not all(isinstance(a, str) for a in decided):
        raise ContractError("shape")
    if len(set(decided)) != len(decided):
        raise ContractError("duplicate")
    return Item(h, url, frozenset(decided))


def _valid_url(url):
    if not isinstance(url, str) or not url.startswith("https://") or len(url) > MAX_URL_CHARS:
        return False
    if any(c.isspace() or ord(c) < 32 or ord(c) == 127 for c in url):
        return False
    try:
        return bool(urlsplit(url).hostname)
    except ValueError:
        return False


def _parse_account(raw):
    if not isinstance(raw, dict):
        raise ContractError("shape")
    aid = raw.get("accountId")
    if not isinstance(aid, str) or not _ACCOUNT_ID.fullmatch(aid):
        raise ContractError("account_id")
    try:
        size = len(json.dumps(raw, ensure_ascii=False, separators=(",", ":")).encode("utf-8"))
    except (TypeError, ValueError, RecursionError):
        raise ContractError("shape") from None
    if size > MAX_ACCOUNT_BYTES:
        raise ContractError("account_size")
    if set(raw) - _ACCOUNT_KEYS:
        raise ContractError("shape")
    name = raw.get("name")
    remaining = raw.get("draftsRemaining")
    if not isinstance(name, str) or not _is_int(remaining) or remaining < 0:
        raise ContractError("shape")
    return AccountPack(
        account_id=aid,
        name=name,
        services=_str_list(raw.get("services")),
        opportunities=tuple(
            Opportunity(*t) for t in _dict_list(raw.get("opportunities"), ("projectId", "name", "stage"))
        ),
        news=_dict_list(raw.get("news"), ("title", "summary")),
        insights=_dict_list(raw.get("insights"), ("type", "text")),
        notes=_str_list(raw.get("notes")),
        block_list=_str_list(raw.get("blockList")),
        drafts_remaining=remaining,
    )


def _parse_budgets(raw):
    if raw is None:
        return Budgets(MAX_RESEARCH_PAIRS, MAX_DRAFTS)
    if not isinstance(raw, dict):
        raise ContractError("budgets")
    pairs = raw.get("maxResearchPairs", MAX_RESEARCH_PAIRS)
    drafts = raw.get("maxDrafts", MAX_DRAFTS)
    if set(raw) - {"maxResearchPairs", "maxDrafts"}:
        raise ContractError("budgets")
    if not (_is_int(pairs) and 0 <= pairs <= MAX_RESEARCH_PAIRS):
        raise ContractError("budgets")
    if not (_is_int(drafts) and 0 <= drafts <= MAX_DRAFTS):
        raise ContractError("budgets")
    return Budgets(pairs, drafts)


def parse_run(payload):
    if not isinstance(payload, dict):
        raise ContractError("shape")
    if set(payload) - _TOP_KEYS:
        raise ContractError("shape")
    version = payload.get("version")
    if not _is_int(version) or version != 1:
        raise ContractError("version")
    run_id = payload.get("runId")
    if not isinstance(run_id, str) or not _RUN_ID.fullmatch(run_id):
        raise ContractError("run_id")
    raw_items = payload.get("items")
    if not isinstance(raw_items, list) or not 1 <= len(raw_items) <= MAX_ITEMS:
        raise ContractError("items")
    raw_accounts = payload.get("accounts")
    if not isinstance(raw_accounts, list) or not 1 <= len(raw_accounts) <= MAX_ACCOUNTS:
        raise ContractError("accounts")
    items = tuple(_parse_item(i) for i in raw_items)
    accounts = tuple(_parse_account(a) for a in raw_accounts)
    if len({i.url_hash for i in items}) != len(items):
        raise ContractError("duplicate")
    if len({a.account_id for a in accounts}) != len(accounts):
        raise ContractError("duplicate")
    known = {a.account_id for a in accounts}
    if any(not i.decided_accounts <= known for i in items):
        raise ContractError("decided_accounts")
    return RunInput(run_id, items, accounts, _parse_budgets(payload.get("budgets")))
