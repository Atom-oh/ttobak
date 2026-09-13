# Exact canonical source selection

`search_knowledge_base.resource_ids` selects one to five meeting or
personal/account DocHub document IDs. It is mutually exclusive with
`source_keys`, which remains restricted to original private/shared binary KB
keys. IDs, snapshot paths and another user's private keys are not accepted as
manual keys.

Current discovery resolves candidate identities; `SourceReader` authorizes each
selected resource before any provider request or content hydration. Unknown and
unauthorized IDs produce no content. Multiple authorized identities with the
same ID are rejected rather than arbitrarily selected.

Each authorized resource is queried with its canonical schema, partition,
sort key and current revision. Exact selection can use a low-scoring result
only after these checks. Provider candidates with another identity, revision,
filename or original-object binding are removed before hydration. Existing
source/S3 validation then runs again, and a change during retrieval fails the
operation. Default semantic search keeps its existing score threshold.

Current saved text can be returned for a selected resource even if the query
terms do not match. Binary content still requires a current verified indexed
excerpt. An existing file without such an excerpt remains explicitly
`filePending`, without fabricated text; it is nonreplayable. Proof-bound async
delivery may reject that unavailable content rather than publish a cached
answer. Provider/read errors are never converted into empty successful results.

Selection returns at most one aggregated result per requested resource and
does not automatically expand a selected meeting into attachment sources.
Use the existing attachment tools when attachments are requested.

Missing IDs in an otherwise successful selection are recorded individually in
empty-search receipts. These preserve the current user, query, result limit and
selection, and re-run the selected search before history replay. A new source
or newly granted access invalidates an earlier absence claim. Source edits,
file-byte changes and grant revocation retain the existing whole-history and
delivery-proof checks.

The real failure that motivated this change reported an invalid binary-key
selection while the question named DocHub IDs. The redacted logs do not retain
the exact rejected array; they establish the invalid selection, not its literal
contents. Local tests verify the new contract; actual public file-QA acceptance
must follow reviewed deployment without replaying the failed job.

Run `python3 -m unittest test_handler -v` from this directory. The registered
suite includes exact low-score file retrieval, authorization/revocation,
malformed and mixed selectors, ambiguous IDs, provider failure, revision races,
pending files, SDK-serialized absence receipts and handler/provenance wiring.
