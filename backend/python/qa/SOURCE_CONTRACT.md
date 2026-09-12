# Current-source reader foundation

These modules define and test current-source reads without changing the active
QA handler, retrieval filters, prompts or session behavior.

- `document_context.py` authorizes personal/direct-share/exact account document
  reads before retaining source contents.
- `source_revision.py` pins canonical identity and hashes the exact present
  fields plus S3 bindings. Missing and null remain distinct; test vectors match
  the Go worker when its fixture is present.
- `source_context.py` performs fresh metadata authorization before S3, validates
  source identities, and bounds reads pinned to ETag/version.
- `attachment_context.py` reads authorized `ATTACH#`/`ATTEXT#` records and verifies
  immutable result identity, original ETag, byte limits and continuation.
  Partial/retained results remain explicit and never get audio timestamps.
- `indexed_retrieval.py` discovers all authorized identity pages, consumes
  current saved text, and accepts indexed file excerpts only with the current
  canonical revision and S3 bindings. Legacy meeting exports supply identities,
  never meeting text. Saved keyword matches cover ingestion delay.
- `manual_kb.py` verifies private `manual-kb-v1` and authenticated-shared
  `shared-kb-v1` snapshots. Original `kb/{owner}/...` and `shared/**` binary
  chunks need a new immutable copy; adding current metadata to an old URI is
  insufficient. Missing snapshots return explicit pending status. Source
  read failures propagate instead of returning empty success.
- `session_provenance.py` keeps source dependencies beside model messages.
  Restore requires current authorization/revisions for every dependency and
  an explicit replayable marker. Both integer version 1 before persistence and
  Decimal version 1 after a boto3 resource read are accepted; booleans, floats,
  strings, unknown versions and untracked histories are rejected.
- `source_access.py` composes these readers into current-source search and
  meeting/document/attachment contexts, with source dependencies attached.
  Its constructor receives readers and callbacks; it creates no AWS clients.
- `source_tools.py` defines and formats the three document/attachment tools.
  It is not registered by the current handler. The final wiring imports these
  definitions into the existing authenticated tool loop and preserves its
  error boundary.

The existing handler-suite command also loads these contract suites:

```bash
python3 -m unittest test_handler -v
```

Tests use synthetic table/S3 responses, including the real boto3 attribute
serializer/deserializer, without live AWS/model calls. The helper tests are
loaded by the same command; existing handler tests remain unchanged.

This foundation does not grant new IAM or enable any new retrieval path.
The runtime integration must wire the helpers, expose only an explicit
allowlist of public source fields, and deploy `KB_BUCKET_NAME` with source-read
permissions. Private/shared binary snapshots must be produced before adopting
the strict consumer; pending-only migration is not a substitute for existing
file answerability. Documented snapshot support is PDF, DOC, DOCX, XLS and XLSX.
PPT/PPTX remain visible with an unsupported-file state until a supported
conversion path exists. Producer deployment, actual recall and session
revalidation through the public endpoints remain separate acceptance work.
