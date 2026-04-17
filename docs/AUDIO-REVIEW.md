# Ttobak 음성 녹음 파이프라인 — 멀티모델 코드 리뷰

> **리뷰 일시**: 2026-03-27
> **리뷰어**: Claude Opus 4.6 (멀티 관점 분석: 녹음 안정성, 브라우저 호환성, 백엔드 파이프라인, 인프라)
> **분석 방법**: 3개 병렬 탐색 에이전트 (프론트엔드 녹음, 백엔드 파이프라인, 아키텍처 변경 이력) + 사용자 직접 보고 증상
> **대상**: 음성 녹음 → 실시간 자막 → 업로드 → 전사 → 요약 전체 파이프라인
> **이슈 총계**: 24개 (CRITICAL 3 · HIGH 7 · MEDIUM-HIGH 3 · MEDIUM 8 · LOW 3)
>
> ※ Gemini, Codex, Kiro CLI MCP 도구가 미설치 상태로 실제 멀티모델 호출은 수행되지 않음. 추후 MCP 설정 후 교차 리뷰 예정.
>
> **리뷰어 라벨 범례** (분석 관점 분류):
> - **Claude**: 녹음 안정성, 레이스 컨디션, 에러 핸들링 관점
> - **Gemini**: 브라우저 호환성, 메모리/성능, 코덱 설정 관점
> - **Codex**: 백엔드 파이프라인, 타임아웃, 에러 복구 관점
> - **Kiro**: 상태 관리, pause/resume 라이프사이클, 인프라 관점
> - **사용자 보고**: 실제 사용 중 발견된 증상 기반

---

## 1. Executive Summary

### 핵심 결론

**음성 끊김의 원인은 마이크 볼륨이 아닌 구조적 결함이다.**

가장 심각한 3가지:
1. **FS-004**: Web Speech API `network` 에러를 fatal로 분류하여 STT가 영구 중단됨
2. **FR-001**: MediaRecorder에 `onerror` 핸들러가 없어 녹음 실패를 감지할 수 없음
3. **FR-002**: 모바일에서 AudioContext가 자동 중단(suspended)되는데 `resume()` 호출이 없음

### 사용자 보고 증상 ↔ 원인 매핑

| 증상 | 원인 이슈 |
|------|----------|
| "가끔 멈추면 stop/resume하면 잘 될 때가 있다" | FS-004 (`network` 에러로 STT 영구 중단 → 새 인스턴스로 우회), FR-001 (MediaRecorder 무음 실패) |
| "마이크 입력이 문제있을 때가 있다" | FR-002 (AudioContext 중단), FR-003 (디바이스 연결 끊김 미감지) |
| "브라우저 창 이동 시 음성입력이 끊긴다" | FS-003 (탭 전환 시 Web Speech API 자동 중단, 복귀 시에만 재시작) |
| Console: `Speech recognition error: network` | FS-004 (`network`를 fatal error로 분류) |
| Console: `Summary failed: Error: Request failed` | 실시간 요약 API 호출 실패 — Lambda cold start, 네트워크, 또는 Bedrock 타임아웃 |
| Console: `watchdog: no results for 30s, restarting...` | 정상 동작이나, 재시작 시 FS-004 `network` 에러 유발 가능 → STT 영구 중단 연쇄 반응 |

### 아키텍처 변경 요약

최근 ECS GPU 기반 faster-whisper 실시간 STT 시스템이 **전면 제거**되고, 브라우저 Web Speech API만으로 대체됨. 제거된 파일: `realtimeClient.ts`, `sttOrchestrator.ts`, `audio-processor.js`, `backend/python/realtime/*`, `realtime-stack.ts` 등 7개 파일/디렉토리. 이로 인해 서버사이드 GPU STT, 클라이언트 VAD, AudioWorklet PCM 스트리밍, 서버 오디오 결합 기능이 모두 소실됨.

---

## 2. Architecture Overview

### 현재 파이프라인

```
[브라우저 녹음]                          [백엔드 처리]
┌─────────────────────┐                 ┌──────────────────────┐
│ MediaRecorder        │──── webm ────→ │ S3 (audio/)          │
│ (RecordButton.tsx)   │    presigned   │                      │
│                      │    URL upload  │    ↓ EventBridge     │
│ Web Speech API       │                │                      │
│ (speechRecognition.ts)│               │ Transcribe Lambda    │
│  └→ 실시간 자막 표시  │               │  └→ AWS Transcribe   │
│                      │                │      ↓               │
│ ※ 두 시스템은 독립적  │               │ S3 (transcripts/)    │
│   (서로 상태 공유 없음)│               │    ↓ EventBridge     │
└─────────────────────┘                 │                      │
                                        │ Summarize Lambda     │
                                        │  └→ Bedrock Claude   │
                                        │      ↓               │
                                        │ DynamoDB (결과 저장)  │
                                        └──────────────────────┘
```

### 제거된 시스템 vs 현재 시스템

| 기능 | 이전 (ECS Whisper) | 현재 (Web Speech API) | 영향 |
|------|-------------------|----------------------|------|
| 실시간 STT 엔진 | faster-whisper large-v3 (GPU) | Chrome Web Speech API (Google Cloud) | 정확도 하락, 네트워크 의존 |
| 오디오 스트리밍 | AudioWorklet → WebSocket PCM 16kHz | 없음 (독립적 MediaRecorder만) | 서버사이드 오디오 없음 |
| 음성 활동 감지 (VAD) | 에너지 기반 클라이언트 VAD | 없음 | 대역폭 절감 없음 |
| 서버 오디오 결합 | 16kHz WAV S3 저장 | MediaRecorder webm blob만 | 백업 오디오 품질 하락 |
| 번역 | 서버사이드 AWS Translate 즉시 적용 | 클라이언트 translateApi (debounced) | 레이턴시 증가 |
| 장애 복구 | STT Orchestrator 자동 전환 | 없음 (단일 엔진) | 복원력 없음 |

---

## 3. [핵심] 음성 녹음 이슈

### FR-001 | CRITICAL | MediaRecorder.onerror 미설정
- **리뷰어**: Claude, Kiro
- **파일**: `frontend/src/components/RecordButton.tsx:116-148`
- **현상**: `MediaRecorder` 인스턴스에 `ondataavailable`와 `onstop`만 설정. `onerror` 핸들러 없음.
- **영향**: 인코더 오류, 스트림 트랙 종료, 시스템 리소스 부족 등으로 MediaRecorder가 실패해도 UI는 "Recording in progress..." 표시. 사용자는 녹음이 되는 줄 알지만 실제 오디오 데이터 없음.
- **재현**: 녹음 중 블루투스 마이크 연결 끊김 or 장시간 녹음으로 시스템 리소스 부족
- **수정 방향**:
  ```typescript
  mediaRecorder.onerror = (event) => {
    console.error('MediaRecorder error:', event.error);
    onError?.(`녹음 오류: ${event.error.name}`);
    stopRecording(); // 안전하게 중단
  };
  ```

### FR-002 | CRITICAL | AudioContext suspended 상태 미확인
- **리뷰어**: Claude, Gemini
- **파일**: `frontend/src/components/RecordButton.tsx:119`
- **현상**: `new AudioContext()` 생성 후 `.state` 확인이나 `.resume()` 호출 없음.
- **영향**: 모바일 브라우저(iOS Safari, Chrome Android)에서 AudioContext가 `suspended` 상태로 생성됨. AnalyserNode(파형 시각화)가 작동하지 않아 레벨미터가 0 표시 → 사용자가 "마이크가 안 된다"고 오인. `page.tsx:154`의 미리보기 AudioContext에도 동일 문제.
- **재현**: 모바일 브라우저에서 녹음 시작 → 파형 애니메이션 없음
- **수정 방향**:
  ```typescript
  const audioContext = new AudioContext();
  if (audioContext.state === 'suspended') {
    await audioContext.resume();
  }
  ```

### FR-003 | HIGH | MediaStream track ended 이벤트 미처리
- **리뷰어**: Claude
- **파일**: `frontend/src/components/RecordButton.tsx:110-113`
- **현상**: `getUserMedia`로 받은 `MediaStream`의 audio track에 `ended` 이벤트 리스너 없음.
- **영향**: 블루투스 헤드폰 연결 해제, USB 마이크 분리 시 track이 `ended` 이벤트 발생. 리스너 없으면 MediaRecorder가 계속 실행되지만 무음/빈 데이터만 수집. 특히 `deviceId: { exact: deviceId }` 사용 시 (line 108) 선택한 디바이스 분리가 치명적.
- **재현**: 블루투스 마이크로 녹음 시작 → 블루투스 연결 해제 → 타이머는 계속 진행
- **수정 방향**:
  ```typescript
  const track = stream.getAudioTracks()[0];
  track.addEventListener('ended', () => {
    onError?.('마이크 연결이 끊어졌습니다');
    stopRecording();
  });
  ```

### FR-004 | HIGH | 체크포인트 Blob 메모리 누적
- **리뷰어**: Gemini, Codex
- **파일**: `frontend/src/components/RecordButton.tsx:159-167`
- **현상**: 60초마다 `chunksRef.current` **전체**로 Blob 생성. chunks는 녹음 시작 시(line 128)에만 초기화되고, 체크포인트 간 초기화 없음.
- **영향**: 1시간 녹음 @128kbps 시: chunks ~3600개(~57MB). 매 체크포인트마다 전체 Blob 재생성(1분: 1MB, 30분: 28MB, 60분: 57MB). 에스컬레이팅 메모리 압박 → 모바일에서 브라우저 탭 강제 종료 → 전체 녹음 손실.
- **재현**: 1시간 이상 녹음 → 모바일에서 탭 크래시
- **수정 방향**: 체크포인트 전송 후 전송된 chunks 인덱스 기록, delta만 전송. 또는 체크포인트 시 chunks 초기화 후 새 체크포인트부터 누적.

### FR-005 | HIGH | 코덱/비트레이트 미설정
- **리뷰어**: Gemini
- **파일**: `frontend/src/components/RecordButton.tsx:116`
- **현상**: `new MediaRecorder(stream, { mimeType })` — `mimeType`만 지정, `audioBitsPerSecond` 없음.
- **영향**: 브라우저 기본 비트레이트에 의존 (Chrome ~128kbps, 일부 브라우저 더 낮음). UI에 "48 kHz / Stereo" 표시(line 371-377)되지만 실제 값과 무관한 하드코딩. 또한 `getUserMedia`에 `autoGainControl`, `noiseSuppression`, `echoCancellation` constraint가 명시되지 않아 브라우저 기본값 사용.
- **수정 방향**:
  ```typescript
  const mediaRecorder = new MediaRecorder(stream, {
    mimeType,
    audioBitsPerSecond: 128000,
  });
  // getUserMedia constraints:
  { audio: { deviceId, autoGainControl: true, noiseSuppression: true, echoCancellation: true } }
  ```

### FR-006 | HIGH | stopRecording/onstop 레이스 컨디션
- **리뷰어**: Claude, Codex
- **파일**: `frontend/src/components/RecordButton.tsx:201-217`
- **현상**: `stopRecording()`이 `mediaRecorder.stop()` 호출(line 212) 후 **즉시** `onRecordingStop?.()` 실행(line 216). 하지만 `onstop`(line 136)은 비동기로 나중에 실행됨.
- **영향**: `page.tsx`의 `handleRecordingStop`(line 306-312)이 STT client를 null로 설정하는데, 이후 `onstop` → `onBlobReady` → `handleBlobReady`(line 314)가 실행될 때 transcripts 상태가 이미 정리된 상태일 수 있음. ref 사용으로 대부분 안전하지만 타이밍에 의존하는 취약한 구조.
- **수정 방향**: `onRecordingStop` 호출을 `onstop` 핸들러 내부(`onBlobReady` 호출 후)로 이동.

### FR-007 | MEDIUM | pause/resume 후 체크포인트 타이머 미재시작
- **리뷰어**: Kiro
- **파일**: `frontend/src/components/RecordButton.tsx:184, 190-198`
- **현상**: `pauseRecording()`(line 184)에서 체크포인트 타이머 제거. `resumeRecording()`(line 190-198)에서 경과시간 타이머만 재시작, 체크포인트 타이머 재시작 없음.
- **영향**: pause/resume 이후 체크포인트 저장 영구 중단. 이후 녹음이 손실되면 마지막 체크포인트는 pause 이전 시점.
- **수정 방향**: `resumeRecording()`에서 `checkpointTimerRef` 재설정.

### FR-008 | MEDIUM | getPreferredMimeType 폴백 오류
- **리뷰어**: Gemini
- **파일**: `frontend/src/lib/device.ts:40-57`
- **현상**: 지원되는 MIME 타입이 없을 때 `'audio/webm'`을 반환하지만, 이는 이미 미지원으로 확인된 타입. `MediaRecorder` 생성자에 전달하면 에러 발생.
- **영향**: 일부 모바일 브라우저에서 녹음 시작 불가. `startRecording`의 try/catch에서 잡히지만 에러 메시지가 모호함.
- **수정 방향**: 폴백 시 빈 문자열 반환 → `MediaRecorder`가 기본 포맷 자동 선택하도록.

### FR-009 | LOW | iPadOS 데스크톱 UA 미감지
- **리뷰어**: Claude
- **파일**: `frontend/src/lib/device.ts:3-8`
- **현상**: iPadOS 13+는 macOS user agent 사용. `isIOS()`가 `false` 반환 → MediaRecorder 경로 사용 (native file input 대신).
- **영향**: iPad Safari에서 MediaRecorder 지원이 제한적이면 FR-008 폴백 문제 발생 가능. 실제 영향은 낮음 (Safari 14.5+ 지원).
- **수정 방향**: `navigator.maxTouchPoints > 1 && /Macintosh/.test(navigator.userAgent)` 추가.

### FR-010 | LOW | 프리뷰/녹음 AudioContext 이중 생성
- **리뷰어**: Claude
- **파일**: `frontend/src/app/record/page.tsx:126-172`, `RecordButton.tsx:119`
- **현상**: 마이크 프리뷰(page.tsx line 154)와 녹음(RecordButton line 119)에서 각각 AudioContext 생성. 두 개의 `getUserMedia` 호출 발생.
- **영향**: 프리뷰 stream은 녹음 시작 시 정리되지만, 선택한 deviceId가 stale이면 프리뷰는 성공하나 녹음은 실패할 수 있음. 실제 발생 빈도 낮음.

---

## 4. [핵심] 실시간 자막(STT) 이슈

### FS-004 | CRITICAL | `network` 에러를 fatal로 분류 — STT 영구 중단
- **리뷰어**: Claude (사용자 직접 보고: `Speech recognition error: network`)
- **파일**: `frontend/src/lib/speechRecognition.ts:250-255`
- **현상**:
  ```typescript
  const fatalErrors = ['not-allowed', 'network', 'service-not-allowed', 'language-not-supported'];
  if (fatalErrors.includes(event.error)) {
      this.shouldRestart = false;  // 재시작 영구 차단
      this.isListening = false;     // 수신 영구 중단
      this.onError?.(event.error);
  }
  ```
  `network` 에러가 `not-allowed`와 같은 레벨의 fatal error로 분류됨.
- **영향**: Web Speech API는 Google Cloud 서버에 오디오를 전송. WiFi 전환, 일시적 네트워크 불안정, Google 서버 일시 장애 등에서 `network` 에러 발생. **이 에러가 한 번만 발생해도 `shouldRestart=false`로 STT가 영구 중단**. 사용자가 수동으로 stop/resume하지 않으면 자막이 다시 나오지 않음. 이것이 "가끔 멈추면 stop/resume하면 잘 된다"의 직접적 원인.
- **재현**: 녹음 중 WiFi 잠깐 끊김 또는 VPN 연결 변경 → Console에 `Speech recognition error: network` → 자막 영구 중단
- **수정 방향**:
  ```typescript
  const fatalErrors = ['not-allowed', 'service-not-allowed', 'language-not-supported'];
  const transientErrors = ['network', 'audio-capture'];

  if (fatalErrors.includes(event.error)) {
      this.shouldRestart = false;
      this.isListening = false;
      this.onError?.(event.error);
  } else if (transientErrors.includes(event.error)) {
      // 일시적 오류: 3초 후 자동 재시작, 최대 5회
      this.networkRetryCount = (this.networkRetryCount || 0) + 1;
      if (this.networkRetryCount <= 5) {
          setTimeout(() => this.restartRecognition(), 3000);
      } else {
          this.onError?.('recognition-stalled');
      }
  }
  ```

### FS-003 | HIGH | 브라우저 탭/창 전환 시 STT 자동 중단
- **리뷰어**: Claude (사용자 보고)
- **파일**: `frontend/src/lib/speechRecognition.ts:164-172`
- **현상**: `visibilitychange` 핸들러가 `document.visibilityState === 'visible'` 일 때만 재시작. 탭이 hidden 될 때 Chrome이 자동으로 Web Speech API 중단 → `onend` 발생 → 재시작 시도하지만 hidden 상태에서는 Chrome이 허용하지 않음. visible 복귀 시 300ms 대기 후 재시작, 재시작 자체에 500ms 소요 → 총 ~800ms 자막 갭.
- **영향**: 다른 앱이나 탭으로 잠깐 전환하고 돌아오면 자막이 끊겨 있음. 모바일에서는 탭 suspend로 MediaRecorder도 중단될 수 있음.
- **수정 방향**: 탭 hidden 시 `recognition.stop()` 명시적 호출 + 상태 표시("탭 복귀 시 자막이 재시작됩니다"). visible 복귀 시 즉시 재시작 (300ms 대기 제거).

### FS-001 | MEDIUM | Web Speech API 재시작 시 ~500ms 자막 갭
- **리뷰어**: Claude, Kiro
- **파일**: `frontend/src/lib/speechRecognition.ts:125-162`
- **현상**: watchdog(30초 무음) 또는 Chrome 자연 종료 시 `restartRecognition()` 호출. 새 인스턴스 생성 → old abort → fresh start → 500ms 후 `isRestarting=false`. 이 ~500ms + Chrome 초기화 시간 동안 음성 인식 없음.
- **영향**: 재시작 중 발화된 내용은 transcript에서 누락. overlap buffer가 중복을 방지하지만 갭은 방지 못함.
- **수정 방향**: `isRestarting` 타임아웃을 `onresult` 첫 수신으로 대체 (실제 준비 완료 기준).

### FS-002 | MEDIUM | MediaRecorder/Web Speech API 독립 동작
- **리뷰어**: Kiro
- **파일**: `frontend/src/app/record/page.tsx:268-293`
- **현상**: MediaRecorder(녹음)와 Web Speech API(자막)가 완전히 독립적. 상태 공유 없음.
- **영향**: Web Speech API 실패 시 자막이 멈추지만 녹음은 계속 → 사용자가 "녹음이 안 된다"고 오인. 반대로 MediaRecorder 실패 시 자막은 계속 나오지만 실제 오디오 없음 → 사용자가 "정상"이라고 오인.
- **수정 방향**: STT 에러 배너에 "녹음은 정상 진행 중입니다" 또는 "녹음이 중단되었습니다" 상태 구분 표시.

---

## 5. [부록] 백엔드 전사(Transcribe) 이슈

### BT-001 | HIGH | 한국어 하드코딩
- **리뷰어**: Gemini, Kiro
- **파일**: `backend/internal/service/transcribe.go:53, 97`
- **현상**: `LanguageCode: types.LanguageCodeKoKr` 하드코딩. meeting record에서 언어를 읽지 않음.
- **영향**: 한국어 외 회의는 전사 품질 극히 저하.
- **수정 방향**: meeting record에 `language` 필드 추가, `IdentifyLanguage` 옵션 지원.

### BT-002 | HIGH | WebM 포맷 + 가변 샘플레이트
- **리뷰어**: Claude, Gemini
- **파일**: `backend/internal/service/transcribe.go:164-183`
- **현상**: `getMediaFormat`이 `.webm` → `"webm"` 반환. `MediaSampleRateHertz` 미설정. 프론트엔드 `getUserMedia`에 `sampleRate` constraint 없어 디바이스마다 8kHz~48kHz 가변.
- **영향**: AWS Transcribe가 WebM/Opus 포맷의 가변 샘플레이트를 올바르게 처리하지 못할 수 있음.
- **수정 방향**: `getUserMedia`에 `sampleRate: 48000` constraint 추가. Transcribe input에 `MediaSampleRateHertz` 명시.

### BT-003 | HIGH | Transcribe job name 충돌
- **리뷰어**: Codex
- **파일**: `backend/internal/service/transcribe.go:48`
- **현상**: `jobName := fmt.Sprintf("ttobak-%s", meetingID)` — meetingID만 사용. Nova도 동일 (`ttobak-nova-%s`, line 87).
- **영향**: 동일 meeting에 오디오 재업로드 시 AWS Transcribe `ConflictException` 발생. job name은 계정 내 전역 고유해야 함.
- **수정 방향**: `fmt.Sprintf("ttobak-%s-%d", meetingID, time.Now().Unix())`.

### BT-004 | MEDIUM-HIGH | 중복 상태 업데이트
- **리뷰어**: Claude
- **파일**: `backend/internal/service/upload.go:108`, `backend/internal/service/transcribe.go:72`
- **현상**: meeting status를 `StatusTranscribing`으로 설정하는 곳이 3곳: frontend(`page.tsx:339`), `CompleteUpload`(`upload.go:108`), transcribe Lambda(`transcribe.go:72`). Last-writer-wins.
- **영향**: 경합 조건에서 더 진행된 상태가 이전 상태로 되돌아갈 수 있음.
- **수정 방향**: 상태 업데이트를 transcribe Lambda에서만 수행. `CompleteUpload`에서 제거.

### BT-005 | MEDIUM | Nova Sonic 스텁
- **리뷰어**: Claude, Codex
- **파일**: `backend/internal/service/transcribe.go:80-116`
- **현상**: "placeholder" 주석(line 83-85). 실제로는 표준 Transcribe와 동일하게 배치 작업 시작.
- **영향**: "Nova Sonic V2" 선택 사용자가 동일한 결과를 받음. job name만 다르고 output key에 `-nova` 접미사.
- **수정 방향**: 스텁 제거 (UI에서 Nova Sonic 옵션 숨김) 또는 실제 Transcribe Streaming API 구현.

---

## 6. [부록] 백엔드 요약(Summarize) 이슈

### BS-001 | MEDIUM-HIGH | EventBridge 재귀 트리거 위험
- **리뷰어**: Claude, Gemini
- **파일**: `backend/cmd/summarize/main.go`, `infra/lib/gateway-stack.ts:266-282`
- **현상**: `TranscriptUploadRule`이 `transcripts/` 프리픽스의 모든 S3 Object Created 이벤트를 트리거. Summarize Lambda가 `transcripts/{meetingID}/transcriptA.txt`에 쓰면 재트리거 가능.
- **영향**: 현재는 `.json` 확인 로직으로 `.txt`는 JSON 파싱 실패 → 에러 상태 설정 후 종료. 하지만 불필요한 Lambda 호출 발생.
- **수정 방향**: EventBridge 규칙에 `suffix: '.json'` 필터 추가, 또는 S3 key prefix를 분리.

### BS-002 | MEDIUM | Summarize Lambda 2분 타임아웃
- **리뷰어**: Codex
- **파일**: `infra/lib/gateway-stack.ts:98`
- **현상**: `timeout: cdk.Duration.minutes(2)`. Summarize Lambda는 S3 다운로드 + Bedrock Claude 요약(max_tokens: 4096) + Bedrock Haiku 액션아이템 추출 + DynamoDB 복수 업데이트 수행.
- **영향**: 긴 회의록에서 2개 Bedrock 호출만으로 60-90초 소요 가능. DynamoDB/S3 포함 시 2분 초과 위험.
- **수정 방향**: 타임아웃 5분으로 증가.

### BS-003 | MEDIUM | TranscriptSegments S3 미오프로드
- **리뷰어**: Claude
- **파일**: `backend/internal/repository/dynamodb.go:322-346`
- **현상**: `UpdateMeeting`이 `TranscriptA`/`TranscriptB`만 300KB 초과 시 S3 오프로드. `TranscriptSegments` (화자 분리 JSON)는 직접 DynamoDB 저장.
- **영향**: 긴 회의의 segments JSON이 수백 KB → 다른 속성과 합쳐 DynamoDB 400KB 항목 제한 초과 위험.
- **수정 방향**: `TranscriptSegments`도 300KB 초과 시 S3 오프로드.

### BS-004 | MEDIUM | Summarize Lambda 에러 삼킴
- **리뷰어**: Codex
- **파일**: `backend/cmd/summarize/main.go:152-170`
- **현상**: 트랜스크립트 파싱 실패, DynamoDB 업데이트 실패 시 `StatusError` 설정 후 `return nil`. Lambda가 성공으로 보고.
- **영향**: S3 스로틀링, DynamoDB 용량 부족 등 일시적 실패가 재시도 없이 영구 에러 처리됨.
- **수정 방향**: 일시적 에러(throttling, timeout)는 `error` 반환으로 EventBridge 자동 재시도 허용. 영구 에러(파싱 실패)만 `nil` 반환.

---

## 7. [부록] 인프라 이슈

### IN-001 | MEDIUM | 30초 업로드 타임아웃
- **리뷰어**: Codex
- **파일**: `frontend/src/lib/upload.ts:92`
- **현상**: `uploadAudioWithRetry`에서 `setTimeout(() => controller.abort(), 30000)`. 최대 3회 시도 (2초 간격) → 총 ~96초.
- **영향**: 1시간 회의 녹음(~57MB @128kbps) 업로드가 느린 네트워크에서 타임아웃. 반면 `uploadToS3` XHR 경로(line 29-63)는 타임아웃 없음 (무한 대기).
- **수정 방향**: 파일 크기 기반 동적 타임아웃 (최소 60초, MB당 +10초). XHR 경로에도 합리적 타임아웃 추가.

---

## 8. 아키텍처 퇴보 분석

ECS Whisper 시스템 제거로 인한 품질 영향:

| 제거된 기능 | 품질 영향 | 복구 비용 | 권장 |
|------------|----------|----------|------|
| GPU STT (faster-whisper large-v3) | 높음 — 한국어 정확도 현저히 하락 | 높음 (ECS+GPU 인프라 재구축) | Phase 2+ 검토 |
| AudioWorklet PCM 스트리밍 | 중간 — 서버사이드 고품질 오디오 없음 | 중간 | GPU STT와 함께 |
| 클라이언트 VAD | 낮음 — 현재 서버 스트리밍 없어 불필요 | 낮음 | GPU STT 복구 시 함께 |
| 서버 오디오 결합 (16kHz WAV) | 중간 — 백업 오디오 없음 | 낮음 | 별도 구현 가능 |
| STT Orchestrator 자동 전환 | 높음 — 단일 엔진 장애 시 복구 없음 | 중간 | FS-004 수정으로 부분 완화 |
| 서버사이드 번역 | 낮음 — 클라이언트 translateApi로 대체됨 | N/A | 현재 방식 유지 |

---

## 9. 우선순위 수정 로드맵

### Phase 1: 데이터 손실 방지 (1-2일) ★ 최우선

| ID | 수정 내용 | 파일 |
|----|----------|------|
| FS-004 | `network`를 fatal에서 제거, transient 분류 + 3초 후 자동 재시작 (최대 5회) | `speechRecognition.ts` |
| FR-001 | `MediaRecorder.onerror` 핸들러 추가, 사용자 알림 | `RecordButton.tsx` |
| FR-002 | `AudioContext.resume()` 호출, state 확인 | `RecordButton.tsx` |
| FR-003 | `track.onended` 리스너 추가 → 마이크 연결 끊김 알림 | `RecordButton.tsx` |
| FR-006 | `onRecordingStop` 호출을 `onstop` 내부로 이동 | `RecordButton.tsx` |

### Phase 2: 녹음 품질 개선 (2-3일)

| ID | 수정 내용 | 파일 |
|----|----------|------|
| FR-004 | 체크포인트 delta 전송 또는 chunks 인덱스 관리 | `RecordButton.tsx` |
| FR-005 | `audioBitsPerSecond: 128000`, `autoGainControl`/`noiseSuppression` constraint 명시 | `RecordButton.tsx` |
| BT-003 | job name에 timestamp 접미사 | `transcribe.go` |
| IN-001 | 파일 크기 기반 동적 타임아웃 | `upload.ts` |

### Phase 3: 백엔드 안정성 (2-3일)

| ID | 수정 내용 | 파일 |
|----|----------|------|
| BT-001 | meeting record에서 언어 읽기, `IdentifyLanguage` 옵션 | `transcribe.go` |
| BT-002 | `getUserMedia` sampleRate constraint, UI 표시값 동기화 | `RecordButton.tsx`, `transcribe.go` |
| BT-004 | 상태 업데이트를 transcribe Lambda에서만 수행 | `upload.go` |
| BS-002 | Summarize Lambda 타임아웃 5분으로 증가 | `gateway-stack.ts` |
| BS-003 | TranscriptSegments 300KB 초과 시 S3 오프로드 | `dynamodb.go` |
| BS-004 | 일시적 에러 시 error return | `summarize/main.go` |

### Phase 4: 엣지 케이스 및 안정화 (1-2일)

| ID | 수정 내용 | 파일 |
|----|----------|------|
| FR-007 | resume에서 체크포인트 타이머 재시작 | `RecordButton.tsx` |
| FR-008 | 폴백 시 빈 문자열 반환 → 기본 포맷 선택 | `device.ts` |
| FS-001 | 재시작 갭 최소화 (isRestarting을 onresult 기준으로) | `speechRecognition.ts` |
| FS-002 | 상태 배너에 녹음/자막 독립 상태 표시 | `page.tsx` |
| FS-003 | 탭 전환 시 명시적 stop/재시작 + 사용자 안내 | `speechRecognition.ts` |
| BT-005 | Nova Sonic 스텁 제거 또는 실제 구현 | `transcribe.go` |

---

## 10. References

- [CODE-REVIEW.md](./CODE-REVIEW.md) — 일반 코드 리뷰 (2026-03-05)
- [TRI-MODEL-REVIEW.md](../review/TRI-MODEL-REVIEW.md) — 3모델 아키텍처 리뷰 (2026-03-25)
- [ISSUES.md](../ISSUES.md) — 운영 이슈 트래커
- [PRD.md](./PRD.md) — 제품 요구사항
- [DESIGN-SPEC.md](./DESIGN-SPEC.md) — 디자인 사양
- [INFRA-SPEC.md](./INFRA-SPEC.md) — 인프라 사양
