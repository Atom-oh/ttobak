# ADR-006: Tab Audio Capture for Browser Meetings + Tauri Mac App for System Audio

<a href="#english"><img src="https://img.shields.io/badge/lang-English-blue.svg" alt="English"></a>
<a href="#korean"><img src="https://img.shields.io/badge/lang-한국어-red.svg" alt="Korean"></a>

---

<a id="english"></a>

# English

## Status
Accepted

## Context

Ttobak currently records audio exclusively through the browser's `getUserMedia` API, which captures only the local microphone input. Solutions Architects frequently join customer meetings via Google Meet (browser-based) and Zoom (desktop app), but cannot record these meetings through Ttobak without manually holding a phone to the speaker or asking for a recording file after the meeting.

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
2. **Sub-project 2 (Tauri Mac App) deferred**: After evaluating the clamshell mode use case, the Tauri Mac App was deprioritized. The primary use case (recording in meetings without opening a laptop) is better served by recording on an iPhone and uploading via the existing file upload flow. The web app's upload mode (`/record?mode=upload`) already supports this.
3. **ADR-001 superseded**: ADR-001 (original system audio proposal) has been marked as superseded by this ADR.

## References
- `docs/superpowers/specs/2026-04-20-tab-audio-capture-design.md` -- Sub-project 1 design spec
- `frontend/src/components/RecordButton.tsx` -- Tab audio capture implementation (`getDisplayMedia`)
- `frontend/src/lib/device.ts` -- `supportsTabAudioCapture()` capability check
- `docs/decisions/ADR-001-system-audio-capture-for-remote-meetings.md` -- Superseded by this ADR
- `docs/decisions/ADR-003-mcp-server-for-external-meeting-access.md` -- OAuth PKCE pattern reusable for Mac App auth
- [MDN getDisplayMedia](https://developer.mozilla.org/en-US/docs/Web/API/MediaDevices/getDisplayMedia)
- [Tauri Framework](https://tauri.app/)
- [macOS ScreenCaptureKit](https://developer.apple.com/documentation/screencapturekit)

---

<a id="korean"></a>

# 한국어

## 상태
승인됨

## 배경

Ttobak은 현재 브라우저의 `getUserMedia` API를 통해서만 오디오를 녹음하며, 이는 로컬 마이크 입력만 캡처합니다. Solutions Architect는 고객 미팅에 Google Meet(브라우저 기반)과 Zoom(데스크탑 앱)으로 자주 참여하지만, 직접 스피커에 폰을 대거나 미팅 후 녹음 파일을 요청하지 않고는 Ttobak으로 이러한 미팅을 녹음할 수 없습니다.

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
2. **서브프로젝트 2 (Tauri Mac App) 보류**: 클램쉘 모드 사용 사례를 평가한 후, Tauri Mac App의 우선순위가 낮아졌습니다. 주요 사용 사례(노트북을 열지 않고 미팅 녹음)는 iPhone으로 녹음하고 기존 파일 업로드 플로우를 통해 업로드하는 것이 더 적합합니다. 웹 앱의 업로드 모드(`/record?mode=upload`)가 이미 이를 지원합니다.
3. **ADR-001 대체됨**: ADR-001(원래 시스템 오디오 제안)이 이 ADR에 의해 대체됨으로 표시되었습니다.

## 참고 자료
- `docs/superpowers/specs/2026-04-20-tab-audio-capture-design.md` -- 서브프로젝트 1 설계 명세
- `frontend/src/components/RecordButton.tsx` -- 탭 오디오 캡처 구현 (`getDisplayMedia`)
- `frontend/src/lib/device.ts` -- `supportsTabAudioCapture()` 기능 확인
- `docs/decisions/ADR-001-system-audio-capture-for-remote-meetings.md` -- 이 ADR에 의해 대체됨
- `docs/decisions/ADR-003-mcp-server-for-external-meeting-access.md` -- Mac App 인증에 재사용 가능한 OAuth PKCE 패턴
- [MDN getDisplayMedia](https://developer.mozilla.org/en-US/docs/Web/API/MediaDevices/getDisplayMedia)
- [Tauri Framework](https://tauri.app/)
- [macOS ScreenCaptureKit](https://developer.apple.com/documentation/screencapturekit)
