# Actual model probe: inferred follow-up task

Historical probe: on 2026-09-12, the AWS MCP connector successfully invoked
`bedrock-runtime.InvokeModel` in `us-west-2` with the deployed summarizer model,
`global.anthropic.claude-opus-5`. The tool's `api_calls` record reported success.
This was one exploratory synthetic case, not a completed corpus evaluation.

The request used the production prompt and the selected B transcript exported
by the evaluator. The exported request SHA-256 was
`3a6a728dd190d02f4f636cd7c1341a78374e197e446d2d359deded0aab7f4937`.
The connector returned the sent request body for comparison. No customer
records were read or modified.

The synthetic transcript stated only:

- Monthly budget: KRW 1,200,000.
- Latency: 30 milliseconds; CPU usage: 30 percent.
- This week's deployment was not approved.

The model retained these facts but invented a follow-up task. English paraphrase:
check why deployment was not approved and when to resubmit, with unknown owner
and deadline, citing TS:20. The original response was Korean; this description
is not a verbatim provider response.

This is an inferred recommendation presented as an agreed task. The initial
numeric/negation rubric did not cover that omission; add an explicit no-task
criterion and a deliberate bad-answer mutation before accepting future results.
Both the summary and action-extraction prompts must prohibit inventing follow-up
work from missing information, problems or non-approval.

Provider response metadata:

- Model: `claude-opus-5`
- Response ID: `msg_bdrk_frqgl4e6osnn35nob2an7pbhdaoz3c4faignw6p6kuovdzouomaq`
- Stop reason: `end_turn`
- Input tokens: 1838
- Output tokens: 1386, including 191 thinking tokens

The provider's opaque thinking signature is not reproduced here. A fresh,
complete live corpus run with retained response artifacts is still required
after the prompt change; this probe must not be reported as a passing evaluation.
