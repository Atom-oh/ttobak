# ADR-019: Adopt pyannote.audio for Acoustic Speaker Diarization

> **Partially superseded by [ADR-035](ADR-035-diarization-pyannote4-community1-asr-pins.md)
> (2026-09-03)**: the model-choice portion (pyannote 3.1 bundle) is
> superseded — production now runs pyannote 4.x community-1. Everything
> else here (same-GPU post-transcription diarization, `max_speakers`
> semantics, preserve-mode refine, `spk_N` namespacing) remains in force.

<a href="#english"><img src="https://img.shields.io/badge/lang-English-blue.svg" alt="English"></a>
<a href="#korean"><img src="https://img.shields.io/badge/lang-한국어-red.svg" alt="Korean"></a>

---

<a id="english"></a>

# English

## Status
Accepted

## Context
The default batch STT path (Whisper GPU on ECS Spot, ADR-009) performs no acoustic speaker diarization. `transcribe.py` outputs segments with only `start`/`end`/`text` — no speaker information. Speaker labels (`spk_0`, `spk_1`, ...) are assigned afterward by Claude Sonnet in the summarize Lambda's `RefineTranscript`, which infers speakers purely from conversational text (`backend/internal/service/bedrock.go`'s `refineChunk`).

This produces real conflation bugs: in an 8-person meeting (3 customer + 5 AWS, with 3 main speakers — Account Manager, Solutions Architect, and Customer Success Engineer), the SA and CSE were merged into the same speaker label because their speaking roles and topics were textually similar enough that the LLM couldn't tell them apart from text alone. The AWS Transcribe fallback path does have real diarization (`ShowSpeakerLabels`), but Whisper is the default because of its far better Korean/English code-switching accuracy (ADR-009), and switching back to Transcribe would regress transcript quality to fix diarization.

Additional contributing factors identified in code:
- `meeting.Participants` (headcount, `backend/internal/model/meeting.go`) exists but was never used as a speaker-count hint anywhere in the pipeline.
- For meetings longer than ~25 minutes, `refineParallel` processes chunks concurrently and only gives every parallel chunk the first chunk's speaker-label tail as a hint, so labels can drift across chunk boundaries by design (documented in the existing code comment).

## Options Considered

### Option 1: pyannote.audio speaker-diarization-3.1 in the Whisper container
- **Pros**: Acoustic (voice-based) diarization, language-agnostic, runs on the same GPU already provisioned for Whisper (sequential VRAM use fits easily in A10G's 24GB), model weights can be bundled via S3 the same way the Whisper model already is (no runtime HuggingFace dependency after a one-time bundling step)
- **Cons**: Increases the container image size (torch + torchaudio, ~4-5GB); the three underlying HF model repos are gated and require one-time account approval + an `HF_TOKEN` to bundle; diarization accuracy on heavily overlapping speech is still imperfect

### Option 2: NVIDIA NeMo diarization toolkit
- **Pros**: Also acoustic, mature
- **Cons**: Heavier dependency footprint than pyannote, no clear accuracy advantage for this use case, less common/simpler integration path than pyannote for a single ffmpeg→wav→pipeline flow

### Option 3: Switch the default STT path to AWS Transcribe (already has diarization)
- **Pros**: No new dependency, `ShowSpeakerLabels` already implemented
- **Cons**: Regresses transcript quality from 7.5/10 back to 3.5/10 on Korean-English mixed technical meetings (ADR-009) — trades a diarization fix for a much larger transcription-quality regression. Rejected.

### Option 4: Pass `meeting.Participants` headcount as a text hint only (no acoustic model)
- **Pros**: Zero new dependencies, trivial change
- **Cons**: Doesn't fix the root cause — the LLM still can't distinguish two textually-similar speakers (e.g. SA vs. CSE) without hearing their voices. Kept as a complementary improvement (Task 4 below), not a substitute.

## Decision
**Option 1: pyannote.audio speaker-diarization-3.1**, run inside the existing Whisper ECS container after transcription completes, sequentially on the same GPU.

Architecture:
```text
audio -> ffmpeg (mono 16kHz wav) -> pyannote diarization pipeline -> speaker turns
                                                                          |
Whisper transcription -> segments (start/end/text) -----------> overlap-based merge
                                                                          |
                                                          segments with acoustic "speaker"
                                                                          |
                                                        summarize Lambda (RefineTranscript):
                                                        preserve given labels, clean text only
```

Key design decisions:
- **Sequential execution on one GPU**: Whisper (~3GB VRAM) then pyannote (~1GB VRAM) — both fit easily within the A10G's 24GB, so no additional GPU capacity is needed.
- **Model weights bundled via S3**, same pattern as the existing Whisper model (`upload-model.sh` → `_ensure_model()`): a one-time `upload-diarization-model.sh` script (run by an operator with `HF_TOKEN` after accepting the three gated HF model terms) downloads and re-packages the pipeline config to reference local paths, so the running container has zero HuggingFace dependency.
- **Overlap-based segment assignment**: each Whisper segment is assigned the diarization turn with maximum time overlap; segments with zero overlap (silence gaps) fall back to the closest turn by midpoint distance. Raw pyannote labels are normalized to `spk_N` in first-appearance order to match the existing `speakerMap`/frontend convention.
- **Refine prompt gains a preserve mode**: when segments carry an acoustic `speaker` field, `RefineTranscript`'s system prompt tells the model the labels are authoritative — text cleanup only, never relabel/merge across speakers. Without a `speaker` field (older transcripts, diarization failure), the original text-inference prompt is used unchanged as a fallback. This also removes the chunk-boundary drift problem for new transcripts, since identity now lives in the data rather than being re-inferred per chunk.
- **`meeting.Participants` headcount now flows through** as pyannote's `max_speakers` upper bound (`backend/cmd/transcribe/main.go` → `NUM_SPEAKERS` env → `transcribe.py`, passed as `max_speakers` rather than `num_speakers`) — pyannote still auto-detects the actual count within that bound, which matters because a registered headcount can exceed the number of people who actually spoke (e.g. invited-but-silent attendees); forcing an exact count in that case would over-split a single speaker into several labels.
- **Diarization is best-effort**: any failure (missing S3 bundle, pyannote exception) is caught and logged; the container falls back to unlabeled segments rather than failing the transcription job.

## Consequences

### Positive
- Speaker labels for new Whisper transcripts come from voice, not text guessing — fixes the SA/CSE conflation class of bug.
- `meeting.Participants` is finally used, improving diarization accuracy further when the headcount is known.
- The multi-chunk label-drift caveat in `refineParallel` is eliminated for diarized transcripts.
- Labels remain user-correctable via the existing `SpeakerMapEditor`/`UpdateSpeakers` API — no UI changes required.

### Negative
- Container image grows by several GB (torch + torchaudio + pyannote.audio); Whisper ECS task GPU time increases by roughly 1-3 minutes per meeting for the diarization pass.
- One-time manual operator step required: accept HuggingFace gating for `pyannote/speaker-diarization-3.1`, `pyannote/segmentation-3.0`, and `pyannote/wespeaker-voxceleb-resnet34-LM`, then run `upload-diarization-model.sh` with an `HF_TOKEN` before the new container image is deployed.
- pyannote's accuracy on heavily overlapping Korean speech is not perfect — this is a large improvement over pure text inference, not a complete fix. Old transcripts and any transcript where diarization fails still fall back to the pre-existing text-inference behavior.

## References
- [ADR-009: Whisper GPU on ECS Spot](ADR-009-whisper-gpu-ecs-spot-zero-scale.md) — why Whisper (not AWS Transcribe) is the default STT engine, and why this ADR extends it rather than switching engines
- [pyannote.audio](https://github.com/pyannote/pyannote-audio) — speaker diarization toolkit
- `backend/whisper/transcribe.py`, `backend/whisper/upload-diarization-model.sh` — implementation
- `backend/internal/service/bedrock.go`'s `refineChunk`/`buildRefineSystemPrompt` — preserve-vs-infer prompt modes

---

<a id="korean"></a>

# 한국어

## 상태
승인됨

## 배경
기본 배치 STT 경로(ECS Spot의 Whisper GPU, ADR-009)는 음향 기반 화자분리를 전혀 수행하지 않습니다. `transcribe.py`는 `start`/`end`/`text`만 있는 세그먼트를 출력하며, 화자 라벨(`spk_0`, `spk_1` 등)은 이후 summarize Lambda의 `RefineTranscript`가 Claude Sonnet에게 **텍스트 흐름만으로** 화자를 추측시켜 부여합니다(`backend/internal/service/bedrock.go`의 `refineChunk`).

이로 인해 실제 화자 혼동 버그가 발생합니다: 8명이 참여한 미팅(고객사 3명 + AWS 5명, 주 화자는 AM/SA/CSE 3명)에서 SA와 CSE의 역할·주제가 텍스트만으로는 구분하기 어려울 만큼 비슷해 같은 화자 라벨로 병합되었습니다. AWS Transcribe fallback 경로는 실제 diarization(`ShowSpeakerLabels`)을 갖고 있지만, Whisper가 한영 코드 스위칭 정확도에서 훨씬 우수하기 때문에(ADR-009) 기본 경로로 유지되고 있으며, diarization을 고치려고 Transcribe로 되돌리면 전사 품질이 크게 후퇴합니다.

코드에서 확인된 추가 원인:
- `meeting.Participants`(참가자 수, `backend/internal/model/meeting.go`)가 존재하지만 파이프라인 어디에서도 화자 수 힌트로 사용되지 않았습니다.
- 25분 이상 미팅은 `refineParallel`이 청크를 병렬 처리하며 모든 병렬 청크에 첫 청크의 화자 tail만 힌트로 제공하므로, 청크 경계에서 라벨이 드리프트할 수 있습니다(기존 코드 주석에도 명시됨).

## 검토한 옵션

### 옵션 1: Whisper 컨테이너에 pyannote.audio speaker-diarization-3.1 추가
- **장점**: 음향(음성) 기반 diarization, 언어 무관, Whisper용으로 이미 프로비저닝된 동일 GPU에서 순차 실행 가능(A10G 24GB VRAM에 여유 있게 적합), Whisper 모델과 동일한 S3 번들링 패턴 사용 가능(1회 번들링 후 런타임 HuggingFace 의존성 없음)
- **단점**: 컨테이너 이미지 크기 증가(torch + torchaudio, ~4-5GB), 기반 HF 모델 3개가 gated라 1회 계정 동의 + `HF_TOKEN` 필요, 심하게 겹치는 발화에서는 정확도가 완벽하지 않음

### 옵션 2: NVIDIA NeMo diarization 툴킷
- **장점**: 역시 음향 기반, 성숙한 프로젝트
- **단점**: pyannote보다 무거운 의존성, 이 사용 사례에서 명확한 정확도 우위 없음, ffmpeg→wav→pipeline 흐름에는 pyannote가 더 간단

### 옵션 3: 기본 STT 경로를 AWS Transcribe로 전환(이미 diarization 있음)
- **장점**: 새 의존성 불필요, `ShowSpeakerLabels` 이미 구현됨
- **단점**: 한영 혼용 기술 미팅 전사 품질이 7.5/10에서 3.5/10으로 후퇴(ADR-009) — diarization 버그를 고치려고 훨씬 큰 전사 품질 후퇴를 감수하는 셈. 기각.

### 옵션 4: `meeting.Participants` 인원수를 텍스트 힌트로만 전달(음향 모델 없음)
- **장점**: 새 의존성 없음, 변경 사소
- **단점**: 근본 원인을 해결하지 못함 — LLM은 목소리를 듣지 않고는 여전히 텍스트가 비슷한 두 화자(예: SA vs CSE)를 구분할 수 없음. 대체가 아닌 보완책으로만 채택(아래 Task 4).

## 결정
**옵션 1: pyannote.audio speaker-diarization-3.1**을 기존 Whisper ECS 컨테이너 내부에서, 전사 완료 후 동일 GPU에서 순차 실행합니다.

주요 설계 결정:
- **동일 GPU에서 순차 실행**: Whisper(~3GB VRAM) 이후 pyannote(~1GB VRAM) — A10G 24GB에 여유 있게 적합하므로 추가 GPU 용량 불필요
- **모델 가중치는 S3로 번들링**, 기존 Whisper 모델과 동일 패턴(`upload-model.sh` → `_ensure_model()`): 운영자가 `HF_TOKEN`으로 1회 실행하는 `upload-diarization-model.sh`가 3개 gated HF 모델을 다운로드하고 config를 로컬 경로로 재작성 — 이후 런타임은 HuggingFace 의존성 zero
- **겹침 기반 세그먼트 배정**: 각 Whisper 세그먼트에 시간 겹침이 최대인 diarization turn의 화자를 배정, 겹침이 없는 세그먼트(무음 구간)는 중점 거리가 가장 가까운 turn으로 fallback. pyannote 원본 라벨은 등장 순서대로 `spk_N`으로 정규화해 기존 `speakerMap`/프론트엔드 관례 유지
- **Refine 프롬프트에 보존 모드 추가**: 세그먼트에 음향 `speaker` 필드가 있으면 `RefineTranscript` 시스템 프롬프트가 해당 라벨을 authoritative로 취급하도록 지시(텍스트 정제만, 화자 간 재라벨/병합 금지). `speaker` 필드가 없으면(구 트랜스크립트, diarization 실패) 기존 텍스트 추론 프롬프트를 그대로 fallback으로 사용. 이는 신규 트랜스크립트에서 청크 경계 드리프트 문제도 함께 해소함(화자 정체성이 매 청크 재추론이 아니라 데이터에 있으므로)
- **`meeting.Participants` 인원수가 마침내 사용됨**: pyannote의 `max_speakers` 상한으로 전달(`backend/cmd/transcribe/main.go` → `NUM_SPEAKERS` env → `transcribe.py`, `num_speakers`가 아닌 `max_speakers`로 전달됨) — pyannote는 이 상한 내에서도 실제 화자 수를 자동 감지합니다. 등록된 참석자 수가 실제 발화자 수보다 많을 수 있기 때문(예: 참석했지만 발화하지 않은 참석자)에 중요한 차이이며, exact-count로 강제할 경우 한 화자가 여러 라벨로 잘못 분할될 수 있습니다.
- **Diarization은 best-effort**: 실패(S3 번들 누락, pyannote 예외 등) 시 로그만 남기고 라벨 없는 세그먼트로 폴백 — 전사 작업 자체는 절대 실패시키지 않음

## 영향

### 긍정적
- 신규 Whisper 트랜스크립트의 화자 라벨이 텍스트 추측이 아닌 음성에서 나옴 — SA/CSE 혼동 같은 버그 유형을 해결
- `meeting.Participants`가 마침내 사용되어, 인원수가 알려진 경우 diarization 정확도가 더 개선됨
- `refineParallel`의 다중 청크 라벨 드리프트 한계가 diarized 트랜스크립트에서는 해소됨
- 라벨은 기존 `SpeakerMapEditor`/`UpdateSpeakers` API로 여전히 사용자가 수정 가능 — UI 변경 불필요

### 부정적
- 컨테이너 이미지가 수 GB 증가(torch + torchaudio + pyannote.audio); Whisper ECS 태스크의 미팅당 GPU 시간이 diarization 단계로 약 1-3분 증가
- 1회 수동 운영자 절차 필요: HuggingFace에서 `pyannote/speaker-diarization-3.1`, `pyannote/segmentation-3.0`, `pyannote/wespeaker-voxceleb-resnet34-LM` gating 동의 후 `HF_TOKEN`으로 `upload-diarization-model.sh` 실행 — 신규 컨테이너 이미지 배포 전에 완료 필요
- 심하게 겹치는 한국어 발화에서 pyannote 정확도가 완벽하지는 않음 — 순수 텍스트 추론 대비 큰 개선이지만 완전한 해결은 아님. 구 트랜스크립트와 diarization 실패 시에는 기존 텍스트 추론 동작으로 폴백

## 후속 업데이트 (2026-08): 사용자 지정 화자 수로 재분석

pyannote는 `max_speakers` 상한 내에서 실제 화자 수를 자동 감지하므로(위 "결정" 절
참조), 등록된 참석자 수보다 실제 발화자가 적으면 문제없지만 그 반대(예: 6명이
발화했는데 4명만 감지)는 순수 acoustic 오탐으로 남아 있었다 — 이 ADR이 채택한
best-effort 폴백만으로는 사후 교정 수단이 없었다.

`POST /api/meetings/{meetingId}/rediarize`(`UploadService.RediarizeMeeting`,
API-SPEC.md 참조)가 이 교정 수단을 추가한다: 사용자가 지정한 화자 수를
`Meeting.DiarizationSpeakerHint`에 저장해 이후 모든 재전사에서 `len(Participants)`
대신 `max_speakers` 힌트로 사용하고(sticky), 기존 오디오를 새 S3 키로
`CopyObject`해 이 ADR이 구축한 동일 EventBridge → `ttobak-transcribe` → Whisper
ECS(+pyannote) 파이프라인을 그대로 재사용한다 — ECS `RunTask`를 직접 호출하지
않으므로 새 IAM 권한이 필요 없다. Whisper 전용(AWS Transcribe 폴백 미팅은
acoustic diarization이 없어 대상 아님)이며, v1은 단일 파트 오디오로 스코프를
제한한다(멀티파트는 파트별 재트리거 + `AudioPartsReady` 리셋이 추가로 필요).

## 참고 자료
- [ADR-009: ECS Spot의 Whisper GPU](ADR-009-whisper-gpu-ecs-spot-zero-scale.md) — Whisper(AWS Transcribe 아님)가 기본 STT 엔진인 이유, 그리고 이번 ADR이 엔진 전환이 아니라 확장인 이유
- [pyannote.audio](https://github.com/pyannote/pyannote-audio) — 화자분리 툴킷
- `backend/whisper/transcribe.py`, `backend/whisper/upload-diarization-model.sh` — 구현
- `backend/internal/service/bedrock.go`의 `refineChunk`/`buildRefineSystemPrompt` — 보존/추론 프롬프트 모드
- `backend/internal/service/upload.go`의 `RediarizeMeeting`/`validateRediarizeEligibility` — 사용자 지정 화자 수 재분석 구현
