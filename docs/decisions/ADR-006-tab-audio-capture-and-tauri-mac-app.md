# ADR-006: Tab Audio Capture for Browser Meetings + Tauri Mac App for System Audio

<a href="#english"><img src="https://img.shields.io/badge/lang-English-blue.svg" alt="English"></a>
<a href="#korean"><img src="https://img.shields.io/badge/lang-한국어-red.svg" alt="Korean"></a>

---

<a id="english"></a>

# English

## Status
Accepted

## Context

TTOBAK currently records audio exclusively through the browser's `getUserMedia` API, which captures only the local microphone input. Solutions Architects frequently join customer meetings via Google Meet (browser-based) and Zoom (desktop app), but cannot record these meetings through TTOBAK without manually holding a phone to the speaker or asking for a recording file after the meeting.

Two distinct capture scenarios exist:
1. **Browser-based meetings (Google Meet)**: The meeting runs in a Chrome tab, and Chrome's `getDisplayMedia` API can capture that tab's audio output, which includes all participants' voices.
2. **Desktop app meetings (Zoom)**: The meeting runs outside the browser. `getDisplayMedia` tab capture cannot reach desktop app audio. macOS further restricts system-level audio capture from browsers, requiring either a virtual audio driver (BlackHole) or a native application with ScreenCaptureKit access.

A single web-only solution cannot address both scenarios on macOS. A hybrid approach is needed.

## Options Considered

### Option 1: Web-Only with getDisplayMedia (Tab + Screen Audio)

Add a "Tab Audio" mode to the existing Record page using `getDisplayMedia({audio: true})`. For desktop apps, offer "Entire Screen" sharing which captures system audio on Windows/Linux but not reliably on macOS.

- **Pros**: No additional app to build or distribute; works immediately for Google Meet on Chrome/Edge; zero backend changes
- **Cons**: Cannot capture Zoom desktop app audio on macOS; screen sharing for audio is confusing UX; Safari/Firefox unsupported

### Option 2: Chrome Extension with tabCapture API

Build a Chrome extension that uses `chrome.tabCapture` for tab audio and `chrome.desktopCapture` for system audio.

- **Pros**: No tab picker popup needed; can auto-detect meeting tabs; works for both tab and system audio
- **Cons**: Requires Chrome Web Store publishing and review; separate codebase to maintain; users must install extension; still cannot capture Zoom app audio on macOS without virtual audio driver

### Option 3: Web + Tauri Mac App Hybrid (Chosen)

Two-phase approach:
- **Sub-project 1 (Web)**: Add `getDisplayMedia` tab audio capture to the existing Record page for Google Meet and other browser-based meetings. Chrome/Edge only. No backend changes.
- **Sub-project 2 (Mac App)**: Build a lightweight Tauri application that wraps the existing web frontend in a WebView and adds native macOS audio capture via ScreenCaptureKit/AVAudioEngine. Supports Zoom, Teams, and all desktop apps. Includes offline recording mode (record locally, sync when online). Same Cognito authentication and S3 upload pipeline.

- **Pros**: Covers all meeting tools; Tauri apps are small (~10MB vs Electron ~200MB); web frontend reused via WebView; backend 100% shared; offline mode for field work; each sub-project is independently useful and deployable
- **Cons**: Two codebases (web modifications + Tauri/Rust app); Tauri requires Rust for native audio bridge; Mac App Store distribution or notarization needed; users must install the app for Zoom capture

### Option 4: Electron App

Wrap the web frontend in Electron with native audio capture via Node.js addons.

- **Pros**: JavaScript/TypeScript throughout; large ecosystem; system audio capture possible
- **Cons**: ~200MB app size; high memory usage; Chromium bundled (redundant since users already have Chrome); slower startup; still needs native module for macOS audio

## Decision

Use Option 3: Web + Tauri Mac App Hybrid, implemented as two independent sub-projects.

**Sub-project 1 (Web Tab Audio)** is implemented first because it requires only frontend changes to existing files, has zero backend impact, and immediately enables Google Meet recording for all Chrome/Edge users.

**Sub-project 2 (Tauri Mac App)** follows as a separate effort. It reuses the web frontend via Tauri's WebView, adds Rust-based system audio capture using macOS ScreenCaptureKit, and includes an offline recording mode that stores audio locally and syncs to S3 when connectivity is restored. Authentication uses the same Cognito flow (OAuth PKCE via the system browser, same as the MCP server pattern from ADR-003).

Tauri was chosen over Electron because:
- App size: ~10MB vs ~200MB
- Memory footprint: uses system WebView instead of bundled Chromium
- Rust provides safe, performant access to macOS native APIs (ScreenCaptureKit)
- The team already has Rust exposure through Tauri's minimal boilerplate requirements

## Consequences

### Positive
- Google Meet recording works immediately after Sub-project 1 (web-only, no install)
- Zoom and all desktop app recording enabled by Sub-project 2 (Mac App)
- Backend pipeline (S3, Transcribe, Summarize) requires zero changes for either sub-project
- Offline recording mode enables field use without network dependency
- Tauri's small footprint (~10MB) makes distribution practical
- Each sub-project delivers standalone value and can be released independently

### Negative
- Two client codebases to maintain (web modifications + Tauri/Rust app)
- Rust learning curve for ScreenCaptureKit integration in the Mac App
- Mac App requires code signing and notarization for distribution
- Tab audio capture is Chrome/Edge only (Safari, Firefox users need the Mac App)
- Offline mode requires local storage management and sync conflict resolution
- macOS permission prompts (Screen Recording permission) may confuse users on first launch of the Mac App

## Post-Implementation Updates

1. **Sub-project 1 (Tab Audio) completed**: `getDisplayMedia` tab audio capture is implemented in `RecordButton.tsx` and `device.ts`. Users can capture Google Meet tab audio on Chrome/Edge. Audio source selector on the Record page lets users choose between Microphone and Tab Audio modes.
2. **Sub-project 2 (Tauri Mac App) reactivated and delivered**: The earlier deferral (in favor of iPhone-record-and-upload) was reversed once the clamshell-mode + Zoom desktop combination came up in real use — `getDisplayMedia` cannot capture Zoom desktop app audio on macOS, and iPhone uploads are too high-friction for that scenario. The Mac app now lives in `mac-app/` as a Tauri 2 + Rust wrapper around the existing SPA, with system-audio capture via ScreenCaptureKit (`mac-app/src-tauri/src/audio.rs`) exposed to the WebView through `start_recording` / `stop_recording` / `read_recording_bytes` / `cleanup_recording` Tauri commands. The frontend gates the "System Audio" source on `isTauri()` (`frontend/src/lib/tauri.ts`).
3. **iPhone upload path retained as a complement, not a replacement**: `/record?mode=upload` is still the right answer when the laptop isn't present at all (phone-only field recording). The Mac app and the upload path cover different scenarios and both stay supported.
4. **Mac app distribution is local-build only**: Tauri 2's default ad-hoc signing skips `codesign --entitlements`, which silently breaks mic / screen-recording prompts. `mac-app/scripts/sign.sh` (wired into `npm run build:signed`) re-signs the bundle with the Entitlements.plist explicitly. There is no CI for the Mac module — builds happen on a developer Mac and the `.app` is shared out-of-band.
5. **Direct disk→S3 streaming upload (`upload_recording`)**: Long meetings (1h+) produce WAV files that exceed the WKWebView JS heap when ferried across IPC as `ArrayBuffer`. Added a `upload_recording(temp_path, upload_url, content_type)` Tauri command (`mac-app/src-tauri/src/lib.rs`) that streams the file directly from disk to the presigned URL via `tokio::io::AsyncRead`. A `ProgressReader` wrapper counts bytes and emits throttled `native-upload-progress` events (~5 Hz) for the UI progress bar. `usePostRecording.handleNativeRecordingReady` drives the post-recording flow off the disk path instead of a Blob. (`readRecordingBytes` was later removed entirely — ADR-024: the IPC bridge fatally crashed the WebContent process on large WAVs, so disk→S3 streaming is now the only upload path.)
6. **Native ScreenCaptureKit audio level meter (`native-audio-level`)**: System mode has no MediaStream → no `AnalyserNode` → the SPA waveform stays flat even when audio is being captured, giving users the impression nothing is recording. `audio.rs` now computes RMS per audio buffer and emits a throttled `native-audio-level` event (~30 Hz, 0–1 normalized). `RecordButton.tsx` subscribes via `onNativeAudioLevel` and drives a scope-style waveform in native mode. Doubles as a diagnostic: zero motion + nonzero callback count surfaces format-mismatch issues. AudioOutput also returns `AppError::Backend` from `stop()` if ScreenCaptureKit delivered zero callbacks or zero samples, instead of silently uploading an empty WAV.
7. **Native screen capture (`capture_screen`)**: Added a Tauri command that shells out to macOS' built-in `screencapture` to grab a single full-screen PNG (cursor included) and adds it to the read-whitelist so the SPA can upload it as a meeting attachment. Useful for capturing Zoom/Meet UI state during a call without alt-tabbing to the system screenshot tool. Surfaced in `RecordButton` as a "Capture Screen" button via `captureScreenImage(meetingId)`.
8. **File logger**: Mac app logs are written to `<tmp>/ttobak-mac/app.log` via `env_logger` configured in `init_file_logger`. Default level INFO, override with `RUST_LOG=debug`. Lets the user share a log file directly when reporting a Mac-side issue instead of running the .app through `RUST_LOG=... open -W`.
9. **`onNativeRecordingReady` callback**: `RecordButton` no longer round-trips through `onBlobReady` for native recordings (which forced a full WAV materialization). Instead it fires `onNativeRecordingReady({ tempPath, byteSize, durationMs, mimeType })` after `stop_recording`, and `usePostRecording.handleNativeRecordingReady` drives the disk→S3 streaming upload from there.
10. **ADR-001 superseded**: ADR-001 (original system audio proposal) has been marked as superseded by this ADR.

## References
- `docs/superpowers/specs/2026-04-20-tab-audio-capture-design.md` -- Sub-project 1 design spec
- `frontend/src/components/RecordButton.tsx` -- Tab audio + system audio dispatch, native level meter scope, screen-capture button
- `frontend/src/lib/device.ts` -- `supportsTabAudioCapture()` capability check
- `frontend/src/lib/tauri.ts` -- `isTauri()` gate + invoke wrappers (`startNativeRecording`, `uploadRecording`, `captureScreenImage`, `onNativeAudioLevel`, `onNativeUploadProgress`)
- `frontend/src/hooks/usePostRecording.ts` -- `handleNativeRecordingReady` drives disk→S3 streaming upload
- `mac-app/` -- Tauri 2 Mac app (Sub-project 2)
- `mac-app/src-tauri/src/lib.rs` -- Tauri command surface (`start_recording`, `stop_recording`, `recording_status`, `read_recording_bytes`, `upload_recording`, `capture_screen`, `cleanup_recording`), `ProgressReader`, `init_file_logger`
- `mac-app/src-tauri/src/audio.rs` -- ScreenCaptureKit recorder + RMS level emit + zero-callback hard fail
- `mac-app/scripts/sign.sh` -- post-build `codesign --entitlements` step
- `docs/decisions/ADR-001-system-audio-capture-for-remote-meetings.md` -- Superseded by this ADR
- `docs/decisions/ADR-003-mcp-server-for-external-meeting-access.md` -- OAuth PKCE pattern reusable for Mac App auth
- `docs/decisions/ADR-014-multi-file-audio-and-linked-meetings.md` -- Multi-file audio (companion ADR, addresses chunked Mac recordings)
- [MDN getDisplayMedia](https://developer.mozilla.org/en-US/docs/Web/API/MediaDevices/getDisplayMedia)
- [Tauri Framework](https://tauri.app/)
- [macOS ScreenCaptureKit](https://developer.apple.com/documentation/screencapturekit)

---

<a id="korean"></a>

# 한국어

## 상태
승인됨

## 배경

TTOBAK은 현재 브라우저의 `getUserMedia` API를 통해서만 오디오를 녹음하며, 이는 로컬 마이크 입력만 캡처합니다. Solutions Architect는 고객 미팅에 Google Meet(브라우저 기반)과 Zoom(데스크탑 앱)으로 자주 참여하지만, 직접 스피커에 폰을 대거나 미팅 후 녹음 파일을 요청하지 않고는 TTOBAK으로 이러한 미팅을 녹음할 수 없습니다.

두 가지 별도의 캡처 시나리오가 존재합니다:
1. **브라우저 기반 미팅 (Google Meet)**: 미팅이 Chrome 탭에서 실행되며, Chrome의 `getDisplayMedia` API로 해당 탭의 오디오 출력(모든 참가자의 음성 포함)을 캡처할 수 있습니다.
2. **데스크탑 앱 미팅 (Zoom)**: 미팅이 브라우저 밖에서 실행됩니다. `getDisplayMedia` 탭 캡처로는 데스크탑 앱 오디오에 접근할 수 없습니다. macOS는 브라우저에서의 시스템 레벨 오디오 캡처를 추가로 제한하여, 가상 오디오 드라이버(BlackHole)나 ScreenCaptureKit 접근 권한이 있는 네이티브 앱이 필요합니다.

웹 전용 솔루션만으로는 macOS에서 두 시나리오를 모두 해결할 수 없습니다. 하이브리드 접근이 필요합니다.

## 검토한 옵션

### 옵션 1: 웹 전용 getDisplayMedia (탭 + 화면 오디오)

기존 Record 페이지에 `getDisplayMedia({audio: true})`를 사용한 "Tab Audio" 모드를 추가합니다. 데스크탑 앱의 경우 Windows/Linux에서 시스템 오디오를 캡처하는 "전체 화면" 공유를 제공하지만 macOS에서는 안정적이지 않습니다.

- **장점**: 추가 앱 빌드/배포 불필요; Google Meet에 즉시 작동; 백엔드 변경 없음
- **단점**: macOS에서 Zoom 데스크탑 앱 오디오 캡처 불가; 오디오를 위한 화면 공유는 혼란스러운 UX; Safari/Firefox 미지원

### 옵션 2: Chrome Extension + tabCapture API

Chrome 확장 프로그램을 구축하여 `chrome.tabCapture`로 탭 오디오, `chrome.desktopCapture`로 시스템 오디오를 캡처합니다.

- **장점**: 탭 선택 팝업 불필요; 미팅 탭 자동 감지 가능; 탭 및 시스템 오디오 모두 지원
- **단점**: Chrome Web Store 게시 및 심사 필요; 별도 코드베이스 유지; 사용자 확장 프로그램 설치 필요; macOS에서 가상 오디오 드라이버 없이 Zoom 앱 오디오 캡처 불가

### 옵션 3: 웹 + Tauri Mac App 하이브리드 (선택됨)

2단계 접근:
- **서브프로젝트 1 (웹)**: 기존 Record 페이지에 Google Meet 등 브라우저 기반 미팅을 위한 `getDisplayMedia` 탭 오디오 캡처를 추가합니다. Chrome/Edge만 지원. 백엔드 변경 없음.
- **서브프로젝트 2 (Mac App)**: 기존 웹 프론트엔드를 WebView로 감싸고 ScreenCaptureKit/AVAudioEngine을 통한 네이티브 macOS 오디오 캡처를 추가하는 경량 Tauri 앱을 구축합니다. Zoom, Teams 등 모든 데스크탑 앱을 지원합니다. 오프라인 녹음 모드(로컬 녹음 후 온라인 시 동기화)를 포함합니다. 동일한 Cognito 인증 및 S3 업로드 파이프라인을 사용합니다.

- **장점**: 모든 미팅 도구 지원; Tauri 앱은 소형(~10MB vs Electron ~200MB); WebView로 웹 프론트엔드 재사용; 백엔드 100% 공유; 현장 업무를 위한 오프라인 모드; 각 서브프로젝트가 독립적으로 유용하고 배포 가능
- **단점**: 두 개의 코드베이스 관리 필요; Tauri는 네이티브 오디오 브릿지에 Rust 필요; Mac App Store 배포 또는 공증 필요; Zoom 캡처를 위해 앱 설치 필요

### 옵션 4: Electron 앱

웹 프론트엔드를 Electron으로 감싸고 Node.js 애드온으로 네이티브 오디오 캡처를 구현합니다.

- **장점**: JavaScript/TypeScript 통일; 대규모 생태계; 시스템 오디오 캡처 가능
- **단점**: ~200MB 앱 크기; 높은 메모리 사용; Chromium 번들링(사용자가 이미 Chrome 보유 시 중복); 느린 시작; macOS 오디오를 위해 여전히 네이티브 모듈 필요

## 결정

옵션 3을 선택합니다: 두 개의 독립적인 서브프로젝트로 구현되는 웹 + Tauri Mac App 하이브리드.

**서브프로젝트 1 (웹 탭 오디오)**을 먼저 구현합니다. 기존 파일에 대한 프론트엔드 변경만 필요하고, 백엔드 영향이 없으며, 모든 Chrome/Edge 사용자에게 즉시 Google Meet 녹음을 활성화하기 때문입니다.

**서브프로젝트 2 (Tauri Mac App)**는 별도 작업으로 진행됩니다. Tauri의 WebView를 통해 웹 프론트엔드를 재사용하고, macOS ScreenCaptureKit을 사용하는 Rust 기반 시스템 오디오 캡처를 추가하며, 로컬에 오디오를 저장하고 연결 복구 시 S3에 동기화하는 오프라인 녹음 모드를 포함합니다. 인증은 동일한 Cognito 플로우(시스템 브라우저를 통한 OAuth PKCE, ADR-003의 MCP 서버 패턴과 동일)를 사용합니다.

Electron 대신 Tauri를 선택한 이유:
- 앱 크기: ~10MB vs ~200MB
- 메모리 사용: 번들된 Chromium 대신 시스템 WebView 사용
- Rust는 macOS 네이티브 API(ScreenCaptureKit)에 대한 안전하고 성능 좋은 접근 제공
- Tauri의 최소한의 보일러플레이트 요구사항을 통해 Rust 노출이 이미 있음

## 영향

### 긍정적
- 서브프로젝트 1 이후 Google Meet 녹음이 즉시 작동 (웹 전용, 설치 불필요)
- 서브프로젝트 2로 Zoom 및 모든 데스크탑 앱 녹음 가능 (Mac App)
- 백엔드 파이프라인(S3, Transcribe, Summarize)은 두 서브프로젝트 모두 변경 불필요
- 오프라인 녹음 모드로 네트워크 의존 없이 현장 사용 가능
- Tauri의 작은 크기(~10MB)로 배포가 실용적
- 각 서브프로젝트가 독립적인 가치를 제공하며 독립 릴리스 가능

### 부정적
- 두 개의 클라이언트 코드베이스 유지 필요 (웹 수정 + Tauri/Rust 앱)
- Mac App의 ScreenCaptureKit 통합에 Rust 학습 곡선
- Mac App 배포를 위한 코드 서명 및 공증 필요
- 탭 오디오 캡처는 Chrome/Edge 전용 (Safari, Firefox 사용자는 Mac App 필요)
- 오프라인 모드에 로컬 저장소 관리 및 동기화 충돌 해결 필요
- macOS 권한 프롬프트(화면 녹화 권한)가 Mac App 첫 실행 시 사용자를 혼란시킬 수 있음

## 구현 후 업데이트

1. **서브프로젝트 1 (탭 오디오) 완료**: `getDisplayMedia` 탭 오디오 캡처가 `RecordButton.tsx`와 `device.ts`에 구현되었습니다. 사용자가 Chrome/Edge에서 Google Meet 탭 오디오를 캡처할 수 있습니다. Record 페이지의 오디오 소스 선택기로 마이크와 탭 오디오 모드를 선택할 수 있습니다.
2. **서브프로젝트 2 (Tauri Mac App) 재활성화 및 구현 완료**: 클램쉘 모드 + Zoom 데스크탑 앱 조합이 실사용에서 다시 떠오르며 이전의 보류 결정을 뒤집었습니다 — `getDisplayMedia`로는 macOS에서 Zoom 데스크탑 앱 오디오를 캡처할 수 없고, 그 시나리오에 대해 iPhone 업로드는 마찰이 너무 큽니다. Mac 앱은 `mac-app/`에 Tauri 2 + Rust 래퍼로 구현되어 있으며, ScreenCaptureKit 기반 시스템 오디오 캡처(`mac-app/src-tauri/src/audio.rs`)를 `start_recording` / `stop_recording` / `read_recording_bytes` / `cleanup_recording` Tauri 커맨드로 WebView에 노출합니다. 프론트엔드는 `isTauri()`(`frontend/src/lib/tauri.ts`)로 "System Audio" 소스를 게이트합니다.
3. **iPhone 업로드 경로는 대체가 아닌 보완으로 유지**: `/record?mode=upload`는 노트북 자체가 부재한 상황(폰만 들고 외부 미팅)에서 여전히 정답입니다. Mac 앱과 업로드 경로는 서로 다른 시나리오를 담당하며 둘 다 계속 지원됩니다.
4. **Mac 앱은 로컬 빌드 전용**: Tauri 2의 기본 ad-hoc 서명이 `codesign --entitlements`를 건너뛰어 mic / 화면 녹화 권한 프롬프트를 조용히 무력화합니다. `mac-app/scripts/sign.sh`(`npm run build:signed`로 연결)가 Entitlements.plist를 명시적으로 임베드합니다. Mac 모듈에 대한 CI는 없으며, 빌드는 개발자 Mac에서 수행하고 `.app`은 별도로 공유합니다.
5. **디스크→S3 직접 스트리밍 업로드 (`upload_recording`)**: 1시간 이상 미팅의 WAV는 `ArrayBuffer`로 IPC를 통해 WKWebView로 옮길 때 JS heap을 초과합니다. `upload_recording(temp_path, upload_url, content_type)` Tauri 커맨드(`mac-app/src-tauri/src/lib.rs`)를 추가해 `tokio::io::AsyncRead` 기반으로 디스크에서 presigned URL로 바로 스트리밍합니다. `ProgressReader` 래퍼가 바이트를 세어 throttled `native-upload-progress` 이벤트(~5 Hz)를 UI 진행률 바로 송신합니다. `usePostRecording.handleNativeRecordingReady`가 Blob 대신 디스크 경로 기반으로 후처리를 수행합니다. (`readRecordingBytes`는 이후 완전히 제거됨 — ADR-024: 대용량 WAV에서 IPC 브리지가 WebContent 프로세스를 크래시시켜, 디스크→S3 스트리밍이 유일한 업로드 경로.)
6. **네이티브 ScreenCaptureKit 오디오 레벨 미터 (`native-audio-level`)**: System 모드에는 MediaStream이 없어 `AnalyserNode`도 없어, 캡처 중에도 SPA 파형이 평평하게 보입니다. `audio.rs`에서 버퍼당 RMS를 계산해 throttled `native-audio-level` 이벤트(~30 Hz, 0–1 정규화)로 송신합니다. `RecordButton.tsx`가 `onNativeAudioLevel`로 구독해 네이티브 모드 전용 스코프형 파형을 구동합니다. 진단 도구도 겸함 — 콜백 카운트는 양수인데 미터가 움직이지 않으면 포맷 미스매치 감지. `AudioOutput`은 SCStream이 콜백 0개 또는 샘플 0개를 전달한 경우 `AppError::Backend`를 반환해 무음 WAV 무단 업로드를 방지합니다.
7. **네이티브 스크린 캡처 (`capture_screen`)**: macOS 내장 `screencapture`를 호출해 전체 화면 PNG(커서 포함)를 캡처하는 Tauri 커맨드 추가. 읽기 화이트리스트에 자동 등록되어 SPA가 미팅 첨부로 업로드 가능. Zoom/Meet UI 상태를 alt-tab 없이 캡처할 때 유용. `RecordButton`에 "Capture Screen" 버튼으로 노출 (`captureScreenImage(meetingId)`).
8. **파일 로거**: Mac 앱 로그를 `<tmp>/ttobak-mac/app.log`에 `env_logger` 기반으로 기록. 기본 INFO, `RUST_LOG=debug`로 오버라이드. 사용자가 Mac 측 이슈 보고 시 `.app`을 `RUST_LOG=... open -W`로 실행하지 않고도 로그 파일을 직접 첨부 가능.
9. **`onNativeRecordingReady` 콜백**: `RecordButton`은 더 이상 네이티브 녹음을 `onBlobReady`로 라우팅하지 않습니다(전체 WAV 메모리 적재 강요). 대신 `stop_recording` 후 `onNativeRecordingReady({ tempPath, byteSize, durationMs, mimeType })`를 발화하고, `usePostRecording.handleNativeRecordingReady`가 디스크→S3 스트리밍 업로드를 수행합니다.
10. **ADR-001 대체됨**: ADR-001(원래 시스템 오디오 제안)이 이 ADR에 의해 대체됨으로 표시되었습니다.

## 참고 자료
- `docs/superpowers/specs/2026-04-20-tab-audio-capture-design.md` -- 서브프로젝트 1 설계 명세
- `frontend/src/components/RecordButton.tsx` -- 탭/시스템 오디오 디스패치, 네이티브 레벨 미터 스코프, 스크린 캡처 버튼
- `frontend/src/lib/tauri.ts` -- `isTauri()` + invoke 래퍼들(`startNativeRecording`, `uploadRecording`, `captureScreenImage`, `onNativeAudioLevel`, `onNativeUploadProgress`)
- `frontend/src/hooks/usePostRecording.ts` -- `handleNativeRecordingReady`가 디스크→S3 스트리밍 업로드 수행
- `mac-app/` -- Tauri 2 Mac 앱 (서브프로젝트 2)
- `mac-app/src-tauri/src/lib.rs` -- Tauri 커맨드 표면 (`start_recording`, `stop_recording`, `recording_status`, `read_recording_bytes`, `upload_recording`, `capture_screen`, `cleanup_recording`), `ProgressReader`, `init_file_logger`
- `mac-app/src-tauri/src/audio.rs` -- ScreenCaptureKit 레코더 + RMS 레벨 송신 + zero-callback hard fail
- `mac-app/scripts/sign.sh` -- 빌드 후 `codesign --entitlements` 스텝
- `frontend/src/lib/device.ts` -- `supportsTabAudioCapture()` 기능 확인
- `docs/decisions/ADR-001-system-audio-capture-for-remote-meetings.md` -- 이 ADR에 의해 대체됨
- `docs/decisions/ADR-003-mcp-server-for-external-meeting-access.md` -- Mac App 인증에 재사용 가능한 OAuth PKCE 패턴
- `docs/decisions/ADR-014-multi-file-audio-and-linked-meetings.md` -- 멀티파일 오디오(동반 ADR, Mac 청크 녹음 대응)
- [MDN getDisplayMedia](https://developer.mozilla.org/en-US/docs/Web/API/MediaDevices/getDisplayMedia)
- [Tauri Framework](https://tauri.app/)
- [macOS ScreenCaptureKit](https://developer.apple.com/documentation/screencapturekit)
