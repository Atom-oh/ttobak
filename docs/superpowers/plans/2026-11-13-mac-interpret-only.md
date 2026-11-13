# Mac Interpret-Only Mode Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A Mac-app-only `/interpret` screen that listens to microphone plus system audio and shows live interpretation, with no meeting, no audio file, and nothing persisted.

**Architecture:** Rust gains a path-less "listening" session that shares the single recorder slot with recordings and reuses the Core Audio backend with no WAV writer. The web screen reuses `SttManager` (Transcribe Streaming) and `LiveTranslation` (`/api/translate`) through a slim hook; reconnect and idle-stop policy live in a pure module.

**Tech Stack:** Rust / Tauri 2 / Core Audio (`mac-app/src-tauri`), Next.js 16 / React 19 (`frontend`), CDK TypeScript (`infra`).

**Spec:** `docs/superpowers/specs/2026-11-13-mac-interpret-only-design.md`

## Global Constraints

- No audio reaches disk in a listening session; `audio::recording_path` is never called for it.
- No meeting, transcript, checkpoint, upload, or leftover entry is created; no interpretation text in Rust logs, the web console, `localStorage`, or IndexedDB.
- Recording and listening never capture at once: both use `RecorderState.recorder`.
- Never hold `RecorderState.recorder` across blocking Core Audio FFI; `StartGuard` releases abandoned starts (ADR-024).
- `sleep.rs` always answers IOKit's will-sleep notice within its deadline.
- Every new command is in `build.rs` `COMMANDS` and granted in `capabilities/default.json`.
- `sttManager.ts` and `transcribeStreamingClient.ts` behavior on `/record` does not change.
- All new UI is gated on `isTauri()`; web and mobile are unaffected.
- User-facing copy is Korean and exactly as written in this plan; repository docs and code comments are English.
- Auto-stop: 10 minutes (`600_000` ms) with no recognized speech. Reconnect: at most 3 attempts, delays `1000`, `2000`, `4000` ms.
- Mac changes have no CI; report native validation limits honestly.

## Review Focus

1. Start then stop pressed in quick succession (stop while capture is still starting): no capture is left running and the screen returns to idle.
2. Page reload or navigation mid-session: capture stops (unmount stop, plus mount-time `stop_listening`), and a mount-time stop on an old app that lacks the command fails silently.
3. Callbacks from a superseded STT session (during reconnect or after stop): they must not add entries, reset the idle timer, or revive the session.
4. Changing the target language or the interim toggle while running: applies to the running session without restarting capture.
5. A recording started while listening (record page, meeting prompt, MCP): listening is fully stopped first, the screen shows why, and the recording starts.

---

### Task 1: Recorder slot supports a path-less listening session

**Files:**
- Modify: `mac-app/src-tauri/src/audio.rs` (top-level `AudioRecorder`, `RecordingSnapshot`, `RecordingHandle`, `StartGuard`, `mod tests`)
- Modify: `mac-app/src-tauri/src/lib.rs`, `upload.rs`, `control.rs`, `meeting_detect.rs`, `sleep.rs` only as needed to compile against the new types

**Interfaces:**
- Produces:
  - `pub trait CaptureBackend { fn capture_lost(&self) -> bool; }`, implemented for `macos::Backend` and for the non-macOS placeholder.
  - `pub struct AudioRecorder<B = PlatformBackend>`; `PlatformBackend` is `macos::Backend` on macOS, a unit struct elsewhere. `RecorderState.recorder` keeps the default type.
  - `RecordingHandle<B> { pub path: Option<PathBuf>, pub started_at: Instant, pub backend: Arc<B> }`; `path == None` means listening.
  - `RecordingSnapshot` gains `pub listening: bool`. For a listening session: `recording == false`, `path == None`, `capture_lost == false`, `listening == true`, `elapsed_ms` live.
  - `install(&mut self, path: PathBuf, backend: B)` (unchanged meaning) and `install_listening(&mut self, backend: B)`.
  - `take_handle(&mut self) -> Result<RecordingHandle<B>, AppError>`: `NotRunning` when idle **or listening**; never removes a listening session.
  - `take_listening(&mut self) -> Option<RecordingHandle<B>>`: `None` when idle or recording; bumps the generation like `take_handle`.
  - `take_any(&mut self) -> Option<RecordingHandle<B>>`: for app exit.
  - `backend(&self) -> Option<Arc<B>>`: `None` for a listening session (microphone switching is recording-only).

- [ ] **Step 1: Write failing tests** in `audio.rs` `mod tests` using a `FakeBackend` (`capture_lost` returns `false`):

```rust
#[test]
fn listening_session_is_not_a_recording() {
    let mut r = AudioRecorder::<FakeBackend>::new();
    r.begin_start().unwrap();
    r.install_listening(FakeBackend);
    let s = r.snapshot();
    assert!(s.listening && !s.recording && s.path.is_none() && !s.capture_lost);
    assert!(!r.is_starting());
    assert!(r.backend().is_none());
}

#[test]
fn stop_recording_cannot_take_a_listening_session() {
    let mut r = AudioRecorder::<FakeBackend>::new();
    r.begin_start().unwrap();
    r.install_listening(FakeBackend);
    assert!(matches!(r.take_handle(), Err(AppError::NotRunning)));
    assert!(r.snapshot().listening);
    assert!(r.take_listening().is_some());
    assert!(!r.snapshot().listening);
}

#[test]
fn stop_listening_cannot_take_a_recording() {
    let mut r = AudioRecorder::<FakeBackend>::new();
    r.begin_start().unwrap();
    r.install(PathBuf::from("/tmp/x.wav"), FakeBackend);
    assert!(r.take_listening().is_none());
    assert!(r.snapshot().recording && !r.snapshot().listening);
    assert!(r.take_handle().is_ok());
}

#[test]
fn listening_and_recording_are_mutually_exclusive() {
    let mut r = AudioRecorder::<FakeBackend>::new();
    r.begin_start().unwrap();
    r.install_listening(FakeBackend);
    assert!(matches!(r.begin_start(), Err(AppError::AlreadyRunning)));
    let before = r.begin_start_generation_for_test();
    r.take_listening();
    assert!(r.begin_start().unwrap().generation > before);
}

#[test]
fn take_any_takes_either_kind() {
    let mut r = AudioRecorder::<FakeBackend>::new();
    assert!(r.take_any().is_none());
    r.begin_start().unwrap();
    r.install_listening(FakeBackend);
    assert!(r.take_any().is_some());
}
```

  (`begin_start_generation_for_test` is a `#[cfg(test)]` accessor for the current generation value; any equivalent that proves the stop bumped it is fine.)

- [ ] **Step 2:** Run `(cd mac-app/src-tauri && cargo test audio::)`. Expected: compile failure on the missing items.
- [ ] **Step 3:** Implement the generic recorder and the methods above; update call sites so the crate compiles with unchanged recording behavior (`handle.path` consumers unwrap the recording path they just took via `take_handle`, which guarantees `Some`).
- [ ] **Step 4:** Run `(cd mac-app/src-tauri && cargo fmt --check && cargo clippy --all-targets -- -D warnings && cargo test)`. Expected: all pass, including the existing `StartGuard` and `a_start_reservation_reports_starting` tests.
- [ ] **Step 5:** Commit: `refactor(mac): let the recorder slot hold a path-less listening session`.

### Task 2: Capture backend runs without a WAV writer

**Files:**
- Modify: `mac-app/src-tauri/src/audio.rs` (`mod macos`: `Backend`, `Backend::start`, `diagnose`)

**Interfaces:**
- Consumes: Task 1 types.
- Produces:
  - `Backend::start(path: Option<&Path>, app, generation, generation_counter, requested_input)`; `None` creates no `WavWriter` (the shared `Arc<Mutex<Option<Wav>>>` starts as `None`) and no file.
  - `Backend::stop_listening(&self) -> Result<(), AppError>`: `stop_capture_blocking()` only, then one `log::info!` of callbacks, dropped buffers, and peaks. It must not call `diagnose` (which fails on `samples_written == 0` and reads the file size).
  - `start_recording` passes `Some(&path)`; recording behavior, warnings, and `diagnose` are unchanged.

- [ ] **Step 1:** Change `Backend`'s stored path to `Option<PathBuf>` and thread `Option<&Path>` through `start`; keep the worker, mixer, level and PCM events, silence watch, and mute gate identical for both kinds.
- [ ] **Step 2:** Add `stop_listening`. Keep the teardown order (AudioDeviceStop, IOProc, aggregate, tap, close queue, join worker).
- [ ] **Step 3:** Run `(cd mac-app/src-tauri && cargo fmt --check && cargo clippy --all-targets -- -D warnings && cargo test)`. Expected: pass. (Core Audio paths are not unit-testable; this proves types and lints only.)
- [ ] **Step 4:** Commit: `feat(mac): capture without a WAV writer for listening sessions`.

### Task 3: `start_listening` / `stop_listening` commands and their interactions

**Files:**
- Modify: `mac-app/src-tauri/src/lib.rs` (commands, `RecorderState`, `start_recording`, exit handler, `generate_handler!`)
- Modify: `mac-app/src-tauri/build.rs`, `mac-app/src-tauri/capabilities/default.json`
- Modify: `mac-app/src-tauri/src/sleep.rs`, `mac-app/src-tauri/src/control.rs`
- Modify: `mac-app/CLAUDE.md` (wire contract table and events paragraph)

**Interfaces:**
- Consumes: Task 1 and Task 2.
- Produces (wire contract):
  - `start_listening(input_device_uid: Option<String>) -> { warnings: Vec<String>, input: Option<SelectedInput> }`. Errors: `AlreadyRunning` while a recording or another start exists.
  - `stop_listening() -> { stopped: bool, stop_timed_out: bool }`. Idempotent: `{ stopped: false, stop_timed_out: false }` when nothing is listening; never stops a recording.
  - Event `native-listening-stopped`, `emit_to("main", …)`, payload `{ "reason": "sleep" | "recording" }`. Not emitted for a `stop_listening` call.
  - Internal `async fn end_listening(state: &RecorderState, app: &AppHandle, reason: Option<&'static str>) -> (bool, bool)` returning `(stopped, stop_timed_out)`: takes the listening handle under the lock, releases the lock, runs `Backend::stop_listening` in `spawn_blocking` raced against `STOP_CAPTURE_TIMEOUT`, drops the listening power assertion, and emits the event when `reason` is `Some`.
  - `RecorderState.listening_power: Mutex<Option<power::PowerAssertion>>` (macOS only), acquired with reason `"TTOBAK live interpretation"` after a successful start, released by `end_listening`.

- [ ] **Step 1:** Add both names to `COMMANDS`, the two `allow-*` grants to `capabilities/default.json`, and both to `generate_handler!`. Run `(cd mac-app/src-tauri && cargo test command_acl_tests)`. Expected: FAIL until the commands exist, then PASS after Step 2.
- [ ] **Step 2:** Implement `start_listening` mirroring `start_recording`'s reservation, `StartGuard`, and off-lock `spawn_blocking` shape, calling `Backend::start(None, …)` and `install_listening`. It does not touch `recorded_paths`, `recording_power`, `finalizing`, or `allowed_dir()`. Implement `stop_listening` via `end_listening(…, None)`.
- [ ] **Step 3:** In `start_recording`, call `end_listening(&state, &app, Some("recording")).await` before `begin_start()`, so a recording start always preempts a listening session and only proceeds after its stop returned or timed out.
- [ ] **Step 4:** In the `RunEvent::Exit` handler use `take_any()`; call `stop_and_finalize` for a recording and `stop_listening` for a listening session.
- [ ] **Step 5:** In `sleep.rs` `SYSTEM_WILL_SLEEP`: when the snapshot is `listening`, spawn a task that awaits `end_listening(…, Some("sleep"))` under the existing `STOP_DEADLINE` and then calls `allow()`; the not-recording, not-listening path still allows immediately.
- [ ] **Step 6:** In `control::app_busy`, treat `snapshot().listening` as busy alongside `recording` and `is_starting()`. Leave `status` reporting `recording: false` for a listening session.
- [ ] **Step 7:** Add both commands and the event to `mac-app/CLAUDE.md`, plus one invariant line: a listening session has no path, writes no file, and is never a recording for upload, leftover, status, or detection.
- [ ] **Step 8:** Run `(cd mac-app/src-tauri && cargo fmt --check && cargo clippy --all-targets -- -D warnings && cargo test)`. Expected: pass.
- [ ] **Step 9:** Commit: `feat(mac): start_listening and stop_listening commands`.

### Task 4: Web bridge and pure session policy

**Files:**
- Modify: `frontend/src/lib/tauri.ts`
- Create: `frontend/src/lib/interpretSession.ts`
- Test: `frontend/scripts/interpret-session.test.mjs` (same `loadModule` pattern as `scripts/live-translation.test.mjs`)

**Interfaces:**
- Produces in `tauri.ts`:
  - `interface NativeListenStart { warnings?: string[]; input?: NativeSelectedInput }`
  - `startNativeListening(inputDeviceUid?: string): Promise<NativeListenStart>` → `invoke('start_listening', { inputDeviceUid: inputDeviceUid || null })`
  - `stopNativeListening(): Promise<{ stopped: boolean; stop_timed_out: boolean }>`
  - `onNativeListeningStopped(handler: (reason: 'sleep' | 'recording') => void): () => void`
  - `export const INTERPRET_VERSION_SKEW_MESSAGE = '설치된 Mac 앱 버전이 오래되어 통역 기능이 없습니다 — 앱을 업데이트해주세요.'`
- Produces in `interpretSession.ts` (no React, no DOM, timers injected):
  - `export const INTERPRET_IDLE_STOP_MS = 600_000`
  - `export const INTERPRET_RECONNECT_DELAYS_MS = [1000, 2000, 4000]`
  - `export const INTERPRET_MESSAGES` with exactly:
    - `notSaved: '통역 내용과 소리는 저장되지 않습니다.'`
    - `idleStopped: '10분 동안 인식된 말이 없어 통역을 멈췄습니다.'`
    - `sleepStopped: 'Mac이 잠자기에 들어가 통역을 멈췄습니다.'`
    - `recordingStopped: '녹음이 시작되어 통역을 멈췄습니다.'`
    - `reconnecting: '다시 연결 중…'`
    - `reconnectFailed: '실시간 자막 연결이 끊어져 통역을 멈췄습니다. 네트워크를 확인한 뒤 다시 시작해 주세요.'`
    - `unavailable: '실시간 자막을 사용할 수 없어 통역을 시작할 수 없습니다 (AWS 인증/연결 필요).'`
    - `macOnly: '통역 전용 모드는 Mac 앱에서만 사용할 수 있습니다.'`
  - `class ReconnectBudget { next(): number | null; reset(): void }`: `next()` returns the next delay, `null` once the three are used; `reset()` restores all three.
  - `class IdleStop { constructor(ms: number, onIdle: () => void, timers?: { set(fn: () => void, ms: number): unknown; clear(id: unknown): void }); start(): void; touch(): void; stop(): void }`: `onIdle` fires once, `ms` after the last `start()`/`touch()`; `touch()` before `start()` or after `stop()` does nothing.

- [ ] **Step 1: Write failing tests:**

```js
test('reconnect budget yields 1s, 2s, 4s then gives up, and resets', () => {
  const b = new m.ReconnectBudget();
  assert.deepEqual([b.next(), b.next(), b.next(), b.next()], [1000, 2000, 4000, null]);
  b.reset();
  assert.equal(b.next(), 1000);
});

test('idle stop fires once after 10 minutes without speech', () => {
  const t = fakeTimers(); let fired = 0;
  const idle = new m.IdleStop(m.INTERPRET_IDLE_STOP_MS, () => { fired++; }, t);
  idle.start(); t.advance(599_999); assert.equal(fired, 0);
  t.advance(1); assert.equal(fired, 1);
  t.advance(600_000); assert.equal(fired, 1);
});

test('speech resets the idle timer', () => {
  const t = fakeTimers(); let fired = 0;
  const idle = new m.IdleStop(600_000, () => { fired++; }, t);
  idle.start(); t.advance(500_000); idle.touch(); t.advance(500_000);
  assert.equal(fired, 0); t.advance(100_000); assert.equal(fired, 1);
});

test('touch after stop or before start never arms the timer', () => {
  const t = fakeTimers(); let fired = 0;
  const idle = new m.IdleStop(600_000, () => { fired++; }, t);
  idle.touch(); t.advance(600_000); assert.equal(fired, 0);
  idle.start(); idle.stop(); idle.touch(); t.advance(600_000); assert.equal(fired, 0);
});

test('idle stop is 10 minutes and messages are fixed', () => {
  assert.equal(m.INTERPRET_IDLE_STOP_MS, 600_000);
  assert.equal(m.INTERPRET_MESSAGES.idleStopped, '10분 동안 인식된 말이 없어 통역을 멈췄습니다.');
});
```

- [ ] **Step 2:** Run `(cd frontend && node --test scripts/interpret-session.test.mjs)`. Expected: FAIL (module missing).
- [ ] **Step 3:** Implement `interpretSession.ts` and the `tauri.ts` additions.
- [ ] **Step 4:** Run the test again, then `(cd frontend && npm run lint)`. Expected: pass.
- [ ] **Step 5:** Commit: `feat(web): native listening bridge and interpret session policy`.

### Task 5: `useInterpretSession` hook and the `/interpret` screen

**Files:**
- Create: `frontend/src/hooks/useInterpretSession.ts`
- Create: `frontend/src/app/interpret/page.tsx`
- Modify: `frontend/src/components/TranslationView.tsx` (text-size variant only)
- Test: extend `frontend/scripts/live-translation.test.mjs` (or its `TranslationView` render test) for the size variant

**Interfaces:**
- Consumes: Task 4 exports; `SttManager`, `TranscribeStreamingConfig` (`@/lib/sttManager`); `getRuntimeConfig`; `onNativePcmChunk`, `onNativeAudioLevel`, `isTauri`, `isCommandNotFound`; `NativeMicSelector`; `TranslationView`; `TRANSLATION_TARGETS`, `BILINGUAL_TARGET`, `MAX_TRANSLATION_ENTRIES`.
- Produces:
  - `useInterpretSession({ targetLang, interimTranslation }) → { phase: 'idle' | 'starting' | 'running' | 'reconnecting' | 'stopping', translations: TranslationEntry[], error: string | null, notice: string | null, warnings: string[], level: number, elapsedMs: number, input: NativeSelectedInput | null, start(inputDeviceUid?: string): Promise<void>, stop(): Promise<void> }`
  - `TranslationView` prop `size?: 'default' | 'large'`; `'large'` renders caption text with `text-xl leading-relaxed` instead of `text-sm`. Default output is byte-identical to today.

Hook behavior (each line is a requirement):
- The hook builds `SttManager` with `translationEnabled: true`, the current `targetLang`/`interimTranslation`, and the Transcribe config from `getRuntimeConfig()` (same fields as `useRecordingSession`); it does not import `useRecordingSession`, `meetingsApi`, upload, or checkpoint code. `onTranscript` only touches the idle timer and reconnect budget; no transcript text is stored.
- `start`: `phase = 'starting'`; `startNativeListening`; subscribe PCM and level; `manager.startNative('transcribe-streaming')`; `phase = 'running'`; `IdleStop.start()`. `isCommandNotFound` → `error = INTERPRET_VERSION_SKEW_MESSAGE`. No Transcribe config, or `transcribe-native-unavailable` before the first transcript of the first session → stop capture, `error = unavailable`.
- `transcribe-native-unavailable` after the session ran: `phase = 'reconnecting'`, `notice = reconnecting`; take `ReconnectBudget.next()`; after the delay stop the old manager and start a fresh one on the same capture; the first transcript of the new session sets `phase = 'running'`, clears the notice, and resets the budget. `null` → stop capture, `error = reconnectFailed`. Existing `translations` are kept across reconnects.
- Every manager is created with a session token; callbacks whose token is not current are ignored.
- `stop` during `'starting'` waits for the start to settle, then stops; `stop` is safe to call in any phase.
- `IdleStop` fires → stop, `notice = idleStopped`. `onNativeListeningStopped('sleep' | 'recording')` → stop the manager (capture is already gone), `notice = sleepStopped | recordingStopped`.
- On mount (Tauri only) call `stopNativeListening()` and swallow every error. On unmount stop the manager and call `stopNativeListening()`.
- `targetLang` / `interimTranslation` changes call `updateTargetLang` / `updateInterimTranslation` on the running manager.
- `translations` is cleared on `start`, kept after `stop`.

Page behavior:
- Auth guard and `AppLayout activePath="/interpret"` as in `app/dictionary/page.tsx`.
- Not `isTauri()` (checked after mount to avoid a hydration mismatch): show `macOnly` and a link to `/record` labelled `녹음 화면에서 실시간 통역 사용하기`; render no controls.
- Header row: title `실시간 통역`, target-language `<select aria-label="통역 언어 선택">` from `TRANSLATION_TARGETS` (default `BILINGUAL_TARGET`), checkbox `초안 번역 (비용↑)` with the record page's `title` text.
- Second row: `NativeMicSelector` (disabled unless `phase === 'idle'`), level meter, elapsed time `HH:MM:SS`, and one button: `통역 시작` when idle, `정지` otherwise (disabled while `'stopping'`).
- Idle and never started: the `notSaved` line. `error` uses `role="alert"`; `notice` and `warnings` use `role="status"`.
- Body: `<TranslationView fill size="large" isActive={phase !== 'idle'} …/>` filling the remaining height.

- [ ] **Step 1:** Add a failing render test: `TranslationView` with `size="large"` contains `text-xl`, and without `size` the markup equals the current default markup for the same props.
- [ ] **Step 2:** Run `(cd frontend && node --test scripts/live-translation.test.mjs)`. Expected: FAIL.
- [ ] **Step 3:** Implement the `size` variant; run the test. Expected: PASS.
- [ ] **Step 4:** Implement the hook and the page per the requirements above.
- [ ] **Step 5:** Run `(cd frontend && npm run lint && npm run test:unit && npm run build)`. Expected: pass, and the build output contains `interpret.html`.
- [ ] **Step 6:** Commit: `feat(web): Mac interpret-only screen`.

### Task 6: Navigation entry and static routing

**Files:**
- Modify: `frontend/src/lib/navigation.ts`, `frontend/src/components/layout/Sidebar.tsx`, `frontend/src/components/layout/MobileNav.tsx`, `frontend/src/components/layout/DesktopHeader.tsx`
- Modify: `infra/lib/frontend-stack.ts` (`knownPages`)
- Test: `frontend/scripts/navigation.test.mjs`, `infra/test/frontend-stack.test.ts`

**Interfaces:**
- Produces:
  - `export const INTERPRET_ITEM: NavItem = { href: '/interpret', icon: 'translate', label: '통역' }`
  - `export function navGroupsFor(isMacApp: boolean): NavGroup[]`: `NAV_GROUPS` unchanged when `false`; when `true`, a copy with `INTERPRET_ITEM` inserted immediately after `/meetings` in the `업무` group. `NAV_GROUPS` itself is not mutated.
  - Header title map entry `'/interpret': 'Interpretation'`.

- [ ] **Step 1: Write failing tests:**

```js
test('interpret entry appears only in the Mac app, after Meetings', () => {
  const web = nav.navGroupsFor(false).flatMap((g) => g.items.map((i) => i.href));
  assert.ok(!web.includes('/interpret'));
  const work = nav.navGroupsFor(true).find((g) => g.name === '업무').items.map((i) => i.href);
  assert.equal(work[work.indexOf('/meetings') + 1], '/interpret');
  assert.ok(!nav.NAV_GROUPS.flatMap((g) => g.items).some((i) => i.href === '/interpret'));
});
```

  In `infra/test/frontend-stack.test.ts`, add `['/interpret', '/interpret.html']` to the routing table in `SPA routing preserves segmented Next payload filenames…`.

- [ ] **Step 2:** Run `(cd frontend && node --test scripts/navigation.test.mjs)` and `(cd infra && npm test -- frontend-stack)`. Expected: both FAIL.
- [ ] **Step 3:** Implement `navGroupsFor`; make `Sidebar` and `MobileNav` render `navGroupsFor(isMacApp)` where `isMacApp` is state set from `isTauri()` in an effect (prerendered HTML stays the web variant). Add `'/interpret'` to `knownPages` and the header title.
- [ ] **Step 4:** Run `(cd frontend && npm run lint && npm run test:unit && npm run build)` and `(cd infra && npx cdk synth && npm test)`. Expected: pass.
- [ ] **Step 5:** Commit: `feat(web): Mac-only interpretation menu entry and route`.

### Task 7: Documentation and final verification

**Files:**
- Modify: `docs/decisions/ADR-024-mac-native-capture-and-upload.md`, `docs/DESIGN-SPEC.md`, `docs/INFRA-SPEC.md`, `docs/architecture.md`, `mac-app/README.md`

- [ ] **Step 1:** ADR-024: add a dated section recording that listening shares the recorder slot, writes no audio, is preempted by a recording start, stops at system sleep, counts as busy for the updater, and that the screen auto-stops after 10 minutes without speech (cost and battery reason).
- [ ] **Step 2:** `DESIGN-SPEC.md`: the `/interpret` screen and Mac-only menu entry. `INFRA-SPEC.md`: `knownPages`. `architecture.md`: the listen-only path (`start_listening` → `native-pcm-chunk` → Transcribe Streaming → `/api/translate`). `mac-app/README.md`: the feature and its manual test list.
- [ ] **Step 3:** Run `python3 scripts/docs/check_docs.py` and `python3 -m unittest discover -s scripts/docs -p 'test_*.py' -v`. Expected: pass.
- [ ] **Step 4:** Run the full relevant suite once more: the Rust trio in `mac-app/src-tauri`, `frontend` lint/test/build, `infra` synth/test. Expected: pass.
- [ ] **Step 5:** Commit: `docs: record the Mac interpret-only mode`.
- [ ] **Step 6:** Report the manual checks that still need a real Mac build (`npm run build:signed`), unverified by this plan: both sources heard; no file under the recordings directory during or after a session; leftover list empty after a forced quit; recording start from the record page, a meeting prompt, and MCP while listening; sleep while listening; idle auto-stop; reconnect after a network drop; old-app version-skew message.
