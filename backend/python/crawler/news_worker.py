"""Staged per-candidate worker with immutable objects and conditional publication.

No Lambda entry point: deployment must supply bounded SDK clients, verified
pricing, a live remaining-time callback, and deployed publication-aware readers.
"""

import base64
from dataclasses import dataclass
from datetime import datetime, timezone
from decimal import Decimal, ROUND_CEILING
import hashlib
import json
import re
import uuid

from botocore.exceptions import ClientError

from article_fetcher import ArticleBody, FetchRejected, fetch_article
from news_discovery import valid_source_id
from news_enrichment import (
    QualityRejected, enrich_article, FINAL_MODEL_ID, MAX_TOKENS,
    QUALITY_GATE_VERSION, PROMPT_VERSION,
)
from news_pipeline import canonicalize_url, document_hash, is_within_days
from news_renderer import render_news_markdown
from news_store import Attempt, LEASE_SECONDS, StateRejected, _money

MAX_INPUT_TOKENS = 100000
MODEL_SECONDS = 310
SETTLEMENT_SECONDS = 45
FETCH_SECONDS = 125


@dataclass(frozen=True)
class ModelPolicy:
    """Required rates are USD per million tokens; no zero-price default."""
    input_rate: Decimal
    output_rate: Decimal
    model_id: str = FINAL_MODEL_ID

    def __post_init__(self):
        for name in ("input_rate", "output_rate"):
            value = _money(getattr(self, name))
            if value <= 0:
                raise StateRejected("INVALID_PRICING")
            object.__setattr__(self, name, value)
        if self.model_id != FINAL_MODEL_ID:
            raise StateRejected("INVALID_MODEL")

    def cost(self, usage):
        value = (usage["inputTokens"] * self.input_rate
                 + usage["outputTokens"] * self.output_rate) / Decimal(1000000)
        return value.quantize(Decimal("0.000000000001"), rounding=ROUND_CEILING)

    @property
    def reservation(self):
        return self.cost({"inputTokens": 2 * MAX_INPUT_TOKENS, "outputTokens": 2 * MAX_TOKENS})


class MeteredModel:
    """Count input before invocation and retain usage independently of validation."""
    def __init__(self, client, policy, remaining):
        self.client, self.policy, self.remaining = client, policy, remaining
        self.calls = 0
        self.usage = {"inputTokens": 0, "outputTokens": 0}
        self.complete = True
        self.uncertain = False

    def converse(self, **request):
        if (self.calls >= 2 or request.get("modelId") != self.policy.model_id
                or request.get("inferenceConfig", {}).get("maxTokens") != MAX_TOKENS):
            raise QualityRejected("MODEL_BUDGET")
        if self.remaining() < MODEL_SECONDS // 2 + SETTLEMENT_SECONDS:
            raise QualityRejected("RUN_BUDGET")
        count_input = {key: request[key] for key in
                       ("messages", "system", "additionalModelRequestFields") if key in request}
        count = self.client.count_tokens(
            modelId=self.policy.model_id, input={"converse": count_input}).get("inputTokens")
        if type(count) is not int or not 1 <= count <= MAX_INPUT_TOKENS:
            raise QualityRejected("INPUT_BUDGET")
        if self.remaining() < 125 + SETTLEMENT_SECONDS:
            raise QualityRejected("RUN_BUDGET")
        self.calls += 1
        # A failed request may have been billed. Only a complete valid usage
        # response can clear this uncertainty for the current invocation.
        self.complete = False
        try:
            response = self.client.converse(**request)
        except Exception:
            self.uncertain = True
            raise
        values = response.get("usage", {})
        valid = True
        overrun = False
        for key, cap in (("inputTokens", MAX_INPUT_TOKENS), ("outputTokens", MAX_TOKENS)):
            value = values.get(key)
            if type(value) is int and 0 <= value <= 10000000:
                self.usage[key] += value
                overrun |= value > cap
            else:
                valid = False
        self.uncertain |= not valid
        self.complete = not self.uncertain
        if not valid:
            raise QualityRejected("USAGE_INVALID")
        if overrun:
            raise QualityRejected("MODEL_BUDGET")
        return response


@dataclass
class WorkerDeps:
    store: object
    s3: object
    bedrock: object
    artifact_bucket: str
    kb_bucket: str
    policy: ModelPolicy
    month_limit: Decimal
    run_limit: Decimal
    remaining: object
    now: object
    fetch: object = fetch_article
    token: object = lambda: uuid.uuid4().hex


def _result(status, source_id, doc_hash="", reason="", warnings=()):
    return {"status": status, "sourceId": source_id, "docHash": doc_hash,
            "reason": reason, "docsAdded": 0, "docsUpdated": 0,
            "fetchWarnings": list(warnings)}


def _put_immutable(deps, bucket, key, data, content_type):
    digest = hashlib.sha256(data).digest()
    deps.s3.put_object(
        Bucket=bucket, Key=key, Body=data, ContentType=content_type,
        IfNoneMatch="*", ChecksumSHA256=base64.b64encode(digest).decode("ascii"),
        Metadata={"sha256": digest.hex()})
    return digest.hex()


def _settle_storage_rejection(deps, job, model, warnings, error):
    # A later rejection cannot resolve an earlier ambiguous SDK attempt.
    metadata = error.response.get("ResponseMetadata")
    detail = error.response.get("Error")
    codes = {"AccessDenied": 403, "NoSuchBucket": 404, "KMS.AccessDeniedException": 400}
    if (error.operation_name != "PutObject"
            or not isinstance(metadata, dict) or not isinstance(detail, dict)
            or type(metadata.get("RetryAttempts")) is not int
            or metadata["RetryAttempts"] != 0
            or not isinstance(detail.get("Code"), str)
            or detail["Code"] not in codes
            or type(metadata.get("HTTPStatusCode")) is not int
            or metadata["HTTPStatusCode"] != codes[detail["Code"]]
            or deps.remaining() < SETTLEMENT_SECONDS):
        raise error
    # The existing atomic writer checks ownership and lease freshness. Its own
    # failure remains ambiguous; never fabricate a receipt or remove an object.
    return deps.store.finish(
        job, now=deps.now(), status="FAILED", reason="STORAGE_REJECTED",
        known_cost=deps.policy.cost(model.usage), usage_complete=model.complete,
        usage=model.usage, warnings=warnings)


def _candidate(value):
    if not isinstance(value, dict):
        raise StateRejected("INVALID_CANDIDATE")
    try:
        size = len(json.dumps(value, ensure_ascii=False, allow_nan=False).encode("utf-8"))
        source_id, run_id, revision = (value[key] for key in ("sourceId", "runId", "sourceRevision"))
        if (size > 64000 or not all(isinstance(item, str) for item in (source_id, run_id, revision))
                or not valid_source_id(source_id)
                or not re.fullmatch(r"[A-Za-z0-9_-]{1,128}", run_id)
                or not re.fullmatch(r"[0-9a-f]{64}", revision)):
            raise ValueError()
        url = value["url"]
        canonical = canonicalize_url(url)
        # Activation uses authenticated TLS for publisher content too.
        if not canonical.startswith("https://"):
            raise ValueError()
        custom = value.get("ingestSource") == "custom"
        if value.get("ingestSource") not in ("custom", "search"):
            raise ValueError()
        if type(value.get("requireRelevance")) is not bool or value["requireRelevance"] == custom:
            raise ValueError()
        if not custom:
            if (type(value.get("freshnessDays")) is not int
                    or value["freshnessDays"] not in (7, 30)
                    or not isinstance(value.get("publishedAt"), str)
                    or not 1 <= len(value["publishedAt"]) <= 100):
                raise ValueError()
        related = value.get("corroboratingSources", [])
        if not isinstance(related, list) or len(related) > 2:
            raise ValueError()
        urls, warnings, seen = [], [], {canonical}
        for item in related:
            try:
                target = canonicalize_url(item["url"])
                if not target.startswith("https://") or target in seen:
                    raise ValueError()
                seen.add(target)
                urls.append(item["url"])
            except (KeyError, TypeError, ValueError, UnicodeError):
                warnings.append("CORROBORATION_UNUSABLE")
        return source_id, run_id, revision, url, urls, custom, warnings
    except (KeyError, TypeError, ValueError, UnicodeError, RecursionError):
        raise StateRejected("INVALID_CANDIDATE") from None


def _fresh(candidate, now, lead=None):
    if candidate["ingestSource"] == "custom":
        return True
    instant = datetime.fromtimestamp(now, timezone.utc)
    days = candidate["freshnessDays"]
    return (is_within_days(candidate["publishedAt"], days, instant)
            and (lead is None or not lead.published_at
                 or is_within_days(lead.published_at, days, instant)))


def _configured_custom(config, url):
    canonical = canonicalize_url(url)
    for value in config.get("customUrls", []):
        try:
            if canonicalize_url(value) == canonical:
                return True
        except (ValueError, TypeError, UnicodeError):
            continue
    return False


def process_candidate(candidate, deps):
    source_id, run_id, *_ = _candidate(candidate)
    result = _process_candidate(candidate, deps)
    return {**result, "sourceId": source_id, "runId": run_id}


def _process_candidate(candidate, deps):
    source_id, run_id, revision, url, related, custom, warnings = _candidate(candidate)
    config = deps.store.current_config(source_id, revision)
    if custom and not _configured_custom(config, url):
        raise StateRejected("SOURCE_CHANGED")
    if not _fresh(candidate, deps.now()):
        return _result("FETCH_REJECTED", source_id, reason="STALE_CANDIDATE")
    if deps.remaining() < FETCH_SECONDS + MODEL_SECONDS + SETTLEMENT_SECONDS:
        return _result("DEFERRED", source_id, reason="RUN_BUDGET")
    try:
        lead = deps.fetch(url, https_only=True)
    except FetchRejected as error:
        return _result("FETCH_REJECTED", source_id, reason=error.reason)
    if not isinstance(lead, ArticleBody) or not lead.final_url.startswith("https://"):
        return _result("FETCH_REJECTED", source_id, reason="INSECURE_REDIRECT")
    if not _fresh(candidate, deps.now(), lead):
        return _result("FETCH_REJECTED", source_id, reason="STALE_ARTICLE")
    canonical = canonicalize_url(lead.canonical_url)
    doc_hash = document_hash(canonical)
    if deps.store.published_hashes(source_id, {doc_hash}):
        return _result("DUPLICATE", source_id, doc_hash)
    bodies = [lead]
    for index, other in enumerate(related):
        if deps.remaining() < FETCH_SECONDS + MODEL_SECONDS + SETTLEMENT_SECONDS:
            warnings.extend(["CORROBORATION_DEFERRED"] * (len(related) - index))
            break
        try:
            body = deps.fetch(other, https_only=True)
            if (isinstance(body, ArticleBody) and body.final_url.startswith("https://")
                    and canonicalize_url(body.final_url)
                    not in {canonicalize_url(item.final_url) for item in bodies}
                    and canonicalize_url(body.canonical_url)
                    not in {canonicalize_url(item.canonical_url) for item in bodies}):
                bodies.append(body)
            else:
                warnings.append("CORROBORATION_UNUSABLE")
        except FetchRejected:
            warnings.append("CORROBORATION_FETCH_FAILED")
            continue
    if deps.remaining() < MODEL_SECONDS + SETTLEMENT_SECONDS:
        return _result("DEFERRED", source_id, doc_hash, "RUN_BUDGET", warnings)
    if not _fresh(candidate, deps.now(), lead):
        return _result("FETCH_REJECTED", source_id, doc_hash, "STALE_ARTICLE", warnings)
    now = deps.now()
    job = Attempt(source_id, doc_hash, run_id, deps.token(),
                  datetime.fromtimestamp(now, timezone.utc).strftime("%Y-%m"), revision,
                  deps.policy.reservation, now + LEASE_SECONDS)
    deps.store.reserve(job, now=now, month_limit=deps.month_limit, run_limit=deps.run_limit)
    model = MeteredModel(deps.bedrock, deps.policy, deps.remaining)
    # Identity/context is current canonical config, never arbitrary caller prose.
    prompt_candidate = {"sourceName": config.get("sourceName", ""),
                        "aliases": config.get("aliases") or [],
                        "keywords": config.get("newsQueries") or [],
                        "title": lead.title, "requireRelevance": not custom}
    try:
        artifact, _ = enrich_article(prompt_candidate, bodies, model,
                                     model_id=deps.policy.model_id)
    except QualityRejected as error:
        return deps.store.finish(
            job, now=deps.now(), status="REJECTED", reason=error.reason,
            known_cost=deps.policy.cost(model.usage), usage_complete=model.complete,
            usage=model.usage, warnings=warnings)
    if not _fresh(candidate, deps.now(), lead):
        return deps.store.finish(
            job, now=deps.now(), status="REJECTED", reason="STALE_ARTICLE",
            known_cost=deps.policy.cost(model.usage), usage_complete=model.complete,
            usage=model.usage, warnings=warnings)
    if deps.remaining() < SETTLEMENT_SECONDS:
        # Leave the reservation and claim intact on an unknown/unfinished path.
        raise StateRejected("RUN_BUDGET")
    try:
        artifact_bytes = json.dumps(artifact, ensure_ascii=False, allow_nan=False,
                                   sort_keys=True, separators=(",", ":")).encode("utf-8")
        markdown = render_news_markdown(artifact, prompt_candidate, len(bodies)).encode("utf-8")
    except (ValueError, UnicodeError):
        return deps.store.finish(
            job, now=deps.now(), status="REJECTED", reason="RENDER_REJECTED",
            known_cost=deps.policy.cost(model.usage), usage_complete=model.complete,
            usage=model.usage, warnings=warnings)
    try:
        artifact_hash = _put_immutable(deps, deps.artifact_bucket, job.artifact_key,
                                      artifact_bytes, "application/json; charset=utf-8")
    except ClientError as error:
        return _settle_storage_rejection(deps, job, model, warnings, error)
    if deps.remaining() < SETTLEMENT_SECONDS:
        raise StateRejected("RUN_BUDGET")
    if not _fresh(candidate, deps.now(), lead):
        return deps.store.finish(
            job, now=deps.now(), status="REJECTED", reason="STALE_ARTICLE",
            known_cost=deps.policy.cost(model.usage), usage_complete=model.complete,
            usage=model.usage, warnings=warnings)
    try:
        markdown_hash = _put_immutable(deps, deps.kb_bucket, job.markdown_key,
                                      markdown, "text/markdown; charset=utf-8")
    except ClientError as error:
        return _settle_storage_rejection(deps, job, model, warnings, error)
    if deps.remaining() < SETTLEMENT_SECONDS:
        raise StateRejected("RUN_BUDGET")
    if not _fresh(candidate, deps.now(), lead):
        return deps.store.finish(
            job, now=deps.now(), status="REJECTED", reason="STALE_ARTICLE",
            known_cost=deps.policy.cost(model.usage), usage_complete=model.complete,
            usage=model.usage, warnings=warnings)
    row = {
        "PK": job.pk, "SK": "DOC#" + doc_hash, "sourceId": source_id, "docHash": doc_hash,
        "schemaVersion": 1, "qualityStatus": "PUBLISHED", "type": "news",
        "attemptToken": job.token, "sourceRevision": revision,
        "s3Key": job.markdown_key, "artifactKey": job.artifact_key,
        "markdownSha256": markdown_hash, "artifactSha256": artifact_hash,
        "contentHash": hashlib.sha256(lead.body.encode()).hexdigest(),
        "canonicalUrl": canonical, "url": lead.final_url,
        "title": artifact["title"], "summary": artifact["executiveSummary"][:1500],
        "keyTakeaways": [fact["fact"] for fact in artifact["keyFacts"][:3]],
        "tags": artifact["tags"], "sourceName": config.get("sourceName", ""),
        "pubDate": lead.published_at or (candidate.get("publishedAt", "") if not custom else ""),
        "publishedAtBasis": "article" if lead.published_at else ("unknown" if custom else "search"),
        "ingestSource": "custom" if custom else "search",
        "qualityScore": artifact["relevanceConfidence"], "qualityGateVersion": QUALITY_GATE_VERSION,
        "promptVersion": PROMPT_VERSION, "modelId": deps.policy.model_id,
        "corroboratingSourceCount": len(bodies) - 1,
        "crawledAt": deps.now(), "GSI4PK": "DOC#news", "GSI4SK": deps.now(), "inKB": True,
    }
    try:
        return deps.store.finish(job, now=deps.now(), status="PUBLISHED",
                                 known_cost=deps.policy.cost(model.usage),
                                 usage_complete=model.complete, usage=model.usage,
                                 document=row, warnings=warnings)
    except StateRejected as error:
        if error.reason != "SOURCE_CHANGED":
            raise
        return deps.store.finish(job, now=deps.now(), status="FAILED", reason="SOURCE_CHANGED",
                                 known_cost=deps.policy.cost(model.usage),
                                 usage_complete=model.complete, usage=model.usage, warnings=warnings)
