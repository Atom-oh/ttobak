"""Published-text verifier for the area agents (agents.py): typed tokens of a text (money, percent, count,
date, calendar, identifier, bare number) must each match a value in the index built from exactly the input
the agent saw. agents.py re-exports every name here.
"""
import re

SIGN = r"([+\-−]?)"
NUM = r"(\d[\d,]*(?:\.\d+)?)"
# lookarounds, not \b: Korean particles are word characters ("YYYY-MM-DD에")
RE_DATE = re.compile(r"(?<![\d-])\d{4}-\d{2}(?:-\d{2})?(?![\d-])")
# glued or spaced ("1.2 억"): a Korean scale word is never folded into the amount
RE_KMONEY = re.compile(r"\d\s?(?:천|만|억)(?!료)")
# money in any other form than $ ("52달러", "30,100원", "USD 84", "₩"): not typed as money, so a percent or count
# would match it as a bare number; rejected (RULES ask for $ only)
RE_CURWORD = re.compile(r"\d\s?(?:달러|원(?!인|칙)|usd|krw|eur|jpy|dollars?)(?![A-Za-z])|(?<![A-Za-z])(?:usd|krw|eur|jpy|us\$)\s?\d|[₩€£¥]",
                        re.I)
RE_MONEY = re.compile(SIGN + r"\$" + NUM + r"([A-Za-z]*)")
# a Latin scale word after a space ("$1.2 million", "$1.2 K", "1.2 billion"): only a glued K/M/B is folded, so the
# amount alone would match a source value 10^3-10^12 times smaller; rejected
SCALE_WORDS = r"(?:thousand|million|billion|trillion)"
RE_SCALEWORD = re.compile(r"(?:\$" + NUM + r"[A-Za-z]*\s+(?:[kmbt]|mm|mn|bn|" + SCALE_WORDS + r")|\d\s*" + SCALE_WORDS + r")(?![A-Za-z0-9])",
                          re.I)
# a spelled-out percent ("25퍼센트", "25프로", "3포인트", "25 percent", "25％") is a percent, typed like "%": left to the
# bare-number pass it would verify against any unrelated number in the input (a count of 25 recs)
PCT_UNIT = (r"(?:[%％]p?|퍼센트(?:\s?포인트)?|포인트|프로(?!젝|그|비|모|파|세|덕|토|필|듀|미|시|페|포|브|스)"
            r"|(?i:percent(?:age)?(?:\s?points?)?|pct|pp)(?![A-Za-z]))")
RE_PCT = re.compile(SIGN + NUM + r"\s?" + PCT_UNIT)
# a percent or money range ("20–30%", "20~30 퍼센트", "$1.2–1.5M"): the unit is written once, on the second endpoint,
# so both endpoints are typed by it; left to the single-token passes the first endpoint would reach the bare pool
RANGE_SEP = r"\s?[~∼–—\-]\s?"
RE_PCT_RANGE = re.compile(SIGN + NUM + r"(?:\s?" + PCT_UNIT + r")?" + RANGE_SEP + SIGN + NUM + r"\s?" + PCT_UNIT)
COUNT_UNITS = "개월|건|개|대|일|년"
RE_MONEY_RANGE = re.compile(SIGN + r"\$" + NUM + r"([A-Za-z]*)" + RANGE_SEP + SIGN + r"\$?" + NUM + r"([A-Za-z]*)"
                            + r"(?![\d.,A-Za-z]|\s?(?:" + PCT_UNIT + "|" + COUNT_UNITS + "))")
RE_RANGE = re.compile(NUM + r"\s?[~∼]\s?" + NUM + r"\s?(" + COUNT_UNITS + r")")
RE_COUNT = re.compile(SIGN + NUM + r"\s?(" + COUNT_UNITS + r")")
RE_CAL = re.compile(r"(?<![\d.,])(\d{1,2})\s?(월|분기)|(?<![\d.,])(20[2-3]\d)\s?년")
# a calendar year ("2026년"), taken before durations: 년 is also a count unit ("3년 약정")
RE_CALYEAR = re.compile(r"(?<![\d.,])20[2-3]\d\s?년")
RE_IDENT = re.compile(r"[A-Za-z0-9][A-Za-z0-9._/\-]*")
RE_BARE = re.compile(SIGN + NUM)
# a plain Latin word naming a customer account/workload/cluster ("phantom 계정") must also come from the input
RE_NAMED = re.compile(r"(?<![A-Za-z0-9._-])([A-Za-z][A-Za-z0-9]*)\s?(?:계정|워크로드|클러스터)")
SCALE = {"": 1, "K": 1e3, "M": 1e6, "B": 1e9}
# generic AWS/tech identifiers that carry no customer fact
GENERIC = {"ec2", "s3", "x86", "x86_64", "arm64", "eks", "ecs", "rds", "aurora", "elasticache", "opensearch", "karpenter",
           "fargate", "lambda", "bedrock", "sagemaker", "kubernetes", "dev", "prod", "staging", "test", "sandbox", "on-demand", "multi-az", "cross-az", "cross-region", "well-architected",
           "multi-arch", "read-only", "auto-scaling", "follow-up", "end-to-end", "e-mail", "pay-as-you-go", "x86-64"}
MONEY_KEY = re.compile(r"(cost|spend|savings|impact|commit|hourly|^m[1-6]$|^last$|^prev$|^first$|^base$|^delta$|^run_rate_|upfront|net|gap|^est_|^monthly_|_avg$|_hr$|_last$|_first$|_6m$)")
PCT_KEY = re.compile(r"(pct|share|coverage|elasticity)")
COUNT_KEY = re.compile(r"^(recs|count|resources|n|expiring_n|accounts_first|accounts_last|bedrock_models_last|linked_accounts)$")


def _num(s):
    return float(s.replace(",", ""))


def _tol(s, scale=1):
    d = len(s.split(".")[1]) if "." in s else 0
    return 0.5 * 10 ** -d * scale


def _signed(sign, v):
    return -v if sign in ("-", "−") else v


def _money_range(m):
    """Both endpoints of "$1.2–1.5M" as money; a scale written on one endpoint only applies to both."""
    a, b = m.group(3), m.group(6)
    a, b = a or b, b or a
    if a not in SCALE or b not in SCALE:
        return ("bad", "money-suffix")
    return [("money", m.group(1), _num(m.group(2)) * SCALE[a], _tol(m.group(2), SCALE[a])),
            ("money", m.group(4), _num(m.group(5)) * SCALE[b], _tol(m.group(5), SCALE[b]))]


def _scan(text):
    """Typed tokens of a text, plus the leftover (digits there make a claim unparseable)."""
    toks = []
    s = text

    def take(rx, fn):
        nonlocal s
        out = []
        for m in rx.finditer(s):
            out.append(fn(m))
        s = rx.sub(lambda m: " " * len(m.group(0)), s)
        toks.extend(y for x in out if x for y in (x if isinstance(x, list) else [x]))

    if RE_KMONEY.search(s):
        toks.append(("bad", "korean-money"))
    if RE_CURWORD.search(s):
        toks.append(("bad", "currency-form"))
    if RE_SCALEWORD.search(s):
        toks.append(("bad", "money-scale"))
    take(RE_MONEY_RANGE, _money_range)
    take(RE_MONEY, lambda m: ("money", m.group(1), _num(m.group(2)) * SCALE[m.group(3)], _tol(m.group(2), SCALE[m.group(3)]))
         if m.group(3) in SCALE else ("bad", "money-suffix"))
    if "$" in s:  # a currency sign the money pattern did not take ("$ 59", "$+59")
        toks.append(("bad", "money-form"))
    take(RE_PCT_RANGE, lambda m: [("pct", m.group(1), _num(m.group(2)), _tol(m.group(2))),
                                  ("pct", m.group(3), _num(m.group(4)), _tol(m.group(4)))])
    take(RE_PCT, lambda m: ("pct", m.group(1), _num(m.group(2)), _tol(m.group(2))))
    idents = []
    for m in RE_IDENT.finditer(s):
        t = m.group(0).rstrip(".-_/")
        # with a digit (c7i.8xlarge) or a joiner (ts-playground, wl_core): names that must come from the input
        if re.search(r"[A-Za-z]", t) and (re.search(r"\d", t) or re.search(r"[A-Za-z0-9][-_.][A-Za-z0-9]", t)):
            idents.append(t)
    s = RE_IDENT.sub(lambda m: " " * len(m.group(0)) if re.search(r"[A-Za-z]", m.group(0)) else m.group(0), s)
    toks.extend(("ident", t) for t in idents)
    take(RE_DATE, lambda m: ("date", m.group(0)))
    take(RE_CALYEAR, lambda m: ("cal", m.group(0)))
    take(RE_RANGE, lambda m: ("range", m.group(3), _num(m.group(1)), _tol(m.group(1)), _num(m.group(2)), _tol(m.group(2))))
    take(RE_COUNT, lambda m: ("count", m.group(3), m.group(1), _num(m.group(2)), _tol(m.group(2))))
    take(RE_CAL, lambda m: ("cal", m.group(0)))
    take(RE_BARE, lambda m: ("bare", m.group(1), _num(m.group(2)), _tol(m.group(2))))
    return toks


def _walk(obj, key, ix):
    if isinstance(obj, dict):
        for k, v in obj.items():
            _walk(v, k, ix)
    elif isinstance(obj, list):
        for v in obj:
            _walk(v, key, ix)
    elif isinstance(obj, bool) or obj is None:
        return
    elif isinstance(obj, (int, float)):
        v = float(obj)
        ix["bare"].append((v, 0.0))
        k = key or ""
        if PCT_KEY.search(k):
            ix["pct"].append((v, 0.0))  # every *share* field in the digests is already a percent (briefing.py benchmark)
        elif MONEY_KEY.search(k) and not COUNT_KEY.match(k):  # accounts_last matches _last but is a count
            ix["money"].append((v, 0.0))
        if k == "days_left":
            ix["count"].setdefault("일", []).append((v, 0.0))
        elif COUNT_KEY.match(k) and v == int(v):
            for u in ("건", "개", "대"):
                ix["count"].setdefault(u, []).append((v, 0.0))
    elif isinstance(obj, str):
        ix["text"].append(obj.lower())
        if re.fullmatch(r"\d+", obj):
            ix["bare"].append((float(obj), 0.0))
        for t in _scan(obj):
            kind = t[0]
            if kind == "date":
                ix["date"].add(t[1])
            elif kind == "money":
                ix["money"].append((_signed(t[1], t[2]), t[3]))
            elif kind == "pct":
                ix["pct"].append((_signed(t[1], t[2]), t[3]))
            elif kind == "count":
                ix["count"].setdefault(t[1], []).append((_signed(t[2], t[3]), t[4]))
            elif kind == "range":
                ix["count"].setdefault(t[1], []).extend([(t[2], t[3]), (t[4], t[5])])
            elif kind == "ident":
                ix["ident"].add(t[1].lower())
            elif kind == "bare":
                ix["bare"].append((_signed(t[1], t[2]), t[3]))
            elif kind == "cal":
                _cal_into(t[1], ix)
        for d in ix["date"].copy():
            ix["date"].add(d[:7])
            ix["month"].add(int(d[5:7]))
            ix["year"].add(int(d[:4]))


def _cal_into(tok, ix):
    m = re.match(r"(\d+)\s?(월|분기)", tok)
    if m:
        ix["month" if m.group(2) == "월" else "quarter"].add(int(m.group(1)))
    else:
        ix["year"].add(int(tok[:4]))


def index(obj, deny=()):
    ix = {"money": [], "pct": [], "count": {}, "date": set(), "ident": set(), "bare": [], "text": [],
          "month": set(), "quarter": set(), "year": set(),
          "deny": [re.compile(r"(?<![A-Za-z0-9])" + re.escape(d.lower()) + r"(?![A-Za-z0-9])") for d in deny if d]}
    _walk(obj, None, ix)
    ix["quarter"] |= {(m - 1) // 3 + 1 for m in ix["month"]}
    ix["blob"] = "\n".join(ix["text"])
    return ix


def _match(pool, sign, v, tol):
    """pool: (value, source rounding). A claim may not be more precise than its source: "$1.9K" in the input
    supports "$1.9K" or "$2K", never "$1,900"; exact numeric fields support any rounding of themselves."""
    ok = [p for p, st in pool if st <= tol + 1e-9]
    if sign:
        sv = _signed(sign, v)
        return any(abs(sv - p) <= tol and (p >= 0) == (sv >= 0) for p in ok)
    return any(abs(abs(p) - v) <= tol for p in ok)


def verify(text, ix):
    if not isinstance(text, str):
        return False
    text = " ".join(text.split())  # consumers render whitespace-collapsed text; verify that same text
    low = text.lower()
    if any(d.search(low) for d in ix["deny"]):
        return False
    for m in RE_NAMED.finditer(text):
        w = m.group(1).lower()
        if w not in GENERIC and w not in ix["ident"] and not re.search(r"(?<![a-z0-9])" + re.escape(w) + r"(?![a-z0-9])", ix["blob"]):
            return False
    for t in _scan(text):
        kind = t[0]
        if kind == "bad":
            return False
        if kind == "date" and t[1] not in ix["date"]:
            return False
        if kind == "money" and not _match(ix["money"], t[1], t[2], t[3]):
            return False
        if kind == "pct" and not _match(ix["pct"], t[1], t[2], t[3]):
            return False
        if kind == "count" and not _match(ix["count"].get(t[1], []), t[2], t[3], t[4]):
            return False
        if kind == "range":
            pool = ix["count"].get(t[1], [])
            if not (_match(pool, "", t[2], t[3]) and _match(pool, "", t[4], t[5])):
                return False
        if kind == "cal":
            m = re.match(r"(\d+)\s?(월|분기)", t[1])
            if m and int(m.group(1)) not in ix["month" if m.group(2) == "월" else "quarter"]:
                return False
            if not m and int(t[1][:4]) not in ix["year"]:
                return False
        if kind == "ident" and t[1].lower() not in ix["ident"] and t[1].lower() not in GENERIC:
            return False
        if kind == "bare" and not _match(ix["bare"], t[1], t[2], t[3]):
            return False
    return True
