"""Check archived observations offline; this never calls AWS."""
import json
from pathlib import Path

from verify import deleted, indexed


root = Path(__file__).parent
manifest = json.loads((root / "manifest.json").read_text())
proofs = []
for name in ("private", "shared"):
    for generation in (1, 2):
        observed = json.loads((root / f"{name}-v{generation}-observed.json").read_text())
        proofs.append(indexed(manifest, name, generation, observed))
        if generation == 2:
            previous = json.loads((root / f"{name}-v1-verified.json").read_text())["uri"]
            assert observed["removedDocumentStatuses"] == {previous: "NOT_FOUND"}
    observed = json.loads((root / f"{name}-deleted-observed.json").read_text())
    proofs.append(deleted(manifest, name, observed))
    original = "s3://" + manifest["config"]["kbBucket"] + "/" + manifest["cases"][name]["sourceKey"]
    assert observed["originalDocumentStatuses"] == {original: "NOT_FOUND"}

cleanup = json.loads((root / "cleanup-verified.json").read_text())
expected = {case[key] for case in manifest["cases"].values() for key in ("sourceKey", "snapshotPrefix")}
assert {item["prefix"] for item in cleanup["finalInventory"]} == expected
assert all(item["dataVersions"] == item["deleteMarkers"] == 0 for item in cleanup["finalInventory"])
assert cleanup["dataVersionsDeleted"] == 17 and cleanup["deleteMarkersDeleted"] == 10
assert set(cleanup["jobTombstonesRetained"]) == {case["jobHash"] for case in manifest["cases"].values()}
print(json.dumps({"verifiedObservations": len(proofs), "cleanupScopes": len(expected),
                  "scope": "Offline consistency check of archived AWS observations"}, indent=2))
