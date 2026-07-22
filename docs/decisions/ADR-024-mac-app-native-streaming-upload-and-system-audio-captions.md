# ADR-024: Native Streaming Upload and Live Captions for Mac App System Audio Mode

<a href="#english"><img src="https://img.shields.io/badge/lang-English-blue.svg" alt="English"></a>
<a href="#korean"><img src="https://img.shields.io/badge/lang-한국어-red.svg" alt="Korean"></a>

---

<a id="english"></a>

# English

## Status
Accepted

## Context
A real ~35-minute meeting recorded in the TTOBAK Mac app's System Audio mode (Tauri 2 + Rust, ScreenCaptureKit) froze the app immediately after "end meeting." Investigation (live process forensics — a hang sample, the macOS crash reporter, the app's own log, and the TCC database — plus code tracing) established the actual mechanism, which turned out to be unrelated to any hang in Rust:

1. **The freeze.** `read_recording_bytes` (`mac-app/src-tauri/src/lib.rs`) was a synchronous Tauri command that `std::fs::read` the entire recorded WAV (400,949,804 bytes) into memory and returned it as one IPC `Response`. Because this app's window loads the remote production SPA (`https://ttobak.atomai.click`) rather than a local `tauri://` origin, Tauri 2 delivers that binary response to the WebView via `evaluateJavaScript` — i.e. as one giant JS array literal. JavaScriptCore hit a fatal `RELEASE_ASSERT` while bytecode-compiling it (`EXC_BREAKPOINT` in `JSC::ArrayNode::emitBytecode`; the crashing thread's register held exactly the WAV's byte count). The WebContent process died ~27 seconds after stop; WebKit silently respawned a blank one. The native Rust process itself was never hung — a hang sample taken while the app looked frozen showed its main thread idle in the normal event loop. `cleanup_recording` never ran, so the WAV survived on disk by luck.
2. **The flat waveform.** Capture emitted `native-audio-level` RMS events correctly, but `mac-app/src-tauri/capabilities/default.json` granted only `core:default` with no `"remote"` field. Tauri 2.11's ACL only matches a capability's default (local) context against `tauri://localhost`; this app's actual origin is `Origin::Remote`, so `plugin:event|listen` was silently rejected there — while the app's own custom commands bypassed the ACL entirely (no `__app-acl__` manifest), which is why start/stop/read worked while events didn't. The frontend's `listen()` rejection handler swallowed the error silently.
3. **No live captions.** By design (Rust doesn't capture the user's own microphone in System Audio mode — see `mac-app/CLAUDE.md`'s "System audio only for MVP"), but poorly signposted: the during-recording banner was gated on `session.isRecording`, which only ever becomes true from a browser `MediaStream` — never set in this mode — so the recording screen looked blank rather than showing the pre-recording setup screen's existing "no live captions" notice.
4. Two additional data-loss bugs surfaced during the fix: the resume-upload path aborted every PUT at a fixed 30 seconds regardless of file size (impossible for a large recording to survive), and the temp WAV was deleted before the upload even started; and a `stop_capture` error on the Rust side used to skip `finalize()` entirely, leaving an unpatched (zero-size) WAV header even after substantial audio had been captured.

## Decision
1. **Bulk audio bytes never cross the Tauri IPC bridge to this WebView again.** `read_recording_bytes` is deleted. A new async command, `upload_recording(path, upload_url, content_type)` (`mac-app/src-tauri/src/upload.rs`), streams the WAV straight from disk to a presigned S3 URL via `reqwest`, with an explicitly set `Content-Length` (the same pattern `reqwest`'s own `multipart()` helper uses for a streamed body with a known size — verified against a local mock TCP server in this module's test suite, not just reasoned about: without it, the body would go out with chunked Transfer-Encoding, which S3 presigned PUT rejects with a 501) and a progress/stall-watchdog event instead of a fixed total-request timeout. This amends ADR-006's "upload lives in the SPA" boundary in one respect only: byte *transport* moves to Rust; presign, auth, and completion-notification are unchanged and still owned by the SPA.
2. **`capabilities/default.json` gains an explicit `remote` grant** for `https://ttobak.atomai.click` — the actual fix for the flat waveform, and the enabling change for live captions (below), since both ride on Tauri events reaching this remote-origin WebView.
3. **`stop_recording` is hardened**: the handle is taken out of `RecorderState.recorder` and the lock dropped *before* the blocking, no-timeout-of-its-own `stop_capture()` FFI call (previously held across it); the call now races `spawn_blocking` against a 10-second timeout, returning `stop_timed_out` instead of hanging the command forever. The WAV writer is now finalized in every exit path — success, a `stop_capture` error, or a timeout — via an idempotent `finalize_writer`, closing the second data-loss bug above. The audio callback also now periodically flushes the writer (~5s cadence) so a force-kill loses at most that much instead of an unpatched header.
4. **Frontend upload/retry semantics are rewritten for data safety**: the pending-upload payload (a blob, for browser modes, or a file path, for native mode) is a tagged union cleared only after the backend's upload-complete notification succeeds; a `putDone` marker means a notify-only failure retries just that call instead of re-uploading the whole file (which would also re-fire the S3-upload EventBridge rule and duplicate the transcription run); "Try Again" is now a real retry from the retained payload, distinct from "Home"/dismiss (which only clears state); the fixed-30-second upload timeout is replaced by a progress-driven stall watchdog (abort only after 60 seconds of no progress).
5. **The during-recording UI is fixed** to reflect System Audio mode honestly: an `isNativeRecording` flag (set as soon as native capture starts, independent of the STT session) now drives the banner, title, and navigation lock that previously depended on `session.isRecording` alone; the banner initially stated that live captions were unavailable in this mode; Decision 6 then added best-effort captions, and the shipped banner wording reflects that (captions best-effort, not guaranteed).
6. **Live captions are added for System Audio mode** (closing the gap in decision 5, not just describing it): the same audio callback that writes the WAV also downsamples to 16kHz mono and emits it as `native-pcm-chunk` events (base64-encoded, ~64ms chunks — small enough that the `evaluateJavaScript` bridge this ADR just fixed for symptom 1 stays safe for this new traffic too). `TranscribeStreamingSession` (frontend) gains `startNative()`/`pushChunk()`, reusing its existing Cognito-credentialed WebSocket connection to Amazon Transcribe Streaming instead of building a second STT pipeline; there is deliberately no Web Speech fallback in this mode (it requires a microphone that doesn't exist here), so a connection failure surfaces a clear message instead of silently producing no captions.

## Consequences

### Positive
- The crash is structurally un-retriggerable: the command that caused it no longer exists, and no replacement path re-introduces a large binary transfer through IPC.
- The waveform and (new) live captions both work in System Audio mode; the capabilities fix generalizes to any future Tauri event this app adds for a remote-origin window.
- A recording is no longer at risk of being silently lost: the WAV is deleted only after a confirmed upload, uploads survive being large and slow, and a retry after a successful PUT skips straight to the completion notify (the PUT itself is re-sent in full if it hadn't succeeded — no byte-range resume).
- A version-skew window (new SPA deployed before the mac app is rebuilt) degrades safely: the old binary's removed command simply isn't found, surfaced as a clear "update the app" message with the file path preserved, recoverable via `/record?mode=upload`.

### Negative
- `capabilities/default.json`'s `remote` grant hands `https://ttobak.atomai.click` `core:default` (events/window IPC) inside the app; scoped to exactly one origin (never a wildcard), and that origin already fully controls the UI, so this is a modest widening of trust, not a new one. A future pass could split this into a least-privilege events-only capability.
- The Rust-side upload has a single-attempt design (no internal retry/backoff); retry orchestration lives in the SPA instead, which re-presigns on every attempt — simpler, but means a transient failure always costs one fresh presigned URL round-trip.
- Live captions in System Audio mode inherit a small phase discontinuity at each ScreenCaptureKit callback boundary (the per-callback-independent linear interpolation mirrors what the existing browser-mode AudioWorklet already accepts) — negligible for speech recognition, not appropriate if this path were ever reused for archival-quality audio (it isn't; the WAV writer is untouched and remains the source of truth for the recording itself).
- Deferred, not fixed here: a start-time zero-callback watchdog (today's failure is still only detected at stop), moving the recording directory off `$TMPDIR` (OS-purged after ~3 days idle), and a UI for recovering orphaned WAVs left by a killed app session.

## References
- `mac-app/CLAUDE.md` — Tauri commands table, signing, pitfalls.
- `mac-app/src-tauri/src/{lib.rs,audio.rs,upload.rs}` — implementation; `upload.rs`'s test module verifies the Content-Length/non-chunked assumption against a local mock server.
- `mac-app/src-tauri/capabilities/default.json` — the `remote` grant.
- `frontend/src/lib/{tauri.ts,upload.ts,transcribeStreamingClient.ts,sttManager.ts}`, `frontend/src/hooks/{usePostRecording.ts,useRecordingSession.ts}`, `frontend/src/components/RecordButton.tsx`, `frontend/src/app/record/page.tsx` — implementation.
- [ADR-006](ADR-006-tab-audio-capture-and-tauri-mac-app.md) — original "upload lives in the SPA" boundary, amended here in the byte-transport respect only.

---

<a id="korean"></a>

# 한국어

## 상태
승인됨

## 배경
TTOBAK 맥 앱(Tauri 2 + Rust, ScreenCaptureKit)의 시스템 오디오 모드로 실제 약 35분간 녹음한 회의가 "미팅 종료" 직후 앱을 멈추게 만들었습니다. 조사(살아있는 프로세스에 대한 실측 증거 — hang sample, macOS 크래시 리포터, 앱 자체 로그, TCC 데이터베이스 — 및 코드 추적)로 실제 메커니즘을 확인했는데, Rust 쪽의 행(hang)과는 무관한 것으로 밝혀졌습니다.

1. **멈춤 현상.** `read_recording_bytes`(`mac-app/src-tauri/src/lib.rs`)는 동기(sync) Tauri 커맨드로, 녹음된 WAV 전체(400,949,804바이트)를 `std::fs::read`로 메모리에 올려 하나의 IPC `Response`로 반환했습니다. 이 앱의 창이 로컬 `tauri://` 오리진이 아니라 원격 프로덕션 SPA(`https://ttobak.atomai.click`)를 로드하기 때문에, Tauri 2는 이 바이너리 응답을 웹뷰에 `evaluateJavaScript`로 전달합니다 — 즉 거대한 JS 배열 리터럴로 전달됩니다. JavaScriptCore가 이를 바이트코드로 컴파일하던 중 치명적인 `RELEASE_ASSERT`에 걸렸고(`EXC_BREAKPOINT`, `JSC::ArrayNode::emitBytecode`; 크래시한 스레드의 레지스터에는 정확히 WAV의 바이트 수가 담겨 있었음), WebContent 프로세스가 stop 약 27초 후 죽었습니다. WebKit은 조용히 빈 프로세스를 다시 띄웠습니다. 네이티브 Rust 프로세스 자체는 전혀 멈추지 않았습니다 — 앱이 멈춘 것처럼 보이던 시점에 뜬 hang sample은 메인 스레드가 정상적인 이벤트 루프에서 idle 상태였음을 보여줬습니다. `cleanup_recording`이 실행되지 않아 WAV 파일은 운 좋게 디스크에 남아있었습니다.
2. **파형이 움직이지 않는 문제.** 캡처는 `native-audio-level` RMS 이벤트를 정상적으로 발생시켰지만, `mac-app/src-tauri/capabilities/default.json`은 `"remote"` 필드 없이 `core:default`만 부여하고 있었습니다. Tauri 2.11의 ACL은 capability의 기본(로컬) 컨텍스트를 `tauri://localhost`에만 매칭하는데, 이 앱의 실제 오리진은 `Origin::Remote`이므로 `plugin:event|listen`이 조용히 거부되었습니다 — 반면 앱 자체 커스텀 커맨드는 ACL을 완전히 우회하므로(`__app-acl__` 매니페스트 없음) start/stop/read는 동작하면서 이벤트만 동작하지 않았습니다. 프런트엔드의 `listen()` 거부 처리 코드는 이 에러를 조용히 삼켰습니다.
3. **실시간 자막 없음.** 의도된 동작이었으나(Rust는 시스템 오디오 모드에서 사용자 본인의 마이크를 캡처하지 않음 — `mac-app/CLAUDE.md`의 "System audio only for MVP" 참조) 안내가 부실했습니다: 녹음 중 배너가 `session.isRecording`에 걸려있었는데, 이는 브라우저 `MediaStream`이 있을 때만 true가 되며 이 모드에서는 결코 설정되지 않아, 화면이 녹음 전 설정 화면에 이미 있던 "실시간 자막 미지원" 안내 대신 텅 비어 보였습니다.
4. 수정 과정에서 두 가지 추가 데이터 유실 버그도 드러났습니다: 재개-업로드 경로가 파일 크기와 무관하게 모든 PUT을 고정 30초에 중단시켜(큰 녹음이 살아남을 수 없음), 업로드가 시작되기도 전에 임시 WAV를 삭제했고; Rust 쪽에서 `stop_capture` 에러가 나면 `finalize()`를 완전히 건너뛰어 상당한 오디오가 캡처됐음에도 WAV 헤더가 패치되지 않은(크기 0) 상태로 남았습니다.

## 결정
1. **대용량 오디오 바이트가 이 웹뷰로 가는 Tauri IPC 브리지를 다시는 건너지 않도록 함.** `read_recording_bytes`를 삭제. 새 비동기 커맨드 `upload_recording(path, upload_url, content_type)`(`mac-app/src-tauri/src/upload.rs`)가 WAV를 디스크에서 곧바로 presigned S3 URL로 `reqwest`를 통해 스트리밍하며, `Content-Length`를 명시적으로 설정(크기를 아는 스트리밍 바디에 대해 `reqwest`의 `multipart()` 헬퍼가 내부적으로 쓰는 것과 같은 패턴 — 이 모듈의 테스트 스위트에서 로컬 mock TCP 서버로 실제 검증했으며, 추론에만 의존하지 않음: 이게 없으면 바디가 청크 Transfer-Encoding으로 나가고 S3 presigned PUT은 이를 501로 거부함)하고, 고정 전체-요청 타임아웃 대신 진행률/정체(stall) 감시 이벤트를 둠. 이는 ADR-006의 "업로드는 SPA에" 경계를 단 한 가지 측면에서만 수정합니다: 바이트 *전송*만 Rust로 이동하며, presign·인증·완료 알림은 변경 없이 여전히 SPA가 담당합니다.
2. **`capabilities/default.json`에 `https://ttobak.atomai.click`에 대한 명시적 `remote` 승인을 추가** — 파형 문제의 실제 해결책이며, 아래 실시간 자막 기능을 가능케 하는 변경이기도 합니다. 둘 다 이 원격 오리진 웹뷰에 Tauri 이벤트가 도달하는 데 의존하기 때문입니다.
3. **`stop_recording` 강화**: 핸들을 `RecorderState.recorder`에서 꺼내고, 블로킹이며 자체 타임아웃이 없는 `stop_capture()` FFI 호출 *전에* 락을 해제(기존엔 그 호출 내내 락을 쥐고 있었음). 이제 `spawn_blocking`을 10초 타임아웃과 경쟁시켜, 커맨드를 영원히 멈추게 하는 대신 `stop_timed_out`을 반환. WAV writer는 이제 모든 종료 경로(성공, `stop_capture` 에러, 타임아웃)에서 멱등(idempotent) `finalize_writer`를 통해 finalize되어, 위의 두 번째 데이터 유실 버그를 닫습니다. 오디오 콜백도 이제 주기적으로(~5초) writer를 flush해, 강제 종료 시 최대 그 정도만 손실되도록 함.
4. **프런트엔드 업로드/재시도 로직을 데이터 안전성을 위해 재작성**: 대기 중인 업로드 페이로드(브라우저 모드는 blob, 네이티브 모드는 파일 경로)는 백엔드의 업로드 완료 알림이 성공한 뒤에만 지워지는 태그드 유니온이며; `putDone` 마커가 있으면 알림만 실패했을 때 전체 파일을 재업로드하지 않고 그 호출만 재시도(재업로드는 S3 업로드 EventBridge 규칙을 다시 발동시켜 전사를 중복 실행시킴); "다시 시도"는 이제 보존된 페이로드로부터의 실제 재시도이며, "홈"/닫기(상태만 지움)와 구분됨; 고정 30초 업로드 타임아웃은 진행률 기반 정체 감시(60초간 진행 없을 때만 중단)로 대체.
5. **녹음 중 UI가 시스템 오디오 모드를 정직하게 반영하도록 수정**: `isNativeRecording` 플래그(네이티브 캡처가 시작되는 즉시, STT 세션과 무관하게 설정됨)가 이전에 `session.isRecording`에만 의존했던 배너·제목·네비게이션 잠금을 구동하며, 배너는 이 모드에서 실시간 자막이 지원되지 않음을 명확히 안내.
6. **시스템 오디오 모드에 실시간 자막 추가** (5번 결정의 공백을 설명만 하는 게 아니라 실제로 닫음): WAV를 쓰는 동일한 오디오 콜백이 16kHz 모노로 다운샘플해 `native-pcm-chunk` 이벤트(base64 인코딩, ~64ms 청크 — 이 ADR이 증상 1을 위해 방금 고친 `evaluateJavaScript` 브리지가 이 새 트래픽에도 안전할 만큼 작음)로도 내보냅니다. 프런트엔드의 `TranscribeStreamingSession`은 `startNative()`/`pushChunk()`를 얻어, 별도의 STT 파이프라인을 새로 만드는 대신 기존의 Cognito 인증 기반 Amazon Transcribe Streaming 웹소켓 연결을 그대로 재사용합니다. 이 모드에서는 Web Speech 폴백을 의도적으로 두지 않았습니다(마이크가 필요한데 이 모드에는 마이크가 없음) — 연결 실패 시 조용히 자막이 안 나오는 대신 명확한 메시지를 표시합니다.

## 결과

### 긍정적
- 크래시는 구조적으로 재발 불가능함: 원인이 된 커맨드 자체가 없어졌고, 대체 경로 어디에도 IPC를 통한 대용량 바이너리 전송이 다시 도입되지 않음.
- 파형과 (신규) 실시간 자막 모두 시스템 오디오 모드에서 동작함. capabilities 수정은 이 앱이 원격 오리진 창에 추가할 미래의 어떤 Tauri 이벤트에도 일반적으로 적용됨.
- 녹음이 조용히 사라질 위험이 없어짐: WAV는 업로드가 확인된 뒤에만 삭제되고, 업로드는 크고 느려도 견디며, PUT 성공 후의 재시도는 완료 notify만 다시 보냄(PUT 자체가 실패했었다면 전체를 다시 전송 — byte-range resume 아님).
- 버전 스큐 구간(맥 앱 재빌드 전에 새 SPA가 먼저 배포됨)이 안전하게 저하됨: 이전 바이너리에서는 제거된 커맨드를 찾지 못할 뿐이며, 파일 경로가 보존된 명확한 "앱 업데이트 필요" 메시지로 표면화되고, `/record?mode=upload`로 복구 가능.

### 부정적
- `capabilities/default.json`의 `remote` 승인은 `https://ttobak.atomai.click`에 앱 내부의 `core:default`(이벤트/창 IPC)를 부여함. 정확히 하나의 오리진에만 한정되며(와일드카드 아님) 그 오리진이 이미 UI를 완전히 제어하므로 신뢰 범위가 완만하게 넓어지는 것일 뿐 새로운 신뢰는 아님. 향후 최소 권한의 이벤트 전용 capability로 분리할 수 있음.
- Rust 쪽 업로드는 단일 시도 설계(내부 재시도/백오프 없음)이며, 재시도 오케스트레이션은 SPA에 있음. 매 시도마다 재-presign — 더 단순하지만, 일시적 실패마다 새 presigned URL 왕복이 한 번 더 드는 대가가 있음.
- 시스템 오디오 모드의 실시간 자막은 ScreenCaptureKit 콜백 경계마다 작은 위상 불연속을 물려받음(콜백별 독립적인 선형 보간은 기존 브라우저 모드 AudioWorklet이 이미 받아들이던 것과 동일) — 음성 인식에는 무시할 만하나, 이 경로를 아카이브 품질 오디오에 재사용한다면 부적절함(그렇게 하지 않음; WAV writer는 손대지 않았고 녹음 본체의 근거 자료로 그대로 남음).
- 이번엔 고치지 않고 미룸: 시작 시점의 zero-callback 워치독(오늘은 stop 시점에만 감지됨), 녹음 디렉터리를 `$TMPDIR`(idle ~3일 후 OS가 정리함) 밖으로 옮기는 것, 강제 종료된 앱 세션이 남긴 orphan WAV를 복구하는 UI.

## 참고
- `mac-app/CLAUDE.md` — Tauri 커맨드 표, 서명, pitfalls.
- `mac-app/src-tauri/src/{lib.rs,audio.rs,upload.rs}` — 구현. `upload.rs`의 테스트 모듈이 Content-Length/non-chunked 가정을 로컬 mock 서버로 검증.
- `mac-app/src-tauri/capabilities/default.json` — `remote` 승인.
- `frontend/src/lib/{tauri.ts,upload.ts,transcribeStreamingClient.ts,sttManager.ts}`, `frontend/src/hooks/{usePostRecording.ts,useRecordingSession.ts}`, `frontend/src/components/RecordButton.tsx`, `frontend/src/app/record/page.tsx` — 구현.
- [ADR-006](ADR-006-tab-audio-capture-and-tauri-mac-app.md) — 원래의 "업로드는 SPA에" 경계, 바이트 전송 측면에서만 이번에 수정됨.
