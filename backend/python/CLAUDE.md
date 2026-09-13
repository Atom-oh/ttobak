# Python artifacts

Follow the root guide. Each artifact owns its dependency manifest and deploy path;
do not apply one runtime/library convention to every Python directory.

| Directory | Role |
|---|---|
| `qa/` | Lambda Q&A/tool loop, detection, WebSocket delivery; HTTP payload 2.0 |
| `crawler/` | Step Functions crawler stages and ingestion |
| `document-extract/` | Bounded native-text parser, isolated child and async attachment worker |
| `research-agent/` | ARM64 AgentCore Runtime container, FastAPI/Strands |
| `research-tools/` | Legacy Bedrock Agent tool handlers; check callers before changing |
| `sim/` | Async worker for codegen and Code Interpreter execution |

The research container exposes `/invocations` and `/ping` on port 8080; background
research keeps health responsive. Its Dockerfile owns its Python version. Do not
replace its FastAPI bootstrap based on an SDK recommendation without verifying
startup/health behavior. Managed Lambda runtimes are configured in CDK.

SigV4/MCP web-search plumbing is intentionally duplicated in crawler, research,
and QA because they deploy separately. Keep fixes consistent. QA hash-redacts
queries and enforces its per-user hourly abuse limit before gateway calls, with
the accepted fail-open behavior documented in ADR-028.

Simulator codegen receives validated requirements/options, never the raw meeting
transcript. Preserve matching simRunId writes, the interpreter's empty execution
role, and SANDBOX networking.

## Current-source QA foundations

See [the source contract](qa/SOURCE_CONTRACT.md) for registration status at the
reviewed revision. Helper installation and IAM alone do not wire the handler.
Snapshot bootstrap and synthetic recall verification precede strict cutover;
repository code is not evidence of deployed consumer acceptance.

- Authorize current canonical identities before S3 reads; pin ETag/version/size
  and exact source revision. Legacy and immutable transcript references must match
  the configured bucket, authorized meeting and field. No arbitrary s3:// text
  in saved notes is a storage instruction.
- Current-source retrieval validates document/share/account access and attachment
  result identity. Treat legacy meeting exports as identities, not current text.
  Private/manual and authenticated-shared binaries require verified immutable
  snapshots; pending/partial/unsupported states remain visible. Do not relabel a
  private original as shared or attach current metadata to stale binary evidence.
- History dependencies live outside model messages. Revalidate every source and
  tracked read-only fingerprint before replay, later model rounds and final output.
  Stale/denied/failed/untracked dependencies discard the entire history, including
  assistant paraphrases. Strict account callbacks consume all pages, use exact
  membership and canonical relations, and return CompleteRead only on success.
- Track research creation through a receipt after its one successful mutation;
  never repeat creation to repair history. Budget overflow preserves the current
  result while marking the session nonreplayable with explicit coverage.

Exact fields, limits and integration APIs:
[sources](qa/SOURCE_CONTRACT.md), [history](qa/TOOL_HISTORY_CONTRACT.md),
[account reads](qa/ACCOUNT_READS_CONTRACT.md).

## Attachment extraction

The parent validates canonical MEETING#/ATTACH#/ATTEXT# identities, owner/uploader,
run/lease and source ETag before publishing bounded immutable JSON. Event keys are
not source authority. Terminal writes recheck parent/attachment/run; failures
preserve prior results, ambiguous writes never delete results, and source errors
must not become successful empty extraction. Document positions are never audio
timestamps. PDF/PPTX/DOCX/Markdown native-text support excludes OCR.

Use the credential-stripped, resource-limited child for untrusted files. Deployment
owns isolation/no-egress/IAM; the child audit hook is not an RCE-proof sandbox.
The worker/construct exists, but API producers and summary/QA consumers remain
staged. See [parent contract](document-extract/LAMBDA.md) and
[parser scope/limits](document-extract/README.md).

## Verification

Use the root unittest commands and each artifact's requirements. Crawler/research
SigV4 tests and QA source/history serializer suites need boto3; use `boto3<2`.
The QA `test_handler` suite also loads source/account/history contract tests.
From backend/python:

```bash
(cd qa && python3 -m unittest test_handler -v)
(cd document-extract && python3 -m pip install -r requirements-lambda.txt && python3 -m unittest test_extract test_worker test_handler -v)
```

No global ban on invoke_model, HTTP libraries or SDK packages is implied; inspect
each artifact's actual calls and requirements.
