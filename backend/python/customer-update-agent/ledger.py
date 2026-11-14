"""In-code gate and budget ledger for one customer-update run (single thread)."""

SEARCHES_PER_PAIR = 5
FETCHES_PER_PAIR = 3
REWRITES_PER_PAIR = 1


class LedgerRefused(Exception):
    """A gate or budget refused the action; `code` is a short stable string."""

    def __init__(self, code):
        super().__init__(code)
        self.code = code


class RunLedger:
    def __init__(self, budgets, accounts, items=()):
        self._budgets = budgets
        self._remaining = {a.account_id: a.drafts_remaining for a in accounts}
        self._items = set(items)
        self._gates = {}
        self._taken = set()
        self._taken_by_account = {}
        self._drafted = set()
        self._discarded = set()
        self._deferred = set()
        self._searches = {}
        self._fetches = {}
        self._rewrites = {}
        self._fetched = {}

    def record_gate(self, url_hash, account_id, choice, score):
        self._gates[(url_hash, account_id)] = (choice, score)

    def require_gate_pass(self, url_hash, account_id):
        gate = self._gates.get((url_hash, account_id))
        if gate is None or gate[0] != "needed":
            raise LedgerRefused("not_passed")

    def take_research(self, url_hash, account_id):
        pair = (url_hash, account_id)
        if pair in self._taken or pair in self._drafted:
            raise LedgerRefused("already_drafted")
        if len(self._taken) >= self._budgets.max_research_pairs:
            raise LedgerRefused("research_budget")
        if len(self._taken) >= self._budgets.max_drafts:
            raise LedgerRefused("draft_budget")
        used = self._taken_by_account.get(account_id, 0)
        if used >= self._remaining.get(account_id, 0):
            raise LedgerRefused("account_budget")
        self._taken.add(pair)
        self._taken_by_account[account_id] = used + 1

    def take_search(self, pair):
        n = self._searches.get(pair, 0)
        if n >= SEARCHES_PER_PAIR:
            raise LedgerRefused("search_budget")
        self._searches[pair] = n + 1

    def take_fetch(self, pair):
        n = self._fetches.get(pair, 0)
        if n >= FETCHES_PER_PAIR:
            raise LedgerRefused("fetch_budget")
        self._fetches[pair] = n + 1

    def take_rewrite(self, pair):
        n = self._rewrites.get(pair, 0)
        if n >= REWRITES_PER_PAIR:
            return False
        self._rewrites[pair] = n + 1
        return True

    def record_draft(self, pair):
        self._drafted.add(pair)

    def record_discard(self, pair):
        self._discarded.add(pair)

    def record_defer(self, pair):
        self._deferred.add(pair)

    def add_fetched(self, pair, url):
        self._fetched.setdefault(pair, set()).add(url)

    def allowed_urls(self, pair):
        return set(self._fetched.get(pair, ()))

    def stats(self):
        return {
            "items": len(self._items),
            "gated": len(self._gates),
            "needed": sum(1 for c, _ in self._gates.values() if c == "needed"),
            "researched": len(self._taken),
            "drafted": len(self._drafted),
            "discarded": len(self._discarded),
            "deferred": len(self._deferred),
            "searches": sum(self._searches.values()),
            "fetches": sum(self._fetches.values()),
        }
