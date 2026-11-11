"""Candidate contract, query expansion, canonicalization, and
near-duplicate grouping for the knowledge-grade news pipeline.

Pure functions only -- no boto3, no network, no filesystem. Staged for a
future discovery and publication worker to share URL/hash identity.

Search snippets are discovery signals only. Group output forwards only
candidate contract fields and reconstructs corroborating URLs.
"""

import hashlib
import ipaddress
import re
from dataclasses import dataclass
from datetime import date, datetime, timedelta, timezone
from urllib.parse import parse_qsl, urlencode, urlsplit, urlunsplit

MAX_QUERIES_PER_SOURCE = 12
MAX_URL_CHARS = 4096
MAX_CANDIDATES = 300
TRACKING_PARAMS = {"gclid", "fbclid", "msclkid"}
_CANDIDATE_FIELDS = frozenset({
    "runId", "candidateId", "kind", "sourceId", "sourceName", "aliases", "keywords",
    "title", "url", "canonicalUrl", "publishedAt", "discoveredAt", "queryClass",
    "searchResultRank", "ingestSource", "requireRelevance",
})

# Jaccard similarity threshold (whitespace-tokenized title) for two
# candidates to be considered the same underlying event.
NEAR_DUP_JACCARD_THRESHOLD = 0.75
NEAR_DUP_MAX_DAYS_APART = 7

_DEFAULT_PORTS = {"http": 80, "https": 443}

# Fixed query-class vocabulary combined with each identity anchor
# (source name + alias) to diversify search coverage beyond a bare name
# search. "identity" (bare anchor) is handled separately, first.
_QUERY_CLASS_TERMS = {
    "investment": "투자",
    "partnership": "파트너십",
    "regulatory": "규제",
    "expansion": "확대",
}


@dataclass(frozen=True)
class SearchQuery:
    query_class: str
    text: str


# ---------------------------------------------------------------------------
# URL canonicalization + content identity
# ---------------------------------------------------------------------------

def canonicalize_url(url: str) -> str:
    """Normalize a candidate/article URL to a stable identity string:
    lowercase scheme/host, default port removed, fragment removed,
    tracking params (utm_* plus TRACKING_PARAMS) removed, remaining query
    pairs sorted. Raises ValueError for a non-HTTP(S) scheme or missing
    host so a bad URL fails the candidate outright rather than silently
    producing a garbage identity."""
    if not isinstance(url, str) or not url or len(url) > MAX_URL_CHARS:
        raise ValueError("URL must be a non-empty string")
    if any(ord(c) <= 32 or ord(c) == 127 for c in url) or "\\" in url:
        raise ValueError("URL contains whitespace or control characters")
    parts = urlsplit(url)
    scheme = parts.scheme.lower()
    if scheme not in ("http", "https"):
        raise ValueError(f"Unsupported URL scheme: {scheme!r}")
    hostname = parts.hostname
    if not hostname or parts.username is not None or parts.password is not None:
        raise ValueError("URL has no host")
    if "%" in hostname:
        raise ValueError("Encoded hosts and IPv6 zone identifiers are unsupported")
    try:
        address = ipaddress.ip_address(hostname)
    except ValueError:
        hostname = hostname.encode("idna").decode("ascii").lower().rstrip(".")
        if not hostname or len(hostname) > 253:
            raise ValueError("Invalid hostname")
    else:
        hostname = f"[{address.compressed}]" if address.version == 6 else str(address)
    port = parts.port
    if port == 0:
        raise ValueError("Invalid port")
    netloc = hostname if port in (None, _DEFAULT_PORTS[scheme]) else f"{hostname}:{port}"
    query_pairs = sorted(
        (k, v) for k, v in parse_qsl(parts.query, keep_blank_values=True,
                                    max_num_fields=100, errors="strict")
        if not k.lower().startswith("utm_") and k.lower() not in TRACKING_PARAMS
    )
    query = urlencode(query_pairs)
    return urlunsplit((scheme, netloc, parts.path or "/", query, ""))


def document_hash(canonical_url: str) -> str:
    """Content-identity hash for a canonicalized URL. Namespaced with
    'news:' for the same reason the legacy crawler's _make_hash was: keeps
    this identity space distinct from any other kind's hash space sharing
    the same function in the future."""
    return hashlib.sha256(f"news:{canonical_url}".encode("utf-8")).hexdigest()[:16]


def candidate_id(source_id: str, canonical_url: str) -> str:
    """Pre-fetch candidate identity: sha256(sourceId + canonicalUrl), per
    the design's candidate contract. Distinct from document_hash, which is
    the post-fetch published-document identity (url-only, not source-scoped
    since DynamoDB already partitions by CRAWLER#{sourceId})."""
    return hashlib.sha256(f"{source_id}:{canonical_url}".encode("utf-8")).hexdigest()


MAX_SEARCH_DOMAINS = 100
_DOMAIN_LABEL = re.compile(r"[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?")


def valid_search_domain(value) -> bool:
    """A lowercase ASCII registrable host for the search domain filter: no
    scheme, port, path, wildcard, trailing dot or IP literal."""
    if not isinstance(value, str) or not 3 <= len(value) <= 253:
        return False
    labels = value.split(".")
    if len(labels) < 2 or labels[-1].isdigit():
        return False
    return all(_DOMAIN_LABEL.fullmatch(label) for label in labels)


def in_search_scope(url, domains) -> bool:
    """True without an allowlist, else when the canonical host is a listed
    domain or one of its subdomains (the connector's include semantics)."""
    if not domains:
        return True
    host = urlsplit(canonicalize_url(url)).hostname or ""
    return any(host == domain or host.endswith("." + domain) for domain in domains)


# ---------------------------------------------------------------------------
# Query expansion
# ---------------------------------------------------------------------------

def expand_queries(source_name: str, aliases, keywords, after_date: date) -> list:
    """Build up to MAX_QUERIES_PER_SOURCE search queries from public source
    configuration only (name, aliases, keywords) -- never from meeting
    transcripts, project data, or action items. Each query anchors on a
    quoted identity (source name or alias). A non-None after_date appends a
    literal `after:` clause; the connector does not interpret it (it matched
    pages about the year itself), so the publication workflow passes None and
    bounds freshness with the connector's publishedDateFilter instead."""
    def public_terms(values):
        if not isinstance(values, (list, tuple)) or len(values) > 32:
            raise ValueError("Too many public source terms")
        terms = []
        for value in values:
            if not isinstance(value, str) or len(value) > 200:
                raise ValueError("Invalid public source term")
            value = " ".join(value.replace('"', " ").split())
            if value and value not in terms:
                terms.append(value)
        return terms

    anchors = public_terms(([source_name] if source_name else []) + list(aliases or []))
    keywords = public_terms(keywords or [])
    after_clause = f"after:{after_date.isoformat()}" if after_date else ""

    queries = []
    seen_text = set()

    def add(query_class: str, text: str) -> bool:
        text = text.strip()
        if not text or text in seen_text or len(queries) >= MAX_QUERIES_PER_SOURCE:
            return False
        seen_text.add(text)
        queries.append(SearchQuery(query_class=query_class, text=text))
        return True

    if not anchors:
        # Keyword-only source config (no name/alias configured) -- use
        # each keyword standalone rather than producing zero queries.
        for kw in (keywords or []):
            add("keyword", f'"{kw}" {after_clause}')
            if len(queries) >= MAX_QUERIES_PER_SOURCE:
                break
        return queries

    for anchor in anchors:
        add("identity", f'"{anchor}" {after_clause}')

    for anchor in anchors:
        for kw in (keywords or []):
            if len(queries) >= MAX_QUERIES_PER_SOURCE:
                break
            add("keyword", f'"{anchor}" {kw} {after_clause}')

    for anchor in anchors:
        for query_class, term in _QUERY_CLASS_TERMS.items():
            if len(queries) >= MAX_QUERIES_PER_SOURCE:
                break
            add(query_class, f'"{anchor}" {term} {after_clause}')

    return queries[:MAX_QUERIES_PER_SOURCE]


# ---------------------------------------------------------------------------
# Freshness
# ---------------------------------------------------------------------------

# The web-search gateway's publishedDate form, e.g.
# "12:44AM, Friday, July 24 2026, PDT". The weekday is not checked.
_GATEWAY_DATE = re.compile(
    r"(\d{1,2}):(\d{2})(AM|PM), [A-Za-z]+, ([A-Za-z]+) (\d{1,2}) (\d{4}), ([A-Z]{2,4})")
_MONTHS = {name: i for i, name in enumerate(
    ("January", "February", "March", "April", "May", "June", "July", "August",
     "September", "October", "November", "December"), 1)}
# Only abbreviations with one unambiguous offset; any other fails closed.
_ZONE_OFFSETS = {"UTC": 0, "GMT": 0, "PST": -8, "PDT": -7, "MST": -7, "MDT": -6,
                 "CST": -6, "CDT": -5, "EST": -5, "EDT": -4, "KST": 9, "JST": 9}


def _parse_gateway_date(text):
    match = _GATEWAY_DATE.fullmatch(text)
    if not match:
        return None
    hour, minute, meridiem, month, day, year, zone = match.groups()
    hour, minute = int(hour), int(minute)
    if month not in _MONTHS or zone not in _ZONE_OFFSETS or not 1 <= hour <= 12 or minute > 59:
        return None
    hour = hour % 12 + (12 if meridiem == "PM" else 0)
    try:
        local = datetime(int(year), _MONTHS[month], int(day), hour, minute,
                         tzinfo=timezone(timedelta(hours=_ZONE_OFFSETS[zone])))
        return local.astimezone(timezone.utc)
    except (ValueError, OverflowError):
        return None


def parse_published_at(value):
    """Parse an ISO-8601 timestamp (with or without a 'Z' suffix) or the
    web-search gateway's "hh:mmAM, Weekday, Month DD YYYY, TZ" form to an
    aware UTC datetime. Returns None on any unparseable input (including
    the gateway's "unknown") -- callers must treat that as "freshness
    unknown", not "fresh"."""
    if not value or not isinstance(value, str):
        return None
    text = value.strip()
    if text[:1].isdigit() and ("AM, " in text or "PM, " in text):
        return _parse_gateway_date(text)
    if text.endswith("Z"):
        text = text[:-1] + "+00:00"
    try:
        parsed = datetime.fromisoformat(text)
        if parsed.tzinfo is None:
            parsed = parsed.replace(tzinfo=timezone.utc)
        return parsed.astimezone(timezone.utc)
    except (ValueError, OverflowError):
        return None


def is_within_days(published_at, days: int, now: datetime) -> bool:
    """True if published_at is parseable and within `days` before `now`
    (inclusive). Fails closed for future dates and
    (returns False) for an unparseable timestamp -- an item we can't date
    is not treated as fresh."""
    parsed = parse_published_at(published_at)
    if parsed is None:
        return False
    if now.tzinfo is None:
        now = now.replace(tzinfo=timezone.utc)
    return timedelta(0) <= now - parsed <= timedelta(days=days)


# ---------------------------------------------------------------------------
# Near-duplicate grouping
# ---------------------------------------------------------------------------

def _tokenize_title(title: str) -> set:
    return {t for t in (title or "").split() if t}


def _jaccard(a: set, b: set) -> float:
    if not a and not b:
        return 0.0
    union = a | b
    if not union:
        return 0.0
    return len(a & b) / len(union)


def _alias_set(cand: dict) -> set:
    """The candidate's own organization-identity keys (sourceId,
    sourceName, configured aliases), lowercased -- two candidates from the
    same source always share at least sourceId, so grouping is scoped to
    "same configured organization" even when aliases weren't set on either
    item."""
    values = [cand.get("sourceId", ""), cand.get("sourceName", "")]
    values.extend(cand.get("aliases") or [])
    return {v.strip().lower() for v in values if v}


def group_near_duplicates(candidates: list) -> list:
    """Merge candidates describing the same underlying event into one
    group per event: title-token Jaccard similarity >= threshold, a shared
    configured organization alias, and publication dates no more than
    NEAR_DUP_MAX_DAYS_APART apart. Returns one dict per group: the lead
    candidate's fields plus `eventGroupId` and `corroboratingSources` (the
    other group members, url only)."""
    if len(candidates) > MAX_CANDIDATES:
        raise ValueError("Too many news candidates")
    valid = []
    for candidate in candidates:
        try:
            if not isinstance(candidate, dict):
                continue
            canonical = canonicalize_url(candidate.get("url"))
            if any(not isinstance(candidate.get(key, ""), str)
                   or len(candidate.get(key, "")) > maximum
                   for key, maximum in (("sourceId", 128), ("sourceName", 200), ("title", 500))):
                continue
            aliases = candidate.get("aliases") or []
            if (not isinstance(aliases, list) or len(aliases) > 32
                    or any(not isinstance(value, str) or len(value) > 200 for value in aliases)):
                continue
            valid.append({**candidate, "canonicalUrl": canonical})
        except (ValueError, UnicodeError):
            continue
    candidates = valid
    used = [False] * len(candidates)
    groups = []

    for i, lead in enumerate(candidates):
        if used[i]:
            continue
        used[i] = True
        members = [lead]
        lead_tokens = _tokenize_title(lead.get("title", ""))
        lead_aliases = _alias_set(lead)
        lead_pub = parse_published_at(lead.get("publishedAt"))

        for j in range(i + 1, len(candidates)):
            if used[j]:
                continue
            other = candidates[j]
            if not lead.get("sourceId") or lead.get("sourceId") != other.get("sourceId"):
                continue
            if lead_aliases.isdisjoint(_alias_set(other)):
                continue
            if _jaccard(lead_tokens, _tokenize_title(other.get("title", ""))) < NEAR_DUP_JACCARD_THRESHOLD:
                continue
            other_pub = parse_published_at(other.get("publishedAt"))
            if (not lead_pub or not other_pub
                    or abs(lead_pub - other_pub) > timedelta(days=NEAR_DUP_MAX_DAYS_APART)):
                continue
            used[j] = True
            members.append(other)

        sorted_urls = sorted(canonicalize_url(m["url"]) for m in members if m.get("url"))
        event_group_id = hashlib.sha256(
            (lead.get("sourceId", "") + "|" + "|".join(sorted_urls)).encode("utf-8")
        ).hexdigest()[:24]

        merged = {key: value for key, value in lead.items() if key in _CANDIDATE_FIELDS}
        merged["eventGroupId"] = event_group_id
        merged["corroboratingSources"] = [
            {"url": m.get("url", ""), "sourceUrl": m.get("url", "")} for m in members[1:]
        ]
        groups.append(merged)

    return groups


# ---------------------------------------------------------------------------
# Custom URL candidates
# ---------------------------------------------------------------------------

def custom_url_candidate(source_id: str, source_name: str, url: str, run_id: str) -> dict:
    """An explicit, user-curated URL bypasses the relevance gate (it's an
    ingest request, not a search result to be judged) but not the fetch or
    quality gate -- requireRelevance=False only."""
    canonical_url = canonicalize_url(url)
    return {
        "runId": run_id,
        "candidateId": candidate_id(source_id, canonical_url),
        "kind": "news",
        "sourceId": source_id,
        "sourceName": source_name,
        "aliases": [],
        "keywords": [],
        "title": url,
        "url": url,
        "canonicalUrl": canonical_url,
        "publishedAt": None,
        "discoveredAt": datetime.now(timezone.utc).isoformat(),
        "queryClass": "custom",
        "searchResultRank": 0,
        "ingestSource": "custom",
        "requireRelevance": False,
        "corroboratingSources": [],
    }
