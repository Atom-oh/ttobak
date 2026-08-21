# ADR-030: 모바일 실시간 자막은 녹음을 희생하지 않는다

- **Status**: Accepted
- **Date**: 2026-08-19
- **PR**: #160 (#153의 후속)

## Context

#153에서 iOS Safari의 `RecordButton` 폴백 분기(`isIOS() || !supportsMediaRecorder()`)를 제거해, iOS도
데스크톱과 같은 정상 `MediaRecorder` 경로(실시간 자막, 파형, 일시정지/재개, 체크포인트)를 타도록 고쳤다 —
그 전에는 이 폴백의 `<input type="file" accept="audio/*" capture="environment">`가 `capture="environment"`
(후면 카메라 힌트) 때문에 Record를 누르면 마이크가 아니라 카메라가 열렸다.

그런데 이 변경으로 iOS/Android가 처음으로 데스크톱과 동일한 실시간 자막 경로를 타게 되면서, 이전에는 노출될
일이 없었던 문제가 드러났다: 기본 실시간 자막 provider가 `web-speech`(Browser Web Speech API)였고, 모바일
Safari/Chrome의 `SpeechRecognition`은 `MediaRecorder`가 녹음 중인 `MediaStream`과 무관하게 자체적으로
마이크를 잡는다 — 그 결과 모바일에서 마이크 트랙이 녹음 도중 강제로 종료되는 경우가 실측됐고
(PR #160의 앞선 커밋이 이 트랙 종료를 `onended`/`MediaRecorder.onerror`로 감지해 녹음을 정상 종료시키는
안전장치를 먼저 넣었다), 이 문제의 근본 원인을 이 PR에서 고친다.

## Decision

1. **실시간 자막의 기본 provider를 모든 플랫폼에서 AWS Transcribe Streaming으로 전환한다**
   (`app/record/page.tsx`의 `liveSttProvider` 초기값). Web Speech는 "동등한 선택지"가 아니라 Transcribe
   Streaming이 설정되지 않았거나 실패했을 때만 쓰는 폴백이다.
2. **모바일(iOS/iPadOS/Android)에서는 그 폴백 자체를 막는다** (`lib/sttManager.ts`의
   `SttManager.fallbackToWebSpeech`, `lib/device.ts`의 `hasMobileMicConflictRisk` — 실제 UA/터치 판정,
   `isMobile()`의 `window.innerWidth < 768` 휴리스틱과는 별개). Transcribe Streaming이 미설정이거나
   실패하면 자막만 사용 불가 상태(amber 배너)가 되고, 녹음 자체는 그대로 계속된다 — **자막을 위해 녹음을
   희생하지 않는다**는 것이 이 ADR의 핵심 정책이다. `LiveSttSelector`의 "Browser" 옵션도 모바일에서
   비활성화해 사용자가 이 위험한 조합을 직접 고를 수 없게 한다.
3. **모바일 마이크 레벨 프리뷰(idle 화면의 두 번째 `getUserMedia`)를 모바일에서 생략한다**
   (`record/page.tsx`) — 실제 녹음용 스트림이 열리기 전까지 정리되지 않아 두 마이크 스트림이 겹치는
   구간이 생겼고, 이 역시 트랙 종료 위험을 더했다.
4. **`AudioContext`의 `sampleRate: 48000` 강제를 제거한다** (`lib/transcribeStreamingClient.ts`) —
   `public/pcm-processor.js`가 워클릿 전역 `sampleRate` 기준으로 이미 임의 비율 다운샘플을 하므로 불필요했고,
   블루투스 헤드셋처럼 하드웨어 레이트가 다른 상황에서 두 번째 `AudioContext` 생성 실패 위험만 더했다.
5. **녹음 시작이 런타임 config(Cognito identity pool + 커스텀 vocabulary) 로딩보다 빠른 경합을 다룬다**
   (`SttManager.retryWithConfig`, `useRecordingSession`) — config가 늦게 도착해도 그 녹음의 나머지
   전체가 자막 없이 굳어버리지 않고, 도착 즉시 Transcribe Streaming으로 승격한다. 일시정지 중에는 config만
   저장해 두고 재개 시점에 단일 세션으로 승격한다(재개 로직이 이미 하던 "일시정지 중 세션 정지 → 재개 시
   재시작"과 경합해 세션이 중복 생성되는 것을 피하기 위함).

## Consequences

- 모바일에서 Web Speech가 녹음 도중 마이크를 빼앗는 경로가 원천적으로 막힌다. 대가로, Transcribe
  Streaming이 설정되지 않은 배포(로컬 개발 등)의 모바일 사용자는 자막을 전혀 받지 못한다 — 이전에는 최소한
  Web Speech로 자막을 받았을 상황이다. 의도된 트레이드오프다.
- 기본 provider가 전 플랫폼에서 Transcribe Streaming으로 바뀌므로, config가 준비된 배포에서는 데스크톱을
  포함해 모든 신규 녹음이 기본적으로 AWS로 오디오를 스트리밍한다. 이 제품의 오디오는 배치 파이프라인
  (S3 → Whisper ECS → Bedrock 요약)에서도 이미 전부 AWS로 가므로 새로운 신뢰 경계는 아니지만, 동시 스트림
  수·과금 프로필이 달라진다 — 인프라 변경(CDK)은 필요 없다(Transcribe Streaming은 이미 배포되어 있던
  opt-in 경로였을 뿐이다).
- `SttManager`에 `paused`/`stopped`/`preferredProvider` 내부 상태가 추가되어 pause/resume/stop과
  `retryWithConfig`의 상호작용이 더 복잡해졌다 — 대가로 pause 중 config 도착 시 세션 중복 생성, stop 이후
  도착하는 비동기 실패가 다시 마이크를 여는 문제, 그리고 사용자가 데스크톱에서 명시적으로 고른
  `web-speech` 선택이 pause/resume 중 config 도착만으로 AWS로 무단 전환되는 문제를 막는다.
- 데스크톱에서 Web Speech가 폴백(또는 명시적 선택)으로 쓰이는 동안에는 회의 오디오가 브라우저 벤더의 음성
  인식 서비스(Chrome이면 Google)로 나간다 — 이 PR 이전부터의 기본 동작이었고 이 PR은 그 경로를 줄이는
  방향이지만, 새로 생긴 동작은 아니라는 점을 명시한다.
- Transcribe Streaming이 기본값이 되면서 동시 스트림 수가 늘어날 수 있다 — 필요 시 사용량/과금 알람 검토
  대상. 권한은 이미 `infra/lib/auth-stack.ts`의 인증된 identity pool 역할에
  `transcribe:StartStreamTranscriptionWebSocket` 단일 액션으로 배포되어 있어(최소 권한), 이 결정에 필요한
  새 IAM/CDK 변경은 없다.
- 서버측(Go/CDK) 변경 없음 — 프론트엔드 전용 결정.
