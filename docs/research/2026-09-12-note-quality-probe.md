# Actual model probe: inferred follow-up task

On 2026-09-12, the AWS MCP connector successfully invoked
`bedrock-runtime.InvokeModel` in `us-west-2` with the deployed summarizer model,
`global.anthropic.claude-opus-5`. The tool's `api_calls` record reported success.
This was one exploratory synthetic case, not a completed corpus evaluation.

The request used the production prompt and the selected B transcript exported
by the evaluator. The exported request SHA-256 was
`3a6a728dd190d02f4f636cd7c1341a78374e197e446d2d359deded0aab7f4937`.
The connector returned the sent request body for comparison. No customer
records were read or modified.

The synthetic transcript stated only:

- Monthly budget: 120만원.
- Latency: 30밀리초; CPU usage: 30퍼센트.
- This week's deployment was not approved.

The model retained these facts, but added this action item without source support:

> - [ ] 담당자 미정: 배포 미승인 사유 및 재상정 시점 확인 — 녹취록에 담당자·기한 언급 없음 (미정) [TS:20]

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
