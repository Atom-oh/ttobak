# Historical three-engine STT benchmark

Historical run: 2026-04-23. Evaluator: Claude Sonnet 4.6. Compared managed
multi-language AWS Transcribe, faster-whisper large-v3 CPU/int8, and GPU/float16 on
g5.xlarge (A10G 24GB, recorded CUDA 13.2). These are run settings, not current pins.

| Recording | Size | Transcribe | Whisper CPU | Whisper GPU |
|---|---|---|---|---|
| Hana Bank R&D network | 75MB | about 3m | 71m | 7.9m |
| Hana financial technology research | 78MB | about 3m | 62m | 6.2m |
| Mobile research recording | 78MB | about 3m | Not run | 6.4m |
| Total | | about 9m | 133m for two | 20.4m |

| Recording | Transcribe characters | CPU characters | GPU characters |
|---|---|---|---|
| R&D network | about 30,000 | 34,282 | 35,181 |
| Research | 37,441 | 36,457 | 36,269 |
| Mobile | 36,729 | Not run | 36,016 |

For the research recording, the evaluator rated Transcribe/GPU respectively:
AWS names 3/8, Korean language 5/6, technical terms 3/8, mixed English 4/9 and
readability 4/6, with a recorded overall 3.5/7.5. GPU better preserved examples
such as EKS, GPU and Evaluation; neither output was perfect. These are subjective
rubric scores, not proof of twice the accuracy or a controlled WER study.

Historical one-hour cost estimates were $1.44 Transcribe, $0.04 GPU Spot and $0.12
GPU on-demand. They are not current prices or measured end-to-end bills. The
original near-zero CPU estimate excluded host opportunity/runtime cost and should
not be used for budgeting. The observed GPU speedup versus the two CPU runs was
about 9-10x for this sample.

The recommendation was asynchronous GPU Whisper after upload with separate live
captions. The old proposed Fargate/ALB path and baked-in-model/zero-cold-start claim
were not the final design. Current Whisper uses EC2 GPU Spot tasks, S3 model
loading, zero-scale startup and current exact pins; see ADR-009/035 and the
[WhisperX runbook](runbooks/whisperx-benchmark.md).
