# Manual KB bootstrap acceptance

Historical runtime evidence observed on **2026-09-13**. This validates the
manual-only snapshot producer prerequisite for the current-source QA rollout.
Public REST/WebSocket Q&A, authorization and model answer quality require their
own subsequent acceptance; this record does not assert that runtime cutover.

## Deployed configuration

- Producer: [PR220](https://github.com/Atom-oh/ttobak/pull/220), merge
  `066a04352a86025dd4d3b01084433251e6b209b8`,
  [successful deployment](https://github.com/Atom-oh/ttobak/actions/runs/34726139788).
- Scheduled activation: [PR225](https://github.com/Atom-oh/ttobak/pull/225), merge
  `aeb46efa80bb64ae2388dd3db1adaebf4aaf8e7e`,
  [successful deployment](https://github.com/Atom-oh/ttobak/actions/runs/34726840270).
- Actual `ttobak-kb`: Active/Successful, `INDEXING_MODE=manual-only`,
  1,024 MiB, 720 seconds; one-minute rule enabled; no canonical stream mapping.
  Observed activation artifact SHA-256 (base64):
  `rSOx94jDvyI7kZvyW+5FtprDvT4lYJ/nAC6rqCbUHmw=`.
- KB `BJJLVLFTOR`, data source `3AVMMT3RF3`, region `ap-northeast-2`.
  Actual role policies restricted state writes to `KBINDEX#JOBS` and
  `KBINDEX#CONTROL`, and object writes/deletes to snapshot prefixes. Original
  sources and legacy meeting exports were read-only to this worker.

## Observed results

Run `acc-5136a0a6ff5f4977b657` used synthetic native PDF and DOCX files. Normal
scheduled processing produced every snapshot. No direct ingestion, manual tick,
coordinator edits, customer document bodies or fabricated authentication events
were used.

| Case | Provider ingestion | Current document | Actual filtered retrieval |
|---|---|---|---|
| Private PDF V1 | `F6ONQHVWZF` | INDEXED | Exact `PRIVATE_BLUEFOX` fixture marker |
| Shared DOCX V1 | `Y01HI94LWB` | INDEXED | Exact `SHARED_BLUEFOX` fixture marker |
| Same-key private PDF V2 | `BTJ7ENTG16` | INDEXED; V1 NOT_FOUND | Exact `PRIVATE_GREENOTTER`, no V1 marker |
| Same-key shared DOCX V2 | `D5RPD8WOXW` | INDEXED; V1 NOT_FOUND | Exact `SHARED_GREENOTTER`, no V1 marker |
| Private deletion | `GD45TLFVY9` | Both snapshots and original NOT_FOUND | No result |
| Shared deletion | `UQQAWXDUMZ` | Both snapshots and original NOT_FOUND | No result |

The full marker strings, source ETags/version IDs, independently recomputed
revisions, immutable snapshot identifiers and normalized AWS observations are in
the adjacent JSON files. Each indexed observation required a strong job read,
current source HEAD, settled snapshot inventory, exact per-document INDEXED
status and real Bedrock Retrieve output. Replacement queries filtered by resource
identity, not revision, and former snapshot identifiers were explicitly checked
as NOT_FOUND. Deletion queries covered both the snapshot resource identity and
the original S3 URI.

Global ingestion statistics included failures for other documents. Those counts
were not treated as failure or success evidence for a particular fixture:
its own document status and current-byte retrieval had to pass independently.
These sparse marker cases establish functional recall, not general semantic
accuracy.

Two initial hand-encoded DOCX transfers failed byte checks and were excluded from
acceptance. The sealed local file was then uploaded directly with a conditional
presigned PUT. A version-pinned self-copy restored the run ownership metadata.
V2 uploads also used the actual local files. Transfer receipts and fixture
SHA-256 values identify the accepted bytes; all superseded versions were included
in cleanup.

The new shared fixture waited for the paginated catalogue to wrap; subsequent
known-source replacement and deletion were handled by normal reconciliation.
This smoke test does not establish an indexing latency SLO.

## Existing source readiness and cleanup

The complete paginated inventory found four supported PDFs and two PPTX originals.
For all four PDFs, current source HEADs matched immutable metadata byte bindings,
and the exact provider documents were INDEXED. Only metadata was inspected; no
customer body or customer query was used for evaluation. The two PPTX jobs
remained explicitly FAILED/UNSUPPORTED_FILE, the existing manual-KB limitation.
See [existing-source-readiness.json](existing-source-readiness.json).

After both deletion checks, 17 historical data versions were removed. A fresh
inventory proved zero data versions before removing the 10 delete markers, so
old sources could not become current again. Final reads verified zero versions
and markers in all four exact scopes. The two DELETED job tombstones remain.
See [cleanup-verified.json](cleanup-verified.json).

## Recheck the archive

From this directory:

```bash
python3 check.py
python3 -m unittest test_verify -v
```

These commands validate the archived observations offline; they do not rerun
AWS acceptance. The five verifier regressions cover foreign/stale bindings,
unsettled publication, replacement facts, deletion versus access denial and
revision agreement with the Go producer vectors.

`manifest.json` contains configuration extracted from the immutable pre-run
manifest and its original hash. Execution outcomes are separate observation and
verification files. The archive contains no credentials, signed URLs or customer
document content.
