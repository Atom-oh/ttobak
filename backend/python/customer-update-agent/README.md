# Customer update agent (AgentCore Runtime)

FastAPI + Strands container that takes one `run-v1` payload
([contract](../../../contracts/customer-updates/run-v1.json)): followed links and
customer account packs. It reads each link, decides per customer whether the
update is worth a draft, researches and writes the drafts it can afford, and
writes sealed result rows under `CURUN#{runId}`. It sends nothing to customers.
Nothing invokes it yet; the Go worker that calls it and materializes the rows is
separate. Deployed outside CDK by
[`deploy-customer-update-agent.yml`](../../../.github/workflows/deploy-customer-update-agent.yml);
the owner steps are in the [runbook](../../../docs/runbooks/customer-update-agent.md).

## Contract

- `POST /invocations`: body at most 1 MiB (413). An invalid payload answers 400
  `{"error": {"code": <ContractError code>, "message": "invalid run payload"}}`;
  missing `TABLE_NAME` or `KMS_KEY_ID` answers 503 `UNAVAILABLE`; a run already
  in progress answers 409 `BUSY`. The request then builds the run's wiring and
  claims the single-use `runId` (conditional `CURUN#{runId}/CLAIM` row) before
  any model call: a reused `runId` answers 409 `RUN_EXISTS`, a store or wiring
  failure 503 `UNAVAILABLE`. Otherwise one daemon thread runs `orchestrator.run`
  and the response is `{"accepted": true, "runId": ...}`.
- `GET /ping`: `HealthyBusy` while the run thread is alive, else `Healthy`, with
  `time_of_last_update` (set by the first ping or run, never at import).
- A run writes `DONE` only after every other row; any failure leaves no `DONE`.
  Logs carry `runId`, `urlHash[:12]`, codes, counts and exception class names,
  never URLs, queries, customer names, payloads or draft text. Library loggers
  never reach the container log (root `NullHandler`, levels at WARNING).
- Sealed text (`body`, `why`, `sourceTitle`) is raw: a re-check of a DRAFT row
  with `checks.check_draft` matches the run's check. A consumer posting through
  the Slack API escapes with `checks.escape_slack`.
- A gate failure (candidate scoring or a pair decision) writes the item
  `deferred`/`gate_error`, retryable in a later run; a pair whose decision
  failed has no DEC row.
- Bounds: `llm.MAX_TOKENS` per role; the orchestrator agent stops after 200
  tool calls or a third of `RUN_DEADLINE_SECONDS`, the researcher after 12 tool
  calls per pair (`llm.build_stoppable_agent` cancels the Strands loop); past the
  deadline the sweep raises `RunDeadline` and writes no results and no `DONE`.
- The reviewer also sees the account's insights and notes, fenced as internal
  material the draft must not quote or paraphrase, and the public news apart.

## Modules

| Module | Role |
|---|---|
| `app.py` | HTTP service: validation, one run thread, health |
| `contract.py` | `run-v1` payload parser (`ContractError` codes) |
| `orchestrator.py` | Run: optional orchestrator agent with four tools, then the deterministic sweep and result writes; `build_deps` real wiring |
| `reader.py` | Reads one article once (cache or fetch), summarizes from fetched text only |
| `gate.py` | Candidate customers and needed/not-needed decision (luna, no tools) |
| `ledger.py` | In-code gates and budgets (pairs, drafts, searches, fetches, rewrites) |
| `pipeline.py` | Research, write, check, review and one rewrite for a needed pair |
| `researcher.py`, `writer.py`, `reviewer.py` | Model steps; untrusted text fenced as data |
| `checks.py` | Deterministic draft checks (mirrored by the Go `CheckUpdateDraft`) |
| `tools.py` | Web search through the AgentCore gateway (SigV4) and bounded page fetch |
| `fetching.py` | Single import point for `crawler/article_fetcher.py` |
| `store.py`, `sealing.py` | `CUURL#` cache and `CURUN#` result rows; KMS envelope sealing (no decrypt) |
| `llm.py` | Bedrock model factory per role, strict JSON parsing |

## Environment

| Variable | Default | Purpose |
|---|---|---|
| `TABLE_NAME` | required | Main table (`ttobak-main`) |
| `KMS_KEY_ID` | required | Record key that seals result rows (AiStack `KmsKeyId` output) |
| `WEB_SEARCH_GATEWAY_URL` | required for search | Web search gateway MCP endpoint |
| `WEB_SEARCH_GATEWAY_REGION` | `us-east-1` | Gateway SigV4 region |
| `LUNA_MODEL_ID` | `global.openai.gpt-6-luna` | Reader, gate, reviewer |
| `WRITER_MODEL_ID` | `global.anthropic.claude-sonnet-5-5` | Researcher, writer |
| `ORCHESTRATOR_MODEL_ID` | luna | Orchestrator agent |
| `RUN_DEADLINE_SECONDS` | `1500` | Run deadline; the agent phase ends at a third of it |
| `AWS_REGION` / `AWS_DEFAULT_REGION` | `ap-northeast-2` | Region of every boto3 client and Bedrock |

A model ID outside the execution role's Bedrock grant fails at call time; the
role grants luna and Sonnet 5.5 only.

## Tests

Stdlib `unittest` with fake models; no test calls Bedrock, KMS, DynamoDB or the
network. `strands` is faked through `sys.modules` where needed; with
`strands-agents` installed, `test_llm` also runs a wiring check of the agent
stop against the real library and an offline fake model. Needs `boto3<2`,
`cryptography`, `fastapi` and `httpx`:

```bash
(cd backend/python/customer-update-agent && python3 -m unittest discover -p 'test_*.py' -v)
```

## Evaluating a gate backend

Two operator tools measure a candidate gate (for example a self-hosted model)
against real outcomes. Neither is scheduled or deployed.

```bash
python3 scripts/customer-updates/export_labels.py --user-id USER_ID --key-id KMS_KEY_ID --out labels.jsonl
(cd backend/python/customer-update-agent && python3 eval_gate.py --labels ../../../labels.jsonl --backend luna)
```

- Export reads the user's decisions and drafts and writes one line per labelled
  pair: adopted drafts are `needed`, drafts dismissed as not relevant are
  `not_needed`, everything else is excluded. Stdout shows counts only.
- The labelled population is biased. Only decisions the live gate admitted (needed
  at 0.7 or above) store their gate input, so every exported pair already passed
  the live gate, and a `not_needed` label means the gate said needed and the user
  said not relevant. Precision is the meaningful number. Recall is reported as
  `recall (among pairs the live gate admitted)` and says nothing about articles
  the live gate rejected.
- `--key-id` (or env `KMS_KEY_ID`) is required and is passed to `Decrypt`. KMS
  access or credential errors abort the run; other unopenable rows are counted as
  `unreadable`, and rows with malformed gate input as `malformed`. The tool exits
  non-zero when nothing was exported and some rows were unreadable. It never
  follows a symlink at the output path.
- The `luna` backend uses the production gate system prompt
  (`gate.GATE_SYSTEM_PROMPT`). A backend error on one pair counts as
  `not_needed` for that pair and is reported as `errors=N`.
- The export contains customer context. It is written with mode 0600, refuses to
  overwrite without `--force`, and must stay on the operator's machine.
- It needs the operator's own AWS credentials with `dynamodb:Query` and
  `kms:Decrypt` on the record key for the decision and draft sealing purposes.
  The runtime and worker roles do not grant that to the operator, and this
  tooling makes no IAM change.
- `eval_gate.py --backend luna|module:Class` runs the stored question, options and
  context through the backend, applies the gate's decision rule (`--threshold`,
  default 0.7), and prints the pair count, precision and recall for `needed`, and
  the same for the Korean subset (contexts with at least one Hangul syllable).
  Rates with a zero denominator print `n/a`.
- It exits 1 with `need at least 300 labelled pairs` when fewer are available.

## Image

`Dockerfile` (Python 3.13, ARM64, port 8080, `uvicorn app:app`) builds from the
`backend/python` context so it can copy `crawler/article_fetcher.py` and its only
local import `crawler/news_pipeline.py` next to the agent modules. Tests are not
copied; `test_app.py` checks that the COPY list matches the runtime modules.

```bash
docker build -f customer-update-agent/Dockerfile backend/python
```
