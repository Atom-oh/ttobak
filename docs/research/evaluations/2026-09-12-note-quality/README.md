# Real note quality evaluation — 2026-09-12

Four synthetic meetings passed all 25 corpus checks using the production
`SummarizeTranscript` request and completion-processing paths. Every generated
note was also read in full. This is regression evidence for these cases, not an
estimate of general meeting accuracy.

The main-branch Evaluate Note Quality workflow, run **34708738649**, completed
four real Bedrock calls at 17:38 UTC. It used
`global.anthropic.claude-opus-5` in `us-west-2`, from source commit
`350cf4738948bbf8dd316159c0116acd6b96a654`. No customer data was loaded.
The deployed function's selected model configuration is preserved in
`deployed-model.json`; its code hash records the deployment observed at startup,
not proof that the deployed binary already matched the checked-out prompt.

| Case | Checks | Manual inspection | Input / output tokens |
| --- | --- | --- | --- |
| Selected B, numbers and negation | 7/7 | Preserved 120만원, 30ms, CPU 30% and unapproved deployment; did not create a follow-up task | 1,972 / 1,049 |
| Proposal versus decision | 6/6 | Rolling deployment remained decided; blue-green remained a proposal; no tasks were invented | 1,993 / 836 |
| Owners and deadlines | 6/6 | Preserved 민수 and 2026-09-18; backup verification kept its unknown owner and deadline | 1,957 / 946 |
| Notes versus spoken evidence | 6/6 | Labeled 53만원 and 서연 as user notes, separately from spoken facts; did not invent attendance or attach an audio citation to the memo amount | 2,413 / 684 |

Total provider usage: **8,335 input / 3,515 output tokens**. The exploratory
single-case probe reported elsewhere is separate from this run.

## Preserved evidence and regrading

`live-report.json` is the unmodified workflow report. Each case's `.request.json`,
`.response.json`, `.evidence.json` and `.md` preserve the exact request, provider
response, call metadata and rendered note. All eight request/response SHA-256
hashes were checked against their evidence records after download.

During review, the second case's action criterion was found to reject invented
blue-green tasks but accept an unrelated invented task. Two new deliberate bad
answers reproduced that gap. Enabling its existing `noTasks` check rejects both
checkbox and plain-bullet tasks. No input, production prompt or real response
was changed to obtain a pass.

`report.json` regrades the same four responses with this stricter corpus and
still passes 25/25. Its corpus hash identifies the revised fixture in this PR;
the report's `commit` field records the base commit before that fixture edit.
Grade mode validated the exact regenerated request bytes and all evidence
hashes. **It made no additional model calls**; `invoked=true` refers to the
archived live calls.

To reproduce, copy this directory to a scratch location and run:

```bash
BEDROCK_MODEL_ID=global.anthropic.claude-opus-5 BEDROCK_REGION=us-west-2 \
  scripts/eval-note-quality.sh grade /absolute/scratch-copy
```

## Limits and remaining questions

There is one sample per short fictional case. The rubric uses targeted
patterns, not a semantic judge. It verifies allowed citation IDs but does not
establish claim-by-claim timestamp alignment. Some paragraphs combine several
facts behind one valid timestamp.

The owner/deadline case lists backup verification with unknown fields as the
reference allows, while its narrative calls the task unconfirmed. Whether this
belongs in a separate proposed-work section needs broader product evaluation.
Long meetings, overlapping speakers, recognition errors, document-heavy
meetings and multilingual variation are outside this run.
