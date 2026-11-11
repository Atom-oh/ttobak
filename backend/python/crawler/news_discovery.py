"""Bounded candidate discovery; staged without a Lambda entry point.

Callers supply a current source configuration, bounded search transport, and a
strongly consistent published-identity lookup. No snippets leave this module.
Publication must separately revalidate sourceRevision against canonical config.
"""

import hashlib
import json
import re
from datetime import datetime, timedelta, timezone

import news_pipeline as pipeline

MAX_RESULTS_PER_QUERY = 25
MAX_OUTPUT_CANDIDATES = 20
MAX_OUTPUT_BYTES = 192000
MAX_CUSTOM_URLS = 20
_IDENTIFIER = re.compile(r"[A-Za-z0-9_-]{1,128}")


class DiscoveryRejected(Exception):
    def __init__(self, reason):
        super().__init__(reason)
        self.reason = reason


def _text(value, maximum, *, empty=True):
    if (not isinstance(value, str) or len(value) > maximum
            or (not empty and not value.strip())
            or any(ord(char) < 32 or ord(char) == 127 for char in value)):
        raise DiscoveryRejected("INVALID_CONFIG")
    try:
        value.encode("utf-8")
    except UnicodeError:
        raise DiscoveryRejected("INVALID_CONFIG") from None
    return value.strip()


def _terms(value):
    if not isinstance(value, list) or len(value) > 32:
        raise DiscoveryRejected("INVALID_CONFIG")
    return list(dict.fromkeys(_text(item, 200, empty=False) for item in value))


def valid_source_id(value):
    """Keep stored Unicode identities without normalization or path delimiters."""
    return (isinstance(value, str) and 1 <= len(value) <= 128
            and not value.startswith("__")
            and all(char.isalpha() or char.isdecimal() or char in "_-" for char in value))


def source_snapshot(source):
    if not isinstance(source, dict):
        raise DiscoveryRejected("INVALID_CONFIG")
    source_id = source.get("sourceId")
    if not valid_source_id(source_id):
        raise DiscoveryRejected("INVALID_CONFIG")
    urls = source.get("customUrls") or []
    if not isinstance(urls, list) or len(urls) > MAX_CUSTOM_URLS:
        raise DiscoveryRejected("INVALID_CONFIG")
    # Invalid individual custom URLs are counted and skipped during discovery.
    # Their exact bounded values remain in the fingerprint for revalidation.
    search_domains(source)
    result = {
        "sourceId": source_id,
        "sourceName": _text(source.get("sourceName", ""), 200),
        "aliases": _terms(source.get("aliases") or []),
        "newsQueries": _terms(source.get("newsQueries") or []),
        "customUrls": [_text(url, pipeline.MAX_URL_CHARS, empty=False) for url in urls],
        "status": _text(source.get("status", "active"), 32, empty=False),
        "ownerId": _text(source.get("ownerId", ""), 128),
    }
    if len(result["aliases"]) + bool(result["sourceName"]) > 32:
        raise DiscoveryRejected("INVALID_CONFIG")
    return result


def search_domains(source):
    """The operator's optional search scope, validated.

    It is deliberately outside source_snapshot/source_revision: the QA and Go
    readers share the seven-field revision contract. Scope changes are caught
    by the store's transactional config guard (CONFIG_FIELDS) and by the
    worker's recheck of each candidate against the current scope.
    """
    domains = source.get("searchDomains") if isinstance(source, dict) else None
    domains = domains or []
    if (not isinstance(domains, list) or len(domains) > pipeline.MAX_SEARCH_DOMAINS
            or not all(pipeline.valid_search_domain(domain) for domain in domains)
            or len(set(domains)) != len(domains)):
        raise DiscoveryRejected("INVALID_CONFIG")
    return tuple(domains)


def source_revision(source):
    payload = json.dumps(source_snapshot(source), ensure_ascii=False, sort_keys=True,
                         separators=(",", ":")).encode("utf-8")
    return hashlib.sha256(payload).hexdigest()


def _unpublished(source_id, candidates, lookup):
    if not candidates:
        return [], 0
    hashes = {pipeline.document_hash(item["canonicalUrl"]) for item in candidates}
    try:
        published = lookup(source_id, hashes)
    except Exception:
        raise DiscoveryRejected("STATE_UNAVAILABLE") from None
    if not isinstance(published, set) or not published <= hashes:
        raise DiscoveryRejected("STATE_UNAVAILABLE")
    kept = [item for item in candidates
            if pipeline.document_hash(item["canonicalUrl"]) not in published]
    return kept, len(candidates) - len(kept)


def discover(source, gateway, published_lookup, now, run_id, *, max_candidates=20):
    """At most two 12-query passes, 25 results/query, 300 candidates/pass.

    gateway(query, max_results, published_after=, published_before=, domains=)
    returns (list, error). The date window is the pass's freshness window and
    domains are the source's optional searchDomains; the connector applies
    both, and each result's date is still checked here. The transport owns its
    request timeout; this staged orchestration does not install one implicitly.
    published_lookup(source_id, hashes) must return known published hashes or
    raise on incomplete/unprocessed reads. No missing-state fallback is allowed.
    """
    config = source_snapshot(source)
    domains = search_domains(source)
    if (not isinstance(now, datetime) or now.tzinfo is None
            or not isinstance(run_id, str) or not _IDENTIFIER.fullmatch(run_id)
            or type(max_candidates) is not int
            or not 0 <= max_candidates <= MAX_OUTPUT_CANDIDATES):
        raise DiscoveryRejected("INVALID_REQUEST")
    now = now.astimezone(timezone.utc)
    result = {
        "sourceId": config["sourceId"], "runId": run_id,
        "sourceRevision": source_revision(config), "candidates": [],
        "freshnessDays": 7, "queryCount": 0, "invalidRejected": 0,
        "staleRejected": 0, "scopeRejected": 0, "duplicateRejected": 0, "budgetRejected": 0,
        "errors": [], "status": "READY",
    }
    if config["status"] == "disabled" or max_candidates == 0:
        result["status"] = "DISABLED" if config["status"] == "disabled" else "BUDGET_PAUSED"
        return result

    seen = set()
    candidates = []
    discovered_at = now.isoformat()
    for freshness in (7, 30):
        inspected = 0
        result["freshnessDays"] = freshness
        # No literal after: clause: the connector ignored it as an operator and
        # ranked pages about the year itself (2026-11-10: Wikipedia "2026").
        queries = pipeline.expand_queries(
            config["sourceName"], config["aliases"], config["newsQueries"], None)
        window = {"published_after": now - timedelta(days=freshness), "published_before": now,
                  "domains": domains}
        batch = []
        for query in queries:
            if inspected >= pipeline.MAX_CANDIDATES:
                break
            # The existing search transport caps query bytes/characters. Never
            # silently truncate an identity or remove the after-date suffix.
            if len(query.text) > 200:
                result["errors"].append({"code": "QUERY_TOO_LONG",
                                         "queryHash": hashlib.sha256(query.text.encode()).hexdigest()[:12]})
                continue
            result["queryCount"] += 1
            try:
                values, error = gateway(query.text, MAX_RESULTS_PER_QUERY, **window)
            except Exception:
                values, error = [], True
            if error or not isinstance(values, list):
                result["errors"].append({"code": "SEARCH_FAILED",
                                         "queryHash": hashlib.sha256(query.text.encode()).hexdigest()[:12]})
                continue
            for rank, item in enumerate(values[:MAX_RESULTS_PER_QUERY], 1):
                if inspected >= pipeline.MAX_CANDIDATES:
                    break
                inspected += 1
                if not isinstance(item, dict):
                    result["invalidRejected"] += 1
                    continue
                try:
                    url = _text(item.get("url"), pipeline.MAX_URL_CHARS, empty=False)
                    canonical = pipeline.canonicalize_url(url)
                    if not canonical.startswith("https://"):
                        raise ValueError("HTTPS_REQUIRED")
                    title = _text(item.get("title", ""), 500)
                    published = _text(item.get("publishedDate", ""), 100)
                    fresh = pipeline.is_within_days(published, freshness, now)
                except (ValueError, OverflowError, DiscoveryRejected):
                    result["invalidRejected"] += 1
                    continue
                if not fresh:
                    result["staleRejected"] += 1
                    continue
                # The connector enforces the scope remotely; an older connector
                # ignores the filter, so the allowlist is also applied here.
                if not pipeline.in_search_scope(canonical, domains):
                    result["scopeRejected"] += 1
                    continue
                if canonical in seen:
                    result["duplicateRejected"] += 1
                    continue
                seen.add(canonical)
                batch.append({
                    "runId": run_id, "candidateId": pipeline.candidate_id(config["sourceId"], canonical),
                    "kind": "news", "sourceId": config["sourceId"],
                    "sourceName": config["sourceName"], "aliases": config["aliases"],
                    "keywords": config["newsQueries"], "title": title, "url": url,
                    "canonicalUrl": canonical, "publishedAt": published,
                    "discoveredAt": discovered_at, "queryClass": query.query_class,
                    "searchResultRank": rank, "ingestSource": "search", "requireRelevance": True,
                })
        grouped = pipeline.group_near_duplicates(batch)
        kept, duplicates = _unpublished(config["sourceId"], grouped, published_lookup)
        result["duplicateRejected"] += duplicates
        candidates.extend(kept)
        if kept:
            break

    custom = []
    custom_seen = set()
    for url in config["customUrls"]:
        try:
            item = pipeline.custom_url_candidate(config["sourceId"], config["sourceName"], url, run_id)
            if not item["canonicalUrl"].startswith("https://"):
                raise ValueError("HTTPS_REQUIRED")
        except ValueError:
            result["invalidRejected"] += 1
            continue
        if item["canonicalUrl"] in custom_seen:
            result["duplicateRejected"] += 1
            continue
        custom_seen.add(item["canonicalUrl"])
        candidates = [entry for entry in candidates
                      if entry["canonicalUrl"] != item["canonicalUrl"]]
        item["discoveredAt"] = discovered_at
        # A long URL must not become an over-limit synthesis title.
        item["title"] = ""
        custom.append(item)
    custom, duplicates = _unpublished(config["sourceId"], custom, published_lookup)
    result["duplicateRejected"] += duplicates
    candidates = custom + candidates  # Explicit requests receive the bounded slots first.
    for item in candidates:
        item["corroboratingSources"] = item.get("corroboratingSources", [])[:2]
        item["sourceRevision"] = result["sourceRevision"]
        item["freshnessDays"] = result["freshnessDays"]
        # Match the worker's complete candidate limit, including UTF-8 metadata.
        if (len(result["candidates"]) >= max_candidates
                or len(json.dumps(item, ensure_ascii=False).encode("utf-8")) > 64000):
            result["budgetRejected"] += 1
            continue
        result["candidates"].append(item)
        if len(json.dumps(result, ensure_ascii=False).encode("utf-8")) > MAX_OUTPUT_BYTES - 1024:
            result["candidates"].pop()
            result["budgetRejected"] += 1
    if result["errors"]:
        result["status"] = "PARTIAL" if result["candidates"] else "SEARCH_FAILED"
    if len(json.dumps(result, ensure_ascii=False).encode("utf-8")) > MAX_OUTPUT_BYTES:
        raise DiscoveryRejected("OUTPUT_TOO_LARGE")
    return result
