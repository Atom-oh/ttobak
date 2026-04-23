# ADR-001: System Audio Capture for Remote Meeting Recording

<a href="#english"><img src="https://img.shields.io/badge/lang-English-blue.svg" alt="English"></a>
<a href="#korean"><img src="https://img.shields.io/badge/lang-한국어-red.svg" alt="Korean"></a>

---

<a id="english"></a>

# English

## Status
Superseded by [ADR-006](ADR-006-tab-audio-capture-and-tauri-mac-app.md)

## Context

Ttobak currently records audio exclusively through the browser's `getUserMedia({ audio })` API, which captures only the local microphone input. When a user is in a Zoom, Google Meet, or Microsoft Teams call, the remote participants' audio is routed through the system audio output (speakers/headphones) and is **not captured** by the microphone stream.

This is a significant limitation because the primary use case for Ttobak --- meeting note-taking --- often involves multi-party video conferences where the user needs a complete transcript of **all participants**, not just themselves.

### Technical Background

- `RecordButton.tsx:136` calls `navigator.mediaDevices.getUserMedia({ audio: audioConstraints })` which only captures mic input
- Remote participant audio arrives via the WebRTC stack of the conferencing app and is played through the system audio device
- The browser's `MediaRecorder` can only record `MediaStream` objects, so we need a way to obtain a `MediaStream` containing the system audio
- Browser security models intentionally restrict access to system audio to prevent eavesdropping

## Options Considered

### Option 1: `getDisplayMedia` with System Audio Mixing

Use `navigator.mediaDevices.getDisplayMedia({ audio: true })` to capture system audio (or a specific browser tab's audio), then mix it with the existing microphone stream using `AudioContext`.

**Implementation sketch:**

```typescript
// Capture system/tab audio
const displayStream = await navigator.mediaDevices.getDisplayMedia({
  video: true, // required by the API, can be a minimal capture
  audio: true,
});

// Capture microphone
const micStream = await navigator.mediaDevices.getUserMedia({ audio: true });

// Mix both streams
const audioCtx = new AudioContext();
const dest = audioCtx.createMediaStreamDestination();
audioCtx.createMediaStreamSource(micStream).connect(dest);
audioCtx.createMediaStreamSource(displayStream).connect(dest);

// dest.stream contains mixed audio from both sources
const mediaRecorder = new MediaRecorder(dest.stream, { mimeType });
```

- **Pros**:
  - Works entirely in the browser with standard Web APIs
  - No server-side changes required
  - Captures all system audio including Zoom, Google Meet, Teams, and any other conferencing tool
  - `AudioContext` mixing enables per-source gain control (e.g., boost remote audio)
  - Chrome supports tab-specific audio capture (no screen sharing prompt for the entire screen)
- **Cons**:
  - Requires user to go through a screen/tab sharing prompt (UX friction)
  - On some OS/browser combinations, `audio: true` in `getDisplayMedia` is not supported (Safari has limited support)
  - Captures all system audio indiscriminately --- notification sounds, music, etc. are included unless tab-specific capture is used
  - The `video` track is mandatory in the API call (though it can be immediately stopped)
  - Speaker diarization becomes harder when both sides are in a single mixed stream

### Option 2: Virtual Audio Device (User-Configured Loopback)

Instruct users to install a virtual audio loopback driver (e.g., BlackHole on macOS, VB-Cable on Windows) that routes system output back as a virtual microphone input. The user then selects this virtual device in Ttobak's microphone selector.

- **Pros**:
  - No code changes required in Ttobak (the existing `getUserMedia` + device selector already supports this)
  - Captures all system audio at the OS level
  - Works with any conferencing application
- **Cons**:
  - Requires users to install third-party software (high friction, potential security concerns)
  - Complex setup: users must configure a multi-output device or aggregate device
  - Different software required per OS (BlackHole for macOS, VB-Cable for Windows, PulseAudio for Linux)
  - No control over the mixing --- user's own mic audio may or may not be included depending on configuration
  - Cannot be offered as a seamless in-app feature

### Option 3: Zoom / Google Meet API Integration

Integrate directly with the conferencing platform's API or SDK to access audio streams programmatically.

- **Pros**:
  - Clean, per-participant audio streams (ideal for speaker diarization)
  - No screen sharing prompt or user-visible capture flow
  - Can access meeting metadata (participant names, join/leave events)
- **Cons**:
  - Requires separate integration per platform (Zoom SDK, Google Meet API, Teams Graph API)
  - Zoom Meeting SDK requires a paid plan and app marketplace approval
  - Google Meet API has very limited audio access (primarily for add-ons running within Meet)
  - Significant development effort for each integration
  - Platform policy changes could break the integration at any time
  - Does not cover lesser-used platforms (Discord, Webex, etc.)

### Option 4: Electron / Desktop App with System Audio Access

Build a native desktop application (e.g., using Electron or Tauri) that can capture system audio at the OS level without browser security restrictions.

- **Pros**:
  - Full access to system audio via native APIs (Core Audio on macOS, WASAPI on Windows)
  - No screen sharing prompt required
  - Can run as a background service during any meeting
  - Enables advanced features like per-application audio routing
- **Cons**:
  - Requires building and maintaining a desktop application alongside the web app
  - Significant increase in development and maintenance scope
  - Users must download and install the app
  - macOS requires explicit Screen Recording permission (and potentially audio permission)
  - Platform-specific code for each OS

## Decision

**Choose Option 1: `getDisplayMedia` with System Audio Mixing** as the primary approach, with **Option 2 documented as a fallback** for users on unsupported browsers.

Rationale:
- Option 1 provides the best balance of capability and development effort
- It works across all conferencing platforms without per-platform integration
- Chrome (the dominant browser) has strong support for tab audio capture, which avoids capturing extraneous system sounds
- The screen sharing prompt is a one-time UX friction that users understand from other recording tools (Loom, Grain, etc.)
- The implementation is confined to the frontend recording layer (`RecordButton.tsx` and related hooks) with no backend changes
- Option 2 (virtual audio device) requires no code changes and can be documented as an alternative for Safari/Firefox users

## Consequences

### Positive
- Users can record complete Zoom/Google Meet/Teams conversations with all participants
- The existing STT pipeline (Transcribe, Nova Sonic) receives a mixed audio stream and can transcribe all speakers
- No backend changes required --- the `MediaRecorder` output is the same `Blob` format regardless of the audio source
- Tab-specific capture in Chrome avoids capturing unrelated system sounds

### Negative
- The screen/tab sharing prompt adds UX friction to the recording start flow
- Safari has limited `getDisplayMedia` audio support; users on Safari may need the virtual audio device workaround
- Mixed audio makes speaker diarization harder compared to per-participant streams (future consideration)
- The `video` track in `getDisplayMedia` is mandatory but wasteful; it must be immediately discarded to avoid resource consumption

## Implementation Notes

### Affected Files
- `frontend/src/components/RecordButton.tsx` --- Add "Record System Audio" toggle; implement `getDisplayMedia` + `AudioContext` mixing
- `frontend/src/hooks/useRecordingSession.ts` --- Handle mixed stream lifecycle
- `frontend/src/components/record/RecordingConfig.tsx` --- Add system audio toggle to config UI

### Browser Support Matrix

| Browser | `getDisplayMedia` audio | Tab-specific audio | Notes |
|---------|------------------------|--------------------|-------|
| Chrome 94+ | Yes | Yes | Best experience |
| Edge 94+ | Yes | Yes | Chromium-based |
| Firefox 66+ | Yes | No (whole screen only) | Audio capture may require `about:config` flag |
| Safari 16+ | Partial | No | Audio capture not supported in all contexts |

### Future Considerations
- Consider Zoom Meeting SDK integration if Ttobak targets enterprise users who primarily use Zoom
- Evaluate Web Audio API `AudioWorklet` for real-time noise/notification filtering on the system audio track
- Investigate `MediaStreamTrack` APIs for speaker diarization hints when multiple audio sources are available

## References
- [MDN: getDisplayMedia](https://developer.mozilla.org/en-US/docs/Web/API/MediaDevices/getDisplayMedia)
- [Chrome Tab Audio Capture](https://developer.chrome.com/docs/web-platform/screen-sharing#primitives)
- [Zoom Meeting SDK](https://developers.zoom.us/docs/meeting-sdk/)
- [Google Meet REST API](https://developers.google.com/meet/api)
- Current recording implementation: `frontend/src/components/RecordButton.tsx:127-146`

---

<a id="korean"></a>

# 한국어

## 상태
[ADR-006](ADR-006-tab-audio-capture-and-tauri-mac-app.md)으로 대체됨

## 배경

Ttobak은 현재 브라우저의 `getUserMedia({ audio })` API를 통해 로컬 마이크 입력만 녹음합니다. 사용자가 Zoom, Google Meet, Microsoft Teams 통화 중일 때, 상대방의 음성은 시스템 오디오 출력(스피커/이어폰)으로 전달되기 때문에 마이크 스트림으로는 **캡처되지 않습니다**.

이것은 중요한 제약사항입니다. Ttobak의 주요 사용 사례인 회의록 작성에서는 본인뿐만 아니라 **모든 참가자**의 완전한 트랜스크립트가 필요하기 때문입니다.

### 기술적 배경

- `RecordButton.tsx:136`에서 `navigator.mediaDevices.getUserMedia({ audio: audioConstraints })`를 호출하여 마이크 입력만 캡처합니다
- 상대방 음성은 화상회의 앱의 WebRTC 스택을 통해 도착하여 시스템 오디오 장치로 재생됩니다
- 브라우저의 `MediaRecorder`는 `MediaStream` 객체만 녹음할 수 있으므로, 시스템 오디오를 포함하는 `MediaStream`을 얻는 방법이 필요합니다
- 브라우저 보안 모델은 도청 방지를 위해 시스템 오디오 접근을 의도적으로 제한합니다

## 검토한 옵션

### 옵션 1: `getDisplayMedia` + 시스템 오디오 믹싱

`navigator.mediaDevices.getDisplayMedia({ audio: true })`를 사용하여 시스템 오디오(또는 특정 브라우저 탭의 오디오)를 캡처한 후, `AudioContext`를 사용하여 기존 마이크 스트림과 믹싱합니다.

**구현 스케치:**

```typescript
// 시스템/탭 오디오 캡처
const displayStream = await navigator.mediaDevices.getDisplayMedia({
  video: true, // API에서 필수, 최소한의 캡처 가능
  audio: true,
});

// 마이크 캡처
const micStream = await navigator.mediaDevices.getUserMedia({ audio: true });

// 두 스트림 믹싱
const audioCtx = new AudioContext();
const dest = audioCtx.createMediaStreamDestination();
audioCtx.createMediaStreamSource(micStream).connect(dest);
audioCtx.createMediaStreamSource(displayStream).connect(dest);

// dest.stream에 양쪽 소스의 믹싱된 오디오가 포함됨
const mediaRecorder = new MediaRecorder(dest.stream, { mimeType });
```

- **장점**:
  - 표준 Web API만으로 브라우저에서 완전히 동작합니다
  - 서버 측 변경이 필요 없습니다
  - Zoom, Google Meet, Teams 및 기타 모든 화상회의 도구의 시스템 오디오를 캡처합니다
  - `AudioContext` 믹싱으로 소스별 볼륨 조절이 가능합니다 (예: 상대방 오디오 증폭)
  - Chrome은 탭별 오디오 캡처를 지원합니다 (전체 화면 공유 프롬프트 불필요)
- **단점**:
  - 사용자가 화면/탭 공유 프롬프트를 거쳐야 합니다 (UX 마찰)
  - 일부 OS/브라우저 조합에서 `getDisplayMedia`의 `audio: true`가 지원되지 않습니다 (Safari 제한적 지원)
  - 탭별 캡처를 사용하지 않으면 알림 소리, 음악 등 모든 시스템 오디오가 무분별하게 캡처됩니다
  - API 호출에서 `video` 트랙이 필수입니다 (즉시 중지 가능하지만)
  - 양쪽이 하나의 믹싱된 스트림에 있으면 화자 분리가 어려워집니다

### 옵션 2: 가상 오디오 장치 (사용자 설정 루프백)

사용자에게 가상 오디오 루프백 드라이버(macOS의 BlackHole, Windows의 VB-Cable 등)를 설치하도록 안내하여, 시스템 출력을 가상 마이크 입력으로 라우팅합니다. 사용자는 Ttobak의 마이크 선택기에서 이 가상 장치를 선택합니다.

- **장점**:
  - Ttobak에 코드 변경이 필요 없습니다 (기존 `getUserMedia` + 장치 선택기가 이미 지원)
  - OS 수준에서 모든 시스템 오디오를 캡처합니다
  - 모든 화상회의 애플리케이션에서 동작합니다
- **단점**:
  - 사용자가 서드파티 소프트웨어를 설치해야 합니다 (높은 마찰, 보안 우려 가능)
  - 복잡한 설정: 다중 출력 장치 또는 통합 장치를 구성해야 합니다
  - OS별로 다른 소프트웨어가 필요합니다 (macOS: BlackHole, Windows: VB-Cable, Linux: PulseAudio)
  - 믹싱에 대한 제어가 없습니다 --- 설정에 따라 사용자 자신의 마이크 오디오가 포함될 수도 있고 아닐 수도 있습니다
  - 원활한 인앱 기능으로 제공할 수 없습니다

### 옵션 3: Zoom / Google Meet API 연동

화상회의 플랫폼의 API 또는 SDK를 직접 통합하여 프로그래밍 방식으로 오디오 스트림에 접근합니다.

- **장점**:
  - 깔끔한 참가자별 오디오 스트림 (화자 분리에 이상적)
  - 화면 공유 프롬프트나 사용자 가시적 캡처 흐름이 없습니다
  - 회의 메타데이터에 접근할 수 있습니다 (참가자 이름, 입장/퇴장 이벤트)
- **단점**:
  - 플랫폼별로 별도의 통합이 필요합니다 (Zoom SDK, Google Meet API, Teams Graph API)
  - Zoom Meeting SDK는 유료 플랜과 앱 마켓플레이스 승인이 필요합니다
  - Google Meet API는 오디오 접근이 매우 제한적입니다 (주로 Meet 내부에서 실행되는 애드온용)
  - 각 통합에 상당한 개발 노력이 필요합니다
  - 플랫폼 정책 변경으로 언제든 통합이 깨질 수 있습니다
  - 덜 사용되는 플랫폼(Discord, Webex 등)을 커버하지 못합니다

### 옵션 4: Electron / 데스크톱 앱 (네이티브 시스템 오디오 접근)

네이티브 데스크톱 애플리케이션(Electron 또는 Tauri)을 빌드하여 브라우저 보안 제한 없이 OS 수준에서 시스템 오디오를 캡처합니다.

- **장점**:
  - 네이티브 API를 통한 시스템 오디오 완전 접근 (macOS: Core Audio, Windows: WASAPI)
  - 화면 공유 프롬프트가 필요 없습니다
  - 모든 회의 중 백그라운드 서비스로 실행 가능합니다
  - 애플리케이션별 오디오 라우팅 등 고급 기능이 가능합니다
- **단점**:
  - 웹 앱과 함께 데스크톱 애플리케이션을 빌드하고 유지보수해야 합니다
  - 개발 및 유지보수 범위가 크게 증가합니다
  - 사용자가 앱을 다운로드하여 설치해야 합니다
  - macOS는 명시적인 화면 녹화 권한(및 잠재적으로 오디오 권한)이 필요합니다
  - OS별 플랫폼 특화 코드가 필요합니다

## 결정

**옵션 1: `getDisplayMedia` + 시스템 오디오 믹싱**을 주요 방식으로 채택하고, **옵션 2를 지원되지 않는 브라우저 사용자를 위한 대안으로 문서화**합니다.

근거:
- 옵션 1은 기능성과 개발 노력 사이의 최적의 균형을 제공합니다
- 플랫폼별 통합 없이 모든 화상회의 플랫폼에서 동작합니다
- Chrome(주요 브라우저)은 탭 오디오 캡처를 잘 지원하며, 불필요한 시스템 사운드 캡처를 방지합니다
- 화면 공유 프롬프트는 다른 녹화 도구(Loom, Grain 등)에서 사용자들이 이미 이해하는 일회성 UX 마찰입니다
- 구현이 프론트엔드 녹음 레이어(`RecordButton.tsx` 및 관련 훅)에 한정되며 백엔드 변경이 필요 없습니다
- 옵션 2(가상 오디오 장치)는 코드 변경이 필요 없으며 Safari/Firefox 사용자를 위한 대안으로 문서화할 수 있습니다

## 영향

### 긍정적
- 사용자가 모든 참가자가 포함된 완전한 Zoom/Google Meet/Teams 대화를 녹음할 수 있습니다
- 기존 STT 파이프라인(Transcribe, Nova Sonic)이 믹싱된 오디오 스트림을 수신하여 모든 화자를 인식합니다
- 백엔드 변경이 필요 없습니다 --- `MediaRecorder` 출력은 오디오 소스에 관계없이 동일한 `Blob` 형식입니다
- Chrome의 탭별 캡처로 무관한 시스템 사운드 캡처를 방지합니다

### 부정적
- 화면/탭 공유 프롬프트가 녹음 시작 흐름에 UX 마찰을 추가합니다
- Safari는 `getDisplayMedia` 오디오 지원이 제한적이므로 Safari 사용자는 가상 오디오 장치 우회 방법이 필요할 수 있습니다
- 믹싱된 오디오는 참가자별 스트림에 비해 화자 분리를 어렵게 만듭니다 (향후 고려사항)
- `getDisplayMedia`의 `video` 트랙이 필수이지만 낭비적입니다; 리소스 소비를 방지하기 위해 즉시 폐기해야 합니다

## 구현 참고사항

### 영향받는 파일
- `frontend/src/components/RecordButton.tsx` --- "시스템 오디오 녹음" 토글 추가; `getDisplayMedia` + `AudioContext` 믹싱 구현
- `frontend/src/hooks/useRecordingSession.ts` --- 믹싱된 스트림 라이프사이클 처리
- `frontend/src/components/record/RecordingConfig.tsx` --- 설정 UI에 시스템 오디오 토글 추가

### 브라우저 지원 매트릭스

| 브라우저 | `getDisplayMedia` 오디오 | 탭별 오디오 | 비고 |
|----------|--------------------------|-------------|------|
| Chrome 94+ | 지원 | 지원 | 최상의 경험 |
| Edge 94+ | 지원 | 지원 | Chromium 기반 |
| Firefox 66+ | 지원 | 미지원 (전체 화면만) | 오디오 캡처에 `about:config` 플래그 필요할 수 있음 |
| Safari 16+ | 부분 지원 | 미지원 | 모든 컨텍스트에서 오디오 캡처가 지원되지 않음 |

### 향후 고려사항
- Ttobak이 주로 Zoom을 사용하는 기업 사용자를 대상으로 하는 경우 Zoom Meeting SDK 통합을 고려합니다
- 시스템 오디오 트랙에서 실시간 노이즈/알림 필터링을 위한 Web Audio API `AudioWorklet` 평가가 필요합니다
- 여러 오디오 소스가 있을 때 화자 분리 힌트를 위한 `MediaStreamTrack` API를 조사합니다

## 참고 자료
- [MDN: getDisplayMedia](https://developer.mozilla.org/en-US/docs/Web/API/MediaDevices/getDisplayMedia)
- [Chrome Tab Audio Capture](https://developer.chrome.com/docs/web-platform/screen-sharing#primitives)
- [Zoom Meeting SDK](https://developers.zoom.us/docs/meeting-sdk/)
- [Google Meet REST API](https://developers.google.com/meet/api)
- 현재 녹음 구현: `frontend/src/components/RecordButton.tsx:127-146`
