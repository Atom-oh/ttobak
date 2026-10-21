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


def source_snapshot(source):
    if not isinstance(source, dict):
        raise DiscoveryRejected("INVALID_CONFIG")
    source_id = _text(source.get("sourceId"), 128, empty=False)
    if not _IDENTIFIER.fullmatch(source_id) or source_id.startswith("__"):
        raise DiscoveryRejected("INVALID_CONFIG")
    urls = source.get("customUrls") or []
    if not isinstance(urls, list) or len(urls) > MAX_CUSTOM_URLS:
        raise DiscoveryRejected("INVALID_CONFIG")
    # Invalid individual custom URLs are counted and skipped during discovery.
    # Their exact bounded values remain in the fingerprint for revalidation.
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

    gateway(query, max_results) returns (list, error). The transport owns its
    request timeout; this staged orchestration does not install one implicitly.
    published_lookup(source_id, hashes) must return known published hashes or
    raise on incomplete/unprocessed reads. No missing-state fallback is allowed.
    """
    config = source_snapshot(source)
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
        "staleRejected": 0, "duplicateRejected": 0, "budgetRejected": 0,
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
        queries = pipeline.expand_queries(
            config["sourceName"], config["aliases"], config["newsQueries"],
            now.date() - timedelta(days=freshness))
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
                values, error = gateway(query.text, MAX_RESULTS_PER_QUERY)
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
                    title = _text(item.get("title", ""), 500)
                    published = _text(item.get("publishedDate", ""), 100)
                except (ValueError, DiscoveryRejected):
                    result["invalidRejected"] += 1
                    continue
                if not pipeline.is_within_days(published, freshness, now):
                    result["staleRejected"] += 1
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
        if len(result["candidates"]) >= max_candidates:
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
