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

The existing handler-suite command also loads these contract suites:

```bash
python3 -m unittest test_handler -v
```

Tests use synthetic table/S3 responses and reject live networking. This foundation
does not grant new IAM or enable any new retrieval path. Unified discovery,
source-derived session invalidation, legacy binary migration and deployment
remain separate integration work.
