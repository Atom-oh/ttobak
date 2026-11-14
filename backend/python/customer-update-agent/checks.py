"""Deterministic draft checks. The Go port (CheckUpdateDraft) must match these rules exactly.

Rules use plain string operations only; every rule is described in one comment line.
"""

CODES = (
    "empty",
    "too_long",
    "link_not_allowed",
    "link_internal",
    "link_missing",
    "blocked_name",
    "stage_word",
    "opportunity_word",
    "figure",
    "slack_control",
    "no_meeting_offer",
)

MAX_CHARS = 2000

# Mirrors of contracts/customer-updates/draft-terms-ko-v1.json and internal-hosts-v1.json (tests compare).
OPPORTUNITY_WORDS = ("oppty", "opportunity", "오퍼튜니티", "기회 단계")
MEETING_OFFER_WORDS = ("미팅", "세션", "자리", "통화", "시간")
FIGURE_UNITS = ("원", "달러", "USD", "KRW", "%")
INTERNAL_SUFFIXES = (
    "slack.com",
    "slack-files.com",
    "amazon.dev",
    "aws.dev",
    "a2z.com",
    "corp.amazon.com",
    "w.amazon.com",
    "quip-amazon.com",
    "amazon.work",
    "awsapps.com",
    "sharepoint.com",
    "salesforce.com",
    "force.com",
    "sim.amazon.com",
    "issues.amazon.com",
    "code.amazon.com",
    "phonetool.amazon.com",
    "broadcast.amazon.com",
)

CURRENCY_SIGNS = "$₩€¥£"
KOREAN_NUMBER_UNITS = ("만", "억", "천", "조")
DIGITS = "0123456789"
# Whitespace is exactly these characters (never str.isspace/strip() defaults).
WHITESPACE = " \t\n\v\f\r 　"
URL_END_CHARS = "<>\"'"
URL_TRAIL_CHARS = ".,;:!?)"
ASCII_LETTERS = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
HOST_RUN_CHARS = ASCII_LETTERS + DIGITS + "-."
# Last label of a bare host (ASCII-lowercased) that makes it a link candidate.
BARE_HOST_TLDS = (
    "com", "net", "org", "io", "co", "kr", "ai", "dev", "app", "me", "info", "biz", "xyz", "ly",
    "to", "cc", "gg", "tv", "us", "jp", "cn", "uk", "de", "fr", "link", "site", "online", "cloud", "tech",
    "ru", "sh", "top", "do", "tk", "zip", "mov", "land", "page", "run", "sbs", "icu", "vip", "work",
    "click", "live", "shop", "store", "blog", "news", "wiki", "edu", "gov", "in", "br", "au", "ca",
    "es", "it", "nl", "se", "ch", "pl", "tw", "hk", "sg", "id", "vn", "th", "my", "ph",
)

_LOWER = {ord(c): ord(c) + 32 for c in "ABCDEFGHIJKLMNOPQRSTUVWXYZ"}


def ascii_lower(text):
    # Case-insensitive means lowercasing only ASCII A-Z; every other code point is unchanged.
    return text.translate(_LOWER)


def escape_slack(text):
    # Slack escaping: & first, then < and >; applied once by a consumer that posts through the Slack API.
    return text.replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;")


def _contains_any(haystack, needles):
    # ASCII-case-insensitive substring test; empty needles are ignored.
    h = ascii_lower(haystack)
    return any(n != "" and ascii_lower(n) in h for n in needles)


def _candidate_end(body, start):
    # A candidate ends at the first WHITESPACE char or one of <>"' (or end of body).
    e = start
    while e < len(body) and body[e] not in WHITESPACE and body[e] not in URL_END_CHARS:
        e += 1
    return e


def extract_urls(body):
    """Link candidates in body, left to right: scheme URLs, mailto:, and bare hosts."""
    out = []
    n = len(body)
    i = 0
    while i < n:
        c = body[i]
        # (a)/(b) at the start of an ASCII letter run: "<2-10 letters>://" or "mailto:" (case-insensitive).
        if c in ASCII_LETTERS and (i == 0 or body[i - 1] not in ASCII_LETTERS):
            j = i
            while j < n and body[j] in ASCII_LETTERS:
                j += 1
            if (2 <= j - i <= 10 and body.startswith("://", j)) or (
                ascii_lower(body[i:j]) == "mailto" and body.startswith(":", j)
            ):
                e = _candidate_end(body, i)
                out.append(body[i:e].rstrip(URL_TRAIL_CHARS))  # trailing .,;:!?) is stripped
                i = e
                continue
        # (c) bare host: a maximal run of ASCII letters, digits, "-" and "." (start-of-run implies no such char before).
        if c in HOST_RUN_CHARS and (i == 0 or body[i - 1] not in HOST_RUN_CHARS):
            r = i
            while r < n and body[r] in HOST_RUN_CHARS:
                r += 1
            prev = body[i - 1] if i > 0 else ""
            # Not after "@", and not after "/" unless that "/" is the second of "//".
            blocked = prev == "@" or (prev == "/" and not (i >= 2 and body[i - 2] == "/"))
            if not blocked:
                if body.startswith("/", r):
                    # An optional /path extends the candidate to the terminator; the host part is the run.
                    host_run = body[i:r]
                    e = _candidate_end(body, r)
                    cand = body[i:e].rstrip(URL_TRAIL_CHARS)
                    # Before "/": any dotted host or dotted-quad IPv4 also counts, whatever its last label.
                    is_link = _is_bare_host(host_run) or _is_dotted_host(host_run)
                else:
                    host_run = body[i:r].rstrip(".")  # trailing dots (sentence end) are not part of the host
                    cand = host_run
                    # Without a path: a listed TLD, "www.", or a whole run that is a dotted-quad IPv4.
                    is_link = _is_bare_host(host_run) or _is_ipv4(host_run)
                if is_link:
                    out.append(cand)
            i = r
            continue
        i += 1
    return out


def _is_bare_host(run):
    # Bare host: contains a dot and its last label is in BARE_HOST_TLDS, or it starts with "www." plus a label.
    if "." not in run:
        return False
    low = ascii_lower(run)
    if low.startswith("www.") and len(low) > 4:
        return True
    return low.rsplit(".", 1)[1] in BARE_HOST_TLDS


def _is_ipv4(run):
    # Dotted quad: exactly four dot-separated groups of 1-3 ASCII digits (a longer version run is not one).
    groups = run.split(".")
    return len(groups) == 4 and all(1 <= len(g) <= 3 and all(ch in DIGITS for ch in g) for g in groups)


def _is_dotted_host(run):
    # Dotted host: non-empty labels of ASCII letters, digits and "-", at least one dot, and a last label of
    # at least 2 ASCII letters; or a dotted-quad IPv4.
    if _is_ipv4(run):
        return True
    labels = run.split(".")
    if len(labels) < 2 or any(label == "" for label in labels):
        return False
    return len(labels[-1]) >= 2 and all(ch in ASCII_LETTERS for ch in labels[-1])


def url_host(url):
    # Host: drop a "<2-10 letters>://" prefix if present; cut at first of / ? #; after the last "@";
    # before the first ":"; ASCII-lowercased; one trailing "." stripped.
    rest = url
    k = url.find("://")
    if 2 <= k <= 10 and all(ch in ASCII_LETTERS for ch in url[:k]):
        rest = url[k + 3:]
    for sep in "/?#":
        rest = rest.split(sep, 1)[0]
    rest = rest.rsplit("@", 1)[-1]
    host = ascii_lower(rest.split(":", 1)[0])
    if host.endswith("."):
        host = host[:-1]
    return host


def is_internal_host(host):
    # Internal when the host equals a suffix or ends with "." + suffix.
    return any(host == s or host.endswith("." + s) for s in INTERNAL_SUFFIXES)


def has_figure(body):
    # Figure: a currency sign, one optional space, then an ASCII digit; or an ASCII digit, one optional space,
    # then a figure unit or Korean number unit (ASCII-case-insensitive). Unit before digit and fullwidth digits pass.
    text = ascii_lower(body)
    units = tuple(ascii_lower(u) for u in FIGURE_UNITS) + KOREAN_NUMBER_UNITS
    n = len(text)
    for i, ch in enumerate(text):
        if ch in CURRENCY_SIGNS:
            j = i + 1
            if j < n and text[j] == " ":
                j += 1
            if j < n and text[j] in DIGITS:
                return True
        elif ch in DIGITS:
            j = i + 1
            if j < n and text[j] == " ":
                j += 1
            if any(text.startswith(u, j) for u in units):
                return True
    return False


def check_draft(body, links, allowed_urls, block_list, stages):
    """Return failure codes in CODES order, each at most once; empty list when the draft passes."""
    found = set()
    # empty: nothing left after trimming WHITESPACE characters from both ends.
    if body.strip(WHITESPACE) == "":
        found.add("empty")
    # too_long: more than 2,000 Unicode code points.
    if len(body) > MAX_CHARS:
        found.add("too_long")
    # link_missing: the links list is empty.
    if not links:
        found.add("link_missing")
    # A links entry must be exactly an https:// URL in allowed_urls.
    for url in links:
        if not url.startswith("https://") or url not in allowed_urls:
            found.add("link_not_allowed")
    # A body candidate must be exactly an https:// URL that is in allowed_urls AND in links; anything else fails.
    candidates = extract_urls(body)
    for url in candidates:
        if not url.startswith("https://") or url not in allowed_urls or url not in links:
            found.add("link_not_allowed")
    # link_internal: any links entry or body candidate whose host is an internal host.
    for url in list(links) + candidates:
        if is_internal_host(url_host(url)):
            found.add("link_internal")
    if _contains_any(body, block_list):
        found.add("blocked_name")
    if _contains_any(body, stages):
        found.add("stage_word")
    if _contains_any(body, OPPORTUNITY_WORDS):
        found.add("opportunity_word")
    if has_figure(body):
        found.add("figure")
    # slack_control: "<!" (here/channel/everyone/subteam) or "<@" mention.
    if "<!" in body or "<@" in body:
        found.add("slack_control")
    # no_meeting_offer: none of the meeting offer words present (exact substring).
    if not any(w in body for w in MEETING_OFFER_WORDS):
        found.add("no_meeting_offer")
    return [c for c in CODES if c in found]
