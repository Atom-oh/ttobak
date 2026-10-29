# Billing analysis worker

Python worker that turns uploaded cost exports (COT zips and CUR files) into the datasets the Billing dashboard reads, plus the Bedrock agent notes and the insights report.

Imported from the companion repo at commit dd540f5. This is a faithful code import; later changes split and extend it.

## Layout

- `scripts/` - dataset builders, agents, report, merge helpers; `aws_whats_new.json` is the public What's New seed.
- `cur/` - CUR staging, normalization and checks.
- `worker/` - Fargate entry point (`main.py`) and the AI step (`ai_step.py`).
- `run_account.sh` - per-account pipeline run by the worker (validate, extract, datasets, check).
- `tests/` - unit tests (synthetic data only), including `test_contract.py` for the import contract.
- `Dockerfile.ingest`, `.dockerignore` - image for the upload worker; the build context is this directory.
- `requirements-ingest.txt` (duckdb, boto3), `requirements-agents.txt` (boto3, fastapi, uvicorn), `requirements-dev.txt` (test-only).

Image build:

    docker build --platform linux/arm64 -f Dockerfile.ingest .

Git-ignored local output: `datasets/`, `out/`, `.agent_cache/`, `dispatch/dist/`.

## Run the tests locally

Python 3.12 is what CI uses.

    python3.12 -m venv ~/.tmp/billing-worker-venv
    ~/.tmp/billing-worker-venv/bin/pip install -r requirements-ingest.txt -r requirements-agents.txt -r requirements-dev.txt
    cd services/billing/worker
    ~/.tmp/billing-worker-venv/bin/python -m unittest discover -s tests -v

Two worker smoke tests run the real pipelines and are skipped unless `WORKER_SMOKE=1` is set.

## Changes since the companion import

- Agents child: fresh per-job HOME, and `AWS_CONFIG_FILE` / `AWS_SHARED_CREDENTIALS_FILE` set to `/dev/null`.
- CUR publish: the META update requires `current_cur` to equal the predecessor the run read its inputs from (or be absent when there was none), not just `<=` the new version id; a mismatch fails the run with `StaleVersion` and leaves META unchanged.
- Worker process: `AWS_CONFIG_FILE` / `AWS_SHARED_CREDENTIALS_FILE` are `/dev/null` in the image ENV and set again at startup, before any client or child, so a config planted in the shared HOME never configures its own clients.
- `cur/datasets.py`: `cur_trend` is also capped at `MAX_TREND_BYTES` (40 MiB as written, under the BFF's 48 MiB file limit); the cut is reported in `cur_summary` `truncated` / `dropped` like the row cap.
- Rerun lease: `ACCT#{acct}` / `RERUN#{src}#{version}` (`owner`, `expiresAt`, `ttl`) by conditional PutItem; a rerun that finds it held exits 0 without writing, checks it is still the owner before publishing, and releases it in a finally. Rerun deadline from `RERUN_TIMEOUT_S` (default 3600); the lease lasts that plus 120 s.
- Normalized CUR Parquet goes up by managed multipart transfer (`upload_fileobj`, 64 MiB parts); the session policy adds `s3:AbortMultipartUpload` on the same `accounts/{acct}/*` resource as PutObject.
- Startup fails closed (exit 2, nothing read or written) when `PR_SET_DUMPABLE` fails; `PR_SET_CHILD_SUBREAPER` stays best effort, logged.
- A failed, timed-out or stopped rerun sets `rerunError` (reason kind) and `rerunFailedAt` (epoch s) on the version's `UP#` row; a successful rerun removes both.

## Retired companion tests

- `test_fsisearch.py` (whole file): tested a companion-only search function; replaced by a later task.
- `test_pipeline_wiring.py::test_wrapper_runs_accounts_then_merges`, `test_entity_discovery`, `test_entity_discovery_rejects_bad_names_and_empty`, `test_agents_guarded_and_report_after_briefing`: read or ran the local multi-account wrapper script, which is not imported.
- `test_pipeline_wiring.py::test_pipeline_wiring`: only its `run_account.sh` assertions are kept, as `RunAccountWiring` (step order, no agents/report, script order); `run_account.sh` is the production per-account script.
- `test_generic.py::test_no_customer_name_outside_docs`: scanned companion paths; replaced by a CI-time grep of this tree.
- `test_generic.py::Compare` (`test_renamed_fields_match`, `test_value_change_reported_without_values`, `test_dataset_missing_on_one_side_is_a_difference`): exercised a companion-only comparison tool that is not imported.

Retargeted rather than retired: `test_server_allows_agent_notes` now reads the Billing BFF dataset sets, `test_generated_outputs_ignored` checks this repo's `.gitignore`, and the report rebuild hint points to the Billing upload page (AI rerun).
