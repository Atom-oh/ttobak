"""Verify only this manifest's synthetic sources; never call AWS or mutate data."""
import hashlib
import json
from pathlib import Path


def require(condition, message):
    if not condition:
        raise ValueError(message)


def revision(schema, bucket, key, etag, version, size):
    digest = hashlib.sha256()
    for value in (schema, bucket, key, etag, version, str(size)):
        raw = value.encode("utf-8")
        digest.update(str(len(raw)).encode("ascii") + b":" + raw)
    return digest.hexdigest()


def indexed(manifest, name, generation, observed):
    require(name in ("private", "shared"), "not a bootstrap case")
    require(generation in (1, 2), "invalid fixture generation")
    case = manifest["cases"][name]
    config = manifest["config"]
    suffix = "pdf" if name == "private" else "docx"
    fixture = manifest["files"][f"{name}-v{generation}.{suffix}"]
    source = observed["source"]
    require(source["key"] == case["sourceKey"], "foreign source key")
    require(source["bucket"] == config["kbBucket"], "foreign source bucket")
    require(source["metadata"].get("acceptance-run-id") == manifest["runId"], "source is not run-owned")
    require(source["size"] == fixture["bytes"], "source size differs from the prepared fixture")
    require(source.get("etag") and source.get("versionId"), "source byte binding incomplete")
    # expectedSource comes from the successful conditional fixture upload receipt.
    receipt = observed["expectedSource"]
    require(receipt["fixtureSHA256"] == fixture["sha256"], "upload receipt belongs to different bytes")
    require(source["etag"] == receipt["etag"] and source["versionId"] == receipt["versionId"],
            "source changed after the recorded upload")
    schema = "manual-kb-v1" if name == "private" else "shared-kb-v1"
    current = revision(schema, config["kbBucket"], case["sourceKey"],
                       source["etag"], source["versionId"], source["size"])
    job = observed["job"]
    require(job["state"] == "INDEXED", "worker has not indexed the source")
    require(job["resource"] == {
        "pk": case["pk"], "sk": case["sk"], "kind": case["kind"],
        "id": case["resourceId"], "sourceKey": case["sourceKey"],
    }, "job is not bound to the fixture")
    require(job["revision"] == current, "job revision is stale")
    require(not job.get("desiredRevision") or job["desiredRevision"] == current, "newer work is pending")
    require(not job.get("pendingKeys") and not job.get("removedKeys"), "publication/removal is unsettled")
    body_key = case["snapshotPrefix"] + current + "/" + job["runId"] + "/document." + suffix
    require(set(job["keys"]) == {body_key, body_key + ".metadata.json"}, "unexpected snapshot keys")
    require(set(observed["inventory"]) == set(job["keys"]), "snapshot inventory is not settled")
    uri = "s3://" + config["kbBucket"] + "/" + body_key
    require(observed["documentStatuses"] == {uri: "INDEXED"}, "provider document is not indexed")
    require(observed["ingestion"]["id"] == job["syncId"], "ingestion identity mismatch")
    require(observed["ingestion"]["status"] in ("COMPLETE", "FAILED", "STOPPED"),
            "ingestion is not terminal")
    # Global failed counts include unrelated files. Per-document status plus
    # a current, scoped retrieval proves this source independently.
    hits = observed["retrieval"]
    require(bool(hits), "no current synthetic retrieval hit")
    found = False
    for hit in hits:
        metadata = hit["metadata"]
        require(hit["uri"] == uri, "foreign or old snapshot returned")
        expected = {
            "indexSchema": schema, "resourceKind": case["kind"],
            "resourceId": case["resourceId"], "sourceRevision": current,
            "indexRunId": job["runId"], "sourceBucket": config["kbBucket"],
            "sourceKey": case["sourceKey"], "sourceETag": source["etag"],
            "sourceVersionId": source["versionId"], "sourceSize": source["size"],
        }
        if name == "private":
            expected["ownerId"] = case["pk"].removeprefix("USER#")
            require("visibility" not in metadata, "private file relabeled shared")
        else:
            expected["visibility"] = "authenticated-shared"
            require("ownerId" not in metadata, "shared file has an invented owner")
        require(all(metadata.get(key) == value for key, value in expected.items()), "retrieval binding mismatch")
        found = found or fixture["marker"] in hit["text"]
        if generation == 2:
            require(case["markerV1"] not in hit["text"], "old fixture text survived replacement")
    require(found, "expected current fixture fact is absent")
    return {"case": name, "generation": generation, "revision": current, "uri": uri,
            "actualRetrieval": True, "scope": "IAM worker/provider; no public QA proof"}


def deleted(manifest, name, observed):
    case = manifest["cases"][name]
    require(observed["sourceMissing"] is True, "original is still present or its absence is unverified")
    require(observed.get("sourceErrorCode") in ("404", "NoSuchKey", "NotFound"), "denial is not deletion")
    job = observed["job"]
    require(job["state"] == "DELETED", "worker has not settled deletion")
    require(job["resource"]["sourceKey"] == case["sourceKey"], "foreign deletion job")
    require(not job.get("keys") and not job.get("pendingKeys") and not job.get("removedKeys"),
            "deletion obligations remain")
    require(not observed["inventory"] and not observed["retrieval"], "snapshot or vector remains")
    prior = observed["previousSnapshotURIs"]
    require(bool(prior), "missing pre-delete snapshot evidence")
    prefix = "s3://" + manifest["config"]["kbBucket"] + "/" + case["snapshotPrefix"]
    require(all(uri.startswith(prefix) for uri in prior), "foreign cleanup proof")
    require(observed["documentStatuses"] == {uri: "NOT_FOUND" for uri in prior}, "provider deletion unproved")
    return {"case": name, "deleted": True, "jobTombstoneRetained": case["jobHash"]}


def verify_file(manifest_path, evidence_path, name, generation):
    manifest = json.loads(Path(manifest_path).read_text())
    observed = json.loads(Path(evidence_path).read_text())
    return indexed(manifest, name, generation, observed)
