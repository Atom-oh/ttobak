"""Offline verification regressions. These are never deployment evidence."""
import json
from pathlib import Path
import unittest

from verify import deleted, indexed, revision


class VerifyTests(unittest.TestCase):
    def setUp(self):
        self.manifest = json.loads((Path(__file__).parent / "manifest.json").read_text())

    def observation(self, name="private", generation=1):
        case, config = self.manifest["cases"][name], self.manifest["config"]
        suffix = "pdf" if name == "private" else "docx"
        fixture = self.manifest["files"][f"{name}-v{generation}.{suffix}"]
        schema = "manual-kb-v1" if name == "private" else "shared-kb-v1"
        version, etag = f"synthetic-v{generation}", f'"synthetic-e{generation}"'
        rev = revision(schema, config["kbBucket"], case["sourceKey"], etag, version, fixture["bytes"])
        run = "00000000-0000-4000-8000-000000000001"
        key = case["snapshotPrefix"] + rev + "/" + run + "/document." + suffix
        uri = "s3://" + config["kbBucket"] + "/" + key
        metadata = {
            "indexSchema": schema, "resourceKind": case["kind"], "resourceId": case["resourceId"],
            "sourceRevision": rev, "indexRunId": run, "sourceBucket": config["kbBucket"],
            "sourceKey": case["sourceKey"], "sourceETag": etag, "sourceVersionId": version,
            "sourceSize": fixture["bytes"],
        }
        metadata.update({"ownerId": case["pk"][5:]} if name == "private" else {"visibility": "authenticated-shared"})
        return {
            "source": {"key": case["sourceKey"], "bucket": config["kbBucket"], "etag": etag,
                       "versionId": version, "size": fixture["bytes"],
                       "metadata": {"acceptance-run-id": self.manifest["runId"]}},
            "expectedSource": {"etag": etag, "versionId": version, "fixtureSHA256": fixture["sha256"]},
            "job": {"state": "INDEXED", "revision": rev, "desiredRevision": rev, "runId": run,
                    "syncId": "sync-1", "keys": [key, key + ".metadata.json"],
                    "resource": {"pk": case["pk"], "sk": case["sk"], "kind": case["kind"],
                                 "id": case["resourceId"], "sourceKey": case["sourceKey"]}},
            "inventory": [key, key + ".metadata.json"], "documentStatuses": {uri: "INDEXED"},
            "ingestion": {"id": "sync-1", "status": "COMPLETE", "failedDocumentCount": 3},
            "retrieval": [{"uri": uri, "metadata": metadata, "text": fixture["marker"]}],
        }

    def test_unrelated_partial_sync_does_not_invalidate_an_indexed_current_file(self):
        for name in ("private", "shared"):
            proof = indexed(self.manifest, name, 1, self.observation(name))
            self.assertTrue(proof["actualRetrieval"])

    def test_revision_matches_independent_producer_vectors(self):
        path = Path(__file__).resolve().parents[4] / "backend/internal/service/testdata/knowledge-revisions.json"
        for vector in json.loads(path.read_text()):
            actual = revision(vector["indexSchema"], vector["sourceBucket"], vector["sourceKey"],
                              vector["sourceETag"], vector["sourceVersionId"], vector["sourceSize"])
            self.assertEqual(actual, vector["sourceRevision"])

    def test_stale_foreign_unsettled_or_unproven_outputs_fail(self):
        changes = [
            lambda o: o["source"].update(versionId="overwritten"),
            lambda o: o["job"].update(revision="old"),
            lambda o: o["job"].update(pendingKeys=["pending"]),
            lambda o: o["job"]["resource"].update(sourceKey="kb/other/private.pdf"),
            lambda o: o["retrieval"][0].update(uri="s3://foreign/private.pdf"),
            lambda o: o["retrieval"][0]["metadata"].update(sourceETag='"old"'),
            lambda o: o["retrieval"][0].update(text="OLD_FACT"),
            lambda o: o.update(documentStatuses={}),
        ]
        for change in changes:
            observed = self.observation()
            change(observed)
            with self.assertRaises(ValueError):
                indexed(self.manifest, "private", 1, observed)

    def test_replacement_does_not_accept_old_marker_under_current_metadata(self):
        observed = self.observation(generation=2)
        observed["retrieval"][0]["text"] += self.manifest["cases"]["private"]["markerV1"]
        with self.assertRaises(ValueError):
            indexed(self.manifest, "private", 2, observed)

    def test_access_denied_never_proves_deletion(self):
        observed = self.observation()
        uri = observed["retrieval"][0]["uri"]
        observed.update(sourceMissing=True, sourceErrorCode="AccessDenied", inventory=[], retrieval=[],
                        previousSnapshotURIs=[uri], documentStatuses={uri: "NOT_FOUND"})
        observed["job"].update(state="DELETED", keys=[])
        with self.assertRaises(ValueError):
            deleted(self.manifest, "private", observed)
        observed["sourceErrorCode"] = "404"
        self.assertTrue(deleted(self.manifest, "private", observed)["deleted"])
