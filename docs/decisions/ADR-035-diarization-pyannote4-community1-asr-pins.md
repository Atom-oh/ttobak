# ADR-035: 프로덕션 화자분리를 pyannote 4.x community-1로 교체하고 ASR 스택을 exact-pin 동결

- Status: 승인됨 (Accepted)
- Date: 2026-09-03
- Supersedes: ADR-019의 "모델 선택" 부분 (pyannote 3.1 번들). ADR-019의
  나머지 — 전사 후 동일 GPU에서 diarization 실행, `max_speakers` 상한 의미론,
  preserve-mode refine, `spk_N` 네임스페이싱 — 는 그대로 유효하다.

## Context

ADR-019는 pyannote.audio 3.1(런타임 해석으로 3.4.0) 기반 화자분리를
도입했지만, 실제 회의에서 유령 화자가 반복 관찰됐다(20분 회의 a435a3dc:
실제 4명+한두마디 2명인데 8명 검출, 4명은 발화시간 ≤3.3%의 유령).

AWS가 WhisperX 3.8.6 DLC(pyannote.audio 4.x 포함)를 발표하면서 엔진 교체를
평가했다(Phase 1: 격리된 벤치 이미지·task def, 제품 무변경 —
`docs/runbooks/whisperx-benchmark.md`). 4개 회의, 그중 2개는 운영자 확인
ground truth로 벤치한 결론:

1. **화자분리 개선의 원천은 whisperx가 아니라 pyannote community-1 모델이다.**
   기존 faster-whisper ASR + community-1 조합(`ENGINE=fw_p4` 하이브리드 벤치
   엔진)이 a435a3dc에서 4명 정확 검출 + 레거시가 놓친 실제 내용 147s 회수
   (레거시 대비 잔여 gap 87s vs whisperx 최적 설정 143s), 3804e0f5(GT ~4명)
   에서 주 화자 4명 + 한두마디 2명, >5s coverage gap 0을 기록했다.
2. **whisperx 벤치 이미지의 ASR 스택은 신뢰할 수 없다.** 64분 회의
   5944e3df에서 연속 ~5분 블록(857–1163s)을 무음으로 누락했고(실제 대화
   내용 확인), 현재 프로덕션 이미지는 같은 오디오를 완전 전사했다. 두
   이미지를 `importlib.metadata`로 직접 조사한 결과 faster-whisper(1.2.1)·
   av(18.1.0)·onnxruntime(1.29.0)은 동일 — 차이는 ctranslate2(4.8.1 vs
   4.8.2)와 torch/CUDA(2.5.1/cu124 vs 2.8.0/cu128) 조합뿐이다.
3. Qwen3-ASR 제3 엔진 검토는 불필요해졌다: 화자분리는 같은 pyannote를
   쓰므로 이득이 없고, 정확성 축은 기존 whisper가 이미 최선이다.

운영자 결정 기준은 "속도보다 정확성·화자분리, 기존 whisper가 정확하면
whisper 유지"였다.

## Decision

1. **whisperx 컷오버·Qwen3 도입 없이**, 프로덕션 엔진(`transcribe.py`)의
   화자분리 모델만 pyannote 4.x `speaker-diarization-community-1`
   (`pyannote.audio==4.0.7`, 벤치에서 검증된 정확한 버전)로 교체한다.
   번들은 Phase 1이 이미 스테이징한 `models/whisperx-diarization-4.x.tar.gz`
   를 재사용한다(이름은 bench 유래, 내용은 엔진 중립인 self-contained
   community-1 파이프라인).
2. **ASR 경로 패키지는 검증된 2026-08-28 프로덕션 이미지의 해석 결과로
   exact-pin 동결한다**: faster-whisper==1.2.1, ctranslate2==4.8.1,
   onnxruntime==1.29.0, av==18.1.0, tokenizers==0.23.1,
   huggingface-hub==1.29.0. 특히 ctranslate2는 위 5분 누락의 유력
   differentiator이므로 절대 함께 올리지 않는다.
3. torch/torchaudio 2.5.1→2.8.0(cu128)은 pyannote 4.0.7의 `torch>=2.8.0`
   floor가 강제한 것으로, faster-whisper의 ASR 경로(ct2/onnxruntime/av)에는
   torch가 전혀 쓰이지 않으므로 ASR 동작 변화가 아니다. torchcodec은
   디코드 경로 전용이며 `_load_wav_waveform` waveform preload로 우회된다.

## Rollback

번들 키와 이미지 pin은 **함께** 롤백해야 한다: `whisper-stack.ts`의
`DIARIZATION_S3_KEY`를 `models/pyannote-diarization-3.1.tar.gz`로 되돌리는
것(CDK 하드코딩 값 — 스택 재배포 필요)만으로는 부족하고, Dockerfile의
`pyannote.audio==4.0.7`/torch 2.8 pin도 함께 되돌려 이미지를 재빌드해야
한다. 4.0.7 코드가 3.1 번들을 로드하면(또는 그 반대) config/체크포인트
비호환으로 실패하고 `_diarize`가 예외를 삼켜 **unlabeled fallback으로
조용히 저하**되기 때문이다. 배포 순서 주의사항은 INFRA-SPEC §7과 runbook을
따른다.

## Consequences

- 유령 화자 제거(ground-truth 검증), 레거시 ASR recall 그대로 유지.
- 이미지(`deploy-whisper.yml`)와 task-def env(`deploy-infra.yml`)가 별개
  워크플로로 배포되므로, 한쪽만 배포된 기간에는 구/신 조합 비호환으로
  diarization이 조용히 비활성화된다 — 두 워크플로를 연속 실행해 창을
  최소화하고, 사전에 프로덕션 버킷에 community-1 번들 존재를 확인한다.
- pyannote 3.1 번들과 `upload-diarization-model.sh`는 롤백 경로로 S3/repo에
  유지한다. community-1 스테이징은 `upload-whisperx-diarization-model.sh`.
- 벤치 상세와 재현 절차는 `docs/runbooks/whisperx-benchmark.md` §3b/§3c/§6.
